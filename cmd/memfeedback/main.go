// Command memfeedback summarizes the memory_feedback events in the session logs,
// so you can judge whether explicit memory citation (the memory_feedback tool) is a
// trustworthy usefulness signal on your models before any learning is driven from
// it. It reports how often the model calls memory_feedback when memory was
// searched, how selectively it cites, and how often it cites ids that were never
// actually recalled.
//
//	go run ./cmd/memfeedback [-dir data/sessions]
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ivoras/harlequin/internal/server/sessionlog"
)

func main() {
	dir := flag.String("dir", "data/sessions", "directory of session .jsonl logs to scan (recursively)")
	flag.Parse()

	var (
		turns           int // memory_feedback events (turns where memory was searched)
		called          int // turns where the model called memory_feedback
		calledNoneValid int // called, but cited zero ids that were actually recalled
		turnsInvalid    int // turns that cited at least one non-recalled (bogus) id
		sumRecalled     int
		sumUseful       int
		sumInvalid      int
	)

	err := filepath.WalkDir(*dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		f, e := os.Open(path)
		if e != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for sc.Scan() {
			var ev sessionlog.Event
			if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Type != sessionlog.TypeMemoryFeedback {
				continue
			}
			turns++
			rc := intOf(ev.Data["recalled_count"])
			uc := intOf(ev.Data["useful_count"])
			inv := intOf(ev.Data["invalid_cited"])
			tc, _ := ev.Data["tool_called"].(bool)
			sumRecalled += rc
			sumUseful += uc
			sumInvalid += inv
			if tc {
				called++
				if uc == 0 {
					calledNoneValid++
				}
			}
			if inv > 0 {
				turnsInvalid++
			}
		}
		if e := sc.Err(); e != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", path, e)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan:", err)
		os.Exit(1)
	}

	if turns == 0 {
		fmt.Printf("no memory_feedback events found under %q\n", *dir)
		return
	}

	fmt.Printf("memory_feedback summary (%s)\n", *dir)
	fmt.Printf("  turns with memory searched : %d\n", turns)
	fmt.Printf("  memory_feedback called     : %d (%s of those turns)\n", called, pct(called, turns))
	fmt.Printf("  memories recalled (total)  : %d  (avg %.1f/turn)\n", sumRecalled, avg(sumRecalled, turns))
	fmt.Printf("  cited as useful (total)    : %d  (avg %.1f/turn)\n", sumUseful, avg(sumUseful, turns))
	fmt.Println()
	fmt.Printf("  call rate          : %s  — how often it reports usefulness when it could\n", pct(called, turns))
	fmt.Printf("  selectivity        : %s  — useful / recalled (lower = more selective; ~100%% = cites everything)\n", pct(sumUseful, sumRecalled))
	fmt.Printf("  citation precision : %s  — valid / (valid+bogus) cited ids\n", pct(sumUseful, sumUseful+sumInvalid))
	fmt.Printf("  hallucinated cites : %d across %s of called turns cited a non-recalled id\n", sumInvalid, pct(turnsInvalid, called))
	fmt.Printf("  called, none valid : %s of called turns cited only bogus/zero ids\n", pct(calledNoneValid, called))
}

func intOf(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

func pct(a, b int) string {
	if b == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(a)/float64(b))
}

func avg(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}
