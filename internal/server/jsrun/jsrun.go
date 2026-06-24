// Package jsrun is a shared, sandboxed JavaScript runner built on goja
// (github.com/dop251/goja, an ES5.1+ engine with much of ES6). It backs both the
// run_js agent tool and the <?js ?> skill templating engine, and gives the same
// safety guarantees to both: a hard execution timeout and an output-size cap.
// Beyond print/println it can expose, when configured per run: a network fetch()
// (routed through an SSRF-guarded fetcher), an HTML dom helper, scoped
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

	"github.com/dop251/goja"
	"github.com/ivoras/harlequin/internal/server/dom"
	"github.com/ivoras/harlequin/internal/server/sandboxfs"
	"golang.org/x/net/html"
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

// HostFunc is a native goja function exposed to JavaScript.
type HostFunc func(call goja.FunctionCall) goja.Value

// RunContext carries the host API to expose for a run.
type RunContext struct {
	// Globals are set as top-level JS bindings (e.g. "ctx", "args").
	Globals map[string]any
	// Funcs are set as top-level JS functions. Each value may be a native
	// goja func (func(goja.FunctionCall) goja.Value) or any plain Go func, which
	// goja marshals automatically (args in, return value out).
	Funcs map[string]any
	// Ctx bounds host operations (fetch); defaults to context.Background().
	Ctx context.Context
	// Tmp and Storage, when set, expose tmp.* / storage.* scoped filesystems.
	Tmp     *sandboxfs.Root
	Storage *sandboxfs.Root
	// Resolve loads the source of a skill://, storage:// or tmp:// URI, backing
	// load()/include(). Nil disables them.
	Resolve func(uri string) (string, error)
}

// throwf panics with a JS Error (caught by goja and re-thrown into the script).
func throwf(vm *goja.Runtime, format string, a ...any) {
	panic(vm.NewGoError(fmt.Errorf(format, a...)))
}

// Run executes code, exposing the given host context, and returns its output and value.
func (r *Runner) Run(code string, rc RunContext) (res Result, err error) {
	vm := goja.New()

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

	_ = vm.Set("print", func(call goja.FunctionCall) goja.Value {
		write(joinArgs(call.Arguments))
		return goja.Undefined()
	})
	_ = vm.Set("println", func(call goja.FunctionCall) goja.Value {
		write(joinArgs(call.Arguments) + "\n")
		return goja.Undefined()
	})

	baseCtx := rc.Ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}

	// fetch(): full fetcher when configured, else the legacy allow-listed GET.
	if r.opts.Fetcher != nil {
		fetchFn := r.makeFetchVia(vm, baseCtx, r.opts.Fetcher)
		_ = vm.Set("fetch", func(call goja.FunctionCall) goja.Value { return fetchFn(call) })
	} else if len(r.opts.FetchAllowlist) > 0 {
		fetchFn := r.makeFetch(vm)
		_ = vm.Set("fetch", func(call goja.FunctionCall) goja.Value { return fetchFn(call) })
	}

	// Host functions for the dom helper, scoped filesystems, and load/include.
	for name, fn := range r.hostAPI(vm, rc) {
		hf := fn
		_ = vm.Set(name, func(call goja.FunctionCall) goja.Value { return hf(call) })
	}

	for k, v := range rc.Globals {
		_ = vm.Set(k, v)
	}
	for k, fn := range rc.Funcs {
		// goja marshals a native func (exact signature) as-is, and any other Go
		// func via reflection; either works set directly.
		_ = vm.Set(k, fn)
	}

	// Watchdog for the halting problem: interrupt the VM after the timeout.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-time.After(r.opts.Timeout):
			vm.Interrupt(ErrTimeout)
		case <-done:
		}
	}()

	// Build the dom/tmp/storage/load/include JS API on top of the host functions.
	if _, runErr := vm.RunString(bootstrapJS); runErr != nil {
		return Result{Output: sb.String()}, runErr
	}

	// Wrap the code so a bare `return` at top level is legal and its value captured.
	wrapped := "(function(){\n" + code + "\n})()"
	val, runErr := vm.RunString(wrapped)
	if runErr != nil {
		var ie *goja.InterruptedError
		if errors.As(runErr, &ie) {
			return Result{Output: sb.String()}, ErrTimeout
		}
		return Result{Output: sb.String()}, runErr
	}
	if capped {
		return Result{Output: sb.String()}, ErrOutputCap
	}

	var exported any
	if val != nil && !goja.IsUndefined(val) && !goja.IsNull(val) {
		exported = val.Export()
	}
	return Result{Output: sb.String(), Value: exported}, nil
}

