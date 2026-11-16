// Package telegram is a minimal Telegram Bot API client: sending messages
// (notifications, chat replies) and long-polling for incoming ones. A zero bot
// token disables it (Configured reports false).
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf16"
)

// DefaultAPIBase is the public Bot API host. Overridable (e.g. for tests).
const DefaultAPIBase = "https://api.telegram.org"

// MaxMessageLen is Telegram's per-message limit, in UTF-16 code units.
const MaxMessageLen = 4096

// pollSeconds is how long Telegram holds a getUpdates call open.
const pollSeconds = 50

// Client talks to the Bot API as one bot.
type Client struct {
	token   string
	apiBase string
	http    *http.Client
}

// New builds a client. token is the bot token (empty = disabled); apiBase
// defaults to DefaultAPIBase when empty.
func New(token, apiBase string) *Client {
	if apiBase == "" {
		apiBase = DefaultAPIBase
	}
	return &Client{
		token:   token,
		apiBase: strings.TrimRight(apiBase, "/"),
		http:    &http.Client{}, // per-call timeouts: a long poll outlasts any fixed one
	}
}

// Configured reports whether a bot token is set.
func (c *Client) Configured() bool { return c != nil && strings.TrimSpace(c.token) != "" }

// call POSTs one Bot API method and decodes its result into out (nil to ignore).
func (c *Client) call(ctx context.Context, timeout time.Duration, method string, params, out any) error {
	if !c.Configured() {
		return fmt.Errorf("telegram: bot token not configured")
	}
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// The URL in a transport error carries the token; keep it out of logs.
		return fmt.Errorf("telegram: %s: %s", method, strings.ReplaceAll(err.Error(), c.token, "<token>"))
	}
	defer resp.Body.Close()
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&env); err != nil {
		return fmt.Errorf("telegram: %s: status %d: %w", method, resp.StatusCode, err)
	}
	if !env.OK {
		return fmt.Errorf("telegram: %s: %d %s", method, env.ErrorCode, env.Description)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Result, out)
}

// Send delivers text to a chat, as several messages when it exceeds Telegram's
// limit. chatID is the numeric id or @channel handle.
func (c *Client) Send(ctx context.Context, chatID, text string) error {
	if strings.TrimSpace(chatID) == "" {
		return fmt.Errorf("telegram: no chat id")
	}
	for _, part := range Split(text, MaxMessageLen) {
		if err := c.call(ctx, 15*time.Second, "sendMessage", map[string]string{"chat_id": chatID, "text": part}, nil); err != nil {
			return err
		}
	}
	return nil
}

// Split cuts text into pieces of at most limit UTF-16 code units, at a newline
// when one falls in the second half of a piece.
func Split(text string, limit int) []string {
	var out []string
	for text != "" {
		units, cut, nl := 0, len(text), -1
		for i, r := range text {
			if units += utf16.RuneLen(r); units > limit {
				cut = i
				break
			}
			if r == '\n' {
				nl = i
			}
		}
		if cut == len(text) {
			return append(out, text)
		}
		if nl > cut/2 {
			cut = nl
		}
		out = append(out, text[:cut])
		text = strings.TrimLeft(text[cut:], "\n")
	}
	return out
}

// Update is one incoming event; only messages are requested.
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

// Message is an incoming chat message. Text is empty for photos, files, etc.
type Message struct {
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"` // "private", "group", "supergroup", "channel"
	} `json:"chat"`
	From struct {
		Username string `json:"username"`
	} `json:"from"`
	Text string `json:"text"`
}

// GetUpdates long-polls for updates with id >= offset; passing the last id + 1
// confirms everything before it. Returns empty when the poll times out.
func (c *Client) GetUpdates(ctx context.Context, offset int64) ([]Update, error) {
	var out []Update
	err := c.call(ctx, (pollSeconds+10)*time.Second, "getUpdates", map[string]any{
		"offset": offset, "timeout": pollSeconds, "allowed_updates": []string{"message"},
	}, &out)
	return out, err
}

// Username returns the bot's own @username (without the @).
func (c *Client) Username(ctx context.Context) (string, error) {
	var me struct {
		Username string `json:"username"`
	}
	err := c.call(ctx, 15*time.Second, "getMe", struct{}{}, &me)
	return me.Username, err
}

// SendTyping shows the "typing…" indicator in a chat for about five seconds.
func (c *Client) SendTyping(ctx context.Context, chatID string) error {
	return c.call(ctx, 15*time.Second, "sendChatAction", map[string]string{"chat_id": chatID, "action": "typing"}, nil)
}

// Command is one entry of the bot's command menu.
type Command struct {
	Command     string `json:"command"` // without the leading slash
	Description string `json:"description"`
}

// SetCommands replaces the bot's command menu.
func (c *Client) SetCommands(ctx context.Context, cmds []Command) error {
	return c.call(ctx, 15*time.Second, "setMyCommands", map[string]any{"commands": cmds}, nil)
}
