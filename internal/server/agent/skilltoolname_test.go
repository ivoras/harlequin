package agent

import (
	"context"
	"database/sql"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/session"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/server/skills/jstmpl"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// Skill tools are offered under names every provider accepts; the old dotted
// name stays callable but hidden.
func TestSkillToolNames(t *testing.T) {
	valid := regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	reg := map[string]toolEntry{}
	a := &Agent{}
	a.registerSkillTool(reg, "e-racun", skills.ToolDefinition{Name: "check"}, "")
	a.registerSkillTool(reg, "naplata ios.v2", skills.ToolDefinition{Name: "usporedi"}, "")
	a.registerSkillTool(reg, strings.Repeat("dugo-ime-vjestine-", 5), skills.ToolDefinition{Name: "alat"}, "")
	visible := 0
	for name, e := range reg {
		if e.hidden {
			continue
		}
		visible++
		if !valid.MatchString(name) || e.def.Function.Name != name {
			t.Errorf("offered tool name %q (def %q) is not valid for providers", name, e.def.Function.Name)
		}
	}
	if visible != 3 {
		t.Fatalf("visible tools = %d, want 3", visible)
	}
	if e, ok := reg["e-racun__check"]; !ok || e.hidden {
		t.Fatalf("e-racun__check missing or hidden: %v", ok)
	}
	if e, ok := reg["e-racun.check"]; !ok || !e.hidden {
		t.Fatalf("dotted alias missing or offered: %v", ok)
	}
	if _, ok := reg["naplata_ios_v2__usporedi"]; !ok {
		t.Fatalf("unsafe characters not replaced: %v", keys(reg))
	}
}

// scriptProvider makes the tool calls in script, one step each, then answers;
// offered records, per request, whether dice__roll was in the tool list.
type scriptProvider struct {
	script  []llm.ToolCall
	offered *[]bool
}

func (scriptProvider) Name() string { return "script" }
func (p scriptProvider) Chat(_ context.Context, req llm.ChatRequest) (<-chan llm.Chunk, error) {
	has := false
	for _, t := range req.Tools {
		has = has || t.Function.Name == "dice__roll"
	}
	*p.offered = append(*p.offered, has)
	ch := make(chan llm.Chunk, 2)
	if n := len(*p.offered); n <= len(p.script) {
		ch <- llm.Chunk{Done: true, Model: "fake", ToolCalls: []llm.ToolCall{p.script[n-1]}}
	} else {
		ch <- llm.Chunk{TextDelta: "done"}
		ch <- llm.Chunk{Done: true, Model: "fake"}
	}
	close(ch)
	return ch, nil
}

// A skill's tools are offered from the step after a successful load_skill (a
// failed one does not count) and in the session's later turns; with
// skill_tools_upfront they are offered from the start.
func TestSkillToolsDeferredUntilLoaded(t *testing.T) {
	load := func(id, name string) llm.ToolCall {
		return llm.ToolCall{ID: id, Function: llm.FunctionCall{Name: "load_skill", Arguments: `{"name":"` + name + `"}`}}
	}
	for _, c := range []struct {
		name    string
		upfront bool
		want    string // offered per request: turn 1 (load nope, load dice, answer), then turn 2
	}{
		{"deferred", false, "--+ +"},
		{"upfront", true, "+++ +"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
			if err != nil {
				t.Fatal(err)
			}
			defer mgr.Close()
			sk := skills.NewManager(mgr.Shared, fstest.MapFS{}, jsrun.New(jsrun.Options{}),
				func(int64, string, string) jstmpl.Context { return jstmpl.Context{} })
			md := "---\nname: dice\ndescription: Dice.\ntools:\n  - name: roll\n    description: Roll.\n    run: return 4;\n---\nRoll with dice__roll.\n"
			if err := sk.Save(ctx, mgr.Shared, "dice", 1, map[string]string{"SKILL.md": md}); err != nil {
				t.Fatal(err)
			}
			var offered []bool
			sessions := session.NewStore()
			a := &Agent{Storage: mgr, Sessions: sessions, MaxSteps: 8, Skills: sk, Runner: jsrun.New(jsrun.Options{}),
				SkillToolsUpfront: c.upfront,
				Provider:          scriptProvider{[]llm.ToolCall{load("c1", "nope"), load("c2", "dice")}, &offered}}
			var sessID int64
			_ = mgr.WithUser(ctx, 1, func(udb *sql.DB) error {
				s, err := sessions.Create(ctx, udb, 1, "t", "", types.APIREST, types.InterfaceWeb)
				sessID = s.ID
				return err
			})
			for _, prompt := range []string{"roll", "again"} {
				if err := a.Run(ctx, 0, sessID, 1, "u@x", types.RoleUser, "", "", prompt, func(types.StreamEvent) {}); err != nil {
					t.Fatal(err)
				}
			}
			got := ""
			for i, o := range offered {
				if i == 3 {
					got += " "
				}
				got += map[bool]string{true: "+", false: "-"}[o]
			}
			if got != c.want {
				t.Fatalf("offered = %q, want %q", got, c.want)
			}
		})
	}
}

func keys(m map[string]toolEntry) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// MCP tool names go through the same rules; the server is still called with
// the tool's own name.
func TestAPIToolName(t *testing.T) {
	for _, c := range []struct {
		parts []string
		want  string
	}{
		{[]string{"mcp", "github", "create_issue"}, "mcp__github__create_issue"},
		{[]string{"mcp", "my server", "files.read"}, "mcp__my_server__files_read"},
		{[]string{"usporedba-računa", "provjeri"}, "usporedba-ra_una__provjeri"},
	} {
		if got := apiToolName(c.parts...); got != c.want {
			t.Errorf("apiToolName(%q) = %q, want %q", c.parts, got, c.want)
		}
	}
	long := strings.Repeat("x", 70)
	a, b := apiToolName("mcp", "s", long+"a"), apiToolName("mcp", "s", long+"b")
	if len(a) != maxToolNameLen || a == b {
		t.Fatalf("long names: %q (%d), %q", a, len(a), b)
	}
}
