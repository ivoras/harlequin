package api

import "github.com/ivoras/harlequin/internal/shared/types"

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
