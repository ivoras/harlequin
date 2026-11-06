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

// whoAgent records which user each turn ran as, and blocks each turn until released.
type whoAgent struct {
	ran     chan int64
	release chan struct{}
}

func (w *whoAgent) LastMessageID(context.Context, int64, int64, int64) (int64, error) { return 0, nil }

func (w *whoAgent) Run(ctx context.Context, _, _, userID int64, _, _, _, _, _ string, emit func(types.StreamEvent)) error {
	w.ran <- userID
	select {
	case <-w.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	emit(types.StreamEvent{Type: types.SSEDone})
	return nil
}

// In a shared project session each turn runs as the member who sent the prompt,
// and DropUser cuts only the removed member off (connection, queued prompts).
func TestProjectTurnsRunAsSenderAndDropUser(t *testing.T) {
	w := &whoAgent{ran: make(chan int64, 4), release: make(chan struct{}, 4)}
	hub := New(w, nil, time.Minute)
	defer hub.Stop()
	sess := &types.Session{ID: 7, API: types.APIREST, Interface: types.InterfaceWeb}
	alice := &types.User{ID: 1, Email: "a@x", Role: types.RoleAdmin}
	bob := &types.User{ID: 2, Email: "b@x", Role: types.RoleUser}

	a := hub.Attach(alice, 5, sess) // alice opens the session first
	defer a.Close()
	a.Resume(0)
	b := hub.Attach(bob, 5, sess)
	defer b.Close()
	b.Resume(0)

	b.Submit("from bob")
	if got := <-w.ran; got != bob.ID {
		t.Fatalf("bob's prompt ran as user %d", got)
	}
	b.Submit("bob again, queued") // queued behind the running turn
	hub.DropUser(5, bob.ID)       // interrupts bob's turn, drops his queued prompt

	if _, open := <-b.Events(); open {
		// drain until closed
		for range b.Events() {
		}
	}
	a.Submit("from alice")
	select {
	case got := <-w.ran:
		if got != alice.ID {
			t.Fatalf("after removal, next turn ran as user %d (bob's queued prompt not dropped?)", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("alice's turn did not run")
	}
	w.release <- struct{}{}
	// Alice stays connected.
	select {
	case _, open := <-a.Events():
		if !open {
			t.Fatal("alice was disconnected by bob's removal")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no events for alice")
	}
}

func TestBusyWhileTurnRuns(t *testing.T) {
	w := &whoAgent{ran: make(chan int64, 2), release: make(chan struct{}, 2)}
	hub := New(w, nil, time.Minute)
	defer hub.Stop()
	user, sess := testUserSess()
	if hub.Busy(0, user.ID, sess.ID) {
		t.Fatal("no live session should be idle")
	}
	a := hub.Attach(user, 0, sess)
	defer a.Close()
	a.Resume(0)
	a.Submit("go")
	<-w.ran
	if !hub.Busy(0, user.ID, sess.ID) {
		t.Fatal("running turn should be busy")
	}
	w.release <- struct{}{}
	deadline := time.Now().Add(2 * time.Second)
	for hub.Busy(0, user.ID, sess.ID) {
		if time.Now().After(deadline) {
			t.Fatal("still busy after the turn ended")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStatsCountsConnectedSessions(t *testing.T) {
	w := &whoAgent{ran: make(chan int64, 2), release: make(chan struct{}, 2)}
	hub := New(w, nil, time.Minute)
	defer hub.Stop()
	sess := &types.Session{ID: 7, API: types.APIREST, Interface: types.InterfaceWeb}
	alice := &types.User{ID: 1, Email: "a@x", Role: types.RoleUser}
	bob := &types.User{ID: 2, Email: "b@x", Role: types.RoleUser}
	a1 := hub.Attach(alice, 5, sess) // shared project session: alice twice, bob once
	a1.Resume(0)
	a2 := hub.Attach(alice, 5, sess)
	a2.Resume(0)
	b := hub.Attach(bob, 5, sess)
	b.Resume(0)
	p := hub.Attach(alice, 0, &types.Session{ID: 9}) // alice's personal session
	p.Resume(0)
	a1.Submit("go")
	<-w.ran
	if st := hub.Stats(0); st != (LiveStats{Sessions: 2, Connections: 4, Running: 1}) {
		t.Fatalf("server-wide %+v", st)
	}
	if st := hub.Stats(bob.ID); st != (LiveStats{Sessions: 1, Connections: 1, Running: 1}) {
		t.Fatalf("bob %+v", st)
	}
	p.Close()
	if st := hub.Stats(alice.ID); st.Sessions != 1 || st.Connections != 2 {
		t.Fatalf("alice after closing her personal tab %+v", st)
	}
	w.release <- struct{}{}
	a1.Close()
	a2.Close()
	b.Close()
}
