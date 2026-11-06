package documents

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"
)

// captureLog redirects the standard logger for the duration of a test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	flags, out := log.Flags(), log.Writer()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(out); log.SetFlags(flags) })
	return &buf
}

// Each phase line has to be placeable in time on its own: the server runs with
// log timestamps off by default, so a line carrying only a duration could not be
// correlated with anything else in the log.
func TestPhaseLogLinesCarryTimestamps(t *testing.T) {
	buf := captureLog(t)
	pl := NewPhaseLog("report.pdf")
	pl.Phase("extract", "1200 chars, 4 pages")
	pl.Done()

	out := buf.String()
	if !strings.Contains(out, `ingest "report.pdf" phase=extract`) {
		t.Errorf("phase line missing or mislabelled:\n%s", out)
	}
	for _, want := range []string{"start=", "dur=", "elapsed=", "1200 chars, 4 pages"} {
		if !strings.Contains(out, want) {
			t.Errorf("phase line missing %q:\n%s", want, out)
		}
	}
	// The timestamp must parse, or a log reader cannot reconstruct the timeline.
	line := firstLineWith(out, "phase=extract")
	ts := fieldValue(line, "start=")
	if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
		t.Errorf("start=%q is not RFC3339: %v", ts, err)
	}
}

// The summary must name every phase and the total, so one line explains where a
// slow ingestion spent its time.
func TestPhaseLogSummary(t *testing.T) {
	buf := captureLog(t)
	pl := NewPhaseLog("big.pdf")
	pl.Phase("extract")
	pl.Phase("embed-sentences", "1443 sentences")
	pl.Phase("embed-chunks", "700 chunks")
	pl.Done()

	line := firstLineWith(buf.String(), "complete")
	for _, want := range []string{"extract", "embed-sentences", "embed-chunks", "total=", "start=", "end="} {
		if !strings.Contains(line, want) {
			t.Errorf("summary missing %q: %s", want, line)
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, fieldValue(line, "end=")); err != nil {
		t.Errorf("end= is not RFC3339: %s", line)
	}
}

// Both upload paths defer Done, and the async one may also return early; a
// second call must not emit a second, contradictory summary.
func TestPhaseLogDoneIsIdempotent(t *testing.T) {
	buf := captureLog(t)
	pl := NewPhaseLog("x.pdf")
	pl.Phase("extract")
	pl.Done()
	pl.Done()
	if n := strings.Count(buf.String(), "complete"); n != 1 {
		t.Errorf("got %d summary lines, want 1", n)
	}
}

// A nil PhaseLog is the no-instrumentation case (any ingest path that does not
// set one up). It must be silently inert rather than panic.
func TestPhaseLogNilIsNoOp(t *testing.T) {
	buf := captureLog(t)
	var pl *PhaseLog
	pl.Phase("extract", "detail")
	pl.Relabel("y.pdf")
	pl.Done()
	if buf.Len() != 0 {
		t.Errorf("nil PhaseLog logged: %s", buf.String())
	}
}

// The log travels on the context so phases performed in other packages join one
// timeline; a context without one yields the inert nil.
func TestPhaseLogContextRoundTrip(t *testing.T) {
	pl := NewPhaseLog("a.pdf")
	if got := PhaseLogFrom(WithPhaseLog(context.Background(), pl)); got != pl {
		t.Error("PhaseLog did not survive the context round trip")
	}
	if got := PhaseLogFrom(context.Background()); got != nil {
		t.Errorf("bare context yielded %v, want nil", got)
	}
	if ctx := WithPhaseLog(context.Background(), nil); PhaseLogFrom(ctx) != nil {
		t.Error("storing a nil PhaseLog should leave the context without one")
	}
}

func TestPhaseLogRelabel(t *testing.T) {
	buf := captureLog(t)
	pl := NewPhaseLog("(upload)")
	pl.Relabel("real-name.pdf")
	pl.Relabel("") // empty must not erase the label
	pl.Phase("extract")
	if !strings.Contains(buf.String(), `"real-name.pdf"`) {
		t.Errorf("relabel did not take: %s", buf.String())
	}
}

func firstLineWith(out, want string) string {
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, want) {
			return ln
		}
	}
	return ""
}

// fieldValue reads a "key=value" field, where value runs to the next space.
func fieldValue(line, key string) string {
	i := strings.Index(line, key)
	if i < 0 {
		return ""
	}
	rest := line[i+len(key):]
	if j := strings.IndexByte(rest, ' '); j >= 0 {
		return rest[:j]
	}
	return rest
}

func TestPhaseLogFailReplacesComplete(t *testing.T) {
	buf := captureLog(t)
	pl := NewPhaseLog("doc.xlsx")
	pl.Phase("extract")
	pl.Fail("embed document: boom")
	pl.Done() // must stay silent after Fail
	out := buf.String()
	if !strings.Contains(out, `FAILED after phase=extract`) || !strings.Contains(out, "embed document: boom") {
		t.Errorf("missing failure line: %s", out)
	}
	if strings.Contains(out, "complete") {
		t.Errorf("a failed ingestion must not log complete: %s", out)
	}
}
