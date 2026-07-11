package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ivoras/harlequin/internal/client/apiclient"
	"github.com/ivoras/harlequin/internal/shared/types"
)

const helpText = `Commands:
  /help                 show this help
  /new                  start a new session
  /clear                clear this session's messages (fresh context; keeps session, title, hat)
  /queue [del <n>|clear] messages typed while busy are queued; list/remove them
  /skills               list available skills
  /skill create <name> <description>   create a new skill (scope: --user|--shared|--project)
  /skill edit <name> [file]            edit a skill file in the built-in editor
  /skill download <name> [file]        download a skill (or one file) for local editing
  /skill upload <name> [file]          upload a skill (or one file); scope flag as above
  /skill del <name>                    delete the skill from its scope (scope flag as above)
  /skill diff <name>                   show local edits vs the server version
  /hat                  list hats (a hat = overlay skills + system prompt)
  /hat show <name>      show a hat's details (visible + overlay skills)
  /hat wear <name>      wear a hat in this session
  /hat off              remove the hat (use the default)
  /hat create <name> [description]     (admin) create a hat
  /hat edit <name> [file]              (admin) edit a hat file (default system_prompt.md)
  /hat files <name>     list a hat's files (prompt + skill overlays)
  /hat addskill <hat> <skill>          (admin) copy the resolved skill into the hat's overlay
  /hat prompt <name> on|off            (admin) toggle the hat's custom prompt (content is kept)
  /hat rmskill <hat> <skill>           (admin) remove a skill overlay from the hat
  /hat del <name>       (admin) delete a hat
  /mcp                  list MCP servers (shared + your own) and status
  /mcp show <s/name>    show one MCP server (scope = shared|user)
  /mcp add <s/name> <url> [header Name:"Value" [Name2:"Value2" ...] | oauth]  register an MCP server
  /mcp test <s/name>    connect and list the server's tools
  /mcp auth <s/name>    authorize an OAuth MCP server (prints a URL to open)
  /mcp del <s/name>     remove an MCP server
  /cron                 list scheduled jobs
  /cron show <id>       show a job (incl. last run output)
  /cron add "<name>" "<spec>" js "<target>" ["<input-json>"]   schedule a JS job
  /cron add "<name>" "<spec>" skill "<skill|->" "<prompt>"     schedule a skill job
  /cron on|off <id>     enable / disable a job
  /cron run <id>        run a job now
  /cron del <id>        delete a job
  /config               list your per-user config
  /config set <k> <v>   set a config key (e.g. telegram.chat_id 12345)
  /config del <key>     delete a config key
  /memory [scope]       list memories with ids (scope: user|shared)
  /memory find <phrase> search memories (own + shared) by relevance
  /memory show <id>     show one memory
  /memory del <id>…     delete one or more memories by id (shared if admin)
  /memory conflicts     list flagged duplicate/conflicting memory pairs
  /memory resolve <id>  mark a conflict flag as resolved
  /docs search <query>     search documents (personal + shared, + project if active)
  /docs list               list documents across scopes
  /docs del <scope> <id>   delete a document
  /docs add [scope] <path> upload a .txt/.md/.html/.pdf for RAG (same as /upload)
  /upload [scope] <path>   upload a doc into personal|shared|project (default personal)
  /resume [query]       pick a session to resume (optionally filter by title)
  /resume <id>          resume a specific session by id
  /dismiss [n ...|all]  dismiss alert(s) by number, or all (default)
  /run <n>              run the prompt carried by alert n
  /alert <message>      (owner/admin) broadcast an alert to all users
  /usage                show your token/cost usage
  /context              show context window usage breakdown
  /export [raw]         save transcript to session_YYYYMMDD_HHMM.md (cwd); raw includes thinking/tools, else User+Assistant only
  /quit                 exit`

