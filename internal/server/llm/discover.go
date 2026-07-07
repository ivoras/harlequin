package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DiscoverContextWindows queries an OpenAI-compatible GET /v1/models endpoint
// and returns model id -> max context tokens. Recognises llama.cpp's
// meta.n_ctx and OpenRouter's context_length / top_provider.context_length.
func DiscoverContextWindows(ctx context.Context, baseURL, apiKey string) (map[string]int, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("empty base URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return parseModelsContext(body)
}

func parseModelsContext(body []byte) (map[string]int, error) {
	var raw struct {
		Data []struct {
			ID   string `json:"id"`
			Meta struct {
				NCtx int `json:"n_ctx"` // llama.cpp
			} `json:"meta"`
			ContextLength int `json:"context_length"` // OpenRouter: model's max context
			TopProvider   struct {
				ContextLength int `json:"context_length"` // OpenRouter: current serving provider's limit, may be < ContextLength
			} `json:"top_provider"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]int)
	for _, m := range raw.Data {
		if m.ID == "" {
			continue
		}
		n := m.Meta.NCtx
		switch {
		case n > 0:
			// llama.cpp value is authoritative for that single loaded model.
		case m.TopProvider.ContextLength > 0:
			n = m.TopProvider.ContextLength
		case m.ContextLength > 0:
			n = m.ContextLength
		}
		if n > 0 {
			out[m.ID] = n
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no models with a known context length")
	}
	return out, nil
}

// ApplyConfigModelAlias maps a YAML provider model name (e.g. "local-model") to a
// discovered context size when the server exposes a different model id.
func ApplyConfigModelAlias(windows map[string]int, configModel string) {
	if configModel == "" || len(windows) == 0 {
		return
	}
	if _, ok := windows[configModel]; ok {
		return
	}
	if len(windows) == 1 {
		for _, n := range windows {
			windows[configModel] = n
			return
		}
	}
}
