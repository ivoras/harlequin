// Command crontest exercises the cron Store (CRUD + scheduling math against a
// real per-user sqlite DB) and the JS execution path (agent.RunCronJS) without
// an LLM. Run from the repo root:
//
//	go run -tags sqlite_fts5 ./cmd/crontest
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ivoras/harlequin/internal/server/agent"
	"github.com/ivoras/harlequin/internal/server/cron"
	"github.com/ivoras/harlequin/internal/server/db"
	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", a...)
	os.Exit(1)
}

func main() {
	ctx := context.Background()
	dir, _ := os.MkdirTemp("", "crontest")
	defer os.RemoveAll(dir)

	udb, err := db.Open(filepath.Join(dir, "user.db"), db.User, 8)
	if err != nil {
		fail("open user db: %v", err)
	}
	defer udb.Close()

	store := cron.NewStore()

	// --- Store CRUD + scheduling math ---
	job, err := store.Create(ctx, udb, types.CreateCronJobRequest{
		Name:   "fzoeu",
		Spec:   "@every 1m",
		Kind:   types.CronKindJS,
		Target: "skill://web-monitor/lib/check.js",
		Input:  `{"name":"fzoeu"}`,
	})
	if err != nil {
		fail("create: %v", err)
	}
	if job.ID == 0 || job.NextRunAt == nil {
		fail("created job missing id/next_run_at: %+v", job)
	}
	if !job.NextRunAt.After(time.Now()) {
		fail("next_run_at should be in the future: %v", job.NextRunAt)
	}
	fmt.Printf("[create] #%d %s next=%s\n", job.ID, job.Name, job.NextRunAt.Format(time.RFC3339))

	// Not due now, due once its scheduled minute has passed.
	if due, _ := store.DueJobs(ctx, udb, time.Now()); len(due) != 0 {
		fail("job should not be due now, got %d", len(due))
	}
	if due, _ := store.DueJobs(ctx, udb, time.Now().Add(2*time.Minute)); len(due) != 1 {
		fail("job should be due in 2m, got %d", len(due))
	}
	fmt.Println("[due] scheduling window correct")

	// Invalid spec is rejected.
	if _, err := store.Create(ctx, udb, types.CreateCronJobRequest{Name: "bad", Spec: "nonsense", Kind: types.CronKindJS, Target: "x"}); err == nil {
		fail("invalid spec should be rejected")
	}

	// Disable → not due even in the future.
	if err := store.SetEnabled(ctx, udb, job.ID, false); err != nil {
		fail("disable: %v", err)
	}
	if due, _ := store.DueJobs(ctx, udb, time.Now().Add(2*time.Minute)); len(due) != 0 {
		fail("disabled job should not be due, got %d", len(due))
	}
	fmt.Println("[disable] disabled job is not due")

	// RecordRun persists status/output.
	if err := store.RecordRun(ctx, udb, job.ID, time.Now(), "ok", "No change: 3 items"); err != nil {
		fail("record run: %v", err)
	}
	got, _ := store.Get(ctx, udb, job.ID)
	if got.LastStatus != "ok" || got.LastOutput != "No change: 3 items" || got.LastRunAt == nil {
		fail("record run not persisted: %+v", got)
	}
	fmt.Println("[record] last run persisted")

	// --- JS execution path (agent.RunCronJS) without an LLM ---
	ag := &agent.Agent{Runner: jsrun.New(jsrun.Options{}), DataDir: dir}
	jsJob := types.CronJob{
		Kind:   types.CronKindJS,
		Target: `println("hello " + args.who); storage.write("note.txt", "ran");`,
		Input:  `{"who":"world"}`,
	}
	out, err := ag.RunCronJS(ctx, 1, "tester", udb, jsJob)
	if err != nil {
		fail("RunCronJS: %v", err)
	}
	if out != "hello world" {
		fail("RunCronJS output = %q, want %q", out, "hello world")
	}
	// The job's storage.write must have landed in the per-user storage dir.
	notePath := filepath.Join(dir, "users", "1", ".storage", "note.txt")
	if b, err := os.ReadFile(notePath); err != nil || string(b) != "ran" {
		fail("storage.write did not persist to %s: %v", notePath, err)
	}
	fmt.Println("[runjs] inline JS ran with args and wrote to per-user storage")

	// Delete.
	if err := store.Delete(ctx, udb, job.ID); err != nil {
		fail("delete: %v", err)
	}
	if list, _ := store.List(ctx, udb); len(list) != 0 {
		fail("list should be empty after delete, got %d", len(list))
	}

	fmt.Println("\nPASS: cron store + scheduling + JS execution")
}
