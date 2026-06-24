package agent

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/sandboxfs"
)

// grepResultCap bounds the Grep output handed to the model.
const grepResultCap = 15000

const grepDescription = `Search the contents of files saved in your sandbox namespaces (tmp:// and storage://) for a regular-expression pattern. Use this to search a page saved by WebFetchDOM (save_file), a stored document, or any saved data — without reading the whole file.
- pattern is a regular expression (RE2 syntax).
- path is a namespace location: a file ("tmp://links.html"), a namespace root ("tmp://" — searches all files there), or a subdirectory. Defaults to "tmp://".
- output_mode: "files_with_matches" (default) lists matching file paths; "content" shows matching lines; "count" shows per-file match counts.
- glob filters files by name (e.g. "*.html"); type filters by kind (e.g. "html", "json", "js").
- -A/-B/-C add lines after/before/around each match (content mode); -i is case-insensitive; -n adds line numbers (content mode); multiline lets the pattern span lines (. matches newline); head_limit caps results.`

func grepToolDef() llm.Tool {
	return fnTool("Grep", grepDescription, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern":     map[string]any{"type": "string", "description": "Regular expression to search for (RE2 syntax)"},
			"path":        map[string]any{"type": "string", "description": "Namespace file, root, or directory to search (e.g. tmp://links.html, tmp://, storage://docs). Defaults to tmp://"},
			"output_mode": map[string]any{"type": "string", "enum": []string{"content", "files_with_matches", "count"}, "description": "content = matching lines; files_with_matches = file paths (default); count = match counts per file"},
			"glob":        map[string]any{"type": "string", "description": "Filter files by glob pattern (e.g. \"*.html\")"},
			"type":        map[string]any{"type": "string", "description": "Filter by file type (e.g. \"html\", \"json\", \"js\")"},
			"-A":          map[string]any{"type": "integer", "description": "Lines to show after each match (output_mode: content)"},
			"-B":          map[string]any{"type": "integer", "description": "Lines to show before each match (output_mode: content)"},
			"-C":          map[string]any{"type": "integer", "description": "Lines to show before and after each match (output_mode: content)"},
			"-i":          map[string]any{"type": "boolean", "description": "Case-insensitive search"},
			"-n":          map[string]any{"type": "boolean", "description": "Show line numbers (output_mode: content)"},
			"multiline":   map[string]any{"type": "boolean", "description": "Allow the pattern to span lines (. matches newline)"},
			"head_limit":  map[string]any{"type": "integer", "description": "Limit output to the first N results"},
		},
		"required":             []string{"pattern"},
		"additionalProperties": false,
	})
}

func (a *Agent) grepEntry() toolEntry {
	return toolEntry{def: grepToolDef(), handler: a.grep}
}

func (a *Agent) grep(_ context.Context, rc *runContext, args map[string]any) (string, error) {
	pattern := argString(args, "pattern")
	if strings.TrimSpace(pattern) == "" {
		return "error: pattern is required", nil
	}
	rawPath := strings.TrimSpace(argString(args, "path"))
	if rawPath == "" {
		rawPath = "tmp://"
	}
	mode := strings.TrimSpace(argString(args, "output_mode"))
	if mode == "" {
		mode = "files_with_matches"
	}
	glob := strings.TrimSpace(argString(args, "glob"))
	ftype := strings.TrimSpace(argString(args, "type"))
	after, before := argInt(args, "-A", 0), argInt(args, "-B", 0)
	if c := argInt(args, "-C", 0); c > 0 {
		after, before = c, c
	}
	headLimit := argInt(args, "head_limit", 0)

	// Compile the pattern with the requested flags.
	var flags string
	if argBool(args, "-i", false) {
		flags += "i"
	}
	if argBool(args, "multiline", false) {
		flags += "s"
	}
	expr := pattern
	if flags != "" {
		expr = "(?" + flags + ")" + pattern
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return fmt.Sprintf("error: invalid pattern %q: %v", pattern, err), nil
	}

	root, scheme, rel, err := a.resolveNamespaceRoot(rc, rawPath)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	files, err := grepCandidateFiles(root, rel, glob, ftype)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	if len(files) == 0 {
		return "No files to search under " + rawPath, nil
	}

	showNums := argBool(args, "-n", false)
	var out []string
	matchedFiles := 0
	for _, f := range files {
		data, err := root.Read(f)
		if err != nil {
			continue
		}
		uri := scheme + f
		switch mode {
		case "files_with_matches":
			if re.Match(data) {
				out = append(out, uri)
			}
		case "count":
			if n := len(re.FindAllIndex(data, -1)); n > 0 {
				out = append(out, fmt.Sprintf("%s:%d", uri, n))
			}
		default: // content
			lines := grepContentLines(uri, string(data), re, before, after, showNums)
			if len(lines) > 0 {
				if matchedFiles > 0 {
					out = append(out, "--")
				}
				out = append(out, lines...)
				matchedFiles++
			}
		}
		if headLimit > 0 && mode != "content" && len(out) >= headLimit {
			out = out[:headLimit]
			break
		}
	}
	if mode == "content" && headLimit > 0 && len(out) > headLimit {
		out = out[:headLimit]
	}
	if len(out) == 0 {
		return fmt.Sprintf("No matches for %q in %s", pattern, rawPath), nil
	}
	res := strings.Join(out, "\n")
	if len(res) > grepResultCap {
		res = res[:grepResultCap] + "\n…[truncated — narrow with glob/type or a tighter pattern, or use head_limit]"
	}
	return res, nil
}

