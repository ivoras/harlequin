// Package sheetdb loads spreadsheets (xlsx, csv) into a private in-memory
// SQLite database, one table per sheet, so a model can answer questions about
// millions of cells with SQL instead of reading them. Queries run read-only
// under an authorizer: no ATTACH, no writes, no pragmas, no extensions.
package sheetdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	sqlite3 "github.com/mattn/go-sqlite3"
	"golang.org/x/text/unicode/norm"
)

const (
	// MaxCells bounds one load (all sources together): ~1 GB of SQLite at worst.
	MaxCells = 20_000_000
	// headerScan is how many leading rows header detection looks at.
	headerScan = 30
	// aboveKeep is how many rows above a header are quoted in the schema.
	aboveKeep = 3
)

const driverName = "sheetdb_sqlite3"

var registerOnce sync.Once

func register() {
	registerOnce.Do(func() {
		sql.Register(driverName, &sqlite3.SQLiteDriver{ConnectHook: func(c *sqlite3.SQLiteConn) error {
			c.SetLimit(sqlite3.SQLITE_LIMIT_ATTACHED, 0)
			c.SetLimit(sqlite3.SQLITE_LIMIT_LENGTH, 16<<20) // no gigabyte zeroblob()/printf()
			return c.RegisterFunc("num", sqlNum, true)
		}})
	})
}

// sqlNum is the num() SQL function: numbers pass through, text is parsed as a
// written number (parseNum), anything else is NULL.
func sqlNum(v any) any {
	switch x := v.(type) {
	case int64, float64:
		return x
	case string:
		if f, ok := parseNum(x); ok {
			return number(f)
		}
	case []byte:
		if f, ok := parseNum(string(x)); ok {
			return number(f)
		}
	}
	return nil
}

// Source is one spreadsheet to load: Label names it in the schema (e.g.
// `u.12 "Sales.xlsx"`), Name's extension picks the format (.csv/.tsv/.txt are
// CSV, anything else must be an xlsx package).
type Source struct {
	Label, Name string
	Data        []byte
}

// Options narrow a load.
type Options struct {
	Sheet string // load only this sheet (name, case-insensitive); "" = all
	// HeaderRow forces the header row for every loaded sheet: n>0 is that
	// spreadsheet row, -1 means no header (columns are named by letter), 0
	// means detect.
	HeaderRow int
}

// Column describes one table column.
type Column struct {
	Name, Letter, Header string
	nonEmpty, nums, ints int
	dates, numText       int
	codes                int // pure-digit text: account numbers, ids
	samples              []string
}

// Table is one loaded sheet.
type Table struct {
	Name, Sheet, Source string
	Hidden              bool
	Rows                int
	HeaderRow           int      // 0 = none
	Above               []string // text of rows above the header (first few)
	Columns             []*Column
}

// DB is a loaded set of spreadsheets. Safe for concurrent use: queries are
// serialised on its one connection.
type DB struct {
	mu     sync.Mutex
	db     *sql.DB
	conn   *sql.Conn
	Tables []*Table
	Cells  int
}

// Close frees the database.
func (d *DB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn != nil {
		d.conn.Close()
		d.conn = nil
	}
	return d.db.Close()
}

