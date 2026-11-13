package config

import "testing"

func TestMarkupPercentValidation(t *testing.T) {
	for _, m := range []float64{-1, 1001} {
		c := &Config{JWTSecret: "x", ModelSuite: "y"}
		c.Server.OrgLLMMarkupPercent = m
		if err := c.validate(); err == nil {
			t.Errorf("markup %v accepted", m)
		}
	}
}

func TestUnpricedModels(t *testing.T) {
	c := &Config{
		Prices:  map[string]Price{"priced": {PromptPer1K: 1}},
		Routing: RoutingConfig{DefaultProvider: "chat", FallbackModel: "fb"},
		Providers: []ProviderConfig{
			{Name: "chat", Model: "priced"},
			{Name: "aux", Model: "priced"}, // same model twice: listed at most once
			{Name: "vision", Model: "vis"},
			{Name: "local", Model: "llama", Local: true},
		},
		Embeddings: EmbeddingsConfig{Model: "emb"},
	}
	got := c.UnpricedModels()
	if len(got) != 3 || got[0] != "emb" || got[1] != "fb" || got[2] != "vis" {
		t.Errorf("got %v", got)
	}
	c.Prices["vis"], c.Prices["fb"], c.Prices["emb"] = Price{}, Price{}, Price{}
	if got := c.UnpricedModels(); len(got) != 0 {
		t.Errorf("all priced: %v", got)
	}
}
