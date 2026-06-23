package cron

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/ivoras/harlequin/internal/server/agent"
	"github.com/ivoras/harlequin/internal/server/cron/cronspec"
	"github.com/ivoras/harlequin/internal/server/notifyx"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// maxOutput bounds the stored last_output / notification body of a run.
const maxOutput = 4000

// Scheduler ticks once a minute, finds due jobs across all users, and runs each
// in its own goroutine via the agent.
type Scheduler struct {
	storage  *storage.Manager
	store    *Store
	agent    *agent.Agent
	dispatch *notifyx.Dispatcher
	running  sync.Map // key "userID:jobID" -> struct{}, in-flight guard
}

// NewScheduler wires a Scheduler.
func NewScheduler(st *storage.Manager, store *Store, ag *agent.Agent, d *notifyx.Dispatcher) *Scheduler {
	return &Scheduler{storage: st, store: store, agent: ag, dispatch: d}
}

// Start runs the scheduler loop until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	go s.loop(ctx)
}

// loop ticks on minute boundaries (1-minute granularity).
func (s *Scheduler) loop(ctx context.Context) {
	// Align the first tick to the next whole minute, then run every minute.
	now := time.Now()
	first := now.Truncate(time.Minute).Add(time.Minute)
	timer := time.NewTimer(time.Until(first))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-timer.C:
			s.tick(ctx, t)
			next := time.Now().Truncate(time.Minute).Add(time.Minute)
			timer.Reset(time.Until(next))
		}
	}
}

// tick dispatches every due job for every user.
func (s *Scheduler) tick(ctx context.Context, now time.Time) {
	err := s.storage.EachUser(ctx, func(userID int64, udb *sql.DB) error {
		due, err := s.store.DueJobs(ctx, udb, now)
		if err != nil {
			log.Printf("cron: list due jobs for user %d: %v", userID, err)
			return nil // keep going for other users
		}
		for _, job := range due {
			// Persist the next run BEFORE launching so a long run isn't re-picked
			// next tick; disable jobs whose spec has become invalid to avoid a
			// hot loop.
			next, nerr := cronspec.Next(job.Spec, now)
			if nerr != nil {
				log.Printf("cron: disabling job %d (user %d): %v", job.ID, userID, nerr)
				_ = s.store.SetEnabled(ctx, udb, job.ID, false)
				continue
			}
			_ = s.store.Reschedule(ctx, udb, job.ID, next)
			s.launch(userID, job, "scheduled")
		}
		return nil
	})
	if err != nil {
		log.Printf("cron: tick: %v", err)
	}
}

// launch starts a job in its own goroutine unless it is already running. trigger
// describes what dispatched it ("scheduled" or "manual") and is logged so the
// console shows every task as it begins executing.
func (s *Scheduler) launch(userID int64, job types.CronJob, trigger string) {
	key := fmt.Sprintf("%d:%d", userID, job.ID)
	if _, busy := s.running.LoadOrStore(key, struct{}{}); busy {
		log.Printf("cron: skipping job %d (%q) for user %d (%s): previous run still in flight", job.ID, job.Name, userID, trigger)
		return
	}
	log.Printf("cron: starting job %d (%q) for user %d (%s, kind=%s)", job.ID, job.Name, userID, trigger, job.Kind)
	go func() {
		defer s.running.Delete(key)
		s.run(context.Background(), userID, job)
	}()
}

// RunNow dispatches a job immediately (used by the "run now" API / TUI command).
func (s *Scheduler) RunNow(ctx context.Context, userID, jobID int64) error {
	var job types.CronJob
	if err := s.storage.WithUser(ctx, userID, func(udb *sql.DB) error {
		j, err := s.store.Get(ctx, udb, jobID)
		if err != nil {
			return err
		}
		job = j
		return nil
	}); err != nil {
		return err
	}
	s.launch(userID, job, "manual")
	return nil
}

// run executes one job, records the outcome, and notifies on change/error.
func (s *Scheduler) run(ctx context.Context, userID int64, job types.CronJob) {
	start := time.Now()
	err := s.storage.WithUser(ctx, userID, func(udb *sql.DB) error {
		username, role, _ := s.identity(ctx, userID)

		var output string
		var runErr error
		switch job.Kind {
		case types.CronKindJS:
			output, runErr = s.agent.RunCronJS(ctx, userID, username, udb, job)
		case types.CronKindSkill:
			output, runErr = s.agent.RunCronSkill(ctx, userID, username, role, udb, job)
		default:
			runErr = fmt.Errorf("unknown cron kind %q", job.Kind)
		}

		status := "ok"
		if runErr != nil {
			status = "error"
			if output == "" {
				output = runErr.Error()
			} else {
				output += "\n" + runErr.Error()
			}
		}
		output = truncate(output, maxOutput)

		// A "no result" run (empty output or the NO_UPDATE sentinel) is a no-op: it
		// never notifies, and it keeps the previous meaningful output as the change
		// baseline so a later real finding still diffs against it (and a flapping
		// "nothing → something → nothing" cycle doesn't re-alert).
		noop := status == "ok" && isNoResult(output)

		// Notify only on a real change between two *successful*, non-no-op runs.
		// This avoids flapping: transient errors (fetch failures, anti-bot blocks,
		// selector misses) don't alert, the first run doesn't alert (nothing to
		// compare), recovering from an error doesn't read as a change, and a
		// "nothing to report" run doesn't alert at all.
		notifyNow := job.Notify && s.dispatch != nil &&
			status == "ok" && job.LastStatus == "ok" && !noop && output != job.LastOutput

		var recErr error
		if noop {
			recErr = s.store.RecordRunStatus(ctx, udb, job.ID, time.Now(), status)
		} else {
			recErr = s.store.RecordRun(ctx, udb, job.ID, time.Now(), status, output)
		}
		if recErr != nil {
			log.Printf("cron: record run for job %d (user %d): %v", job.ID, userID, recErr)
		}
		if notifyNow {
			email, _, _ := s.identity(ctx, userID)
			if derr := s.dispatch.Deliver(ctx, udb, email, job.NotifyChannel, "cron", "Cron: "+job.Name, truncate(output, 500)); derr != nil {
				log.Printf("cron: deliver notification for job %d (user %d) via %s: %v", job.ID, userID, job.NotifyChannel, derr)
			} else {
				log.Printf("cron: notified user %d of change in job %d (%q) via %s", userID, job.ID, job.Name, job.NotifyChannel)
			}
		}
		log.Printf("cron: ran job %d (%q) for user %d: status=%s, %d bytes in %s", job.ID, job.Name, userID, status, len(output), time.Since(start).Round(time.Millisecond))
		return nil
	})
	if err != nil {
		log.Printf("cron: run job %d (user %d): %v", job.ID, userID, err)
	}
}

// identity looks up a user's email (login identity) and role from the system db.
func (s *Scheduler) identity(ctx context.Context, userID int64) (string, string, error) {
	var email, role string
	err := s.storage.System.QueryRowContext(ctx,
		`SELECT email, role FROM users WHERE id = ?`, userID).Scan(&email, &role)
	return email, role, err
}

// isNoResult reports whether a run's output declares "nothing to report": empty
// (e.g. a JS job returning ""/null) or the NO_UPDATE sentinel as the whole output
// or its first line (skill jobs are instructed to reply with it).
func isNoResult(output string) bool {
	t := strings.TrimSpace(output)
	if t == "" {
		return true
	}
	first := t
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		first = strings.TrimSpace(t[:i])
	}
	return strings.EqualFold(first, types.CronNoUpdateSentinel)
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
