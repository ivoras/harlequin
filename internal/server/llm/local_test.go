package llm

import "testing"

func TestOpenAICompatibleLocal(t *testing.T) {
	p := NewOpenAICompatible("chat", "http://example.invalid/v1", "", "m")
	if p.Local() {
		t.Error("a provider is hosted unless marked local")
	}
	p.SetLocal(true)
	if !p.Local() {
		t.Error("SetLocal(true) should mark the provider local")
	}
}

func TestRoutingProviderLocalFollowsDefault(t *testing.T) {
	local := NewOpenAICompatible("a", "http://127.0.0.1:2234/v1", "", "m")
	local.SetLocal(true)
	providers := map[string]*OpenAICompatible{
		"a": local,
		"b": NewOpenAICompatible("b", "https://example.invalid/v1", "", "m"),
	}
	for def, want := range map[string]bool{"a": true, "b": false, "missing": false} {
		r := NewRoutingProvider(providers, def, nil, nil, nil, nil)
		if got := r.Local(); got != want {
			t.Errorf("router Local() with default %q = %v, want %v", def, got, want)
		}
	}
}

func TestRoutingContextMaxDefaultsToTheDefaultModel(t *testing.T) {
	providers := map[string]*OpenAICompatible{"p": NewOpenAICompatible("p", "http://x", "", "small")}
	r := NewRoutingProvider(providers, "p", nil, nil, map[string]int{"small": 32000}, nil)
	if got := r.ContextMax(""); got != 32000 {
		t.Fatalf("ContextMax(\"\") = %d, want the default model's 32000", got)
	}
}
