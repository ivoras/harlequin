package tui

import (
	"context"
	"fmt"
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
  /queue [del <n>|clear] messages typed while busy are queued; list/remove them
  /skills               list available skills
  /skill pull <name>    download a skill for local editing
  /skill push <name>    upload your edited skill as an override
  /skill reset <name>   remove your override (revert to server version)
  /skill diff <name>    show local edits vs the server version
  /skill new <name>     scaffold a new skill locally
  /hat                  list hats (a hat = system prompt + visible skills)
  /hat show <name>      show a hat's details
  /hat wear <name>      wear a hat in this session
  /hat off              remove the hat (use the default)
  /mcp                  list MCP servers (shared + your own) and status
  /mcp show <s/name>    show one MCP server (scope = shared|user)
  /mcp add <s/name> <url> [header Name:"Value" [Name2:"Value2" ...] | oauth]  register an MCP server
  /mcp test <s/name>    connect and list the server's tools
  /mcp auth <s/name>    authorize an OAuth MCP server (prints a URL to open)
  /mcp rm <s/name>      remove an MCP server
  /cron                 list scheduled jobs
  /cron show <id>       show a job (incl. last run output)
  /cron add "<name>" "<spec>" js "<target>" ["<input-json>"]   schedule a JS job
  /cron add "<name>" "<spec>" skill "<skill|->" "<prompt>"     schedule a skill job
  /cron on|off <id>     enable / disable a job
  /cron run <id>        run a job now
  /cron rm <id>         delete a job
  /config               list your per-user config
  /config set <k> <v>   set a config key (e.g. telegram.chat_id 12345)
  /config rm <key>      delete a config key
  /reload               (admin) re-read skill/prompt/hat files
  /memory [scope]       list memories with ids (scope: user|shared)
  /memory find <phrase> search memories (own + shared) by relevance
  /memory show <id>     show one memory
  /memory delete <id>…  delete one or more memories by id (shared if admin)
  /memory conflicts     list flagged duplicate/conflicting memory pairs
  /memory resolve <id>  mark a conflict flag as resolved
  /docs <query>         search organisation documents
  /docs add <path>      upload a local file (e.g. a PDF) into the corpus
  /resume [query]       pick a session to resume (optionally filter by title)
  /resume <id>          resume a specific session by id
  /dismiss [n|all]      dismiss an alert from the alert box (all by default)
  /run <n>              run the prompt carried by alert n
  /alert <message>      (owner/admin) broadcast an alert to all users
  /usage                show your token/cost usage
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
	case "/quit", "/exit":
		return tea.Quit
	case "/new":
		return func() tea.Msg {
			sess, err := m.client.CreateSession(context.Background(), "Session", m.currentHat)
			if err != nil {
				return errMsg{err}
			}
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
	case "/reload":
		if !m.canManageShared() {
			return infoCmd("/reload is owner/admin only")
		}
		return func() tea.Msg {
			if err := m.client.Reload(context.Background()); err != nil {
				return errMsg{err}
			}
			return infoMsg{"reloaded: skill, system prompt, and hat files will be re-read from disk"}
		}
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
	case "/docs":
		// "/docs add <path>" uploads a local file (e.g. a PDF); otherwise search.
		if len(args) >= 2 && args[0] == "add" {
			path := strings.TrimSpace(strings.Join(args[1:], " "))
			return func() tea.Msg {
				d, err := m.client.UploadDocument(context.Background(), path, "")
				if err != nil {
					return errMsg{err}
				}
				return infoMsg{fmt.Sprintf("uploaded %q (document id=%d) into the org corpus", d.Title, d.ID)}
			}
		}
		q := strings.Join(args, " ")
		return func() tea.Msg {
			res, err := m.client.SearchDocuments(context.Background(), q)
			if err != nil {
				return errMsg{err}
			}
			var sb strings.Builder
			sb.WriteString("Document results:\n")
			for _, r := range res {
				fmt.Fprintf(&sb, "  - %s\n", truncate(r.Content, 160))
			}
			return infoMsg{strings.TrimRight(sb.String(), "\n")}
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
	default:
		return infoCmd("usage: /hat [list|show <name>|wear <name>|off]")
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
	case "rm", "remove", "delete":
		if len(args) < 2 {
			return infoCmd("usage: /mcp rm <scope/name>")
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
		return infoCmd("usage: /mcp [list|show <s/name>|add ...|test <s/name>|auth <s/name>|rm <s/name>]")
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
		fmt.Fprintf(&sb, "  skills:      %s\n", strings.Join(h.Skills, ", "))
	} else {
		sb.WriteString("  skills:      (all)\n")
	}
	if h.SystemPrompt != "" {
		sb.WriteString("  prompt:      custom (overrides the default)")
	} else {
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
	case "del", "rm", "remove":
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
	case "delete", "rm", "del":
		if len(args) < 2 {
			return infoCmd("usage: /memory delete <id> [<id> ...]  (ids like u.7 or s.3 from /memory)")
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
		if scope != "user" && scope != "shared" {
			return infoCmd("usage: /memory [user|shared|find|show|delete|conflicts|resolve]")
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
		formatMemorySlotKey(mem.SlotKey), mem.Content)
}

// formatMemorySlotKey renders the slot key column for list lines; "{-}" when none.
func formatMemorySlotKey(key string) string {
	if key == "" {
		return "{-}"
	}
	return "{" + key + "}"
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
	fmt.Fprintf(&sb, "  slot:    %s", formatMemorySlotKey(mem.SlotKey))
	if mem.SlotKey != "" {
		fmt.Fprintf(&sb, " = %s", mem.SlotValue)
	}
	sb.WriteByte('\n')
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

func (m *Model) handleSkillSub(args []string) tea.Cmd {
	if len(args) < 2 {
		return infoCmd("usage: /skill <pull|push|reset|diff|new> <name>")
	}
	sub, name := strings.ToLower(args[0]), args[1]
	switch sub {
	case "pull":
		return func() tea.Msg {
			dir, err := m.skills.Pull(context.Background(), name)
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{"pulled " + name + " to " + dir}
		}
	case "push":
		return func() tea.Msg {
			if err := m.skills.Push(context.Background(), name); err != nil {
				return errMsg{err}
			}
			return infoMsg{"pushed " + name + " — server now uses your version"}
		}
	case "reset":
		return func() tea.Msg {
			if err := m.skills.Reset(context.Background(), name); err != nil {
				return errMsg{err}
			}
			return infoMsg{"reset " + name + " to the server version"}
		}
	case "new":
		return func() tea.Msg {
			dir, err := m.skills.Scaffold(name)
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{"scaffolded " + name + " at " + dir + " (edit, then /skill push " + name + ")"}
		}
	case "diff":
		return func() tea.Msg {
			return infoMsg{m.skillDiff(name)}
		}
	default:
		return infoCmd("unknown /skill subcommand: " + sub)
	}
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
