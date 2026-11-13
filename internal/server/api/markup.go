package api

import (
	"bytes"
	"encoding/json"

	"github.com/ivoras/harlequin/internal/shared/types"
)

// The organisation's LLM markup (server.org_llm_markup_percent) is applied to
// every cost the API hands to users, and only there: the recorded usage rows
// stay the provider's figures. Every endpoint that returns a cost must pass it
// through markup (or one of the helpers below).

// markup returns usd raised by the configured percentage.
func (s *Server) markup(usd float64) float64 {
	if s.Cfg == nil || s.Cfg.Server.OrgLLMMarkupPercent == 0 {
		return usd
	}
	return usd * (1 + s.Cfg.Server.OrgLLMMarkupPercent/100)
}

func (s *Server) markupDays(days []types.UsageDay) {
	for i := range days {
		days[i].CostUSD = s.markup(days[i].CostUSD)
	}
}

// markupLog rewrites the cost_usd of every usage event in a session's NDJSON
// trajectory; other lines pass through byte for byte.
func (s *Server) markupLog(data []byte) []byte {
	if s.markup(1) == 1 {
		return data
	}
	lines := bytes.Split(data, []byte("\n"))
	for i, ln := range lines {
		if !bytes.Contains(ln, []byte(`"cost_usd"`)) {
			continue
		}
		var ev map[string]json.RawMessage
		if json.Unmarshal(ln, &ev) != nil {
			continue
		}
		var d map[string]json.RawMessage
		if json.Unmarshal(ev["data"], &d) != nil {
			continue
		}
		var c *float64
		if json.Unmarshal(d["cost_usd"], &c) != nil || c == nil { // null: provider sent no cost
			continue
		}
		d["cost_usd"], _ = json.Marshal(s.markup(*c))
		ev["data"], _ = json.Marshal(d)
		if out, err := json.Marshal(ev); err == nil {
			lines[i] = out
		}
	}
	return bytes.Join(lines, []byte("\n"))
}
