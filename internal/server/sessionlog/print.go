package sessionlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// PrintOptions controls trajectory pretty-printing.
type PrintOptions struct {
	Verbose bool // include token/thinking deltas
	Color   bool // ANSI colors when writing to a terminal
}

// ReadFile parses a JSONL trajectory log file.
func ReadFile(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Read(f)
}

// Read parses JSONL trajectory events from r.
func Read(r io.Reader) ([]Event, error) {
	var events []Event
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// Print writes a human-readable trajectory to w.
func Print(w io.Writer, events []Event, opts PrintOptions) {
	c := newPrinter(w, opts)
	for _, ev := range events {
		c.printEvent(ev)
	}
}

type printer struct {
	w     io.Writer
	opts  PrintOptions
	bold  string
	dim   string
	cyan  string
	yell  string
	green string
	red   string
	reset string
}

func newPrinter(w io.Writer, opts PrintOptions) *printer {
	p := &printer{w: w, opts: opts}
	if opts.Color {
		p.bold = "\033[1m"
		p.dim = "\033[2m"
		p.cyan = "\033[36m"
		p.yell = "\033[33m"
		p.green = "\033[32m"
		p.red = "\033[31m"
		p.reset = "\033[0m"
	}
	return p
}

func (p *printer) println(format string, args ...any) {
	fmt.Fprintf(p.w, format+"\n", args...)
}

func (p *printer) printEvent(ev Event) {
	if !p.opts.Verbose && (ev.Type == TypeLLMDelta || ev.Type == TypeThinkingDelta) {
		return
	}

	header := fmt.Sprintf("%s[%s]%s turn=%d step=%d %s%s%s",
		p.dim, ev.TS, p.reset, ev.Turn, ev.Step, p.bold, ev.Type, p.reset)

	switch ev.Type {
	case TypeToolCall:
		name := strData(ev.Data, "name")
		marker := p.toolMarker(name)
		p.println("%s %s", header, marker)
		p.printToolCallDetails(ev.Data)
	case TypeToolResult:
		name := strData(ev.Data, "name")
		marker := p.toolMarker(name)
		dur := formatDuration(ev.Data)
		p.println("%s %s%s%s", header, marker, p.dim, dur)
		p.printToolResultDetails(ev.Data, name)
	case TypeToolsAvailable:
		p.println("%s", header)
		p.printToolsAvailable(ev.Data)
	case TypeSkillLoaded:
		p.println("%s %s★ skill_loaded%s", header, p.yell, p.reset)
		p.printDataFields(ev.Data, "name", "source", "files")
	default:
		p.println("%s", header)
		p.printGenericData(ev)
	}
	p.println("")
}

func (p *printer) toolMarker(name string) string {
	switch name {
	case "list_skills":
		return fmt.Sprintf("%s★ TOOL list_skills (skill catalogue)%s", p.yell, p.reset)
	case "load_skill":
		return fmt.Sprintf("%s★ TOOL load_skill (skill load)%s", p.yell, p.reset)
	default:
		return fmt.Sprintf("%s⚙ TOOL %s%s", p.cyan, name, p.reset)
	}
}

func (p *printer) printToolCallDetails(data map[string]any) {
	if id := strData(data, "id"); id != "" {
		p.println("  id: %s", id)
	}
	if args, ok := data["args"]; ok && args != nil {
		p.println("  args: %s", compactJSON(args))
	} else if raw := strData(data, "args_raw"); raw != "" {
		p.println("  args: %s", raw)
	}
}

func (p *printer) printToolResultDetails(data map[string]any, name string) {
	if id := strData(data, "id"); id != "" {
		p.println("  id: %s", id)
	}
	if ok, exists := data["ok"]; exists {
		if b, _ := ok.(bool); b {
			p.println("  status: %sok%s", p.green, p.reset)
		} else {
			p.println("  status: %serror%s", p.red, p.reset)
		}
	}
	if errMsg := strData(data, "error"); errMsg != "" {
		p.println("  error: %s%s%s", p.red, errMsg, p.reset)
	}
	if name == "list_skills" {
		p.printSkillListOutput(strData(data, "output"))
		return
	}
	if out := strData(data, "output"); out != "" {
		p.printIndentedBlock("output", out, 4000)
	}
	if n, ok := asInt(data["output_bytes"]); ok {
		p.println("  output_bytes: %d", n)
	}
}

func (p *printer) printSkillListOutput(output string) {
	p.println("  %sskills:%s", p.yell, p.reset)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		p.println("    %s", line)
	}
}

