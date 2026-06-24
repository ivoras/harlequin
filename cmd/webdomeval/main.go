// Command webdomeval drives the configured local LLM to check whether a given
// WebFetchDOM JSON result lets a small model perform a generic list operation
// over a page (read / count / pick items) — not any number- or price-specific
// task. It auto-picks the most descriptive repeating list, renders the tool's
// records JSON (capped to the result cap), and asks the model to count the items
// and name a couple, so changes to the result encoding can be regression-checked
// end to end. Requires the local model from server.yaml at /v1/chat/completions.
//
//	go run -tags sqlite_fts5 ./cmd/webdomeval <file.html>
package main

import (
	"bytes"; "context"; "encoding/json"; "fmt"; "io"; "net/http"; "os"; "strings"; "time"
	"github.com/ivoras/harlequin/internal/server/dom"
)

const llmURL = "http://127.0.0.1:2234/v1/chat/completions"
const resultCap = 20000

func capTo(s string) string { if len(s) > resultCap { return s[:resultCap] + "\n…[truncated]" }; return s }

func chat(system, user string) string {
	body, _ := json.Marshal(map[string]any{"model": "local-model", "temperature": 0,
		"messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}}})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second); defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", llmURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req); if err != nil { return "ERR: " + err.Error() }
	defer resp.Body.Close(); b, _ := io.ReadAll(resp.Body)
	var r struct{ Choices []struct{ Message struct{ Content string } `json:"message"` } `json:"choices"` }
	json.Unmarshal(b, &r); if len(r.Choices) == 0 { return "NOCHOICE" }
	return strings.TrimSpace(r.Choices[0].Message.Content)
}

func main() {
	page, _ := os.ReadFile(os.Args[1]); d, _ := dom.Parse(page)
	// generic pick: the most descriptive repeating list (longest sample, count>=3)
	groups := d.RepeatingGroups(3, 30, 200); best, bl := "", -1
	for _, g := range groups {
		if g.Count >= 3 && len(g.Sample) > bl { best, bl = g.Selector, len(g.Sample) }
	}
	recs, _ := d.Records(best, 40, 110)
	for i := range recs { recs[i].Path = "" } // tool drops path from bulk records
	j, _ := json.Marshal(recs)

	fmt.Printf("list selector=%q  items=%d  records=%dB (fits=%v)\n\n", best, len(recs), len(j), len(j) <= resultCap)
	sys := `You are given a web page's list as JSON records (one per item, with text). ` +
		`Answer ONLY with compact JSON {"count":<number of items>,"first":"<short name of the first item>","last":"<short name of the last item>"}.`
	fmt.Println("[records JSON — generic read/count]\n  ", chat(sys, "Records:\n"+capTo(string(j))))
}
