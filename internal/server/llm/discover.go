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
// and returns model id -> max context tokens (from meta.n_ctx when present).
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
				NCtx int `json:"n_ctx"`
			} `json:"meta"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]int)
	for _, m := range raw.Data {
		if m.ID != "" && m.Meta.NCtx > 0 {
			out[m.ID] = m.Meta.NCtx
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no models with meta.n_ctx")
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
