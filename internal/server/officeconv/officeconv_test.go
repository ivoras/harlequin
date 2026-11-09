package officeconv

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestUnavailableWithoutSoffice(t *testing.T) {
	t.Setenv("PATH", "")
	if _, _, err := ToModern(context.Background(), "a.xls", []byte("x")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	if Handles("a.docx") || !Handles("A.XLS") || !Handles("b.ods") {
		t.Fatal("Handles")
	}
}

// Round-trips a real file through LibreOffice when it is installed.
func TestToModernRealSoffice(t *testing.T) {
	if _, err := exec.LookPath("soffice"); err != nil {
		t.Skip("soffice not installed")
	}
	csv := []byte("a,b\n1,2\n")
	xls, _, err := convert(t, "t.csv", csv, "xls")
	if err != nil {
		t.Skipf("cannot build an .xls fixture: %v", err)
	}
	out, name, err := ToModern(context.Background(), "Report.xls", xls)
	if err != nil || name != "Report.xlsx" || !bytes.HasPrefix(out, []byte("PK")) {
		t.Fatalf("name %q err %v", name, err)
	}
}

// convert runs soffice to build a fixture (test helper only).
func convert(t *testing.T, name string, data []byte, to string) ([]byte, string, error) {
	t.Helper()
	dir := t.TempDir()
	if err := writeFile(dir+"/"+name, data); err != nil {
		return nil, "", err
	}
	cmd := exec.Command("soffice", "-env:UserInstallation=file://"+dir+"/p", "--headless", "--convert-to", to, "--outdir", dir+"/o", dir+"/"+name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, "", errors.New(string(out))
	}
	b, err := readFile(dir + "/o/t." + to)
	return b, "t." + to, err
}

func writeFile(p string, b []byte) error { return os.WriteFile(p, b, 0o600) }
func readFile(p string) ([]byte, error)  { return os.ReadFile(p) }
