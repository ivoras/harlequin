package agent

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Pure text-editing primitives shared by the edit_file (sandbox files) and
// edit_doc (corpus documents) tools. Each returns the new text or an error that
// is written for the model: it says what went wrong and how to fix the call.

// textEditReplace replaces old with new. Without replaceAll, old must occur
// exactly once, so an edit can never silently hit the wrong spot.
func textEditReplace(text, old, new string, replaceAll bool) (string, int, error) {
	if old == "" {
		return "", 0, fmt.Errorf("old_string must not be empty (to add text use append)")
	}
	if old == new {
		return "", 0, fmt.Errorf("old_string and new_string are identical; nothing to change")
	}
	n := strings.Count(text, old)
	switch {
	case n == 0:
		return "", 0, fmt.Errorf("old_string was not found (matching is exact, including whitespace and line breaks); read the current text and copy the passage exactly")
	case n > 1 && !replaceAll:
		return "", 0, fmt.Errorf("old_string occurs %d times; include more surrounding text to make it unique, or set replace_all to replace every occurrence", n)
	}
	if replaceAll {
		return strings.ReplaceAll(text, old, new), n, nil
	}
	return strings.Replace(text, old, new, 1), 1, nil
}

// textEditAppend appends add to text. If the text does not end with a newline
// one is inserted first, so the addition starts on its own line.
func textEditAppend(text, add string) string {
	if add == "" {
		return text
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + add
}

var hunkHeaderRE = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

type diffHunk struct {
	oldStart int      // 1-based line in the old text; -1 when the header carried no numbers
	oldLines []string // context + removed lines
	newLines []string // context + added lines
	bareTail int      // trailing bare-empty lines (see parseUnifiedDiff)
}

// parseUnifiedDiff reads the hunks of a unified diff. File headers (---, +++,
// diff, index) before the first hunk are ignored. Line counts in the hunk
// headers are not trusted (models often get them wrong); the body decides.
func parseUnifiedDiff(diff string) ([]diffHunk, error) {
	var hunks []diffHunk
	var cur *diffHunk
	for _, line := range strings.Split(strings.ReplaceAll(diff, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "@@") {
			h := diffHunk{oldStart: -1}
			if m := hunkHeaderRE.FindStringSubmatch(line); m != nil {
				h.oldStart, _ = strconv.Atoi(m[1])
			}
			hunks = append(hunks, h)
			cur = &hunks[len(hunks)-1]
			continue
		}
		if cur == nil {
			continue // file header / preamble
		}
		switch {
		case strings.HasPrefix(line, "\\"): // "\ No newline at end of file"
		case strings.HasPrefix(line, "+"):
			cur.newLines = append(cur.newLines, line[1:])
			cur.bareTail = 0
		case strings.HasPrefix(line, "-"):
			cur.oldLines = append(cur.oldLines, line[1:])
			cur.bareTail = 0
		case strings.HasPrefix(line, " "):
			cur.oldLines = append(cur.oldLines, line[1:])
			cur.newLines = append(cur.newLines, line[1:])
			cur.bareTail = 0
		case line == "":
			// A blank context line whose leading space was stripped — or just a
			// separator / the diff's final newline; trailing ones are dropped below.
			cur.oldLines = append(cur.oldLines, "")
			cur.newLines = append(cur.newLines, "")
			cur.bareTail++
		default:
			return nil, fmt.Errorf("hunk %d: line %q does not start with ' ', '+' or '-'", len(hunks), truncateForErr(line))
		}
	}
	if len(hunks) == 0 {
		return nil, fmt.Errorf("no hunks found: the diff must contain at least one '@@ -a,b +c,d @@' section")
	}
	// Bare-empty lines at the end of a hunk are separators, not context.
	for i := range hunks {
		h := &hunks[i]
		h.oldLines = h.oldLines[:len(h.oldLines)-h.bareTail]
		h.newLines = h.newLines[:len(h.newLines)-h.bareTail]
	}
	return hunks, nil
}

func truncateForErr(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

// textEditApplyDiff applies a unified diff. Hunks are located by their content
// (the context and removed lines must match the text), using the header's line
// number only as a hint, so a diff made against slightly shifted text still
// applies. It is all-or-nothing: if any hunk fails, nothing is changed.
func textEditApplyDiff(text, diff string) (string, int, error) {
	hunks, err := parseUnifiedDiff(diff)
	if err != nil {
		return "", 0, err
	}
	trailingNL := strings.HasSuffix(text, "\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if text == "" {
		lines = nil
	}
	delta := 0  // cumulative growth from earlier hunks
	minPos := 0 // hunks must be in file order and not overlap
	for i, h := range hunks {
		n := i + 1
		var pos int
		if len(h.oldLines) == 0 {
			// Pure insertion: header "-N,0" means "after line N".
			switch {
			case h.oldStart >= 0:
				pos = min(h.oldStart+delta, len(lines))
			default:
				pos = len(lines)
			}
			pos = max(pos, minPos)
		} else {
			hint := h.oldStart - 1 + delta
			pos = findHunk(lines, h.oldLines, hint, minPos)
			if pos == -2 {
				return "", 0, fmt.Errorf("hunk %d matches several places; add more context lines or a correct line number in its @@ header", n)
			}
			if pos < 0 {
				return "", 0, fmt.Errorf("hunk %d does not match the text (the context and '-' lines must appear exactly as written, in order); re-read the current text and regenerate the diff", n)
			}
		}
		out := make([]string, 0, len(lines)-len(h.oldLines)+len(h.newLines))
		out = append(out, lines[:pos]...)
		out = append(out, h.newLines...)
		out = append(out, lines[pos+len(h.oldLines):]...)
		lines = out
		delta += len(h.newLines) - len(h.oldLines)
		minPos = pos + len(h.newLines)
	}
	res := strings.Join(lines, "\n")
	if trailingNL || (text == "" && len(lines) > 0) {
		res += "\n"
	}
	return res, len(hunks), nil
}

// findHunk returns the index in lines (>= minPos) where old matches, or -1 for
// none and -2 for an ambiguous match with no usable position hint. Exact
// matches win; failing that, trailing whitespace is ignored. With a hint the
// nearest match is chosen.
func findHunk(lines, old []string, hint, minPos int) int {
	for _, norm := range []func(string) string{func(s string) string { return s }, func(s string) string { return strings.TrimRight(s, " \t\r") }} {
		var found []int
		for p := minPos; p+len(old) <= len(lines); p++ {
			ok := true
			for j, o := range old {
				if norm(lines[p+j]) != norm(o) {
					ok = false
					break
				}
			}
			if ok {
				found = append(found, p)
			}
		}
		switch {
		case len(found) == 0:
			continue
		case len(found) == 1:
			return found[0]
		case hint < 0:
			return -2
		}
		best := found[0]
		for _, p := range found[1:] {
			if abs(p-hint) < abs(best-hint) {
				best = p
			}
		}
		// Equally good candidates on both sides of the hint are ambiguous.
		for _, p := range found {
			if p != best && abs(p-hint) == abs(best-hint) {
				return -2
			}
		}
		return best
	}
	return -1
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

var listItemRE = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s`)

// lineKind classifies a line as a Markdown table row, a list item or neither.
func lineKind(line string) string {
	switch {
	case strings.HasPrefix(strings.TrimSpace(line), "|"):
		return "table"
	case listItemRE.MatchString(line):
		return "list"
	}
	return ""
}

// textEditInsert adds ins after (or, with before, in front of) the passage
// anchor, which must occur exactly once. Normally ins becomes its own paragraph,
// placed beside the block (run of non-empty lines) holding the anchor and
// separated by one empty line; a single table row or list item next to a row or
// item of the same kind goes on the adjacent line instead. Other text is untouched.
func textEditInsert(text, ins, anchor string, before bool) (string, error) {
	name := "after"
	if before {
		name = "before"
	}
	ins = strings.Trim(ins, "\r\n")
	if ins == "" {
		return "", fmt.Errorf("insert must not be empty")
	}
	if strings.Trim(anchor, "\r\n") == "" {
		return "", fmt.Errorf("%s must name a passage of the document", name)
	}
	switch n := strings.Count(text, anchor); {
	case n == 0:
		return "", fmt.Errorf("%s was not found (matching is exact, including whitespace and line breaks); read the current text and copy the passage exactly", name)
	case n > 1:
		return "", fmt.Errorf("%s occurs %d times; include more surrounding text to make it unique", name, n)
	}
	idx := strings.Index(text, anchor)
	end := idx + len(strings.TrimRight(anchor, "\r\n"))
	// The line holding the start (before) or the end (after) of the anchor.
	pos := end
	if before {
		pos = idx
	}
	ls := strings.LastIndex(text[:pos], "\n") + 1
	le := len(text)
	if i := strings.IndexByte(text[pos:], '\n'); i >= 0 {
		le = pos + i
	}
	if k := lineKind(text[ls:le]); k != "" && !strings.Contains(ins, "\n") && lineKind(ins) == k {
		if before {
			return text[:ls] + ins + "\n" + text[ls:], nil
		}
		return text[:le] + "\n" + ins + text[le:], nil
	}
	blank := func(a, b int) bool { return strings.TrimSpace(text[a:b]) == "" }
	if before {
		for ls > 0 { // walk up to the block's first line
			ps := strings.LastIndex(text[:ls-1], "\n") + 1
			if blank(ps, ls-1) {
				break
			}
			ls = ps
		}
		return text[:ls] + ins + "\n\n" + text[ls:], nil
	}
	for le < len(text) { // walk down to the block's last line
		ne := len(text)
		if i := strings.IndexByte(text[le+1:], '\n'); i >= 0 {
			ne = le + 1 + i
		}
		if blank(le+1, ne) {
			break
		}
		le = ne
	}
	return text[:le] + "\n\n" + ins + text[le:], nil
}

// textEditOp is the shared, validated form of an edit request.
type textEditOp struct {
	oldString, newString string
	replaceAll           bool
	diff                 string
	appendText           string
	hasReplace, hasDiff  bool
	hasAppend            bool
	insertText, anchor   string
	insertBefore         bool
	hasInsert            bool
	cells                map[string]any // uploaded Excel: address -> value
	hasCells             bool
	sheet                string // uploaded Excel: default sheet
}

// parseTextEditOp reads exactly one operation (old_string/new_string, diff,
// append, append_source or insert with after/before) from tool args.
func parseTextEditOp(args map[string]any) (textEditOp, error) {
	var op textEditOp
	_, op.hasReplace = args["old_string"]
	op.oldString = argString(args, "old_string")
	op.newString = argString(args, "new_string")
	op.replaceAll = argBool(args, "replace_all", false)
	op.diff = argString(args, "diff")
	op.hasDiff = strings.TrimSpace(op.diff) != ""
	op.appendText = argString(args, "append")
	op.hasAppend = op.appendText != ""
	op.hasReplace = op.hasReplace && (op.oldString != "" || op.newString != "")
	op.insertText = argString(args, "insert")
	after, before := argString(args, "after"), argString(args, "before")
	if after != "" && before != "" {
		return op, fmt.Errorf("give either after or before with insert, not both")
	}
	op.anchor, op.insertBefore = after, false
	if before != "" {
		op.anchor, op.insertBefore = before, true
	}
	if (op.insertText != "") != (op.anchor != "") {
		if op.insertText == "" {
			return op, fmt.Errorf("after/before needs insert: the text to add")
		}
		return op, fmt.Errorf("insert needs exactly one of after or before: a passage quoted exactly from the document")
	}
	op.hasInsert = op.insertText != ""
	op.sheet = strings.TrimSpace(argString(args, "sheet"))
	if v, ok := args["cells"]; ok && v != nil {
		m, isMap := v.(map[string]any)
		if !isMap {
			return op, fmt.Errorf(`cells must be an object from cell address to value, for example {"B2": 1700, "October!C2": "=B2*0.73"}`)
		}
		op.cells, op.hasCells = m, true
	}
	n := 0
	for _, b := range []bool{op.hasReplace, op.hasDiff, op.hasAppend, op.hasInsert, op.hasCells} {
		if b {
			n++
		}
	}
	switch n {
	case 0:
		return op, fmt.Errorf("give exactly one operation: old_string+new_string (replace), diff (unified diff), append (text), append_source (documents/files), insert with after or before (add text next to a passage), or cells (set Excel cells)")
	case 1:
		return op, nil
	}
	return op, fmt.Errorf("give exactly one operation (old_string+new_string, diff, append, append_source, insert+after/before, or cells), not several")
}

// apply runs the operation on text and describes what it did.
func (op textEditOp) apply(text string) (string, string, error) {
	switch {
	case op.hasCells:
		return "", "", fmt.Errorf("cells only works on an uploaded Excel workbook (an .xlsx file saved as a document); for text use replace, insert or append")
	case op.hasReplace:
		out, n, err := textEditReplace(text, op.oldString, op.newString, op.replaceAll)
		if err != nil {
			return "", "", err
		}
		return out, fmt.Sprintf("replaced %d occurrence(s)", n), nil
	case op.hasDiff:
		out, n, err := textEditApplyDiff(text, op.diff)
		if err != nil {
			return "", "", err
		}
		return out, fmt.Sprintf("applied %d hunk(s)", n), nil
	case op.hasInsert:
		out, err := textEditInsert(text, op.insertText, op.anchor, op.insertBefore)
		if err != nil {
			return "", "", err
		}
		where := "after"
		if op.insertBefore {
			where = "before"
		}
		return out, fmt.Sprintf("inserted %d characters %s the given passage", len([]rune(op.insertText)), where), nil
	default:
		return textEditAppend(text, op.appendText), fmt.Sprintf("appended %d characters", len([]rune(op.appendText))), nil
	}
}
