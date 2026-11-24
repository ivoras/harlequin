package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/sandboxfs"
	"github.com/ivoras/harlequin/internal/server/storage"
)

// tmpDirName is the per-user transient sandbox subdirectory; SweepTmp and
// userTmpDir must agree on it.
const tmpDirName = ".tmp"

// Per-user sandbox roots live under the user's data directory:
//
//	<DataDir>/users/<id>/.tmp      transient — DOM dumps, scratch
//	<DataDir>/users/<id>/.storage  persistent — generated parsers, recipes
func (a *Agent) userTmpDir(userID int64) string {
	return filepath.Join(storage.UserDir(a.DataDir, userID), tmpDirName)
}

// SweepTmp deletes files under every user's .tmp directory whose modtime is
// older than retentionDays. The tmp sandbox is quota-capped (see tmpRoot) but is
// never cleaned on its own, so without this it fills up and new saves start
// failing ("storage limit reached"). retentionDays <= 0 disables the sweep.
func (a *Agent) SweepTmp(retentionDays int) {
	if retentionDays <= 0 || a.DataDir == "" {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	usersDir := filepath.Join(a.DataDir, "users")
	entries, err := os.ReadDir(usersDir)
	if err != nil {
		return // users dir not created yet, or unreadable
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		tmpDir := filepath.Join(usersDir, e.Name(), tmpDirName)
		_ = filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil // missing .tmp (Walk's first call errors) is fine
			}
			if info.ModTime().Before(cutoff) {
				_ = os.Remove(path)
			}
			return nil
		})
	}
}

func (a *Agent) userStorageDir(userID int64) string {
	return filepath.Join(storage.UserDir(a.DataDir, userID), ".storage")
}

// tmpRoot is sized to hold a fetched page dump (webfetch caps bodies at 5 MiB)
// or the text of the largest real spreadsheets (~9 MB). Not higher: run_js
// reading a file into goja costs ~24 bytes of RAM per byte (16 MiB ≈ 400 MB).
func (a *Agent) tmpRoot(userID int64) *sandboxfs.Root {
	return sandboxfs.New(a.userTmpDir(userID), sandboxfs.Options{
		MaxFileBytes: 16 << 20, MaxFiles: 512, MaxTotalBytes: 128 << 20,
	})
}

func (a *Agent) storageRoot(userID int64) *sandboxfs.Root {
	return sandboxfs.New(a.userStorageDir(userID), sandboxfs.Options{})
}

// jsRunContext builds the per-user sandbox environment for a run_js or
// skill-defined-tool execution: scoped tmp/storage filesystems plus a resolver
// for skill:// / storage:// / tmp:// URIs (backing load()/include() inside the
// sandbox and run_js script-by-reference).
func (a *Agent) jsRunContext(ctx context.Context, rc *runContext) jsrun.RunContext {
	tmp := a.tmpRoot(rc.userID)
	store := a.storageRoot(rc.userID)
	return jsrun.RunContext{
		Ctx:     ctx,
		Tmp:     tmp,
		Storage: store,
		Resolve: a.makeResolver(ctx, rc, tmp, store),
	}
}

// makeResolver resolves a sandbox URI to its source text. skill:// reads the
// file's source as the session resolves the skill (hat overlay, then project →
// shared → personal; see readFile), so a hat or a deeper scope can override any
// shipped script; storage:// and tmp:// read the per-user scoped filesystems.
func (a *Agent) makeResolver(ctx context.Context, rc *runContext, tmp, store *sandboxfs.Root) func(string) (string, error) {
	return func(uri string) (string, error) {
		uri = strings.TrimSpace(uri)
		switch {
		case strings.HasPrefix(uri, "skill://"):
			b, err := a.readFile(ctx, rc, uri)
			return string(b), err
		case strings.HasPrefix(uri, "storage://"):
			b, err := store.Read(strings.TrimPrefix(uri, "storage://"))
			return string(b), err
		case strings.HasPrefix(uri, "tmp://"):
			b, err := tmp.Read(strings.TrimPrefix(uri, "tmp://"))
			return string(b), err
		default:
			return "", fmt.Errorf("unsupported URI %q (use skill://, storage:// or tmp://)", uri)
		}
	}
}

// ReadTmp reads a file from the user's tmp sandbox (tmp://<rel>) for the
// download route. Only that user's own tmp files are reachable; the sandbox
// root rejects paths that escape it.
func (a *Agent) ReadTmp(userID int64, rel string) ([]byte, error) {
	return a.tmpRoot(userID).Read(strings.TrimPrefix(strings.TrimSpace(rel), "/"))
}
