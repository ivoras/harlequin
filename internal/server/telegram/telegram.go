// Package telegram is a minimal Telegram Bot API client for delivering
// notifications. A zero bot token disables sending (Configured reports false).
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
)

// DefaultAPIBase is the public Bot API host. Overridable (e.g. for tests).
const DefaultAPIBase = "https://api.telegram.org"

// Client sends messages via a Telegram bot.
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
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// Configured reports whether a bot token is set.
func (c *Client) Configured() bool { return c != nil && strings.TrimSpace(c.token) != "" }

type sendMessageRequest struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}

// Send delivers text to a chat. chatID is the numeric id or @channel handle.
func (c *Client) Send(ctx context.Context, chatID, text string) error {
	if !c.Configured() {
		return fmt.Errorf("telegram: bot token not configured")
	}
	if strings.TrimSpace(chatID) == "" {
		return fmt.Errorf("telegram: no chat id")
	}
	body, err := json.Marshal(sendMessageRequest{ChatID: chatID, Text: text})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/bot%s/sendMessage", c.apiBase, c.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("telegram: status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}
