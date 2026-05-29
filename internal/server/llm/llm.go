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
	Model    string
	Messages []Message
	Tools    []Tool
}

// Usage holds token accounting from a completion.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
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
	name    string
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// NewOpenAICompatible constructs a provider. defaultModel is used when a request
// does not specify one.
func NewOpenAICompatible(name, baseURL, apiKey, defaultModel string) *OpenAICompatible {
	return &OpenAICompatible{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   defaultModel,
		client:  &http.Client{Timeout: 5 * time.Minute},
	}
}

// Name returns the provider name.
func (p *OpenAICompatible) Name() string { return p.name }

// Model returns the provider's default model.
func (p *OpenAICompatible) Model() string { return p.model }

type streamRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []Tool    `json:"tools,omitempty"`
	Stream      bool      `json:"stream"`
	StreamOpts  streamOpt `json:"stream_options"`
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
		Model:      model,
		Messages:   req.Messages,
		Tools:      req.Tools,
		Stream:     true,
		StreamOpts: streamOpt{IncludeUsage: true},
	})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("provider %s: status %d: %s", p.name, resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	out := make(chan Chunk, 32)
	go p.readStream(resp, model, out)
	return out, nil
}

type sseChunk struct {
	Choices []struct {
		Delta jsonDelta `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
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

func (p *OpenAICompatible) readStream(resp *http.Response, model string, out chan<- Chunk) {
	defer resp.Body.Close()
	defer close(out)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	// Accumulate tool calls by index across deltas.
	toolAcc := map[int]*ToolCall{}
	var order []int
	var usage *Usage

	flush := func(streamErr error) {
		calls := make([]ToolCall, 0, len(order))
		for _, idx := range order {
			calls = append(calls, *toolAcc[idx])
		}
		out <- Chunk{ToolCalls: calls, Usage: usage, Provider: p.name, Model: model, Done: true, Err: streamErr}
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
		if c.Usage != nil {
			usage = c.Usage
		}
		for _, ch := range c.Choices {
			if thinking := normalizeThinking(ch.Delta.extra); thinking != "" {
				out <- Chunk{ThinkingDelta: thinking, Provider: p.name, Model: model}
			}
			if ch.Delta.Content != "" {
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
