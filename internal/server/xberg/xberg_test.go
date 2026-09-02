package xberg

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// capturedRequest is what the fake server saw: the uploaded file plus the
// decoded extraction config.
type capturedRequest struct {
	filename string
	fileData []byte
	config   map[string]any
}

// fakeServer stands in for an xberg server. It records each /extract request
// and replies with the supplied document payload.
func fakeServer(t *testing.T, doc map[string]any) (*httptest.Server, *capturedRequest) {
	t.Helper()
	var got capturedRequest
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/extract", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f, hdr, err := r.FormFile("files")
		if err != nil {
			t.Errorf("form file %q: %v", "files", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer f.Close()
		got.filename = hdr.Filename
		got.fileData, _ = io.ReadAll(f)
		if raw := r.FormValue("config"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &got.config); err != nil {
				t.Errorf("config is not valid JSON: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"results": []any{doc}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestConvert(t *testing.T) {
	srv, got := fakeServer(t, map[string]any{"content": "# Title\n\nBody text."})
	c := New(srv.URL, 0, true)

	md, err := c.Convert(context.Background(), "report.docx", []byte("DOCXBYTES"))
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if md != "# Title\n\nBody text." {
		t.Errorf("content = %q", md)
	}
	if got.filename != "report.docx" {
		t.Errorf("filename = %q, want report.docx", got.filename)
	}
	if string(got.fileData) != "DOCXBYTES" {
		t.Errorf("file data = %q", got.fileData)
	}
	if got.config["output_format"] != "markdown" {
		t.Errorf("output_format = %v, want markdown", got.config["output_format"])
	}
	// Unpaged conversions must not request page markers — they would leak into
	// the stored text with nothing to strip them.
	if _, ok := got.config["pages"]; ok {
		t.Errorf("unpaged conversion requested page config: %v", got.config["pages"])
	}
}

// The fast flag turns OCR off and layout detection stays out of the payload;
// clearing it asks for layout-driven Markdown instead.
func TestConvertConfigFastAndQuality(t *testing.T) {
	for _, tc := range []struct {
		name       string
		fast       bool
		wantOCROff bool
		wantLayout bool
	}{
		{name: "fast", fast: true, wantOCROff: true, wantLayout: false},
		{name: "quality", fast: false, wantOCROff: false, wantLayout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, got := fakeServer(t, map[string]any{"content": "text"})
			if _, err := New(srv.URL, 0, tc.fast).Convert(context.Background(), "a.pdf", []byte("x")); err != nil {
				t.Fatalf("Convert: %v", err)
			}
			if off, _ := got.config["disable_ocr"].(bool); off != tc.wantOCROff {
				t.Errorf("disable_ocr = %v, want %v", got.config["disable_ocr"], tc.wantOCROff)
			}
			_, hasLayout := got.config["layout"]
			if hasLayout != tc.wantLayout {
				t.Errorf("layout present = %v, want %v", hasLayout, tc.wantLayout)
			}
			if use, _ := got.config["use_layout_for_markdown"].(bool); use != tc.wantLayout {
				t.Errorf("use_layout_for_markdown = %v, want %v", use, tc.wantLayout)
			}
		})
	}
}

func TestConvertPagedFromMarkers(t *testing.T) {
	content := marker(1) + "Page one." + marker(2) + "Page two."
	srv, got := fakeServer(t, map[string]any{"content": content})
	c := New(srv.URL, 0, true)

	text, starts, err := c.ConvertPaged(context.Background(), "doc.pdf", []byte("%PDF-x"))
	if err != nil {
		t.Fatalf("ConvertPaged: %v", err)
	}
	if strings.Contains(text, markerPrefix) {
		t.Errorf("marker leaked into text: %q", text)
	}
	if len(starts) != 2 {
		t.Fatalf("got %d page starts, want 2", len(starts))
	}
	checkStarts(t, text, starts, map[int]string{1: "Page one.", 2: "Page two."})

	pages, ok := got.config["pages"].(map[string]any)
	if !ok {
		t.Fatalf("paged conversion sent no page config: %v", got.config)
	}
	if insert, _ := pages["insert_page_markers"].(bool); !insert {
		t.Error("insert_page_markers not requested")
	}
	if pages["marker_format"] != markerFormat {
		t.Errorf("marker_format = %v, want %q", pages["marker_format"], markerFormat)
	}
}

// When markers do not survive the render, the byte boundaries in metadata carry
// the mapping instead.
func TestConvertPagedFallsBackToBoundaries(t *testing.T) {
	content := "Page one.\n\nPage two."
	srv, _ := fakeServer(t, map[string]any{
		"content": content,
		"metadata": map[string]any{
			"pages": map[string]any{
				"total_count": 2,
				"unit_type":   "page",
				"boundaries": []any{
					map[string]any{"byte_start": 0, "byte_end": 9, "page_number": 1},
					map[string]any{"byte_start": 11, "byte_end": 20, "page_number": 2},
				},
			},
		},
	})

	text, starts, err := New(srv.URL, 0, true).ConvertPaged(context.Background(), "doc.pdf", []byte("%PDF-x"))
	if err != nil {
		t.Fatalf("ConvertPaged: %v", err)
	}
	if text != content {
		t.Errorf("text = %q, want %q", text, content)
	}
	if len(starts) != 2 {
		t.Fatalf("got %d page starts, want 2", len(starts))
	}
	checkStarts(t, text, starts, map[int]string{1: "Page one.", 2: "Page two."})
}

// Neither markers nor boundaries: keep the text, drop the page citations.
func TestConvertPagedNoMapping(t *testing.T) {
	srv, _ := fakeServer(t, map[string]any{"content": "Unpaged body."})
	text, starts, err := New(srv.URL, 0, true).ConvertPaged(context.Background(), "doc.pdf", []byte("%PDF-x"))
	if err != nil {
		t.Fatalf("ConvertPaged: %v", err)
	}
	if text != "Unpaged body." {
		t.Errorf("text = %q", text)
	}
	if starts != nil {
		t.Errorf("got page starts %v, want nil", starts)
	}
}

func TestConvertEmptyResult(t *testing.T) {
	srv, _ := fakeServer(t, map[string]any{"content": "   \n  "})
	if _, err := New(srv.URL, 0, true).Convert(context.Background(), "a.pdf", []byte("x")); err == nil {
		t.Error("expected an error for whitespace-only content")
	}
}

func TestConvertServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"unsupported format"}`, http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	_, err := New(srv.URL, 0, true).Convert(context.Background(), "a.xyz", []byte("x"))
	if err == nil {
		t.Fatal("expected an error")
	}
	// The server's own message must reach the log, not just a status code.
	if !strings.Contains(err.Error(), "unsupported format") {
		t.Errorf("error lost the server message: %v", err)
	}
}

func TestHealthy(t *testing.T) {
	srv, _ := fakeServer(t, map[string]any{"content": "x"})
	if !New(srv.URL, 0, true).Healthy(context.Background()) {
		t.Error("Healthy = false for a live server")
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()
	if New(down.URL, 0, true).Healthy(context.Background()) {
		t.Error("Healthy = true for a server returning 503")
	}
}
