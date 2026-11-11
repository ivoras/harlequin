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

// A registered fallback (vision OCR) runs before docling's own OCR retry, and
// its failure falls through to that retry.
func TestNoTextFallbackRunsBeforeOCRRetry(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			calls++
			r.ParseMultipartForm(1 << 20)
			id := "ocr"
			if r.FormValue("do_ocr") == "false" {
				id = "plain"
			}
			w.Write([]byte(`{"task_id":"` + id + `","task_status":"success"}`))
		case r.URL.Path == "/v1/result/ocr":
			w.Write([]byte(`{"status":"success","document":{"json_content":{"texts":[{"text":"docling ocr","prov":[{"page_no":1}]}],"pages":{"1":{}}}}}`))
		default:
			w.Write([]byte(`{"status":"success","document":{"json_content":{}}}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL, 5*time.Second, true)
	c.SetNoTextFallback(func(context.Context, string, []byte) (string, []int, error) { return "vision text", []int{0}, nil })
	text, _, err := c.ConvertPaged(context.Background(), "s.pdf", []byte("x"))
	if err != nil || text != "vision text" || calls != 1 {
		t.Fatalf("text=%q err=%v calls=%d, want vision text after only the fast call", text, err, calls)
	}
	c.SetNoTextFallback(func(context.Context, string, []byte) (string, []int, error) { return "", nil, context.DeadlineExceeded })
	if _, _, err := c.ConvertPaged(context.Background(), "s.pdf", []byte("x")); err != nil && calls != 3 {
		t.Fatalf("after vision failure: err=%v calls=%d, want the docling OCR retry (calls=3)", err, calls)
	}
}
