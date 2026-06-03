// Command mcptest is a small integration harness for the mcp.Registry against
// real SQLite databases. The mcp and memory packages cannot host _test.go files
// because the sqlite-vec extension fails to link into test binaries (see
// memory MEMORY.md: sqlite-vec-test-linking), so DB-touching registry logic is
// exercised here instead.
//
//	Run: CGO_ENABLED=1 CGO_CFLAGS="-I$(pwd)/third_party/sqlite/include" \
//	     go run -tags sqlite_fts5 ./cmd/mcptest
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/ivoras/harlequin/internal/server/mcp"
	"github.com/ivoras/harlequin/internal/server/secrets"
	"github.com/ivoras/harlequin/internal/server/storage"
	"golang.org/x/oauth2"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("FAIL: %v", err)
	}
	fmt.Println("PASS: mcp registry round-trips")
}

func run() error {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "mcptest")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	store, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 64)
	if err != nil {
		return fmt.Errorf("storage.New: %w", err)
	}
	defer store.Close()

	key := make([]byte, secrets.KeySize)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	cipher, err := secrets.New(key)
	if err != nil {
		return err
	}
	reg := mcp.NewRegistry(store.Shared, cipher)

	// 1. Shared header server: create, list, verify decryption.
	shared := mcp.Server{
		Scope: mcp.ScopeShared, Name: "wiki", URL: "https://wiki.example.com/mcp",
		AuthType: mcp.AuthHeader, HeaderName: "Authorization", HeaderValue: "Bearer secret-123",
		Enabled: true, CreatedBy: 1,
	}
	if err := reg.Create(ctx, shared, nil); err != nil {
		return fmt.Errorf("create shared: %w", err)
	}
	got, err := reg.Get(ctx, mcp.ScopeShared, "wiki", nil)
	if err != nil {
		return fmt.Errorf("get shared: %w", err)
	}
	if got.HeaderValue != "Bearer secret-123" || got.HeaderName != "Authorization" {
		return fmt.Errorf("header credential did not round-trip: %+v", got)
	}

	// Verify the stored credential is actually ciphertext, not plaintext.
	var enc []byte
	if err := store.Shared.QueryRowContext(ctx, `SELECT auth_secret_enc FROM mcp_servers WHERE name='wiki'`).Scan(&enc); err != nil {
		return err
	}
	if len(enc) == 0 {
		return fmt.Errorf("expected encrypted credential blob")
	}
	if containsPlaintext(enc, "secret-123") {
		return fmt.Errorf("credential stored in plaintext!")
	}

	// Duplicate name should fail.
	if err := reg.Create(ctx, shared, nil); err == nil {
		return fmt.Errorf("expected duplicate-name error")
	}

	// 2. Per-user OAuth server + token persistence.
	return store.WithUser(ctx, 1, func(udb *sql.DB) error {
		userSrv := mcp.Server{
			Scope: mcp.ScopeUser, Name: "github", URL: "https://api.githubcopilot.com/mcp",
			AuthType: mcp.AuthOAuth, Enabled: true, CreatedBy: 1,
			OAuth: &mcp.OAuthMeta{ClientID: "client-abc", AuthorizationEndpoint: "https://x/auth", TokenEndpoint: "https://x/token"},
		}
		if err := reg.Create(ctx, userSrv, udb); err != nil {
			return fmt.Errorf("create user: %w", err)
		}

		// ListVisible should show both shared and user servers.
		vis, err := reg.ListVisible(ctx, udb)
		if err != nil {
			return err
		}
		if len(vis) != 2 {
			return fmt.Errorf("expected 2 visible servers, got %d", len(vis))
		}

		// Save/load an OAuth token.
		tok := &oauth2.Token{AccessToken: "at-1", RefreshToken: "rt-1", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}
		if err := reg.SaveToken(ctx, udb, mcp.ScopeUser, "github", tok); err != nil {
			return fmt.Errorf("save token: %w", err)
		}
		loaded, err := reg.LoadToken(ctx, udb, mcp.ScopeUser, "github")
		if err != nil {
			return fmt.Errorf("load token: %w", err)
		}
		if loaded == nil || loaded.AccessToken != "at-1" || loaded.RefreshToken != "rt-1" {
			return fmt.Errorf("token did not round-trip: %+v", loaded)
		}

		// Delete should also drop tokens.
		if err := reg.Delete(ctx, mcp.ScopeUser, "github", udb); err != nil {
			return fmt.Errorf("delete: %w", err)
		}
		if _, err := reg.Get(ctx, mcp.ScopeUser, "github", udb); err != mcp.ErrNotFound {
			return fmt.Errorf("expected ErrNotFound after delete, got %v", err)
		}
		gone, err := reg.LoadToken(ctx, udb, mcp.ScopeUser, "github")
		if err != nil {
			return err
		}
		if gone != nil {
			return fmt.Errorf("expected token removed after delete")
		}
		return nil
	})
}

func containsPlaintext(haystack []byte, needle string) bool {
	n := []byte(needle)
	for i := 0; i+len(n) <= len(haystack); i++ {
		if string(haystack[i:i+len(n)]) == needle {
			return true
		}
	}
	return false
}
