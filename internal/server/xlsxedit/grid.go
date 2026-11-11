package xlsxedit

import (
	"fmt"
	"strings"

	"github.com/xuri/excelize/v2"
)

// Grid renders the workbook as Markdown with the sheet names and cell
// addresses a model needs to point at a cell: one section per sheet, a header
// row of column letters, the row number first in every row, formulas as their
// text and merged ranges listed under the table.
func Grid(data []byte) (string, error) {
	f, err := open(data)
	if err != nil {
		return "", err
	}
	defer f.Close()
	in, err := snapshot(f)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for i, sh := range f.GetSheetList() {
		if i > 0 {
			sb.WriteString("\n")
		}
		maxRow, maxCol := 0, 0
		for addr := range in[sh] {
			c, r, _ := excelize.CellNameToCoordinates(addr)
			maxRow, maxCol = max(maxRow, r), max(maxCol, c)
		}
		if maxRow == 0 {
			fmt.Fprintf(&sb, "## Sheet %q (empty)\n", sh)
			continue
		}
		last, _ := excelize.CoordinatesToCellName(maxCol, maxRow)
		fmt.Fprintf(&sb, "## Sheet %q (A1:%s)\n\n|   |", sh, last)
		sep := "|---|"
		for c := 1; c <= maxCol; c++ {
			name, _ := excelize.ColumnNumberToName(c)
			fmt.Fprintf(&sb, " %s |", name)
			sep += "---|"
		}
		sb.WriteString("\n" + sep + "\n")
		for r := 1; r <= maxRow; r++ {
			fmt.Fprintf(&sb, "| %d |", r)
			for c := 1; c <= maxCol; c++ {
				addr, _ := excelize.CoordinatesToCellName(c, r)
				cl := in[sh][addr]
				v := cl.val
				if cl.typ == "f" {
					v = "=" + cl.formula
				}
				v = strings.NewReplacer("|", `\|`, "\n", " ", "\r", "").Replace(v)
				sb.WriteString(" " + v + " |")
			}
			sb.WriteString("\n")
		}
		if ms := mergeList(f, sh); len(ms) > 0 {
			fmt.Fprintf(&sb, "\nMerged: %s\n", strings.Join(ms, ", "))
		}
	}
	return sb.String(), nil
}
