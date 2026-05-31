// Package mdtmpl builds the single JS-template context used when the server
// renders any .md (or other text) through the jstmpl engine — skills, the
// system prompt, and hat prompts all go through the same context. Today it
// exposes the current date and the logged-in username, plus guarded
// memory/document search helpers; see MD_JS_TEMPLATING.md.
package mdtmpl

import (
	"context"
	"database/sql"
	"time"

	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/server/skills/jstmpl"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// ContextFunc produces the template context for a (user, skill) render. It
// matches the function the skills Manager renders with.
type ContextFunc func(userID int64, username, skill string) jstmpl.Context

// New returns the context provider, wiring in the date, username, and the
// memory/document search helpers (the per-user database is opened on demand).
func New(mem *memory.Store, docs *documents.Store, store *storage.Manager) ContextFunc {
	return func(userID int64, username, skill string) jstmpl.Context {
		return jstmpl.Context{
			User:  username,
			Skill: skill,
			Now:   time.Now,
			MemorySearch: func(q string) []string {
				var res []types.SearchResult
				_ = store.WithUser(context.Background(), userID, func(udb *sql.DB) error {
					res, _ = mem.Search(context.Background(), udb, q, userID, "", 5)
					return nil
				})
				return contents(res)
			},
			SearchDocs: func(q string) []string {
				res, _ := docs.Search(context.Background(), q, 5)
				return contents(res)
			},
		}
	}
}

func contents(res []types.SearchResult) []string {
	out := make([]string, len(res))
	for i, r := range res {
		out[i] = r.Content
	}
	return out
}
