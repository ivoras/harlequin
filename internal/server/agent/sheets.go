package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/ivoras/harlequin/internal/server/officeconv"
	"github.com/ivoras/harlequin/internal/server/sheetdb"
)

const (
	sheetQueryTimeout = 30 * time.Second
	sheetShowRows     = 200     // rows of a result shown to the model
	sheetSaveRows     = 200_000 // rows save_to and run_js may take
	sheetCacheSize    = 3       // loaded workbooks kept (each up to ~1 GB at MaxCells)
	sheetCacheIdle    = 15 * time.Minute
	sheetCacheMaxAge  = 7 * 24 * time.Hour // even a workbook queried non-stop is reloaded weekly
)

// bigSheetChars: above this much table text read_doc steers to query_sheet.
const bigSheetChars = 30000

const querySheetPrompt = `Answer questions about a spreadsheet (Excel .xlsx/.xls/.ods or CSV) with SQL, without reading the rows yourself. The server loads every sheet into a read-only SQLite table and runs your query on all rows, so it works the same for 50 rows or 5 million. Use it for ANY question about a spreadsheet's data: totals, counts, averages, finding rows, comparing, top-N, checking a rule against every row. Never read a big spreadsheet with read_doc, and never type its numbers into run_js yourself.

How to use it, in two steps:
1. Call query_sheet with only doc (e.g. doc "u.12"). You get the tables and their columns: one table per sheet, with each column's name, type, the original header and example values. Column names are the headers made SQL-safe ("Iznos (EUR)" -> iznos_eur, "Šifra" -> sifra), so they never need quotes. Every table also has _row, the row number in the spreadsheet.
2. Call query_sheet again with the same doc and sql: one SELECT query. Use ONLY the table and column names from step 1.

SQL tips (SQLite):
- Totals per group: SELECT kupac, SUM(iznos) AS ukupno, COUNT(*) AS n FROM prodaja GROUP BY kupac ORDER BY ukupno DESC LIMIT 20
- Find rows: SELECT _row, * FROM prodaja WHERE kupac LIKE '%horvat%' LIMIT 50   (LIKE ignores upper/lower case for A-Z only; = is exact)
- Dates are text 'YYYY-MM-DD': WHERE datum BETWEEN '2026-01-01' AND '2026-03-31'; month: substr(datum, 1, 7); year: substr(datum, 1, 4)
- Column type code = identifiers such as account numbers ("01270000"): stored as text so leading zeros are kept. Never SUM them or turn them into numbers. Compare as text with quotes: WHERE konto = '01270000'; for a group of accounts use LIKE '012%' or substr(konto, 1, 3) = '012'.
- If the same accounts look different in two sheets or files ('01270000' in one, '1270000' in the other), a leading zero was lost: match with ltrim(a.konto, '0') = ltrim(b.konto, '0'). Do not invent other rules (such as removing a digit); check a match rule on all rows with COUNT(*) before using it.
- A column of type text-number holds amounts written as text ("1.234,56", "12,5 €"): wrap it in num(), e.g. SUM(num(iznos)).
- Sheets often contain subtotal or total rows ("Ukupno", "Total") among the data: look for them before summing (e.g. WHERE opis LIKE '%ukupno%') and exclude them, or you will count amounts twice.
- Join sheets or files on a shared column: SELECT a.konto, a.iznos, b.naziv FROM bilanca a JOIN konta b ON b.konto = a.konto
- Compare two files: put the second in more_docs; its tables are loaded next to the first (a clashing table name gets _2 added).
- Only one statement; only SELECT/WITH. Results show at most 200 rows: aggregate or filter instead of paging through data.

To give the user a big result as a file, set save_to (e.g. "tmp://result.md") — all result rows (up to 200000) are written there and only a short confirmation comes back; then call create_excel with source "tmp://result.md". save_to ending in .csv writes CSV, .json writes a JSON array of objects.

If a sheet has title rows above its table and the header was not found (columns named a, b, c…), or the wrong row was taken as the header, set header_row to the spreadsheet row that holds the column names (with sheet to pick that one sheet). header_row -1 means the sheet has no header.`

