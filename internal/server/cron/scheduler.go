package cron

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/ivoras/harlequin/internal/server/agent"
	"github.com/ivoras/harlequin/internal/server/cron/cronspec"
	"github.com/ivoras/harlequin/internal/server/notify"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// maxOutput bounds the stored last_output / notification body of a run.
const maxOutput = 4000

// Scheduler ticks once a minute, finds due jobs across all users, and runs each
// in its own goroutine via the agent.
type Scheduler struct {
	storage *storage.Manager
	store   *Store
	agent   *agent.Agent
	notify  *notify.Store
	running sync.Map // key "userID:jobID" -> struct{}, in-flight guard
}

// NewScheduler wires a Scheduler.
func NewScheduler(st *storage.Manager, store *Store, ag *agent.Agent, nf *notify.Store) *Scheduler {
	return &Scheduler{storage: st, store: store, agent: ag, notify: nf}
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
			s.launch(userID, job)
		}
		return nil
	})
	if err != nil {
		log.Printf("cron: tick: %v", err)
	}
}

// launch starts a job in its own goroutine unless it is already running.
func (s *Scheduler) launch(userID int64, job types.CronJob) {
	key := fmt.Sprintf("%d:%d", userID, job.ID)
	if _, busy := s.running.LoadOrStore(key, struct{}{}); busy {
		log.Printf("cron: skipping job %d (user %d): previous run still in flight", job.ID, userID)
		return
	}
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
	s.launch(userID, job)
	return nil
}

// run executes one job, records the outcome, and notifies on change/error.
func (s *Scheduler) run(ctx context.Context, userID int64, job types.CronJob) {
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

		if err := s.store.RecordRun(ctx, udb, job.ID, time.Now(), status, output); err != nil {
			log.Printf("cron: record run for job %d (user %d): %v", job.ID, userID, err)
		}
		if job.Notify && s.notify != nil && shouldNotify(status, output, job.LastOutput) {
			_, _ = s.notify.Create(ctx, udb, types.Notification{
				Kind:        "cron",
				Title:       "Cron: " + job.Name,
				Description: truncate(output, 500),
			})
		}
		log.Printf("cron: ran job %d (%q) for user %d: status=%s, %d bytes", job.ID, job.Name, userID, status, len(output))
		return nil
	})
	if err != nil {
		log.Printf("cron: run job %d (user %d): %v", job.ID, userID, err)
	}
}

// shouldNotify suppresses steady-state noise: notify on error, or when the
// output differs from the previous run (so an unchanged "no change" check stays
// quiet but a real change surfaces).
func shouldNotify(status, output, lastOutput string) bool {
	return status == "error" || output != lastOutput
}

// identity looks up a user's username and role from the system database.
func (s *Scheduler) identity(ctx context.Context, userID int64) (string, string, error) {
	var username, role string
	err := s.storage.System.QueryRowContext(ctx,
		`SELECT username, role FROM users WHERE id = ?`, userID).Scan(&username, &role)
	return username, role, err
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
