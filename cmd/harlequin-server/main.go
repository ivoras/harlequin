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
	"github.com/ivoras/harlequin/internal/server/conversation"
	"github.com/ivoras/harlequin/internal/server/cron"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/embed"
	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/mcp"
	"github.com/ivoras/harlequin/internal/server/mdtmpl"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/server/notify"
	"github.com/ivoras/harlequin/internal/server/presence"
	"github.com/ivoras/harlequin/internal/server/secrets"
	"github.com/ivoras/harlequin/internal/server/sessionlog"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/server/usage"
	"github.com/ivoras/harlequin/internal/server/userconfig"
	"github.com/ivoras/harlequin/internal/server/webfetch"
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
		providers[p.Name] = llm.NewOpenAICompatible(p.Name, p.BaseURL, p.APIKey, p.Model)
	}
	// Usage is recorded in the agent loop where the user/conversation are known,
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
	if cfg.Memory.ConflictCheckEnabled() {
		memStore.SetConflictJudge(router, cfg.Memory.ConflictCandidates)
	}
	docStore := documents.NewStore(store.Shared, embedder)
	convStore := conversation.NewStore()
	auditStore := audit.NewStore()
	session := sessionlog.New(cfg.SessionsDir(), cfg.Sessions.EnabledValue(), cfg.Sessions.LogTokens, cfg.Sessions.Redact)

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
	cronStore := cron.NewStore()
	presenceTracker := presence.New()

	ag := &agent.Agent{
		Provider:            router,
		Storage:             store,
		Memory:              memStore,
		Docs:                docStore,
		Skills:              skillMgr,
		Runner:              runner,
		Conversations:       convStore,
		Session:             session,
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
		Presence:            presenceTracker,
		RecordUsage: func(ctx context.Context, userDB *sql.DB, userID int64, conversationID *int64, provider, model string, u llm.Usage) {
			_ = usageStore.Record(ctx, userDB, conversationID, provider, model, u.PromptTokens, u.CompletionTokens)
		},
		ContextMax: router.ContextMax,
	}

	srv := &api.Server{
		Cfg:           cfg,
		Storage:       store,
		Auth:          authStore,
		Conversations: convStore,
		Memory:        memStore,
		Docs:          docStore,
		Skills:        skillMgr,
		Usage:         usageStore,
		Audit:         auditStore,
		Session:       session,
		Agent:         ag,
		MCP:           mcpManager,
		Notify:        notifyStore,
		Cron:          cronStore,
		CronSched:     cron.NewScheduler(store, cronStore, ag, notifyStore),
		UserConfig:    userconfig.NewStore(),
		Presence:      presenceTracker,
	}

	// Queue onboarding for any existing users who still need it.
	srv.SweepOnboarding(context.Background())

	// Background maintenance: expire memories and sweep old session logs (hourly).
	go maintenance(store, memStore, session, cfg.Sessions.RetentionDaysValue())

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

func maintenance(store *storage.Manager, mem *memory.Store, session *sessionlog.Logger, sessionRetentionDays int) {
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
		session.SweepRetention(sessionRetentionDays)
	}
	sweep() // run once at startup, then every hour
	for range ticker.C {
		sweep()
	}
}