func (m *Model) handleSlash(line string) tea.Cmd {
	// Echo the command into the transcript (bold command word, args in the
	// user-prompt colour) so it's visible alongside its result. Both Enter paths
	// in update.go funnel here, so this is the single place to do it.
	m.appendBlock("command", line)
	fields := strings.Fields(line)
	cmd := strings.ToLower(fields[0])
	args := fields[1:]

	switch cmd {
	case "/help":
		return infoCmd(helpText)
	case "/project":
		return m.runProject(args)
	case "/say":
		return m.sayToChat(strings.Join(args, " "))
	case "/dismiss":
		return m.dismissAlert(args)
	case "/run":
		return m.runAlert(args)
	case "/alert":
		if m.user == nil || !types.IsElevated(m.user.Role) {
			return infoCmd("/alert is for owners and admins only")
		}
		msg := strings.TrimSpace(strings.Join(args, " "))
		if msg == "" {
			return infoCmd("usage: /alert <message>")
		}
		return func() tea.Msg {
			if err := m.client.BroadcastAlert(context.Background(), msg); err != nil {
				return errMsg{err}
			}
			return infoMsg{"alert sent to all users"}
		}
	case "/export":
		raw := len(args) > 0 && strings.EqualFold(args[0], "raw")
		return func() tea.Msg {
			path, err := m.exportSession(raw)
			if err != nil {
				return errMsg{err}
			}
			what := "session"
			if raw {
				what = "full transcript"
			}
			return infoMsg{fmt.Sprintf("exported %s to %s", what, path)}
		}
	case "/clear":
		if m.loading {
			return infoCmd("a turn is in flight — wait for it to finish (or Esc to interrupt) before /clear")
		}
		sessionID, projectID := m.sessionID, m.activeProjectID
		return func() tea.Msg {
			if err := m.client.ClearSession(context.Background(), sessionID, projectID); err != nil {
				return errMsg{err}
			}
			return sessionClearedMsg{}
		}
	case "/quit", "/exit":
		return tea.Quit
	case "/new":
		return func() tea.Msg {
			sess, err := m.client.CreateSession(context.Background(), "Session", m.currentHat)
			if err != nil {
				return errMsg{err}
			}
			m.leaveProject() // a new personal session leaves any project context
			m.switchSession(sess.ID)
			m.sessTitle = ""
			m.blocks = nil
			m.appendConnectedStatus()
			if m.currentHat != "" {
				return infoMsg{"started a new session wearing the " + m.currentHat + " hat"}
			}
			return infoMsg{"started a new session"}
		}
	case "/hat":
		return m.handleHatSub(args)
	case "/mcp":
		return m.handleMCPSub(args, line)
	case "/cron":
		return m.handleCronSub(args, line)
	case "/config":
		return m.handleConfigSub(args)
	case "/queue":
		return m.handleQueueSub(args)
	case "/skills":
		return func() tea.Msg {
			infos, err := m.client.ListSkills(context.Background())
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{renderSkillList(infos)}
		}
	case "/skill":
		return m.handleSkillSub(args)
	case "/memory":
		return m.handleMemorySub(args)
	case "/upload":
		return m.handleUpload(args)
	case "/docs":
		// Subcommands (no implicit search):
		//   search|q <query>     search documents
		//   list|ls              list documents (across scopes)
		//   del <scope> <id>     delete a document
		//   add [scope] <path>   upload (== /upload)
		sub := ""
		if len(args) > 0 {
			sub = strings.ToLower(args[0])
		}
		switch sub {
		case "add":
			return m.handleUpload(args[1:])
		case "search", "q":
			return m.handleDocsSearch(strings.Join(args[1:], " "))
		case "list", "ls":
			return m.handleDocsList()
		case "del":
			return m.handleDocsDelete(args[1:])
		case "view":
			return m.handleDocsView(args[1:])
		default:
			return infoCmd("usage: /docs <search|q> <query> | list | del <scope> <id> | view <scope> <id> | add [scope] <path>")
		}
	case "/resume":
		// "/resume <id>" jumps straight to that session; "/resume [query]" opens the
		// interactive picker (optionally filtered by a title substring).
		if len(args) == 1 {
			if id, err := strconv.ParseInt(args[0], 10, 64); err == nil {
				return m.resumeSession(id)
			}
		}
		return m.resumeListCmd(strings.Join(args, " "))
	case "/usage":
		return func() tea.Msg {
			rows, err := m.client.Usage(context.Background())
			if err != nil {
				return errMsg{err}
			}
			var total float64
			var pt, ct int
			for _, r := range rows {
				total += r.EstCostUSD
				pt += r.PromptTokens
				ct += r.CompletionTokens
			}
			return infoMsg{fmt.Sprintf("Usage: %d records, %d prompt + %d completion tokens, est $%.4f", len(rows), pt, ct, total)}
		}
	case "/context":
		return func() tea.Msg {
			bd, err := m.client.ContextBreakdown(context.Background(), m.sessionID, m.ctxMeter.model)
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{formatContextBreakdown(bd)}
		}
	default:
		return infoCmd("unknown command: " + cmd + " (try /help)")
	}
}

func (m *Model) handleHatSub(args []string) tea.Cmd {
	if len(args) == 0 || strings.ToLower(args[0]) == "list" {
		return func() tea.Msg {
			hats, err := m.client.ListHats(context.Background())
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{renderHatList(hats, m.currentHat)}
		}
	}
	switch strings.ToLower(args[0]) {
	case "show":
		if len(args) < 2 {
			return infoCmd("usage: /hat show <name>")
		}
		name := args[1]
		return func() tea.Msg {
			h, err := m.client.GetHat(context.Background(), name)
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{renderHatDetail(*h)}
		}
	case "wear", "use":
		if len(args) < 2 {
			return infoCmd("usage: /hat wear <name>")
		}
		name := args[1]
		return func() tea.Msg {
			if err := m.client.SetSessionHat(context.Background(), m.sessionID, name); err != nil {
				return errMsg{err}
			}
			m.currentHat = name
			return infoMsg{"now wearing the " + name + " hat in this session"}
		}
	case "off", "none", "remove":
		return func() tea.Msg {
			if err := m.client.SetSessionHat(context.Background(), m.sessionID, ""); err != nil {
				return errMsg{err}
			}
			m.currentHat = ""
			return infoMsg{"hat removed; using the default prompt and skills"}
		}
	case "create":
		if !m.canManageShared() {
			return infoCmd("/hat create is owner/admin only")
		}
		if len(args) < 2 {
			return infoCmd("usage: /hat create <name> [description…]")
		}
		name, desc := args[1], strings.Join(args[2:], " ")
		return func() tea.Msg {
			if err := m.client.CreateHat(context.Background(), name, desc); err != nil {
				return errMsg{err}
			}
			return infoMsg{"created hat " + name + " — /hat edit " + name + " to set its prompt, /hat addskill " + name + " <skill> to overlay skills"}
		}
	case "edit":
		if !m.canManageShared() {
			return infoCmd("/hat edit is owner/admin only")
		}
		if len(args) < 2 {
			return infoCmd("usage: /hat edit <name> [file]  (default file: system_prompt.md; overlay files are skills/<skill>/…)")
		}
		name, relpath := args[1], "system_prompt.md"
		if len(args) > 2 {
			relpath = args[2]
		}
		return m.openHatEditor(name, relpath)
	case "files":
		if len(args) < 2 {
			return infoCmd("usage: /hat files <name>")
		}
		name := args[1]
		return func() tea.Msg {
			files, err := m.client.GetHatFiles(context.Background(), name)
			if err != nil {
				return errMsg{err}
			}
			rels := make([]string, 0, len(files))
			for rel := range files {
				rels = append(rels, rel)
			}
			sort.Strings(rels)
			return infoMsg{"Files of hat " + name + ":\n  " + strings.Join(rels, "\n  ")}
		}
	case "addskill":
		if !m.canManageShared() {
			return infoCmd("/hat addskill is owner/admin only")
		}
		if len(args) < 3 {
			return infoCmd("usage: /hat addskill <hat> <skill>  (copies the resolved skill into the hat's overlay)")
		}
		hat, skill := args[1], args[2]
		return func() tea.Msg {
			if err := m.client.AddHatSkill(context.Background(), hat, skill); err != nil {
				return errMsg{err}
			}
			return infoMsg{"copied " + skill + " into hat " + hat + " — edit it with /hat edit " + hat + " skills/" + skill + "/SKILL.md"}
		}
	case "rmskill":
		if !m.canManageShared() {
			return infoCmd("/hat rmskill is owner/admin only")
		}
		if len(args) < 3 {
			return infoCmd("usage: /hat rmskill <hat> <skill>")
		}
		hat, skill := args[1], args[2]
		return func() tea.Msg {
			if err := m.client.RemoveHatSkill(context.Background(), hat, skill); err != nil {
				return errMsg{err}
			}
			return infoMsg{"removed the " + skill + " overlay from hat " + hat}
		}
	case "prompt":
		if !m.canManageShared() {
			return infoCmd("/hat prompt is owner/admin only")
		}
		if len(args) < 3 || (args[2] != "on" && args[2] != "off") {
			return infoCmd("usage: /hat prompt <name> on|off  (off keeps the custom prompt's content but uses the default)")
		}
		name, enabled := args[1], args[2] == "on"
		return func() tea.Msg {
			if err := m.client.SetHatPromptEnabled(context.Background(), name, enabled); err != nil {
				return errMsg{err}
			}
			if enabled {
				return infoMsg{"hat " + name + ": custom system prompt enabled"}
			}
			return infoMsg{"hat " + name + ": custom system prompt disabled (content kept; default prompt in use)"}
		}
	case "del":
		if !m.canManageShared() {
			return infoCmd("/hat del is owner/admin only")
		}
		if len(args) < 2 {
			return infoCmd("usage: /hat del <name>")
		}
		name := args[1]
		return func() tea.Msg {
			if err := m.client.DeleteHat(context.Background(), name); err != nil {
				return errMsg{err}
			}
			return infoMsg{"deleted hat " + name}
		}
	default:
		return infoCmd("usage: /hat [list|show <name>|wear <name>|off|create|edit|files|addskill|rmskill|prompt|del]")
	}
}

