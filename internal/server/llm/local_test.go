package llm

import "testing"

func TestOpenAICompatibleLocal(t *testing.T) {
	cases := []struct {
		baseURL string
		want    bool
	}{
		{"http://127.0.0.1:2234/v1", true},
		{"http://localhost:8080/v1", true},
		{"http://[::1]:2234/v1", true},
		{"https://openrouter.ai/api/v1", false},
		{"https://api.anthropic.com/v1", false},
		{"http://192.168.1.20:2234/v1", false},
	}
	for _, c := range cases {
		p := NewOpenAICompatible("p", c.baseURL, "", "m")
		if got := p.Local(); got != c.want {
			t.Errorf("Local(%q) = %v, want %v", c.baseURL, got, c.want)
		}
	}
}
