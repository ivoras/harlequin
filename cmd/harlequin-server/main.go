// Command harlequin-server runs the Harlequin REST/SSE API server.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/ivoras/harlequin"
	"github.com/ivoras/harlequin/internal/server/agent"
	"github.com/ivoras/harlequin/internal/server/api"
	"github.com/ivoras/harlequin/internal/server/audit"
	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/config"
	"github.com/ivoras/harlequin/internal/server/cron"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/email"
	"github.com/ivoras/harlequin/internal/server/embed"
	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/mcp"
	"github.com/ivoras/harlequin/internal/server/mdtmpl"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/server/notify"
	"github.com/ivoras/harlequin/internal/server/notifyx"
	"github.com/ivoras/harlequin/internal/server/pdfextract"
	"github.com/ivoras/harlequin/internal/server/presence"
	"github.com/ivoras/harlequin/internal/server/project"
	"github.com/ivoras/harlequin/internal/server/secrets"
	"github.com/ivoras/harlequin/internal/server/session"
	"github.com/ivoras/harlequin/internal/server/sessionhub"
	"github.com/ivoras/harlequin/internal/server/sessionlog"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/server/telegram"
	"github.com/ivoras/harlequin/internal/server/usage"
	"github.com/ivoras/harlequin/internal/server/userconfig"
	"github.com/ivoras/harlequin/internal/server/webfetch"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func main() {
	if dispatchCLI(os.Args[1:]) {
		return
	}

	configPath := flag.String("config", "server.yaml", "path to server config YAML")
	flag.Parse()
	// Anything left over is invalid (e.g. a subcommand placed after the flags);
	// fail loudly instead of silently starting the server.
	rejectStrayArgs(flag.Args())

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	store, err := storage.New(cfg.DataDir, cfg.DBPath, cfg.Embeddings.Dim)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer store.Close()

	authStore := auth.NewStore(store.System)

	// Deploy baked-in skills and hats to the data dir.
	if err := skills.Deploy(harlequin.BakedFS(), "skills", cfg.SkillsDir(), cfg.DataDir); err != nil {
		log.Fatalf("deploy skills: %v", err)
	}
	if err := skills.Deploy(harlequin.BakedFS(), "hats", cfg.HatsDir(), cfg.DataDir); err != nil {
		log.Fatalf("deploy hats: %v", err)
	}

	// Build providers and the routing provider.
	usageStore := usage.NewStore(cfg.Prices)
	providers := map[string]*llm.OpenAICompatible{}
	for _, p := range cfg.Providers {
		prov := llm.NewOpenAICompatible(p.Name, p.BaseURL, p.APIKey, p.Model)
		prov.SetReturnProgress(p.ReturnProgress)
		providers[p.Name] = prov
	}
	// Usage is recorded in the agent loop where the user/session are known,
	// so the routing provider's recorder is left nil to avoid double-counting.
	ctxWindows := make(map[string]int, len(cfg.ContextWindows))
	for model, n := range cfg.ContextWindows {
		ctxWindows[model] = n
	}
	for _, p := range cfg.Providers {
		if p.ContextWindow > 0 {
			ctxWindows[p.Model] = p.ContextWindow
		}
		if discovered, err := llm.DiscoverContextWindows(context.Background(), p.BaseURL, p.APIKey); err == nil {
			for id, n := range discovered {
				ctxWindows[id] = n
			}
			llm.ApplyConfigModelAlias(ctxWindows, p.Model)
			if p.Model != "" && ctxWindows[p.Model] > 0 {
				log.Printf("context window: provider %q config model %q -> %d tokens (from /v1/models)", p.Name, p.Model, ctxWindows[p.Model])
			}
			for id, n := range discovered {
				log.Printf("context window: provider %q loaded model %q -> %d tokens", p.Name, id, n)
			}
		} else if p.ContextWindow == 0 {
			log.Printf("context window: provider %q: /v1/models discovery failed (%v); set context_window in config", p.Name, err)
		}
	}
	router := llm.NewRoutingProvider(providers, cfg.Routing.DefaultProvider, cfg.Routing.FallbackOrder, cfg.Routing.ModelRules, ctxWindows, nil)

	embedder := embed.New(cfg.Embeddings.BaseURL, cfg.Embeddings.APIKey, cfg.Embeddings.Model, cfg.Embeddings.Dim)

	skillRunner := jsrun.New(jsrun.Options{
		Timeout:        cfg.Agent.SkillRenderTimeout.D(),
		OutputCap:      cfg.Agent.JSOutputCap,
		FetchAllowlist: cfg.Agent.JSFetchAllowlist,
	})

	memStore := memory.NewStore(store.Shared, embedder)
	memStore.SetSlotSearchWeight(cfg.Memory.SlotSearchWeightValue())
	memStore.SetSearchMaxDistance(cfg.Memory.SearchMaxDistanceValue())
	if cfg.Memory.ConflictCheckEnabled() {
		memStore.SetConflictJudge(router, cfg.Memory.ConflictCandidates)
	}
	docStore := documents.NewStore(store.Shared, embedder)
	sessStore := session.NewStore()
	auditStore := audit.NewStore()
	sessionLog := sessionlog.New(cfg.SessionsDir(), cfg.Sessions.EnabledValue(), cfg.Sessions.LogTokens, cfg.Sessions.Redact)

	// The single JS-template context provider, used for every .md the server
	// renders (skills, the system prompt, and hat prompts).
	makeCtx := mdtmpl.New(memStore, docStore, store)
	skillMgr := skills.NewManager(store.Shared, cfg.SkillsDir(), cfg.HatsDir(), skillRunner, makeCtx)

	var webFetcher *webfetch.Client
	if cfg.Agent.WebFetch.EnabledValue() {
		webFetcher = webfetch.New(webfetch.Options{AllowPrivate: cfg.Agent.WebFetch.AllowPrivate})
	}

	// The run_js / skill-tool runner. fetch() is routed through the web fetcher
	// (any public host, SSRF-guarded) when WebFetch is enabled; otherwise it falls
	// back to the optional host allowlist.
	runnerOpts := jsrun.Options{
		Timeout:        cfg.Agent.JSToolTimeout.D(),
		OutputCap:      cfg.Agent.JSOutputCap,
		FetchAllowlist: cfg.Agent.JSFetchAllowlist,
	}
	if webFetcher != nil {
		runnerOpts.Fetcher = webFetchAdapter{c: webFetcher}
	}
	runner := jsrun.New(runnerOpts)

	// MCP client: external tool servers. Credentials are encrypted at rest with
	// the configured master key; without it, only auth-less servers are usable.
	var mcpManager *mcp.Manager
	if cfg.MCP.Enabled {
		var cipher *secrets.Cipher
		if len(cfg.SecretKey) > 0 {
			if cipher, err = secrets.New(cfg.SecretKey); err != nil {
				log.Fatalf("mcp: invalid secret key: %v", err)
			}
		} else {
			log.Printf("mcp: HARLEQUIN_SECRET_KEY not set; only auth-less MCP servers will work")
		}
		reg := mcp.NewRegistry(store.Shared, cipher)
		mcpManager = mcp.NewManager(reg, mcp.ManagerConfig{
			SessionIdle:     cfg.MCP.SessionIdleValue(),
			ToolsCacheTTL:   cfg.MCP.ToolsCacheTTLValue(),
			CallbackBaseURL: cfg.MCP.OAuthCallbackBaseURL,
			ClientName:      "harlequin",
			ClientVersion:   "0.1.0",
		}, nil)
		defer mcpManager.Close()
	}

	notifyStore := notify.NewStore()
	projectStore := project.NewStore(store.System)
	cronStore := cron.NewStore()
	presenceTracker := presence.New()
	userCfgStore := userconfig.NewStore()

	// Outbound notification delivery: in-app store + optional email/Telegram.
	emailSender := email.New(cfg.Email)
	tgClient := telegram.New(cfg.Telegram.BotToken, cfg.Telegram.APIBase)
	dispatch := notifyx.NewDispatcher(notifyStore, emailSender, tgClient, userCfgStore)

	ag := &agent.Agent{
		Provider:            router,
		Storage:             store,
		Memory:              memStore,
		Docs:                docStore,
		Skills:              skillMgr,
		Runner:              runner,
		Sessions:            sessStore,
		Session:             sessionLog,
		WebFetcher:          webFetcher,
		MCP:                 mcpManager,
		WebFetchModel:       cfg.Agent.WebFetch.Model,
		WebFetchTemperature: cfg.Agent.WebFetch.TemperatureValue(),
		ReportTiming:        cfg.Agent.ReportTiming,
		MaxSteps:            cfg.Agent.MaxSteps,
		Temperature:         cfg.Agent.TemperatureValue(),
		AutoExtract:         cfg.Memory.AutoExtract,
		MemDefaultTTL:       cfg.Memory.DefaultTTL.D(),
		DataDir:             cfg.DataDir,
		Cron:                cronStore,
		Notify:              notifyStore,
		NotifyDispatch:      dispatch,
		Presence:            presenceTracker,
		RecordUsage: func(ctx context.Context, userDB *sql.DB, userID int64, sessionID *int64, provider, model string, u llm.Usage) {
			_ = usageStore.Record(ctx, userDB, sessionID, provider, model, u.PromptTokens, u.CompletionTokens)
		},
		ContextMax: router.ContextMax,
	}

	// Live sessions: each active chat is a server-side goroutine that survives
	// client disconnects and streams over WebSocket; it exits after idle_timeout
	// with no connection and no running turn.
	hub := sessionhub.New(sessionHubAgent{ag: ag, store: store, sessions: sessStore}, sessionLog, cfg.Sessions.IdleTimeoutValue())
	defer hub.Stop()

	srv := &api.Server{
		Cfg:        cfg,
		Storage:    store,
		Auth:       authStore,
		Sessions:   sessStore,
		Memory:     memStore,
		Docs:       docStore,
		Skills:     skillMgr,
		Usage:      usageStore,
		Audit:      auditStore,
		Session:    sessionLog,
		Agent:      ag,
		MCP:        mcpManager,
		Notify:     notifyStore,
		Cron:       cronStore,
		CronSched:  cron.NewScheduler(store, cronStore, ag, dispatch),
		UserConfig: userCfgStore,
		Presence:   presenceTracker,
		Email:      emailSender,
		Hub:        hub,
		Projects:   projectStore,
	}

	// PDF text extraction for document uploads (PDFium via wasm; best-effort).
	if ex, err := pdfextract.New(); err != nil {
		log.Printf("pdf extraction unavailable: %v", err)
	} else {
		srv.PDFExtract = ex
		defer ex.Close()
	}

	// Queue onboarding for any existing users who still need it.
	srv.SweepOnboarding(context.Background())

	// One-time cleanup: drop personal memories that duplicate a shared slot (an
	// attribute may not live in both scopes). Backgrounded — it may call the LLM.
	go srv.SweepCrossScopeSlots(context.Background())

	// Background maintenance: expire memories and sweep old session logs (hourly).
	go maintenance(store, memStore, sessionLog, cfg.Sessions.RetentionDaysValue())

	// Start the cron scheduler (1-minute granularity; each due job runs in its
	// own goroutine).
	srv.CronSched.Start(context.Background())

	// Background session auto-titler: names idle, generically-titled sessions.
	go ag.RunAutoTitle(context.Background(), cfg.Agent.AutoTitle.EnabledValue())

	httpServer := &http.Server{
		Addr:    cfg.Server.Addr,
		Handler: srv.Router(),
	}
	log.Printf("harlequin-server listening on %s (data dir %s)", cfg.Server.Addr, cfg.DataDir)
	if cfg.Server.Web.Dir != "" {
		log.Printf("web UI: serving static files from %q at /", cfg.Server.Web.Dir)
	}
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server: %v", err)
	}
}

