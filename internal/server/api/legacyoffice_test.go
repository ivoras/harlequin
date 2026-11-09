package api

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

type fakeConv struct {
	md  string
	err error
	got string
}

func (f *fakeConv) Name() string                 { return "fake" }
func (f *fakeConv) Healthy(context.Context) bool { return true }
func (f *fakeConv) ConvertPaged(context.Context, string, []byte) (string, []int, error) {
	return "", nil, errors.New("unused")
}
func (f *fakeConv) Convert(_ context.Context, name string, _ []byte) (string, error) {
	f.got = name
	return f.md, f.err
}

func TestLegacyOfficeUploads(t *testing.T) {
	ole := []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1} // binary, not valid UTF-8 text
	for _, name := range []string{"a.doc", "b.XLS", "c.ppt", "d.ods"} {
		in := uploadInput{data: ole, filename: name, title: name, scope: "personal"}

		// No converter configured: a clear 415 that says what to do.
		if _, status, msg := (&Server{}).extractDocument(context.Background(), in); status != http.StatusUnsupportedMediaType || msg == "" {
			t.Errorf("%s without converter: status %d msg %q", name, status, msg)
		}

		// Converter available: its Markdown becomes the document content.
		fc := &fakeConv{md: "| a | b |"}
		req, status, msg := (&Server{Converter: fc}).extractDocument(context.Background(), in)
		if status != 0 || msg != "" || req.Content != "| a | b |" || fc.got != name || req.Mime == "" {
			t.Errorf("%s with converter: status %d msg %q req %+v", name, status, msg, req)
		}

		// Converter failure or empty output is an error, not an empty document.
		if _, status, _ = (&Server{Converter: &fakeConv{err: errors.New("boom")}}).extractDocument(context.Background(), in); status != http.StatusUnprocessableEntity {
			t.Errorf("%s converter error: status %d", name, status)
		}
		if _, status, _ = (&Server{Converter: &fakeConv{md: "  "}}).extractDocument(context.Background(), in); status != http.StatusUnprocessableEntity {
			t.Errorf("%s empty output: status %d", name, status)
		}
	}
}
