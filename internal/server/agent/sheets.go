package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dop251/goja"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/officeconv"
	"github.com/ivoras/harlequin/internal/server/sheetdb"
)

const (
	sheetQueryTimeout = 30 * time.Second
	sheetShowRows     = 200     // rows of a result shown to the model
	sheetSaveRows     = 200_000 // rows save_to and run_js may take
	sheetCacheSize    = 6       // open databases kept: one file's are disk-backed, several files' in memory
	sheetCacheIdle    = 15 * time.Minute
	sheetCacheMaxAge  = 7 * 24 * time.Hour // even a workbook queried non-stop is reloaded weekly
)

// bigSheetChars: above this much table text read_doc steers to query_sheet.
const bigSheetChars = 30000

const querySheetPrompt = `Answer questions about spreadsheets (Excel .xlsx/.xls/.xlsb, OpenDocument .ods, CSV, TSV) — and about the tables inside PDF and Word documents — with SQL, without reading the rows yourself. The server loads every sheet into a read-only SQLite table and runs your query on all rows, so it works the same for 50 rows or 5 million. Use it for ANY question about the data: totals, counts, finding rows, comparing or reconciling files, looking values up in another file, checking a rule against every row. Never read a big spreadsheet with read_doc, and never type its numbers into run_js yourself.

How to use it, in two steps:
1. Call query_sheet with only doc (e.g. doc "u.12"), plus more_docs listing EVERY other file the question involves (all attached spreadsheets, in one call). You get the tables and their columns: one table per sheet, each column's name, type, original header, Excel column letter and example values. Column names are the headers made SQL-safe ("Iznos (EUR)" -> iznos_eur), so they never need quotes. Every table has _row, the row number in the spreadsheet.
2. Call query_sheet again with the same doc and more_docs and sql: one SELECT query. Use ONLY the table and column names from step 1.

Several files: with more than one file, each table is named after its file ("F2 Podaci 03-2026.xlsx" -> f2_podaci_03_2026). Files or sheets with the same columns (monthly exports of one report) are also combined into a VIEW named ..._all with extra columns _file and _sheet: query that view instead of writing UNION ALL yourself, e.g. SELECT _file, SUM(iznos) FROM f2_podaci_all GROUP BY _file.

When the user names a column by its Excel letter ("column V"), find the column whose letter is V in step 1's list.

SQL recipes (SQLite):
- Totals per group: SELECT kupac, SUM(iznos) AS ukupno, COUNT(*) AS n FROM prodaja GROUP BY kupac ORDER BY ukupno DESC LIMIT 20
- Find rows: SELECT _row, * FROM prodaja WHERE kupac LIKE '%horvat%' LIMIT 50   (LIKE ignores upper/lower case for A-Z only; = is exact)
- Dates are text 'YYYY-MM-DD': WHERE datum BETWEEN '2026-01-01' AND '2026-03-31'; month: substr(datum, 1, 7)
- Match keys with key(): two systems write the same invoice or account number differently ('1/11/01' and '1-11-1', '01270000' and 1270000); key() normalises both (case, spaces, separators, leading zeros), so join ON key(a.racun) = key(b.racun). Check a match rule on all rows with COUNT(*) before relying on it; never invent rules such as dropping a digit.
- Reconcile A against B (what is missing, what differs):
  only in A: SELECT a.* FROM a LEFT JOIN b ON key(b.racun) = key(a.racun) WHERE b.racun IS NULL
  only in B: the same with a and b swapped
  in both but different amounts: SELECT a.racun, a.iznos, b.iznos FROM a JOIN b ON key(b.racun) = key(a.racun) WHERE abs(a.iznos - b.iznos) > 0.005
- Look a value up in another file (like VLOOKUP): SELECT a.*, b.datum FROM a LEFT JOIN b ON key(b.racun) = key(a.racun)
- Column type code = identifiers such as account numbers ("01270000"): text, leading zeros kept. Never SUM them. Compare as text: WHERE konto = '01270000'; a group of accounts: konto LIKE '25%'.
- Column type text-number = amounts written as text ("1.234,56"): SUM(num(iznos)).
- Column type color = the background colour of the cells of another column ("light blue #BDD7EE"), when the sheet uses colour to mark rows (e.g. paid ones): WHERE status_color LIKE '%blue%'.
- Sheets often contain subtotal or total rows ("Ukupno", "Total") among the data: look for them before summing and exclude them, or you will count amounts twice.
- Only one statement; only SELECT/WITH. Results show at most 200 rows: aggregate or filter instead of paging through data.

A PDF or Word document can be a doc too: its tables (as extracted from the file) become tables named after the heading above them. Extraction can split or garble a table: check the row count and a few rows against what the user expects.

To give the user a big result as a file, set save_to (e.g. "tmp://result.md") — all result rows (up to 200000) are written there and only a short confirmation comes back; then call create_excel with source "tmp://result.md". save_to ending in .csv writes CSV, .json writes a JSON array of objects.

If a sheet has title rows above its table and the header was not found (columns named a, b, c…), or the wrong row was taken as the header, set header_row to the spreadsheet row that holds the column names (with sheet to pick that one sheet). header_row -1 means the sheet has no header.`

