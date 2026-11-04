package loopdetect

import (
	"fmt"
	"strings"
	"testing"
)

// feed streams s in small pieces (like token deltas) and reports whether, and
// after how many bytes, the detector fired.
func feed(s string) (bool, int) {
	var d Detector
	for i := 0; i < len(s); i += 37 {
		end := min(i+37, len(s))
		if hit, _ := d.Feed(s[i:end]); hit {
			return true, end
		}
	}
	return false, len(s)
}

func natural(n int) string {
	var sb strings.Builder
	for i := 0; sb.Len() < n; i++ {
		fmt.Fprintf(&sb, "Step %d: article %d of the act refers to obligation %d, so the amendment %d applies when condition %d holds.\n", i, i*7+3, i*13%97, i*31%211, i*5%17)
	}
	return sb.String()
}

func TestDetectsRepeatedParagraph(t *testing.T) {
	para := "Wait, I need to reconsider whether the amendments apply to the original text. Let me check article 399.a again and then decide on the structure of the combined document, because the user wants everything.\n"
	text := natural(8000) + strings.Repeat(para, 60)
	hit, at := feed(text)
	if !hit {
		t.Fatal("repeated paragraph not detected")
	}
	if at > 8000+len(para)*(minRepeats+2)+checkEvery*2 {
		t.Errorf("detected too late, after %d bytes", at)
	}
}

func TestDetectsRepeatedLinesAndRuns(t *testing.T) {
	if hit, _ := feed(natural(7000) + strings.Repeat("Let me think about this again.\n", 400)); !hit {
		t.Error("repeated short lines not detected")
	}
	if hit, _ := feed(natural(7000) + strings.Repeat(".", 4000)); !hit {
		t.Error("repeated character run not detected")
	}
}

func TestNaturalTextAndShortOutputPass(t *testing.T) {
	if hit, _ := feed(natural(200_000)); hit {
		t.Error("false positive on long varied text")
	}
	if hit, _ := feed(strings.Repeat("same line over and over\n", 100)); hit && len(strings.Repeat("same line over and over\n", 100)) < minTotal {
		t.Error("fired before the minimum output size")
	}
}

func TestWindowStaysBounded(t *testing.T) {
	var d Detector
	for i := 0; i < 100; i++ {
		d.Feed(natural(5000))
	}
	if len(d.buf) > maxBuf {
		t.Errorf("buffer grew to %d", len(d.buf))
	}
}

// Reasoning that quotes the same source passage a few times while re-checking
// it is not a loop (a real run was wrongly stopped for quoting a law article 4x).
func TestRepeatedQuoteIsNotALoop(t *testing.T) {
	quote := "(3) Oštećenik ima, po odredbama ovoga odsjeka, pravo na naknadu štete prouzročenu uništenjem ili oštećenjem stvari različite od neispravnog proizvoda, ako se radi o takvoj vrsti stvari koja je uobičajeno namijenjena za osobnu uporabu te"
	var sb strings.Builder
	for i := 0; i < 6; i++ {
		sb.WriteString(natural(3000))
		sb.WriteString("\nLet me re-check what the page says: \"" + quote + "\" — so the amount is as stated.\n")
	}
	if hit, _ := feed(sb.String()); hit {
		t.Error("six quotes of one passage amid varied reasoning were flagged as a loop")
	}
}

// A long-period loop (several KB per cycle) must still be caught within the window.
func TestLongPeriodLoopDetected(t *testing.T) {
	block := natural(5000)
	if hit, _ := feed(natural(7000) + strings.Repeat(block, 14)); !hit {
		t.Error("5 KB-period loop not detected")
	}
}
