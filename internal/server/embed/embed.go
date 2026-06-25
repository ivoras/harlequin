// Package embed provides a dedicated embeddings client, independent of the chat LLM.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Embedder turns text into vectors.
type Embedder interface {
	// Embed returns one vector per input string, treating each input as a
	// document/passage (the document prompt prefix is applied).
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
	// EmbedQuery is like Embed but treats each input as a search query (the
	// query prompt prefix is applied). Asymmetric retrieval models embed
	// queries and documents differently; symmetric models make the two equal.
	EmbedQuery(ctx context.Context, inputs []string) ([][]float32, error)
	// Dim is the embedding dimension.
	Dim() int
}

// OpenAIEmbedder calls an OpenAI-compatible /embeddings endpoint.
type OpenAIEmbedder struct {
	baseURL     string
	apiKey      string
	model       string
	dim         int
	queryPrefix string
	docPrefix   string
	client      *http.Client
}

// New builds an embedder. queryPrefix/docPrefix are the model's prompt
// conventions (e.g. snowflake-arctic wants "query: " on queries and nothing on
// documents); pass "" for symmetric models.
func New(baseURL, apiKey, model string, dim int, queryPrefix, docPrefix string) *OpenAIEmbedder {
	return &OpenAIEmbedder{
		baseURL:     strings.TrimRight(baseURL, "/"),
		apiKey:      apiKey,
		model:       model,
		dim:         dim,
		queryPrefix: queryPrefix,
		docPrefix:   docPrefix,
		client: &http.Client{
			Timeout: 60 * time.Second,
			// Disable keep-alive: local embedding servers (llama.cpp) close idle
			// connections, and a reused-but-stale pooled connection makes the next
			// POST hang until timeout (POSTs aren't auto-retried). A fresh TCP
			// connection per request is negligible over loopback.
			Transport: &http.Transport{DisableKeepAlives: true},
		},
	}
}

// Dim returns the embedding dimension.
func (e *OpenAIEmbedder) Dim() int { return e.dim }

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

// Embed returns document embeddings (the document prefix is applied).
func (e *OpenAIEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	return e.embed(ctx, e.docPrefix, inputs)
}

// EmbedQuery returns query embeddings (the query prefix is applied).
func (e *OpenAIEmbedder) EmbedQuery(ctx context.Context, inputs []string) ([][]float32, error) {
	return e.embed(ctx, e.queryPrefix, inputs)
}

// embed prepends prefix to every input (if non-empty) and calls the endpoint.
func (e *OpenAIEmbedder) embed(ctx context.Context, prefix string, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	if prefix != "" {
		prefixed := make([]string, len(inputs))
		for i, s := range inputs {
			prefixed[i] = prefix + s
		}
		inputs = prefixed
	}
	body, err := json.Marshal(embedRequest{Model: e.model, Input: inputs})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("embeddings: status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, err
	}
	out := make([][]float32, len(er.Data))
	for _, d := range er.Data {
		if d.Index >= 0 && d.Index < len(out) {
			out[d.Index] = d.Embedding
		}
	}
	return out, nil
}