// parseMCPRef parses "scope/name" or bare "name" (default scope "user").
func parseMCPRef(s string) (scope, name string) {
	if i := strings.Index(s, "/"); i >= 0 {
		scope, name = strings.ToLower(s[:i]), s[i+1:]
		if scope == "shared" || scope == "user" {
			return scope, name
		}
	}
	return "user", s
}

func (m *Model) handleMCPSub(args []string, raw string) tea.Cmd {
	if len(args) == 0 || strings.ToLower(args[0]) == "list" {
		return func() tea.Msg {
			servers, err := m.client.ListMCP(context.Background())
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{renderMCPList(servers)}
		}
	}
	switch strings.ToLower(args[0]) {
	case "show":
		if len(args) < 2 {
			return infoCmd("usage: /mcp show <scope/name>")
		}
		scope, name := parseMCPRef(args[1])
		return func() tea.Msg {
			srv, err := m.client.GetMCP(context.Background(), scope, name)
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{renderMCPDetail(*srv)}
		}
	case "add":
		return m.handleMCPAdd(args[1:], raw)
	case "del":
		if len(args) < 2 {
			return infoCmd("usage: /mcp del <scope/name>")
		}
		scope, name := parseMCPRef(args[1])
		return func() tea.Msg {
			if err := m.client.DeleteMCP(context.Background(), scope, name); err != nil {
				return errMsg{err}
			}
			return infoMsg{fmt.Sprintf("removed MCP server %s/%s", scope, name)}
		}
	case "test":
		if len(args) < 2 {
			return infoCmd("usage: /mcp test <scope/name>")
		}
		scope, name := parseMCPRef(args[1])
		return func() tea.Msg {
			res, err := m.client.TestMCP(context.Background(), scope, name)
			if err != nil {
				return errMsg{err}
			}
			if !res.OK {
				return errMsg{fmt.Errorf("connection failed: %s", res.Error)}
			}
			if len(res.Tools) == 0 {
				return infoMsg{fmt.Sprintf("%s/%s connected; no tools advertised", scope, name)}
			}
			return infoMsg{fmt.Sprintf("%s/%s connected; tools: %s", scope, name, strings.Join(res.Tools, ", "))}
		}
	case "auth":
		if len(args) < 2 {
			return infoCmd("usage: /mcp auth <scope/name>")
		}
		scope, name := parseMCPRef(args[1])
		return func() tea.Msg {
			res, err := m.client.StartMCPOAuth(context.Background(), scope, name)
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{"Open this URL in a browser to authorize, then run /mcp test " + scope + "/" + name + ":\n" + res.AuthorizeURL}
		}
	default:
		return infoCmd("usage: /mcp [list|show <s/name>|add ...|test <s/name>|auth <s/name>|del <s/name>]")
	}
}

