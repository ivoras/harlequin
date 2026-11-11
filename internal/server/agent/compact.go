package agent

import (
	"fmt"
	"strings"

	"github.com/ivoras/harlequin/internal/server/llm"
)

const (
	// compactMinChars: tool results shorter than this are not worth eliding.
	compactMinChars = 600
	// elidedPrefix starts every stub.
	elidedPrefix = "[older tool result omitted to save context"
)

// compactStats describes what the newest request point of a replay did; Total
// counts every stubbed result in the returned prompt.
type compactStats struct {
	Elided, CharsFreed, TokensBefore, TokensAfter, Total int
}

// compactHistory is tier-1 context compaction: it returns msgs with the oldest
// large tool results replaced by short stubs. Only Content changes:
// ToolCallID/Name stay, so every tool call still has its result and providers
// accept the request. msgs is not modified (the result is a copy when anything
// was elided).
//
// It is a stateless replay of the whole conversation, not a check of the
// current size: it walks msgs and, at every point where a request was sent (just
// before each assistant message, and at the end), applies the rule "over
// threshold tokens -> stub the oldest results until at 60% of the threshold".
// Each step and each turn (which reloads the full results from the session
// store) therefore repeats the decisions of the ones before it and only ever
// adds stubs at the newest point, roughly once per 40% of the threshold of
// growth. That keeps the prompt prefix byte-stable for provider-side caching;
// re-deciding from the current size alone would move the cut-off every turn.
//
// At each point the newest keep tool results stay whole, and so do the results
// of the step that just ran: the model has not read those yet.
func compactHistory(msgs []llm.Message, tools []llm.Tool, threshold, keep int) ([]llm.Message, compactStats) {
	st := compactStats{}
	if threshold <= 0 {
		return msgs, st
	}
	if keep < 0 {
		keep = 0
	}
	target := threshold * 6 / 10
	est := llm.EstimateMessagesTokens(nil, tools)
	out := msgs
	var seen []int // tool messages so far, oldest first
	next := 0      // seen[:next] are already stubbed or too small to bother
	lastAsst := -1
	point := func() {
		st.Elided, st.CharsFreed, st.TokensBefore = 0, 0, est
		if est > threshold {
			limit := len(seen) - keep
			for limit > 0 && seen[limit-1] > lastAsst { // this step's results: unread
				limit--
			}
			for ; next < limit && est > target; next++ {
				m := out[seen[next]]
				if len(m.Content) < compactMinChars || strings.HasPrefix(m.Content, elidedPrefix) {
					continue
				}
				if st.Total == 0 {
					out = append([]llm.Message(nil), msgs...)
				}
				before, n := llm.EstimateMessageTokens(m), len(m.Content)
				m.Content = fmt.Sprintf("%s: %d characters. Call the tool again if you need it.]", elidedPrefix, n)
				out[seen[next]] = m
				est -= before - llm.EstimateMessageTokens(m)
				st.Elided++
				st.Total++
				st.CharsFreed += n - len(m.Content)
			}
		}
		st.TokensAfter = est
	}
	for i, m := range msgs {
		if m.Role == llm.RoleAssistant {
			point()
			lastAsst = i
		}
		est += llm.EstimateMessageTokens(m)
		if m.Role == llm.RoleTool {
			seen = append(seen, i)
		}
	}
	point()
	return out, st
}
