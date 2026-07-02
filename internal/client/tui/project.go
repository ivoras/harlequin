package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/ivoras/harlequin/internal/client/apiclient"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// runProject handles the /project command and its subcommands. Project management
// (list/create/invite/accept/members/assign/sessions) plus switch/leave/depart
// (leave = deselect/back to personal; depart = remove your membership), which
// enter and exit the shared project context (and its chatroom side-pane).
func (m *Model) runProject(args []string) tea.Cmd {
	sub := "list"
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	rest := strings.TrimSpace(strings.Join(args[1:], " "))
	switch sub {
	case "list":
		return func() tea.Msg {
			ps, err := m.client.ListProjects(context.Background())
			if err != nil {
				return errMsg{err}
			}
			var sb strings.Builder
			sb.WriteString("Projects (use /project switch <id>):\n")
			for _, p := range ps {
				fmt.Fprintf(&sb, "  #%d %s\n", p.ID, p.Name)
			}
			if len(ps) == 0 {
				sb.WriteString("  (none — /project new <name>)\n")
			}
			return infoMsg{strings.TrimRight(sb.String(), "\n")}
		}
	case "new", "create":
		if rest == "" {
			return infoCmd("usage: /project new <name>")
		}
		return func() tea.Msg {
			p, err := m.client.CreateProject(context.Background(), rest)
			if err != nil {
				return errMsg{err}
			}
			return m.enterProject(p.ID, p.Name)
		}
	case "invites":
		return func() tea.Msg {
			inv, err := m.client.ListProjectInvites(context.Background())
			if err != nil {
				return errMsg{err}
			}
			var sb strings.Builder
			sb.WriteString("Invitations (use /project accept <id>):\n")
			for _, i := range inv {
				fmt.Fprintf(&sb, "  #%d %s — from %s\n", i.ID, i.ProjectName, i.InvitedBy)
			}
			if len(inv) == 0 {
				sb.WriteString("  (none)\n")
			}
			return infoMsg{strings.TrimRight(sb.String(), "\n")}
		}
	case "accept":
		id, err := strconv.ParseInt(rest, 10, 64)
		if err != nil {
			return infoCmd("usage: /project accept <invite-id>")
		}
		return func() tea.Msg {
			pid, err := m.client.AcceptInvite(context.Background(), id)
			if err != nil {
				return errMsg{err}
			}
			p, err := m.client.GetProject(context.Background(), pid)
			if err != nil {
				return errMsg{err}
			}
			return m.enterProject(p.ID, p.Name)
		}
	case "switch", "open":
		id, err := strconv.ParseInt(rest, 10, 64)
		if err != nil {
			return infoCmd("usage: /project switch <id>")
		}
		return func() tea.Msg {
			p, err := m.client.GetProject(context.Background(), id)
			if err != nil {
				return errMsg{err}
			}
			return m.enterProject(p.ID, p.Name)
		}
	case "leave":
		if m.activeProjectID == 0 {
			return infoCmd("not in a project")
		}
		m.leaveProject()
		return m.bootstrapChat()
	case "depart":
		// Remove the caller's membership. Targets <id> if given, else the active
		// project; deselects (back to personal) if it was the active one.
		id := m.activeProjectID
		if rest != "" {
			parsed, err := strconv.ParseInt(rest, 10, 64)
			if err != nil {
				return infoCmd("usage: /project depart [id]  (defaults to the active project)")
			}
			id = parsed
		}
		if id == 0 {
			return infoCmd("usage: /project depart <id>  (or switch to a project first)")
		}
		depart := func() tea.Msg {
			if err := m.client.DepartProject(context.Background(), id); err != nil {
				return errMsg{err}
			}
			return infoMsg{fmt.Sprintf("departed project #%d (membership removed)", id)}
		}
		if id == m.activeProjectID {
			m.leaveProject()
			return tea.Batch(m.bootstrapChat(), depart)
		}
		return depart
	case "invite":
		if m.activeProjectID == 0 {
			return infoCmd("switch to a project first: /project switch <id>")
		}
		if rest == "" {
			return infoCmd("usage: /project invite <email>")
		}
		pid := m.activeProjectID
		return func() tea.Msg {
			if err := m.client.InviteToProject(context.Background(), pid, rest); err != nil {
				return errMsg{err}
			}
			return infoMsg{"invited " + rest}
		}
	case "members":
		if m.activeProjectID == 0 {
			return infoCmd("not in a project")
		}
		pid := m.activeProjectID
		return func() tea.Msg {
			p, err := m.client.GetProject(context.Background(), pid)
			if err != nil {
				return errMsg{err}
			}
			var sb strings.Builder
			sb.WriteString("Members of " + p.Name + ":\n")
			for _, mem := range p.Members {
				sb.WriteString("  " + mem.Email + "\n")
			}
			return infoMsg{strings.TrimRight(sb.String(), "\n")}
		}
	case "assign":
		if m.activeProjectID == 0 {
			return infoCmd("switch to a project first")
		}
		pid, sid := m.activeProjectID, m.sessionID
		return func() tea.Msg {
			newID, err := m.client.AssignSession(context.Background(), pid, sid)
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{fmt.Sprintf("assigned session to the project (now #%d)", newID)}
		}
	case "sessions":
		if m.activeProjectID == 0 {
			return infoCmd("not in a project")
		}
		pid := m.activeProjectID
		return func() tea.Msg {
			ss, err := m.client.ListProjectSessions(context.Background(), pid)
			if err != nil {
				return errMsg{err}
			}
			var sb strings.Builder
			sb.WriteString("Project sessions:\n")
			for _, s := range ss {
				fmt.Fprintf(&sb, "  #%d %s\n", s.ID, s.Title)
			}
			return infoMsg{strings.TrimRight(sb.String(), "\n")}
		}
	default:
		return infoCmd("usage: /project [list|new <name>|invites|accept <id>|switch <id>|invite <email>|members|assign|sessions|leave]")
	}
}