func (a *Agent) querySheetEntry() toolEntry {
	return toolEntry{
		def: fnTool("query_sheet", querySheetPrompt, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"doc":        map[string]any{"type": "string", "description": `The spreadsheet: a document reference (u.12, s.3, p.7, or as attached, e.g. @"u.18/sales.xlsx"), or a sandbox file (tmp://data.csv, tmp://report.xlsx)`},
				"sql":        map[string]any{"type": "string", "description": "One SQLite SELECT query over the tables. Omit it to see the tables and columns first."},
				"more_docs":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional: more spreadsheets to load next to doc, so one query can join or compare them"},
				"sheet":      map[string]any{"type": "string", "description": "Optional: load only this sheet (its name as in the workbook)"},
				"header_row": map[string]any{"type": "integer", "description": "Optional: the spreadsheet row number holding the column names (-1 = no header). Default: found automatically."},
				"save_to":    map[string]any{"type": "string", "description": `Optional: write ALL result rows to this sandbox file instead of showing them, e.g. "tmp://result.md" (.md for create_excel, .csv, or .json)`},
			},
			"required": []string{"doc"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			refs := append([]string{argString(args, "doc")}, argStrings(args, "more_docs")...)
			opt := sheetdb.Options{Sheet: argString(args, "sheet"), HeaderRow: argInt(args, "header_row", 0)}
			db, labels, errMsg := a.loadSheets(ctx, rc, refs, opt)
			if errMsg != "" {
				return errMsg, nil
			}
			query := strings.TrimSpace(argString(args, "sql"))
			if query == "" {
				return sheetOverview(db, labels), nil
			}
			saveTo := strings.TrimSpace(argString(args, "save_to"))
			keep := sheetShowRows
			if saveTo != "" {
				keep = sheetSaveRows
			}
			qctx, cancel := context.WithTimeout(ctx, sheetQueryTimeout)
			defer cancel()
			res, err := db.Query(qctx, query, keep)
			if err != nil {
				return fmt.Sprintf("error: %v\n\nThe tables and their columns are:\n%s", err, columnList(db)), nil
			}
			if saveTo != "" {
				return a.saveSheetResult(rc, saveTo, res), nil
			}
			return formatSheetResult(res), nil
		},
	}
}

// loadSheets resolves the references and returns their loaded database (from
// the cache when the same files with the same options were loaded recently).
func (a *Agent) loadSheets(ctx context.Context, rc *runContext, refs []string, opt sheetdb.Options) (*sheetdb.DB, []string, string) {
	var srcs []sheetdb.Source
	h := sha256.New()
	fmt.Fprintf(h, "%q %d\x00", strings.ToLower(opt.Sheet), opt.HeaderRow)
	for _, ref := range refs {
		if strings.TrimSpace(ref) == "" {
			continue
		}
		src, errMsg := a.sheetSource(ctx, rc, ref)
		if errMsg != "" {
			return nil, nil, errMsg
		}
		sum := sha256.Sum256(src.Data) // of the original: a legacy file is converted only on a cache miss
		h.Write(sum[:])
		srcs = append(srcs, src)
	}
	if len(srcs) == 0 {
		return nil, nil, `error: doc is required: the spreadsheet's document reference (u.12) or a sandbox file (tmp://data.csv)`
	}
	labels := make([]string, len(srcs))
	for i, s := range srcs {
		labels[i] = s.Label
	}
	db, err := a.sheets.get(hex.EncodeToString(h.Sum(nil)), func() (*sheetdb.DB, error) {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*sheetQueryTimeout)
		defer cancel()
		for i, s := range srcs {
			if isLegacySheet(s) {
				data, name, err := officeconv.ToModern(lctx, s.Name, s.Data)
				if err != nil {
					return nil, fmt.Errorf("%s could not be converted to .xlsx for querying: %w", s.Label, err)
				}
				srcs[i].Data, srcs[i].Name = data, name
			}
		}
		return sheetdb.Load(lctx, srcs, opt)
	})
	if err != nil {
		return nil, nil, "error: " + err.Error()
	}
	return db, labels, ""
}

