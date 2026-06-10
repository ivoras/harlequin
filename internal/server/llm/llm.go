// Package llm defines the chat LLM Provider interface and an OpenAI-compatible
// streaming implementation that works with both llama.cpp and OpenRouter.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// Role constants.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message is a single chat message in OpenAI format.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolCall is an OpenAI-style tool call.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall holds the called function name and raw JSON arguments.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool is a tool definition advertised to the model.
type Tool struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

// FunctionDefinition describes a callable function.
type FunctionDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ChatRequest is a chat completion request.
type ChatRequest struct {
	Model       string
	Messages    []Message
	Tools       []Tool
	Temperature *float64 // omitted when nil (provider default)
}

// Ptr returns a pointer to v (for optional request fields).
func Ptr(v float64) *float64 { return &v }

// Usage holds token accounting from a completion.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// PromptTokensDetails carries cache info when the provider reports it
	// (OpenAI/OpenRouter style); used to discount cached prefill in timing.
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
}

// CachedPromptTokens returns the number of prompt tokens served from cache, or 0.
func (u *Usage) CachedPromptTokens() int {
	if u == nil || u.PromptTokensDetails == nil {
		return 0
	}
	return u.PromptTokensDetails.CachedTokens
}

// Timings holds server-reported model operation timing (llama.cpp `timings`
// object). prompt_n counts tokens actually evaluated, excluding KV-cache hits,
// so PP derived from it is accurate even when the conversation is cached.
type Timings struct {
	PromptN            int     `json:"prompt_n"`
	PromptMS           float64 `json:"prompt_ms"`
	PromptPerSecond    float64 `json:"prompt_per_second"`
	PredictedN         int     `json:"predicted_n"`
	PredictedMS        float64 `json:"predicted_ms"`
	PredictedPerSecond float64 `json:"predicted_per_second"`
}

// PromptProgress is llama.cpp's live prompt-processing progress, emitted before
// the first token when `return_progress` is requested. total = total prompt
// tokens, cache = tokens reused from the KV cache, processed = tokens evaluated
// so far. The work remaining is (total - cache); percent = processed/(total-cache).
type PromptProgress struct {
	Total     int `json:"total"`
	Cache     int `json:"cache"`
	Processed int `json:"processed"`
	TimeMS    int `json:"time_ms"`
}

// Chunk is a streamed piece of a completion.
type Chunk struct {
	// TextDelta is incremental assistant text (final response).
	TextDelta string
	// ThinkingDelta is incremental model reasoning/thinking text, normalized from
	// provider-specific fields (reasoning_content, thinking, etc.).
	ThinkingDelta string
	// ToolCalls is set on the final chunk if the model requested tools.
	ToolCalls []ToolCall
	// Usage is set on the final chunk when reported.
	Usage *Usage
	// Timings is set on the final chunk when the provider reports server-side
	// model operation timing (llama.cpp).
	Timings *Timings
	// PromptProgress is set on a pre-generation progress chunk (llama.cpp
	// `return_progress`); such chunks carry no text/tool calls and are not Done.
	PromptProgress *PromptProgress
	// Provider/Model that served this response.
	Provider string
	Model    string
	// Done marks the terminal chunk.
	Done bool
	// Err carries a streaming error.
	Err error
}

// Provider is a chat LLM backend.
type Provider interface {
	Name() string
	Chat(ctx context.Context, req ChatRequest) (<-chan Chunk, error)
}

// OpenAICompatible talks to any OpenAI-compatible /chat/completions endpoint.
type OpenAICompatible struct {
	name           string
	baseURL        string
	apiKey         string
	model          string
	client         *http.Client
	ppAvg          *ppTracker // rolling prompt-processing rate, drives the dynamic timeout
	returnProgress bool       // request llama.cpp prompt-processing progress events
}

// NewOpenAICompatible constructs a provider. defaultModel is used when a request
// does not specify one.
func NewOpenAICompatible(name, baseURL, apiKey, defaultModel string) *OpenAICompatible {
	return &OpenAICompatible{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   defaultModel,
		// No fixed client timeout: each call gets a dynamic per-request deadline
		// via context (see Chat / requestTimeout).
		client: &http.Client{},
		ppAvg:  newPPTracker(),
	}
}

// SetReturnProgress enables requesting llama.cpp prompt-processing progress
// (the `return_progress` extension). Leave off for non-llama.cpp backends.
func (p *OpenAICompatible) SetReturnProgress(v bool) { p.returnProgress = v }

// Name returns the provider name.
func (p *OpenAICompatible) Name() string { return p.name }

// Model returns the provider's default model.
func (p *OpenAICompatible) Model() string { return p.model }

type streamRequest struct {
	Model          string    `json:"model"`
	Messages       []Message `json:"messages"`
	Tools          []Tool    `json:"tools,omitempty"`
	Stream         bool      `json:"stream"`
	StreamOpts     streamOpt `json:"stream_options"`
	Temperature    *float64  `json:"temperature,omitempty"`
	ReturnProgress bool      `json:"return_progress,omitempty"`
}

type streamOpt struct {
	IncludeUsage bool `json:"include_usage"`
}

