// Package sheetdb loads spreadsheets (xlsx, csv) into a private in-memory
// SQLite database, one table per sheet, so a model can answer questions about
// millions of cells with SQL instead of reading them. Queries run read-only
// under an authorizer: no ATTACH, no writes, no pragmas, no extensions.
package sheetdb

import (
	"archive/zip"
	"bytes"
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
			if err := c.RegisterFunc("key", sqlKey, true); err != nil {
				return err
			}
			return c.RegisterFunc("num", sqlNum, true)
		}})
	})
}

// sqlNum is the num() SQL function: numbers pass through, text is parsed as a
// written number, anything else is NULL. An optional second argument names
// the decimal separator (num(x, ',') reads "1.234" as 1234).
func sqlNum(v any, dec ...string) any {
	var d byte
	if len(dec) > 0 && len(dec[0]) == 1 {
		d = dec[0][0]
	}
	switch x := v.(type) {
	case int64, float64:
		return x
	case string:
		if f, ok := parseNumDec(x, d); ok {
			return number(f)
		}
	case []byte:
		if f, ok := parseNumDec(string(x), d); ok {
			return number(f)
		}
	}
	return nil
}

// sqlKey is the key() SQL function: a value normalised for matching the same
// identifier written differently by two systems — upper case, no spaces, any
// of - / . _ \ as one "-", leading zeros dropped from each group of digits.
// '01270000' and 1270000 give "1270000"; '1/11/01' and '1-11-1' give "1-11-1".
func sqlKey(v any) any {
	var s string
	switch x := v.(type) {
	case nil:
		return nil
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		s = Cell(x)
	case []byte:
		s = string(x)
	case string:
		s = x
	default:
		s = fmt.Sprint(x)
	}
	var out strings.Builder
	sep, zeros := false, true // zeros: still at the start of a digit group
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		switch {
		case r == ' ' || r == '\u00a0':
		case strings.ContainsRune("-/._\\", r):
			sep = out.Len() > 0
			zeros = true
		default:
			if sep {
				out.WriteByte('-')
				sep = false
			}
			isDigit := r >= '0' && r <= '9'
			if !isDigit {
				zeros = true
			} else if r == '0' && zeros {
				continue
			} else {
				zeros = false
			}
			out.WriteRune(r)
		}
	}
	// A group that was all zeros ("0", "00") is kept as one zero.
	if out.Len() == 0 && strings.ContainsRune(s, '0') {
		return "0"
	}
	return out.String()
}

// Source is one spreadsheet to load. Label names it in the schema (e.g.
// `u.12 "Sales.xlsx"`), Short is the file's own name (for table names when
// several files load together, and the _file column of combined views).
// Format is "xlsx", "csv", "tsv", or "md" (the pipe tables of a document's
// Markdown Text, e.g. a PDF's); "" = Detect.
type Source struct {
	Label, Name, Short, Format string
	Data                       []byte
	Text                       string
}

var errNoTables = errors.New("the document contains no tables")

// Detect tells a spreadsheet's format from its content first and its name or
// MIME type second: "xlsx"; "xls", "xlsb", "ods" (which need converting to
// xlsx); "csv", "tsv"; or "" when it is not a spreadsheet.
func Detect(name, mime string, data []byte) string {
	ext := strings.ToLower(path.Ext(name))
	if bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return ""
		}
		for _, f := range zr.File {
			switch f.Name {
			case "xl/workbook.xml":
				return "xlsx"
			case "xl/workbook.bin":
				return "xlsb"
			case "mimetype":
				if b, err := readAll(f); err == nil && strings.TrimSpace(string(b)) == "application/vnd.oasis.opendocument.spreadsheet" {
					return "ods"
				}
			}
		}
		return ""
	}
	// OLE2 compound file: a legacy .xls (a .doc shares the signature).
	if bytes.HasPrefix(data, []byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1")) {
		if ext == ".xls" || ext == "" || mime == "application/vnd.ms-excel" {
			return "xls"
		}
		return ""
	}
	switch {
	case ext == ".tsv" || ext == ".tab" || mime == "text/tab-separated-values":
		return "tsv"
	case ext == ".csv" || mime == "text/csv":
		return "csv"
	}
	return ""
}

