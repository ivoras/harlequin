package sandboxfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadList(t *testing.T) {
	r := New(t.TempDir(), Options{})
	if err := r.Write("fzoeu/parser.js", []byte("print(1)")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	b, err := r.Read("fzoeu/parser.js")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(b) != "print(1)" {
		t.Fatalf("got %q", b)
	}
	ok, _ := r.Exists("fzoeu/parser.js")
	if !ok {
		t.Fatal("Exists should be true")
	}
	files, err := r.List("fzoeu/*.js")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(files) != 1 || files[0] != "fzoeu/parser.js" {
		t.Fatalf("List = %v", files)
	}
	if err := r.Remove("fzoeu/parser.js"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if ok, _ := r.Exists("fzoeu/parser.js"); ok {
		t.Fatal("file should be gone")
	}
}

func TestTraversalBlocked(t *testing.T) {
	base := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	// Plant a secret outside the sandbox.
	secret := filepath.Join(filepath.Dir(base), "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(base, Options{})
	for _, name := range []string{"../secret.txt", "../../secret.txt", "/etc/passwd", "a/../../secret.txt"} {
		if b, err := r.Read(name); err == nil {
			t.Fatalf("Read(%q) escaped sandbox, got %q", name, b)
		}
	}
	// A traversal write must not create files outside the base.
	if err := r.Write("../escape.txt", []byte("x")); err == nil {
		if _, statErr := os.Stat(filepath.Join(filepath.Dir(base), "escape.txt")); statErr == nil {
			t.Fatal("write escaped the sandbox")
		}
	}
}

func TestSymlinkEscapeBlocked(t *testing.T) {
	base := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(base), "outside.txt")
	if err := os.WriteFile(outside, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the root pointing out must not be readable through it.
	if err := os.Symlink(outside, filepath.Join(base, "link.txt")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	r := New(base, Options{})
	if _, err := r.Read("link.txt"); err == nil {
		t.Fatal("symlink escape was allowed")
	}
}

func TestQuotas(t *testing.T) {
	r := New(t.TempDir(), Options{MaxFileBytes: 8, MaxFiles: 2, MaxTotalBytes: 12})
	if err := r.Write("a", []byte("123456789")); err == nil {
		t.Fatal("per-file cap not enforced")
	}
	if err := r.Write("a", []byte("12345")); err != nil {
		t.Fatalf("a: %v", err)
	}
	if err := r.Write("b", []byte("12345")); err != nil {
		t.Fatalf("b: %v", err)
	}
	// Third distinct file exceeds MaxFiles.
	if err := r.Write("c", []byte("1")); err == nil {
		t.Fatal("file-count cap not enforced")
	}
	// Overwriting an existing file is fine even at the file-count cap.
	if err := r.Write("a", []byte("99")); err != nil {
		t.Fatalf("overwrite a: %v", err)
	}
}