// sheetSource reads one spreadsheet: a document's stored original or a
// sandbox file.
func (a *Agent) sheetSource(ctx context.Context, rc *runContext, ref string) (sheetdb.Source, string) {
	ref = strings.TrimSpace(ref)
	var src sheetdb.Source
	mime := ""
	if isSandboxPath(ref) {
		root, _, rel, err := a.resolveNamespaceRoot(rc, ref)
		if err != nil {
			return src, "error: " + err.Error()
		}
		data, err := root.Read(rel)
		if err != nil {
			return src, fmt.Sprintf("error: cannot read %s: %v", ref, err)
		}
		src = sheetdb.Source{Label: ref, Name: rel, Data: data}
	} else {
		doc, errMsg := a.loadDocRef(ctx, rc, ref)
		if errMsg != "" {
			return src, errMsg
		}
		data, name, m, ok := a.storedOriginal(ctx, rc, doc)
		if !ok {
			return src, fmt.Sprintf("error: document %s (%q) has no stored file to query; query_sheet needs an uploaded or generated spreadsheet.", docRef(doc), doc.Title)
		}
		if path.Ext(name) == "" {
			name += path.Ext(doc.Title)
		}
		src, mime = sheetdb.Source{Label: fmt.Sprintf("%s %q", docRef(doc), doc.Title), Name: name, Data: data}, m
	}
	low := strings.ToLower(src.Name)
	switch {
	case isLegacySheet(src): // .ods is a zip too: check first
		return src, ""
	case bytes.HasPrefix(src.Data, []byte("PK")):
		if !strings.HasSuffix(low, ".xlsx") && !strings.HasSuffix(low, ".xlsm") && mime != xlsxMime {
			src.Name += ".xlsx" // a zip without a telling name: let the reader decide
		}
		return src, ""
	case strings.HasSuffix(low, ".csv") || strings.HasSuffix(low, ".tsv") || mime == "text/csv" || mime == "text/tab-separated-values":
		if !strings.HasSuffix(low, ".csv") && !strings.HasSuffix(low, ".tsv") {
			src.Name += ".csv"
		}
		return src, ""
	}
	return src, fmt.Sprintf("error: %s is not a spreadsheet (Excel .xlsx/.xls/.ods or CSV), so it has no tables to query. For text documents use read_doc, search_docs or check_text.", src.Label)
}

// isLegacySheet: a spreadsheet LibreOffice must turn into .xlsx first. The
// loader converts it, so a cache hit skips LibreOffice.
func isLegacySheet(s sheetdb.Source) bool {
	switch strings.ToLower(path.Ext(s.Name)) {
	case ".xls", ".xlsb", ".ods":
		return true
	}
	return false
}

// sheetOverview is the schema plus ready-to-run example queries built from the
// actual names, so a small model starts from something that works.
func sheetOverview(db *sheetdb.DB, labels []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Loaded %s: %d table(s). Query them with the sql argument (SQLite).\n\n%s\n", strings.Join(labels, " and "), len(db.Tables), db.Schema())
	var t *sheetdb.Table
	for _, x := range db.Tables {
		if t == nil || x.Rows > t.Rows {
			t = x
		}
	}
	if t == nil || t.Rows == 0 {
		return sb.String()
	}
	var group, sum string
	for _, c := range t.Columns {
		switch c.Type() {
		case "text", "date":
			if group == "" {
				group = c.Name
			}
		case "number", "integer":
			if sum == "" && !strings.Contains(c.Name, "rbr") && c.Name != "id" {
				sum = c.Name
			}
		case "text-number":
			if sum == "" {
				sum = "num(" + c.Name + ")"
			}
		}
	}
	fmt.Fprintf(&sb, "\nExample queries:\n  SELECT * FROM %s LIMIT 5\n  SELECT * FROM %s ORDER BY _row DESC LIMIT 5   -- the last rows: totals often sit there\n", t.Name, t.Name)
	if group != "" && sum != "" {
		fmt.Fprintf(&sb, "  SELECT %s, SUM(%s) AS total, COUNT(*) AS n FROM %s GROUP BY %s ORDER BY total DESC LIMIT 20\n", group, sum, t.Name, group)
	}
	return sb.String()
}