func joinArgs(args []goja.Value) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = a.String()
	}
	return strings.Join(parts, " ")
}

// makeFetch returns the legacy allow-listed HTTP GET helper (used only when no
// full Fetcher is configured).
func (r *Runner) makeFetch(vm *goja.Runtime) HostFunc {
	client := &http.Client{Timeout: r.opts.Timeout}
	allowed := func(host string) bool {
		for _, h := range r.opts.FetchAllowlist {
			if strings.EqualFold(h, host) {
				return true
			}
		}
		return false
	}
	return func(call goja.FunctionCall) goja.Value {
		url := call.Argument(0).String()
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			throwf(vm, "FetchError: %s", err.Error())
		}
		if !allowed(req.URL.Hostname()) {
			throwf(vm, "FetchError: host %q not in allowlist", req.URL.Hostname())
		}
		resp, err := client.Do(req)
		if err != nil {
			throwf(vm, "FetchError: %s", err.Error())
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, int64(r.opts.OutputCap)))
		obj := vm.NewObject()
		_ = obj.Set("status", resp.StatusCode)
		_ = obj.Set("body", string(body))
		return obj
	}
}

// makeFetchVia routes fetch() through the configured Fetcher (any public host,
// SSRF-guarded). The network call is bounded by the run's timeout.
func (r *Runner) makeFetchVia(vm *goja.Runtime, ctx context.Context, f Fetcher) HostFunc {
	return func(call goja.FunctionCall) goja.Value {
		url := strings.TrimSpace(call.Argument(0).String())
		fctx, cancel := context.WithTimeout(ctx, r.opts.Timeout)
		defer cancel()
		res, err := f.FetchRaw(fctx, url)
		if err != nil {
			throwf(vm, "FetchError: %s", err.Error())
		}
		obj := vm.NewObject()
		_ = obj.Set("status", res.Status)
		_ = obj.Set("body", string(res.Body))
		_ = obj.Set("finalUrl", res.FinalURL)
		_ = obj.Set("contentType", res.ContentType)
		return obj
	}
}

