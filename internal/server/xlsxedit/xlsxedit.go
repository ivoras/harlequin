// Package xlsxedit edits an uploaded .xlsx in place with excelize: set cells,
// append or insert table rows, replace text. Styles, formulas, other sheets and
// the package's other parts are kept; before anything is returned the output is
// re-read and every cell compared with the input, so a bug or an excelize
// limitation can never hand back a damaged workbook.
package xlsxedit

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// Kind selects the operation.
type Kind int

const (
	SetCells   Kind = iota + 1 // Cells: address -> value
	AppendRows                 // Text: Markdown table rows, after the last used row
	InsertRows                 // Text rows next to the row Anchor names
	Replace                    // Old -> New in text and formula cells
)

type Op struct {
	Kind       Kind
	Sheet      string // default sheet for addresses without one
	Cells      map[string]any
	Text       string
	Anchor     string // "row 7" or a passage occurring in exactly one row
	Before     bool
	Old, New   string
	ReplaceAll bool
}

const maxCells = 2_000_000 // snapshot budget: bigger workbooks are not edited

var oleMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// partGroups are package parts a round trip must not lose; a group with fewer
// parts afterwards is a feature the edit would destroy.
var partGroups = []struct{ prefix, name string }{
	{"xl/charts/", "charts"}, {"xl/drawings/", "drawings (images, shapes)"}, {"xl/media/", "images"},
	{"xl/pivotTables/", "pivot tables"}, {"xl/pivotCache/", "pivot tables"}, {"xl/slicers/", "slicers"},
	{"xl/slicerCaches/", "slicers"}, {"xl/externalLinks/", "links to other workbooks"},
	{"xl/embeddings/", "embedded objects"}, {"xl/ctrlProps/", "form controls"}, {"customXml/", "custom XML"},
	{"xl/printerSettings/", "printer settings"}, {"xl/comments", "comments"}, {"xl/threadedComments/", "threaded comments"},
	{"xl/tables/", "tables"}, {"xl/queryTables/", "queries"}, {"xl/connections.xml", "data connections"},
	{"xl/metadata.xml", "dynamic-array metadata"}, {"docProps/custom.xml", "custom properties"},
}

// markers are elements of sheet and workbook XML that must keep their count.
var markers = []struct {
	re   *regexp.Regexp
	name string
}{
	{regexp.MustCompile(`sparklineGroup>`), "sparklines"},
	{regexp.MustCompile(`x14:conditionalFormatting`), "extended conditional formatting"},
	{regexp.MustCompile(`x14:dataValidation>`), "extended data validation"},
	{regexp.MustCompile(`<legacyDrawing`), "comments or controls"},
	{regexp.MustCompile(`<drawing `), "drawings"},
	{regexp.MustCompile(`<controls`), "form controls"},
	{regexp.MustCompile(`<oleObjects`), "embedded objects"},
	{regexp.MustCompile(`<extLst`), "extension data"},
	{regexp.MustCompile(`<definedName `), "defined names"},
	{regexp.MustCompile(`<conditionalFormatting`), "conditional formatting"},
	{regexp.MustCompile(`<dataValidation `), "data validation"},
	{regexp.MustCompile(`<hyperlink `), "hyperlinks"},
	{regexp.MustCompile(`<pane `), "frozen panes"},
	{regexp.MustCompile(`<sheetProtection`), "sheet protection"},
	{regexp.MustCompile(`<mergeCell `), "merged cells"},
	{regexp.MustCompile(`<tablePart `), "tables"},
}

type pkgInfo struct {
	groups  map[string]int
	markers map[string]int
}

func scanPackage(data []byte) (*pkgInfo, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	pi := &pkgInfo{groups: map[string]int{}, markers: map[string]int{}}
	for _, f := range zr.File {
		for _, g := range partGroups {
			if strings.HasPrefix(f.Name, g.prefix) {
				pi.groups[g.name]++
			}
		}
		if strings.HasPrefix(f.Name, "xl/worksheets/sheet") || f.Name == "xl/workbook.xml" {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			b, err := io.ReadAll(io.LimitReader(rc, 256<<20))
			rc.Close()
			if err != nil {
				return nil, err
			}
			for _, m := range markers {
				pi.markers[m.name] += len(m.re.FindAll(b, -1))
			}
		}
	}
	return pi, nil
}

