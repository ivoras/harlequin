package sessionhub

import (
	"context"
	"testing"
	"time"

	"github.com/ivoras/harlequin/internal/shared/types"
)

// fakeAgent emits a fixed in-flight turn (user echo + two tokens), then blocks on
// release before finishing — so a test can attach mid-turn and inspect the buffer.
type fakeAgent struct {
	emitted chan struct{}
	release chan struct{}
}

func (f *fakeAgent) LastMessageID(context.Context, int64, int64, int64) (int64, error) { return 0, nil }

func (f *fakeAgent) Run(_ context.Context, _, _, _ int64, _, _, _, _, content string, emit func(types.StreamEvent)) error {
	emit(types.StreamEvent{Type: types.SSEUserMessage, Text: content})
	emit(types.StreamEvent{Type: types.SSEToken, Text: "a"})
	emit(types.StreamEvent{Type: types.SSEToken, Text: "b"})
	f.emitted <- struct{}{}
	<-f.release
	emit(types.StreamEvent{Type: types.SSEDone})
	return nil
}

func testUserSess() (*types.User, *types.Session) {
	return &types.User{ID: 1, Email: "e@x", Role: types.RoleUser},
		&types.Session{ID: 7, API: types.APIREST, Interface: types.InterfaceWeb}
}

func TestResumeReplaysInflightTurn(t *testing.T) {
	fake := &fakeAgent{emitted: make(chan struct{}, 1), release: make(chan struct{})}
	hub := New(fake, nil, time.Minute)
	defer hub.Stop()
	user, sess := testUserSess()

	a := hub.Attach(user, 0, sess)
	defer a.Close()
	a.Resume(0)
	a.Submit("hello")

	// Wait until the turn is mid-flight (user echo + two tokens buffered).
	select {
	case <-fake.emitted:
	case <-time.After(2 * time.Second):
		t.Fatal("turn did not start")
	}

	// Cold resume: a fresh connection gets the whole in-flight turn.
	b := hub.Attach(user, 0, sess)
	defer b.Close()
	synced, replay := b.Resume(0)
	if !synced.Running {
		t.Fatalf("expected running turn in synced frame")
	}
	if len(replay) != 3 {
		t.Fatalf("cold replay = %d events, want 3", len(replay))
	}
	if replay[0].Type != types.SSEUserMessage || replay[0].Seq != 1 {
		t.Fatalf("first replayed event = %+v, want user_message seq 1", replay[0])
	}

	// Warm reconnect: only the tail beyond have_seq replays.
	c := hub.Attach(user, 0, sess)
	defer c.Close()
	_, tail := c.Resume(2)
	if len(tail) != 1 || tail[0].Seq != 3 {
		t.Fatalf("warm replay = %+v, want only seq 3", tail)
	}

	close(fake.release)
}

func TestIdleExpiry(t *testing.T) {
	fake := &fakeAgent{emitted: make(chan struct{}, 1), release: make(chan struct{})}
	hub := New(fake, nil, 40*time.Millisecond)
	defer hub.Stop()
	user, sess := testUserSess()

	a := hub.Attach(user, 0, sess)
	a.Resume(0)
	a.Close() // no turn, no connection: the reaper should expire it

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hub.mu.Lock()
		n := len(hub.sessions)
		hub.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("idle session was not reaped")
}
