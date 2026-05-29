// Command harlequin-server runs the Harlequin REST/SSE API server.
package main

import (
	"context"
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
	"github.com/ivoras/harlequin/internal/server/db"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/embed"
	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/server/sessionlog"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/server/skills/jstmpl"
	"github.com/ivoras/harlequin/internal/server/usage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func main() {
	if dispatchCLI(os.Args[1:]) {
		return
	}

	configPath := flag.String("config", "server.yaml", "path to server config YAML")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	database, err := db.Open(cfg.DBPath, cfg.Embeddings.Dim)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer database.Close()

	authStore := auth.NewStore(database)

	// Deploy baked-in skills to the data dir.
	if err := skills.Deploy(harlequin.BakedSkillsFS(), cfg.SkillsDir(), cfg.DataDir); err != nil {
		log.Fatalf("deploy skills: %v", err)
	}

	// Build providers and the routing provider.
	usageStore := usage.NewStore(database, cfg.Prices)
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

	memStore := memory.NewStore(database, embedder)
	if cfg.Memory.ConflictCheckEnabled() {
		memStore.SetConflictJudge(router, cfg.Memory.ConflictCandidates)
	}
	docStore := documents.NewStore(database, embedder)
	convStore := conversation.NewStore(database)
	auditStore := audit.NewStore(database)
	session := sessionlog.New(cfg.SessionsDir(), cfg.Sessions.Enabled, cfg.Sessions.LogTokens, cfg.Sessions.Redact)

	makeCtx := func(userID int64, username, skill string) jstmpl.Context {
		return jstmpl.Context{
			User:  username,
			Skill: skill,
			Now:   time.Now,
			MemorySearch: func(q string) []string {
				res, _ := memStore.Search(context.Background(), q, userID, "", 5)
				return resultsToStrings(res)
			},
			SearchDocs: func(q string) []string {
				res, _ := docStore.Search(context.Background(), q, 5)
				return resultsToStrings(res)
			},
		}
	}
	skillMgr := skills.NewManager(database, cfg.SkillsDir(), skillRunner, makeCtx)

	ag := &agent.Agent{
		Provider:      router,
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
		RecordUsage: func(ctx context.Context, userID int64, conversationID *int64, provider, model string, u llm.Usage) {
			_ = usageStore.Record(ctx, userID, conversationID, provider, model, u.PromptTokens, u.CompletionTokens)
		},
	}

	srv := &api.Server{
		Cfg:           cfg,
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
	go maintenance(memStore, session, cfg.Sessions.RetentionDays)

	httpServer := &http.Server{
		Addr:    cfg.Server.Addr,
		Handler: srv.Router(),
	}
	log.Printf("harlequin-server listening on %s (data dir %s)", cfg.Server.Addr, cfg.DataDir)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server: %v", err)
	}
}

func maintenance(mem *memory.Store, session *sessionlog.Logger, retentionDays int) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		_, _ = mem.SweepExpired(context.Background())
		session.SweepRetention(retentionDays)
		<-ticker.C
	}
}

func resultsToStrings(res []types.SearchResult) []string {
	out := make([]string, len(res))
	for i, r := range res {
		out[i] = r.Content
	}
	return out
}