// lost names what the package had before and no longer has.
func (a *pkgInfo) lost(b *pkgInfo) []string {
	var out []string
	for n, c := range a.groups {
		if b.groups[n] < c {
			out = append(out, n)
		}
	}
	for n, c := range a.markers {
		if b.markers[n] < c {
			out = append(out, n)
		}
	}
	return out
}

// open validates the package and opens it with excelize.
func open(data []byte) (*excelize.File, error) {
	if bytes.HasPrefix(data, oleMagic) {
		return nil, fmt.Errorf("the file is an old .xls workbook or an encrypted/password-protected one, which cannot be edited in place; the user can save it as a plain .xlsx in Excel and upload that")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("the file is not a readable .xlsx (not a zip archive)")
	}
	haveWB := false
	for _, f := range zr.File {
		switch {
		case f.Name == "xl/workbook.xml":
			haveWB = true
		case f.Name == "xl/vbaProject.bin":
			return nil, fmt.Errorf("the workbook contains macros (.xlsm); editing it would not keep them, so it cannot be edited in place; the user can edit the cells in Excel")
		case f.Name == "[Content_Types].xml":
			rc, err := f.Open()
			if err == nil {
				b, _ := io.ReadAll(io.LimitReader(rc, 4<<20))
				rc.Close()
				if bytes.Contains(b, []byte("macroEnabled")) {
					return nil, fmt.Errorf("the workbook is macro-enabled (.xlsm); editing it would not keep its macros, so it cannot be edited in place")
				}
			}
		}
	}
	if !haveWB {
		return nil, fmt.Errorf("the file is not a readable .xlsx (no xl/workbook.xml; it may be encrypted)")
	}
	f, err := excelize.OpenReader(bytes.NewReader(data), excelize.Options{UnzipSizeLimit: 512 << 20, UnzipXMLSizeLimit: 128 << 20})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "password") {
			return nil, fmt.Errorf("the workbook is password-protected, so it cannot be edited; the user can remove the password in Excel and upload it again")
		}
		return nil, fmt.Errorf("the file is not a readable .xlsx: %v", err)
	}
	return f, nil
}

type cell struct {
	typ     string // n b e s f
	val     string
	formula string
}

type snap map[string]map[string]cell

func (s snap) clone() snap {
	out := snap{}
	for sh, m := range s {
		c := make(map[string]cell, len(m))
		for k, v := range m {
			c[k] = v
		}
		out[sh] = c
	}
	return out
}

func (s snap) hasFormula() bool {
	for _, m := range s {
		for _, c := range m {
			if c.typ == "f" {
				return true
			}
		}
	}
	return false
}

func dims(f *excelize.File, sheet string, rows [][]string) (maxRow, maxCol int) {
	maxRow = len(rows)
	for _, r := range rows {
		maxCol = max(maxCol, len(r))
	}
	if d, err := f.GetSheetDimension(sheet); err == nil && d != "" {
		parts := strings.Split(d, ":")
		if c, r, err := excelize.CellNameToCoordinates(parts[len(parts)-1]); err == nil {
			maxRow, maxCol = max(maxRow, r), max(maxCol, c)
		}
	}
	return
}

var raw = excelize.Options{RawCellValue: true}

func snapshot(f *excelize.File) (snap, error) {
	out := snap{}
	total := 0
	for _, sh := range f.GetSheetList() {
		rows, err := f.GetRows(sh, raw)
		if err != nil {
			return nil, err
		}
		mr, mc := dims(f, sh, rows)
		if total += mr * mc; total > maxCells {
			return nil, fmt.Errorf("the workbook is too large to edit safely (more than %d cells in its used ranges)", maxCells)
		}
		m := map[string]cell{}
		for r := 1; r <= mr; r++ {
			for c := 1; c <= mc; c++ {
				addr, _ := excelize.CoordinatesToCellName(c, r)
				fo, _ := f.GetCellFormula(sh, addr)
				val := ""
				if r <= len(rows) && c <= len(rows[r-1]) {
					val = rows[r-1][c-1]
				}
				if fo == "" && val == "" {
					continue
				}
				ct, _ := f.GetCellType(sh, addr)
				cl := cell{val: val}
				switch {
				case fo != "":
					cl = cell{typ: "f", formula: fo}
				case ct == excelize.CellTypeBool:
					cl.typ = "b"
				case ct == excelize.CellTypeError:
					cl.typ = "e"
				case ct == excelize.CellTypeSharedString || ct == excelize.CellTypeInlineString:
					cl.typ = "s"
				default: // numbers carry no t attribute (CellTypeUnset)
					cl.typ = "n"
				}
				m[addr] = cl
			}
		}
		out[sh] = m
	}
	return out, nil
}

