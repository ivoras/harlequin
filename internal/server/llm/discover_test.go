package llm

import "testing"

func TestParseModelsContext_llamaCpp(t *testing.T) {
	body := []byte(`{
		"data": [{
			"id": "Qwen3.6-35B-A3B-IQ4_XS-3.53bpw.gguf",
			"meta": {"n_ctx": 120064, "n_ctx_train": 262144}
		}]
	}`)
	got, err := parseModelsContext(body)
	if err != nil {
		t.Fatal(err)
	}
	if got["Qwen3.6-35B-A3B-IQ4_XS-3.53bpw.gguf"] != 120064 {
		t.Fatalf("n_ctx: got %v", got)
	}
}

func TestParseModelsContext_openRouter(t *testing.T) {
	body := []byte(`{
		"data": [{
			"id": "anthropic/claude-sonnet-4",
			"context_length": 200000,
			"top_provider": {"context_length": 200000, "max_completion_tokens": 64000}
		}, {
			"id": "some/provider-limited",
			"context_length": 128000,
			"top_provider": {"context_length": 32000}
		}]
	}`)
	got, err := parseModelsContext(body)
	if err != nil {
		t.Fatal(err)
	}
	if got["anthropic/claude-sonnet-4"] != 200000 {
		t.Fatalf("context_length: got %v", got)
	}
	// top_provider.context_length (the currently serving provider's limit) wins
	// over the model's nominal context_length when it's smaller.
	if got["some/provider-limited"] != 32000 {
		t.Fatalf("top_provider.context_length precedence: got %v", got)
	}
}

func TestApplyConfigModelAlias_singleModel(t *testing.T) {
	w := map[string]int{"Qwen3.6-35B.gguf": 120064}
	ApplyConfigModelAlias(w, "local-model")
	if w["local-model"] != 120064 {
		t.Fatalf("alias: %+v", w)
	}
}
