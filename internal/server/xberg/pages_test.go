package xberg

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// marker renders one page marker the way xberg's renderer emits it (the
// configured format, trimmed, as its own block).
func marker(page int) string {
	return "\n\n" + markerPrefix + itoa(page) + markerSuffix + "\n\n"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// checkStarts asserts that every page start indexes the text at the expected
// prefix — the property that actually matters, since pageFor maps a chunk's
// rune offset back to a page number through these.
func checkStarts(t *testing.T, text string, starts []int, want map[int]string) {
	t.Helper()
	runes := []rune(text)
	for page, prefix := range want {
		if page > len(starts) {
			t.Fatalf("page %d beyond mapping of %d pages", page, len(starts))
		}
		off := starts[page-1]
		if off > len(runes) {
			t.Fatalf("page %d start %d beyond text (%d runes)", page, off, len(runes))
		}
		if got := string(runes[off:]); !strings.HasPrefix(got, prefix) {
			t.Errorf("page %d starts at %q, want prefix %q", page, truncate(got, 40), prefix)
		}
	}
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func TestStripPageMarkers(t *testing.T) {
	text := marker(1) + "First page text." + marker(2) + "## Second page\n\nBody." + marker(3) + "Third."
	clean, starts, ok := stripPageMarkers(text)
	if !ok {
		t.Fatal("markers not recognised")
	}
	if strings.Contains(clean, markerPrefix) {
		t.Errorf("marker survived into cleaned text: %q", clean)
	}
	if want := "First page text.\n\n## Second page\n\nBody.\n\nThird."; clean != want {
		t.Errorf("clean text = %q, want %q", clean, want)
	}
	if len(starts) != 3 {
		t.Fatalf("got %d page starts, want 3", len(starts))
	}
	if starts[0] != 0 {
		t.Errorf("page 1 starts at %d, want 0", starts[0])
	}
	checkStarts(t, clean, starts, map[int]string{
		1: "First page",
		2: "## Second page",
		3: "Third.",
	})
}

// A page with no extractable text produces no marker of its own. It must still
// occupy a slot, pointing at the next page that does exist, so page numbering
// downstream stays aligned with the document's real pages.
func TestStripPageMarkersSkippedPage(t *testing.T) {
	text := marker(1) + "One." + marker(4) + "Four."
	clean, starts, ok := stripPageMarkers(text)
	if !ok {
		t.Fatal("markers not recognised")
	}
	if len(starts) != 4 {
		t.Fatalf("got %d page starts, want 4", len(starts))
	}
	checkStarts(t, clean, starts, map[int]string{
		1: "One.",
		2: "Four.", // page 2 had nothing: cite the next real page
		3: "Four.",
		4: "Four.",
	})
}

// Multi-byte text: offsets are runes, not bytes.
func TestStripPageMarkersRuneOffsets(t *testing.T) {
	text := marker(1) + "Přílohy — účet." + marker(2) + "Konec."
	clean, starts, ok := stripPageMarkers(text)
	if !ok {
		t.Fatal("markers not recognised")
	}
	want := utf8.RuneCountInString("Přílohy — účet.") + 2 // + the "\n\n" separator
	if starts[1] != want {
		t.Errorf("page 2 starts at rune %d, want %d", starts[1], want)
	}
	checkStarts(t, clean, starts, map[int]string{1: "Přílohy", 2: "Konec."})
}

func TestStripPageMarkersNone(t *testing.T) {
	text := "Just text, no markers.\n\nSecond paragraph."
	clean, starts, ok := stripPageMarkers(text)
	if ok {
		t.Error("reported markers in marker-free text")
	}
	if clean != text {
		t.Errorf("text was modified: %q", clean)
	}
	if starts != nil {
		t.Errorf("got page starts %v, want nil", starts)
	}
}

// An HTML comment that shares our prefix but carries no page number must not be
// mistaken for a marker, and must survive into the text.
func TestStripPageMarkersNonNumeric(t *testing.T) {
	text := marker(1) + "Before.\n\n<!-- PAGE break -->\n\nAfter."
	clean, starts, ok := stripPageMarkers(text)
	if !ok {
		t.Fatal("markers not recognised")
	}
	if !strings.Contains(clean, "<!-- PAGE break -->") {
		t.Errorf("non-marker comment was stripped: %q", clean)
	}
	if len(starts) != 1 {
		t.Errorf("got %d page starts, want 1", len(starts))
	}
}

func TestBoundaryStarts(t *testing.T) {
	content := "Page one.\n\nPage two.\n\nPage three."
	// Deliberately out of order: the server makes no ordering guarantee.
	bs := []pageBoundary{
		{ByteStart: 22, ByteEnd: 33, PageNumber: 3},
		{ByteStart: 0, ByteEnd: 9, PageNumber: 1},
		{ByteStart: 11, ByteEnd: 20, PageNumber: 2},
	}
	starts := boundaryStarts(content, bs)
	if len(starts) != 3 {
		t.Fatalf("got %d page starts, want 3", len(starts))
	}
	checkStarts(t, content, starts, map[int]string{
		1: "Page one.",
		2: "Page two.",
		3: "Page three.",
	})
}

// Byte offsets become rune offsets.
func TestBoundaryStartsMultibyte(t *testing.T) {
	content := "Přílohy.\n\nKonec."
	split := strings.Index(content, "Konec.")
	starts := boundaryStarts(content, []pageBoundary{
		{ByteStart: 0, ByteEnd: split, PageNumber: 1},
		{ByteStart: split, ByteEnd: len(content), PageNumber: 2},
	})
	if want := utf8.RuneCountInString(content[:split]); starts[1] != want {
		t.Errorf("page 2 starts at rune %d, want %d", starts[1], want)
	}
	checkStarts(t, content, starts, map[int]string{1: "Přílohy.", 2: "Konec."})
}

// Offsets that do not address the content string must be discarded, not sliced.
func TestBoundaryStartsOutOfRange(t *testing.T) {
	content := "Short."
	starts := boundaryStarts(content, []pageBoundary{
		{ByteStart: 0, ByteEnd: 6, PageNumber: 1},
		{ByteStart: 9999, ByteEnd: 10000, PageNumber: 2},
	})
	if len(starts) != 1 {
		t.Fatalf("got %d page starts, want 1 (out-of-range boundary dropped)", len(starts))
	}
	if starts[0] != 0 {
		t.Errorf("page 1 starts at %d, want 0", starts[0])
	}
}

func TestBoundaryStartsEmpty(t *testing.T) {
	if got := boundaryStarts("text", nil); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}
