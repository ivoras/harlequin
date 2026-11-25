package project

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ivoras/harlequin/internal/server/db"
)

// Moving a session into a project keeps its messages' reasoning (the model gets
// it back with the history).
func TestMoveSessionKeepsReasoning(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	udb, err := db.Open(filepath.Join(dir, "user.db"), db.User, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer udb.Close()
	pdb, err := db.Open(filepath.Join(dir, "project.db"), db.Project, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer pdb.Close()
	res, err := udb.Exec(`INSERT INTO sessions(title) VALUES ('t')`)
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := res.LastInsertId()
	if _, err := udb.Exec(`INSERT INTO messages(session_id, role, content, reasoning) VALUES (?, 'assistant', 'a', 'because')`, sid); err != nil {
		t.Fatal(err)
	}
	newID, err := NewStore(nil).MoveSessionToProject(ctx, udb, pdb, 1, sid, 1)
	if err != nil {
		t.Fatal(err)
	}
	var r string
	if err := pdb.QueryRow(`SELECT reasoning FROM messages WHERE session_id = ?`, newID).Scan(&r); err != nil || r != "because" {
		t.Fatalf("reasoning = %q, %v", r, err)
	}
}
