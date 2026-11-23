package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

// maxIdenticalCalls stops a turn when one tool call has returned the same
// result this many times: some models resend a call verbatim for dozens of
// steps (seen: one 3 KB run_js sent 25+ times), each step re-reading the whole
// growing context.
const maxIdenticalCalls = 4

// Stale calls: the call differs every time, but its output is almost all text
// the turn has already seen (seen: run_js re-slicing one string with offsets
// shifted by one, 90 steps, until max_steps). Paging through a document yields
// new lines each call, so it is not stale.
const (
	staleNoticeAt  = 3   // stale calls before the model gets a notice
	maxStaleCalls  = 8   // stale calls that end the turn
	staleMinBytes  = 200 // shorter outputs ("ok", "saved") repeat legitimately
	staleSeenRatio = 0.9 // share of an output's bytes already seen that makes it stale
	maxCorpus      = 2 << 20
)

// repeatGuard watches one turn's tool calls for loops. The zero value is ready.
type repeatGuard struct {
	identical map[[32]byte]int // call+result hash → times seen
	lines     map[string]bool  // every output line seen this turn
	corpus    strings.Builder  // the same lines, for fragments cut out of them
	stale     int              // stale calls since the last one that brought new text
}

// check records one finished call. note is appended to the result the model
// sees; a non-empty stop is the error that ends the turn.
func (g *repeatGuard) check(name, rawArgs, result string) (note, stop string) {
	if g.identical == nil {
		g.identical, g.lines = map[[32]byte]int{}, map[string]bool{}
	}
	args := parseToolArgs(rawArgs)
	canon, _ := json.Marshal(args) // map keys sort, so key order and spacing don't matter
	key := sha256.Sum256([]byte(name + "\x00" + string(canon) + "\x00" + result))
	g.identical[key]++
	if n := g.identical[key]; n >= maxIdenticalCalls {
		return "", fmt.Sprintf("the assistant kept repeating the same %s call with the same result and was stopped; ask again, or ask it to try a different approach", name)
	} else if n >= 2 {
		return fmt.Sprintf("\n\n[Harlequin notice] You already made this exact %s call in this turn and it returned exactly this result (%d times now). Calling it again will not change anything. Use the result you have, change the call (different code or arguments), or answer the user. After %d identical calls the turn is stopped.", name, n, maxIdenticalCalls), ""
	}

	if !g.seenMostly(result) {
		if len(result) >= staleMinBytes {
			g.stale = 0 // new material: the model is getting somewhere
		}
		return "", ""
	}
	g.stale++
	if g.stale >= maxStaleCalls {
		return "", fmt.Sprintf("the assistant kept making calls that returned only output it had already seen (last: %s) and was stopped; ask again, or ask it to try a different approach", name)
	}
	if g.stale >= staleNoticeAt {
		return fmt.Sprintf("\n\n[Harlequin notice] This %s output is almost entirely text you already saw earlier in this turn (%d such calls now). Re-reading it in other slices will not reveal anything new. Work with what you have, try a genuinely different approach, or tell the user what you cannot do. After %d such calls the turn is stopped.", name, g.stale, maxStaleCalls), ""
	}
	return "", ""
}

// seenMostly reports whether result is long enough to judge and nearly all of
// its bytes are text seen in earlier outputs — whole lines, or fragments of
// them (a slice boundary cuts lines mid-way) — then records result's lines.
func (g *repeatGuard) seenMostly(result string) bool {
	var total, seen int
	var fresh []string
	corpus := g.corpus.String()
	for _, l := range strings.Split(result, "\n") {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		total += len(l)
		switch {
		case g.lines[l]:
			seen += len(l)
		case strings.Contains(corpus, l):
			seen += len(l)
		default:
			fresh = append(fresh, l)
		}
	}
	for _, l := range fresh {
		g.lines[l] = true
		if g.corpus.Len() < maxCorpus { // shortcut: later text is only matched as whole lines; fine unless turns read >2 MB
			g.corpus.WriteString(l)
			g.corpus.WriteByte('\n')
		}
	}
	return total >= staleMinBytes && float64(seen) >= staleSeenRatio*float64(total)
}
