package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// maxIdenticalCalls stops a turn when one tool call has returned the same
// result this many times: some models resend a call verbatim for dozens of
// steps (seen: one 3 KB run_js sent 25+ times), each step re-reading the whole
// growing context.
const maxIdenticalCalls = 4

// repeatGuard counts, per turn, tool calls that repeat an earlier call with the
// same arguments and got the same result. A repeat with a different result
// (the sandbox or a document changed in between) is not counted.
type repeatGuard map[[32]byte]int

// check records one finished call. note is appended to the result the model
// sees; stop reports that the turn should end.
func (g repeatGuard) check(name, rawArgs, result string) (note string, stop bool) {
	args := parseToolArgs(rawArgs)
	canon, _ := json.Marshal(args) // map keys sort, so key order and spacing don't matter
	key := sha256.Sum256([]byte(name + "\x00" + string(canon) + "\x00" + result))
	g[key]++
	n := g[key]
	if n < 2 {
		return "", false
	}
	if n >= maxIdenticalCalls {
		return "", true
	}
	return fmt.Sprintf("\n\n[Harlequin notice] You already made this exact %s call in this turn and it returned exactly this result (%d times now). Calling it again will not change anything. Use the result you have, change the call (different code or arguments), or answer the user. After %d identical calls the turn is stopped.", name, n, maxIdenticalCalls), false
}
