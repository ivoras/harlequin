package sheetdb

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// DecodeText returns CSV/TSV bytes as UTF-8 text (BOM dropped).
func DecodeText(data []byte) []byte {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if utf8.Valid(data) && !looksMisread(data) {
		return data
	}
	// shortcut: non-UTF-8 is taken as Windows-1250 (this deployment's legacy
	// Excel CSV export); upgrade to detection if other codepages show up.
	if d, err := charmap.Windows1250.NewDecoder().Bytes(data); err == nil {
		return d
	}
	return data
}

// looksMisread: Windows-1250 letters can chain into valid UTF-8 by accident
// ("čžš" = E8 9E 9A = one CJK character). "UTF-8" whose CJK characters all
// stand alone between other letters (real CJK text has runs of them), and
// whose bytes read as clean Windows-1250, is Windows-1250.
func looksMisread(data []byte) bool {
	isCJK := func(r rune) bool {
		return (r >= 0x3000 && r <= 0x9fff) || (r >= 0xac00 && r <= 0xd7af) || (r >= 0xf900 && r <= 0xfaff)
	}
	cjk, prev := false, false
	for _, r := range string(data) {
		c := isCJK(r)
		if c && prev {
			return false // a run: genuine CJK text
		}
		cjk, prev = cjk || c, c
	}
	if !cjk {
		return false
	}
	d, err := charmap.Windows1250.NewDecoder().Bytes(data)
	if err != nil {
		return false
	}
	for _, r := range string(d) {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

// sniffDelim picks the delimiter that splits the first line into the most
// fields, ignoring quoted text ("Iznos, EUR" is one field).
func sniffDelim(data []byte) rune {
	first, _, _ := bytes.Cut(data, []byte("\n"))
	counts := map[rune]int{}
	inQ := false
	for _, r := range string(first) {
		switch {
		case r == '"':
			inQ = !inQ
		case !inQ && (r == ',' || r == ';' || r == '\t' || r == '|'):
			counts[r]++
		}
	}
	delim, best := ',', 0
	for _, d := range []rune{',', ';', '\t', '|'} {
		if counts[d] > best {
			delim, best = d, counts[d]
		}
	}
	return delim
}

// readCSV streams a CSV/TSV file as one sheet; tsv forces the tab delimiter,
// otherwise it is sniffed. A CSV has no types, so values are read the way a
// spreadsheet program shows them: plain numbers become numbers, dates
// (2026-01-21, 21.01.2026.) become ISO dates, pure-digit values with a
// leading zero stay text (codes). Amounts with separators ("1.234,56") stay
// text here; the builder converts whole columns of them once it knows which
// separator the column uses. It returns the delimiter used.
func readCSV(data []byte, tsv bool, row func(rowNum int, cells []any) error) (rune, error) {
	data = DecodeText(data)
	delim := '\t'
	if !tsv {
		delim = sniffDelim(data)
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma, r.FieldsPerRecord, r.LazyQuotes, r.ReuseRecord = delim, -1, true, true
	for n := 1; ; n++ {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return delim, nil
		}
		if err != nil {
			return delim, err
		}
		cells := make([]any, len(rec))
		has := false
		for i, s := range rec {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			has = true
			cells[i] = csvValue(s)
		}
		if has {
			if err := row(n, cells); err != nil {
				return delim, err
			}
		}
	}
}

var (
	plainNumRE = regexp.MustCompile(`^-?(0|[1-9]\d*)(\.\d+)?$`)
	isoDateRE  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}( \d{2}:\d{2}(:\d{2})?)?$`)
	// d.m.yyyy with optional trailing dot and spaces (21.01.2026., 1. 2. 2026);
	// d/m/yyyy is left as text: it reads differently in different locales.
	dmyRE = regexp.MustCompile(`^(\d{1,2})\.\s?(\d{1,2})\.\s?(\d{4})\.?$`)
)

func csvValue(s string) any {
	if plainNumRE.MatchString(s) {
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			return number(v)
		}
	}
	if isoDateRE.MatchString(s) {
		if _, err := time.Parse("2006-01-02", s[:10]); err == nil {
			return isoDate(s)
		}
	}
	if m := dmyRE.FindStringSubmatch(s); m != nil {
		d, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		y, _ := strconv.Atoi(m[3])
		if t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC); t.Day() == d && int(t.Month()) == mo {
			return isoDate(t.Format("2006-01-02"))
		}
	}
	return s
}

// sepVote says which decimal separator a written number implies: ',' or '.',
// or 0 when it can't tell ("1,234" and "1.234" read both ways).
func sepVote(s string) byte {
	s = numJunkRE.ReplaceAllString(s, "")
	dot, comma := strings.LastIndexByte(s, '.'), strings.LastIndexByte(s, ',')
	switch {
	case dot >= 0 && comma >= 0:
		if comma > dot {
			return ','
		}
		return '.'
	case comma >= 0 && strings.Count(s, ",") > 1:
		return '.' // repeated: thousands
	case dot >= 0 && strings.Count(s, ".") > 1:
		return ','
	case comma >= 0 && len(s)-comma-1 != 3:
		return ','
	case dot >= 0 && len(s)-dot-1 != 3:
		return '.'
	}
	return 0
}

var numJunkRE = regexp.MustCompile(`[\s\x{00a0}'€$£%]|kn|eur|EUR|HRK|USD`)

// parseNum reads a number written as text in common spreadsheet styles:
// "1.234,56", "1,234.56", "1 234,56 €", "(123)", "-12,5%", "12-". With only one
// kind of separator, it is the decimal point when it appears once ("1,5",
// "1.234" = 1.234) and a thousands separator when it repeats ("1.234.567").
func parseNum(s string) (float64, bool) { return parseNumDec(s, 0) }

// parseNumDec is parseNum with the decimal separator known (',' or '.'); the
// other one is then a thousands separator. dec 0 guesses per value.
func parseNumDec(s string, dec byte) (float64, bool) {
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
	if dec == ',' {
		s = strings.ReplaceAll(s, ".", "")
		s = strings.Replace(s, ",", ".", 1)
	} else if dec == '.' {
		s = strings.ReplaceAll(s, ",", "")
	}
	dot, comma := strings.LastIndexByte(s, '.'), strings.LastIndexByte(s, ',')
	switch {
	case dec != 0:
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
