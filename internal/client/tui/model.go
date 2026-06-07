// Package tui implements the Harlequin Bubble Tea client: a Claude Code-like
// chat UI with a teal/apricot theme (truecolor with 256/16-color fallback), live streaming, a tool-call
// timeline, and slash-commands.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
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
	phaseAsk // interactive ask_user answering
)

// askItem is one question the model asked via ask_user, with suggested options.
type askItem struct {
	question string
	options  []string
}

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

	phase  phase
	width  int
	height int

	vp        viewport.Model
	input     textarea.Model
	spin      spinner.Model
	loading   bool
	turnStart time.Time // when the in-flight turn began (drives the elapsed readout)

	// login scratch
	loginUser string

	blocks            []roleBlock
	streamingThinking strings.Builder // in-flight reasoning text
	streaming         strings.Builder // in-flight assistant response text

	conversationID int64
	currentHat     string // hat worn by new conversations / the active one
	slashSel       int    // highlighted item in the slash-command autocomplete menu
	user           *types.User
	ctxMeter       contextMeterState
	pendingTiming  *types.TurnTiming // timing from the latest SSEDone, shown after the turn

	// Submitted input lines for up/down recall (messages and slash commands).
	inputHistory []string
	historyIndex int    // len(inputHistory) when editing a fresh line
	historyDraft string // draft saved when browsing history

	// cancel for the in-flight stream (Esc).
	cancelStream context.CancelFunc

	// ask_user interaction (phaseAsk): questions collected during a turn and the
	// answers being assembled.
	pendingAsk []askItem
	askIndex   int      // which question is being answered
	askSel     int      // highlighted option (len(options) == the "Other" entry)
	askAnswers []string // answers collected so far
	askOther   bool     // free-text entry mode for the current question
	askFrame   int      // animation frame for the selected-row marker
}

// New constructs the TUI model.
func New(cfg *clientcfg.Config) *Model {
	client := apiclient.New(cfg.ServerURL, cfg.Token, types.InterfaceTUI)
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
	taKeyMap := ta.KeyMap
	taKeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "alt+enter", "ctrl+j"))
	ta.KeyMap = taKeyMap

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	vp := viewport.New()
	vpKeyMap := viewport.DefaultKeyMap()
	vpKeyMap.PageUp = key.NewBinding(key.WithKeys("pgup"))
	vpKeyMap.PageDown = key.NewBinding(key.WithKeys("pgdown"))
	vp.KeyMap = vpKeyMap

	m := &Model{
		cfg:    cfg,
		client: client,
		skills: clientskills.NewManager(client, cfg.ExpandedSkillsDir()),
		styles: newStyles(),
		input:  ta,
		spin:   sp,
		vp:     vp,
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

// flushStreaming commits any in-flight thinking and assistant text to the
// transcript (chronological order: reasoning then text) and clears the buffers.
// Called at every tool-call boundary and at turn end so that reasoning, answer
// text, and tool calls interleave in the order they actually occurred rather
// than collapsing into separate sections. Does not refresh; the caller does.
func (m *Model) flushStreaming() {
	if m.cfg.ShowThinking && m.streamingThinking.Len() > 0 {
		m.blocks = append(m.blocks, roleBlock{role: "thinking", text: m.streamingThinking.String()})
		m.streamingThinking.Reset()
	}
	if m.streaming.Len() > 0 {
		m.blocks = append(m.blocks, roleBlock{role: "assistant", text: m.streaming.String()})
		m.streaming.Reset()
	}
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
	if m.loading {
		sb.WriteString(m.renderThinkingIndicator())
		sb.WriteString("\n")
	}
	atBottom := m.vp.AtBottom()
	m.vp.SetContent(sb.String())
	if atBottom {
		m.vp.GotoBottom()
	}
}

func (m *Model) renderBlock(b roleBlock) string {
	switch b.role {
	case "user":
		return m.wrapStyled(m.styles.User, "› "+b.text)
	case "command":
		return m.renderCommand(b.text)
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
	case "notification":
		return m.wrapStyled(m.styles.Accent, b.text)
	default:
		return m.wrapStyled(m.styles.Help, b.text)
	}
}

// renderCommand echoes a slash command the user typed: the command word in bold,
// the arguments in the regular user-prompt colour.
func (m *Model) renderCommand(text string) string {
	cmd, rest := text, ""
	if i := strings.IndexAny(text, " \t"); i >= 0 {
		cmd, rest = text[:i], text[i:]
	}
	styled := "› " + m.styles.User.Render(cmd) + m.styles.UserArg.Render(rest)
	return wrapWidth(m.contentWidth(), styled)
}

func (m *Model) renderAssistant(text string) string {
	return wrapWidth(m.contentWidth(), renderMarkdown(m.contentWidth(), text))
}

// renderThinkingIndicator is the animated status shown as the final transcript
// line while a turn is in flight: a braille spinner and a "Thinking…" label that
// glow with the same colour pulse as the top bar, an elapsed-seconds readout, and
// a dim cancel hint. The spinner advances and the glow breathes because
// refreshViewport re-renders on each spinner tick while loading.
func (m *Model) renderThinkingIndicator() string {
	pulse := lipgloss.Color(thinkingPulseColor(time.Now()))
	glow := lipgloss.NewStyle().Foreground(pulse).Bold(true)
	out := glow.Render(m.spin.View()+" Thinking") + glow.Render("…")
	if !m.turnStart.IsZero() {
		out += m.styles.Help.Render(fmt.Sprintf("  %ds", int(time.Since(m.turnStart).Seconds())))
	}
	out += m.styles.Help.Render("   esc to cancel")
	return out
}

func (m *Model) renderThinking(text string, streaming bool) string {
	label := "💭 thinking"
	if streaming {
		label = "💭 thinking…"
	}
	header := m.styles.Thinking.Render(label)
	// Style each line separately: styling the whole block at once makes lipgloss
	// pad short lines to the widest and fill the padding with the background.
	lines := strings.Split(wrapWidth(m.contentWidth(), text), "\n")
	for i, ln := range lines {
		ln = strings.TrimRight(ln, " ")
		if ln == "" {
			lines[i] = ""
			continue
		}
		lines[i] = m.styles.Thinking.Render(ln)
	}
	return header + "\n" + strings.Join(lines, "\n")
}

func errCmd(err error) tea.Cmd { return func() tea.Msg { return errMsg{err} } }
func infoCmd(s string) tea.Cmd { return func() tea.Msg { return infoMsg{s} } }

func renderSkillList(infos []types.SkillInfo) string {
	var sb strings.Builder
	sb.WriteString("Skills:\n")
	for _, i := range infos {
		fmt.Fprintf(&sb, "  %s [%s] — %s\n", i.Name, i.Source, i.Description)
	}
	return strings.TrimRight(sb.String(), "\n")
}