// handleMCPAdd parses: <scope/name> <url> [header Name:"Value" [Name2:"Value2" ...] | oauth [scope...]]
func (m *Model) handleMCPAdd(args []string, raw string) tea.Cmd {
	const headerUsage = `usage: /mcp add <scope/name> <url> header Name:"Value" [Name2:"Value2" ...]`
	if len(args) < 2 {
		return infoCmd(`usage: /mcp add <scope/name> <url> [header Name:"Value" ... | oauth]`)
	}
	scope, name := parseMCPRef(args[0])
	req := types.RegisterMCPRequest{Scope: scope, Name: name, URL: args[1], AuthType: "none"}
	rest := args[2:]
	if len(rest) > 0 {
		switch strings.ToLower(rest[0]) {
		case "header":
			// Header values may contain spaces inside quotes, which the field
			// splitter loses, so parse the headers from the raw command line
			// (everything after "/mcp add <ref> <url> header").
			headers, err := parseHeaderSpecs(afterNFields(raw, 5))
			if err != nil {
				return infoCmd(headerUsage + " — " + err.Error())
			}
			req.AuthType = "header"
			req.Headers = headers
		case "oauth":
			req.AuthType = "oauth"
			req.OAuthScopes = rest[1:]
		default:
			return infoCmd(`unknown auth mode; use 'header Name:"Value" ...' or 'oauth'`)
		}
	}
	return func() tea.Msg {
		if err := m.client.RegisterMCP(context.Background(), req); err != nil {
			return errMsg{err}
		}
		msg := fmt.Sprintf("registered MCP server %s/%s", scope, name)
		if req.AuthType == "oauth" {
			msg += "; run /mcp auth " + scope + "/" + name + " to authorize"
		} else {
			msg += "; run /mcp test " + scope + "/" + name + " to verify"
		}
		return infoMsg{msg}
	}
}

// afterNFields returns the remainder of s after skipping n whitespace-separated
// fields, preserving the original spacing/quoting of that remainder.
func afterNFields(s string, n int) string {
	for i := 0; i < n; i++ {
		s = strings.TrimLeft(s, " \t")
		j := strings.IndexAny(s, " \t")
		if j < 0 {
			return ""
		}
		s = s[j:]
	}
	return strings.TrimSpace(s)
}

// parseHeaderSpecs parses one or more `Name:"Value"` header specs (values are
// quoted so they may contain spaces).
func parseHeaderSpecs(s string) ([]types.MCPHeader, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("no headers given")
	}
	var out []types.MCPHeader
	for len(s) > 0 {
		colon := strings.IndexByte(s, ':')
		if colon < 0 {
			return nil, fmt.Errorf(`expected Name:"Value"`)
		}
		name := strings.TrimSpace(s[:colon])
		if name == "" {
			return nil, fmt.Errorf("empty header name")
		}
		rest := strings.TrimLeft(s[colon+1:], " \t")
		if len(rest) == 0 || rest[0] != '"' {
			return nil, fmt.Errorf("value for %q must be quoted", name)
		}
		end := strings.IndexByte(rest[1:], '"')
		if end < 0 {
			return nil, fmt.Errorf("unterminated quote for %q", name)
		}
		out = append(out, types.MCPHeader{Name: name, Value: rest[1 : 1+end]})
		s = strings.TrimSpace(rest[1+end+1:])
	}
	return out, nil
}

func renderMCPList(servers []types.MCPServer) string {
	if len(servers) == 0 {
		return "No MCP servers registered. Add one with /mcp add <scope/name> <url>."
	}
	var sb strings.Builder
	sb.WriteString("MCP servers (scope/name — auth — status):\n")
	for _, s := range servers {
		fmt.Fprintf(&sb, " %s/%-16s %-7s %s\n", s.Scope, s.Name, s.AuthType, mcpStatusText(s))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func mcpStatusText(s types.MCPServer) string {
	switch {
	case !s.Enabled:
		return "disabled"
	case s.NeedsAuth:
		return "needs auth (run /mcp auth)"
	case s.Error != "":
		return "error: " + s.Error
	case s.AuthSatisfied:
		return fmt.Sprintf("ready (%d tools)", s.ToolCount)
	default:
		return "no credential"
	}
}

func renderMCPDetail(s types.MCPServer) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "MCP server %s/%s\n", s.Scope, s.Name)
	fmt.Fprintf(&sb, "  url:    %s\n", s.URL)
	fmt.Fprintf(&sb, "  auth:   %s", s.AuthType)
	if len(s.HeaderNames) > 0 {
		fmt.Fprintf(&sb, " (headers: %s)", strings.Join(s.HeaderNames, ", "))
	}
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "  status: %s", mcpStatusText(s))
	if len(s.Tools) > 0 {
		sb.WriteString("\n  tools:")
		for _, t := range s.Tools {
			fmt.Fprintf(&sb, "\n    - %s", t.Name)
			if t.Description != "" {
				fmt.Fprintf(&sb, ": %s", firstLine(t.Description))
			}
		}
	}
	return sb.String()
}

// firstLine returns the first non-empty line of a (possibly multi-line) tool
// description, trimmed, so the /mcp show listing stays compact.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func renderHatList(hats []types.Hat, current string) string {
	if len(hats) == 0 {
		return "No hats defined."
	}
	var sb strings.Builder
	sb.WriteString("Hats (use /hat wear <name>; * = current):\n")
	for _, h := range hats {
		mark := " "
		if h.Name == current {
			mark = "*"
		}
		fmt.Fprintf(&sb, " %s%-16s %s\n", mark, h.Name, h.Description)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func renderHatDetail(h types.Hat) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Hat %s\n", h.Name)
	if h.Description != "" {
		fmt.Fprintf(&sb, "  description: %s\n", h.Description)
	}
	if len(h.Skills) > 0 {
		fmt.Fprintf(&sb, "  visible:     %s\n", strings.Join(h.Skills, ", "))
	} else {
		sb.WriteString("  visible:     (all skills)\n")
	}
	if len(h.OverlaySkills) > 0 {
		fmt.Fprintf(&sb, "  overlays:    %s\n", strings.Join(h.OverlaySkills, ", "))
	}
	switch {
	case h.HasCustomPrompt && h.PromptDisabled:
		sb.WriteString("  prompt:      custom, DISABLED (default in use; /hat prompt " + h.Name + " on)")
	case h.HasCustomPrompt:
		sb.WriteString("  prompt:      custom (overrides the default)")
	default:
		sb.WriteString("  prompt:      (default)")
	}
	return sb.String()
}

