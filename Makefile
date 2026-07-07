.PHONY: build build-server build-client run-server run-client test tidy clean fmt vendor-sqlite web-install web-build web-dev

# CGO + FTS5 are required for sqlite (mattn/go-sqlite3) and sqlite-vec.
CGO_ENABLED ?= 1
GOFLAGS := -tags "sqlite_fts5"
export CGO_ENABLED

# Vendored sqlite3.h (see third_party/sqlite/). No system libsqlite3-dev needed.
SQLITE_INCLUDE := $(CURDIR)/third_party/sqlite/include
CGO_CFLAGS += -I$(SQLITE_INCLUDE)
export CGO_CFLAGS

BIN_DIR := bin

# build runs web-build first so the server binary embeds the current UI.
# Use build-server directly for a fast Go-only iteration (embeds whatever is
# in web/dist at the time).
build: web-build build-server build-client

build-server:
	go build $(GOFLAGS) -o $(BIN_DIR)/harlequin-server ./cmd/harlequin-server

build-client:
	go build $(GOFLAGS) -o $(BIN_DIR)/harlequin ./cmd/harlequin

run-server: build-server
	$(BIN_DIR)/harlequin-server

run-client: build-client
	$(BIN_DIR)/harlequin

test:
	go test $(GOFLAGS) ./...

tidy:
	go mod tidy

fmt:
	go fmt ./...

clean:
	rm -rf $(BIN_DIR)

# --- Web UI (static Svelte SPA in web/) ---
web-install:
	cd web && npm install

# vite's emptyOutDir wipes dist, including the committed .gitkeep that the
# go:embed directive relies on when no UI has been built — restore it.
web-build: web-install
	cd web && npm run build
	@touch web/dist/.gitkeep

web-dev:
	cd web && npm run dev

# Copy sqlite3.h from mattn/go-sqlite3 (matches the runtime embedded engine).
vendor-sqlite:
	@bash scripts/vendor-sqlite.sh
