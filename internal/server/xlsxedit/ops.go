package xlsxedit

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/ivoras/harlequin/internal/server/docxedit"
	"github.com/ivoras/harlequin/internal/server/mdxlsx"
)

type editor struct {
	f            *excelize.File
	in           snap
	sheets       []string
	mergesBefore map[string][]string
}

// sheetNamed finds a sheet by name (exactly, else case-insensitively).
func (e *editor) sheetNamed(name string) (string, error) {
	for _, s := range e.sheets {
		if s == name {
			return s, nil
		}
	}
	found := ""
	for _, s := range e.sheets {
		if strings.EqualFold(s, name) {
			if found != "" {
				found = ""
				break
			}
			found = s
		}
	}
	if found != "" {
		return found, nil
	}
	return "", fmt.Errorf("there is no sheet %q; the sheets are: %s", name, e.sheetList())
}

func (e *editor) sheetList() string {
	q := make([]string, len(e.sheets))
	for i, s := range e.sheets {
		q[i] = strconv.Quote(s)
	}
	return strings.Join(q, ", ")
}

// defaultSheet is the sheet argument, else the workbook's only sheet.
func (e *editor) defaultSheet(name string) (string, error) {
	if name != "" {
		return e.sheetNamed(name)
	}
	if len(e.sheets) == 1 {
		return e.sheets[0], nil
	}
	return "", fmt.Errorf("the workbook has several sheets; say which one (sheet argument). The sheets are: %s", e.sheetList())
}

// splitAddr reads "Sheet!B2", "'My sheet'!B2" or "B2".
func (e *editor) splitAddr(s, def string) (sheet, addr string, err error) {
	s = strings.TrimSpace(s)
	sheet = def
	if i := strings.LastIndex(s, "!"); i >= 0 {
		name := strings.ReplaceAll(strings.Trim(s[:i], "'"), "''", "'")
		if sheet, err = e.sheetNamed(name); err != nil {
			return "", "", err
		}
		s = s[i+1:]
	} else if sheet == "" {
		if sheet, err = e.defaultSheet(""); err != nil {
			return "", "", fmt.Errorf("address %q has no sheet: %w", s, err)
		}
	}
	col, row, err := excelize.CellNameToCoordinates(strings.ToUpper(strings.ReplaceAll(s, "$", "")))
	if err != nil {
		return "", "", fmt.Errorf("%q is not a cell address (use for example B2 or October!B2)", s)
	}
	addr, _ = excelize.CoordinatesToCellName(col, row)
	return sheet, addr, nil
}

// checkMerged refuses a write into a merged range except its top-left cell.
func (e *editor) checkMerged(sheet, addr string) error {
	col, row, _ := excelize.CellNameToCoordinates(addr)
	ms, _ := e.f.GetMergeCells(sheet, true)
	for _, m := range ms {
		c1, r1, _ := excelize.CellNameToCoordinates(m.GetStartAxis())
		c2, r2, _ := excelize.CellNameToCoordinates(m.GetEndAxis())
		if col >= c1 && col <= c2 && row >= r1 && row <= r2 && addr != m.GetStartAxis() {
			return fmt.Errorf("%s!%s is inside the merged range %s:%s; write to its top-left cell %s instead", sheet, addr, m.GetStartAxis(), m.GetEndAxis(), m.GetStartAxis())
		}
	}
	return nil
}

// typed converts a JSON value: number, "=formula", text, or clear (nil / "").
func typed(v any) (c cell, clear bool, err error) {
	switch x := v.(type) {
	case nil:
		return c, true, nil
	case float64:
		return cell{typ: "n", val: strconv.FormatFloat(x, 'f', -1, 64)}, false, nil
	case int:
		return cell{typ: "n", val: strconv.Itoa(x)}, false, nil
	case int64:
		return cell{typ: "n", val: strconv.FormatInt(x, 10)}, false, nil
	case json.Number:
		if _, err := x.Float64(); err != nil {
			return c, false, fmt.Errorf("%q is not a number", string(x))
		}
		return cell{typ: "n", val: string(x)}, false, nil
	case bool:
		if x {
			return cell{typ: "b", val: "1"}, false, nil
		}
		return cell{typ: "b", val: "0"}, false, nil
	case string:
		switch {
		case x == "":
			return c, true, nil
		case strings.HasPrefix(x, "=") && len(x) > 1:
			return cell{typ: "f", formula: x[1:]}, false, nil
		case len([]rune(x)) > 32767:
			return c, false, fmt.Errorf("a cell holds at most 32767 characters")
		}
		return cell{typ: "s", val: x}, false, nil
	}
	return c, false, fmt.Errorf("unsupported value %v (use a number, text, a =formula, or null to clear)", v)
}

