# Vendored SQLite headers

Harlequin compiles with CGO (`mattn/go-sqlite3` + `asg017/sqlite-vec-go-bindings`).
Those packages need **`sqlite3.h` at compile time** but do not require a system
`libsqlite3-dev` install.

## What is vendored here

| File | Purpose |
|------|---------|
| `include/sqlite3.h` | C API declarations for cgo |
| `include/sqlite3ext.h` | Extension API (included by sqlite-vec) |

The **SQLite library itself** is not linked from these files. Runtime SQLite
comes from the amalgamation embedded inside `github.com/mattn/go-sqlite3`
(`sqlite3-binding.c`). The headers here must stay **version-aligned** with that
dependency so cgo and the embedded engine agree on types and constants.

Current version: see `VERSION` (sourced from the same release as `go-sqlite3`).

## Updating

```sh
make vendor-sqlite
```

Or run `scripts/vendor-sqlite.sh` directly. It copies headers from the
`mattn/go-sqlite3` module in your Go module cache (matching `go.mod`).

## License

SQLite is in the public domain. See the blessing at the top of `sqlite3.h`.
