package api

import (
	"math"
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
