package agent

import (
	"testing"

	"github.com/ivoras/harlequin/internal/server/llm"
)

// The tools array is rendered into the prompt prefix; an unstable order defeats
// llama.cpp prompt-prefix caching. sortToolDefs must impose a deterministic order
// regardless of input order (the registry is a map).
func TestSortToolDefsIsDeterministic(t *testing.T) {
	mk := func(name string) llm.Tool {
		return llm.Tool{Type: "function", Function: llm.FunctionDefinition{Name: name}}
	}
	defs := []llm.Tool{mk("run_js"), mk("WebFetch"), mk("calculator"), mk("ask_user"), mk("Grep")}
	sortToolDefs(defs)
	want := []string{"Grep", "WebFetch", "ask_user", "calculator", "run_js"}
	for i, w := range want {
		if defs[i].Function.Name != w {
			t.Fatalf("position %d: got %q, want %q (order=%v)", i, defs[i].Function.Name, w, toolNamesOf(defs))
		}
	}
	// A different input order yields the same output order.
	defs2 := []llm.Tool{mk("calculator"), mk("Grep"), mk("run_js"), mk("ask_user"), mk("WebFetch")}
	sortToolDefs(defs2)
	for i := range defs {
		if defs[i].Function.Name != defs2[i].Function.Name {
			t.Fatalf("orders differ at %d: %v vs %v", i, toolNamesOf(defs), toolNamesOf(defs2))
		}
	}
}

func toolNamesOf(defs []llm.Tool) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.Function.Name
	}
	return out
}
