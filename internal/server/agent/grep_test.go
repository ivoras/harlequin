package agent

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/sandboxfs"
)

func TestGrepContentLines(t *testing.T) {
	t.Parallel()
	content := "alpha\nBETA price 9\ngamma\ndelta price 5\nepsilon"
	re := regexp.MustCompile("price")
	// match lines only, with line numbers
	got := grepContentLines("tmp://f.txt", content, re, 0, 0, true)
	want := []string{"tmp://f.txt:2:BETA price 9", "tmp://f.txt:4:delta price 5"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v want %v", got, want)
	}
	// with -B1 context before each match, context lines use "-". Here the kept
	// lines 1-4 are contiguous, so no "--" gap separator appears.
	got = grepContentLines("tmp://f.txt", content, re, 1, 0, false)
	want = []string{"tmp://f.txt-alpha", "tmp://f.txt:BETA price 9", "tmp://f.txt-gamma", "tmp://f.txt:delta price 5"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("ctx got %v want %v", got, want)
	}
	// a gap between matches yields a "--" separator under context.
	got = grepContentLines("tmp://f.txt", "m1\nx\nx\nx\nm2", regexp.MustCompile("m[12]"), 1, 0, false)
	if strings.Join(got, "|") != "tmp://f.txt:m1|--|tmp://f.txt-x|tmp://f.txt:m2" {
		t.Fatalf("gap got %v", got)
	}
}

func TestGrepCandidateFiles(t *testing.T) {
	t.Parallel()
	root := sandboxfs.New(t.TempDir(), sandboxfs.Options{})
	for _, n := range []string{"a.html", "b.html", "c.json", "sub/d.html"} {
		if err := root.Write(n, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	// whole root, glob *.html
	got, _ := grepCandidateFiles(root, "", "*.html", "")
	if len(got) != 3 { // a, b, sub/d (glob matches basename)
		t.Fatalf("glob *.html got %v", got)
	}
	// type json
	got, _ = grepCandidateFiles(root, "", "", "json")
	if len(got) != 1 || got[0] != "c.json" {
		t.Fatalf("type json got %v", got)
	}
	// a specific file
	got, _ = grepCandidateFiles(root, "a.html", "", "")
	if len(got) != 1 || got[0] != "a.html" {
		t.Fatalf("file got %v", got)
	}
	// a subdirectory
	got, _ = grepCandidateFiles(root, "sub", "", "")
	if len(got) != 1 || got[0] != "sub/d.html" {
		t.Fatalf("subdir got %v", got)
	}
}

func TestArgBool(t *testing.T) {
	t.Parallel()
	if !argBool(map[string]any{"-i": true}, "-i", false) {
		t.Fatal("bool true")
	}
	if !argBool(map[string]any{"-i": "yes"}, "-i", false) {
		t.Fatal("string yes")
	}
	if argBool(map[string]any{}, "-i", false) {
		t.Fatal("default false")
	}
}