// hostAPI returns the low-level host functions consumed by bootstrapJS: the dom
// helper (with a per-run document registry), the scoped filesystems, and
// load/include. Unconfigured capabilities return a JS error when used.
func (r *Runner) hostAPI(vm *goja.Runtime, rc RunContext) map[string]HostFunc {
	docs := map[int64]*dom.Doc{}      // dom.parse handle -> doc (for json/lists)
	nodeReg := map[int64]*html.Node{} // handle -> element/root node (for chainable query)
	var nextHandle int64
	register := func(n *html.Node) int64 {
		nextHandle++
		nodeReg[nextHandle] = n
		return nextHandle
	}

	toJS := func(v any) goja.Value {
		b, err := json.Marshal(v)
		if err != nil {
			throwf(vm, "DomError: %s", err.Error())
		}
		var generic any
		if err := json.Unmarshal(b, &generic); err != nil {
			throwf(vm, "DomError: %s", err.Error())
		}
		return vm.ToValue(generic)
	}
	resolveDoc := func(call goja.FunctionCall) *dom.Doc {
		h := call.Argument(0).ToInteger()
		d, ok := docs[h]
		if !ok {
			throwf(vm, "DomError: dom.json/dom.lists need the handle returned by dom.parse")
		}
		return d
	}
	// resolveCtx accepts a dom handle (number, from dom.parse) or a node object
	// returned by a previous dom.query (it carries an _h field), so queries chain.
	resolveCtx := func(v goja.Value) *html.Node {
		if obj, ok := v.(*goja.Object); ok {
			if hv := obj.Get("_h"); hv != nil && !goja.IsUndefined(hv) {
				if n, ok := nodeReg[hv.ToInteger()]; ok {
					return n
				}
			}
		} else if n, ok := nodeReg[v.ToInteger()]; ok {
			return n
		}
		throwf(vm, "DomError: first argument must be a dom handle (from dom.parse) or a node returned by a previous dom.query")
		return nil
	}
	makeNodeObj := func(n *html.Node) map[string]any {
		s := dom.Summarize(n, 0)
		m := map[string]any{"tag": s.Tag, "path": s.Path, "_h": register(n)}
		if s.ID != "" {
			m["id"] = s.ID
		}
		if s.Class != "" {
			m["class"] = s.Class
		}
		if len(s.Attrs) > 0 {
			m["attrs"] = s.Attrs
		}
		if s.Text != "" {
			m["text"] = s.Text
		}
		// textContent mirrors the DOM property: the element's full text (bounded),
		// so model code reaching for node.textContent works. getAttribute is added
		// JS-side (see bootstrapJS) since funcs don't survive the JSON bridge.
		if full := dom.Summarize(n, 8192).Text; full != "" {
			m["textContent"] = full
		}
		return m
	}
	fsRoot := func(area string) *sandboxfs.Root {
		switch area {
		case "tmp":
			if rc.Tmp == nil {
				throwf(vm, "FSError: tmp filesystem not available here")
			}
			return rc.Tmp
		case "storage":
			if rc.Storage == nil {
				throwf(vm, "FSError: storage filesystem not available here")
			}
			return rc.Storage
		}
		throwf(vm, "FSError: unknown filesystem %s", area)
		return nil
	}

	api := map[string]HostFunc{}

	api["__dom_parse"] = func(call goja.FunctionCall) goja.Value {
		d, err := dom.Parse([]byte(call.Argument(0).String()))
		if err != nil {
			throwf(vm, "DomError: %s", err.Error())
		}
		h := register(d.RootNode())
		docs[h] = d
		return vm.ToValue(h)
	}
	api["__dom_query"] = func(call goja.FunctionCall) goja.Value {
		ctx := resolveCtx(call.Argument(0))
		nodes, err := dom.QueryNode(ctx, call.Argument(1).String())
		if err != nil {
			throwf(vm, "DomError: %s", err.Error())
		}
		out := make([]map[string]any, 0, len(nodes))
		for _, n := range nodes {
			out = append(out, makeNodeObj(n))
		}
		return toJS(out)
	}
	api["__dom_grep"] = func(call goja.FunctionCall) goja.Value {
		ctx := resolveCtx(call.Argument(0))
		opts := exportMap(call.Argument(2))
		gopts := dom.GrepOptions{
			Regex:      optBool(opts, "regex", false),
			IgnoreCase: optBool(opts, "ignoreCase", true),
			Attrs:      optBool(opts, "attrs", true),
			MaxMatches: optInt(opts, "maxMatches"),
			TextChars:  optInt(opts, "textChars"),
		}
		nodes, err := dom.GrepNode(ctx, call.Argument(1).String(), gopts)
		if err != nil {
			throwf(vm, "DomError: %s", err.Error())
		}
		return toJS(nodes)
	}
	api["__dom_lists"] = func(call goja.FunctionCall) goja.Value {
		return toJS(resolveDoc(call).RepeatingGroups(3, 20, 160))
	}
	api["__dom_json"] = func(call goja.FunctionCall) goja.Value {
		opts := exportMap(call.Argument(1))
		sk, err := resolveDoc(call).Skeleton(dom.SkelOptions{
			Selector:    optString(opts, "selector"),
			MaxDepth:    optIntDefault(opts, "maxDepth", 3),
			MaxChildren: optInt(opts, "maxChildren"),
			TextChars:   optInt(opts, "textChars"),
			Paths:       optBool(opts, "paths", true),
		})
		if err != nil {
			throwf(vm, "DomError: %s", err.Error())
		}
		return toJS(sk)
	}

	api["__fs_read"] = func(call goja.FunctionCall) goja.Value {
		b, err := fsRoot(call.Argument(0).String()).Read(call.Argument(1).String())
		if err != nil {
			throwf(vm, "FSError: %s", err.Error())
		}
		return vm.ToValue(string(b))
	}
	api["__fs_write"] = func(call goja.FunctionCall) goja.Value {
		if err := fsRoot(call.Argument(0).String()).Write(call.Argument(1).String(), []byte(call.Argument(2).String())); err != nil {
			throwf(vm, "FSError: %s", err.Error())
		}
		return goja.Undefined()
	}
	api["__fs_list"] = func(call goja.FunctionCall) goja.Value {
		names, err := fsRoot(call.Argument(0).String()).List(call.Argument(1).String())
		if err != nil {
			throwf(vm, "FSError: %s", err.Error())
		}
		return vm.ToValue(names)
	}
	api["__fs_remove"] = func(call goja.FunctionCall) goja.Value {
		if err := fsRoot(call.Argument(0).String()).Remove(call.Argument(1).String()); err != nil {
			throwf(vm, "FSError: %s", err.Error())
		}
		return goja.Undefined()
	}
	api["__fs_exists"] = func(call goja.FunctionCall) goja.Value {
		ok, err := fsRoot(call.Argument(0).String()).Exists(call.Argument(1).String())
		if err != nil {
			throwf(vm, "FSError: %s", err.Error())
		}
		return vm.ToValue(ok)
	}

	api["__resolve_load"] = func(call goja.FunctionCall) goja.Value {
		if rc.Resolve == nil {
			throwf(vm, "ResolveError: load/include not available here")
		}
		src, err := rc.Resolve(call.Argument(0).String())
		if err != nil {
			throwf(vm, "ResolveError: %s", err.Error())
		}
		return vm.ToValue(src)
	}
	api["__resolve_include"] = func(call goja.FunctionCall) goja.Value {
		if rc.Resolve == nil {
			throwf(vm, "ResolveError: load/include not available here")
		}
		uri := call.Argument(0).String()
		src, err := rc.Resolve(uri)
		if err != nil {
			throwf(vm, "ResolveError: %s", err.Error())
		}
		// Run in the VM's global scope so the included script's functions/vars
		// become available to the caller.
		if _, err := vm.RunString(src); err != nil {
			throwf(vm, "ResolveError: include %s: %v", uri, err)
		}
		return goja.Undefined()
	}

	return api
}

// bootstrapJS wires the low-level host functions into ergonomic globals: dom,
// tmp, storage, load, include.
const bootstrapJS = `
function __domDecorate(n){
  if (n && typeof n === 'object') {
    n.getAttribute = function(name){
      if (n.attrs && n.attrs[name] !== undefined && n.attrs[name] !== null) return n.attrs[name];
      if (name === 'class') return n.class || null;
      if (name === 'id') return n.id || null;
      return null;
    };
    if (n.textContent === undefined) n.textContent = n.text || "";
  }
  return n;
}
function __domDecorateAll(a){ if (a) { for (var i=0;i<a.length;i++) __domDecorate(a[i]); } return a; }
var dom = {
  parse: function(html){ return __dom_parse(html); },
  query: function(ctx, sel){ return __domDecorateAll(__dom_query(ctx, sel)); },
  grep:  function(ctx, pat, opts){ return __domDecorateAll(__dom_grep(ctx, pat, opts || {})); },
  lists: function(h){ return __dom_lists(h); },
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

// --- option-object helpers ---

func exportMap(v goja.Value) map[string]any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	m, _ := v.Export().(map[string]any)
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
