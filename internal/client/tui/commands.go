package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
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
  /memory [scope]       list memories with ids (scope: user|shared)
  /memory show <id>     show one memory
  /memory delete <id>   delete your user memory (shared too if admin)
  /memory conflicts     list flagged duplicate/conflicting memory pairs
  /memory resolve <id>  mark a conflict flag as resolved
  /docs <query>         search organisation documents
  /resume               list recent conversations
  /usage                show your token/cost usage
  /quit                 exit`

func (m *Model) handleSlash(line string) tea.Cmd {
	fields := strings.Fields(line)
	cmd := strings.ToLower(fields[0])
	args := fields[1:]

	switch cmd {
	case "/help":
		return infoCmd(helpText)
	case "/quit", "/exit":
		return tea.Quit
	case "/new":
		return func() tea.Msg {
			conv, err := m.client.CreateConversation(context.Background(), "Session")
			if err != nil {
				return errMsg{err}
			}
			m.conversationID = conv.ID
			m.blocks = nil
			return infoMsg{"started a new conversation"}
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

func (m *Model) handleMemorySub(args []string) tea.Cmd {
	if len(args) == 0 {
		return func() tea.Msg {
			mems, err := m.client.ListMemory(context.Background(), "")
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{renderMemoryList(mems, m.isAdmin())}
		}
	}
	switch strings.ToLower(args[0]) {
	case "delete", "rm", "del":
		if len(args) < 2 {
			return infoCmd("usage: /memory delete <id>  (id like u.7 or s.3 from /memory)")
		}
		id := args[1]
		return func() tea.Msg {
			if err := m.client.DeleteMemory(context.Background(), id); err != nil {
				return errMsg{err}
			}
			return infoMsg{fmt.Sprintf("deleted memory %s", id)}
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
			return infoMsg{renderMemoryDetail(*mem, m.isAdmin())}
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
			return infoCmd("usage: /memory [user|shared|show|delete|conflicts|resolve]")
		}
		return func() tea.Msg {
			mems, err := m.client.ListMemory(context.Background(), scope)
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{renderMemoryList(mems, m.isAdmin())}
		}
	}
}

func (m *Model) isAdmin() bool {
	return m.user != nil && m.user.Role == "admin"
}

func renderMemoryList(mems []types.Memory, isAdmin bool) string {
	if len(mems) == 0 {
		return "No memories."
	}
	var sb strings.Builder
	sb.WriteString("Memories (use /memory show <id> or /memory delete <id> when deletable):\n")
	for _, mem := range mems {
		sb.WriteString(renderMemoryLine(mem, isAdmin))
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

func memoryDeletable(mem types.Memory, isAdmin bool) bool {
	if mem.Scope == "user" {
		return true
	}
	return isAdmin && mem.Scope == "shared"
}

func renderMemoryLine(mem types.Memory, isAdmin bool) string {
	pin := " "
	if mem.Pinned {
		pin = "*"
	}
	deletable := ""
	if memoryDeletable(mem, isAdmin) {
		deletable = " (deletable)"
	}
	return fmt.Sprintf(" %s%-6s %s [%s/%s]%s %s",
		pin, mem.ID, formatMemoryTime(mem.CreatedAt), mem.Scope, mem.Source, deletable, mem.Content)
}

// formatMemoryTime formats a memory timestamp as ISO 8601 UTC to minute precision.
func formatMemoryTime(t time.Time) string {
	if t.IsZero() {
		return "????-??-??T??:??Z"
	}
	return t.UTC().Format("2006-01-02T15:04") + "Z"
}

func renderMemoryDetail(mem types.Memory, isAdmin bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Memory %s\n", mem.ID)
	fmt.Fprintf(&sb, "  scope:   %s\n", mem.Scope)
	fmt.Fprintf(&sb, "  source:  %s\n", mem.Source)
	fmt.Fprintf(&sb, "  pinned:  %t\n", mem.Pinned)
	if mem.ExpiresAt != nil {
		fmt.Fprintf(&sb, "  expires: %s\n", formatMemoryTime(*mem.ExpiresAt))
	}
	fmt.Fprintf(&sb, "  created: %s\n", formatMemoryTime(mem.CreatedAt))
	fmt.Fprintf(&sb, "  content: %s", mem.Content)
	if memoryDeletable(mem, isAdmin) {
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
