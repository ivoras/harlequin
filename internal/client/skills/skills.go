// Package skills provides the client-side skill workflow: download skill files
// into the local editing directory, read them back for upload, and reset. The
// server runs everything; this is purely a local working copy.
package skills

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

// Push reads the local skill directory and uploads it as the user's override.
func (m *Manager) Push(ctx context.Context, name string) error {
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
	if err != nil {
		return err
	}
	return m.client.PutSkill(ctx, name, files)
}

// Reset removes the user's server-side override.
func (m *Manager) Reset(ctx context.Context, name string) error {
	return m.client.ResetSkill(ctx, name)
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

// Scaffold creates a new skill template locally.
func (m *Manager) Scaffold(name string) (string, error) {
	root := m.LocalDir(name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	tmpl := strings.Join([]string{
		"---",
		"name: " + name,
		"description: TODO describe when this skill should be used.",
		"---",
		"# " + name,
		"",
		"TODO: write the skill instructions here. You can use <?js print(ctx.user); ?> for dynamic content.",
		"",
	}, "\n")
	dest := filepath.Join(root, "SKILL.md")
	if err := os.WriteFile(dest, []byte(tmpl), 0o644); err != nil {
		return "", err
	}
	return root, nil
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
