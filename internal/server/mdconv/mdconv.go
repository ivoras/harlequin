// Package mdconv converts HTML, PPTX, XLSX, ODT, EPUB and RTF uploads to
// Markdown with pure Go (stdlib + html-to-markdown), so these formats ingest
// even when no docling/xberg converter is configured. When one is, the upload
// handler tries it first for the Office-like formats; this package is the
// fallback (and the only path for HTML). Output is text-faithful rather than
// layout-faithful: no images, no styling beyond headings, lists and tables.
package mdconv

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
)

// ErrUnsupported means the file extension is not handled here.
var ErrUnsupported = errors.New("mdconv: unsupported file type")

var errNotRTF = errors.New("not an RTF file")

// maxPart caps one decompressed archive member (zip-bomb guard).
const maxPart = 64 << 20

var conv = map[string]struct {
	mime string
	fn   func([]byte) (string, error)
}{
	".html": {"text/html", htmlMD},
	".htm":  {"text/html", htmlMD},
	".pptx": {"application/vnd.openxmlformats-officedocument.presentationml.presentation", pptxMD},
	".xlsx": {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", xlsxMD},
	".odt":  {"application/vnd.oasis.opendocument.text", odtMD},
	".epub": {"application/epub+zip", epubMD},
	".rtf":  {"application/rtf", rtfMD},
}

// Mime returns the MIME type for name's extension, or "" when unsupported.
func Mime(name string) string { return conv[strings.ToLower(filepath.Ext(name))].mime }

// Convert renders data (named name, which selects the format) as Markdown.
func Convert(name string, data []byte) (string, error) {
	c, ok := conv[strings.ToLower(filepath.Ext(name))]
	if !ok {
		return "", ErrUnsupported
	}
	md, err := c.fn(data)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(md) == "" {
		return "", errors.New("no extractable text in the file")
	}
	return md, nil
}

func htmlMD(data []byte) (string, error) {
	md, err := htmltomarkdown.ConvertString(string(data))
	return strings.TrimSpace(strings.ReplaceAll(md, "­", "")), err
}

func openZip(data []byte) (map[string]*zip.File, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("not a zip archive: %w", err)
	}
	m := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		m[f.Name] = f
	}
	return m, nil
}

func readPart(z map[string]*zip.File, name string) ([]byte, error) {
	f := z[name]
	if f == nil {
		return nil, fmt.Errorf("missing %s", name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, maxPart))
}

// mdTable renders rows as a pipe table, first row as the header. Rows are
// padded to the widest; "|" and newlines in cells are escaped.
func mdTable(rows [][]string) string {
	w := 0
	for _, r := range rows {
		w = max(w, len(r))
	}
	if w == 0 {
		return ""
	}
	var sb strings.Builder
	line := func(r []string) {
		sb.WriteByte('|')
		for i := 0; i < w; i++ {
			c := ""
			if i < len(r) {
				c = strings.NewReplacer("|", `\|`, "\r", "", "\n", " ").Replace(strings.TrimSpace(r[i]))
			}
			sb.WriteString(" " + c + " |")
		}
		sb.WriteByte('\n')
	}
	line(rows[0])
	sb.WriteString("|" + strings.Repeat(" --- |", w) + "\n")
	for _, r := range rows[1:] {
		line(r)
	}
	return sb.String()
}
