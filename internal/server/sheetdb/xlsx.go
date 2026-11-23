package sheetdb

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
	"time"
)

// isoDate marks a cell whose number format is a date/time: the serial number
// is already turned into ISO text ("2026-01-21", "2026-01-21 08:30:00").
type isoDate string

// readXLSX streams every worksheet, calling sheet() at the start of each and
// row() for each row with at least one value (rowNum is the 1-based
// spreadsheet row; cells[i] is column i, nil when empty). Values are float64,
// int64, string, isoDate or nil. The sheet XML is never held in memory whole.
func readXLSX(data []byte, sheet func(name string, hidden bool) error, row func(rowNum int, cells []any) error) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("not an Excel (.xlsx) file: %w", err)
	}
	parts := map[string]*zip.File{}
	for _, f := range zr.File {
		parts[f.Name] = f
	}
	if parts["xl/workbook.xml"] == nil {
		return fmt.Errorf("not an Excel (.xlsx) file: no workbook")
	}
	var shared []string
	if f := parts["xl/sharedStrings.xml"]; f != nil {
		if shared, err = readShared(f); err != nil {
			return fmt.Errorf("shared strings: %w", err)
		}
	}
	styles := map[int]cellFmt{}
	if f := parts["xl/styles.xml"]; f != nil {
		styles, _ = readStyles(f) // unreadable styles: dates and padded codes stay plain numbers
	}
	wb, err := readAll(parts["xl/workbook.xml"])
	if err != nil {
		return err
	}
	var wd struct {
		Pr struct {
			Date1904 string `xml:"date1904,attr"`
		} `xml:"workbookPr"`
		Sheets []struct {
			Name  string `xml:"name,attr"`
			State string `xml:"state,attr"`
			RID   string `xml:"id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal(wb, &wd); err != nil {
		return fmt.Errorf("workbook: %w", err)
	}
	epoch := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	if wd.Pr.Date1904 == "1" || wd.Pr.Date1904 == "true" {
		epoch = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	targets := map[string]string{}
	if f := parts["xl/_rels/workbook.xml.rels"]; f != nil {
		b, _ := readAll(f)
		var rd struct {
			R []struct{ ID, Target string } `xml:"Relationship"`
		}
		if xml.Unmarshal(b, &rd) == nil {
			for _, r := range rd.R {
				if t, abs := strings.CutPrefix(r.Target, "/"); abs {
					targets[r.ID] = t
				} else {
					targets[r.ID] = path.Join("xl", r.Target)
				}
			}
		}
	}
	for i, sh := range wd.Sheets {
		name := strings.TrimSpace(sh.Name)
		if name == "" {
			name = fmt.Sprintf("Sheet%d", i+1)
		}
		if err := sheet(name, sh.State == "hidden" || sh.State == "veryHidden"); err != nil {
			return err
		}
		p := targets[sh.RID]
		if p == "" {
			p = fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)
		}
		f := parts[p]
		if f == nil {
			continue // a chart sheet or a missing part: an empty table
		}
		if err := streamSheet(f, shared, styles, epoch, row); err != nil {
			return fmt.Errorf("sheet %q: %w", name, err)
		}
	}
	return nil
}

func readAll(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func decoder(f *zip.File) (*xml.Decoder, io.Closer, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, nil, err
	}
	return xml.NewDecoder(bufio.NewReaderSize(rc, 1<<16)), rc, nil
}

// readShared reads the shared-string table: each <si> is one string, the
// concatenation of its <t> runs (phonetic <rPh> runs are skipped).
func readShared(f *zip.File) ([]string, error) {
	dec, c, err := decoder(f)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	var out []string
	var cur strings.Builder
	inT, inPh := false, false
	for {
		tok, err := dec.RawToken()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				cur.Reset()
			case "t":
				inT = true
			case "rPh":
				inPh = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				out = append(out, cur.String())
			case "t":
				inT = false
			case "rPh":
				inPh = false
			}
		case xml.CharData:
			if inT && !inPh {
				cur.Write(t)
			}
		}
	}
}

// cellFmt is what a cell style's number format means for its value.
type cellFmt struct {
	date bool // shows a date or time
	pad  int  // shows a whole number zero-padded to this many digits ("00000000"), 0 = no
}

// readStyles maps cell-style indexes (the s attribute of a cell) to the
// number-format facts that change how a value reads.
func readStyles(f *zip.File) (map[int]cellFmt, error) {
	b, err := readAll(f)
	if err != nil {
		return nil, err
	}
	var st struct {
		NumFmts []struct {
			ID   int    `xml:"numFmtId,attr"`
			Code string `xml:"formatCode,attr"`
		} `xml:"numFmts>numFmt"`
		Xfs []struct {
			NumFmtID int `xml:"numFmtId,attr"`
		} `xml:"cellXfs>xf"`
	}
	if err := xml.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	custom := map[int]string{}
	for _, n := range st.NumFmts {
		custom[n.ID] = n.Code
	}
	out := map[int]cellFmt{}
	for i, x := range st.Xfs {
		if code, ok := custom[x.NumFmtID]; ok {
			out[i] = cellFmt{date: isDateFormat(code), pad: zeroPad(code)}
		} else {
			out[i] = cellFmt{date: builtinDate(x.NumFmtID)}
		}
	}
	return out, nil
}

// zeroPad: a format of only zeros ("00000000", as accounting exports use for
// account numbers stored as numbers) shows the number padded to that width.
func zeroPad(code string) int {
	code, _, _ = strings.Cut(code, ";")
	if len(code) >= 2 && strings.Trim(code, "0") == "" {
		return len(code)
	}
	return 0
}

// builtinDate: the built-in number formats that are dates or times
// (ECMA-376 18.8.30, plus the common East Asian date ids).
func builtinDate(id int) bool {
	return (id >= 14 && id <= 22) || (id >= 27 && id <= 36) || (id >= 45 && id <= 47) || (id >= 50 && id <= 58)
}

// isDateFormat tells whether a custom format code shows a date or time: it
// has d/m/y/h/s outside quoted text, escapes and [..] sections (colours,
// locales; [h] elapsed hours counts as time).
func isDateFormat(code string) bool {
	if i := strings.IndexByte(code, ';'); i >= 0 {
		code = code[:i] // the positive section decides
	}
	code = strings.ToLower(code)
	inQuote, inBracket := false, false
	for i := 0; i < len(code); i++ {
		c := code[i]
		switch {
		case inQuote:
			inQuote = c != '"'
		case inBracket:
			if c == ']' {
				inBracket = false
			} else if c == 'h' || c == 'm' || c == 's' {
				return true
			}
		case c == '"':
			inQuote = true
		case c == '[':
			inBracket = true
		case c == '\\' || c == '_' || c == '*':
			i++ // escaped or padding character
		case c == 'd' || c == 'm' || c == 'y' || c == 'h' || c == 's':
			return true
		}
	}
	return false
}

// serialDate turns an Excel serial number into ISO text.
func serialDate(v float64, epoch time.Time) isoDate {
	if v < 1 {
		return isoDate(epoch.Add(time.Duration(math.Round(v*86400)) * time.Second).Format("15:04:05"))
	}
	if epoch.Year() == 1899 && v < 60 {
		v++ // Excel's fictional 1900-02-29 (serial 60): earlier serials are a day off from the 1899-12-30 epoch
	}
	d := epoch.Add(time.Duration(math.Round(v*86400)) * time.Second)
	if d.Hour() == 0 && d.Minute() == 0 && d.Second() == 0 {
		return isoDate(d.Format("2006-01-02"))
	}
	return isoDate(d.Format("2006-01-02 15:04:05"))
}

func streamSheet(f *zip.File, shared []string, styles map[int]cellFmt, epoch time.Time, row func(int, []any) error) error {
	dec, c, err := decoder(f)
	if err != nil {
		return err
	}
	defer c.Close()
	var cells []any
	rowNum, lastRow := 0, 0
	col, nextCol := 0, 0
	var typ string
	style := 0
	var text strings.Builder
	inV, inT, inCell := false, false, false
	has := false
	for {
		tok, err := dec.RawToken()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				cells, has, nextCol = cells[:0], false, 0
				rowNum = lastRow + 1 // r is optional; rows then follow one another
				for _, a := range t.Attr {
					if a.Name.Local == "r" {
						if n, err := strconv.Atoi(a.Value); err == nil {
							rowNum = n
						}
					}
				}
				lastRow = rowNum
			case "c":
				inCell, typ, style, col = true, "", 0, nextCol
				text.Reset()
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "t":
						typ = a.Value
					case "s":
						style, _ = strconv.Atoi(a.Value)
					case "r":
						col = colIndex(a.Value)
					}
				}
				nextCol = col + 1
			case "v":
				inV = inCell
			case "t":
				inT = inCell
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inT = false
			case "c":
				inCell = false
				v := cellValue(typ, text.String(), shared, styles[style], epoch)
				if v == nil {
					continue
				}
				for len(cells) <= col {
					cells = append(cells, nil)
				}
				cells[col], has = v, true
			case "row":
				if has {
					if err := row(rowNum, cells); err != nil {
						return err
					}
				}
			}
		case xml.CharData:
			if inV || inT {
				text.Write(t)
			}
		}
	}
}

func cellValue(typ, raw string, shared []string, cf cellFmt, epoch time.Time) any {
	switch typ {
	case "s":
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n < 0 || n >= len(shared) {
			return nil
		}
		return textValue(shared[n])
	case "inlineStr", "str", "e":
		return textValue(raw)
	case "b":
		if strings.TrimSpace(raw) == "1" {
			return int64(1)
		}
		return int64(0)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return textValue(raw)
	}
	if cf.date && v >= 0 && v < 2958466 { // up to 9999-12-31
		return serialDate(v, epoch)
	}
	if cf.pad > 0 && v >= 0 && v == math.Trunc(v) && v < 1e15 {
		return fmt.Sprintf("%0*d", cf.pad, int64(v)) // the code as the user sees it, leading zeros included
	}
	return number(v)
}

func textValue(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// number keeps whole values as integers so they print and compare as such.
func number(v float64) any {
	if v == math.Trunc(v) && math.Abs(v) < 1<<53 {
		return int64(v)
	}
	return v
}

// colIndex turns an A1 reference's letters into a 0-based column ("C5" -> 2).
func colIndex(ref string) int {
	n := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		n = n*26 + int(r-'A') + 1
	}
	return max(n-1, 0)
}

// colName turns a 0-based column into its letters (2 -> "C").
func colName(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('A'+(i-1)%26)) + s
	}
	return s
}