// handleQueueSub lists or edits the pending message queue (messages typed while
// a turn was in flight). Mutates state inline + refreshes rather than going
// through infoMsg, which would clear the in-flight loading state.
func (m *Model) handleQueueSub(args []string) tea.Cmd {
	report := func(s string) tea.Cmd {
		m.appendBlock("info", s)
		m.refreshViewport()
		return nil
	}
	if len(args) == 0 {
		if len(m.msgQueue) == 0 {
			return report("queue is empty")
		}
		var sb strings.Builder
		sb.WriteString("Queued messages (/queue del <n> to remove):")
		for i, q := range m.msgQueue {
			fmt.Fprintf(&sb, "\n  %d. %s", i+1, q)
		}
		return report(sb.String())
	}
	switch strings.ToLower(args[0]) {
	case "clear":
		n := len(m.msgQueue)
		m.msgQueue = nil
		return report(fmt.Sprintf("cleared %d queued message(s)", n))
	case "del":
		if len(args) < 2 {
			return report("usage: /queue del <n>")
		}
		idx, err := strconv.Atoi(args[1])
		if err != nil || idx < 1 || idx > len(m.msgQueue) {
			return report(fmt.Sprintf("no queued message #%s", args[1]))
		}
		removed := m.msgQueue[idx-1]
		m.msgQueue = append(m.msgQueue[:idx-1], m.msgQueue[idx:]...)
		return report("removed: " + truncate(removed, 60))
	}
	return report("usage: /queue [del <n>|clear]")
}

// handleDocsSearch runs a document search and renders ranked, scope-labelled
// results with their source document and a clean excerpt.
func (m *Model) handleDocsSearch(q string) tea.Cmd {
	q = strings.TrimSpace(q)
	if q == "" {
		return infoCmd("usage: /docs search <query>")
	}
	return func() tea.Msg {
		res, err := m.client.SearchDocuments(context.Background(), q)
		if err != nil {
			return errMsg{err}
		}
		if len(res) == 0 {
			return infoMsg{fmt.Sprintf("No document results for %q.", q)}
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "Document results for %q (%d):\n", q, len(res))
		for i, r := range res {
			src := r.Source
			if src == "" {
				src = "document"
			}
			// Full, untruncated chunk text (whitespace collapsed so it reads as a
			// clean passage and the terminal can wrap it).
			fmt.Fprintf(&sb, "%d. [%s] %s\n   %s\n", i+1, r.Scope, src, collapseWS(r.Content))
		}
		return infoMsg{strings.TrimRight(sb.String(), "\n")}
	}
}

// handleDocsList lists documents in personal + shared (+ the active project).
func (m *Model) handleDocsList() tea.Cmd {
	pid := m.activeProjectID
	return func() tea.Msg {
		docs, err := m.client.ListDocuments(context.Background(), pid)
		if err != nil {
			return errMsg{err}
		}
		if len(docs) == 0 {
			return infoMsg{"No documents. Upload one with /upload or /docs add."}
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "Documents (%d) — delete with /docs delete <scope> <id>:\n", len(docs))
		for _, d := range docs {
			name := d.Title
			if name == "" {
				name = d.OriginalName
			}
			fmt.Fprintf(&sb, "  [%s] #%d  %s  (%s, %d chunks)\n", d.Scope, d.ID, name, d.Mime, d.Chunks)
		if d.Description != "" {
			fmt.Fprintf(&sb, "        %s\n", d.Description)
		}
		}
		return infoMsg{strings.TrimRight(sb.String(), "\n")}
	}
}

// handleDocsDelete deletes a document: "/docs delete <scope> <id>". project
// scope uses the active project.
func (m *Model) handleDocsDelete(args []string) tea.Cmd {
	if len(args) < 2 {
		return infoCmd("usage: /docs del <personal|shared|project> <id>")
	}
	scope := strings.ToLower(args[0])
	switch scope {
	case "personal", "shared", "project":
	default:
		return func() tea.Msg { return errMsg{fmt.Errorf("scope must be personal, shared, or project")} }
	}
	id, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return func() tea.Msg { return errMsg{fmt.Errorf("invalid document id %q", args[1])} }
	}
	var projectID int64
	if scope == "project" {
		if m.activeProjectID == 0 {
			return func() tea.Msg { return errMsg{fmt.Errorf("no active project; switch to one with /project switch first")} }
		}
		projectID = m.activeProjectID
	}
	return func() tea.Msg {
		if err := m.client.DeleteDocument(context.Background(), id, scope, projectID); err != nil {
			return errMsg{err}
		}
		return infoMsg{fmt.Sprintf("deleted %s document #%d", scope, id)}
	}
}

// scopeLetters maps a document ref's scope letter (as shown throughout the
// UI and in [d.x.N] citations, e.g. "p.19") to the /docs command's scope word.
var scopeLetters = map[string]string{"u": "personal", "s": "shared", "p": "project"}

// parseDocRef splits a leading "<scope>.<id>" token (e.g. "p.19", matching how
// documents are referenced everywhere else — citations, align_docs, search
// results) into its scope word and id, consuming one argument instead of two.
func parseDocRef(tok string) (scope string, id int64, ok bool) {
	letter, idStr, found := strings.Cut(tok, ".")
	if !found {
		return "", 0, false
	}
	scope, ok = scopeLetters[strings.ToLower(letter)]
	if !ok {
		return "", 0, false
	}
	n, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return scope, n, true
}

