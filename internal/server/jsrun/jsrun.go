// Package jsrun is a shared, sandboxed JavaScript runner built on otto. It backs
// both the run_js agent tool and the <?js ?> skill templating engine, and gives
// the same safety guarantees to both: a hard execution timeout and an output-size
// cap. Beyond print/println it can expose, when configured per run: a network
// fetch() (routed through an SSRF-guarded fetcher), an HTML dom helper, scoped
// tmp/storage filesystems, and load()/include() for pulling in skill scripts.
package jsrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/server/dom"
	"github.com/ivoras/harlequin/internal/server/sandboxfs"
	"github.com/robertkrimen/otto"
)

// ErrTimeout is the sentinel used to interrupt (and report) a run that exceeds
// the configured execution timeout.
var ErrTimeout = errors.New("jsrun: execution timeout")

// ErrOutputCap is returned when output exceeds the configured cap.
var ErrOutputCap = errors.New("jsrun: output cap exceeded")

// FetchResult is the raw response a Fetcher returns to the sandbox fetch().
type FetchResult struct {
	Status      int
	Body        []byte
	FinalURL    string
	ContentType string
}

// Fetcher retrieves a URL's raw body for the sandbox fetch(). Implementations
// must enforce the deployment's network policy (e.g. block private addresses).
type Fetcher interface {
	FetchRaw(ctx context.Context, url string) (FetchResult, error)
}

// Options configure a Runner.
type Options struct {
	Timeout   time.Duration
	OutputCap int
	// FetchAllowlist enables a minimal GET-only fetch() restricted to these hosts.
	// Used only when Fetcher is nil (legacy fallback).
	FetchAllowlist []string
	// Fetcher, when set, backs fetch() with the full web fetcher (any public host,
	// SSRF-guarded); FetchAllowlist is then ignored.
	Fetcher Fetcher
}

// Runner executes JavaScript snippets in a sandbox.
type Runner struct {
	opts Options
}

// New constructs a Runner with sensible defaults.
func New(opts Options) *Runner {
	if opts.Timeout <= 0 {
		opts.Timeout = 3 * time.Second
	}
	if opts.OutputCap <= 0 {
		opts.OutputCap = 64 * 1024
	}
	return &Runner{opts: opts}
}

// Result holds the outcome of a run.
type Result struct {
	// Output is everything written via print/println.
	Output string
	// Value is the exported final expression value (may be nil).
	Value any
}

// HostFunc is a Go function exposed to JavaScript.
type HostFunc func(call otto.FunctionCall) otto.Value

// RunContext carries the host API to expose for a run.
type RunContext struct {
	// Globals are set as top-level JS bindings (e.g. "ctx", "args").
	Globals map[string]any
	// Funcs are set as top-level JS functions (e.g. custom helpers).
	Funcs map[string]HostFunc
	// Ctx bounds host operations (fetch); defaults to context.Background().
	Ctx context.Context
	// Tmp and Storage, when set, expose tmp.* / storage.* scoped filesystems.
	Tmp     *sandboxfs.Root
	Storage *sandboxfs.Root
	// Resolve loads the source of a skill://, storage:// or tmp:// URI, backing
	// load()/include(). Nil disables them.
	Resolve func(uri string) (string, error)
}

