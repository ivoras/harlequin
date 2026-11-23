package agent

import (
	"strings"
	"testing"
)

// create_html makes a document and an editable file; every create_* tool takes
// the same save_to/overwrite/scope parameters and means the same by them.
func TestCreateHTMLAndSharedParams(t *testing.T) {
	h := newGenHarness(t)

	id, stored := h.create("create_html", "Notes", "# Notes\n\nPay in **30 days**.\n\n<script>alert(1)</script>")
	page := string(readFile(t, stored))
	if !strings.HasSuffix(stored, ".html") || !strings.Contains(page, "<strong>30 days</strong>") || strings.Contains(page, "alert(1)") {
		t.Fatalf("html page: stored %s\n%s", stored, page)
	}
	var mime string
	h.db.QueryRow(`SELECT mime FROM documents WHERE id = ?`, id).Scan(&mime)
	if mime != "text/html" {
		t.Fatalf("mime = %q", mime)
	}
	// Like the other generated files, edit_doc edits its source and rebuilds it.
	if out := h.call("edit_doc", map[string]any{"doc": ref(id), "old_string": "30 days", "new_string": "14 days"}); strings.HasPrefix(out, "error") {
		t.Fatal(out)
	}
	if page = string(readFile(t, stored)); !strings.Contains(page, "14 days") || strings.Contains(page, "30 days") {
		t.Fatalf("edit did not rebuild the page:\n%s", page)
	}
	if out := h.call("edit_doc", map[string]any{"doc": ref(id), "undo": true}); !strings.Contains(out, "Restored") {
		t.Fatal(out)
	}
	if !strings.Contains(string(readFile(t, stored)), "30 days") {
		t.Fatal("undo did not restore the page")
	}

	// save_to: the same on every create_* tool — a temporary file, no document.
	content := "# T\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"
	for tool, ext := range map[string]string{"create_html": ".html", "create_excel": ".xlsx"} {
		path := "tmp://made" + ext
		out := h.call(tool, map[string]any{"title": "Made", "content": content, "save_to": path})
		if !strings.Contains(out, "written to "+path) || !strings.Contains(out, "download link") {
			t.Fatalf("%s save_to: %s", tool, out)
		}
		if again := h.call(tool, map[string]any{"title": "Made", "content": content, "save_to": path}); !strings.Contains(again, "already exists") {
			t.Fatalf("%s overwrite guard: %s", tool, again)
		}
		if ok := h.call(tool, map[string]any{"title": "Made", "content": content, "save_to": path, "overwrite": true}); !strings.Contains(ok, "written to") {
			t.Fatalf("%s overwrite: %s", tool, ok)
		}
		if bad := h.call(tool, map[string]any{"title": "Made", "content": content, "save_to": "https://x/y"}); !strings.HasPrefix(bad, "error: save_to must be a sandbox file") {
			t.Fatalf("%s bad save_to: %s", tool, bad)
		}
	}
	var n int
	h.db.QueryRow(`SELECT COUNT(*) FROM documents WHERE title LIKE 'Made%'`).Scan(&n)
	if n != 0 {
		t.Fatalf("save_to created %d documents", n)
	}

	// The parameter sets agree: all four take the shared ones; format/landscape only where they apply.
	tools := h.a.buildTools(h.ctx, h.rc)
	for _, tool := range []string{"create_pdf", "create_word", "create_excel", "create_html"} {
		props := tools[tool].def.Function.Parameters["properties"].(map[string]any)
		for _, k := range []string{"title", "content", "source", "scope", "save_to", "overwrite"} {
			if _, ok := props[k]; !ok {
				t.Errorf("%s lacks %s", tool, k)
			}
		}
	}
}