func (p *printer) printToolsAvailable(data map[string]any) {
	if n, ok := asInt(data["count"]); ok {
		p.println("  count: %d", n)
	}
	if tools, ok := data["tools"].([]any); ok {
		names := make([]string, 0, len(tools))
		desc := map[string]string{}
		for _, t := range tools {
			m, _ := t.(map[string]any)
			name := strData(m, "name")
			if name == "" {
				continue
			}
			names = append(names, name)
			desc[name] = strData(m, "description")
		}
		sort.Strings(names)
		for _, name := range names {
			tag := p.cyan + "⚙" + p.reset
			if name == "list_skills" || name == "load_skill" {
				tag = p.yell + "★" + p.reset
			}
			p.println("  %s %s — %s", tag, name, desc[name])
		}
	}
}

func (p *printer) printGenericData(ev Event) {
	switch ev.Type {
	case TypeUserMessage:
		p.printIndentedBlock("content", strData(ev.Data, "content"), 2000)
	case TypeLLMResponse:
		p.printDataFields(ev.Data, "provider", "model", "content", "thinking")
		if calls, ok := ev.Data["tool_calls"].([]any); ok && len(calls) > 0 {
			p.println("  tool_calls:")
			for _, c := range calls {
				p.println("    - %s", compactJSON(c))
			}
		}
	case TypeLLMRequest:
		p.printDataFields(ev.Data, "provider", "model", "messages", "tools")
		if names, ok := ev.Data["tool_names"].([]any); ok {
			p.println("  tool_names: %s", joinAny(names))
		}
	case TypeUsage:
		p.printDataFields(ev.Data, "provider", "model", "prompt_tokens", "completion_tokens", "total_tokens")
	case TypeSessionStart, TypeSessionEnd:
		p.printDataFields(ev.Data, "status", "max_steps", "provider", "steps", "total_tokens")
	case TypeSystemPrompt:
		content := strData(ev.Data, "content")
		if len(content) > 500 {
			content = content[:500] + "…"
		}
		p.println("  content: %q", content)
	default:
		if len(ev.Data) > 0 {
			p.println("  %s", compactJSON(ev.Data))
		}
	}
}

func (p *printer) printDataFields(data map[string]any, keys ...string) {
	for _, k := range keys {
		if v, ok := data[k]; ok && v != nil && fmt.Sprint(v) != "" {
			p.println("  %s: %v", k, v)
		}
	}
}

func (p *printer) printIndentedBlock(label, text string, max int) {
	if len(text) > max {
		text = text[:max] + "…"
	}
	p.println("  %s:", label)
	for _, line := range strings.Split(text, "\n") {
		p.println("    %s", line)
	}
}

func strData(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	default:
		return 0, false
	}
}

func formatDuration(data map[string]any) string {
	if ms, ok := asInt(data["duration_ms"]); ok {
		if ns, ok := asInt(data["duration_ns"]); ok && ns%1_000_000 != 0 {
			return fmt.Sprintf(" (%dms %dns)", ms, ns%1_000_000)
		}
		return fmt.Sprintf(" (%dms)", ms)
	}
	return ""
}

func compactJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func joinAny(items []any) string {
	parts := make([]string, len(items))
	for i, v := range items {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, ", ")
}
