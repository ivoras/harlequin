package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ivoras/harlequin/internal/client/apiclient"
	"github.com/ivoras/harlequin/internal/shared/types"
)

const helpText = `Commands:
  /help                 show this help
  /new                  start a new conversation
  /skills               list available skills
  /skill pull <name>    download a skill for local editing
  /skill push <name>    upload your edited skill as an override
  /skill reset <name>   remove your override (revert to server version)
  /skill diff <name>    show local edits vs the server version
  /skill new <name>     scaffold a new skill locally
  /hat                  list hats (a hat = system prompt + visible skills)
  /hat show <name>      show a hat's details
  /hat wear <name>      wear a hat in this conversation
  /hat off              remove the hat (use the default)
  /reload               (admin) re-read skill/prompt/hat files
  /memory [scope]       list memories with ids (scope: user|shared)
  /memory find <phrase> search memories (own + shared) by relevance
  /memory show <id>     show one memory
  /memory delete <id>…  delete one or more memories by id (shared if admin)
  /memory conflicts     list flagged duplicate/conflicting memory pairs
  /memory resolve <id>  mark a conflict flag as resolved
  /docs <query>         search organisation documents
  /resume               list recent conversations
  /usage                show your token/cost usage
  /export [raw]         save transcript to session_YYYYMMDD_HHMM.md (cwd); raw includes thinking/tools, else User+Assistant only
  /quit                 exit`

func (m *Model) handleSlash(line string) tea.Cmd {
	fields := strings.Fields(line)
	cmd := strings.ToLower(fields[0])
	args := fields[1:]

	switch cmd {
	case "/help":
		return infoCmd(helpText)
	case "/export":
		raw := len(args) > 0 && strings.EqualFold(args[0], "raw")
		return func() tea.Msg {
			path, err := m.exportSession(raw)
			if err != nil {
				return errMsg{err}
			}
			what := "conversation"
			if raw {
				what = "full transcript"
			}
			return infoMsg{fmt.Sprintf("exported %s to %s", what, path)}
		}
	case "/quit", "/exit":
		return tea.Quit
	case "/new":
		return func() tea.Msg {
			conv, err := m.client.CreateConversation(context.Background(), "Session", m.currentHat)
			if err != nil {
				return errMsg{err}
			}
			m.conversationID = conv.ID
			m.blocks = nil
			m.appendConnectedStatus()
			if m.currentHat != "" {
				return infoMsg{"started a new conversation wearing the " + m.currentHat + " hat"}
			}
			return infoMsg{"started a new conversation"}
		}
	case "/hat":
		return m.handleHatSub(args)
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
		return func() tea.Msg {
			convos, err := m.client.ListConversations(context.Background(), strings.Join(args, " "))
			if err != nil {
				return errMsg{err}
			}
			var sb strings.Builder
			sb.WriteString("Conversations:\n")
			for _, c := range convos {
				fmt.Fprintf(&sb, "  #%d %s (%s)\n", c.ID, c.Title, c.UpdatedAt.Format("2006-01-02 15:04"))
			}
			return infoMsg{strings.TrimRight(sb.String(), "\n")}
		}
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
			if err := m.client.SetConversationHat(context.Background(), m.conversationID, name); err != nil {
				return errMsg{err}
			}
			m.currentHat = name
			return infoMsg{"now wearing the " + name + " hat in this conversation"}
		}
	case "off", "none", "remove":
		return func() tea.Msg {
			if err := m.client.SetConversationHat(context.Background(), m.conversationID, ""); err != nil {
				return errMsg{err}
			}
			m.currentHat = ""
			return infoMsg{"hat removed; using the default prompt and skills"}
		}
	default:
		return infoCmd("usage: /hat [list|show <name>|wear <name>|off]")
	}
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
