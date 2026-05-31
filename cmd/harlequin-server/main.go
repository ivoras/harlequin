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
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/embed"
	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/mdtmpl"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/server/sessionlog"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/server/usage"
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
	router := llm.NewRoutingProvider(providers, cfg.Routing.DefaultProvider, cfg.Routing.FallbackOrder, cfg.Routing.ModelRules, nil)

	embedder := embed.New(cfg.Embeddings.BaseURL, cfg.Embeddings.APIKey, cfg.Embeddings.Model, cfg.Embeddings.Dim)

	runner := jsrun.New(jsrun.Options{
		Timeout:        cfg.Agent.JSToolTimeout.D(),
		OutputCap:      cfg.Agent.JSOutputCap,
		FetchAllowlist: cfg.Agent.JSFetchAllowlist,
	})
	skillRunner := jsrun.New(jsrun.Options{
		Timeout:        cfg.Agent.SkillRenderTimeout.D(),
		OutputCap:      cfg.Agent.JSOutputCap,
		FetchAllowlist: cfg.Agent.JSFetchAllowlist,
	})

	memStore := memory.NewStore(store.Shared, embedder)
	if cfg.Memory.ConflictCheckEnabled() {
		memStore.SetConflictJudge(router, cfg.Memory.ConflictCandidates)
	}
	docStore := documents.NewStore(store.Shared, embedder)
	convStore := conversation.NewStore()
	auditStore := audit.NewStore()
	session := sessionlog.New(cfg.SessionsDir(), cfg.Sessions.Enabled, cfg.Sessions.LogTokens, cfg.Sessions.Redact)

	// The single JS-template context provider, used for every .md the server
	// renders (skills, the system prompt, and hat prompts).
	makeCtx := mdtmpl.New(memStore, docStore, store)
	skillMgr := skills.NewManager(store.Shared, cfg.SkillsDir(), cfg.HatsDir(), skillRunner, makeCtx)

	ag := &agent.Agent{
		Provider:      router,
		Storage:       store,
		Memory:        memStore,
		Docs:          docStore,
		Skills:        skillMgr,
		Runner:        runner,
		Conversations: convStore,
		Session:       session,
		MaxSteps:      cfg.Agent.MaxSteps,
		Temperature:   cfg.Agent.TemperatureValue(),
		AutoExtract:   cfg.Memory.AutoExtract,
		MemDefaultTTL: cfg.Memory.DefaultTTL.D(),
		RecordUsage: func(ctx context.Context, userDB *sql.DB, userID int64, conversationID *int64, provider, model string, u llm.Usage) {
			_ = usageStore.Record(ctx, userDB, conversationID, provider, model, u.PromptTokens, u.CompletionTokens)
		},
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
	}

	// Background maintenance: expire memories and sweep session logs.
	go maintenance(store, memStore, session, cfg.Sessions.RetentionDays)

	httpServer := &http.Server{
		Addr:    cfg.Server.Addr,
		Handler: srv.Router(),
	}
	log.Printf("harlequin-server listening on %s (data dir %s)", cfg.Server.Addr, cfg.DataDir)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server: %v", err)
	}
}

func maintenance(store *storage.Manager, mem *memory.Store, session *sessionlog.Logger, retentionDays int) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		ctx := context.Background()
		// Sweep expired memories from the shared database and every user database.
		_, _ = mem.SweepExpiredDB(ctx, store.Shared)
		_ = store.EachUser(ctx, func(_ int64, udb *sql.DB) error {
			_, _ = mem.SweepExpiredDB(ctx, udb)
			return nil
		})
		session.SweepRetention(retentionDays)
		<-ticker.C
	}
}
