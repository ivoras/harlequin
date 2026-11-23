package sheetdb

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// readCSV streams a CSV/TSV file as one sheet. The delimiter is sniffed from
// the first line. Cells that are plain numbers (no thousands separators, no
// leading zero) become numbers; everything else stays text, so codes like
// "01270000" keep their zeros — num() converts the rest in SQL.
func readCSV(data []byte, row func(rowNum int, cells []any) error) error {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) {
		// shortcut: non-UTF-8 is taken as Windows-1250 (this deployment's
		// legacy Excel CSV export); upgrade to detection if other codepages show up.
		if d, err := charmap.Windows1250.NewDecoder().Bytes(data); err == nil {
			data = d
		}
	}
	first, _, _ := bytes.Cut(data, []byte("\n"))
	delim, best := ',', 0
	for _, d := range []rune{',', ';', '\t', '|'} {
		if n := strings.Count(string(first), string(d)); n > best {
			delim, best = d, n
		}
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma, r.FieldsPerRecord, r.LazyQuotes, r.ReuseRecord = delim, -1, true, true
	for n := 1; ; n++ {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		cells := make([]any, len(rec))
		has := false
		for i, s := range rec {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			has = true
			if plainNumRE.MatchString(s) {
				if v, err := strconv.ParseFloat(s, 64); err == nil {
					cells[i] = number(v)
					continue
				}
			}
			cells[i] = s
		}
		if has {
			if err := row(n, cells); err != nil {
				return err
			}
		}
	}
}

var plainNumRE = regexp.MustCompile(`^-?(0|[1-9]\d*)(\.\d+)?$`)

var numJunkRE = regexp.MustCompile(`[\s\x{00a0}'€$£%]|kn|eur|EUR|HRK|USD`)

// parseNum reads a number written as text in common spreadsheet styles:
// "1.234,56", "1,234.56", "1 234,56 €", "(123)", "-12,5%", "12-". With only one
// kind of separator, it is the decimal point when it appears once ("1,5",
// "1.234" = 1.234) and a thousands separator when it repeats ("1.234.567").
func parseNum(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if !strings.ContainsAny(s, "0123456789") {
		return 0, false
	}
	neg := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		neg, s = true, s[1:len(s)-1]
	}
	if t, ok := strings.CutSuffix(s, "-"); ok {
		neg, s = true, t
	}
	s = numJunkRE.ReplaceAllString(s, "")
	if t, ok := strings.CutPrefix(s, "-"); ok {
		neg, s = !neg, t
	} else {
		s = strings.TrimPrefix(s, "+")
	}
	dot, comma := strings.LastIndexByte(s, '.'), strings.LastIndexByte(s, ',')
	switch {
	case dot >= 0 && comma >= 0: // both: the later one is the decimal point
		if comma > dot {
			s = strings.ReplaceAll(s, ".", "")
			s = strings.Replace(s, ",", ".", 1)
		} else {
			s = strings.ReplaceAll(s, ",", "")
		}
	case comma >= 0:
		if strings.Count(s, ",") == 1 {
			s = strings.Replace(s, ",", ".", 1)
		} else {
			s = strings.ReplaceAll(s, ",", "")
		}
	case dot >= 0 && strings.Count(s, ".") > 1:
		s = strings.ReplaceAll(s, ".", "")
	}
	if s == "" || strings.Count(s, ".") > 1 {
		return 0, false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && c != '.' {
			return 0, false
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	if neg {
		v = -v
	}
	return v, true
}
