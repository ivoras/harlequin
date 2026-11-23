package sheetdb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// View stacks tables that have the same columns (monthly exports of one
// report, one sheet per month) into one, with _file and _sheet saying where
// each row came from, so a question over all of them is one query.
type View struct {
	Name    string
	Tables  []string
	Columns []string // the shared data columns
}

func createViews(ctx context.Context, tx *sql.Tx, tables []*Table, used map[string]bool) ([]*View, error) {
	var order []string
	groups := map[string][]*Table{}
	for _, t := range tables {
		cols := dataColumns(t)
		if len(cols) == 0 || t.Rows == 0 {
			continue
		}
		sig := strings.Join(cols, "\x00")
		if groups[sig] == nil {
			order = append(order, sig)
		}
		groups[sig] = append(groups[sig], t)
	}
	var views []*View
	for _, sig := range order {
		g := groups[sig]
		if len(g) < 2 {
			continue
		}
		cols := strings.Split(sig, "\x00")
		// Colour columns every table has come along ("the blue rows of all months").
		for _, c := range g[0].Columns {
			if c.FillOf == "" {
				continue
			}
			all := true
			for _, t := range g[1:] {
				all = all && columnByName(t, c.Name).FillOf != ""
			}
			if all {
				cols = append(cols, c.Name)
			}
		}
		v := &View{Name: uniqueName(viewName(g), used), Columns: cols}
		// A column numeric in one table and text in another (codes stored as
		// numbers in one export) is text in the view: one type per column, so
		// = and joins see every row.
		text := map[string]bool{}
		for _, c := range cols {
			num, other := false, false
			for _, t := range g {
				switch columnByName(t, c).Type() {
				case "integer", "number":
					num = true
				case "empty":
				default:
					other = true
				}
			}
			text[c] = num && other
		}
		var arms []string
		for _, t := range g {
			sel := []string{lit(t.File) + " AS _file", lit(t.Sheet) + " AS _sheet", `"_row"`}
			for _, c := range cols {
				if text[c] {
					sel = append(sel, fmt.Sprintf("CAST(%s AS TEXT) AS %s", quote(c), quote(c)))
				} else {
					sel = append(sel, quote(c))
				}
			}
			arms = append(arms, fmt.Sprintf("SELECT %s FROM %s", strings.Join(sel, ", "), quote(t.Name)))
			v.Tables = append(v.Tables, t.Name)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("CREATE VIEW %s AS %s", quote(v.Name), strings.Join(arms, " UNION ALL "))); err != nil {
			return nil, err
		}
		views = append(views, v)
	}
	return views, nil
}

func dataColumns(t *Table) []string {
	var out []string
	for _, c := range t.Columns {
		if c.FillOf == "" {
			out = append(out, c.Name)
		}
	}
	return out
}

func columnByName(t *Table, name string) *Column {
	for _, c := range t.Columns {
		if c.Name == name {
			return c
		}
	}
	return &Column{}
}

// viewName: the tables' common name prefix ("f2_podaci_01_26", "f2_podaci_02_26"
// → f2_podaci_all), or combined_all when they share none worth keeping.
func viewName(g []*Table) string {
	p := g[0].Name
	for _, t := range g[1:] {
		for !strings.HasPrefix(t.Name, p) {
			p = p[:len(p)-1]
		}
	}
	p = strings.TrimRight(p, "_0123456789")
	if len(p) < 2 {
		p = "combined"
	}
	return p + "_all"
}

func lit(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