// handleDocsView shows a document: "/docs view <scope> <id> [text|file]", or
// the shorter "/docs view <p|u|s>.<id> [text|file]" using the same scoped-ref
// shorthand as citations and search results (e.g. "/docs view p.19"). A
// TXT-type document (plain text or markdown, no stored original file — this
// includes every save_doc report) is fetched and printed inline immediately,
// no prompt needed. A PDF/DOCX (has a stored original file) is NOT
// auto-fetched — the terminal can't render either format, so instead of
// guessing which the user wants, this asks: "text" prints the extracted text
// already sitting in the corpus from ingest-time conversion (Docling/PDFium/
// docxextract — no new conversion needed, it already happened); "file" saves
// the original for opening with an external viewer. Re-run the command with
// that keyword to proceed. project scope uses the active project.
func (m *Model) handleDocsView(args []string) tea.Cmd {
	const usage = "usage: /docs view <personal|shared|project> <id> [text|file]  (or /docs view p.19 [text|file])"
	if len(args) < 1 {
		return infoCmd(usage)
	}
	var scope string
	var id int64
	rest := args[1:]
	if s, n, ok := parseDocRef(args[0]); ok {
		scope, id = s, n
	} else {
		if len(args) < 2 {
			return infoCmd(usage)
		}
		scope = strings.ToLower(args[0])
		switch scope {
		case "personal", "shared", "project":
		default:
			return func() tea.Msg { return errMsg{fmt.Errorf("scope must be personal, shared, or project")} }
		}
		parsed, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return func() tea.Msg { return errMsg{fmt.Errorf("invalid document id %q", args[1])} }
		}
		id = parsed
		rest = args[2:]
	}
	mode := ""
	if len(rest) >= 1 {
		mode = strings.ToLower(rest[0])
		if mode != "text" && mode != "file" {
			return infoCmd(fmt.Sprintf("usage: /docs view %s %d [text|file]", scope, id))
		}
	}
	var projectID int64
	if scope == "project" {
		if m.activeProjectID == 0 {
			return func() tea.Msg { return errMsg{fmt.Errorf("no active project; switch to one with /project switch first")} }
		}
		projectID = m.activeProjectID
	}
	return func() tea.Msg {
		ctx := context.Background()

		showText := func() tea.Msg {
			content, cerr := m.client.GetDocumentContent(ctx, id, scope, projectID)
			if cerr != nil {
				return errMsg{fmt.Errorf("document not found or has no content: %w", cerr)}
			}
			return infoMsg{content}
		}
		saveFile := func() tea.Msg {
			data, contentType, ferr := m.client.DownloadDocumentFile(ctx, id, scope, projectID)
			if ferr != nil {
				return errMsg{fmt.Errorf("no stored file for this document: %w", ferr)}
			}
			ext := ".bin"
			switch {
			case strings.Contains(contentType, "pdf"):
				ext = ".pdf"
			case strings.Contains(contentType, "wordprocessingml"):
				ext = ".docx"
			}
			f, werr := os.CreateTemp("", fmt.Sprintf("harlequin-doc-%d-*%s", id, ext))
			if werr != nil {
				return errMsg{werr}
			}
			defer f.Close()
			if _, werr := f.Write(data); werr != nil {
				return errMsg{werr}
			}
			return infoMsg{fmt.Sprintf("saved to %s — open it with your system's PDF/DOCX viewer", f.Name())}
		}

		switch mode {
		case "text":
			return showText()
		case "file":
			return saveFile()
		}

		// No mode given: TXT-type documents (no stored file) show directly;
		// file-backed documents (PDF/DOCX) prompt instead of guessing.
		docs, lerr := m.client.ListDocuments(ctx, projectID)
		if lerr != nil {
			return errMsg{lerr}
		}
		for _, d := range docs {
			if d.ID != id || d.Scope != scope {
				continue
			}
			if d.StoredPath == "" {
				return showText()
			}
			return infoMsg{fmt.Sprintf(
				"%q is a %s document. View it as: /docs view %s %d text (the extracted text already in the corpus) or /docs view %s %d file (save the original to open externally)",
				d.Title, d.Mime, scope, id, scope, id)}
		}
		return errMsg{fmt.Errorf("document %s #%d not found", scope, id)}
	}
}

// handleUpload implements "/upload [personal|shared|project] <path>" (and the
// equivalent "/docs add …"): uploads a .txt/.md/.html/.pdf file into the chosen
// corpus for RAG. With no explicit scope it defaults to the active project (when
// you've switched to one), otherwise personal. The server enforces permissions;
// the client pre-checks for a clearer message.
func (m *Model) handleUpload(args []string) tea.Cmd {
	usage := "usage: /upload [personal|shared|project] <path>  (defaults to the active project, else personal; formats: .txt .md .html .pdf)"
	if len(args) == 0 {
		return func() tea.Msg { return infoMsg{usage} }
	}
	// Optional leading scope. With none, follow the working context: the active
	// project if you've switched to one, otherwise your personal corpus.
	scope := "personal"
	if m.activeProjectID != 0 {
		scope = "project"
	}
	rest := args
	switch strings.ToLower(args[0]) {
	case "personal", "shared", "project":
		scope = strings.ToLower(args[0])
		rest = args[1:]
	}
	if len(rest) == 0 {
		return func() tea.Msg { return infoMsg{usage} }
	}
	path := strings.TrimSpace(strings.Join(rest, " "))
	switch strings.ToLower(filepath.Ext(path)) {
	case ".txt", ".md", ".markdown", ".html", ".htm", ".pdf":
	default:
		return func() tea.Msg {
			return errMsg{fmt.Errorf("unsupported format %q (use .txt, .md, .html, or .pdf)", filepath.Ext(path))}
		}
	}
	var projectID int64
	switch scope {
	case "shared":
		if m.user == nil || !types.IsElevated(m.user.Role) {
			return func() tea.Msg { return errMsg{fmt.Errorf("only owners/admins can upload shared documents")} }
		}
	case "project":
		if m.activeProjectID == 0 {
			return func() tea.Msg {
				return errMsg{fmt.Errorf("no active project; switch to one with /project switch first")}
			}
		}
		projectID = m.activeProjectID
	}
	dest := scope + " corpus"
	if scope == "project" {
		dest = fmt.Sprintf("project %q", m.activeProjectName)
	}
	// Immediate feedback: ingestion (extract → chunk → embed) is synchronous and
	// can take a while for big PDFs, so show a status line before it starts.
	m.appendBlock("status", fmt.Sprintf("uploading %q into the %s — extracting, chunking and embedding (this can take a while)…", filepath.Base(path), dest))
	return func() tea.Msg {
		d, err := m.client.UploadDocument(context.Background(), path, "", scope, projectID)
		if err != nil {
			return errMsg{err}
		}
		return infoMsg{fmt.Sprintf("ingested %q into the %s for RAG: %d chunks (document id=%d)", d.Title, dest, d.Chunks, d.ID)}
	}
}

