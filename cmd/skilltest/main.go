// Command skilltest exercises the scoped-skill store end to end against real
// sqlite DBs (no LLM): seeding baked skills into the shared DB, seed
// idempotency + edit preservation, scope precedence (project → shared → user),
// and single-file writes. Run from the repo root:
//
//	go run -tags sqlite_fts5 ./cmd/skilltest
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	harlequin "github.com/ivoras/harlequin"
	"github.com/ivoras/harlequin/internal/server/db"
	"github.com/ivoras/harlequin/internal/server/skills"
)

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", a...)
	os.Exit(1)
}

func main() {
	ctx := context.Background()
	dir, _ := os.MkdirTemp("", "skilltest")
	defer os.RemoveAll(dir)

	shared, err := db.Open(filepath.Join(dir, "shared.db"), db.Shared, 8)
	if err != nil {
		fail("open shared: %v", err)
	}
	defer shared.Close()
	user, err := db.Open(filepath.Join(dir, "user.db"), db.User, 8)
	if err != nil {
		fail("open user: %v", err)
	}
	defer user.Close()
	proj, err := db.Open(filepath.Join(dir, "project.db"), db.Project, 8)
	if err != nil {
		fail("open project: %v", err)
	}
	defer proj.Close()

	m := skills.NewManager(shared, harlequin.BakedFS(), nil, nil)

	// --- Seed baked skills into shared ---
	if err := m.Seed(ctx); err != nil {
		fail("seed: %v", err)
	}
	const baked = "example-greeter"
	files, scope, err := m.ResolveRawFiles(ctx, user, nil, baked)
	if err != nil || scope != skills.ScopeShared {
		fail("baked %q not in shared after seed: scope=%q err=%v", baked, scope, err)
	}
	if _, ok := files["SKILL.md"]; !ok {
		fail("baked %q has no SKILL.md", baked)
	}
	fmt.Printf("ok: seeded %q into shared (%d files)\n", baked, len(files))

	// --- Edit a shared skill, then re-seed: the edit must be preserved ---
	edited := files
	edited["SKILL.md"] = "---\nname: " + baked + "\ndescription: EDITED\n---\nlocal edit\n"
	if err := m.Save(ctx, shared, baked, 1, edited); err != nil {
		fail("save shared edit: %v", err)
	}
	if err := m.Seed(ctx); err != nil {
		fail("re-seed: %v", err)
	}
	after, _, err := m.ResolveRawFiles(ctx, user, nil, baked)
	if err != nil {
		fail("resolve after re-seed: %v", err)
	}
	if after["SKILL.md"] != edited["SKILL.md"] {
		fail("re-seed clobbered a local edit:\n%s", after["SKILL.md"])
	}
	fmt.Println("ok: re-seed preserved the local edit")

	// --- Scope precedence: project → shared → user ---
	sk := func(desc string) map[string]string {
		return map[string]string{"SKILL.md": "---\nname: demo\ndescription: " + desc + "\n---\nb\n"}
	}
	if err := m.Save(ctx, user, "demo", 1, sk("user")); err != nil {
		fail("save user demo: %v", err)
	}
	if _, s, _ := m.ResolveRawFiles(ctx, user, proj, "demo"); s != skills.ScopeUser {
		fail("demo should resolve from user, got %q", s)
	}
	if err := m.Save(ctx, shared, "demo", 1, sk("shared")); err != nil {
		fail("save shared demo: %v", err)
	}
	if _, s, _ := m.ResolveRawFiles(ctx, user, proj, "demo"); s != skills.ScopeShared {
		fail("demo should resolve from shared (shadows user), got %q", s)
	}
	if err := m.Save(ctx, proj, "demo", 1, sk("project")); err != nil {
		fail("save project demo: %v", err)
	}
	if _, s, _ := m.ResolveRawFiles(ctx, user, proj, "demo"); s != skills.ScopeProject {
		fail("demo should resolve from project, got %q", s)
	}
	fmt.Println("ok: scope precedence project → shared → user")

	// --- Single-file write into the user scope (a user-only skill, so nothing
	// shadows it). ---
	if err := m.Save(ctx, user, "solo", 1, sk("user-solo")); err != nil {
		fail("save user solo: %v", err)
	}
	if err := m.PutFile(ctx, user, user, proj, "solo", "lib/y.js", "// y", 1); err != nil {
		fail("putfile: %v", err)
	}
	content, s, err := m.GetFile(ctx, user, nil, "solo", "lib/y.js")
	if err != nil || s != skills.ScopeUser || content != "// y" {
		fail("getfile after putfile: content=%q scope=%q err=%v", content, s, err)
	}
	fmt.Println("ok: single-file write + read round-trip")

	fmt.Println("PASS")
}