// enterProject resolves a session to open inside the project (the first existing
// one, or a freshly assigned session) and returns a projectSwitchedMsg.
func (m *Model) enterProject(projectID int64, name string) tea.Msg {
	ctx := context.Background()
	ss, err := m.client.ListProjectSessions(ctx, projectID)
	if err != nil {
		return projectSwitchedMsg{err: err}
	}
	if len(ss) > 0 {
		return projectSwitchedMsg{id: projectID, name: name, sessionID: ss[0].ID}
	}
	// No sessions yet: create a personal one and assign it to the project.
	sess, err := m.client.CreateSession(ctx, "Session", "")
	if err != nil {
		return projectSwitchedMsg{err: err}
	}
	newID, err := m.client.AssignSession(ctx, projectID, sess.ID)
	if err != nil {
		return projectSwitchedMsg{err: err}
	}
	return projectSwitchedMsg{id: projectID, name: name, sessionID: newID}
}

// leaveProject clears the project context and closes the chatroom socket.
func (m *Model) leaveProject() {
	m.activeProjectID = 0
	m.activeProjectName = ""
	m.client.SetProject(0)
	if m.chat != nil {
		_ = m.chat.Close()
		m.chat = nil
	}
	m.chatMessages = nil
}

// openChatCmd opens the project chatroom socket and pumps messages into the program.
func (m *Model) openChatCmd(projectID int64) tea.Cmd {
	return func() tea.Msg {
		c, err := m.client.OpenProjectChat(context.Background(), projectID)
		return chatOpenedMsg{c: c, err: err}
	}
}

// pumpChat forwards chatroom messages from the socket to the program.
func (m *Model) pumpChat(c *apiclient.ProjectChat) {
	for ev := range c.Events() {
		if ev.Type == types.SSEChat && ev.Chat != nil && m.prog != nil {
			m.prog.Send(chatRecvMsg{m: *ev.Chat})
		}
	}
}

// sayToChat posts a message to the active project's chatroom (/say <message>).
func (m *Model) sayToChat(text string) tea.Cmd {
	if m.activeProjectID == 0 || m.chat == nil {
		return infoCmd("not in a project chatroom")
	}
	if strings.TrimSpace(text) == "" {
		return infoCmd("usage: /say <message>")
	}
	_ = m.chat.Post(text)
	return nil
}