// Run executes code, exposing the given host context, and returns its output and value.
func (r *Runner) Run(code string, rc RunContext) (res Result, err error) {
	vm := otto.New()

	var sb strings.Builder
	capped := false
	write := func(s string) {
		if capped {
			return
		}
		if sb.Len()+len(s) > r.opts.OutputCap {
			remain := r.opts.OutputCap - sb.Len()
			if remain > 0 {
				sb.WriteString(s[:remain])
			}
			capped = true
			return
		}
		sb.WriteString(s)
	}

	printFn := func(call otto.FunctionCall) otto.Value {
		write(joinArgs(call.ArgumentList))
		return otto.UndefinedValue()
	}
	printlnFn := func(call otto.FunctionCall) otto.Value {
		write(joinArgs(call.ArgumentList) + "\n")
		return otto.UndefinedValue()
	}
	_ = vm.Set("print", printFn)
	_ = vm.Set("println", printlnFn)

	baseCtx := rc.Ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}

	// fetch(): full fetcher when configured, else the legacy allow-listed GET.
	if r.opts.Fetcher != nil {
		fetchFn := r.makeFetchVia(vm, baseCtx, r.opts.Fetcher)
		_ = vm.Set("fetch", func(call otto.FunctionCall) otto.Value { return fetchFn(call) })
	} else if len(r.opts.FetchAllowlist) > 0 {
		fetchFn := r.makeFetch(vm)
		_ = vm.Set("fetch", func(call otto.FunctionCall) otto.Value { return fetchFn(call) })
	}

	// Host functions for the dom helper, scoped filesystems, and load/include.
	for name, fn := range r.hostAPI(vm, rc) {
		hf := fn
		_ = vm.Set(name, func(call otto.FunctionCall) otto.Value { return hf(call) })
	}

	for k, v := range rc.Globals {
		_ = vm.Set(k, v)
	}
	for k, fn := range rc.Funcs {
		// Wrap in the literal func type otto special-cases; a named type
		// (HostFunc) would otherwise be handled via reflection and demand args.
		hf := fn
		_ = vm.Set(k, func(call otto.FunctionCall) otto.Value { return hf(call) })
	}

	// Watchdog for the halting problem.
	vm.Interrupt = make(chan func(), 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-time.After(r.opts.Timeout):
			vm.Interrupt <- func() { panic(ErrTimeout) }
		case <-done:
		}
	}()

	defer func() {
		if caught := recover(); caught != nil {
			if caught == ErrTimeout {
				err = ErrTimeout
				res = Result{Output: sb.String()}
				return
			}
			panic(caught)
		}
	}()

	// Build the dom/tmp/storage/load/include JS API on top of the host functions.
	if _, runErr := vm.Run(bootstrapJS); runErr != nil {
		return Result{Output: sb.String()}, runErr
	}

	// Wrap the code so a bare `return` at top level is legal and its value captured.
	wrapped := "(function(){\n" + code + "\n})()"
	val, runErr := vm.Run(wrapped)
	if runErr != nil {
		return Result{Output: sb.String()}, runErr
	}
	if capped {
		return Result{Output: sb.String()}, ErrOutputCap
	}

	exported, _ := val.Export()
	return Result{Output: sb.String(), Value: exported}, nil
}

func joinArgs(args []otto.Value) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = a.String()
	}
	return strings.Join(parts, " ")
}

// makeFetch returns the legacy allow-listed HTTP GET helper (used only when no
// full Fetcher is configured).
func (r *Runner) makeFetch(vm *otto.Otto) HostFunc {
	client := &http.Client{Timeout: r.opts.Timeout}
	allowed := func(host string) bool {
		for _, h := range r.opts.FetchAllowlist {
			if strings.EqualFold(h, host) {
				return true
			}
		}
		return false
	}
	return func(call otto.FunctionCall) otto.Value {
		url := call.Argument(0).String()
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			panic(vm.MakeCustomError("FetchError", err.Error()))
		}
		if !allowed(req.URL.Hostname()) {
			panic(vm.MakeCustomError("FetchError", fmt.Sprintf("host %q not in allowlist", req.URL.Hostname())))
		}
		resp, err := client.Do(req)
		if err != nil {
			panic(vm.MakeCustomError("FetchError", err.Error()))
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, int64(r.opts.OutputCap)))
		obj, _ := vm.Object(`({})`)
		_ = obj.Set("status", resp.StatusCode)
		_ = obj.Set("body", string(body))
		return obj.Value()
	}
}

// makeFetchVia routes fetch() through the configured Fetcher (any public host,
// SSRF-guarded). The network call is bounded by the run's timeout.
func (r *Runner) makeFetchVia(vm *otto.Otto, ctx context.Context, f Fetcher) HostFunc {
	return func(call otto.FunctionCall) otto.Value {
		url := strings.TrimSpace(call.Argument(0).String())
		fctx, cancel := context.WithTimeout(ctx, r.opts.Timeout)
		defer cancel()
		res, err := f.FetchRaw(fctx, url)
		if err != nil {
			panic(vm.MakeCustomError("FetchError", err.Error()))
		}
		obj, _ := vm.Object(`({})`)
		_ = obj.Set("status", res.Status)
		_ = obj.Set("body", string(res.Body))
		_ = obj.Set("finalUrl", res.FinalURL)
		_ = obj.Set("contentType", res.ContentType)
		return obj.Value()
	}
}