func columnList(db *sheetdb.DB) string {
	var sb strings.Builder
	for _, t := range db.Tables {
		names := []string{"_row"}
		for _, c := range t.Columns {
			names = append(names, c.Name)
		}
		fmt.Fprintf(&sb, "  %s(%s)\n", t.Name, strings.Join(names, ", "))
	}
	return sb.String()
}

func formatSheetResult(res *sheetdb.Result) string {
	if res.Total == 0 {
		return "0 rows. If you expected matches: text comparison with = is exact (case, spaces, accents); try LIKE '%part%', check the spelling against the example values, or trim(column)."
	}
	head := fmt.Sprintf("%d row(s):\n\n", res.Total)
	if res.Total > len(res.Rows) {
		head = fmt.Sprintf("%d rows; the first %d are shown:\n\n", res.Total, len(res.Rows))
	}
	out := head + sheetdb.Markdown(res.Columns, res.Rows, 300)
	if res.Total > len(res.Rows) {
		out += fmt.Sprintf("\n[%d more rows not shown. Don't page through them: aggregate (GROUP BY, SUM, COUNT), filter (WHERE), or write them all to a file with save_to and give it to the user with create_excel.]", res.Total-len(res.Rows))
	}
	return out
}

func (a *Agent) saveSheetResult(rc *runContext, saveTo string, res *sheetdb.Result) string {
	if !isSandboxPath(saveTo) {
		return fmt.Sprintf("error: save_to must be a sandbox file such as tmp://result.md, not %q", saveTo)
	}
	if res.Total > len(res.Rows) {
		return fmt.Sprintf("error: the result has %d rows, more than the %d save_to takes; filter or aggregate it first. Nothing was saved.", res.Total, sheetSaveRows)
	}
	root, prefix, rel, err := a.resolveNamespaceRoot(rc, saveTo)
	if err != nil {
		return "error: " + err.Error()
	}
	var data []byte
	next := ""
	switch strings.ToLower(path.Ext(rel)) {
	case ".csv":
		data = sheetdb.CSV(res.Columns, res.Rows)
	case ".json":
		data, _ = json.Marshal(sheetdb.Objects(res.Columns, res.Rows))
	case ".md":
		name := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
		data = []byte("# " + name + "\n\n" + sheetdb.Markdown(res.Columns, res.Rows, 0))
		next = fmt.Sprintf(" To give it to the user as an Excel file, call create_excel with source %q.", prefix+rel)
	default:
		return "error: save_to must end in .md (for create_excel), .csv or .json"
	}
	if err := root.Write(rel, data); err != nil {
		return fmt.Sprintf("error: cannot write %s: %v. Nothing was saved; filter or aggregate the result to make it smaller.", saveTo, err)
	}
	return fmt.Sprintf("Saved all %d rows (columns: %s) to %s%s (%d bytes).%s", res.Total, strings.Join(res.Columns, ", "), prefix, rel, len(data), next)
}

