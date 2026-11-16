// Package telegrambot is the Telegram chat surface: it long-polls the bot for
// private messages, maps each chat to the account it was linked to, and runs
// the text as a prompt in that user's Telegram session through the session hub
// (the same live-session runtime the WebSocket clients use), replying with the
// turn's final text.
package telegrambot

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ivoras/harlequin/internal/server/audit"
	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/session"
	"github.com/ivoras/harlequin/internal/server/sessionhub"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/server/telegram"
	"github.com/ivoras/harlequin/internal/server/userconfig"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// LinkTTL is how long a one-time link from NewLink stays valid.
const LinkTTL = 10 * time.Minute

const (
	textNotLinked = "This chat is not connected to a Harlequin account yet. Create a Telegram link in Harlequin and open it here."
	textBadLink   = "That link is invalid or has expired. Create a new one in Harlequin."
	textHelp      = "Send me a message to talk to Harlequin.\n/new — start a fresh session\n/stop — interrupt the current reply"
	textTextOnly  = "I can only read text messages for now."
)

// Bot bridges one Telegram bot to the session hub. All exported fields are
// required except Hat and Audit.
type Bot struct {
	TG         *telegram.Client
	System     *sql.DB // system db: telegram_links
	Storage    *storage.Manager
	Auth       *auth.Store
	Sessions   *session.Store
	UserConfig *userconfig.Store
	Hub        *sessionhub.Hub
	Audit      *audit.Store
	// Hat picks the hat a new session starts with (the user's sticky one).
	Hat func(ctx context.Context, udb *sql.DB) string

	mu       sync.Mutex
	username string
	codes    map[string]linkCode
	chats    map[int64]*chat
}

type linkCode struct {
	userID  int64
	expires time.Time
}

// chat is one Telegram chat's worker: prompts run one at a time, in order.
type chat struct {
	jobs chan job

	mu      sync.Mutex
	sub     *sessionhub.Subscription // the in-flight turn, for /stop
	stopped bool
}

type job struct {
	user *types.User
	text string
}

// Run polls for messages until ctx ends. Only one process may poll a bot token:
// a second one makes Telegram answer 409 to both.
func (b *Bot) Run(ctx context.Context) {
	if name, err := b.botName(ctx); err != nil {
		log.Printf("%v", err)
	} else {
		log.Printf("telegram: chatting as @%s", name)
	}
	if err := b.TG.SetCommands(ctx, []telegram.Command{
		{Command: "new", Description: "Start a fresh session"},
		{Command: "stop", Description: "Interrupt the current reply"},
	}); err != nil {
		log.Printf("%v", err)
	}
	// The offset is not persisted: after a restart Telegram re-sends whatever
	// the last poll had not confirmed, so messages sent while down are answered.
	var offset int64
	for ctx.Err() == nil {
		updates, err := b.TG.GetUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("%v", err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			// Private chats only: in a group, everyone would talk as the linked user.
			if m := u.Message; m != nil && m.Chat.Type == "private" {
				b.handle(ctx, m)
			}
		}
	}
}

func (b *Bot) handle(ctx context.Context, m *telegram.Message) {
	chatID := m.Chat.ID
	text := strings.TrimSpace(m.Text)
	cmd, arg, _ := strings.Cut(text, " ")
	cmd, _, _ = strings.Cut(cmd, "@") // "/new@botname"

	if cmd == "/start" && arg != "" {
		b.reply(ctx, chatID, b.link(ctx, m, strings.TrimSpace(arg)))
		return
	}
	user := b.userForChat(ctx, chatID)
	if user == nil {
		b.reply(ctx, chatID, textNotLinked)
		return
	}
	switch {
	case text == "":
		b.reply(ctx, chatID, textTextOnly)
	case cmd == "/start":
		b.reply(ctx, chatID, "This chat is connected to "+user.Email+".\n\n"+textHelp)
	case cmd == "/new":
		if _, err := b.session(ctx, user, true); err != nil {
			b.reply(ctx, chatID, "⚠ Could not start a session: "+err.Error())
			return
		}
		b.reply(ctx, chatID, "Started a fresh session.")
	case cmd == "/stop":
		c := b.chatFor(ctx, chatID)
		c.mu.Lock()
		sub := c.sub
		c.stopped = sub != nil
		c.mu.Unlock()
		if sub == nil {
			b.reply(ctx, chatID, "Nothing is running.")
			return
		}
		sub.Interrupt()
		b.reply(ctx, chatID, "Stopped.")
	case strings.HasPrefix(cmd, "/"):
		b.reply(ctx, chatID, textHelp)
	default:
		select {
		case b.chatFor(ctx, chatID).jobs <- job{user: user, text: text}:
		default:
			b.reply(ctx, chatID, "⚠ Too many messages are waiting; send that again after the reply.")
		}
	}
}

