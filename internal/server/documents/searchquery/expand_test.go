package searchquery

import (
	"strings"
	"testing"
)

func TestFTSHQ(t *testing.T) {
	t.Parallel()
	got := FTS("EU HQ")
	want := "EU OR European OR \"European Union\" OR HQ OR headquarters OR seat OR Brussels OR Strasbourg OR Luxembourg OR location"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestFTSLongUnchanged(t *testing.T) {
	t.Parallel()
	q := "one two three four five"
	if got := FTS(q); got != q {
		t.Fatalf("long query changed: %q", got)
	}
}

func TestFTSSingleToken(t *testing.T) {
	t.Parallel()
	got := FTS("HQ")
	if got != "HQ OR headquarters OR seat OR Brussels OR Strasbourg OR Luxembourg OR location" {
		t.Fatalf("got %q", got)
	}
}

func TestFTSThreeWordsUnchanged(t *testing.T) {
	t.Parallel()
	q := "location of seats"
	if got := FTS(q); got != q {
		t.Fatalf("unchanged query modified: %q", got)
	}
}

func TestFocusedFTSHQ(t *testing.T) {
	t.Parallel()
	got := FocusedFTS("EU HQ")
	want := "headquarters OR Brussels OR Strasbourg OR Luxembourg OR location"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestFocusedFTSUnchanged(t *testing.T) {
	t.Parallel()
	if got := FocusedFTS("location of seats"); got != "" {
		t.Fatalf("got %q want empty", got)
	}
}

func TestEmbed(t *testing.T) {
	t.Parallel()
	got := Embed("EU HQ")
	for _, sub := range []string{"EU", "HQ", "headquarters", "seat", "Brussels"} {
		if !strings.Contains(got, sub) {
			t.Fatalf("missing %q in %q", sub, got)
		}
	}
}