// sheetJSGlobals are the run_js helpers: sheets.query / sheets.tables and
// read_doc (the name models reach for when they want a document's text).
func (a *Agent) sheetJSGlobals(ctx context.Context, rc *runContext) map[string]any {
	opts := func(o map[string]any) ([]string, sheetdb.Options) {
		return argStrings(o, "more_docs"), sheetdb.Options{Sheet: argString(o, "sheet"), HeaderRow: argInt(o, "header_row", 0)}
	}
	load := func(doc string, o map[string]any) (*sheetdb.DB, error) {
		more, opt := opts(o)
		db, _, errMsg := a.loadSheets(ctx, rc, append([]string{doc}, more...), opt)
		if errMsg != "" {
			return nil, errors.New(strings.TrimPrefix(errMsg, "error: "))
		}
		return db, nil
	}
	return map[string]any{
		"sheets": map[string]any{
			// Native form so each row object keeps the SELECT's column order
			// (a Go map would print its keys in random order).
			"query": func(call goja.FunctionCall, vm *goja.Runtime) goja.Value {
				o, _ := call.Argument(2).Export().(map[string]any)
				db, err := load(call.Argument(0).String(), o)
				if err != nil {
					panic(vm.NewGoError(err))
				}
				qctx, cancel := context.WithTimeout(ctx, sheetQueryTimeout)
				defer cancel()
				res, err := db.Query(qctx, call.Argument(1).String(), sheetSaveRows)
				if err != nil {
					panic(vm.NewGoError(err))
				}
				if res.Total > len(res.Rows) {
					panic(vm.NewGoError(fmt.Errorf("the query returns %d rows, more than %d; filter or aggregate in SQL", res.Total, sheetSaveRows)))
				}
				out := make([]any, len(res.Rows))
				for i, r := range res.Rows {
					obj := vm.NewObject()
					for j, c := range res.Columns {
						_ = obj.Set(c, r[j])
					}
					out[i] = obj
				}
				return vm.NewArray(out...)
			},
			"tables": func(doc string, o map[string]any) ([]any, error) {
				db, err := load(doc, o)
				if err != nil {
					return nil, err
				}
				var out []any
				for _, t := range db.Tables {
					var cols []any
					for _, c := range t.Columns {
						cols = append(cols, map[string]any{"name": c.Name, "type": c.Type(), "header": c.Header, "letter": c.Letter})
					}
					out = append(out, map[string]any{"table": t.Name, "sheet": t.Sheet, "rows": t.Rows, "header_row": t.HeaderRow, "columns": cols})
				}
				return out, nil
			},
		},
		"read_doc": func(ref string) (string, error) {
			text, _, errMsg := a.sourceText(ctx, rc, ref)
			if errMsg != "" {
				return "", errors.New(strings.TrimPrefix(errMsg, "error: "))
			}
			return text, nil
		},
	}
}

// sheetCache keeps recently loaded workbooks: a model asks several questions
// of one file in a turn, and loading a million cells takes seconds.
type sheetCache struct {
	mu      sync.Mutex
	entries map[string]*sheetEntry
}

type sheetEntry struct {
	once         sync.Once
	db           *sheetdb.DB
	err          error
	loaded, used time.Time
}

func (c *sheetCache) get(key string, load func() (*sheetdb.DB, error)) (*sheetdb.DB, error) {
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]*sheetEntry{}
	}
	now := time.Now()
	var drop []*sheetEntry
	e := c.entries[key]
	if e != nil && now.Sub(e.loaded) > sheetCacheMaxAge {
		drop = append(drop, e)
		e = nil
	}
	if e == nil {
		e = &sheetEntry{loaded: now}
		c.entries[key] = e
	}
	e.used = now
	for k, x := range c.entries {
		if x != e && (now.Sub(x.used) > sheetCacheIdle || now.Sub(x.loaded) > sheetCacheMaxAge) {
			drop = append(drop, x)
			delete(c.entries, k)
		}
	}
	for len(c.entries) > sheetCacheSize {
		oldK, old := "", (*sheetEntry)(nil)
		for k, x := range c.entries {
			if x != e && (old == nil || x.used.Before(old.used)) {
				oldK, old = k, x
			}
		}
		drop = append(drop, old)
		delete(c.entries, oldK)
	}
	c.mu.Unlock()
	for _, x := range drop {
		x.once.Do(func() {}) // a load still running finishes first
		if x.db != nil {
			// shortcut: a query that fetched this db just before eviction gets
			// "database closed"; upgrade to refcounts if that shows up in logs.
			x.db.Close()
		}
	}
	// Loads of different files run in parallel; the same file loads once.
	e.once.Do(func() { e.db, e.err = load() })
	if e.err != nil {
		c.mu.Lock()
		if c.entries[key] == e {
			delete(c.entries, key) // don't cache failures
		}
		c.mu.Unlock()
	}
	return e.db, e.err
}
