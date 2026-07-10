// Package sessionhub keeps chat sessions alive on the server independently of any
// client connection. Each active session is a goroutine that runs agent turns,
// buffers the in-flight turn's stream events (sequence-numbered) for resume, and
// fans them out to every connected WebSocket subscriber. A session goroutine
// outlives client disconnects and exits only after an idle period with no
// connection and no running turn — so a user can submit a prompt, disconnect, and
// reconnect later to see the result and continue.
package sessionhub

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/ivoras/harlequin/internal/server/sessionlog"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// Agent is the subset of the server agent the hub drives. It is an interface so
// the hub does not pull in the storage/sqlite-vec link chain (keeping it
// unit-testable); the concrete agent is adapted to it in main.
type Agent interface {
	// Run executes one turn, streaming events to emit. The context is the
	// session's (not a request's), so it survives client disconnects.
	Run(ctx context.Context, projectID, sessionID, userID int64, username, role, api, iface, content string, emit func(types.StreamEvent)) error
	// LastMessageID returns the highest committed message id in the session: the
	// watermark between durable history and the in-flight turn's replay buffer.
	LastMessageID(ctx context.Context, projectID, userID, sessionID int64) (int64, error)
}

// subEventBuffer is the per-subscriber send queue depth. Token events are
// frequent; a slow consumer that overflows it is dropped (its channel closed) so
// the client reconnects and cold-resumes rather than back-pressuring the turn.
const subEventBuffer = 1024

// promptQueueDepth bounds prompts queued for a single session while a turn runs.
const promptQueueDepth = 64

// Hub owns the live session goroutines, keyed by (userID, sessionID).
type Hub struct {
	agent Agent
	log   *sessionlog.Logger
	idle  time.Duration

	baseCtx context.Context
	cancel  context.CancelFunc

	mu       sync.Mutex
	sessions map[string]*liveSession
}

