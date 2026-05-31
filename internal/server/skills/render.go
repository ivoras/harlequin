package skills

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ivoras/harlequin/internal/server/skills/jstmpl"
)

// fileCache caches deployed source files by path, keyed on mtime+size. Only the
// raw source is cached, never the result of templating (which depends on the
// per-request context). A changed mtime invalidates the entry.
type fileCache struct {
	mu sync.Mutex
	m  map[string]cachedFile
}

type cachedFile struct {
	mtime time.Time
	size  int64
	data  []byte
}

func newFileCache() *fileCache {
	return &fileCache{m: map[string]cachedFile{}}
}

// read returns the file's bytes, serving the in-memory copy when the file's
// mtime and size are unchanged since it was last read.
func (c *fileCache) read(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[path]; ok && e.mtime.Equal(info.ModTime()) && e.size == info.Size() {
		return e.data, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c.m[path] = cachedFile{mtime: info.ModTime(), size: info.Size(), data: data}
	return data, nil
}

// RenderFile reads a deployed file under the skills directory (e.g.
// "system_prompt.md") — source cached by mtime — and renders its <?js ?>
// templates fresh for the given user.
func (m *Manager) RenderFile(name string, userID int64, username string) (string, error) {
	raw, err := m.cache.read(filepath.Join(m.skillDir, name))
	if err != nil {
		return "", err
	}
	return jstmpl.Render(m.runner, string(raw), m.makeCtx(userID, username, ""))
}

// RenderText renders arbitrary template text (e.g. a hat's stored system
// prompt) with the same context as files.
func (m *Manager) RenderText(text string, userID int64, username string) (string, error) {
	return jstmpl.Render(m.runner, text, m.makeCtx(userID, username, ""))
}