func same(a, b cell) bool {
	if a.typ != b.typ {
		return false
	}
	switch a.typ {
	case "f":
		return a.formula == b.formula
	case "n":
		x, e1 := strconv.ParseFloat(a.val, 64)
		y, e2 := strconv.ParseFloat(b.val, 64)
		return e1 == nil && e2 == nil && x == y
	}
	return a.val == b.val
}

// change is one intended difference: a cell set (or cleared when clear).
type change struct {
	sheet, addr string
	to          cell
	clear       bool
}

type insertion struct {
	sheet   string
	at, n   int
	present bool
}

// Edit applies op to the .xlsx in data. It returns the new file and a short
// description, or an error and no file.
func Edit(data []byte, op Op) (out []byte, desc string, err error) {
	f, err := open(data)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	before, err := scanPackage(data)
	if err != nil {
		return nil, "", err
	}
	if op.Kind == InsertRows && (before.groups["charts"] > 0 || before.groups["pivot tables"] > 0) {
		return nil, "", fmt.Errorf("inserting rows is not supported in a workbook with charts or pivot tables (their data ranges would not move with the rows); append rows after the last one, or set cells instead")
	}
	in, err := snapshot(f)
	if err != nil {
		return nil, "", err
	}
	e := &editor{f: f, in: in, sheets: f.GetSheetList(), mergesBefore: map[string][]string{}}
	for _, sh := range e.sheets {
		e.mergesBefore[sh] = mergeList(f, sh)
	}
	var changes []change
	var ins insertion
	switch op.Kind {
	case SetCells:
		changes, desc, err = e.setCells(op)
	case AppendRows:
		changes, desc, err = e.appendRows(op)
	case InsertRows:
		changes, ins, desc, err = e.insertRows(op)
	case Replace:
		changes, desc, err = e.replace(op)
	default:
		err = fmt.Errorf("unsupported operation")
	}
	if err != nil {
		return nil, "", err
	}
	if in.hasFormula() || anyFormula(changes) {
		t := true
		if err := f.SetCalcProps(&excelize.CalcPropsOptions{FullCalcOnLoad: &t}); err != nil {
			return nil, "", err
		}
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, "", fmt.Errorf("writing the workbook failed: %w", err)
	}
	out = buf.Bytes()
	if err := verify(data, out, in, e.sheets, changes, ins, e.mergesBefore); err != nil {
		return nil, "", fmt.Errorf("internal check failed, nothing was changed: %w", err)
	}
	return out, desc, nil
}

func anyFormula(cs []change) bool {
	for _, c := range cs {
		if !c.clear && c.to.typ == "f" {
			return true
		}
	}
	return false
}

// verify re-reads out and compares it with the input: only the intended
// changes (and the row shift of an insert) may differ.
func verify(inData, outData []byte, in snap, sheets []string, changes []change, ins insertion, mergesBefore map[string][]string) error {
	exp := in.clone()
	if ins.present {
		exp = shiftSnap(in, ins)
	}
	for _, c := range changes {
		if exp[c.sheet] == nil {
			return fmt.Errorf("unknown sheet %q in change", c.sheet)
		}
		if c.clear {
			delete(exp[c.sheet], c.addr)
		} else {
			exp[c.sheet][c.addr] = c.to
		}
	}
	g, err := excelize.OpenReader(bytes.NewReader(outData))
	if err != nil {
		return fmt.Errorf("the result cannot be re-read: %w", err)
	}
	defer g.Close()
	got, err := snapshot(g)
	if err != nil {
		return err
	}
	if fmt.Sprint(g.GetSheetList()) != fmt.Sprint(sheets) {
		return fmt.Errorf("the sheet list changed")
	}
	for sh, m := range exp {
		gm := got[sh]
		for addr, c := range m {
			if d, ok := gm[addr]; !ok || !same(c, d) {
				return fmt.Errorf("sheet %s cell %s is %+v, expected %+v", sh, addr, gm[addr], c)
			}
		}
		for addr, d := range gm {
			if _, ok := m[addr]; !ok {
				return fmt.Errorf("sheet %s cell %s unexpectedly holds %+v", sh, addr, d)
			}
		}
	}
	for sh := range got {
		if _, ok := exp[sh]; !ok {
			return fmt.Errorf("unexpected sheet %s", sh)
		}
	}
	for sh, want := range mergesBefore {
		have := mergeList(g, sh)
		if ins.present && sh == ins.sheet {
			want = shiftMerges(want, ins)
		}
		if fmt.Sprint(have) != fmt.Sprint(want) {
			return fmt.Errorf("merged ranges of %s changed: %v, expected %v", sh, have, want)
		}
	}
	bi, err := scanPackage(inData)
	if err != nil {
		return err
	}
	bo, err := scanPackage(outData)
	if err != nil {
		return err
	}
	if l := bi.lost(bo); len(l) > 0 {
		return fmt.Errorf("editing would lose the workbook's %s", strings.Join(l, ", "))
	}
	return nil
}