func (b *Bot) reply(ctx context.Context, chatID int64, text string) {
	if err := b.TG.Send(ctx, strconv.FormatInt(chatID, 10), text); err != nil {
		log.Printf("%v", err)
	}
}

func (b *Bot) chatFor(ctx context.Context, chatID int64) *chat {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.chats[chatID]
	if c == nil {
		c = &chat{jobs: make(chan job, 16)}
		if b.chats == nil {
			b.chats = map[int64]*chat{}
		}
		b.chats[chatID] = c
		// One parked goroutine per chat that ever wrote; linked chats are few.
		go func() {
			for j := range c.jobs {
				b.turn(ctx, chatID, c, j)
			}
		}()
	}
	return c
}

// turn runs one prompt in the chat's session and sends the reply.
func (b *Bot) turn(ctx context.Context, chatID int64, c *chat, j job) {
	sess, err := b.session(ctx, j.user, false)
	if err != nil {
		b.reply(ctx, chatID, "⚠ Could not open a session: "+err.Error())
		return
	}
	sub := b.Hub.Attach(j.user, 0, sess)
	defer sub.Close()
	synced, _ := sub.Resume(0)
	c.mu.Lock()
	c.sub, c.stopped = sub, false
	c.mu.Unlock()
	sub.Submit(j.text)

	id := strconv.FormatInt(chatID, 10)
	text, done := collect(sub.Events(), synced.Running, func() { _ = b.TG.SendTyping(ctx, id) })

	c.mu.Lock()
	c.sub = nil
	stopped := c.stopped
	c.mu.Unlock()
	switch {
	case !done:
		text = strings.TrimSpace(text + "\n\n⚠ Lost the connection to the session; please send that again.")
	case text == "" && stopped:
		return
	case text == "":
		text = "(no reply)"
	}
	b.reply(ctx, chatID, text)
}

// collect drains one turn's events into the reply text; false means the event
// stream ended before the turn did. skip: a turn started from another client was
// already running on the session, so everything up to its done is not ours.
// typing is called now and every few seconds (Telegram's indicator lasts ~5s).
func collect(events <-chan types.StreamEvent, skip bool, typing func()) (string, bool) {
	var buf strings.Builder
	stepStart := 0 // where the current LLM step's text begins
	para := func(s string) {
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(s)
	}
	tick := time.NewTicker(4 * time.Second)
	defer tick.Stop()
	typing()
	for {
		select {
		case <-tick.C:
			typing()
		case ev, ok := <-events:
			if !ok {
				return strings.TrimSpace(buf.String()), false
			}
			if skip {
				skip = ev.Type != types.SSEDone
				continue
			}
			switch ev.Type {
			case types.SSEToken:
				buf.WriteString(ev.Text)
			case types.SSEStepReset: // the step is being re-run: drop what it streamed
				kept := buf.String()[:stepStart]
				buf.Reset()
				buf.WriteString(kept)
			case types.SSEToolCall:
				if buf.Len() > stepStart {
					buf.WriteString("\n\n")
				}
				stepStart = buf.Len()
			case types.SSEAskUser:
				q := ev.Text
				for i, o := range ev.Options {
					q += fmt.Sprintf("\n%d. %s", i+1, o)
				}
				para(q)
			case types.SSEError:
				para("⚠ " + ev.Error)
			case types.SSEDone:
				return strings.TrimSpace(buf.String()), true
			}
		}
	}
}

// session returns the session the user's Telegram chat talks in, creating one
// when there is none yet, it is gone (deleted, or moved into a project), or
// fresh is set.
func (b *Bot) session(ctx context.Context, u *types.User, fresh bool) (*types.Session, error) {
	var sess *types.Session
	err := b.Storage.WithUser(ctx, u.ID, func(udb *sql.DB) error {
		if v, _, _ := b.UserConfig.Get(ctx, udb, userconfig.KeyTelegramSession); v != "" && !fresh {
			id, _ := strconv.ParseInt(v, 10, 64)
			if s, err := b.Sessions.Get(ctx, udb, id, u.ID); err == nil {
				sess = s
				return nil
			}
		}
		hat := ""
		if b.Hat != nil {
			hat = b.Hat(ctx, udb)
		}
		s, err := b.Sessions.Create(ctx, udb, u.ID, "", hat, types.APITelegram, types.InterfaceTelegram)
		if err != nil {
			return err
		}
		sess = s
		return b.UserConfig.Set(ctx, udb, userconfig.KeyTelegramSession, strconv.FormatInt(s.ID, 10))
	})
	return sess, err
}

// --- account linking ---

func (b *Bot) botName(ctx context.Context) (string, error) {
	b.mu.Lock()
	name := b.username
	b.mu.Unlock()
	if name != "" {
		return name, nil
	}
	name, err := b.TG.Username(ctx)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	b.username = name
	b.mu.Unlock()
	return name, nil
}

