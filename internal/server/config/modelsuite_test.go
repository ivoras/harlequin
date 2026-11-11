package config

import (
	"os"
	"strings"
	"testing"
)

func fl(v float64) *float64 { return &v }

// A suite with a distinct aux endpoint expands into two providers (chat default
// + aux), a model rule routing the WebFetch model to aux, and the embed settings.
func TestResolveModelSuiteDistinctAux(t *testing.T) {
	c := &Config{
		ModelSuite: "local",
		ModelSuites: map[string]ModelSuite{
			"local": {
				Chat:  ModelSpec{BaseURL: "http://x:2234/v1", Model: "chat-m", ReturnProgress: true, ContextWindow: 100000, Temperature: fl(0.5)},
				Aux:   ModelSpec{BaseURL: "http://x:2236/v1", Model: "aux-m", ReturnProgress: true, Temperature: fl(0.1)},
				Embed: ModelSpec{BaseURL: "http://x:2235/v1", Model: "embed-m", Dim: 1024, QueryPrefix: "query: "},
			},
		},
	}
	if err := c.resolveModelSuite(); err != nil {
		t.Fatal(err)
	}
	if len(c.Providers) != 2 || c.Providers[0].Name != "chat" || c.Providers[1].Name != "aux" {
		t.Fatalf("providers = %+v", c.Providers)
	}
	if c.Providers[0].Model != "chat-m" || !c.Providers[0].ReturnProgress || c.Providers[0].ContextWindow != 100000 {
		t.Fatalf("chat provider = %+v", c.Providers[0])
	}
	if c.Routing.DefaultProvider != "chat" || c.Routing.ModelRules["aux-m"] != "aux" {
		t.Fatalf("routing = %+v", c.Routing)
	}
	if c.Agent.WebFetch.Model != "aux-m" {
		t.Fatalf("web_fetch model = %q", c.Agent.WebFetch.Model)
	}
	if c.Embeddings.Model != "embed-m" || c.Embeddings.Dim != 1024 || c.Embeddings.QueryPrefix != "query: " {
		t.Fatalf("embeddings = %+v", c.Embeddings)
	}
	if c.Agent.Temperature == nil || *c.Agent.Temperature != 0.5 {
		t.Fatalf("chat temp = %v", c.Agent.Temperature)
	}
	if c.Agent.WebFetch.Temperature == nil || *c.Agent.WebFetch.Temperature != 0.1 {
		t.Fatalf("aux temp = %v", c.Agent.WebFetch.Temperature)
	}
}

// aux.tools=false flows through to the WebFetch tools setting (extraction-only).
func TestResolveModelSuiteAuxToolsFlag(t *testing.T) {
	no := false
	c := &Config{
		ModelSuite: "s",
		ModelSuites: map[string]ModelSuite{"s": {
			Chat:  ModelSpec{BaseURL: "http://x/v1", Model: "c"},
			Aux:   ModelSpec{BaseURL: "http://y/v1", Model: "a", Tools: &no},
			Embed: ModelSpec{BaseURL: "http://z/v1", Model: "e"},
		}},
	}
	if err := c.resolveModelSuite(); err != nil {
		t.Fatal(err)
	}
	if c.Agent.WebFetch.ToolsEnabledValue() {
		t.Fatal("aux tools=false should disable WebFetch tools")
	}
	// Default (unset) stays enabled.
	if !(WebFetchConfig{}).ToolsEnabledValue() {
		t.Fatal("WebFetch tools should default to enabled")
	}
}

// When aux is the same endpoint+model as chat (one hosted model serving both),
// no second provider is created and WebFetch uses the default provider/model.
func TestResolveModelSuiteSharedAux(t *testing.T) {
	c := &Config{
		ModelSuite: "or",
		ModelSuites: map[string]ModelSuite{
			"or": {
				Chat:  ModelSpec{BaseURL: "https://or/v1", Model: "minimax/minimax-m3", APIKeyEnv: "OPENROUTER_API_KEY"},
				Aux:   ModelSpec{BaseURL: "https://or/v1", Model: "minimax/minimax-m3", APIKeyEnv: "OPENROUTER_API_KEY"},
				Embed: ModelSpec{BaseURL: "https://or/v1", Model: "qwen/qwen3-embedding-4b", Dim: 2560},
			},
		},
	}
	if err := c.resolveModelSuite(); err != nil {
		t.Fatal(err)
	}
	if len(c.Providers) != 1 || c.Providers[0].Name != "chat" {
		t.Fatalf("expected single chat provider, got %+v", c.Providers)
	}
	if len(c.Routing.ModelRules) != 0 {
		t.Fatalf("no model rules expected, got %+v", c.Routing.ModelRules)
	}
	if c.Agent.WebFetch.Model != "" {
		t.Fatalf("web_fetch model should be empty (use default), got %q", c.Agent.WebFetch.Model)
	}
	if c.Embeddings.Dim != 2560 {
		t.Fatalf("embed dim = %d", c.Embeddings.Dim)
	}
}

