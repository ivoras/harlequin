// Package tui implements the Harlequin Bubble Tea client: a Claude Code-like
// chat UI with a teal/apricot theme (truecolor with 256/16-color fallback), live streaming, a tool-call
// timeline, and slash-commands.
package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ivoras/harlequin/internal/client/apiclient"
	clientcfg "github.com/ivoras/harlequin/internal/client/config"
	clientskills "github.com/ivoras/harlequin/internal/client/skills"
	"github.com/ivoras/harlequin/internal/shared/types"
)

type phase int

const (
	phaseLoginUser phase = iota
	phaseLoginPass
	phaseChat
)

// roleBlock is a rendered transcript entry.
type roleBlock struct {
	role string // user | assistant | thinking | tool | error | info | status
	text string
}

// Model is the root TUI model.
type Model struct {
	cfg    *clientcfg.Config
	client *apiclient.Client
	skills *clientskills.Manager
	prog   *tea.Program

	styles Styles

	phase phase
	width int
	height int

	vp      viewport.Model
	input   textarea.Model
	spin    spinner.Model
	loading bool

	// login scratch
	loginUser string

	blocks    []roleBlock
	streamingThinking strings.Builder // in-flight reasoning text
	streaming         strings.Builder // in-flight assistant response text

	conversationID int64
	currentHat     string // hat worn by new conversations / the active one
	slashSel       int    // highlighted item in the slash-command autocomplete menu
	user           *types.User
	ctxMeter       contextMeterState

	// Submitted input lines for up/down recall (messages and slash commands).
	inputHistory []string
	historyIndex int    // len(inputHistory) when editing a fresh line
	historyDraft string // draft saved when browsing history

	// cancel for the in-flight stream (Esc).
	cancelStream context.CancelFunc
}

// New constructs the TUI model.
func New(cfg *clientcfg.Config) *Model {
	client := apiclient.New(cfg.ServerURL, cfg.Token)
	ta := textarea.New()
	ta.Placeholder = "Type a message, or /help for commands"
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = 8
	ta.CharLimit = 0
	// The textarea defaults to a thick-bar prompt ("┃ ") and visible line
	// numbers; drop both (the bordered box already frames the input) and use a
	// clean themed chevron.
	ta.ShowLineNumbers = false
	ta.Prompt = "› "
	tastyles := ta.Styles()
	tastyles.Focused.Prompt = lipgloss.NewStyle().Foreground(colorAccent)
	tastyles.Focused.Text = lipgloss.NewStyle().Foreground(colorText)
	tastyles.Focused.Placeholder = lipgloss.NewStyle().Foreground(colorMuted)
	tastyles.Blurred.Prompt = lipgloss.NewStyle().Foreground(colorMuted)
	tastyles.Blurred.Text = lipgloss.NewStyle().Foreground(colorMuted)
	tastyles.Cursor.Color = colorAccent
	ta.SetStyles(tastyles)

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	m := &Model{
		cfg:    cfg,
		client: client,
		skills: clientskills.NewManager(client, cfg.ExpandedSkillsDir()),
		styles: newStyles(),
		input:  ta,
		spin:   sp,
		vp:     viewport.New(),
	}
	if cfg.Token != "" {
		m.phase = phaseChat
	} else {
		m.phase = phaseLoginUser
	}
	return m
}

// SetProgram stores the program pointer for out-of-band streaming sends.
func (m *Model) SetProgram(p *tea.Program) { m.prog = p }

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spin.Tick}
	if m.phase == phaseChat {
		cmds = append(cmds, m.bootstrapChat())
	} else {
		m.input.Placeholder = "Enter username"
		cmds = append(cmds, m.input.Focus())
	}
	return tea.Batch(cmds...)
}

// bootstrapChat verifies the token and creates a conversation.
func (m *Model) bootstrapChat() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		u, err := m.client.Me(ctx)
		if err != nil {
			return loginNeededMsg{}
		}
		conv, err := m.client.CreateConversation(ctx, "Session", "")
		if err != nil {
			return errMsg{err}
		}
		return chatReadyMsg{user: u, conversationID: conv.ID}
	}
}

// --- messages ---

type loginNeededMsg struct{}
type chatReadyMsg struct {
	user           *types.User
	conversationID int64
}
type errMsg struct{ err error }
type infoMsg struct{ text string }
type loginDoneMsg struct {
	user *types.User
	err  error
}
type streamEventMsg struct{ ev types.StreamEvent }
type streamEndMsg struct{ err error }

func (m *Model) appendConnectedStatus() {
	if m.user == nil {
		return
	}
	m.appendBlock("status", "connected as "+m.user.Username)
}

func (m *Model) appendBlock(role, text string) {
	m.blocks = append(m.blocks, roleBlock{role: role, text: text})
	m.refreshViewport()
}

func (m *Model) refreshViewport() {
	var sb strings.Builder
	for _, b := range m.blocks {
		sb.WriteString(m.renderBlock(b))
		sb.WriteString("\n")
	}
	if m.cfg.ShowThinking && m.streamingThinking.Len() > 0 {
		sb.WriteString(m.renderThinking(m.streamingThinking.String(), true))
		sb.WriteString("\n")
	}
	if m.streaming.Len() > 0 {
		sb.WriteString(m.renderAssistant(m.streaming.String()))
		sb.WriteString("\n")
	}
	m.vp.SetContent(sb.String())
	m.vp.GotoBottom()
}

func (m *Model) renderBlock(b roleBlock) string {
	switch b.role {
	case "user":
		return m.wrapStyled(m.styles.User, "› "+b.text)
	case "assistant":
		return m.renderAssistant(b.text)
	case "thinking":
		return m.renderThinking(b.text, false)
	case "tool":
		return m.wrapStyled(m.styles.Tool, b.text)
	case "error":
		return m.wrapStyled(m.styles.Error, "error: "+b.text)
	case "status":
		return m.wrapStyled(m.styles.Status, b.text)
	default:
		return m.wrapStyled(m.styles.Help, b.text)
	}
}

func (m *Model) renderAssistant(text string) string {
	return wrapWidth(m.contentWidth(), renderMarkdown(m.contentWidth(), text))
}

func (m *Model) renderThinking(text string, streaming bool) string {
	label := "💭 thinking"
	if streaming {
		label = "💭 thinking…"
	}
	header := m.styles.Thinking.Render(label)
	body := wrapWidth(m.contentWidth(), m.styles.Thinking.Render(text))
	return header + "\n" + body
}

func errCmd(err error) tea.Cmd  { return func() tea.Msg { return errMsg{err} } }
func infoCmd(s string) tea.Cmd  { return func() tea.Msg { return infoMsg{s} } }

func renderSkillList(infos []types.SkillInfo) string {
	var sb strings.Builder
	sb.WriteString("Skills:\n")
	for _, i := range infos {
		fmt.Fprintf(&sb, "  %s [%s] — %s\n", i.Name, i.Source, i.Description)
	}
	return strings.TrimRight(sb.String(), "\n")
}