func (m *Model) handleMemorySub(args []string) tea.Cmd {
	if len(args) == 0 {
		return func() tea.Msg {
			mems, err := m.client.ListMemory(context.Background(), "")
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{renderMemoryList(mems, m.canManageShared())}
		}
	}
	switch strings.ToLower(args[0]) {
	case "find", "search":
		if len(args) < 2 {
			return infoCmd("usage: /memory find <phrase>")
		}
		q := strings.Join(args[1:], " ")
		return func() tea.Msg {
			mems, err := m.client.FindMemory(context.Background(), q)
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{renderMemoryList(mems, m.canManageShared())}
		}
	case "del":
		if len(args) < 2 {
			return infoCmd("usage: /memory del <id> [<id> ...]  (ids like u.7 or s.3 from /memory)")
		}
		ids := args[1:]
		return func() tea.Msg {
			return deleteMemoriesMsg(m.client, ids)
		}
	case "show", "get":
		if len(args) < 2 {
			return infoCmd("usage: /memory show <id>")
		}
		id := args[1]
		return func() tea.Msg {
			mem, err := m.client.GetMemory(context.Background(), id)
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{renderMemoryDetail(*mem, m.canManageShared())}
		}
	case "conflicts", "conflict":
		return func() tea.Msg {
			conflicts, err := m.client.ListMemoryConflicts(context.Background())
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{renderMemoryConflicts(conflicts)}
		}
	case "resolve":
		if len(args) < 2 {
			return infoCmd("usage: /memory resolve <conflict-id>")
		}
		id := args[1]
		return func() tea.Msg {
			if err := m.client.ResolveMemoryConflict(context.Background(), id); err != nil {
				return errMsg{err}
			}
			return infoMsg{fmt.Sprintf("resolved conflict %s", id)}
		}
	default:
		scope := args[0]
		if scope == "project" {
			pid := m.activeProjectID
			if pid == 0 {
				return infoCmd("no active project — switch with /project use <name> first")
			}
			return func() tea.Msg {
				mems, err := m.client.ListProjectMemory(context.Background(), pid)
				if err != nil {
					return errMsg{err}
				}
				return infoMsg{renderMemoryList(mems, m.canManageShared())}
			}
		}
		if scope != "user" && scope != "shared" {
			return infoCmd("usage: /memory [user|shared|project|find|show|del|conflicts|resolve]")
		}
		return func() tea.Msg {
			mems, err := m.client.ListMemory(context.Background(), scope)
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{renderMemoryList(mems, m.canManageShared())}
		}
	}
}

func (m *Model) canManageShared() bool {
	return m.user != nil && types.IsElevated(m.user.Role)
}

// deleteMemoriesMsg deletes each id via the API and reports successes and failures.
func deleteMemoriesMsg(client *apiclient.Client, ids []string) tea.Msg {
	ctx := context.Background()
	var ok, failed []string
	for _, id := range ids {
		if err := client.DeleteMemory(ctx, id); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		ok = append(ok, id)
	}
	switch {
	case len(failed) == 0 && len(ok) == 1:
		return infoMsg{fmt.Sprintf("deleted memory %s", ok[0])}
	case len(failed) == 0:
		return infoMsg{fmt.Sprintf("deleted %d memories: %s", len(ok), strings.Join(ok, ", "))}
	case len(ok) == 0:
		return errMsg{fmt.Errorf("%s", strings.Join(failed, "; "))}
	default:
		return infoMsg{fmt.Sprintf("deleted %s; failed: %s", strings.Join(ok, ", "), strings.Join(failed, "; "))}
	}
}

