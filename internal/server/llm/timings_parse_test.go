package llm

import (
	"encoding/json"
	"testing"
)

func TestSSEChunkParsesTimings(t *testing.T) {
	data := `{"model":"m","choices":[{"delta":{}}],"usage":{"prompt_tokens":1500,"completion_tokens":200,"prompt_tokens_details":{"cached_tokens":1400}},"timings":{"prompt_n":100,"prompt_ms":50.0,"prompt_per_second":2000.0,"predicted_n":200,"predicted_ms":4000.0,"predicted_per_second":50.0}}`
	var c sseChunk
	if err := json.Unmarshal([]byte(data), &c); err != nil {
		t.Fatal(err)
	}
	if c.Timings == nil || c.Timings.PromptN != 100 || c.Timings.PredictedN != 200 {
		t.Fatalf("timings not parsed: %+v", c.Timings)
	}
	if c.Usage.CachedPromptTokens() != 1400 {
		t.Fatalf("cached tokens = %d, want 1400", c.Usage.CachedPromptTokens())
	}
}
