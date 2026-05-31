package sessionlog

import "testing"

func TestTrajectoryFilename(t *testing.T) {
	t.Parallel()
	if got := TrajectoryFilename(7, 42); got != "00007.00042.jsonl" {
		t.Fatalf("got %q", got)
	}
	if got := TrajectoryFilename(100000, 1); got != "100000.00001.jsonl" {
		t.Fatalf("wide id: got %q", got)
	}
}

func TestTrajectoryPath(t *testing.T) {
	t.Parallel()
	want := "data/sessions/00007.00042.jsonl"
	if got := TrajectoryPath("data/sessions", 7, 42); got != want {
		t.Fatalf("got %q", got)
	}
}
