#!/usr/bin/env bash
# Refresh vendored SQLite headers under third_party/sqlite/include/.
# Headers are taken from github.com/mattn/go-sqlite3 in the module cache so they
# match the SQLite version linked into the server at runtime.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="$ROOT/third_party/sqlite/include"
VERSION_FILE="$ROOT/third_party/sqlite/VERSION"

cd "$ROOT"

# Resolve mattn/go-sqlite3 module dir (version from go.mod).
MOD_DIR="$(go list -m -f '{{.Dir}}' github.com/mattn/go-sqlite3)"

if [[ ! -f "$MOD_DIR/sqlite3-binding.h" ]]; then
  echo "error: $MOD_DIR/sqlite3-binding.h not found; run 'go mod download' first" >&2
  exit 1
fi

mkdir -p "$DEST"
rm -f "$DEST/sqlite3.h" "$DEST/sqlite3ext.h"
cp "$MOD_DIR/sqlite3-binding.h" "$DEST/sqlite3.h"
cp "$MOD_DIR/sqlite3ext.h"      "$DEST/sqlite3ext.h"
chmod u+w "$DEST/sqlite3.h" "$DEST/sqlite3ext.h"

# Record version from the header.
VER="$(grep -m1 '#define SQLITE_VERSION ' "$DEST/sqlite3.h" | sed 's/.*"\(.*\)".*/\1/')"
echo "$VER" > "$VERSION_FILE"

echo "Vendored SQLite $VER headers into third_party/sqlite/include/"
