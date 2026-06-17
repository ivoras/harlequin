// Command projecttest exercises the project registry (system DB), the per-project
// database tier, and the chatroom against real sqlite DBs, without an LLM. Run
// from the repo root:
//
//	go run -tags sqlite_fts5 ./cmd/projecttest
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/project"
	"github.com/ivoras/harlequin/internal/server/storage"
)

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", a...)
	os.Exit(1)
}

func main() {
	ctx := context.Background()
	dir, _ := os.MkdirTemp("", "projecttest")
	defer os.RemoveAll(dir)

	store, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 8)
	if err != nil {
		fail("storage: %v", err)
	}
	defer store.Close()

	// Two users.
	au := auth.NewStore(store.System)
	alice, err := au.CreateUser(ctx, "alice@x", "pw", "user")
	if err != nil {
		fail("create alice: %v", err)
	}
	bob, err := au.CreateUser(ctx, "bob@x", "pw", "user")
	if err != nil {
		fail("create bob: %v", err)
	}

	ps := project.NewStore(store.System)

	// Alice creates a project and is its first member.
	p, err := ps.Create(ctx, "Apollo", alice.ID)
	if err != nil {
		fail("create project: %v", err)
	}
	if len(p.Members) != 1 || p.Members[0].UserID != alice.ID {
		fail("creator should be sole member: %+v", p.Members)
	}
	fmt.Printf("[create] project #%d %q owner=%s\n", p.ID, p.Name, p.Members[0].Email)

	if m, _ := ps.IsMember(ctx, p.ID, bob.ID); m {
		fail("bob should not be a member yet")
	}

	// Alice invites Bob; Bob sees the invite and accepts.
	if err := ps.Invite(ctx, p.ID, bob.ID, alice.ID); err != nil {
		fail("invite: %v", err)
	}
	invites, err := ps.ListInvites(ctx, bob.ID)
	if err != nil || len(invites) != 1 || invites[0].ProjectID != p.ID {
		fail("bob should have 1 pending invite: %+v (%v)", invites, err)
	}
	fmt.Printf("[invite] bob invited to %q by %s\n", invites[0].ProjectName, invites[0].InvitedBy)
	if _, err := ps.Accept(ctx, invites[0].ID, bob.ID); err != nil {
		fail("accept: %v", err)
	}
	if m, _ := ps.IsMember(ctx, p.ID, bob.ID); !m {
		fail("bob should be a member after accepting")
	}
	if inv, _ := ps.ListInvites(ctx, bob.ID); len(inv) != 0 {
		fail("invite should be gone after accept")
	}

	// Both Alice and Bob list the project.
	for _, u := range []*struct {
		name string
		id   int64
	}{{"alice", alice.ID}, {"bob", bob.ID}} {
		ls, err := ps.List(ctx, u.id)
		if err != nil || len(ls) != 1 || ls[0].ID != p.ID {
			fail("%s should see 1 project: %+v (%v)", u.name, ls, err)
		}
	}
	fmt.Printf("[members] %d members; both see the project\n", len(p.Members)+1)

	// Chatroom in the project DB.
	if err := store.WithProject(ctx, p.ID, func(pdb *sql.DB) error {
		if _, e := ps.AddChatMessage(ctx, pdb, alice.ID, "hello team"); e != nil {
			return e
		}
		if _, e := ps.AddChatMessage(ctx, pdb, bob.ID, "hi alice"); e != nil {
			return e
		}
		msgs, e := ps.ChatMessages(ctx, pdb, 10)
		if e != nil {
			return e
		}
		if len(msgs) != 2 || msgs[0].Content != "hello team" || msgs[0].Email != "alice@x" {
			return fmt.Errorf("unexpected chat history: %+v", msgs)
		}
		fmt.Printf("[chat] %d messages; first from %s: %q\n", len(msgs), msgs[0].Email, msgs[0].Content)
		return nil
	}); err != nil {
		fail("chat: %v", err)
	}

	// The project DB file exists on disk.
	if _, err := os.Stat(store.ProjectDBPath(p.ID)); err != nil {
		fail("project.db missing: %v", err)
	}

	fmt.Println("OK")
}