// hostAPI returns the low-level host functions consumed by bootstrapJS: the dom
// helper (with a per-run document registry), the scoped filesystems, and
// load/include. Unconfigured capabilities return a JS error when used.
func (r *Runner) hostAPI(vm *otto.Otto, rc RunContext) map[string]HostFunc {
	docs := map[int64]*dom.Doc{}
	var nextHandle int64

	toJS := func(v any) otto.Value {
		b, err := json.Marshal(v)
		if err != nil {
			panic(vm.MakeCustomError("DomError", err.Error()))
		}
		var generic any
		if err := json.Unmarshal(b, &generic); err != nil {
			panic(vm.MakeCustomError("DomError", err.Error()))
		}
		out, err := vm.ToValue(generic)
		if err != nil {
			panic(vm.MakeCustomError("DomError", err.Error()))
		}
		return out
	}
	getDoc := func(call otto.FunctionCall) *dom.Doc {
		h, _ := call.Argument(0).ToInteger()
		d, ok := docs[h]
		if !ok {
			panic(vm.MakeCustomError("DomError", "unknown dom handle"))
		}
		return d
	}
	fsRoot := func(area string) *sandboxfs.Root {
		switch area {
		case "tmp":
			if rc.Tmp == nil {
				panic(vm.MakeCustomError("FSError", "tmp filesystem not available here"))
			}
			return rc.Tmp
		case "storage":
			if rc.Storage == nil {
				panic(vm.MakeCustomError("FSError", "storage filesystem not available here"))
			}
			return rc.Storage
		}
		panic(vm.MakeCustomError("FSError", "unknown filesystem "+area))
	}

	api := map[string]HostFunc{}

	api["__dom_parse"] = func(call otto.FunctionCall) otto.Value {
		d, err := dom.Parse([]byte(call.Argument(0).String()))
		if err != nil {
			panic(vm.MakeCustomError("DomError", err.Error()))
		}
		nextHandle++
		docs[nextHandle] = d
		v, _ := vm.ToValue(nextHandle)
		return v
	}
	api["__dom_query"] = func(call otto.FunctionCall) otto.Value {
		nodes, err := getDoc(call).Query(call.Argument(1).String(), 0)
		if err != nil {
			panic(vm.MakeCustomError("DomError", err.Error()))
		}
		return toJS(nodes)
	}
	api["__dom_grep"] = func(call otto.FunctionCall) otto.Value {
		opts := exportMap(call.Argument(2))
		gopts := dom.GrepOptions{
			Regex:      optBool(opts, "regex", false),
			IgnoreCase: optBool(opts, "ignoreCase", true),
			Attrs:      optBool(opts, "attrs", true),
			MaxMatches: optInt(opts, "maxMatches"),
			TextChars:  optInt(opts, "textChars"),
		}
		nodes, err := getDoc(call).Grep(call.Argument(1).String(), gopts)
		if err != nil {
			panic(vm.MakeCustomError("DomError", err.Error()))
		}
		return toJS(nodes)
	}
	api["__dom_json"] = func(call otto.FunctionCall) otto.Value {
		opts := exportMap(call.Argument(1))
		sk, err := getDoc(call).Skeleton(dom.SkelOptions{
			Selector:    optString(opts, "selector"),
			MaxDepth:    optIntDefault(opts, "maxDepth", 3),
			MaxChildren: optInt(opts, "maxChildren"),
			TextChars:   optInt(opts, "textChars"),
			Paths:       optBool(opts, "paths", true),
		})
		if err != nil {
			panic(vm.MakeCustomError("DomError", err.Error()))
		}
		return toJS(sk)
	}

	api["__fs_read"] = func(call otto.FunctionCall) otto.Value {
		b, err := fsRoot(call.Argument(0).String()).Read(call.Argument(1).String())
		if err != nil {
			panic(vm.MakeCustomError("FSError", err.Error()))
		}
		v, _ := vm.ToValue(string(b))
		return v
	}
	api["__fs_write"] = func(call otto.FunctionCall) otto.Value {
		if err := fsRoot(call.Argument(0).String()).Write(call.Argument(1).String(), []byte(call.Argument(2).String())); err != nil {
			panic(vm.MakeCustomError("FSError", err.Error()))
		}
		return otto.UndefinedValue()
	}
	api["__fs_list"] = func(call otto.FunctionCall) otto.Value {
		names, err := fsRoot(call.Argument(0).String()).List(call.Argument(1).String())
		if err != nil {
			panic(vm.MakeCustomError("FSError", err.Error()))
		}
		v, _ := vm.ToValue(names)
		return v
	}
	api["__fs_remove"] = func(call otto.FunctionCall) otto.Value {
		if err := fsRoot(call.Argument(0).String()).Remove(call.Argument(1).String()); err != nil {
			panic(vm.MakeCustomError("FSError", err.Error()))
		}
		return otto.UndefinedValue()
	}
	api["__fs_exists"] = func(call otto.FunctionCall) otto.Value {
		ok, err := fsRoot(call.Argument(0).String()).Exists(call.Argument(1).String())
		if err != nil {
			panic(vm.MakeCustomError("FSError", err.Error()))
		}
		v, _ := vm.ToValue(ok)
		return v
	}

	api["__resolve_load"] = func(call otto.FunctionCall) otto.Value {
		if rc.Resolve == nil {
			panic(vm.MakeCustomError("ResolveError", "load/include not available here"))
		}
		src, err := rc.Resolve(call.Argument(0).String())
		if err != nil {
			panic(vm.MakeCustomError("ResolveError", err.Error()))
		}
		v, _ := vm.ToValue(src)
		return v
	}
	api["__resolve_include"] = func(call otto.FunctionCall) otto.Value {
		if rc.Resolve == nil {
			panic(vm.MakeCustomError("ResolveError", "load/include not available here"))
		}
		uri := call.Argument(0).String()
		src, err := rc.Resolve(uri)
		if err != nil {
			panic(vm.MakeCustomError("ResolveError", err.Error()))
		}
		// Run in the VM's global scope so the included script's functions/vars
		// become available to the caller.
		if _, err := vm.Run(src); err != nil {
			panic(vm.MakeCustomError("ResolveError", fmt.Sprintf("include %s: %v", uri, err)))
		}
		return otto.UndefinedValue()
	}

	return api
}

