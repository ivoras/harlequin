package sheetdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// CacheVersion is part of every cache file's name: bump it whenever loading
// changes what a file reads as (types, names), so old caches are not used.
const CacheVersion = 1

// metaTable holds a cache file's tables, views and column statistics as JSON.
const metaTable = "_sheetdb_meta"

type meta struct {
	Version int
	Tables  []*Table
	Views   []*View
	Cells   int
}

// ErrStaleCache: the cache file was written by another loader version.
var ErrStaleCache = errors.New("sheetdb: cache file from another version")

// Build loads one source like Load and also writes the result to path (via
// a temporary file and a rename, so a reader never sees half a file). The
// returned database is the in-memory one.
func Build(ctx context.Context, src Source, opt Options, path string) (*DB, error) {
	d, err := newMem(ctx)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*DB, error) {
		d.Close()
		return nil, err
	}
	if err := d.load(ctx, []Source{src}, opt); err != nil {
		return fail(err)
	}
	m, _ := json.Marshal(meta{Version: CacheVersion, Tables: d.Tables, Views: d.Views, Cells: d.Cells})
	if _, err := d.conn.ExecContext(ctx, "CREATE TABLE "+quote(metaTable)+" (v TEXT)"); err != nil {
		return fail(err)
	}
	if _, err := d.conn.ExecContext(ctx, "INSERT INTO "+quote(metaTable)+" VALUES (?)", string(m)); err != nil {
		return fail(err)
	}
	if err := d.saveTo(ctx, path); err != nil {
		return fail(err)
	}
	if err := d.seal(); err != nil {
		return fail(err)
	}
	return d, nil
}

// saveTo copies the database to path with SQLite's backup API (VACUUM INTO
// needs ATTACH, which these connections refuse).
func (d *DB) saveTo(ctx context.Context, path string) error {
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	tmp := path + ".tmp-" + hex.EncodeToString(rnd[:])
	dst, err := openRaw(ctx, tmp)
	if err != nil {
		return err
	}
	err = dst.conn.Raw(func(dc any) error {
		return d.conn.Raw(func(sc any) error {
			bk, err := dc.(*sqlite3.SQLiteConn).Backup("main", sc.(*sqlite3.SQLiteConn), "main")
			if err != nil {
				return err
			}
			if _, err := bk.Step(-1); err != nil {
				bk.Finish()
				return err
			}
			return bk.Finish()
		})
	})
	dst.Close()
	if err == nil {
		err = os.Chmod(tmp, 0o600)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

func openRaw(ctx context.Context, dsn string) (*DB, error) {
	register()
	db, err := openDB(dsn)
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db: db, conn: conn}, nil
}

// Open serves a cache file written by Build: read-only, straight from disk
// (the OS page cache holds it, not the Go heap).
func Open(ctx context.Context, path string) (*DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	d, err := openRaw(ctx, "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		return nil, err
	}
	m, err := d.readMeta(ctx, "main")
	if err != nil {
		d.Close()
		return nil, err
	}
	d.Tables, d.Views, d.Cells = m.Tables, m.Views, m.Cells
	if err := d.seal(); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) readMeta(ctx context.Context, schema string) (*meta, error) {
	var v string
	if err := d.conn.QueryRowContext(ctx, fmt.Sprintf("SELECT v FROM %s.%s", quote(schema), quote(metaTable))).Scan(&v); err != nil {
		return nil, fmt.Errorf("sheetdb: not a cache file: %w", err)
	}
	var m meta
	if err := json.Unmarshal([]byte(v), &m); err != nil {
		return nil, err
	}
	if m.Version != CacheVersion {
		return nil, ErrStaleCache
	}
	return &m, nil
}

// Part is one file of an Assemble: its cache file and how to name it.
type Part struct {
	Path, Label, Short string
}

// attachBatch stays under SQLITE_MAX_ATTACHED (10 in this build).
const attachBatch = 8

// Assemble puts several cache files into one in-memory database, so one
// query can join or stack them: tables are named after their files, and
// same-layout tables get a combined view.
func Assemble(ctx context.Context, parts []Part) (*DB, error) {
	d, err := newMem(ctx)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*DB, error) {
		d.Close()
		return nil, err
	}
	if err := d.conn.Raw(func(dc any) error {
		dc.(*sqlite3.SQLiteConn).SetLimit(sqlite3.SQLITE_LIMIT_ATTACHED, attachBatch)
		return nil
	}); err != nil {
		return fail(err)
	}
	used := map[string]bool{}
	for start := 0; start < len(parts); start += attachBatch {
		batch := parts[start:min(start+attachBatch, len(parts))]
		for i, p := range batch {
			if _, err := d.conn.ExecContext(ctx, fmt.Sprintf("ATTACH DATABASE ? AS p%d", i), p.Path); err != nil {
				return fail(fmt.Errorf("%s: %w", p.Label, err))
			}
		}
		tx, err := d.conn.BeginTx(ctx, nil)
		if err != nil {
			return fail(err)
		}
		for i, p := range batch {
			m, err := d.readMeta(ctx, fmt.Sprintf("p%d", i))
			if err != nil {
				tx.Rollback()
				return fail(fmt.Errorf("%s: %w", p.Label, err))
			}
			for _, t := range m.Tables {
				old := t.Name
				t.Name = uniqueName(tableName(len(parts) > 1, p.Short, t.Sheet, len(m.Tables)), used)
				t.Source, t.File = p.Label, p.Short
				if _, err := tx.ExecContext(ctx, createTyped(t.Name, t.Columns)); err != nil {
					tx.Rollback()
					return fail(err)
				}
				if _, err := tx.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s SELECT * FROM p%d.%s", quote(t.Name), i, quote(old))); err != nil {
					tx.Rollback()
					return fail(err)
				}
				d.Tables = append(d.Tables, t)
			}
			d.Cells += m.Cells
		}
		if err := tx.Commit(); err != nil {
			return fail(err)
		}
		for i := range batch {
			if _, err := d.conn.ExecContext(ctx, fmt.Sprintf("DETACH DATABASE p%d", i)); err != nil {
				return fail(err)
			}
		}
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	if d.Views, err = createViews(ctx, tx, d.Tables, used); err != nil {
		tx.Rollback()
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	if err := d.seal(); err != nil {
		return fail(err)
	}
	return d, nil
}

func openDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