// sessionHubAgent adapts the concrete agent + storage to the sessionhub.Agent
// interface, so the hub package itself stays free of the storage/sqlite link
// chain (and unit-testable).
type sessionHubAgent struct {
	ag       *agent.Agent
	store    *storage.Manager
	sessions *session.Store
}

func (a sessionHubAgent) Run(ctx context.Context, sessionID, userID int64, username, role, api, iface, content string, emit func(types.StreamEvent)) error {
	return a.ag.Run(ctx, sessionID, userID, username, role, api, iface, content, emit)
}

func (a sessionHubAgent) LastMessageID(ctx context.Context, userID, sessionID int64) (int64, error) {
	var id int64
	err := a.store.WithUser(ctx, userID, func(db *sql.DB) error {
		var e error
		id, e = a.sessions.LastMessageID(ctx, db, sessionID)
		return e
	})
	return id, err
}

// webFetchAdapter adapts the web fetcher to the jsrun.Fetcher interface so the
// sandbox fetch() reuses the same anti-bot headers, redirect handling, and SSRF
// guard as the WebFetch tool.
type webFetchAdapter struct{ c *webfetch.Client }

func (a webFetchAdapter) FetchRaw(ctx context.Context, url string) (jsrun.FetchResult, error) {
	r, err := a.c.FetchRaw(ctx, url)
	if err != nil {
		return jsrun.FetchResult{}, err
	}
	return jsrun.FetchResult{Status: r.Status, Body: r.Body, FinalURL: r.FinalURL, ContentType: r.ContentType}, nil
}

func maintenance(store *storage.Manager, mem *memory.Store, sessionLog *sessionlog.Logger, sessionRetentionDays int) {
	const sweepInterval = time.Hour
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	sweep := func() {
		ctx := context.Background()
		_, _ = mem.SweepExpiredDB(ctx, store.Shared)
		_ = store.EachUser(ctx, func(_ int64, udb *sql.DB) error {
			_, _ = mem.SweepExpiredDB(ctx, udb)
			return nil
		})
		sessionLog.SweepRetention(sessionRetentionDays)
	}
	sweep() // run once at startup, then every hour
	for range ticker.C {
		sweep()
	}
}
