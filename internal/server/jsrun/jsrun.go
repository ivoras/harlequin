// Package jsrun is a shared, sandboxed JavaScript runner built on otto. It backs
// both the run_js agent tool and the <?js ?> skill templating engine, and gives
// the same safety guarantees to both: no filesystem, no network (except an
// allow-listed fetch), a hard execution timeout, and an output-size cap.
package jsrun

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/robertkrimen/otto"
)

// errHalt is the sentinel used to interrupt a long-running VM.
var errHalt = errors.New("jsrun: execution timeout")

// ErrOutputCap is returned when output exceeds the configured cap.
var ErrOutputCap = errors.New("jsrun: output cap exceeded")

// Options configure a Runner.
type Options struct {
	Timeout        time.Duration
	OutputCap      int
	FetchAllowlist []string
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

	if len(r.opts.FetchAllowlist) > 0 {
		fetchFn := r.makeFetch(vm)
		_ = vm.Set("fetch", func(call otto.FunctionCall) otto.Value { return fetchFn(call) })
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
			vm.Interrupt <- func() { panic(errHalt) }
		case <-done:
		}
	}()

	defer func() {
		if caught := recover(); caught != nil {
			if caught == errHalt {
				err = errHalt
				res = Result{Output: sb.String()}
				return
			}
			panic(caught)
		}
	}()

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

// makeFetch returns an allow-listed HTTP GET/POST helper.
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