// NewLink returns a one-time https://t.me/<bot>?start=<code> link; the chat
// that opens it within LinkTTL becomes userID's Telegram chat.
func (b *Bot) NewLink(ctx context.Context, userID int64) (string, error) {
	name, err := b.botName(ctx)
	if err != nil {
		return "", err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	code := base64.RawURLEncoding.EncodeToString(raw)
	// shortcut: pending codes live in memory, so a restart voids them; move to
	// the system db if servers ever restart often enough for users to notice.
	b.mu.Lock()
	if b.codes == nil {
		b.codes = map[string]linkCode{}
	}
	for k, c := range b.codes {
		if time.Now().After(c.expires) {
			delete(b.codes, k)
		}
	}
	b.codes[code] = linkCode{userID: userID, expires: time.Now().Add(LinkTTL)}
	b.mu.Unlock()
	return "https://t.me/" + name + "?start=" + code, nil
}

// link redeems a /start code for the chat that sent it and returns the reply.
func (b *Bot) link(ctx context.Context, m *telegram.Message, code string) string {
	b.mu.Lock()
	lc, ok := b.codes[code]
	delete(b.codes, code)
	b.mu.Unlock()
	if !ok || time.Now().After(lc.expires) {
		return textBadLink
	}
	u, err := b.Auth.UserByID(ctx, lc.userID)
	if err != nil {
		return textBadLink
	}
	chatID := m.Chat.ID
	var prev int64 // account this chat was connected to before, if any
	_ = b.System.QueryRowContext(ctx, `SELECT user_id FROM telegram_links WHERE chat_id = ?`, chatID).Scan(&prev)
	// One chat per account and one account per chat: the new link replaces both.
	tx, err := b.System.BeginTx(ctx, nil)
	if err == nil {
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, `DELETE FROM telegram_links WHERE chat_id = ? OR user_id = ?`, chatID, u.ID); err == nil {
			if _, err = tx.ExecContext(ctx, `INSERT INTO telegram_links(chat_id, user_id) VALUES (?, ?)`, chatID, u.ID); err == nil {
				err = tx.Commit()
			}
		}
	}
	if err != nil {
		log.Printf("telegram: link chat %d to user %d: %v", chatID, u.ID, err)
		return "⚠ Could not connect this chat; please try again."
	}
	if prev != 0 && prev != u.ID {
		b.clearNotifyTarget(ctx, prev) // or its notifications would land in this chat
	}
	// Notifications (notifyx) address the chat through the per-user config.
	err = b.Storage.WithUser(ctx, u.ID, func(udb *sql.DB) error {
		if b.Audit != nil {
			b.Audit.Log(ctx, udb, "telegram_link", fmt.Sprintf("chat %d", chatID), map[string]string{"username": m.From.Username})
		}
		if m.From.Username != "" {
			_ = b.UserConfig.Set(ctx, udb, userconfig.KeyTelegramUsername, m.From.Username)
		}
		return b.UserConfig.Set(ctx, udb, userconfig.KeyTelegramChatID, strconv.FormatInt(chatID, 10))
	})
	if err != nil {
		log.Printf("telegram: store chat id for user %d: %v", u.ID, err)
	}
	return "Connected to " + u.Email + ".\n\n" + textHelp
}

// Linked reports whether userID has a connected Telegram chat.
func (b *Bot) Linked(ctx context.Context, userID int64) bool {
	var n int
	_ = b.System.QueryRowContext(ctx, `SELECT COUNT(*) FROM telegram_links WHERE user_id = ?`, userID).Scan(&n)
	return n > 0
}

// Unlink disconnects userID's Telegram chat (a no-op when there is none).
func (b *Bot) Unlink(ctx context.Context, userID int64) error {
	if _, err := b.System.ExecContext(ctx, `DELETE FROM telegram_links WHERE user_id = ?`, userID); err != nil {
		return err
	}
	b.clearNotifyTarget(ctx, userID)
	return nil
}

func (b *Bot) clearNotifyTarget(ctx context.Context, userID int64) {
	_ = b.Storage.WithUser(ctx, userID, func(udb *sql.DB) error {
		_ = b.UserConfig.Delete(ctx, udb, userconfig.KeyTelegramUsername)
		return b.UserConfig.Delete(ctx, udb, userconfig.KeyTelegramChatID)
	})
}

// userForChat resolves the account a chat is linked to (nil when none). The
// account is re-read on every message, so a role change applies at once.
func (b *Bot) userForChat(ctx context.Context, chatID int64) *types.User {
	var uid int64
	if err := b.System.QueryRowContext(ctx, `SELECT user_id FROM telegram_links WHERE chat_id = ?`, chatID).Scan(&uid); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("telegram: look up chat %d: %v", chatID, err)
		}
		return nil
	}
	u, err := b.Auth.UserByID(ctx, uid)
	if err != nil {
		return nil
	}
	return u
}
