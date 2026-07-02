// Package skills provides the client-side skill workflow: download skill files
// into the local editing directory, read them back for upload, and reset. The
// server runs everything; this is purely a local working copy.
package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ivoras/harlequin/internal/client/apiclient"
)

// Manager handles the local skills directory.
type Manager struct {
	client *apiclient.Client
	dir    string
}

// NewManager constructs a client skills Manager rooted at dir (e.g. ~/.agents/skills).
func NewManager(client *apiclient.Client, dir string) *Manager {
	return &Manager{client: client, dir: dir}
}

// LocalDir returns the local directory for a skill.
func (m *Manager) LocalDir(name string) string {
	return filepath.Join(m.dir, name)
}

// Pull downloads the effective skill files into the local directory.
func (m *Manager) Pull(ctx context.Context, name string) (string, error) {
	sf, err := m.client.GetSkill(ctx, name)
	if err != nil {
		return "", err
	}
	root := m.LocalDir(name)
	for rel, content := range sf.Files {
		// The server validates relpaths on write, but never trust it when
		// writing to local disk: a "../" path must not escape the skill dir.
		if !filepath.IsLocal(filepath.FromSlash(rel)) {
			return "", fmt.Errorf("skill %q has unsafe file path %q", name, rel)
		}
		dest := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
			return "", err
		}
	}
	return root, nil
}

// PullFile downloads one file of the effective skill into the local directory.
func (m *Manager) PullFile(ctx context.Context, name, relpath string) (string, error) {
	content, _, err := m.client.GetSkillFile(ctx, name, relpath)
	if err != nil {
		return "", err
	}
	if !filepath.IsLocal(filepath.FromSlash(relpath)) {
		return "", fmt.Errorf("unsafe file path %q", relpath)
	}
	dest := filepath.Join(m.LocalDir(name), filepath.FromSlash(relpath))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
		return "", err
	}
	return dest, nil
}

// Push reads the local skill directory and uploads it as a skill in scope
// ("" = default scope).
func (m *Manager) Push(ctx context.Context, name, scope string) error {
	files, err := m.LocalFiles(name)
	if err != nil {
		return err
	}
	return m.client.PutSkill(ctx, name, scope, files)
}

// PushFile uploads a single local file of a skill into scope.
func (m *Manager) PushFile(ctx context.Context, name, relpath, scope string) error {
	raw, err := os.ReadFile(filepath.Join(m.LocalDir(name), filepath.FromSlash(relpath)))
	if err != nil {
		return err
	}
	return m.client.PutSkillFile(ctx, name, relpath, scope, string(raw))
}

// Create scaffolds a new skill on the server in scope, then pulls it locally.
func (m *Manager) Create(ctx context.Context, name, description, scope string) (string, error) {
	if err := m.client.CreateSkill(ctx, name, description, scope); err != nil {
		return "", err
	}
	return m.Pull(ctx, name)
}

// Reset removes the skill from scope ("" = default scope).
func (m *Manager) Reset(ctx context.Context, name, scope string) error {
	return m.client.ResetSkill(ctx, name, scope)
}

// LocalFiles reads the local copy of a skill (relpath -> content), for diffing.
func (m *Manager) LocalFiles(name string) (map[string]string, error) {
	root := m.LocalDir(name)
	files := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(raw)
		return nil
	})
	return files, err
}

// SortedNames returns local skill directory names.
func (m *Manager) SortedNames() []string {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}