// Load reads the sources into a new in-memory database.
func Load(ctx context.Context, srcs []Source, opt Options) (*DB, error) {
	register()
	db, err := sql.Open(driverName, ":memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	d := &DB{db: db, conn: conn}
	if err := d.load(ctx, srcs, opt); err != nil {
		d.Close()
		return nil, err
	}
	// From here on the connection only reads.
	err = conn.Raw(func(dc any) error {
		dc.(*sqlite3.SQLiteConn).RegisterAuthorizer(authorize)
		return nil
	})
	if err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// Action codes from sqlite3.h (go-sqlite3 does not export RECURSIVE).
const (
	actRead      = 20
	actSelect    = 21
	actFunction  = 31
	actRecursive = 33
)

func authorize(op int, arg1, arg2, _ string) int {
	switch op {
	case actSelect, actRead, actRecursive:
		return sqlite3.SQLITE_OK
	case actFunction:
		switch strings.ToLower(arg2) {
		case "load_extension", "fts3_tokenizer", "readfile", "writefile", "edit":
			return sqlite3.SQLITE_DENY
		}
		return sqlite3.SQLITE_OK
	}
	return sqlite3.SQLITE_DENY
}

func (d *DB) load(ctx context.Context, srcs []Source, opt Options) error {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	used := map[string]bool{}
	var cur *builder
	finish := func() error {
		if cur == nil {
			return nil
		}
		err := cur.finish()
		if err == nil && cur.t != nil {
			d.Tables = append(d.Tables, cur.t)
		}
		cur = nil
		return err
	}
	want := strings.ToLower(strings.TrimSpace(opt.Sheet))
	sheetsSeen := []string{}
	for _, src := range srcs {
		skip := false
		begin := func(name string, hidden bool) error {
			if err := finish(); err != nil {
				return err
			}
			sheetsSeen = append(sheetsSeen, name)
			skip = want != "" && strings.ToLower(name) != want
			if !skip {
				cur = &builder{ctx: ctx, tx: tx, used: used, opt: opt, sheet: name, source: src.Label, hidden: hidden, cells: &d.Cells}
			}
			return nil
		}
		row := func(n int, cells []any) error {
			if skip || cur == nil {
				return nil
			}
			return cur.add(n, cells)
		}
		low := strings.ToLower(src.Name)
		if strings.HasSuffix(low, ".csv") || strings.HasSuffix(low, ".tsv") || strings.HasSuffix(low, ".txt") {
			base := path.Base(src.Name)
			name := strings.TrimSuffix(base, path.Ext(base))
			if err := begin(name, false); err != nil {
				return err
			}
			err = readCSV(src.Data, row)
		} else {
			err = readXLSX(src.Data, begin, row)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", src.Label, err)
		}
		if err := finish(); err != nil {
			return err
		}
	}
	if want != "" && len(d.Tables) == 0 {
		return fmt.Errorf("no sheet named %q; the sheets are: %s", opt.Sheet, strings.Join(sheetsSeen, ", "))
	}
	return tx.Commit()
}

var (
	errTooBig = errors.New("too large")
	digitsRE  = regexp.MustCompile(`^\d+$`)
)

// builder turns one sheet's rows into a table: it buffers the first rows to
// find the header, then streams the rest straight into SQLite.
type builder struct {
	ctx           context.Context
	tx            *sql.Tx
	used          map[string]bool
	opt           Options
	sheet, source string
	hidden        bool
	cells         *int

	buf   [][]any
	nums  []int
	t     *Table
	stage string // the untyped table rows load into; finish() types it
	ins   *sql.Stmt
}

func (b *builder) add(n int, cells []any) error {
	if *b.cells += len(cells); *b.cells > MaxCells {
		return fmt.Errorf("%w: more than %d cells; load one sheet at a time (sheet argument)", errTooBig, MaxCells)
	}
	if b.t == nil {
		b.buf = append(b.buf, append([]any(nil), cells...))
		b.nums = append(b.nums, n)
		if len(b.buf) < headerScan {
			return nil
		}
		return b.start()
	}
	return b.insert(n, cells)
}

// finish copies the loaded rows into the final table, each column declared
// with the type its values showed. The declared type does real work: in a
// code column numbers become text ("1270000"), so = and joins see one type,
// and comparing an integer column with a code column compares as numbers
// ('01270000' = 1270000), which is what a reader of both sheets expects.
func (b *builder) finish() error {
	if b.t == nil {
		if err := b.start(); err != nil {
			return err
		}
	}
	if b.ins != nil {
		b.ins.Close()
		b.ins = nil
	}
	cols := []string{`"_row" INTEGER`}
	for _, c := range b.t.Columns {
		cols = append(cols, strings.TrimSpace(quote(c.Name)+" "+affinity[c.Type()]))
	}
	for _, q := range []string{
		fmt.Sprintf("CREATE TABLE %s (%s)", quote(b.t.Name), strings.Join(cols, ", ")),
		fmt.Sprintf("INSERT INTO %s SELECT * FROM %s", quote(b.t.Name), quote(b.stage)),
		"DROP TABLE " + quote(b.stage),
	} {
		if _, err := b.tx.ExecContext(b.ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// affinity is the SQLite column type declared for each column kind.
var affinity = map[string]string{
	"integer": "INTEGER", "number": "NUMERIC", "date": "TEXT", "code": "TEXT", "text": "TEXT", "text-number": "TEXT",
}

// start picks the header, creates the table and inserts the buffered rows.
func (b *builder) start() error {
	h := -1 // index into buf
	switch {
	case b.opt.HeaderRow > 0:
		for i, n := range b.nums {
			if n == b.opt.HeaderRow {
				h = i
			}
		}
	case b.opt.HeaderRow == 0:
		h = detectHeader(b.buf)
	}
	if b.opt.HeaderRow > 0 && h < 0 && len(b.buf) > 0 {
		return fmt.Errorf("header_row %d is not among the first %d rows with values of sheet %q (they are rows %d–%d)", b.opt.HeaderRow, headerScan, b.sheet, b.nums[0], b.nums[len(b.nums)-1])
	}
	t := &Table{Sheet: b.sheet, Source: b.source, Hidden: b.hidden}
	t.Name = uniqueName(ident(b.sheet, "sheet"), b.used)
	b.stage = "_load_" + t.Name // idents never start with "_"
	var hdr []any
	if h >= 0 {
		hdr, t.HeaderRow = b.buf[h], b.nums[h]
		for _, r := range b.buf[:h] {
			if len(t.Above) < aboveKeep {
				t.Above = append(t.Above, rowText(r))
			}
		}
	}
	width := 0
	for _, r := range b.buf {
		width = max(width, len(r))
	}
	b.t = t
	if err := b.widen(width, hdr); err != nil {
		return err
	}
	for i, r := range b.buf {
		if i <= h {
			continue // the header and the rows above it are not data
		}
		if err := b.insert(b.nums[i], r); err != nil {
			return err
		}
	}
	b.buf, b.nums = nil, nil
	return nil
}

// widen adds columns up to width (the first call creates the table).
func (b *builder) widen(width int, hdr []any) error {
	t := b.t
	first := t.Columns == nil
	colUsed := map[string]bool{"_row": true}
	for _, c := range t.Columns {
		colUsed[c.Name] = true
	}
	var adds []string
	for i := len(t.Columns); i < width; i++ {
		c := &Column{Letter: colName(i)}
		if i < len(hdr) && hdr[i] != nil {
			c.Header = strings.TrimSpace(fmt.Sprint(hdr[i]))
		}
		base := ident(c.Header, "")
		if base == "" {
			base = strings.ToLower(c.Letter)
		}
		c.Name = uniqueName(base, colUsed)
		t.Columns = append(t.Columns, c)
		adds = append(adds, c.Name)
	}
	if first {
		cols := []string{`"_row" INTEGER`}
		for _, n := range adds {
			cols = append(cols, quote(n))
		}
		if _, err := b.tx.ExecContext(b.ctx, fmt.Sprintf("CREATE TABLE %s (%s)", quote(b.stage), strings.Join(cols, ", "))); err != nil {
			return err
		}
	} else {
		for _, n := range adds {
			if _, err := b.tx.ExecContext(b.ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", quote(b.stage), quote(n))); err != nil {
				return err
			}
		}
	}
	if b.ins != nil {
		b.ins.Close()
	}
	ph := strings.Repeat(", ?", len(t.Columns))
	var err error
	b.ins, err = b.tx.PrepareContext(b.ctx, fmt.Sprintf("INSERT INTO %s VALUES (?%s)", quote(b.stage), ph))
	return err
}

func (b *builder) insert(n int, cells []any) error {
	if len(cells) > len(b.t.Columns) {
		if err := b.widen(len(cells), nil); err != nil {
			return err
		}
	}
	args := make([]any, len(b.t.Columns)+1)
	args[0] = n
	for i, v := range cells {
		if v == nil {
			continue
		}
		c := b.t.Columns[i]
		c.nonEmpty++
		switch x := v.(type) {
		case int64:
			c.nums++
			c.ints++
		case float64:
			c.nums++
		case isoDate:
			c.dates++
			v = string(x)
		case string:
			// Pure digits are codes (accounts, ids), not amounts.
			if digitsRE.MatchString(x) {
				c.codes++
			} else if _, ok := parseNum(x); ok {
				c.numText++
			}
		}
		if len(c.samples) < 3 {
			s := truncate(fmt.Sprint(v), 40)
			if !contains(c.samples, s) {
				c.samples = append(c.samples, s)
			}
		}
		args[i+1] = v
	}
	b.t.Rows++
	_, err := b.ins.ExecContext(b.ctx, args...)
	return err
}

// detectHeader finds the header among the first rows: the first row that is
// about as wide as the widest row and mostly text, so title rows above a
// table and all-number sheets (no header) come out right. -1 = no header.
func detectHeader(rows [][]any) int {
	widest := 0
	for _, r := range rows {
		widest = max(widest, filled(r))
	}
	need := max(1, int(math.Ceil(float64(widest)*0.6)))
	for i, r := range rows {
		n, text := 0, 0
		for _, v := range r {
			if v == nil {
				continue
			}
			n++
			if _, ok := v.(string); ok {
				text++
			}
		}
		if n >= need && text*10 >= n*7 {
			if i == len(rows)-1 && len(rows) > 1 {
				return -1 // a "header" with no data under it is more likely data
			}
			return i
		}
	}
	return -1
}

func filled(r []any) int {
	n := 0
	for _, v := range r {
		if v != nil {
			n++
		}
	}
	return n
}

func rowText(r []any) string {
	var parts []string
	for _, v := range r {
		if v != nil {
			parts = append(parts, fmt.Sprint(v))
		}
	}
	return truncate(strings.Join(parts, " | "), 120)
}

// ident turns a header or sheet name into a plain SQL identifier: ASCII
// lower case, letters/digits/underscores ("Iznos (EUR)" -> iznos_eur,
// "Šifra" -> sifra), so a model never needs to quote it.
func ident(s, prefix string) string {
	s = norm.NFD.String(strings.ToLower(strings.TrimSpace(s)))
	var sb strings.Builder
	under := false
	for _, r := range s {
		switch {
		case r == 'đ':
			sb.WriteString("dj")
			under = false
		case r == 'ß':
			sb.WriteString("ss")
			under = false
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			sb.WriteRune(r)
			under = false
		case unicode.Is(unicode.Mn, r): // combining accent left by NFD: drop it
		default:
			if !under && sb.Len() > 0 {
				sb.WriteByte('_')
				under = true
			}
		}
	}
	out := strings.TrimRight(sb.String(), "_")
	if len(out) > 40 {
		out = strings.TrimRight(out[:40], "_")
	}
	if out == "" {
		return prefix
	}
	if out[0] >= '0' && out[0] <= '9' {
		if prefix == "" {
			prefix = "c"
		}
		out = prefix + "_" + out
	}
	if sqlKeywords[out] {
		out += "_"
	}
	return out
}

func uniqueName(base string, used map[string]bool) string {
	name := base
	for i := 2; used[name]; i++ {
		name = base + "_" + strconv.Itoa(i)
	}
	used[name] = true
	return name
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Type is the column's kind as the schema shows it.
func (c *Column) Type() string {
	switch {
	case c.nonEmpty == 0:
		return "empty"
	case c.dates == c.nonEmpty:
		return "date"
	case c.nums == c.nonEmpty && c.ints == c.nums:
		return "integer"
	case c.nums == c.nonEmpty:
		return "number"
	case c.codes > 0 && c.dates == 0 && c.nums == c.ints && c.codes+c.ints == c.nonEmpty:
		return "code"
	case c.nums == 0 && c.dates == 0 && c.numText*10 >= c.nonEmpty*8:
		return "text-number"
	case c.nums == 0 && c.dates == 0:
		return "text"
	}
	return "mixed"
}

// Result is a query's outcome: Rows holds at most the rows asked to keep,
// Total counts them all.
type Result struct {
	Columns []string
	Rows    [][]any
	Total   int
}

// Query runs one read-only SQL statement, keeping at most keep rows.
func (d *DB) Query(ctx context.Context, query string, keep int) (*Result, error) {
	query = strings.TrimSpace(query)
	query = strings.TrimSpace(strings.TrimSuffix(query, ";"))
	if query == "" {
		return nil, errors.New("sql is empty")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn == nil {
		return nil, errors.New("database closed")
	}
	rows, err := d.conn.QueryContext(ctx, query)
	if err != nil {
		return nil, explain(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	res := &Result{Columns: cols}
	for rows.Next() {
		res.Total++
		if len(res.Rows) >= keep {
			continue
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}
		res.Rows = append(res.Rows, vals)
	}
	if err := rows.Err(); err != nil {
		return nil, explain(err)
	}
	return res, nil
}

func explain(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "not authorized"):
		return errors.New("only SELECT (or WITH … SELECT) queries are allowed: the data is read-only")
	case errors.Is(err, context.DeadlineExceeded), strings.Contains(msg, "interrupted"):
		return errors.New("the query took too long and was stopped; filter earlier, avoid joining big tables without a condition, or aggregate")
	}
	return err
}

// Schema describes the tables for a model: names, columns with types, the
// header each came from and example values.
func (d *DB) Schema() string {
	var sb strings.Builder
	for _, t := range d.Tables {
		fmt.Fprintf(&sb, "TABLE %s — sheet %q of %s, %d data rows", t.Name, t.Sheet, t.Source, t.Rows)
		if t.Hidden {
			sb.WriteString(", hidden sheet")
		}
		switch {
		case t.HeaderRow > 0:
			fmt.Fprintf(&sb, "; header is spreadsheet row %d", t.HeaderRow)
		default:
			sb.WriteString("; no header row found, columns are named by letter")
		}
		sb.WriteString("\n")
		for _, a := range t.Above {
			fmt.Fprintf(&sb, "  (row above the header: %s)\n", a)
		}
		w := 4
		for _, c := range t.Columns {
			w = max(w, len(c.Name))
		}
		fmt.Fprintf(&sb, "  %-*s  %-11s spreadsheet row number\n", w, "_row", "integer")
		for _, c := range t.Columns {
			from := "column " + c.Letter
			if c.Header != "" {
				from = fmt.Sprintf("%q, column %s", truncate(c.Header, 60), c.Letter)
			}
			ex := ""
			if len(c.samples) > 0 {
				q := make([]string, len(c.samples))
				for i, s := range c.samples {
					q[i] = strconv.Quote(s)
				}
				ex = "  e.g. " + strings.Join(q, ", ")
			}
			fmt.Fprintf(&sb, "  %-*s  %-11s %s%s\n", w, c.Name, c.Type(), from, ex)
			if c.Type() == "code" && c.ints > 0 {
				fmt.Fprintf(&sb, "  %-*s  (%d of these codes were numbers in the sheet, so a leading zero may be missing: to match codes, compare ltrim(%s, '0'))\n", w, "", c.ints, c.Name)
			}
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

// SortedTableNames lists the table names, for error messages.
func (d *DB) SortedTableNames() []string {
	var out []string
	for _, t := range d.Tables {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}

// sqlKeywords are SQLite keywords that would need quoting as a bare name.
var sqlKeywords = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range strings.Fields(`abort action add after all alter always analyze and as asc attach autoincrement
before begin between by cascade case cast check collate column commit conflict constraint create cross current
current_date current_time current_timestamp database default deferrable deferred delete desc detach distinct do drop
each else end escape except exclude exclusive exists explain fail filter first following for foreign from full generated
glob group groups having if ignore immediate in index indexed initially inner insert instead intersect into is isnull
join key last left like limit match materialized natural no not nothing notnull null nulls of offset on or order others
outer over partition plan pragma preceding primary query raise range recursive references regexp reindex release rename
replace restrict returning right rollback row rows savepoint select set table temp temporary then ties to transaction
trigger unbounded union unique update using vacuum values view virtual when where window with without`) {
		m[k] = true
	}
	return m
}()
