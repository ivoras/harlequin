// Package notifyx routes a notification to a delivery channel: the in-app
// notification store (polled by the TUI/web), email, or Telegram. It is the
// single place that knows which channels are configured/active for a user.
package notifyx

import (
	"context"
	"database/sql"
	"strings"

	"github.com/ivoras/harlequin/internal/server/email"
	"github.com/ivoras/harlequin/internal/server/notify"
	"github.com/ivoras/harlequin/internal/server/telegram"
	"github.com/ivoras/harlequin/internal/server/userconfig"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// Channels a notification can be delivered through.
const (
	ChannelInApp    = "inapp"    // built-in TUI/web notification (always available)
	ChannelEmail    = "email"    // the user's account email address
	ChannelTelegram = "telegram" // the user's registered Telegram chat
)

// Dispatcher delivers notifications across channels.
type Dispatcher struct {
	notify *notify.Store
	email  *email.Sender
	tg     *telegram.Client
	cfg    *userconfig.Store
}

// NewDispatcher wires a Dispatcher. email/tg may be nil (channel unavailable).
func NewDispatcher(n *notify.Store, e *email.Sender, tg *telegram.Client, cfg *userconfig.Store) *Dispatcher {
	return &Dispatcher{notify: n, email: e, tg: tg, cfg: cfg}
}

// ActiveChannels lists the channels currently usable for this user: in-app
// always; email whenever a sender exists (every account has an address —
// delivery falls back to the server console when SMTP is unconfigured); Telegram
// only when a bot token is configured and the user has registered a chat id.
func (d *Dispatcher) ActiveChannels(ctx context.Context, userDB *sql.DB) []string {
	out := []string{ChannelInApp}
	if d.email != nil {
		out = append(out, ChannelEmail)
	}
	if d.telegramReady(ctx, userDB) {
		out = append(out, ChannelTelegram)
	}
	return out
}

func (d *Dispatcher) telegramReady(ctx context.Context, userDB *sql.DB) bool {
	if d.tg == nil || !d.tg.Configured() || d.cfg == nil {
		return false
	}
	id, _, _ := d.cfg.Get(ctx, userDB, userconfig.KeyTelegramChatID)
	return strings.TrimSpace(id) != ""
}

// Deliver sends a notification via channel for the given user. userEmail is the
// account's address (used by the email channel). Unknown or unavailable channels
// fall back to in-app, so a notification is never lost. kind is an optional
// dedupe/category key for the in-app store.
func (d *Dispatcher) Deliver(ctx context.Context, userDB *sql.DB, userEmail, channel, kind, title, body string) error {
	switch channel {
	case ChannelEmail:
		if d.email != nil && strings.TrimSpace(userEmail) != "" {
			return d.email.Send(userEmail, title, body)
		}
	case ChannelTelegram:
		if d.telegramReady(ctx, userDB) {
			id, _, _ := d.cfg.Get(ctx, userDB, userconfig.KeyTelegramChatID)
			return d.tg.Send(ctx, id, title+"\n\n"+body)
		}
	}
	// inapp (explicit), or fallback for an unavailable channel.
	_, err := d.notify.Create(ctx, userDB, types.Notification{Kind: kind, Title: title, Description: body})
	return err
}
