package agent

import (
	"fmt"
	"strings"

	"github.com/ivoras/harlequin/internal/server/llm"
)

const (
	// compactMinChars: tool results shorter than this are not worth eliding.
	compactMinChars = 600
	// elidedPrefix starts every stub, so a result is never elided twice.
	elidedPrefix = "[older tool result omitted to save context"
)

// compactStats describes one elision pass.
type compactStats struct {
	Elided, CharsFreed, TokensBefore, TokensAfter int
}

// elideOldToolResults is tier-1 context compaction: when the request would
// exceed threshold tokens, replace the contents of the oldest large tool results
// with a short stub until it fits in 60% of the threshold (hysteresis, so the
// next steps don't trigger it again at once). The newest keep tool results stay
// intact. Only Content changes: ToolCallID/Name stay, so every tool call still
// has its result and providers accept the request. The stub is deterministic
// (it embeds the original size), which keeps the prompt prefix stable across
// steps and turns for provider-side caching. msgs is not modified; the returned
// slice is a copy when anything was elided.
func elideOldToolResults(msgs []llm.Message, tools []llm.Tool, threshold, keep int) ([]llm.Message, compactStats) {
	st := compactStats{}
	if threshold <= 0 {
		return msgs, st
	}
	est := llm.EstimateMessagesTokens(msgs, tools)
	st.TokensBefore, st.TokensAfter = est, est
	if est <= threshold {
		return msgs, st
	}
	var tool []int // indexes of tool messages, oldest first
	for i, m := range msgs {
		if m.Role == llm.RoleTool {
			tool = append(tool, i)
		}
	}
	if keep < 0 {
		keep = 0
	}
	if len(tool) <= keep {
		return msgs, st
	}
	out := append([]llm.Message(nil), msgs...)
	target := threshold * 6 / 10
	for _, i := range tool[:len(tool)-keep] {
		m := out[i]
		if len(m.Content) < compactMinChars || strings.HasPrefix(m.Content, elidedPrefix) {
			continue
		}
		before := llm.EstimateMessageTokens(m)
		n := len(m.Content)
		m.Content = fmt.Sprintf("%s: %d characters. Call the tool again if you need it.]", elidedPrefix, n)
		out[i] = m
		est -= before - llm.EstimateMessageTokens(m)
		st.Elided++
		st.CharsFreed += n - len(m.Content)
		if est <= target {
			break
		}
	}
	if st.Elided == 0 {
		return msgs, st
	}
	st.TokensAfter = est
	return out, st
}
