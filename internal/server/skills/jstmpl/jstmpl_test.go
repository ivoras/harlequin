package jstmpl

import (
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
