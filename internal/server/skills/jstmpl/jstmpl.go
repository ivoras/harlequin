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
	"github.com/robertkrimen/otto"
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
	}
	funcs := map[string]jsrun.HostFunc{}

	toVal := func(call otto.FunctionCall, v any) otto.Value {
		out, err := call.Otto.ToValue(v)
		if err != nil {
			return otto.UndefinedValue()
		}
		return out
	}

	funcs["__ctx_now"] = func(call otto.FunctionCall) otto.Value {
		return toVal(call, nowFn().Format(time.RFC3339))
	}
	funcs["__ctx_memory_search"] = func(call otto.FunctionCall) otto.Value {
		if c.MemorySearch == nil {
			return toVal(call, []string{})
		}
		return toVal(call, c.MemorySearch(call.Argument(0).String()))
	}
	funcs["__ctx_search_docs"] = func(call otto.FunctionCall) otto.Value {
		if c.SearchDocs == nil {
			return toVal(call, []string{})
		}
		return toVal(call, c.SearchDocs(call.Argument(0).String()))
	}

	// Build a ctx object in JS that wires the helpers together.
	shim := `var ctx = {
		user: __ctx_user,
		skill: __ctx_skill,
		now: function(){ return __ctx_now(); },
		memorySearch: function(q){ return __ctx_memory_search(q); },
		searchDocs: function(q){ return __ctx_search_docs(q); }
	};
` + code

	res, err := runner.Run(shim, jsrun.RunContext{Globals: globals, Funcs: funcs})
	if err != nil {
		return "", err
	}
	return res.Output, nil
}
