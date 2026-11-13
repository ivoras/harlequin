// Package embed provides a dedicated embeddings client, independent of the chat LLM.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
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

// UsageFunc is told about every successful embeddings request so the spend can
// be accounted. cost is the provider-reported USD cost (nil if it sent none);
// estimated says promptTokens was guessed because the response carried no usage.
type UsageFunc func(ctx context.Context, model string, promptTokens int, cost *float64, estimated bool)

// OpenAIEmbedder calls an OpenAI-compatible /embeddings endpoint.
type OpenAIEmbedder struct {
	baseURL     string
	apiKey      string
	model       string
	dim         int
	queryPrefix string
	docPrefix   string
	client      *http.Client
	onUsage     UsageFunc
}

// SetUsageFunc installs the usage recorder (optional; call before use).
func (e *OpenAIEmbedder) SetUsageFunc(f UsageFunc) { e.onUsage = f }

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
	Usage *struct {
		PromptTokens int      `json:"prompt_tokens"`
		TotalTokens  int      `json:"total_tokens"`
		Cost         *float64 `json:"cost"`
	} `json:"usage"`
}

// Embed returns document embeddings (the document prefix is applied).
func (e *OpenAIEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	return e.embed(ctx, e.docPrefix, inputs)
}

// EmbedQuery returns query embeddings (the query prefix is applied).
func (e *OpenAIEmbedder) EmbedQuery(ctx context.Context, inputs []string) ([][]float32, error) {
	return e.embed(ctx, e.queryPrefix, inputs)
}

// maxEmbedBatch caps inputs per embeddings request. A book-sized document
// (10k+ sentences / 1k+ chunks) sent as one request blows the 60s client
// timeout; sequential sub-batches keep every call comfortably under it.
const maxEmbedBatch = 64

// embed prepends prefix to every input (if non-empty) and calls the endpoint,
// splitting large input sets into sequential sub-batches.
func (e *OpenAIEmbedder) embed(ctx context.Context, prefix string, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	if len(inputs) > maxEmbedBatch {
		out := make([][]float32, 0, len(inputs))
		for start := 0; start < len(inputs); start += maxEmbedBatch {
			end := min(start+maxEmbedBatch, len(inputs))
			vecs, err := e.embed(ctx, prefix, inputs[start:end])
			if err != nil {
				return nil, fmt.Errorf("batch %d-%d of %d: %w", start, end, len(inputs), err)
			}
			out = append(out, vecs...)
		}
		return out, nil
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
	// Transient failures (rate limits, 5xx, dropped connections) are retried
	// with backoff: a large document makes dozens of sequential calls, and one
	// blip must not discard the whole ingestion.
	for attempt := 0; ; attempt++ {
		out, retryAfter, err := e.post(ctx, body, inputs)
		if err == nil {
			return out, nil
		}
		if attempt >= embedRetries || ctx.Err() != nil || !transient(err) {
			return nil, err
		}
		delay := max(embedBackoff<<attempt, retryAfter)
		log.Printf("embed: transient error (attempt %d/%d), retrying in %s: %v", attempt+1, embedRetries+1, delay, err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

// Retry policy for transient embedding failures; embedBackoff doubles per attempt.
const embedRetries = 3

var embedBackoff = time.Second

// statusError is a non-200 embeddings response.
type statusError struct {
	code       int
	msg        string
	retryAfter time.Duration // from Retry-After (seconds), capped
}

func (e *statusError) Error() string { return fmt.Sprintf("embeddings: status %d: %s", e.code, e.msg) }

// transient reports whether err is worth retrying: a network-level failure or
// a 408/425/429/5xx response. Client errors (400, 401, 413, …) are not.
func transient(err error) bool {
	var se *statusError
	if errors.As(err, &se) {
		return se.code == 408 || se.code == 425 || se.code == 429 || se.code >= 500
	}
	return true // transport error (timeout, reset, EOF): the request never completed
}

// post sends one embeddings request. On a non-200 response it returns a
// *statusError (with any Retry-After hint).
func (e *OpenAIEmbedder) post(ctx context.Context, body []byte, inputs []string) ([][]float32, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		se := &statusError{code: resp.StatusCode, msg: strings.TrimSpace(string(msg))}
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
			se.retryAfter = min(time.Duration(secs)*time.Second, 30*time.Second)
		}
		return nil, se.retryAfter, se
	}
	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, 0, err
	}
	out := make([][]float32, len(er.Data))
	for _, d := range er.Data {
		if d.Index >= 0 && d.Index < len(out) {
			out[d.Index] = d.Embedding
		}
	}
	e.reportUsage(ctx, er, inputs)
	return out, 0, nil
}

// reportUsage tells the recorder what a successful request consumed: the
// provider's token count (and cost) when given, else a chars/4 estimate.
func (e *OpenAIEmbedder) reportUsage(ctx context.Context, er embedResponse, inputs []string) {
	if e.onUsage == nil {
		return
	}
	var cost *float64
	tokens, estimated := 0, false
	if er.Usage != nil {
		cost = er.Usage.Cost
		tokens = er.Usage.PromptTokens
		if tokens == 0 {
			tokens = er.Usage.TotalTokens
		}
	}
	if tokens == 0 {
		estimated = true
		for _, s := range inputs {
			tokens += (len(s) + 3) / 4
		}
	}
	e.onUsage(ctx, e.model, tokens, cost, estimated)
}
