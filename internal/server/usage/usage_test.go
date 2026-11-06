package usage

import (
	"context"
	"testing"

	"github.com/ivoras/harlequin/internal/server/db"
)

// Daily groups by local calendar day: a call at 23:30 UTC belongs to the next
// day for a UTC+1 viewer.
func TestDailyGroupsByLocalDay(t *testing.T) {
	d, err := db.Open(":memory:", db.User, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, r := range []struct {
		at   string
		cost float64
	}{{"2026-03-01 10:00:00", 1}, {"2026-03-01 23:30:00", 2}, {"2026-03-02 08:00:00", 4}} {
		if _, err := d.Exec(`INSERT INTO usage(provider, model, prompt_tokens, completion_tokens, est_cost_usd, created_at) VALUES ('p','m',10,1,?,?)`, r.cost, r.at); err != nil {
			t.Fatal(err)
		}
	}
	s := NewStore(nil)
	utc, _ := s.Daily(context.Background(), d, 0)
	cet, _ := s.Daily(context.Background(), d, 60)
	if len(utc) != 2 || utc[0].CostUSD != 3 || utc[1].CostUSD != 4 {
		t.Fatalf("utc days %+v", utc)
	}
	if len(cet) != 2 || cet[0].Day != "2026-03-01" || cet[0].CostUSD != 1 || cet[1].CostUSD != 6 || cet[1].Calls != 2 {
		t.Fatalf("utc+1 days %+v", cet)
	}
}