// textCell types a Markdown table cell the way create_excel does.
func textCell(s string) (cell, bool) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return cell{}, true
	case strings.HasPrefix(s, "=") && len(s) > 1:
		return cell{typ: "f", formula: s[1:]}, false
	case mdxlsx.IsNumber(s):
		return cell{typ: "n", val: s}, false
	}
	return cell{typ: "s", val: s}, false
}

// put writes one typed cell, keeping its style.
func (e *editor) put(sheet, addr string, c cell, clear bool) error {
	f := e.f
	if err := f.SetCellFormula(sheet, addr, ""); err != nil {
		return err
	}
	switch {
	case clear:
		return f.SetCellValue(sheet, addr, nil)
	case c.typ == "f":
		if err := f.SetCellValue(sheet, addr, nil); err != nil { // drop a stale cached value
			return err
		}
		return f.SetCellFormula(sheet, addr, c.formula)
	case c.typ == "n":
		n, _ := strconv.ParseFloat(c.val, 64)
		return f.SetCellValue(sheet, addr, n)
	case c.typ == "b":
		return f.SetCellValue(sheet, addr, c.val == "1")
	}
	return f.SetCellValue(sheet, addr, c.val)
}

func (e *editor) apply(sheet, addr string, c cell, clear bool) (change, error) {
	if err := e.checkMerged(sheet, addr); err != nil {
		return change{}, err
	}
	if err := e.put(sheet, addr, c, clear); err != nil {
		return change{}, fmt.Errorf("cannot write %s!%s: %w", sheet, addr, err)
	}
	return change{sheet: sheet, addr: addr, to: c, clear: clear}, nil
}

func (e *editor) setCells(op Op) ([]change, string, error) {
	if len(op.Cells) == 0 {
		return nil, "", fmt.Errorf("cells is empty; give an object such as {\"B2\": 1700}")
	}
	def := ""
	if op.Sheet != "" {
		var err error
		if def, err = e.sheetNamed(op.Sheet); err != nil {
			return nil, "", err
		}
	}
	var changes []change
	seen := map[string]bool{}
	used := map[string]bool{}
	for k, v := range op.Cells {
		sheet, addr, err := e.splitAddr(k, def)
		if err != nil {
			return nil, "", err
		}
		if seen[sheet+"!"+addr] {
			return nil, "", fmt.Errorf("%s!%s is given twice", sheet, addr)
		}
		seen[sheet+"!"+addr] = true
		c, clear, err := typed(v)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", k, err)
		}
		ch, err := e.apply(sheet, addr, c, clear)
		if err != nil {
			return nil, "", err
		}
		changes = append(changes, ch)
		used[sheet] = true
	}
	var names []string
	for _, s := range e.sheets {
		if used[s] {
			names = append(names, s)
		}
	}
	return changes, fmt.Sprintf("set %d cell(s) on sheet %s", len(changes), strings.Join(names, ", ")), nil
}

// tableRows reads Markdown table rows; anything else is an error.
func tableRows(text string) ([][]string, error) {
	var rows [][]string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
		case strings.HasPrefix(t, "|"):
			if cells := mdxlsx.SplitRow(t); !mdxlsx.IsSeparator(cells) {
				rows = append(rows, cells)
			}
		default:
			return nil, fmt.Errorf("only Markdown table rows can be added to a sheet, one per line such as | Ana | 1500 |; %q is not a table row", docxSnippet(t))
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no table rows found; give rows such as | Ana | 1500 |")
	}
	return rows, nil
}

func docxSnippet(s string) string {
	if r := []rune(s); len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}

// lastUsedRow is the last row with a value or formula (0 for an empty sheet).
func (e *editor) lastUsedRow(sheet string) int {
	last := 0
	for addr := range e.in[sheet] {
		if _, r, _ := excelize.CellNameToCoordinates(addr); r > last {
			last = r
		}
	}
	return last
}

