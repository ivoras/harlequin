package db

import (
	"os"
	"testing"
)

// The 0015/0008/0007 text-mimes migration retypes XML/CSV/TSV uploads that
// were stored as text/plain, and nothing else.
func TestTextMimesMigration(t *testing.T) {
	for role, file := range map[Role]string{User: "migrations/user/0015_text_mimes.sql", Shared: "migrations/shared/0008_text_mimes.sql", Project: "migrations/project/0007_text_mimes.sql"} {
		d, err := Open(":memory:", role, 4)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range [][2]string{{"un-020.XML", "text/plain"}, {"izvoz.csv", "text/plain"}, {"a.tsv", "text/plain"}, {"notes.txt", "text/plain"}, {"x.xml", "application/pdf"}} {
			if _, err := d.Exec(`INSERT INTO documents(title, original_name, mime) VALUES (?, ?, ?)`, r[0], r[0], r[1]); err != nil {
				t.Fatal(err)
			}
		}
		sqlText, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(string(sqlText)); err != nil { // already applied at Open; it is idempotent
			t.Fatal(err)
		}
		want := map[string]string{"un-020.XML": "text/xml", "izvoz.csv": "text/csv", "a.tsv": "text/tab-separated-values", "notes.txt": "text/plain", "x.xml": "application/pdf"}
		rows, _ := d.Query(`SELECT original_name, mime FROM documents`)
		for rows.Next() {
			var n, m string
			_ = rows.Scan(&n, &m)
			if want[n] != m {
				t.Errorf("%s: %s is %s, want %s", role, n, m, want[n])
			}
		}
		rows.Close()
		d.Close()
	}
}