func renderMemoryList(mems []types.Memory, canManageShared bool) string {
	if len(mems) == 0 {
		return "No memories."
	}
	var sb strings.Builder
	sb.WriteString("Memories (use /memory show <id> or /memory delete <id>… when deletable):\n")
	for _, mem := range mems {
		sb.WriteString(renderMemoryLine(mem, canManageShared))
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

func memoryDeletable(mem types.Memory, canManageShared bool) bool {
	if mem.Scope == "user" {
		return true
	}
	return canManageShared && mem.Scope == "shared"
}

func renderMemoryLine(mem types.Memory, canManageShared bool) string {
	pin := " "
	if mem.Pinned {
		pin = "*"
	}
	deletable := ""
	if memoryDeletable(mem, canManageShared) {
		deletable = " (deletable)"
	}
	return fmt.Sprintf(" %s%-6s %s [%s/%s]%s %s %s",
		pin, mem.ID, formatMemoryTime(mem.CreatedAt), mem.Scope, mem.Source, deletable,
		formatMemorySlotKeys(mem.Slots), mem.Content)
}

// formatMemorySlotKeys renders the slot-key column for list lines as
// "{key1, key2}", or "{-}" when the memory carries no slots.
func formatMemorySlotKeys(slots []types.MemorySlot) string {
	if len(slots) == 0 {
		return "{-}"
	}
	keys := make([]string, len(slots))
	for i, sl := range slots {
		keys[i] = sl.Key
	}
	return "{" + strings.Join(keys, ", ") + "}"
}

// formatMemoryTime formats a memory timestamp as ISO 8601 UTC to minute precision.
func formatMemoryTime(t time.Time) string {
	if t.IsZero() {
		return "????-??-??T??:??Z"
	}
	return t.UTC().Format("2006-01-02T15:04") + "Z"
}

func renderMemoryDetail(mem types.Memory, canManageShared bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Memory %s\n", mem.ID)
	fmt.Fprintf(&sb, "  scope:   %s\n", mem.Scope)
	fmt.Fprintf(&sb, "  source:  %s\n", mem.Source)
	fmt.Fprintf(&sb, "  pinned:  %t\n", mem.Pinned)
	if mem.ExpiresAt != nil {
		fmt.Fprintf(&sb, "  expires: %s\n", formatMemoryTime(*mem.ExpiresAt))
	}
	fmt.Fprintf(&sb, "  created: %s\n", formatMemoryTime(mem.CreatedAt))
	if len(mem.Slots) == 0 {
		fmt.Fprintf(&sb, "  slots:   {-}\n")
	} else {
		for i, sl := range mem.Slots {
			label := "  slots:  "
			if i > 0 {
				label = "          "
			}
			fmt.Fprintf(&sb, "%s {%s} = %s\n", label, sl.Key, sl.Value)
		}
	}
	fmt.Fprintf(&sb, "  content: %s", mem.Content)
	if memoryDeletable(mem, canManageShared) {
		sb.WriteString("\n  (delete with /memory delete " + mem.ID + ")")
	}
	return sb.String()
}

func renderMemoryConflicts(conflicts []types.MemoryConflict) string {
	if len(conflicts) == 0 {
		return "No flagged memory conflicts."
	}
	var sb strings.Builder
	sb.WriteString("Memory conflicts (use /memory resolve <id> after fixing):\n")
	for _, c := range conflicts {
		fmt.Fprintf(&sb, " !%-6s [%s conf=%d] %s vs %s\n", c.ID, c.Relationship, c.Confidence, c.MemoryA, c.MemoryB)
		fmt.Fprintf(&sb, "      A: %s\n", truncate(c.ContentA, 120))
		fmt.Fprintf(&sb, "      B: %s\n", truncate(c.ContentB, 120))
		if c.Reason != "" {
			fmt.Fprintf(&sb, "      → %s\n", c.Reason)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// skillScopeFlag extracts a --user/--shared/--project flag from args, returning
// the remaining positional args and the scope ("" if unset).
func skillScopeFlag(args []string) ([]string, string) {
	var rest []string
	scope := ""
	for _, a := range args {
		switch strings.ToLower(a) {
		case "--user", "-u":
			scope = "user"
		case "--shared", "-s":
			scope = "shared"
		case "--project", "-p":
			scope = "project"
		default:
			rest = append(rest, a)
		}
	}
	return rest, scope
}

func (m *Model) handleSkillSub(args []string) tea.Cmd {
	args, scope := skillScopeFlag(args)
	if len(args) < 1 {
		return infoCmd("usage: /skill <create|edit|download|upload|del|diff> <name> [file] [--user|--shared|--project]")
	}
	sub := strings.ToLower(args[0])
	if len(args) < 2 {
		return infoCmd("usage: /skill " + sub + " <name> ...")
	}
	name := args[1]
	switch sub {
	case "create":
		description := strings.Join(args[2:], " ")
		return func() tea.Msg {
			dir, err := m.skills.Create(context.Background(), name, description, scope)
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{"created " + name + scopeLabel(scope) + "; pulled to " + dir + " (edit, then /skill upload " + name + ")"}
		}
	case "edit":
		relpath := "SKILL.md"
		if len(args) > 2 {
			relpath = args[2]
		}
		return m.openSkillEditor(name, relpath, scope)
	case "download", "pull":
		if len(args) > 2 {
			relpath := args[2]
			return func() tea.Msg {
				dest, err := m.skills.PullFile(context.Background(), name, relpath)
				if err != nil {
					return errMsg{err}
				}
				return infoMsg{"downloaded " + name + "/" + relpath + " to " + dest}
			}
		}
		return func() tea.Msg {
			dir, err := m.skills.Pull(context.Background(), name)
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{"downloaded " + name + " to " + dir}
		}
	case "upload", "push":
		if len(args) > 2 {
			relpath := args[2]
			return func() tea.Msg {
				if err := m.skills.PushFile(context.Background(), name, relpath, scope); err != nil {
					return errMsg{err}
				}
				return infoMsg{"uploaded " + name + "/" + relpath + scopeLabel(scope)}
			}
		}
		return func() tea.Msg {
			if err := m.skills.Push(context.Background(), name, scope); err != nil {
				return errMsg{err}
			}
			return infoMsg{"uploaded " + name + scopeLabel(scope)}
		}
	case "del":
		return func() tea.Msg {
			if err := m.skills.Reset(context.Background(), name, scope); err != nil {
				return errMsg{err}
			}
			return infoMsg{"deleted " + name + scopeLabel(scope)}
		}
	case "diff":
		return func() tea.Msg {
			return infoMsg{m.skillDiff(name)}
		}
	default:
		return infoCmd("unknown /skill subcommand: " + sub)
	}
}

// scopeLabel renders a " (scope)" suffix, or "" when the default scope applies.
func scopeLabel(scope string) string {
	if scope == "" {
		return ""
	}
	return " (" + scope + ")"
}

// skillDiff compares local files against the server's effective version.
func (m *Model) skillDiff(name string) string {
	local, err := m.skills.LocalFiles(name)
	if err != nil {
		return "no local copy; run /skill pull " + name
	}
	server, err := m.client.GetSkill(context.Background(), name)
	if err != nil {
		return err.Error()
	}
	var sb strings.Builder
	sb.WriteString("Diff for " + name + " (local vs server):\n")
	names := map[string]bool{}
	for k := range local {
		names[k] = true
	}
	for k := range server.Files {
		names[k] = true
	}
	var keys []string
	for k := range names {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		l, hasL := local[k]
		s, hasS := server.Files[k]
		switch {
		case hasL && !hasS:
			fmt.Fprintf(&sb, "  + %s (local only)\n", k)
		case !hasL && hasS:
			fmt.Fprintf(&sb, "  - %s (server only)\n", k)
		case l != s:
			fmt.Fprintf(&sb, "  ~ %s (differs)\n", k)
		default:
			fmt.Fprintf(&sb, "    %s (same)\n", k)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