func TestResolveModelSuiteErrors(t *testing.T) {
	if err := (&Config{ModelSuite: "nope", ModelSuites: map[string]ModelSuite{}}).resolveModelSuite(); err == nil {
		t.Fatal("expected error for undefined suite")
	}
	// Empty ModelSuite is a no-op (legacy path).
	c := &Config{}
	if err := c.resolveModelSuite(); err != nil || len(c.Providers) != 0 {
		t.Fatalf("empty suite should no-op: err=%v providers=%v", err, c.Providers)
	}
}

// Prices, context windows, the local flag and the fallback model are declared
// next to the model and flow into the maps/routing the server consumes; explicit
// top-level entries win.
func TestResolveModelSuitePerModelSettings(t *testing.T) {
	c := &Config{
		ModelSuite: "s",
		Prices:     map[string]Price{"chat-m": {PromptPer1K: 9}},
		ModelSuites: map[string]ModelSuite{"s": {
			Chat: ModelSpec{
				BaseURL: "http://x/v1", Model: "chat-m", Local: true, ContextWindow: 1000,
				Price:         &Price{PromptPer1K: 1, CompletionPer1K: 2},
				FallbackModel: "fb-m", FallbackContextWindow: 500, FallbackPrice: &Price{PromptPer1K: 3},
			},
			Embed: ModelSpec{BaseURL: "http://x/v1", Model: "embed-m", Price: &Price{PromptPer1K: 4}},
		}},
	}
	if err := c.resolveModelSuite(); err != nil {
		t.Fatal(err)
	}
	if !c.Providers[0].Local || c.Routing.FallbackModel != "fb-m" {
		t.Fatalf("provider=%+v routing=%+v", c.Providers[0], c.Routing)
	}
	if c.Prices["chat-m"].PromptPer1K != 9 || c.Prices["fb-m"].PromptPer1K != 3 || c.Prices["embed-m"].PromptPer1K != 4 {
		t.Fatalf("prices = %+v (top-level chat-m must win)", c.Prices)
	}
	if c.ContextWindows["chat-m"] != 1000 || c.ContextWindows["fb-m"] != 500 {
		t.Fatalf("context windows = %+v", c.ContextWindows)
	}
}

func TestLoadRejectsLegacyProviders(t *testing.T) {
	t.Setenv("JWT_SECRET", "x")
	f := t.TempDir() + "/c.yaml"
	if err := os.WriteFile(f, []byte("providers:\n  - name: a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(f); err == nil || !strings.Contains(err.Error(), "model_suites") {
		t.Fatalf("err = %v, want a pointer to model_suites", err)
	}
}

func TestDocEmbedConcurrency(t *testing.T) {
	c := &Config{}
	if got := c.DocEmbedConcurrency(); got != 10 {
		t.Errorf("hosted default = %d, want 10", got)
	}
	c.Embeddings.Local = true
	if got := c.DocEmbedConcurrency(); got != 1 {
		t.Errorf("local default = %d, want 1", got)
	}
	c.Documents.EmbedConcurrency = 3
	if got := c.DocEmbedConcurrency(); got != 3 {
		t.Errorf("explicit setting = %d, want 3", got)
	}
}

// vision_model_for_ocr adds a routed "vision" provider; without a suite vision
// model it is a load error, and when off the vision model is ignored.
func TestResolveModelSuiteVisionOCR(t *testing.T) {
	mk := func(on bool, vis ModelSpec) *Config {
		return &Config{
			ModelSuite: "s", VisionModelForOCR: on,
			ModelSuites: map[string]ModelSuite{"s": {
				Chat:   ModelSpec{BaseURL: "http://x/v1", Model: "chat-m"},
				Embed:  ModelSpec{BaseURL: "http://x/v1", Model: "embed-m", Dim: 8},
				Vision: vis,
			}},
		}
	}
	vis := ModelSpec{BaseURL: "http://v/v1", Model: "vl-m"}
	c := mk(true, vis)
	if err := c.resolveModelSuite(); err != nil {
		t.Fatal(err)
	}
	if len(c.Providers) != 2 || c.Providers[1].Name != "vision" || c.Routing.ModelRules["vl-m"] != "vision" || c.OCRVisionModel() != "vl-m" {
		t.Fatalf("providers=%+v rules=%v", c.Providers, c.Routing.ModelRules)
	}
	if err := mk(true, ModelSpec{}).resolveModelSuite(); err == nil {
		t.Fatal("expected an error when enabled without a vision model")
	}
	off := mk(false, vis)
	if err := off.resolveModelSuite(); err != nil || len(off.Providers) != 1 || off.OCRVisionModel() != "" {
		t.Fatalf("off: err=%v providers=%+v", err, off.Providers)
	}
}