// Chat streams a completion. Returns a channel of chunks; the final chunk has Done=true.
func (p *OpenAICompatible) Chat(ctx context.Context, req ChatRequest) (<-chan Chunk, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}
	body, err := json.Marshal(streamRequest{
		Model:          model,
		Messages:       req.Messages,
		Tools:          req.Tools,
		Stream:         true,
		StreamOpts:     streamOpt{IncludeUsage: true},
		Temperature:    req.Temperature,
		ReturnProgress: p.returnProgress,
	})
	if err != nil {
		return nil, err
	}

	// Dynamic per-call deadline: ppTimeoutMultiplier x the predicted prompt-
	// processing time for this request, instead of one fixed client timeout.
	estTokens := EstimateMessagesTokens(req.Messages, req.Tools)
	timeout := p.requestTimeout(estTokens)
	callCtx, cancel := context.WithTimeout(ctx, timeout)

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	start := time.Now()
	resp, err := p.client.Do(httpReq)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		cancel()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("provider %s: status %d: %s", p.name, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	log.Printf("llm[%s]: model %s, ~%d prompt tokens, timeout %s (avg PP %.0f tok/s)",
		p.name, model, estTokens, timeout.Round(time.Second), p.ppAvg.avg())

	out := make(chan Chunk, 32)
	go p.readStream(resp, model, out, start, cancel)
	return out, nil
}

type sseChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta jsonDelta `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage          *Usage          `json:"usage"`
	Timings        *Timings        `json:"timings"`
	PromptProgress *PromptProgress `json:"prompt_progress"`
	Error          *streamError    `json:"error"`
}

// streamError is a provider error delivered inside a 200 SSE stream (e.g.
// OpenRouter). Without surfacing it the agent sees an empty completion and
// silently stops — so it must become a real error.
type streamError struct {
	Message  string `json:"message"`
	Code     any    `json:"code"`
	Type     string `json:"type"`
	Metadata any    `json:"metadata"`
}

func (e *streamError) String() string {
	if e == nil {
		return ""
	}
	s := e.Message
	if s == "" {
		s = e.Type
	}
	if e.Code != nil {
		s = fmt.Sprintf("%s (code %v)", s, e.Code)
	}
	return s
}

// jsonDelta captures content, tool_calls, and any provider-specific thinking
// fields via a two-pass decode (structured fields + raw map for extensions).
type jsonDelta struct {
	Content   string `json:"content"`
	ToolCalls []struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
	extra map[string]any
}

func (d *jsonDelta) UnmarshalJSON(data []byte) error {
	type alias jsonDelta
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*d = jsonDelta(a)
	_ = json.Unmarshal(data, &d.extra)
	return nil
}

func (p *OpenAICompatible) readStream(resp *http.Response, model string, out chan<- Chunk, start time.Time, cancel context.CancelFunc) {
	defer cancel() // release the per-call deadline once the stream is fully drained
	defer resp.Body.Close()
	defer close(out)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	// Accumulate tool calls by index across deltas.
	toolAcc := map[int]*ToolCall{}
	var order []int
	var usage *Usage
	var timings *Timings
	var firstContentAt time.Time // first token, for the wall-clock PP fallback
	resolvedModel := model

	flush := func(streamErr error) {
		p.recordPP(usage, timings, start, firstContentAt)
		calls := make([]ToolCall, 0, len(order))
		for _, idx := range order {
			calls = append(calls, *toolAcc[idx])
		}
		out <- Chunk{ToolCalls: calls, Usage: usage, Timings: timings, Provider: p.name, Model: resolvedModel, Done: true, Err: streamErr}
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			flush(nil)
			return
		}
		var c sseChunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			continue
		}
		if c.Error != nil {
			// Provider returned an error inside a 200 stream — surface it instead
			// of ending the turn empty.
			flush(fmt.Errorf("provider %s: %s", p.name, c.Error.String()))
			return
		}
		if c.Model != "" {
			resolvedModel = c.Model
		}
		if c.Usage != nil {
			usage = c.Usage
		}
		if c.Timings != nil {
			timings = c.Timings
		}
		if c.PromptProgress != nil {
			// Pre-generation progress; carries no content. Forward as-is and let
			// the caller throttle/render.
			out <- Chunk{PromptProgress: c.PromptProgress, Provider: p.name, Model: model}
		}
		for _, ch := range c.Choices {
			if thinking := normalizeThinking(ch.Delta.extra); thinking != "" {
				if firstContentAt.IsZero() {
					firstContentAt = time.Now()
				}
				out <- Chunk{ThinkingDelta: thinking, Provider: p.name, Model: model}
			}
			if ch.Delta.Content != "" {
				if firstContentAt.IsZero() {
					firstContentAt = time.Now()
				}
				out <- Chunk{TextDelta: ch.Delta.Content, Provider: p.name, Model: model}
			}
			for _, tc := range ch.Delta.ToolCalls {
				acc, ok := toolAcc[tc.Index]
				if !ok {
					acc = &ToolCall{Type: "function"}
					toolAcc[tc.Index] = acc
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					acc.ID = tc.ID
				}
				if tc.Function.Name != "" {
					acc.Function.Name = tc.Function.Name
				}
				acc.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := scanner.Err(); err != nil {
		flush(err)
		return
	}
	flush(nil)
}
