package config

import "testing"

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