// Options narrow a load.
type Options struct {
	Sheet string // load only this sheet (name, case-insensitive); "" = all
	// HeaderRow forces the header row for every loaded sheet: n>0 is that
	// spreadsheet row, -1 means no header (columns are named by letter), 0
	// means detect.
	HeaderRow int
}

// Column describes one table column. The counts are kept in a cache file's
// metadata, so they are exported.
type Column struct {
	Name, Letter, Header string
	NonEmpty, Nums, Ints int
	Dates, NumText       int
	Codes                int // pure-digit text: account numbers, ids
	DecComma, DecDot     int // amounts written as text that show which decimal separator they use
	Samples              []string
	// FillOf is the name of the column whose cell fill colours this column
	// holds ("" for a data column).
	FillOf string `json:",omitempty"`
}

// Table is one loaded sheet.
type Table struct {
	Name, Sheet, Source string
	File                string // Source.Short
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
	Views  []*View
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
	d, err := newMem(ctx)
	if err != nil {
		return nil, err
	}
	if err := d.load(ctx, srcs, opt); err != nil {
		d.Close()
		return nil, err
	}
	if err := d.seal(); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func newMem(ctx context.Context) (*DB, error) {
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
	return &DB{db: db, conn: conn}, nil
}

// seal makes the connection read-only for good: from here on only the
// model's queries run on it.
func (d *DB) seal() error {
	return d.conn.Raw(func(dc any) error {
		c := dc.(*sqlite3.SQLiteConn)
		c.SetLimit(sqlite3.SQLITE_LIMIT_ATTACHED, 0)
		c.RegisterAuthorizer(authorize)
		return nil
	})
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
		if src.Format == "" {
			src.Format = Detect(src.Name, "", src.Data)
		}
		skip := false
		short := src.Short
		if short == "" {
			base := path.Base(src.Name)
			short = strings.TrimSuffix(base, path.Ext(base))
		}
		total := sheetCount(src)
		begin := func(name string, hidden bool) error {
			if err := finish(); err != nil {
				return err
			}
			sheetsSeen = append(sheetsSeen, name)
			skip = want != "" && strings.ToLower(name) != want
			if !skip {
				cur = &builder{ctx: ctx, tx: tx, used: used, opt: opt, sheet: name, source: src.Label, file: short, hidden: hidden, cells: &d.Cells,
					tname: tableName(short, name, total)}
			}
			return nil
		}
		row := func(n int, cells []any, fills []string) error {
			if skip || cur == nil {
				return nil
			}
			return cur.add(n, cells, fills)
		}
		format := src.Format
		switch format {
		case "md":
			err = readMDTables(src.Text, func(name string, hidden bool) error {
				if err := begin(name, hidden); err != nil {
					return err
				}
				if cur != nil {
					cur.csv, cur.delim, cur.headerFirst = true, ',', true
				}
				return nil
			}, row)
		case "csv", "tsv":
			if err := begin(short, false); err != nil {
				return err
			}
			if cur != nil {
				cur.csv = true
			}
			var delim rune
			delim, err = readCSV(src.Data, format == "tsv", row)
			if cur != nil {
				cur.delim = delim
			}
		case "xlsx":
			err = readXLSX(src.Data, begin, row)
		case "":
			err = errors.New("not a spreadsheet (Excel or CSV)")
		default:
			err = fmt.Errorf("a .%s file must be converted to .xlsx first", format)
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
	views, err := createViews(ctx, tx, d.Tables, used)
	if err != nil {
		return err
	}
	d.Views = views
	return tx.Commit()
}

// tableName names a table after its file (plus the sheet when the file has
// more than one), whether the file is loaded alone or with others: a name the
// model saw in one call must work in the next (query_sheet, run_js).
func tableName(file, sheet string, sheets int) string {
	if sheets == 1 {
		return ident(file, "t")
	}
	return ident(file+" "+sheet, "t")
}

// sheetCount is how many sheets (tables) a source has, for tableName.
func sheetCount(src Source) int {
	switch src.Format {
	case "md":
		return len(parseMDTables(src.Text))
	case "csv", "tsv":
		return 1
	}
	return xlsxSheetCount(src.Data)
}

var (
	errTooBig = errors.New("too large")
	digitsRE  = regexp.MustCompile(`^\d+$`)
)

// builder turns one sheet's rows into a table: it buffers the first rows to
// find the header, then streams the rest straight into SQLite.
type builder struct {
	ctx                        context.Context
	tx                         *sql.Tx
	used                       map[string]bool
	opt                        Options
	sheet, source, file, tname string
	hidden                     bool
	cells                      *int
	headerFirst                bool // the first row is the header (a document's table)

	buf      [][]any
	bufFills [][]string
	rowNums  []int
	dataIdx  []int       // Columns index of data column i
	fillIdx  map[int]int // data column i -> Columns index of its _color column
	t        *Table
	stage    string // the untyped table rows load into; finish() types it
	ins      *sql.Stmt
	csv      bool // values came from a CSV/TSV: finish() turns written amounts into numbers
	delim    rune
}

func (b *builder) add(n int, cells []any, fills []string) error {
	if *b.cells += len(cells); *b.cells > MaxCells {
		return fmt.Errorf("%w: more than %d cells; load one sheet at a time (sheet argument)", errTooBig, MaxCells)
	}
	if b.t == nil {
		b.buf = append(b.buf, append([]any(nil), cells...))
		b.bufFills = append(b.bufFills, append([]string(nil), fills...))
		b.rowNums = append(b.rowNums, n)
		if len(b.buf) < headerScan {
			return nil
		}
		return b.start()
	}
	return b.insert(n, cells, fills)
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
	if b.csv {
		if err := b.csvAmounts(); err != nil {
			return err
		}
	}
	for _, q := range []string{
		createTyped(b.t.Name, b.t.Columns),
		fmt.Sprintf("INSERT INTO %s SELECT * FROM %s", quote(b.t.Name), quote(b.stage)),
		"DROP TABLE " + quote(b.stage),
	} {
		if _, err := b.tx.ExecContext(b.ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// csvAmounts converts CSV columns of written amounts ("1.234,56") to numbers,
// as a spreadsheet program opening the file would: in a CSV every value is
// text, so an amount column must not stay text just because it has
// separators. The column's decimal separator is decided from all its values
// (and, when none tells, from the delimiter: ';' files use a decimal comma).
// A column with anything that is not an amount is left as it is.
func (b *builder) csvAmounts() error {
	for _, c := range b.t.Columns {
		if c.NumText == 0 || c.NonEmpty != c.Nums+c.NumText || c.Dates > 0 || c.Codes > 0 {
			continue
		}
		dec := "."
		if c.DecComma > c.DecDot || (c.DecComma == c.DecDot && b.delim == ';') {
			dec = ","
		}
		col, tab := quote(c.Name), quote(b.stage)
		if _, err := b.tx.ExecContext(b.ctx, fmt.Sprintf("UPDATE %s SET %s = num(%s, ?) WHERE typeof(%s) = 'text'", tab, col, col, col), dec); err != nil {
			return err
		}
		var ints int
		if err := b.tx.QueryRowContext(b.ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE typeof(%s) = 'integer'", tab, col)).Scan(&ints); err != nil {
			return err
		}
		c.Nums, c.Ints, c.NumText = c.NonEmpty, ints, 0
		c.Samples = nil
		rows, err := b.tx.QueryContext(b.ctx, fmt.Sprintf("SELECT DISTINCT %s FROM %s WHERE %s IS NOT NULL LIMIT 3", col, tab, col))
		if err != nil {
			return err
		}
		for rows.Next() {
			var v any
			if rows.Scan(&v) == nil {
				c.Samples = append(c.Samples, truncate(Cell(v), 40))
			}
		}
		rows.Close()
	}
	return nil
}

// createTyped is the CREATE TABLE of a final table: _row, then each column
// with its declared type.
func createTyped(name string, columns []*Column) string {
	cols := []string{`"_row" INTEGER`}
	for _, c := range columns {
		cols = append(cols, strings.TrimSpace(quote(c.Name)+" "+affinity[c.Type()]))
	}
	return fmt.Sprintf("CREATE TABLE %s (%s)", quote(name), strings.Join(cols, ", "))
}

// affinity is the SQLite column type declared for each column kind.
var affinity = map[string]string{
	"color": "TEXT", "integer": "INTEGER", "number": "NUMERIC", "date": "TEXT", "code": "TEXT", "text": "TEXT", "text-number": "TEXT",
}

// start picks the header, creates the table and inserts the buffered rows.
func (b *builder) start() error {
	h := -1 // index into buf
	switch {
	case b.opt.HeaderRow > 0:
		for i, n := range b.rowNums {
			if n == b.opt.HeaderRow {
				h = i
			}
		}
	case b.headerFirst && len(b.buf) > 0:
		h = 0
	case b.opt.HeaderRow == 0:
		h = detectHeader(b.buf)
	}
	if b.opt.HeaderRow > 0 && h < 0 && len(b.buf) > 0 {
		return fmt.Errorf("header_row %d is not among the first %d rows with values of sheet %q (they are rows %d–%d)", b.opt.HeaderRow, headerScan, b.sheet, b.rowNums[0], b.rowNums[len(b.rowNums)-1])
	}
	t := &Table{Sheet: b.sheet, Source: b.source, File: b.file, Hidden: b.hidden}
	t.Name = uniqueName(b.tname, b.used)
	b.stage = "_load_" + t.Name // idents never start with "_"
	var hdr []any
	if h >= 0 {
		hdr, t.HeaderRow = b.buf[h], b.rowNums[h]
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
		if err := b.insert(b.rowNums[i], r, b.bufFills[i]); err != nil {
			return err
		}
	}
	b.buf, b.bufFills, b.rowNums = nil, nil, nil
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
	for i := len(b.dataIdx); i < width; i++ {
		c := &Column{Letter: colName(i)}
		if i < len(hdr) && hdr[i] != nil {
			c.Header = strings.TrimSpace(fmt.Sprint(hdr[i]))
		}
		base := ident(c.Header, "")
		if base == "" {
			base = strings.ToLower(c.Letter)
		}
		c.Name = uniqueName(base, colUsed)
		b.dataIdx = append(b.dataIdx, len(t.Columns))
		t.Columns = append(t.Columns, c)
		adds = append(adds, c.Name)
	}
	return b.addColumns(first, adds)
}

// addFill adds the _color column of data column i.
func (b *builder) addFill(i int) error {
	base := b.t.Columns[b.dataIdx[i]]
	used := map[string]bool{"_row": true}
	for _, c := range b.t.Columns {
		used[c.Name] = true
	}
	c := &Column{Letter: base.Letter, Header: base.Header, FillOf: base.Name, Name: uniqueName(base.Name+"_color", used)}
	if b.fillIdx == nil {
		b.fillIdx = map[int]int{}
	}
	b.fillIdx[i] = len(b.t.Columns)
	b.t.Columns = append(b.t.Columns, c)
	return b.addColumns(false, []string{c.Name})
}

func (b *builder) addColumns(first bool, adds []string) error {
	t := b.t
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

func (b *builder) insert(n int, cells []any, fills []string) error {
	if len(cells) > len(b.dataIdx) || len(fills) > len(b.dataIdx) {
		if err := b.widen(max(len(cells), len(fills)), nil); err != nil {
			return err
		}
	}
	for i, f := range fills {
		if _, ok := b.fillIdx[i]; f != "" && !ok {
			if err := b.addFill(i); err != nil {
				return err
			}
		}
	}
	args := make([]any, len(b.t.Columns)+1)
	args[0] = n
	for i, f := range fills {
		if f != "" {
			c := b.t.Columns[b.fillIdx[i]]
			c.NonEmpty++
			if len(c.Samples) < 3 && !contains(c.Samples, f) {
				c.Samples = append(c.Samples, f)
			}
			args[b.fillIdx[i]+1] = f
		}
	}
	for i, v := range cells {
		if v == nil {
			continue
		}
		c := b.t.Columns[b.dataIdx[i]]
		c.NonEmpty++
		switch x := v.(type) {
		case int64:
			c.Nums++
			c.Ints++
		case float64:
			c.Nums++
		case isoDate:
			c.Dates++
			v = string(x)
		case string:
			// Pure digits are codes (accounts, ids), not amounts.
			if digitsRE.MatchString(x) {
				c.Codes++
			} else if _, ok := parseNum(x); ok {
				c.NumText++
				switch sepVote(x) {
				case ',':
					c.DecComma++
				case '.':
					c.DecDot++
				}
			}
		}
		if len(c.Samples) < 3 {
			s := truncate(fmt.Sprint(v), 40)
			if !contains(c.Samples, s) {
				c.Samples = append(c.Samples, s)
			}
		}
		args[b.dataIdx[i]+1] = v
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
		// Cut the middle: file names differ at the end ("…_06_2026") as often
		// as at the start.
		out = strings.TrimRight(out[:20], "_") + "_" + strings.TrimLeft(out[len(out)-19:], "_")
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
	case c.FillOf != "":
		return "color"
	case c.NonEmpty == 0:
		return "empty"
	case c.Dates == c.NonEmpty:
		return "date"
	case c.Nums == c.NonEmpty && c.Ints == c.Nums:
		return "integer"
	case c.Nums == c.NonEmpty:
		return "number"
	case c.Codes > 0 && c.Dates == 0 && c.Nums == c.Ints && c.Codes+c.Ints == c.NonEmpty:
		return "code"
	case c.Nums == 0 && c.Dates == 0 && c.NumText*10 >= c.NonEmpty*8:
		return "text-number"
	case c.Nums == 0 && c.Dates == 0:
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
// header each came from and example values. Combined views come first; a
// table in a view after the view's first only says how it differs.
func (d *DB) Schema() string {
	var sb strings.Builder
	firstOf := map[string]*Table{} // table -> the first table of its view
	byName := map[string]*Table{}
	for _, t := range d.Tables {
		byName[t.Name] = t
	}
	for _, v := range d.Views {
		fmt.Fprintf(&sb, "VIEW %s — the %d tables %s stacked into one (they have the same columns). Use it for questions over all of them; _file and _sheet say which file and sheet each row came from.\n", v.Name, len(v.Tables), strings.Join(v.Tables, ", "))
		first := byName[v.Tables[0]]
		fmt.Fprintf(&sb, "  _file, _sheet, _row, %s\n\n", strings.Join(v.Columns, ", "))
		for _, n := range v.Tables[1:] {
			firstOf[n] = first
		}
	}
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
		if f := firstOf[t.Name]; f != nil {
			var diff []string
			for _, c := range t.Columns {
				if fc := columnByName(f, c.Name); fc.Type() != c.Type() {
					diff = append(diff, c.Name+" is "+c.Type()+" here")
				}
			}
			note := ""
			if len(diff) > 0 {
				note = "; " + strings.Join(diff, ", ")
			}
			fmt.Fprintf(&sb, "  (same columns as %s%s)\n\n", f.Name, note)
			continue
		}
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
			if c.FillOf != "" {
				from = "background colour of the cells of " + c.FillOf + " (" + from + ")"
			}
			ex := ""
			if len(c.Samples) > 0 {
				q := make([]string, len(c.Samples))
				for i, s := range c.Samples {
					q[i] = strconv.Quote(s)
				}
				ex = "  e.g. " + strings.Join(q, ", ")
			}
			fmt.Fprintf(&sb, "  %-*s  %-11s %s%s\n", w, c.Name, c.Type(), from, ex)
			if c.Type() == "code" && c.Ints > 0 {
				fmt.Fprintf(&sb, "  %-*s  (%d of these codes were numbers in the sheet, so a leading zero may be missing: to match codes, compare key(%s))\n", w, "", c.Ints, c.Name)
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