// New constructs a Hub. idle is how long a session may sit with no connection and
// no running turn before its goroutine exits (defaults to 30m if non-positive).
func New(ag Agent, log *sessionlog.Logger, idle time.Duration) *Hub {
	if idle <= 0 {
		idle = 30 * time.Minute
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Hub{
		agent:    ag,
		log:      log,
		idle:     idle,
		baseCtx:  ctx,
		cancel:   cancel,
		sessions: map[string]*liveSession{},
	}
}

// Stop signals all session goroutines to exit (best-effort; in-flight turns end).
func (h *Hub) Stop() { h.cancel() }

// sessionKey identifies a live session. Project sessions are keyed by project id
// (shared across all members → one live session); personal sessions by user id.
func sessionKey(projectID, userID, sessID int64) string {
	if projectID > 0 {
		return "p" + strconv.FormatInt(projectID, 10) + "/" + strconv.FormatInt(sessID, 10)
	}
	return "u" + strconv.FormatInt(userID, 10) + "/" + strconv.FormatInt(sessID, 10)
}

// Attach registers a new connection to the user's session, spawning the session
// goroutine if needed, and returns a Subscription the WebSocket handler drives.
// user provides identity (id/email/role); sess provides the medium/transport and
// session id. The returned Subscription must be Closed when the connection ends.
// projectID is 0 for a personal session, or the owning project for a shared
// project session (members share one live session, keyed by project+session).
func (h *Hub) Attach(user *types.User, projectID int64, sess *types.Session) *Subscription {
	k := sessionKey(projectID, user.ID, sess.ID)
	h.mu.Lock()
	ls := h.sessions[k]
	if ls == nil {
		ls = &liveSession{
			hub:       h,
			key:       k,
			projectID: projectID,
			userID:    user.ID,
			sessID:    sess.ID,
			username:  user.Email,
			role:      user.Role,
			api:       sess.API,
			iface:     sess.Interface,
			prompts:   make(chan string, promptQueueDepth),
			activity:  make(chan struct{}, 1),
			subs:      map[*subscriber]struct{}{},
		}
		h.sessions[k] = ls
		go ls.loop()
	}
	ls.mu.Lock()
	ls.attached++
	ls.mu.Unlock()
	h.mu.Unlock()

	ls.signalActivity()
	return &Subscription{ls: ls, sub: &subscriber{ch: make(chan types.StreamEvent, subEventBuffer)}}
}

// subscriber is one connected client's live event queue.
type subscriber struct {
	ch chan types.StreamEvent
}

// liveSession is one session's long-lived goroutine and shared state.
type liveSession struct {
	hub       *Hub
	key       string
	projectID int64
	userID    int64
	sessID    int64
	username  string
	role      string
	api       string
	iface     string

	prompts  chan string
	activity chan struct{}

	mu       sync.Mutex
	subs     map[*subscriber]struct{} // receive live events (added on resume)
	attached int                      // connections present (for idle liveness)
	buf      []types.StreamEvent      // current turn's events (seq-ordered)
	seq      int                      // session-monotonic event sequence
	running  bool
	// committedThrough is the highest message id durable before the in-flight
	// turn began: the cut between history (load from DB) and the live buffer.
	committedThrough int64
	turnCancel       context.CancelFunc
}

// loop runs the session: it serializes prompts (one turn at a time) and reaps the
// session after an idle period with no connection and no running turn.
func (ls *liveSession) loop() {
	idle := time.NewTimer(ls.hub.idle)
	defer idle.Stop()
	for {
		select {
		case content := <-ls.prompts:
			stopTimer(idle)
			ls.runTurn(content)
			idle.Reset(ls.hub.idle)

		case <-ls.activity:
			stopTimer(idle)
			idle.Reset(ls.hub.idle)

		case <-idle.C:
			// Expire only with no connection and no running turn. Decide under both
			// locks so a concurrent Attach either keeps the session (sees us not yet
			// removed) or creates a fresh one (after removal) — never attaches to a
			// session about to exit.
			ls.hub.mu.Lock()
			ls.mu.Lock()
			expire := ls.attached == 0 && !ls.running
			if expire {
				delete(ls.hub.sessions, ls.key)
			}
			ls.mu.Unlock()
			ls.hub.mu.Unlock()
			if expire {
				ls.log(sessionlog.TypeSessionExpired, map[string]any{"idle": ls.hub.idle.String()})
				return
			}
			idle.Reset(ls.hub.idle)

		case <-ls.hub.baseCtx.Done():
			return
		}
	}
}

func stopTimer(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}

// runTurn runs one agent turn under a session-scoped (interruptible) context. A
// client disconnect does not cancel it; only an explicit interrupt or hub stop
// does.
func (ls *liveSession) runTurn(content string) {
	// Capture the durable-history watermark before the turn writes its first
	// (user) message.
	startID, _ := ls.hub.agent.LastMessageID(ls.hub.baseCtx, ls.projectID, ls.userID, ls.sessID)

	turnCtx, cancel := context.WithCancel(ls.hub.baseCtx)
	ls.mu.Lock()
	ls.buf = nil // fresh buffer; seq keeps incrementing across turns
	ls.running = true
	ls.committedThrough = startID
	ls.turnCancel = cancel
	ls.mu.Unlock()

	err := ls.hub.agent.Run(turnCtx, ls.projectID, ls.sessID, ls.userID, ls.username, ls.role, ls.api, ls.iface, content, ls.emit)

	ls.mu.Lock()
	ls.running = false
	ls.turnCancel = nil
	ls.mu.Unlock()
	cancel()

	// Surface a hard error not already streamed by the loop (skip interrupts).
	if err != nil && turnCtx.Err() == nil {
		ls.emit(types.StreamEvent{Type: types.SSEError, Error: err.Error()})
	}
	// Run emits SSEDone only on success, but clients end the turn only on
	// SSEDone — without one after a failed (or interrupted) turn they keep the
	// spinner up and queue every subsequent prompt against a session that is
	// actually free.
	if err != nil {
		ls.emit(types.StreamEvent{Type: types.SSEDone})
	}
}

// emit assigns a sequence number, appends to the turn buffer, and fans the event
// out to live subscribers. A subscriber whose queue is full is dropped (channel
// closed) so its WebSocket writer ends and the client reconnects/cold-resumes.
func (ls *liveSession) emit(ev types.StreamEvent) {
	ls.mu.Lock()
	ls.seq++
	ev.Seq = ls.seq
	ls.buf = append(ls.buf, ev)
	for sub := range ls.subs {
		select {
		case sub.ch <- ev:
		default:
			close(sub.ch)
			delete(ls.subs, sub)
		}
	}
	ls.mu.Unlock()
}

func (ls *liveSession) signalActivity() {
	select {
	case ls.activity <- struct{}{}:
	default:
	}
}

// submit queues a prompt for the session loop (runs after any in-flight turn).
func (ls *liveSession) submit(content string) {
	select {
	case ls.prompts <- content:
	default:
		// Queue full: report rather than block the connection's reader.
		ls.emit(types.StreamEvent{Type: types.SSEError, Error: "session prompt queue full"})
	}
}

// interrupt cancels the in-flight turn (if any) without ending the session.
func (ls *liveSession) interrupt() {
	ls.mu.Lock()
	cancel := ls.turnCancel
	ls.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// resume registers sub for live events and returns the synced control frame plus
// the replay slice the caller must send before pumping live events. Done under
// one lock so no event falls between the buffer snapshot and going live.
func (ls *liveSession) resume(sub *subscriber, haveSeq int) (types.StreamEvent, []types.StreamEvent) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	synced := types.StreamEvent{
		Type:             types.SSESynced,
		Running:          ls.running,
		CommittedThrough: ls.committedThrough,
		Seq:              ls.seq,
	}
	var replay []types.StreamEvent
	if ls.running {
		for _, ev := range ls.buf {
			if ev.Seq > haveSeq {
				replay = append(replay, ev)
			}
		}
	}
	ls.subs[sub] = struct{}{}
	return synced, replay
}

func (ls *liveSession) detach(sub *subscriber) {
	ls.mu.Lock()
	if _, ok := ls.subs[sub]; ok {
		delete(ls.subs, sub)
		close(sub.ch)
	}
	if ls.attached > 0 {
		ls.attached--
	}
	ls.mu.Unlock()
	ls.signalActivity()
}

func (ls *liveSession) log(typ string, data map[string]any) {
	if ls.hub.log == nil {
		return
	}
	ls.hub.log.Log(ls.hub.baseCtx, sessionlog.Event{
		SessionID: ls.sessID,
		UserID:    ls.userID,
		Type:      typ,
		Data:      data,
	})
}

// Subscription is the WebSocket handler's handle to a live session.
type Subscription struct {
	ls  *liveSession
	sub *subscriber
}

// Events is the channel of live events for this connection, valid after Resume.
func (s *Subscription) Events() <-chan types.StreamEvent { return s.sub.ch }

// Resume registers this connection for live events and returns the synced control
// frame plus the buffered events the caller must send (in order) before pumping
// Events(). haveSeq is the client's last processed Seq (0 = cold resume).
func (s *Subscription) Resume(haveSeq int) (types.StreamEvent, []types.StreamEvent) {
	cold := haveSeq == 0
	s.ls.log(sessionlog.TypeSessionResumed, map[string]any{"have_seq": haveSeq, "cold": cold})
	return s.ls.resume(s.sub, haveSeq)
}

// Submit queues a prompt for the session.
func (s *Subscription) Submit(content string) { s.ls.submit(content) }

// Interrupt cancels the in-flight turn without ending the session.
func (s *Subscription) Interrupt() { s.ls.interrupt() }

// Close detaches this connection from the session.
func (s *Subscription) Close() { s.ls.detach(s.sub) }
