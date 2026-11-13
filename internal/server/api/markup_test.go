package api

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/config"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func serverWithMarkup(pct float64) *Server {
	return &Server{Cfg: &config.Config{Server: config.ServerConfig{OrgLLMMarkupPercent: pct}}}
}

func TestMarkup(t *testing.T) {
	if got := serverWithMarkup(0).markup(2); got != 2 {
		t.Errorf("0%%: %v", got)
	}
	if got := serverWithMarkup(5).markup(2); math.Abs(got-2.1) > 1e-12 {
		t.Errorf("5%%: %v", got)
	}
	if got := (&Server{}).markup(2); got != 2 { // no config at all
		t.Errorf("nil cfg: %v", got)
	}
	days := []types.UsageDay{{CostUSD: 10}, {CostUSD: 0}}
	serverWithMarkup(5).markupDays(days)
	if math.Abs(days[0].CostUSD-10.5) > 1e-12 || days[1].CostUSD != 0 {
		t.Errorf("days: %+v", days)
	}
}

func TestMarkupLogOnlyTouchesCosts(t *testing.T) {
	log := `{"ts":"t","type":"usage","data":{"model":"m","cost_usd":2,"prompt_tokens":7}}` + "\n" +
		`{"ts":"t","type":"usage","data":{"cost_usd":null}}` + "\n" +
		`{"ts":"t","type":"user_message","data":{"content":"cost_usd is not a field here"}}` + "\n"
	if got := string(serverWithMarkup(0).markupLog([]byte(log))); got != log {
		t.Errorf("0%% must pass the log through unchanged:\n%s", got)
	}
	lines := strings.Split(string(serverWithMarkup(5).markupLog([]byte(log))), "\n")
	var ev struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatal(err)
	}
	if c := ev.Data["cost_usd"].(float64); math.Abs(c-2.1) > 1e-12 || ev.Data["prompt_tokens"].(float64) != 7 || ev.Data["model"] != "m" {
		t.Errorf("rewritten usage line: %s", lines[0])
	}
	if lines[1] != `{"ts":"t","type":"usage","data":{"cost_usd":null}}` || !strings.Contains(lines[2], "not a field here") {
		t.Errorf("other lines changed: %q %q", lines[1], lines[2])
	}
}
