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

func TestApplyConfigModelAlias_singleModel(t *testing.T) {
	w := map[string]int{"Qwen3.6-35B.gguf": 120064}
	ApplyConfigModelAlias(w, "local-model")
	if w["local-model"] != 120064 {
		t.Fatalf("alias: %+v", w)
	}
}
