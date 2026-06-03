package jstmpl

import (
	"strings"
	"testing"
	"time"

	"github.com/ivoras/harlequin/internal/server/jsrun"
)

func TestRender(t *testing.T) {
	r := jsrun.New(jsrun.Options{Timeout: 2 * time.Second, OutputCap: 4096})
	src := "Hello <?js print(ctx.user); ?> at <?js print(ctx.now()); ?>!"
	out, err := Render(r, src, Context{User: "alice", Skill: "x"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	t.Logf("out = %q", out)
	if out[:12] != "Hello alice " {
		t.Fatalf("unexpected: %q", out)
	}
}

func TestRenderDate(t *testing.T) {
	r := jsrun.New(jsrun.Options{Timeout: 2 * time.Second, OutputCap: 4096})
	fixed := time.Date(2026, 5, 31, 9, 0, 0, 0, time.UTC)
	out, err := Render(r, "Today is <?js print(ctx.date); ?>.", Context{
		User: "bob", Now: func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if out != "Today is 2026-05-31." {
		t.Fatalf("unexpected: %q", out)
	}
}

func TestRenderMemoryGlob(t *testing.T) {
	r := jsrun.New(jsrun.Options{Timeout: 2 * time.Second, OutputCap: 4096})
	ctx := Context{
		MemoryGlob: func(glob string) []map[string]string {
			if glob != "user.*" {
				t.Fatalf("glob = %q", glob)
			}
			return []map[string]string{
				{"key": "user.name", "value": "Ivan", "content": "User's name is Ivan"},
				{"key": "user.preferred_currency", "value": "EUR", "content": "User prefers the EUR currency."},
			}
		},
	}
	src := "<?js var s = ctx.memoryGlob(\"user.*\"); for (var i=0;i<s.length;i++){ println(\"- \"+s[i].key+\": \"+s[i].content); } ?>"
	out, err := Render(r, src, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "- user.name: User's name is Ivan") ||
		!strings.Contains(out, "- user.preferred_currency: User prefers the EUR currency.") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestRenderMemoryGlobNilSafe(t *testing.T) {
	r := jsrun.New(jsrun.Options{Timeout: 2 * time.Second, OutputCap: 4096})
	out, err := Render(r, "<?js var s = ctx.memoryGlob(\"user.*\"); print(s.length); ?>", Context{})
	if err != nil || out != "0" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}
