package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ivoras/harlequin/internal/server/sheetdb"
	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/charmap"
)

// formatBook is one ledger: a text account code, a numeric one shown with a
// zero-padding format, a date column and amounts.
func formatBook(t *testing.T) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	sh := "Knjiga"
	_ = f.SetSheetName("Sheet1", sh)
	pad, _ := f.NewStyle(&excelize.Style{CustomNumFmt: ptrTo("00000000")})
	date, _ := f.NewStyle(&excelize.Style{NumFmt: 14})
	_ = f.SetSheetRow(sh, "A1", &[]any{"Konto", "Opis, stavka", "Datum", "Iznos"})
	_ = f.SetSheetRow(sh, "A2", &[]any{"01270000", "Licenca čžš", time.Date(2026, 1, 21, 0, 0, 0, 0, time.UTC), 1234.5})
	_ = f.SetSheetRow(sh, "A3", &[]any{1710000, "Najam", time.Date(2026, 1, 30, 0, 0, 0, 0, time.UTC), 2150.25})
	_ = f.SetSheetRow(sh, "A4", &[]any{"01890000", "Oprema", time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC), 870})
	_ = f.SetCellStyle(sh, "A3", "A3", pad)
	_ = f.SetCellStyle(sh, "C2", "C4", date)
	b, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func ptrTo[T any](v T) *T { return &v }

// soffice converts the workbook to a legacy format, as a user's Excel or
// LibreOffice would have saved it.
func soffice(t *testing.T, xlsx []byte, format string) []byte {
	t.Helper()
	if _, err := exec.LookPath("soffice"); err != nil {
		t.Skip("soffice not installed")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "knjiga.xlsx")
	if err := os.WriteFile(in, xlsx, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("soffice", "--headless", "-env:UserInstallation=file://"+filepath.Join(dir, "profile"), "--convert-to", format, "--outdir", dir, in)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("soffice: %v: %s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "knjiga."+format))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Every spreadsheet format reads the same: the same column types, the same
// values, account codes with their leading zeros, ISO dates, numeric amounts.
func TestSheetFormatsReadTheSame(t *testing.T) {
	xlsx := formatBook(t)
	csvText := "Konto;\"Opis, stavka\";Datum;Iznos\n01270000;Licenca čžš;21.01.2026.;1.234,50\n01710000;Najam;30.01.2026.;2.150,25\n01890000;Oprema;03.02.2026.;870,00\n"
	cp1250, err := charmap.Windows1250.NewEncoder().String(csvText)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"knjiga.xlsx": xlsx,
		"knjiga.xls":  soffice(t, xlsx, "xls"),
		"knjiga.ods":  soffice(t, xlsx, "ods"),
		"knjiga.csv":  []byte(cp1250),
		"knjiga.tsv":  []byte("Konto\tOpis, stavka\tDatum\tIznos\n01270000\tLicenca čžš\t2026-01-21\t1234.5\n01710000\tNajam\t2026-01-30\t2150.25\n01890000\tOprema\t2026-02-03\t870\n"),
	}
	a, rc := sheetTestAgent(t)
	want := "| konto | opis_stavka | datum | iznos |\n|---|---|---|---|\n" +
		"| 01270000 | Licenca čžš | 2026-01-21 | 1234.5 |\n| 01710000 | Najam | 2026-01-30 | 2150.25 |\n| 01890000 | Oprema | 2026-02-03 | 870 |\n"
	for name, data := range files {
		t.Run(name, func(t *testing.T) {
			if err := a.tmpRoot(1).Write(name, data); err != nil {
				t.Fatal(err)
			}
			run := func(args map[string]any) string {
				out, err := a.querySheetEntry().handler(context.Background(), rc, args)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			schema := run(map[string]any{"doc": "tmp://" + name})
			for _, w := range []string{"TABLE knjiga", "3 data rows", "konto        code", "datum        date", "iznos        number", "opis_stavka  text"} {
				if !strings.Contains(schema, w) {
					t.Errorf("schema lacks %q:\n%s", w, schema)
				}
			}
			out := run(map[string]any{"doc": "tmp://" + name, "sql": "SELECT konto, opis_stavka, datum, iznos FROM knjiga ORDER BY konto"})
			if !strings.Contains(out, want) {
				t.Errorf("rows:\n%s\nwant:\n%s", out, want)
			}
			out = run(map[string]any{"doc": "tmp://" + name, "sql": "SELECT SUM(iznos), COUNT(*) FROM knjiga WHERE datum < '2026-02-01' AND konto LIKE '01%'"})
			if !strings.Contains(out, "| 3384.75 | 2 |") {
				t.Errorf("sum:\n%s", out)
			}
		})
	}
}

func TestSheetDetect(t *testing.T) {
	zipWith := func(name, body string) []byte {
		var b bytes.Buffer
		w := zip.NewWriter(&b)
		f, _ := w.Create(name)
		_, _ = f.Write([]byte(body))
		_ = w.Close()
		return b.Bytes()
	}
	ole := []byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1rest")
	for _, c := range []struct {
		name, mime, want string
		data             []byte
	}{
		{"a.xlsx", "", "xlsx", formatBook(t)},
		{"stored.bin", "", "xlsx", formatBook(t)}, // content wins over the name
		{"a.xlsb", "", "xlsb", zipWith("xl/workbook.bin", "")},
		{"a.ods", "", "ods", zipWith("mimetype", "application/vnd.oasis.opendocument.spreadsheet")},
		{"a.odt", "", "", zipWith("mimetype", "application/vnd.oasis.opendocument.text")},
		{"a.docx", "", "", zipWith("word/document.xml", "")},
		{"a.xls", "", "xls", ole},
		{"a.doc", "", "", ole},
		{"a.csv", "", "csv", []byte("a,b\n")},
		{"x", "text/csv", "csv", []byte("a,b\n")},
		{"a.tsv", "", "tsv", []byte("a\tb\n")},
		{"a.txt", "", "", []byte("hello")},
	} {
		if got := sheetdb.Detect(c.name, c.mime, c.data); got != c.want {
			t.Errorf("Detect(%s, %q) = %q, want %q", c.name, c.mime, got, c.want)
		}
	}
}