// bootstrapJS wires the low-level host functions into ergonomic globals: dom,
// tmp, storage, load, include.
const bootstrapJS = `
var dom = {
  parse: function(html){ return __dom_parse(html); },
  query: function(h, sel){ return __dom_query(h, sel); },
  grep:  function(h, pat, opts){ return __dom_grep(h, pat, opts || {}); },
  json:  function(h, opts){ return __dom_json(h, opts || {}); }
};
function __mkfs(area){
  return {
    read:   function(n){ return __fs_read(area, n); },
    write:  function(n, d){ return __fs_write(area, n, d); },
    list:   function(g){ return __fs_list(area, g || ""); },
    remove: function(n){ return __fs_remove(area, n); },
    exists: function(n){ return __fs_exists(area, n); }
  };
}
var tmp = __mkfs("tmp");
var storage = __mkfs("storage");
function load(uri){ return __resolve_load(uri); }
function include(uri){ return __resolve_include(uri); }
`

// --- otto option-object helpers ---

func exportMap(v otto.Value) map[string]any {
	exported, err := v.Export()
	if err != nil {
		return nil
	}
	m, _ := exported.(map[string]any)
	return m
}

func optBool(m map[string]any, key string, def bool) bool {
	if m == nil {
		return def
	}
	if v, ok := m[key].(bool); ok {
		return v
	}
	return def
}

func optString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func optInt(m map[string]any, key string) int {
	return optIntDefault(m, key, 0)
}

func optIntDefault(m map[string]any, key string, def int) int {
	if m == nil {
		return def
	}
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int64:
		return int(v)
	case int:
		return v
	}
	return def
}
