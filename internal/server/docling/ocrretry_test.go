package docling

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Fast mode (no OCR) finds no text in a scanned PDF; Convert must retry once
// with OCR on, and not need a retry when fast is already off.
func TestConvertRetriesWithOCRWhenFastFindsNoText(t *testing.T) {
	for _, fast := range []bool{true, false} {
		var calls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost:
				calls++
				r.ParseMultipartForm(1 << 20)
				ocrOff := r.FormValue("do_ocr") == "false"
				id := "ocr"
				if ocrOff {
					id = "plain"
				}
				w.Write([]byte(`{"task_id":"` + id + `","task_status":"success"}`))
			case r.URL.Path == "/v1/result/ocr":
				w.Write([]byte(`{"status":"success","document":{"md_content":"scanned text"}}`))
			case r.URL.Path == "/v1/result/plain":
				w.Write([]byte(`{"status":"success","document":{"md_content":""}}`))
			default:
				http.NotFound(w, r)
			}
		}))
		c := New(srv.URL, 5*time.Second, fast)
		md, err := c.Convert(context.Background(), "scan.pdf", []byte("x"))
		srv.Close()
		if fast && (err != nil || md != "scanned text" || calls != 2) {
			t.Errorf("fast: md=%q err=%v calls=%d, want OCR retry", md, err, calls)
		}
		if !fast && (err != nil || md != "scanned text" || calls != 1) {
			t.Errorf("non-fast: md=%q err=%v calls=%d, want a single OCR call", md, err, calls)
		}
	}
}
