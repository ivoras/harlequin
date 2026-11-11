package decimalcalc

import (
	"errors"
	"strings"
	"testing"
)

func TestEvalExact(t *testing.T) {
	for expr, want := range map[string]string{
		"0.1 + 0.2":                   "0.3",
		"1500 * 1.08":                 "1620",
		"round(1500 * 1.08, 2)":       "1620.00",
		"round(2.675, 2)":             "2.68", // binary floats give 2.67
		"round(-2.675, 2)":            "-2.68",
		"round(1234.5)":               "1235",
		"round(1234, -2)":             "1200",
		"10 / 4":                      "2.5",
		"1 / 8":                       "0.125",
		"2 ^ 10":                      "1024",
		"2 ** -2":                     "0.25",
		"-2 ^ 2":                      "-4",
		"(1 + 0.05) ^ 3":              "1.157625",
		"1e3 + 2.5e-1":                "1000.25",
		"19.99 * 3":                   "59.97",
		"100 - 33.33 - 33.33 - 33.34": "0",
		"floor(2.7) + ceil(2.1)":      "5",
		"floor(-2.5)":                 "-3",
		"ceil(-2.5)":                  "-2",
		"trunc(-2.9)":                 "-2",
		"abs(-3.5)":                   "3.5",
		"max(1, 2.5, 2)":              "2.5",
		"min(3, -1)":                  "-1",
		"0.07 * 100":                  "7",
		"((2 + 3) * 4) / 5":           "4",
		"19.99 * 3 % 100":             "59.97",
		"7 % 3":                       "1",
		"-7 % 3":                      "-1",
		"7.5 % 2":                     "1.5",
		"10 ^ 2 % 7":                  "2",
		"-round(5, 2)":                "-5.00",
		"abs(round(-5, 2))":           "5.00",
	} {
		got, err := Eval(expr)
		if err != nil || got != want {
			t.Errorf("%s = %q (err %v), want %q", expr, got, err, want)
		}
	}
}

func TestNonTerminatingResultNamesTheExactFraction(t *testing.T) {
	got, err := Eval("1 / 3")
	if err != nil || !strings.HasPrefix(got, "0.33333333333333333333 ") || !strings.Contains(got, "exact value 1/3") {
		t.Fatalf("%q %v", got, err)
	}
}

func TestUnsupportedSyntaxIsDistinguishedFromErrors(t *testing.T) {
	for _, e := range []string{"Math.sqrt(144)", "(1.5).toFixed(2)", "2 ^ 0.5", "x + 1", "'a' + 1", "pi * 2", "1 ? 2 : 3"} {
		if _, err := Eval(e); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: want ErrUnsupported, got %v", e, err)
		}
	}
	for _, e := range []string{"1 / 0", "0 ^ -1", "9 ^ 999999", "(1 + ", "round(1, 2, 3)", "round(1, 99)", "7 % 0", "1500 * 8%", "1,234.56 * 2", "1.234,56 * 2", "1,000 + 1"} {
		if _, err := Eval(e); err == nil || errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: want a real error, got %v", e, err)
		}
	}
}
