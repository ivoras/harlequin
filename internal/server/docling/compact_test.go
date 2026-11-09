package docling

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Conversions must ask docling for unpadded Markdown tables (md_compact_tables),
// in both fast and full mode.
func TestConvertRequestsCompactTables(t *testing.T) {
	for _, fast := range []bool{true, false} {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/v1/convert/file/async":
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
				}
				got = r.FormValue("md_compact_tables")
				w.Write([]byte(`{"task_id":"t1","task_status":"success"}`))
			case r.URL.Path == "/v1/result/t1":
				w.Write([]byte(`{"status":"success","document":{"md_content":"| a |\n| - |"}}`))
			default:
				http.NotFound(w, r)
			}
		}))
		c := New(srv.URL, 5*time.Second, fast)
		if _, err := c.Convert(context.Background(), "x.docx", []byte("data")); err != nil {
			t.Fatalf("fast=%v: %v", fast, err)
		}
		srv.Close()
		if got != "true" {
			t.Errorf("fast=%v: md_compact_tables = %q, want true", fast, got)
		}
	}
}
