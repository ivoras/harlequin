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

build: build-server build-client

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

web-build: web-install
	cd web && npm run build

web-dev:
	cd web && npm run dev

# Copy sqlite3.h from mattn/go-sqlite3 (matches the runtime embedded engine).
vendor-sqlite:
	@bash scripts/vendor-sqlite.sh
