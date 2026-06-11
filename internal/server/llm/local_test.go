package llm

import "testing"

func TestOpenAICompatibleLocal(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"local", true},
		{"openrouter", false},
		{"anthropic", false},
		{"localish", false},
	}
	for _, c := range cases {
		p := NewOpenAICompatible(c.name, "http://example.invalid/v1", "", "m")
		if got := p.Local(); got != c.want {
			t.Errorf("Local() for provider %q = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRoutingProviderLocalFollowsDefault(t *testing.T) {
	providers := map[string]*OpenAICompatible{
		"local":      NewOpenAICompatible("local", "http://127.0.0.1:2234/v1", "", "m"),
		"openrouter": NewOpenAICompatible("openrouter", "https://openrouter.ai/api/v1", "", "m"),
	}
	for def, want := range map[string]bool{"local": true, "openrouter": false, "missing": false} {
		r := NewRoutingProvider(providers, def, nil, nil, nil, nil)
		if got := r.Local(); got != want {
			t.Errorf("router Local() with default %q = %v, want %v", def, got, want)
		}
	}
}
