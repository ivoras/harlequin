// Package jstmpl renders PHP-style <?js ... ?> templates using the jsrun sandbox.
// Text outside blocks is emitted literally; inside a block, anything passed to
// print/println is spliced in at that position. Rendering fails closed: any JS
// error or timeout aborts the whole render with an error.
package jstmpl

import (
	"fmt"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/server/jsrun"
)

const (
	openTag  = "<?js"
	closeTag = "?>"
)

// Context is the read-only data exposed to templates as `ctx`.
type Context struct {
	User  string
	Skill string
	// Now returns the current time; injectable for tests.
	Now func() time.Time
	// MemorySearch and SearchDocs are optional guarded helpers.
	MemorySearch func(query string) []string
	SearchDocs   func(query string) []string
	// MemoryGlob returns memories whose slot key matches a GLOB pattern
	// (e.g. "user.*"); each item has id, key, value, content.
	MemoryGlob func(glob string) []map[string]string
}

// Render evaluates all <?js ?> blocks in src and returns the rendered string.
func Render(runner *jsrun.Runner, src string, c Context) (string, error) {
	if !strings.Contains(src, openTag) {
		return src, nil
	}
	nowFn := c.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	var out strings.Builder
	rest := src
	for {
		idx := strings.Index(rest, openTag)
		if idx < 0 {
			out.WriteString(rest)
			break
		}
		out.WriteString(rest[:idx])
		rest = rest[idx+len(openTag):]

		end := strings.Index(rest, closeTag)
		if end < 0 {
			return "", fmt.Errorf("jstmpl: unterminated <?js block")
		}
		code := rest[:end]
		rest = rest[end+len(closeTag):]

		rendered, err := renderBlock(runner, code, c, nowFn)
		if err != nil {
			return "", fmt.Errorf("jstmpl: block error: %w", err)
		}
		out.WriteString(rendered)
	}
	return out.String(), nil
}

func renderBlock(runner *jsrun.Runner, code string, c Context, nowFn func() time.Time) (string, error) {
	globals := map[string]any{
		"__ctx_user":  c.User,
		"__ctx_skill": c.Skill,
		"__ctx_date":  nowFn().Format("2006-01-02"),
	}
	// Plain Go funcs; goja marshals arguments and return values automatically.
	funcs := map[string]any{
		"__ctx_now": func() string { return nowFn().Format(time.RFC3339) },
		"__ctx_memory_search": func(q string) []string {
			if c.MemorySearch == nil {
				return []string{}
			}
			return c.MemorySearch(q)
		},
		"__ctx_search_docs": func(q string) []string {
			if c.SearchDocs == nil {
				return []string{}
			}
			return c.SearchDocs(q)
		},
		"__ctx_memory_glob": func(g string) []map[string]string {
			if c.MemoryGlob == nil {
				return []map[string]string{}
			}
			return c.MemoryGlob(g)
		},
	}

	// Build a ctx object in JS that wires the helpers together.
	shim := `var ctx = {
		user: __ctx_user,
		skill: __ctx_skill,
		date: __ctx_date,
		now: function(){ return __ctx_now(); },
		memorySearch: function(q){ return __ctx_memory_search(q); },
		searchDocs: function(q){ return __ctx_search_docs(q); },
		memoryGlob: function(g){ return __ctx_memory_glob(g); }
	};
` + code

	res, err := runner.Run(shim, jsrun.RunContext{Globals: globals, Funcs: funcs})
	if err != nil {
		return "", err
	}
	return res.Output, nil
}
