package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ivoras/harlequin/internal/shared/types"
)

const cronUsage = `usage:
  /cron                              list jobs
  /cron show <id>                    show a job
  /cron add "<name>" "<spec>" js "<target>" ["<input-json>"] [notify=<channel>]
  /cron add "<name>" "<spec>" skill "<skill|->" "<prompt>" [notify=<channel>]
  /cron on <id> | /cron off <id>     enable / disable
  /cron run <id>                     run now
  /cron rm <id>                      delete
spec is a cron schedule: "min hour dom mon dow", @hourly/@daily, or "@every 30m".
js target is a script URI (skill://… storage://… tmp://…) or inline JS (ES5.1+).
notify=<channel> delivers change alerts via inapp (default), email, or telegram.`

func (m *Model) handleCronSub(args []string, raw string) tea.Cmd {
	if len(args) == 0 || strings.ToLower(args[0]) == "list" {
		return func() tea.Msg {
			jobs, err := m.client.ListCron(context.Background())
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{renderCronList(jobs)}
		}
	}
	switch strings.ToLower(args[0]) {
	case "show":
		id, ok := cronID(args, 1)
		if !ok {
			return infoCmd("usage: /cron show <id>")
		}
		return func() tea.Msg {
			job, err := m.client.GetCron(context.Background(), id)
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{renderCronDetail(*job)}
		}
	case "add":
		return m.handleCronAdd(raw)
	case "rm", "remove", "delete":
		id, ok := cronID(args, 1)
		if !ok {
			return infoCmd("usage: /cron rm <id>")
		}
		return func() tea.Msg {
			if err := m.client.DeleteCron(context.Background(), id); err != nil {
				return errMsg{err}
			}
			return infoMsg{fmt.Sprintf("deleted cron job #%d", id)}
		}
	case "on", "off":
		id, ok := cronID(args, 1)
		if !ok {
			return infoCmd("usage: /cron " + strings.ToLower(args[0]) + " <id>")
		}
		enabled := strings.ToLower(args[0]) == "on"
		return func() tea.Msg {
			job, err := m.client.UpdateCron(context.Background(), id, types.UpdateCronJobRequest{Enabled: &enabled})
			if err != nil {
				return errMsg{err}
			}
			state := "disabled"
			if job.Enabled {
				state = "enabled"
			}
			return infoMsg{fmt.Sprintf("cron job #%d %s", id, state)}
		}
	case "run":
		id, ok := cronID(args, 1)
		if !ok {
			return infoCmd("usage: /cron run <id>")
		}
		return func() tea.Msg {
			if err := m.client.RunCron(context.Background(), id); err != nil {
				return errMsg{err}
			}
			return infoMsg{fmt.Sprintf("cron job #%d started; check /cron show %d for the result", id, id)}
		}
	default:
		return infoCmd(cronUsage)
	}
}

// handleCronAdd parses the quoted positional form of /cron add.
func (m *Model) handleCronAdd(raw string) tea.Cmd {
	// Drop the leading "/cron add" and tokenize the rest, honouring quotes.
	rest := afterNFields(raw, 2)
	toks := splitQuoted(rest)
	// Pull out an optional notify=<channel> token (may appear anywhere).
	var channel string
	kept := toks[:0]
	for _, t := range toks {
		if low := strings.ToLower(t); strings.HasPrefix(low, "notify=") {
			channel = strings.TrimPrefix(low, "notify=")
			continue
		}
		kept = append(kept, t)
	}
	toks = kept
	if len(toks) < 4 {
		return infoCmd(cronUsage)
	}
	req := types.CreateCronJobRequest{
		Name:          toks[0],
		Spec:          toks[1],
		Kind:          strings.ToLower(toks[2]),
		NotifyChannel: channel,
	}
	switch req.Kind {
	case types.CronKindJS:
		req.Target = toks[3]
		if len(toks) > 4 {
			req.Input = toks[4]
		}
	case types.CronKindSkill:
		if toks[3] != "-" {
			req.Target = toks[3]
		}
		if len(toks) > 4 {
			req.Prompt = toks[4]
		}
	default:
		return infoCmd("kind must be 'js' or 'skill'\n" + cronUsage)
	}
	return func() tea.Msg {
		job, err := m.client.CreateCron(context.Background(), req)
		if err != nil {
			return errMsg{err}
		}
		return infoMsg{fmt.Sprintf("created cron job #%d %q (%s, %s); next run %s",
			job.ID, job.Name, job.Kind, job.Spec, cronTime(job.NextRunAt))}
	}
}

// cronID parses args[i] as a positive job id.
func cronID(args []string, i int) (int64, bool) {
	if len(args) <= i {
		return 0, false
	}
	id, err := strconv.ParseInt(args[i], 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// splitQuoted splits s on whitespace, treating double-quoted spans as one token.
func splitQuoted(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote, started := false, false
	flush := func() {
		if started {
			out = append(out, cur.String())
			cur.Reset()
			started = false
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			started = true
		case (r == ' ' || r == '\t') && !inQuote:
			flush()
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	flush()
	return out
}

func renderCronList(jobs []types.CronJob) string {
	if len(jobs) == 0 {
		return "(no cron jobs) — add one with /cron add"
	}
	var sb strings.Builder
	sb.WriteString("Cron jobs:\n")
	for _, j := range jobs {
		state := "enabled"
		if !j.Enabled {
			state = "disabled"
		}
		fmt.Fprintf(&sb, "  #%d %s [%s, %s, %s]  next %s", j.ID, j.Name, j.Kind, j.Spec, state, cronTime(j.NextRunAt))
		if j.LastStatus != "" {
			fmt.Fprintf(&sb, "  last %s @ %s", j.LastStatus, cronTime(j.LastRunAt))
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func renderCronDetail(j types.CronJob) string {
	state := "enabled"
	if !j.Enabled {
		state = "disabled"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "#%d %s (%s)\n", j.ID, j.Name, state)
	fmt.Fprintf(&sb, "  schedule: %s\n", j.Spec)
	fmt.Fprintf(&sb, "  kind:     %s\n", j.Kind)
	if j.Target != "" {
		fmt.Fprintf(&sb, "  target:   %s\n", j.Target)
	}
	if j.Prompt != "" {
		fmt.Fprintf(&sb, "  prompt:   %s\n", j.Prompt)
	}
	if j.Input != "" {
		fmt.Fprintf(&sb, "  input:    %s\n", j.Input)
	}
	channel := j.NotifyChannel
	if channel == "" {
		channel = "inapp"
	}
	fmt.Fprintf(&sb, "  notify:   %v (via %s)\n", j.Notify, channel)
	fmt.Fprintf(&sb, "  next run: %s\n", cronTime(j.NextRunAt))
	if j.LastRunAt != nil {
		fmt.Fprintf(&sb, "  last run: %s (%s)\n", cronTime(j.LastRunAt), j.LastStatus)
	}
	if j.LastOutput != "" {
		fmt.Fprintf(&sb, "  last output:\n%s", indentLines(truncate(j.LastOutput, 1000), "    "))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func cronTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format("2006-01-02 15:04")
}

func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