func mergeList(f *excelize.File, sheet string) []string {
	ms, _ := f.GetMergeCells(sheet, true)
	var out []string
	for _, m := range ms {
		out = append(out, m.GetStartAxis()+":"+m.GetEndAxis())
	}
	return out
}

var refRE = regexp.MustCompile(`((?:'(?:[^']|'')+'|[A-Za-z_][A-Za-z0-9_.]*)!)?(\$?)([A-Z]{1,3})(\$?)(\d+)`)

func isIdent(b byte) bool {
	return b == '_' || b == '.' || (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// shiftFormula moves the row of every reference to ins.sheet that is at or
// below ins.at down by ins.n (what inserting rows does to a formula).
func shiftFormula(fo, host string, ins insertion) string {
	var sb strings.Builder
	for i := 0; i < len(fo); {
		if fo[i] == '"' { // string literal ("" is an escaped quote)
			j := i + 1
			for j < len(fo) {
				if fo[j] == '"' {
					if j+1 < len(fo) && fo[j+1] == '"' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			j = min(j+1, len(fo))
			sb.WriteString(fo[i:j])
			i = j
			continue
		}
		loc := refRE.FindStringSubmatchIndex(fo[i:])
		if loc == nil {
			sb.WriteString(fo[i:])
			break
		}
		s, e := i+loc[0], i+loc[1]
		sb.WriteString(fo[i:s])
		tok := fo[s:e]
		inWord := s > 0 && isIdent(fo[s-1]) && loc[2] < 0
		call := e < len(fo) && fo[e] == '('
		if inWord || call {
			sb.WriteString(tok)
			i = e
			continue
		}
		sheet := host
		if loc[2] >= 0 {
			sheet = strings.ReplaceAll(strings.Trim(fo[i+loc[2]:i+loc[3]-1], "'"), "''", "'")
		}
		row, _ := strconv.Atoi(fo[i+loc[10] : i+loc[11]])
		if sheet == ins.sheet && row >= ins.at {
			row += ins.n
		}
		pre := ""
		if loc[2] >= 0 {
			pre = fo[i+loc[2] : i+loc[3]]
		}
		sb.WriteString(pre + fo[i+loc[4]:i+loc[5]] + fo[i+loc[6]:i+loc[7]] + fo[i+loc[8]:i+loc[9]] + strconv.Itoa(row))
		i = e
	}
	return sb.String()
}

func shiftSnap(in snap, ins insertion) snap {
	out := snap{}
	for sh, m := range in {
		o := make(map[string]cell, len(m))
		for addr, c := range m {
			if c.typ == "f" {
				c.formula = shiftFormula(c.formula, sh, ins)
			}
			if sh == ins.sheet {
				col, row, _ := excelize.CellNameToCoordinates(addr)
				if row >= ins.at {
					addr, _ = excelize.CoordinatesToCellName(col, row+ins.n)
				}
			}
			o[addr] = c
		}
		out[sh] = o
	}
	return out
}

func shiftMerges(ms []string, ins insertion) []string {
	var out []string
	for _, m := range ms {
		p := strings.Split(m, ":")
		for i, a := range p {
			c, r, _ := excelize.CellNameToCoordinates(a)
			if r >= ins.at {
				p[i], _ = excelize.CoordinatesToCellName(c, r+ins.n)
			}
		}
		out = append(out, strings.Join(p, ":"))
	}
	return out
}
