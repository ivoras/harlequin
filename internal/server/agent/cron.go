package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/shared/types"
)

// CronStore is the subset of the cron store the agent uses for its cron_* tools.
// Defined here (consumer-side) so the agent does not import the cron package,
// avoiding an import cycle with the scheduler.
type CronStore interface {
	Create(ctx context.Context, db *sql.DB, req types.CreateCronJobRequest) (types.CronJob, error)
	List(ctx context.Context, db *sql.DB) ([]types.CronJob, error)
	Update(ctx context.Context, db *sql.DB, id int64, req types.UpdateCronJobRequest) (types.CronJob, error)
	Reschedule(ctx context.Context, db *sql.DB, id int64, next time.Time) error
	Delete(ctx context.Context, db *sql.DB, id int64) error
}

// RunCronJS executes a cron job's JavaScript with the same sandbox as the run_js
// tool (dom, fetch, tmp/storage, load/include). job.Target is either a script
// URI (skill:// / storage:// / tmp://) or an inline body; job.Input (a JSON
// object) is exposed to the script as the global `args`. No LLM is involved.
func (a *Agent) RunCronJS(ctx context.Context, userID int64, username string, userDB *sql.DB, job types.CronJob) (string, error) {
	rc := &runContext{userID: userID, username: username, userDB: userDB}
	rcx := a.jsRunContext(ctx, rc)
	args, err := parseJSONObject(job.Input)
	if err != nil {
		return "", fmt.Errorf("invalid input JSON: %w", err)
	}
	if args != nil {
		rcx.Globals = map[string]any{"args": args}
	}
	code := strings.TrimSpace(job.Target)
	if isScriptURI(code) {
		src, err := rcx.Resolve(code)
		if err != nil {
			return "", err
		}
		code = src
	}
	res, runErr := a.Runner.Run(code, rcx)
	out := res.Output
	if res.Value != nil {
		if b, err := json.Marshal(res.Value); err == nil {
			out += "\nresult: " + string(b)
		}
	}
	if runErr != nil {
		return strings.TrimRight(out, "\n"), runErr
	}
	return strings.TrimRight(out, "\n"), nil
}

// RunCronSkill runs one headless agent turn for a cron job in a fresh conversation
// (so per-run context does not accumulate). job.Target optionally names a skill to
// bias toward; job.Prompt is the message; job.Input is appended as context. Returns
// the agent's final text.
func (a *Agent) RunCronSkill(ctx context.Context, userID int64, username, role string, userDB *sql.DB, job types.CronJob) (string, error) {
	conv, err := a.Conversations.Create(ctx, userDB, userID, "cron: "+job.Name, "", types.APICron, types.InterfaceCron)
	if err != nil {
		return "", err
	}
	rc := &runContext{
		conversationID: conv.ID,
		userID:         userID,
		username:       username,
		canShareMemory: types.IsElevated(role),
		userDB:         userDB,
		api:            types.APICron,
		iface:          types.InterfaceCron,
		turn:           1,
		emit:           func(types.StreamEvent) {}, // headless: discard streamed events
	}
	content := strings.TrimSpace(job.Prompt)
	if skill := strings.TrimSpace(job.Target); skill != "" {
		content = "Use the " + skill + " skill if relevant.\n\n" + content
	}
	if in := strings.TrimSpace(job.Input); in != "" {
		content += "\n\nInputs (JSON): " + in
	}
	return a.turn(ctx, rc, content)
}

// isScriptURI reports whether s references a script via a sandbox URI scheme.
func isScriptURI(s string) bool {
	return strings.HasPrefix(s, "skill://") || strings.HasPrefix(s, "storage://") || strings.HasPrefix(s, "tmp://")
}

// parseJSONObject decodes a JSON object string into a map (nil for empty input).
func parseJSONObject(s string) (map[string]any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return m, nil
}
