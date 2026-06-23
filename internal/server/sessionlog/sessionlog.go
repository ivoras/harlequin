// Package sessionlog writes the full chat trajectory to per-session JSONL
// files for reproducibility, debugging, and eval datasets. It is distinct from
// the audit log: this captures everything needed to replay a run.
package sessionlog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Event types.
const (
	TypeSessionStart    = "session_start"
	TypeUserMessage     = "user_message"
	TypeSystemPrompt    = "system_prompt"
	TypeLLMRequest      = "llm_request"
	TypeLLMResponse     = "llm_response"
	TypeLLMDelta        = "llm_delta"
	TypeThinkingDelta   = "thinking_delta"
	TypeToolCall        = "tool_call"
	TypeToolResult      = "tool_result"
	TypeToolsAvailable  = "tools_available"
	TypeSkillsAvailable = "skills_available"
	TypeSkillLoaded     = "skill_loaded"
	TypeUsage           = "usage"
	TypeError           = "error"
	TypeSessionEnd      = "session_end"
	// WebFetch tool: the network fetch and the delegated (inner) LLM call that
	// analyses the fetched page.
	TypeWebFetch             = "web_fetch"
	TypeDelegatedLLMRequest  = "delegated_llm_request"
	TypeDelegatedLLMResponse = "delegated_llm_response"

	// MCP (Model Context Protocol) external tool calls.
	TypeMCPCall = "mcp_call"

	// Live-session lifecycle (WebSocket sessions). TypeSessionResumed records a
	// client (re)attaching to a live session (cold or warm). TypeSessionExpired
	// records the idle reaper closing the session goroutine.
	TypeSessionResumed = "session_resumed"
	TypeSessionExpired = "session_expired"

	// TypeMemoryFeedback records, per turn, which memories were recalled by
	// memory_search and which the model explicitly cited via memory_useful —
	// instrumentation for measuring how reliably the model reports useful memories
	// (no learning is driven from it yet).
	TypeMemoryFeedback = "memory_feedback"
)

// Event is one JSONL line. Fields beyond the envelope go into Data.
type Event struct {
	TS        string         `json:"ts"`
	SessionID int64          `json:"session_id"`
	UserID    int64          `json:"user_id"`
	Turn      int            `json:"turn"`
	Step      int            `json:"step"`
	Type      string         `json:"type"`
	Data      map[string]any `json:"data,omitempty"`
}

// Logger appends events to per-session JSONL files.
type Logger struct {
	dir       string
	enabled   bool
	logTokens bool
	redact    map[string]bool

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// New constructs a Logger.
func New(dir string, enabled, logTokens bool, redact []string) *Logger {
	r := make(map[string]bool, len(redact))
	for _, f := range redact {
		r[f] = true
	}
	return &Logger{
		dir:       dir,
		enabled:   enabled,
		logTokens: logTokens,
		redact:    r,
		locks:     map[string]*sync.Mutex{},
	}
}

// Enabled reports whether logging is on.
func (l *Logger) Enabled() bool { return l != nil && l.enabled }

// LogTokens reports whether token-level deltas should be logged.
func (l *Logger) LogTokens() bool { return l != nil && l.logTokens }

func (l *Logger) fileMutex(path string) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	m, ok := l.locks[path]
	if !ok {
		m = &sync.Mutex{}
		l.locks[path] = m
	}
	return m
}

func (l *Logger) path(userID, sessionID int64) string {
	return TrajectoryPath(l.dir, userID, sessionID)
}

// Log appends an event. Errors are swallowed (logging must not break the loop).
func (l *Logger) Log(ctx context.Context, ev Event) {
	if !l.Enabled() {
		return
	}
	ev.TS = time.Now().Format(time.RFC3339Nano)
	if len(l.redact) > 0 && ev.Data != nil {
		for k := range ev.Data {
			if l.redact[k] {
				ev.Data[k] = "[redacted]"
			}
		}
	}

	path := l.path(ev.UserID, ev.SessionID)
	m := l.fileMutex(path)
	m.Lock()
	defer m.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()

	line, err := json.Marshal(ev)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
}

// ReadAll returns the raw JSONL bytes for a session (for GET .../log).
func (l *Logger) ReadAll(userID, sessionID int64) ([]byte, error) {
	return os.ReadFile(l.path(userID, sessionID))
}

// SweepRetention deletes session files older than retentionDays (0 = keep all).
func (l *Logger) SweepRetention(retentionDays int) {
	if l == nil || retentionDays <= 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	_ = filepath.Walk(l.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
		return nil
	})
}
