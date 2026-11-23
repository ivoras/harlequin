package sheetdb

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Cell renders one value: plain numbers without exponent or float noise
// (0.1+0.2 prints 0.3), NULL as empty.
func Cell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		if math.Abs(x) < 1e15 {
			x = math.Round(x*1e9) / 1e9
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		return x
	case []byte:
		return string(x)
	case bool:
		if x {
			return "1"
		}
		return "0"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// Markdown renders rows as a pipe table, cells cut to maxCell runes. Plain
// numbers stay plain, so create_excel turns them back into numeric cells.
func Markdown(cols []string, rows [][]any, maxCell int) string {
	var sb strings.Builder
	esc := strings.NewReplacer("|", `\|`, "\n", " ", "\r", "")
	sb.WriteString("|")
	for _, c := range cols {
		sb.WriteString(" " + esc.Replace(c) + " |")
	}
	sb.WriteString("\n|")
	for range cols {
		sb.WriteString("---|")
	}
	sb.WriteString("\n")
	for _, r := range rows {
		sb.WriteString("|")
		for _, v := range r {
			s := Cell(v)
			if maxCell > 0 {
				s = truncate(s, maxCell)
			}
			sb.WriteString(" " + esc.Replace(s) + " |")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// CSV renders rows as CSV with a header line.
func CSV(cols []string, rows [][]any) []byte {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write(cols)
	rec := make([]string, len(cols))
	for _, r := range rows {
		for i, v := range r {
			rec[i] = Cell(v)
		}
		_ = w.Write(rec)
	}
	w.Flush()
	return b.Bytes()
}

// Objects returns rows as column-name → value maps (JSON / JavaScript).
func Objects(cols []string, rows [][]any) []any {
	out := make([]any, len(rows))
	for i, r := range rows {
		m := make(map[string]any, len(cols))
		for j, c := range cols {
			m[c] = r[j]
		}
		out[i] = m
	}
	return out
}
