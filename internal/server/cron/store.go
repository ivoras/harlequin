// Package cron stores and runs per-user scheduled jobs. The Store is stateless
// CRUD over a user's database (like the conversation/notify stores); the
// Scheduler ticks once a minute and dispatches due jobs to the agent.
package cron

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/server/cron/cronspec"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// Store is stateless CRUD for cron jobs; methods take the user's DB handle.
type Store struct{}

// NewStore constructs a cron Store.
func NewStore() *Store { return &Store{} }

const selectCols = `id, name, spec, kind, target, prompt, input, enabled, notify,
	notify_channel, next_run_at, last_run_at, last_status, last_output, created_at`

// Create validates and inserts a job, computing its first next_run_at.
func (s *Store) Create(ctx context.Context, db *sql.DB, req types.CreateCronJobRequest) (types.CronJob, error) {
	if strings.TrimSpace(req.Name) == "" {
		return types.CronJob{}, fmt.Errorf("name is required")
	}
	if req.Kind != types.CronKindJS && req.Kind != types.CronKindSkill {
		return types.CronJob{}, fmt.Errorf("kind must be %q or %q", types.CronKindJS, types.CronKindSkill)
	}
	if err := cronspec.Valid(req.Spec); err != nil {
		return types.CronJob{}, err
	}
	if strings.TrimSpace(req.Target) == "" {
		return types.CronJob{}, fmt.Errorf("target is required (js: script/URI; skill: skill name)")
	}
	enabled := req.Enabled == nil || *req.Enabled
	notify := req.Notify == nil || *req.Notify
	channel := normalizeChannel(req.NotifyChannel)
	var next any
	if enabled {
		if n, err := cronspec.Next(req.Spec, time.Now()); err == nil {
			next = n.Unix()
		}
	}
	res, err := db.ExecContext(ctx,
		`INSERT INTO cron_jobs(name, spec, kind, target, prompt, input, enabled, notify, notify_channel, next_run_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		req.Name, req.Spec, req.Kind, req.Target, req.Prompt, req.Input,
		b2i(enabled), b2i(notify), channel, next)
	if err != nil {
		return types.CronJob{}, err
	}
	id, _ := res.LastInsertId()
	return s.Get(ctx, db, id)
}

// Get returns one job by id.
func (s *Store) Get(ctx context.Context, db *sql.DB, id int64) (types.CronJob, error) {
	row := db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM cron_jobs WHERE id = ?`, id)
	return scanJob(row)
}

// List returns all of the user's jobs, newest first.
func (s *Store) List(ctx context.Context, db *sql.DB) ([]types.CronJob, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+selectCols+` FROM cron_jobs ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.CronJob
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// DueJobs returns the enabled jobs whose next_run_at has arrived (or is unset).
func (s *Store) DueJobs(ctx context.Context, db *sql.DB, now time.Time) ([]types.CronJob, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT `+selectCols+` FROM cron_jobs
		 WHERE enabled = 1 AND (next_run_at IS NULL OR next_run_at <= ?)
		 ORDER BY id`, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.CronJob
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Update applies the non-nil fields of req, re-validating and recomputing
// next_run_at when the spec or enabled state changes.
func (s *Store) Update(ctx context.Context, db *sql.DB, id int64, req types.UpdateCronJobRequest) (types.CronJob, error) {
	cur, err := s.Get(ctx, db, id)
	if err != nil {
		return types.CronJob{}, err
	}
	if req.Name != nil {
		cur.Name = *req.Name
	}
	if req.Kind != nil {
		if *req.Kind != types.CronKindJS && *req.Kind != types.CronKindSkill {
			return types.CronJob{}, fmt.Errorf("kind must be %q or %q", types.CronKindJS, types.CronKindSkill)
		}
		cur.Kind = *req.Kind
	}
	if req.Spec != nil {
		if err := cronspec.Valid(*req.Spec); err != nil {
			return types.CronJob{}, err
		}
		cur.Spec = *req.Spec
	}
	if req.Target != nil {
		cur.Target = *req.Target
	}
	if req.Prompt != nil {
		cur.Prompt = *req.Prompt
	}
	if req.Input != nil {
		cur.Input = *req.Input
	}
	if req.Notify != nil {
		cur.Notify = *req.Notify
	}
	if req.NotifyChannel != nil {
		cur.NotifyChannel = normalizeChannel(*req.NotifyChannel)
	}
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	// Recompute the next run from the (possibly new) spec when the job is enabled.
	var next any
	if cur.Enabled {
		if n, err := cronspec.Next(cur.Spec, time.Now()); err == nil {
			next = n.Unix()
		}
	}
	_, err = db.ExecContext(ctx,
		`UPDATE cron_jobs SET name=?, spec=?, kind=?, target=?, prompt=?, input=?,
		 enabled=?, notify=?, notify_channel=?, next_run_at=? WHERE id=?`,
		cur.Name, cur.Spec, cur.Kind, cur.Target, cur.Prompt, cur.Input,
		b2i(cur.Enabled), b2i(cur.Notify), normalizeChannel(cur.NotifyChannel), next, id)
	if err != nil {
		return types.CronJob{}, err
	}
	return s.Get(ctx, db, id)
}

// SetEnabled toggles a job, recomputing next_run_at when enabling.
func (s *Store) SetEnabled(ctx context.Context, db *sql.DB, id int64, enabled bool) error {
	_, err := s.Update(ctx, db, id, types.UpdateCronJobRequest{Enabled: &enabled})
	return err
}

// Reschedule sets a job's next_run_at (called by the scheduler before dispatch).
func (s *Store) Reschedule(ctx context.Context, db *sql.DB, id int64, next time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE cron_jobs SET next_run_at=? WHERE id=?`, next.Unix(), id)
	return err
}

// RecordRun stores the outcome of a run.
func (s *Store) RecordRun(ctx context.Context, db *sql.DB, id int64, ranAt time.Time, status, output string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE cron_jobs SET last_run_at=?, last_status=?, last_output=? WHERE id=?`,
		ranAt.Unix(), status, output, id)
	return err
}

// Delete removes a job.
func (s *Store) Delete(ctx context.Context, db *sql.DB, id int64) error {
	_, err := db.ExecContext(ctx, `DELETE FROM cron_jobs WHERE id=?`, id)
	return err
}

// --- scanning helpers ---

type scanner interface{ Scan(dest ...any) error }

func scanJob(row scanner) (types.CronJob, error) {
	var (
		j               types.CronJob
		enabled, notify int
		next, last      sql.NullInt64
	)
	if err := row.Scan(&j.ID, &j.Name, &j.Spec, &j.Kind, &j.Target, &j.Prompt, &j.Input,
		&enabled, &notify, &j.NotifyChannel, &next, &last, &j.LastStatus, &j.LastOutput, &j.CreatedAt); err != nil {
		return types.CronJob{}, err
	}
	j.Enabled = enabled != 0
	j.Notify = notify != 0
	j.NextRunAt = epochPtr(next)
	j.LastRunAt = epochPtr(last)
	return j, nil
}

func epochPtr(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.Unix(n.Int64, 0)
	return &t
}

// normalizeChannel validates a delivery channel, defaulting unknown/empty to
// in-app. Kept in sync with notifyx channel constants (avoids importing it here).
func normalizeChannel(c string) string {
	switch strings.ToLower(strings.TrimSpace(c)) {
	case "email":
		return "email"
	case "telegram":
		return "telegram"
	default:
		return "inapp"
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
