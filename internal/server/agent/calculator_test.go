package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/jsrun"
)

// The calculator is exact for money; unsupported syntax falls back to float JS
// and says so; real errors are reported rather than silently retried.
func TestCalculatorExactDecimalWithFloatFallback(t *testing.T) {
	a := &Agent{Runner: jsrun.New(jsrun.Options{})}
	calc := a.calculatorEntry().handler
	run := func(expr string) string {
		out, err := calc(context.Background(), &runContext{}, map[string]any{"expression": expr})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := run("0.1 + 0.2"); got != "0.3" {
		t.Errorf("0.1 + 0.2 = %q", got)
	}
	if got := run("round(1500 * 1.08, 2)"); got != "1620.00" {
		t.Errorf("got %q", got)
	}
	if got := run("Math.sqrt(144)"); !strings.HasPrefix(got, "12\n(note: evaluated as floating-point JavaScript") {
		t.Errorf("fallback = %q", got)
	}
	// "^" stays power in the fallback (JS would XOR), and a thousands separator
	// is an error rather than a JS comma expression.
	if got := run("Math.sqrt(3 ^ 2)"); !strings.HasPrefix(got, "3\n(note:") {
		t.Errorf("^ in fallback = %q", got)
	}
	if got := run("4 ^ -0.5"); !strings.HasPrefix(got, "0.5\n(note:") {
		t.Errorf("fractional power = %q", got)
	}
	if got := run("1,234.56 * 2"); !strings.HasPrefix(got, "error evaluating") {
		t.Errorf("thousands separator = %q", got)
	}
	if got := run("1 / 0"); !strings.HasPrefix(got, "error evaluating") || strings.Contains(got, "Infinity") {
		t.Errorf("division by zero = %q", got)
	}
}