// writeRows writes typed rows starting at row at, with each column's style
// copied from styleRow (0 = none).
func (e *editor) writeRows(sheet string, rows [][]string, at, styleRow int, styles map[int]int) ([]change, error) {
	var changes []change
	for i, cells := range rows {
		for j, s := range cells {
			addr, _ := excelize.CoordinatesToCellName(j+1, at+i)
			if st, ok := styles[j+1]; ok && st != 0 {
				if err := e.f.SetCellStyle(sheet, addr, addr, st); err != nil {
					return nil, err
				}
			}
			c, clear := textCell(s)
			if clear {
				continue
			}
			ch, err := e.apply(sheet, addr, c, false)
			if err != nil {
				return nil, err
			}
			changes = append(changes, ch)
		}
	}
	return changes, nil
}

func (e *editor) rowStyles(sheet string, row, cols int) map[int]int {
	st := map[int]int{}
	if row < 1 {
		return st
	}
	for c := 1; c <= cols; c++ {
		addr, _ := excelize.CoordinatesToCellName(c, row)
		if id, err := e.f.GetCellStyle(sheet, addr); err == nil {
			st[c] = id
		}
	}
	return st
}

func maxLen(rows [][]string) int {
	n := 0
	for _, r := range rows {
		n = max(n, len(r))
	}
	return n
}

func (e *editor) appendRows(op Op) ([]change, string, error) {
	sheet, err := e.defaultSheet(op.Sheet)
	if err != nil {
		return nil, "", err
	}
	rows, err := tableRows(op.Text)
	if err != nil {
		return nil, "", err
	}
	last := e.lastUsedRow(sheet)
	changes, err := e.writeRows(sheet, rows, last+1, last, e.rowStyles(sheet, last, maxLen(rows)))
	if err != nil {
		return nil, "", err
	}
	return changes, fmt.Sprintf("appended %d row(s) to sheet %s (rows %d-%d)", len(rows), sheet, last+1, last+len(rows)), nil
}

var rowAnchorRE = regexp.MustCompile(`(?i)^\s*row\s+(\d+)\s*$`)

// anchorRow resolves "row N" or a passage that occurs in exactly one row.
func (e *editor) anchorRow(sheet, anchor, name string) (int, error) {
	if m := rowAnchorRE.FindStringSubmatch(anchor); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n < 1 || n > e.lastUsedRow(sheet) {
			return 0, fmt.Errorf("%s: the sheet %s has rows 1-%d, not row %d", name, sheet, e.lastUsedRow(sheet), n)
		}
		return n, nil
	}
	var parts []string
	for _, l := range strings.Split(anchor, "\n") {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "|") {
			for _, c := range mdxlsx.SplitRow(t) {
				if c != "" && !mdxlsx.IsSeparator([]string{c}) {
					parts = append(parts, c)
				}
			}
		} else if t != "" {
			parts = append(parts, docxedit.StripMarkdown(t))
		}
	}
	q := docxedit.Normalize(strings.Join(parts, " "))
	if q == "" {
		return 0, fmt.Errorf("%s must be row N (for example row 7) or a passage from one row", name)
	}
	rows := map[int][]string{}
	for addr, c := range e.in[sheet] {
		col, r, _ := excelize.CellNameToCoordinates(addr)
		_ = col
		text := c.val
		if c.typ == "f" {
			text = "=" + c.formula
		}
		rows[r] = append(rows[r], fmt.Sprintf("%06d\x00%s", col, text))
	}
	var hits []int
	for r, cs := range rows {
		sortStrings(cs)
		var texts []string
		for _, c := range cs {
			texts = append(texts, c[strings.Index(c, "\x00")+1:])
		}
		if strings.Contains(docxedit.Normalize(strings.Join(texts, " ")), q) {
			hits = append(hits, r)
		}
	}
	switch len(hits) {
	case 0:
		return 0, fmt.Errorf("%s was not found in any row of sheet %s (matching ignores extra spaces and Markdown marks, but is otherwise exact); copy it as read_doc with cells true shows it, or use row N", name, sheet)
	case 1:
		return hits[0], nil
	}
	return 0, fmt.Errorf("%s occurs in %d rows; quote more of the row, or use row N", name, len(hits))
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func (e *editor) insertRows(op Op) ([]change, insertion, string, error) {
	name := "after"
	if op.Before {
		name = "before"
	}
	var ins insertion
	sheet, err := e.defaultSheet(op.Sheet)
	if err != nil {
		return nil, ins, "", err
	}
	rows, err := tableRows(op.Text)
	if err != nil {
		return nil, ins, "", err
	}
	n, err := e.anchorRow(sheet, op.Anchor, name)
	if err != nil {
		return nil, ins, "", err
	}
	at := n + 1
	if op.Before {
		at = n
	}
	styles := e.rowStyles(sheet, n, maxLen(rows)) // read before the rows move
	if err := e.f.InsertRows(sheet, at, len(rows)); err != nil {
		return nil, ins, "", fmt.Errorf("inserting rows failed: %w", err)
	}
	ins = insertion{sheet: sheet, at: at, n: len(rows), present: true}
	changes, err := e.writeRows(sheet, rows, at, n, styles)
	if err != nil {
		return nil, ins, "", err
	}
	return changes, ins, fmt.Sprintf("inserted %d row(s) %s row %d of sheet %s (now rows %d-%d); rows below moved down and formulas, merged ranges and names that refer to them were adjusted", len(rows), name, n, sheet, at, at+len(rows)-1), nil
}

