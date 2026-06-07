// Package sandboxfs is a small, path-scoped filesystem confined to a single base
// directory. It backs the JS sandbox's tmp:// and storage:// areas: callers may
// only read and write regular files under the base (no traversal out, no symlink
// escape), subject to per-file and total size/count quotas.
package sandboxfs

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Options bound how much a Root may hold.
type Options struct {
	// MaxFileBytes caps a single file (<=0 → 1 MiB).
	MaxFileBytes int64
	// MaxFiles caps the number of files under the root (<=0 → 256).
	MaxFiles int
	// MaxTotalBytes caps total bytes under the root (<=0 → 16 MiB).
	MaxTotalBytes int64
}

func (o Options) withDefaults() Options {
	if o.MaxFileBytes <= 0 {
		o.MaxFileBytes = 1 << 20
	}
	if o.MaxFiles <= 0 {
		o.MaxFiles = 256
	}
	if o.MaxTotalBytes <= 0 {
		o.MaxTotalBytes = 16 << 20
	}
	return o
}

// Root is a filesystem confined to base.
type Root struct {
	base string
	opts Options
}

// New returns a Root confined to base (created on first write).
func New(base string, opts Options) *Root {
	return &Root{base: filepath.Clean(base), opts: opts.withDefaults()}
}

// resolve maps a sandbox-relative name to an absolute path guaranteed to live
// under the base, neutralizing "..", absolute paths, and leading slashes.
func (r *Root) resolve(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("empty path")
	}
	// Clean against a virtual root so any ".." is absorbed before joining.
	clean := path.Clean("/" + filepath.ToSlash(name))
	rel := strings.TrimPrefix(clean, "/")
	if rel == "" || rel == "." {
		return "", fmt.Errorf("invalid path %q", name)
	}
	return filepath.Join(r.base, filepath.FromSlash(rel)), nil
}

// withinBase reports whether p resolves (following symlinks where possible) to a
// location under the base — defense-in-depth against a planted symlink.
func (r *Root) withinBase(p string) bool {
	real := p
	if rp, err := filepath.EvalSymlinks(p); err == nil {
		real = rp
	}
	realBase := r.base
	if rb, err := filepath.EvalSymlinks(r.base); err == nil {
		realBase = rb
	}
	rel, err := filepath.Rel(realBase, real)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// Read returns the contents of name.
func (r *Root) Read(name string) ([]byte, error) {
	full, err := r.resolve(name)
	if err != nil {
		return nil, err
	}
	if !r.withinBase(full) {
		return nil, fmt.Errorf("path %q escapes sandbox", name)
	}
	return os.ReadFile(full)
}

// Exists reports whether name exists.
func (r *Root) Exists(name string) (bool, error) {
	full, err := r.resolve(name)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(full); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Write stores data at name (creating parent directories), enforcing the quotas.
func (r *Root) Write(name string, data []byte) error {
	full, err := r.resolve(name)
	if err != nil {
		return err
	}
	if int64(len(data)) > r.opts.MaxFileBytes {
		return fmt.Errorf("file %q is %d bytes, exceeds limit of %d", name, len(data), r.opts.MaxFileBytes)
	}
	count, total, existing := r.usage(full)
	if existing < 0 { // new file
		if count+1 > r.opts.MaxFiles {
			return fmt.Errorf("file count limit reached (%d)", r.opts.MaxFiles)
		}
		existing = 0
	}
	if total-existing+int64(len(data)) > r.opts.MaxTotalBytes {
		return fmt.Errorf("storage limit reached (%d bytes)", r.opts.MaxTotalBytes)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	if !r.withinBase(filepath.Dir(full)) {
		return fmt.Errorf("path %q escapes sandbox", name)
	}
	return os.WriteFile(full, data, 0o644)
}

// Remove deletes name (no error if absent).
func (r *Root) Remove(name string) error {
	full, err := r.resolve(name)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// List returns the relative paths (slash-separated) of files under the root,
// optionally filtered by a glob matched against the relative path. Empty glob
// returns everything.
func (r *Root) List(glob string) ([]string, error) {
	glob = strings.TrimSpace(glob)
	var out []string
	err := filepath.Walk(r.base, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // base not created yet
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(r.base, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if glob != "" {
			ok, err := path.Match(glob, rel)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// usage returns the current file count, total bytes, and the size of the file at
// target (-1 if it does not exist yet) under the root.
func (r *Root) usage(target string) (count int, total int64, targetSize int64) {
	targetSize = -1
	_ = filepath.Walk(r.base, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		count++
		total += info.Size()
		if p == target {
			targetSize = info.Size()
		}
		return nil
	})
	return count, total, targetSize
}
