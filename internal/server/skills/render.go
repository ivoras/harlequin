package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"

	"github.com/ivoras/harlequin/internal/server/skills/jstmpl"
)

// RenderFile reads a baked asset file under the skills root (e.g.
// "system_prompt.md") straight from the server binary and renders its <?js ?>
// templates fresh for the given user.
func (m *Manager) RenderFile(name string, userID int64, username string) (string, error) {
	raw, err := fs.ReadFile(m.baked, "skills/"+name)
	if err != nil {
		return "", err
	}
	return jstmpl.Render(m.runner, string(raw), m.makeCtx(userID, username, ""))
}

// BakedSystemPrompt returns the raw (unrendered) default system prompt
// template from the binary — used to seed a hat's custom prompt editor.
func (m *Manager) BakedSystemPrompt() (string, error) {
	raw, err := fs.ReadFile(m.baked, "skills/system_prompt.md")
	return string(raw), err
}

// RenderText renders arbitrary template text (e.g. a hat's stored system
// prompt) with the same context as files.
func (m *Manager) RenderText(text string, userID int64, username string) (string, error) {
	return jstmpl.Render(m.runner, text, m.makeCtx(userID, username, ""))
}

// hashBytes is the content hash used by the seed reconciler (hex SHA-256).
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