func (e *editor) replace(op Op) ([]change, string, error) {
	if op.Old == "" {
		return nil, "", fmt.Errorf("old_string must not be empty (to add data use cells, append or insert)")
	}
	if op.Old == op.New {
		return nil, "", fmt.Errorf("old_string and new_string are identical; nothing to change")
	}
	only := ""
	if op.Sheet != "" {
		var err error
		if only, err = e.sheetNamed(op.Sheet); err != nil {
			return nil, "", err
		}
	}
	type hit struct{ sheet, addr string }
	var hits []hit
	total := 0
	var numeric []string
	for _, sh := range e.sheets {
		if only != "" && sh != only {
			continue
		}
		for addr, c := range e.in[sh] {
			text := c.val
			switch c.typ {
			case "f":
				text = "=" + c.formula
			case "s":
			default:
				if strings.Contains(c.val, op.Old) {
					numeric = append(numeric, sh+"!"+addr)
				}
				continue
			}
			if k := strings.Count(text, op.Old); k > 0 {
				hits = append(hits, hit{sh, addr})
				total += k
			}
		}
	}
	switch {
	case len(hits) == 0 && len(numeric) > 0:
		sortStrings(numeric)
		return nil, "", fmt.Errorf("old_string matches only numeric cell(s) (%s); replace works on text and formulas, so use cells with the address, for example {%q: <number>}", strings.Join(numeric, ", "), numeric[0])
	case len(hits) == 0:
		return nil, "", fmt.Errorf("old_string was not found in any text or formula cell (matching is exact, including case); copy it as read_doc with cells true shows it")
	case total > 1 && !op.ReplaceAll:
		return nil, "", fmt.Errorf("old_string occurs %d times in %d cell(s); include more surrounding text to make it unique, or set replace_all to replace every occurrence", total, len(hits))
	}
	var changes []change
	for _, h := range hits {
		if rt, _ := e.f.GetCellRichText(h.sheet, h.addr); len(rt) > 1 {
			return nil, "", fmt.Errorf("cell %s!%s has mixed formatting inside the text, which a replace would flatten; use cells to overwrite the whole cell", h.sheet, h.addr)
		}
		c := e.in[h.sheet][h.addr]
		var nc cell
		if c.typ == "f" {
			nt := strings.ReplaceAll("="+c.formula, op.Old, op.New)
			if !strings.HasPrefix(nt, "=") || len(nt) < 2 {
				return nil, "", fmt.Errorf("the replacement would remove the = of the formula in %s!%s", h.sheet, h.addr)
			}
			nc = cell{typ: "f", formula: nt[1:]}
		} else {
			nc = cell{typ: "s", val: strings.ReplaceAll(c.val, op.Old, op.New)}
		}
		ch, err := e.apply(h.sheet, h.addr, nc, nc.typ == "s" && nc.val == "")
		if err != nil {
			return nil, "", err
		}
		changes = append(changes, ch)
	}
	return changes, fmt.Sprintf("replaced %d occurrence(s) in %d cell(s)", total, len(hits)), nil
}
