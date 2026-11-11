package usage

import (
	"context"
	"testing"

	"github.com/ivoras/harlequin/internal/server/db"
)

// Calls with no acting user land in the system db and show up in daily totals.
func TestRecordUnattributedAndDaily(t *testing.T) {
	sys, err := db.Open(":memory:", db.System, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer sys.Close()
	s := NewStore(nil)
	ctx := context.Background()
	c := 0.25
	if err := s.RecordUnattributed(ctx, sys, "chat", "m", 100, 40, 5, &c); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUnattributed(ctx, sys, "chat", "m", 10, 0, 1, &c); err != nil {
		t.Fatal(err)
	}
	days, err := s.DailyUnattributed(ctx, sys, 0)
	if err != nil || len(days) != 1 || days[0].Calls != 2 || days[0].CostUSD != 0.5 || days[0].PromptTokens != 110 || days[0].CachedTokens != 40 {
		t.Fatalf("days=%+v err=%v", days, err)
	}
}
