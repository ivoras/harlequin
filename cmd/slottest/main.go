// Command slottest verifies that memory.WriteSlot is idempotent: repeated keyed
// writes update one memory instead of piling up duplicates, and a changed value
// is reflected in both the memory content and the slot value. Throwaway harness
// (the memory package can't host _test.go due to the sqlite-vec link constraint).
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/ivoras/harlequin/internal/server/db"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/shared/types"
)

const dim = 8

// stubEmbedder returns a fixed-length zero vector so no network is needed.
type stubEmbedder struct{}

func (stubEmbedder) Dim() int { return dim }
func (stubEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	out := make([][]float32, len(inputs))
	for i := range out {
		out[i] = make([]float32, dim)
	}
	return out, nil
}

func main() {
	dir, err := os.MkdirTemp("", "slottest")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	udb, err := db.Open(filepath.Join(dir, "user.db"), db.User, dim)
	if err != nil {
		log.Fatal(err)
	}
	defer udb.Close()

	store := memory.NewStore(nil, stubEmbedder{})
	ctx := context.Background()
	const userID = 1
	const key = "user.birthday"

	id1, created1, err := store.WriteSlot(ctx, udb, "user", key, "December 5th", userID, false)
	must(err)
	fmt.Printf("write 1: id=%s created=%v\n", id1, created1)

	id2, created2, err := store.WriteSlot(ctx, udb, "user", key, "December 5th", userID, false)
	must(err)
	fmt.Printf("write 2 (same value): id=%s created=%v\n", id2, created2)

	id3, created3, err := store.WriteSlot(ctx, udb, "user", key, "May 1st", userID, false)
	must(err)
	fmt.Printf("write 3 (new value): id=%s created=%v\n", id3, created3)

	memCount := count(udb, `SELECT COUNT(*) FROM memories`)
	slotCount := count(udb, `SELECT COUNT(*) FROM memory_slots WHERE key = ?`, key)
	var val, content string
	err = udb.QueryRow(`SELECT s.value, m.content FROM memory_slots s JOIN memories m ON m.id = s.memory_id WHERE s.key = ?`, key).Scan(&val, &content)
	must(err)
	fmt.Printf("memories=%d slots=%d slot.value=%q memory.content=%q\n", memCount, slotCount, val, content)

	ok := created1 && !created2 && !created3 &&
		id1 == id2 && id2 == id3 &&
		memCount == 1 && slotCount == 1 &&
		val == "May 1st" && content == "May 1st"
	if !ok {
		log.Fatal("FAIL: WriteSlot not idempotent/single-valued")
	}

	// Seed a pre-existing duplicate (simulating the buggy u.15/u.16 case) and
	// confirm the next write collapses them to one.
	dup, err := store.Add(ctx, udb, types.CreateMemoryRequest{Scope: "user", Content: "May 1st", Source: "tool"}, userID)
	must(err)
	must(store.AddSlot(ctx, udb, dup.ID, key, "May 1st"))
	fmt.Printf("seeded duplicate %s; slots now=%d\n", dup.ID, count(udb, `SELECT COUNT(*) FROM memory_slots WHERE key = ?`, key))

	id4, created4, err := store.WriteSlot(ctx, udb, "user", key, "June 5th", userID, false)
	must(err)
	slotCount = count(udb, `SELECT COUNT(*) FROM memory_slots WHERE key = ?`, key)
	fmt.Printf("write 4 (collapse): id=%s created=%v slots=%d\n", id4, created4, slotCount)
	if created4 || slotCount != 1 {
		log.Fatal("FAIL: WriteSlot did not collapse duplicates")
	}
	fmt.Println("PASS")
}

func count(db *sql.DB, q string, args ...any) int {
	var n int
	must(db.QueryRow(q, args...).Scan(&n))
	return n
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