// resolveNamespaceRoot maps a namespace path to its sandbox Root, the scheme
// prefix to rebuild URIs, and the relative path within the root.
func (a *Agent) resolveNamespaceRoot(rc *runContext, p string) (*sandboxfs.Root, string, string, error) {
	switch {
	case strings.HasPrefix(p, "tmp://"):
		return a.tmpRoot(rc.userID), "tmp://", strings.TrimPrefix(p, "tmp://"), nil
	case strings.HasPrefix(p, "storage://"):
		return a.storageRoot(rc.userID), "storage://", strings.TrimPrefix(p, "storage://"), nil
	case !strings.Contains(p, "://"):
		// bare path defaults to the tmp namespace
		return a.tmpRoot(rc.userID), "tmp://", p, nil
	default:
		return nil, "", "", fmt.Errorf("unsupported path %q (use tmp:// or storage://)", p)
	}
}

// grepCandidateFiles returns the files under root to search: a single file if rel
// names one, otherwise every file under rel (or the whole root) passing the glob
// and type filters.
func grepCandidateFiles(root *sandboxfs.Root, rel, glob, ftype string) ([]string, error) {
	rel = strings.Trim(strings.TrimSpace(rel), "/")
	all, err := root.List("")
	if err != nil {
		return nil, err
	}
	// rel naming an exact file → just that file (List is the source of truth, so
	// this never mistakes a directory for a file).
	if rel != "" {
		for _, f := range all {
			if f == rel {
				return []string{rel}, nil
			}
		}
	}
	exts := typeExtensions(ftype)
	var out []string
	for _, f := range all {
		if rel != "" && !strings.HasPrefix(f, rel+"/") {
			continue
		}
		if glob != "" {
			if ok, _ := path.Match(glob, path.Base(f)); !ok {
				continue
			}
		}
		if len(exts) > 0 && !hasAnySuffix(f, exts) {
			continue
		}
		out = append(out, f)
	}
	sort.Strings(out)
	return out, nil
}

// grepContentLines renders ripgrep-style content output for one file: match lines
// as "uri:line:text" (or "uri:text" without numbers) and context lines with "-".
func grepContentLines(uri, content string, re *regexp.Regexp, before, after int, showNums bool) []string {
	lines := strings.Split(content, "\n")
	keep := make(map[int]bool)
	isMatch := make(map[int]bool)
	for i, ln := range lines {
		if re.MatchString(ln) {
			isMatch[i] = true
			for j := i - before; j <= i+after; j++ {
				if j >= 0 && j < len(lines) {
					keep[j] = true
				}
			}
		}
	}
	hasCtx := before > 0 || after > 0
	loc := func(sep string, i int, s string) string {
		if showNums {
			return fmt.Sprintf("%s%s%d%s%s", uri, sep, i+1, sep, s)
		}
		return fmt.Sprintf("%s%s%s", uri, sep, s)
	}
	var out []string
	prev := -2
	for i := 0; i < len(lines); i++ {
		if !keep[i] {
			continue
		}
		if hasCtx && prev >= 0 && i > prev+1 {
			out = append(out, "--")
		}
		if isMatch[i] {
			// Long lines (minified HTML) are windowed around each match so a
			// content match never dumps the whole document.
			for _, w := range matchWindows(lines[i], re) {
				out = append(out, loc(":", i, w))
			}
		} else {
			out = append(out, loc("-", i, truncRunes(lines[i], grepMaxLineWidth)))
		}
		prev = i
	}
	return out
}

// grep content windowing bounds: lines no longer than grepMaxLineWidth print
// whole; longer ones show grepWindowHalf chars each side of a match.
const (
	grepMaxLineWidth      = 240
	grepWindowHalf        = 120
	grepMaxWindowsPerLine = 50
)

// matchWindows returns the whole line if short, else a bounded window of text
// around each match (…before<match>after…), capped in count.
func matchWindows(line string, re *regexp.Regexp) []string {
	if len(line) <= grepMaxLineWidth {
		return []string{line}
	}
	var out []string
	for _, m := range re.FindAllStringIndex(line, -1) {
		s, e := m[0]-grepWindowHalf, m[1]+grepWindowHalf
		if s < 0 {
			s = 0
		}
		if e > len(line) {
			e = len(line)
		}
		seg := line[s:e]
		if s > 0 {
			seg = "…" + seg
		}
		if e < len(line) {
			seg = seg + "…"
		}
		out = append(out, seg)
		if len(out) >= grepMaxWindowsPerLine {
			break
		}
	}
	if len(out) == 0 {
		out = append(out, truncRunes(line, grepMaxLineWidth))
	}
	return out
}

// truncRunes truncates s to at most n bytes (rune-boundary safe) with an ellipsis.
func truncRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func typeExtensions(t string) []string {
	switch strings.ToLower(t) {
	case "":
		return nil
	case "html":
		return []string{".html", ".htm"}
	case "yaml":
		return []string{".yaml", ".yml"}
	case "md", "markdown":
		return []string{".md", ".markdown"}
	case "text", "txt":
		return []string{".txt"}
	default:
		return []string{"." + strings.ToLower(t)}
	}
}

func hasAnySuffix(s string, suffixes []string) bool {
	for _, suf := range suffixes {
		if strings.HasSuffix(s, suf) {
			return true
		}
	}
	return false
}

// argBool reads a boolean tool arg (JSON booleans and a few string forms).
func argBool(args map[string]any, key string, def bool) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes":
			return true
		case "false", "0", "no", "":
			return false
		}
	}
	return def
}
