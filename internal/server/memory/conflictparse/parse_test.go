package conflictparse

import "testing"

func TestFlagged(t *testing.T) {
	t.Parallel()
	got, ok := Flagged(`{"judgments":[
		{"other_id":5,"relationship":"conflicts","confidence":9,"reason":"Dark vs light mode"},
		{"other_id":6,"relationship":"none","confidence":10,"reason":"unrelated"},
		{"other_id":7,"relationship":"duplicate","confidence":4,"reason":"low conf"},
		{"other_id":8,"relationship":"duplicate","confidence":8,"reason":"same fact"}
	]}`)
	if !ok {
		t.Fatal("parse failed")
	}
	if len(got) != 2 {
		t.Fatalf("len=%d want 2: %#v", len(got), got)
	}
	if got[0].OtherID != 5 || got[0].Relationship != "conflicts" {
		t.Fatalf("first=%#v", got[0])
	}
}

func TestFlaggedEmpty(t *testing.T) {
	t.Parallel()
	got, ok := Flagged(`{"judgments":[]}`)
	if !ok || len(got) != 0 {
		t.Fatalf("got %#v ok=%v", got, ok)
	}
}