func (a *Agent) querySheetEntry() toolEntry {
	return toolEntry{
		def: fnTool("query_sheet", querySheetPrompt, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"doc":        map[string]any{"type": "string", "description": `The spreadsheet (.xlsx, .xls, .xlsb, .ods, .csv, .tsv) or a PDF/Word document with tables: a document reference (u.12, s.3, p.7, or as attached, e.g. @"u.18/sales.xlsx"), or a sandbox file (tmp://data.csv, tmp://report.xlsx)`},
				"sql":        map[string]any{"type": "string", "description": "One SQLite SELECT query over the tables. Omit it to see the tables and columns first."},
				"more_docs":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Every other file the question involves (e.g. all attached spreadsheets), loaded next to doc so one query can join, compare or stack them. Pass the same list on every call."},
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

// sheetSrc is one resolved spreadsheet: what to load, and where its
// per-document cache file goes ("" for sandbox files and non-default options).
type sheetSrc struct {
	sheetdb.Source
	sum   string // hex sha256 of the original bytes (or a document's text)
	cache string
}

// loadSheets resolves the references and returns their loaded database. A
// document's sheets are kept on disk as a cache file next to its stored
// original (built on first use or right after upload, valid for a week), so
// a returning user's first query does not reload the workbook; several files
// are put together from their cache files in a fraction of a second.
func (a *Agent) loadSheets(ctx context.Context, rc *runContext, refs []string, opt sheetdb.Options) (*sheetdb.DB, []string, string) {
	var srcs []sheetSrc
	h := sha256.New()
	fmt.Fprintf(h, "%q %d\x00", strings.ToLower(opt.Sheet), opt.HeaderRow)
	for _, ref := range refs {
		if strings.TrimSpace(ref) == "" {
			continue
		}
		src, errMsg := a.sheetSource(ctx, rc, ref, opt == sheetdb.Options{})
		if errMsg != "" {
			return nil, nil, errMsg
		}
		h.Write([]byte(src.sum))
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
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*sheetQueryTimeout)
		defer cancel()
		if len(srcs) == 1 {
			if srcs[0].cache != "" {
				return ensureSheetCache(lctx, srcs[0], true)
			}
			s, err := convertLegacy(lctx, srcs[0].Source)
			if err != nil {
				return nil, err
			}
			return sheetdb.Load(lctx, []sheetdb.Source{s}, opt)
		}
		parts := make([]sheetdb.Part, len(srcs))
		for i, s := range srcs {
			if s.cache == "" {
				// Not a document (or unusual options): a throwaway file for this assembly.
				f, err := os.CreateTemp("", "sheetdb-*.db")
				if err != nil {
					return nil, err
				}
				f.Close()
				defer os.Remove(f.Name())
				s.cache = f.Name()
				cs, err := convertLegacy(lctx, s.Source)
				if err != nil {
					return nil, err
				}
				d, err := sheetdb.Build(lctx, cs, opt, s.cache)
				if err != nil {
					return nil, err
				}
				d.Close()
			} else if _, err := ensureSheetCache(lctx, s, false); err != nil {
				return nil, err
			}
			parts[i] = sheetdb.Part{Path: s.cache, Label: s.Label, Short: s.Short}
		}
		return sheetdb.Assemble(lctx, parts)
	})
	if err != nil {
		return nil, nil, "error: " + err.Error()
	}
	return db, labels, ""
}

// ensureSheetCache makes sure s's cache file exists, is this version's and
// under a week old, building it otherwise. With open it returns the database
// (read from the file, or the in-memory one just built).
func ensureSheetCache(ctx context.Context, s sheetSrc, open bool) (*sheetdb.DB, error) {
	if fi, err := os.Stat(s.cache); err == nil && time.Since(fi.ModTime()) < sheetCacheMaxAge {
		if !open {
			return nil, nil
		}
		if d, err := sheetdb.Open(ctx, s.cache); err == nil {
			return d, nil
		}
		// Unreadable or another version: rebuild below.
	}
	cs, err := convertLegacy(ctx, s.Source)
	if err != nil {
		return nil, err
	}
	d, err := sheetdb.Build(ctx, cs, sheetdb.Options{}, s.cache)
	if err != nil {
		return nil, err
	}
	removeOtherSheetCaches(s.cache)
	if !open {
		d.Close()
		return nil, nil
	}
	return d, nil
}

// sheetCachePath is where a stored file's sheet cache goes: next to it, named
// by content hash and loader version, so an edited file or a changed loader
// never reads an old cache.
func sheetCachePath(stored, sum string) string {
	return fmt.Sprintf("%s%s%s.v%d", stored, documents.SheetCacheInfix, sum[:16], sheetdb.CacheVersion)
}

// removeOtherSheetCaches deletes the stored file's older cache files.
func removeOtherSheetCaches(keep string) {
	i := strings.LastIndex(keep, documents.SheetCacheInfix)
	if i < 0 {
		return
	}
	old, _ := filepath.Glob(keep[:i] + documents.SheetCacheInfix + "*")
	for _, p := range old {
		if p != keep && !strings.Contains(p, ".tmp-") {
			os.Remove(p)
		}
	}
}

func convertLegacy(ctx context.Context, s sheetdb.Source) (sheetdb.Source, error) {
	if !isLegacySheet(s) {
		return s, nil
	}
	data, name, err := officeconv.ToModern(ctx, s.Name, s.Data)
	if err != nil {
		return s, fmt.Errorf("%s could not be converted to .xlsx for querying: %w", s.Label, err)
	}
	s.Data, s.Name, s.Format = data, name, "xlsx"
	return s, nil
}

// sheetSource reads one spreadsheet: a document's stored original (or, for a
// PDF/Word/text document, the tables in its text) or a sandbox file.
func (a *Agent) sheetSource(ctx context.Context, rc *runContext, ref string, cacheable bool) (sheetSrc, string) {
	ref = strings.TrimSpace(ref)
	var src sheetSrc
	mime := ""
	if isFilePath(ref) {
		data, err := a.readFile(ctx, rc, ref)
		if err != nil {
			return src, fmt.Sprintf("error: cannot read %s: %v", ref, err)
		}
		rel := ref
		if i := strings.Index(rel, "://"); i >= 0 {
			rel = rel[i+3:]
		}
		base := path.Base(rel)
		src.Source = sheetdb.Source{Label: ref, Name: rel, Short: strings.TrimSuffix(base, path.Ext(base)), Data: data}
		if src.Format = sheetdb.Detect(src.Name, "", data); src.Format == "" && utf8.Valid(data) && strings.Contains(string(data), "|") {
			src.Format, src.Text = "md", string(data) // a Markdown file with tables
		}
		cacheable = false
	} else {
		doc, errMsg := a.loadDocRef(ctx, rc, ref)
		if errMsg != "" {
			return src, errMsg
		}
		data, name, m, ok := a.storedOriginal(ctx, rc, doc)
		if path.Ext(name) == "" {
			name += path.Ext(doc.Title)
		}
		short := strings.TrimSuffix(doc.Title, path.Ext(doc.Title))
		src.Source, mime = sheetdb.Source{Label: fmt.Sprintf("%s %q", docRef(doc), doc.Title), Name: name, Short: short, Data: data}, m
		if ok {
			src.Format = sheetdb.Detect(name, mime, data)
		}
		if src.Format == "" {
			// Not a spreadsheet: its tables, as the converter rendered them.
			text, errMsg := a.docText(ctx, rc, doc)
			if errMsg != "" {
				return src, errMsg
			}
			src.Format, src.Text, src.Data = "md", text, nil
		}
		if dir := a.docFilesDir(rc, doc.Scope); ok && dir != "" && cacheable {
			src.cache = filepath.Join(dir, filepath.Base(name)) // completed with the hash below
		}
	}
	switch src.Format {
	case "":
		return src, fmt.Sprintf("error: %s is not a spreadsheet (Excel .xlsx/.xls/.xlsb/.ods, CSV or TSV) and has no tables to query. For text use read_doc, search_docs or check_text.", src.Label)
	case "md":
		if len(src.Text) == 0 || !strings.Contains(src.Text, "|") {
			return src, fmt.Sprintf("error: %s has no tables to query (its text has no table). For text use read_doc, search_docs or check_text.", src.Label)
		}
	case "xls", "xlsb", "ods":
		// LibreOffice picks the input filter by extension: make it match the content.
		src.Name = strings.TrimSuffix(src.Name, path.Ext(src.Name)) + "." + src.Format
	}
	var sum [32]byte
	if src.Format == "md" {
		sum = sha256.Sum256([]byte(src.Text))
	} else {
		sum = sha256.Sum256(src.Data) // of the original: a legacy file is converted only on a cache miss
	}
	src.sum = hex.EncodeToString(sum[:])
	if src.cache != "" {
		src.cache = sheetCachePath(src.cache, src.sum)
	}
	return src, ""
}

// PrebuildSheetCache builds the sheet cache of a just-uploaded spreadsheet in
// the background, so even the first question about it is fast. Errors are
// only logged: query_sheet builds the cache itself when it is missing.
func (a *Agent) PrebuildSheetCache(storedPath, mime string) {
	data, err := os.ReadFile(storedPath)
	if err != nil {
		return
	}
	format := sheetdb.Detect(storedPath, mime, data)
	if format == "" {
		return
	}
	sum := sha256.Sum256(data)
	src := sheetSrc{Source: sheetdb.Source{Label: filepath.Base(storedPath), Name: storedPath, Format: format, Data: data}, sum: hex.EncodeToString(sum[:])}
	if isLegacySheet(src.Source) {
		src.Name = strings.TrimSuffix(src.Name, path.Ext(src.Name)) + "." + format
	}
	src.cache = sheetCachePath(storedPath, src.sum)
	ctx, cancel := context.WithTimeout(context.Background(), 4*sheetQueryTimeout)
	defer cancel()
	start := time.Now()
	if _, err := ensureSheetCache(ctx, src, false); err != nil {
		log.Printf("sheets: prebuilding the cache of %s: %v", filepath.Base(storedPath), err)
		return
	}
	log.Printf("sheets: cached %s for query_sheet in %v", filepath.Base(storedPath), time.Since(start).Round(time.Millisecond))
}

// SweepSheetCaches deletes sheet cache files older than a week (and stray
// temporary ones), for every scope's files.
func (a *Agent) SweepSheetCaches() {
	for _, pattern := range []string{"users/*/files/*", "shared_files/*", "projects/*/files/*"} {
		files, _ := filepath.Glob(filepath.Join(a.DataDir, pattern+documents.SheetCacheInfix+"*"))
		for _, f := range files {
			if fi, err := os.Stat(f); err == nil && time.Since(fi.ModTime()) > sheetCacheMaxAge {
				os.Remove(f)
			}
		}
	}
}

// isLegacySheet: a spreadsheet LibreOffice must turn into .xlsx first. The
// loader converts it, so a cache hit skips LibreOffice.
func isLegacySheet(s sheetdb.Source) bool {
	return s.Format == "xls" || s.Format == "xlsb" || s.Format == "ods"
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
	// Example columns by type only: a text (or date) column to group by, and
	// a fractional amount before a whole-number one (whole numbers are often
	// counts or ordinals rather than amounts).
	var group, sum, whole string
	for _, c := range t.Columns {
		switch c.Type() {
		case "text", "code", "date":
			if group == "" {
				group = c.Name
			}
		case "number":
			if sum == "" {
				sum = c.Name
			}
		case "text-number":
			if sum == "" {
				sum = "num(" + c.Name + ")"
			}
		case "integer":
			if whole == "" {
				whole = c.Name
			}
		}
	}
	if sum == "" {
		sum = whole
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
		return "0 rows. No rows can itself be the answer (e.g. the values are not in the data); if so, say so instead of re-running the query. If you expected matches: text comparison with = is exact (case, spaces, accents); try LIKE '%part%', check the spelling against the example values, or trim(column)."
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
	if !isFilePath(saveTo) {
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
					panic(vm.NewGoError(fmt.Errorf("%w. The tables and their columns are: %s", err, strings.Join(strings.Fields(columnList(db)), " "))))
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
					out = append(out, map[string]any{"table": t.Name, "name": t.Name, "sheet": t.Sheet, "rows": t.Rows, "header_row": t.HeaderRow, "columns": cols})
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
