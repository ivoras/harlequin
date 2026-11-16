package telegrambot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/session"
	"github.com/ivoras/harlequin/internal/server/sessionhub"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/server/telegram"
	"github.com/ivoras/harlequin/internal/server/userconfig"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// echoAgent answers every prompt with "echo: <prompt>" as <email>.
type echoAgent struct{}

func (echoAgent) Run(_ context.Context, _, _, _ int64, username, _, _, iface, content string, emit func(types.StreamEvent)) error {
	emit(types.StreamEvent{Type: types.SSEUserMessage, Text: content})
	emit(types.StreamEvent{Type: types.SSEToken, Text: "echo: " + content + " (" + username + ", " + iface + ")"})
	emit(types.StreamEvent{Type: types.SSEDone})
	return nil
}
func (echoAgent) LastMessageID(context.Context, int64, int64, int64) (int64, error) { return 0, nil }

// Link a chat with a one-time code, talk through it, and check that an
// unlinked chat and a replayed code get nothing.
func TestLinkAndChat(t *testing.T) {
	incoming := make(chan telegram.Update, 8)
	sent := make(chan string, 8)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var result any = true
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			result = map[string]string{"username": "acme_bot"}
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			select {
			case u := <-incoming:
				result = []telegram.Update{u}
			case <-time.After(50 * time.Millisecond):
				result = []telegram.Update{}
			}
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var m struct{ ChatID, Text string }
			_ = json.NewDecoder(r.Body).Decode(&struct {
				ChatID *string `json:"chat_id"`
				Text   *string `json:"text"`
			}{&m.ChatID, &m.Text})
			sent <- m.ChatID + ": " + m.Text
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	defer api.Close()

	dir := t.TempDir()
	store, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authStore := auth.NewStore(store.System)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	user, err := authStore.CreateUser(ctx, "ana@example.com", "correct horse battery", "user")
	if err != nil {
		t.Fatal(err)
	}
	hub := sessionhub.New(echoAgent{}, nil, time.Minute)
	defer hub.Stop()
	bot := &Bot{
		TG: telegram.New("TOKEN", api.URL), System: store.System, Storage: store, Auth: authStore,
		Sessions: session.NewStore(), UserConfig: userconfig.NewStore(), Hub: hub,
	}
	go bot.Run(ctx)

	say := func(id int64, chat int64, text string) {
		var m telegram.Message
		m.Chat.ID, m.Chat.Type, m.Text = chat, "private", text
		incoming <- telegram.Update{UpdateID: id, Message: &m}
	}
	expect := func(want string) {
		t.Helper()
		select {
		case got := <-sent:
			if !strings.Contains(got, want) {
				t.Fatalf("got %q, want it to contain %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no reply; wanted %q", want)
		}
	}

	say(1, 42, "hello")
	expect("42: " + textNotLinked)

	url, err := bot.NewLink(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, ok := strings.CutPrefix(url, "https://t.me/acme_bot?start=")
	if !ok {
		t.Fatalf("link = %q", url)
	}
	say(2, 42, "/start "+code)
	expect("42: Connected to ana@example.com")
	if !bot.Linked(ctx, user.ID) {
		t.Fatal("Linked = false after connecting")
	}
	say(3, 42, "hello")
	expect("42: echo: hello (ana@example.com, Telegram)")

	say(4, 99, "/start "+code) // one-time: a second chat cannot reuse it
	expect("99: " + textBadLink)
	say(5, 99, "hello")
	expect("99: " + textNotLinked)

	if err := bot.Unlink(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	say(6, 42, "hello")
	expect("42: " + textNotLinked)
}

func TestCollect(t *testing.T) {
	ev := make(chan types.StreamEvent, 16)
	for _, e := range []types.StreamEvent{
		{Type: types.SSEToken, Text: "someone else's turn"}, {Type: types.SSEDone}, // skipped
		{Type: types.SSEToken, Text: "Looking."},
		{Type: types.SSEToolCall},
		{Type: types.SSEToken, Text: "garbl"},
		{Type: types.SSEStepReset},
		{Type: types.SSEToken, Text: "Done."},
		{Type: types.SSEAskUser, Text: "Which?", Options: []string{"A", "B"}},
		{Type: types.SSEDone},
	} {
		ev <- e
	}
	got, done := collect(ev, true, func() {})
	if want := "Looking.\n\nDone.\n\nWhich?\n1. A\n2. B"; got != want || !done {
		t.Fatalf("collect = %q, %v; want %q", got, done, want)
	}
	close(ev)
	if _, done := collect(ev, false, func() {}); done {
		t.Fatal("closed stream reported as a finished turn")
	}
}
