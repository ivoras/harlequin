package sessionhub

import (
	"context"
	"errors"
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

func TestCloseProjectDisconnectsAndExits(t *testing.T) {
	fake := &fakeAgent{emitted: make(chan struct{}, 1), release: make(chan struct{})}
	hub := New(fake, nil, time.Minute)
	defer hub.Stop()
	user, sess := testUserSess()

	proj := hub.Attach(user, 5, sess)
	defer proj.Close()
	proj.Resume(0)
	other := hub.Attach(user, 6, sess) // a different project must be untouched
	defer other.Close()
	other.Resume(0)
	proj.Submit("hello")
	select {
	case <-fake.emitted:
	case <-time.After(2 * time.Second):
		t.Fatal("turn did not start")
	}
	close(fake.release) // let the (cancel-ignoring) fake turn finish

	done := make(chan struct{})
	go func() { hub.CloseProject(5); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("CloseProject did not return")
	}

	// The project's subscriber channel is closed (after any buffered events).
	deadline := time.After(2 * time.Second)
	for open := true; open; {
		select {
		case _, ok := <-proj.Events():
			open = ok
		case <-deadline:
			t.Fatal("subscriber channel not closed")
		}
	}
	hub.mu.Lock()
	n := len(hub.sessions)
	hub.mu.Unlock()
	if n != 1 {
		t.Fatalf("live sessions after CloseProject = %d, want 1 (other project's)", n)
	}
}

// failAgent runs a turn that streams a little, then fails (emitting its own
// error, like the real agent loop does).
type failAgent struct{}

func (failAgent) LastMessageID(context.Context, int64, int64, int64) (int64, error) { return 0, nil }
func (failAgent) Run(_ context.Context, _, _, _ int64, _, _, _, _, content string, emit func(types.StreamEvent)) error {
	emit(types.StreamEvent{Type: types.SSEUserMessage, Text: content})
	emit(types.StreamEvent{Type: types.SSEThinking, Thinking: "hmm"})
	emit(types.StreamEvent{Type: types.SSEError, Error: "boom"})
	return errors.New("boom")
}

// A client that dropped mid-turn and reconnects AFTER the turn failed must still
// receive the error and the done event (it used to get nothing, leaving a phone
// stuck on the last thinking block); a cold reload replays nothing (the error is
// stored in the history); and the failure is emitted exactly once.
func TestResumeAfterFailedTurnDeliversError(t *testing.T) {
	hub := New(failAgent{}, nil, time.Minute)
	defer hub.Stop()
	user, sess := testUserSess()

	a := hub.Attach(user, 0, sess)
	defer a.Close()
	a.Resume(0)
	a.Submit("go")
	deadline := time.After(2 * time.Second)
	for {
		synced, _ := hub.Attach(user, 0, sess).Resume(0)
		if !synced.Running && synced.Seq >= 4 { // user echo, thinking, error, done
			break
		}
		select {
		case <-deadline:
			t.Fatal("turn never finished")
		case <-time.After(20 * time.Millisecond):
		}
	}

	// Warm client that had seen only seq 1..2 (user echo + thinking).
	w := hub.Attach(user, 0, sess)
	defer w.Close()
	synced, tail := w.Resume(2)
	if synced.Running {
		t.Fatal("turn should be over")
	}
	var types_ []string
	for _, ev := range tail {
		types_ = append(types_, string(ev.Type))
	}
	if len(tail) != 2 || tail[0].Type != types.SSEError || tail[1].Type != types.SSEDone {
		t.Fatalf("warm replay after failure = %v, want [error done] (error exactly once)", types_)
	}

	// Cold reload: nothing replayed — the failure is stored in the session's
	// messages (role "error") by the agent, which clients load as history.
	c := hub.Attach(user, 0, sess)
	defer c.Close()
	_, cold := c.Resume(0)
	if len(cold) != 0 {
		t.Fatalf("cold replay after failure = %+v, want none", cold)
	}
}
