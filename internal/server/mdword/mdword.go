// Package mdword turns Markdown or HTML into a DOCX: the source is sanitised
// by mdpdf (same hostile-input rules), written as a plain HTML page, and
// converted by headless LibreOffice inside a bubblewrap sandbox with no
// network, an empty environment and a scratch directory as its only writable
// place.
package mdword

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/server/mdpdf"
)

// Binary is the executable looked up on PATH when none is configured.
const Binary = "soffice"

const (
	convertTimeout = 90 * time.Second
	maxDOCX        = 200 << 20
)

// LibreOffice's HTML import ignores CSS borders on cells and class-based page
// breaks, so those are spelled the way its filter reads them: table attributes
// and an inline page-break style. Its default margins for an HTML import are
// lopsided (2 cm left, 1 cm elsewhere).
const baseCSS = `@page{margin:2cm}`

var (
	pageBreakRE = regexp.MustCompile(`<div class="pagebreak"></div>`)
	tableRE     = regexp.MustCompile(`<table\b[^>]*>`)
)

// tableAttrs gives a table visible cell borders unless the source set its own.
func tableAttrs(tag string) string {
	if strings.Contains(tag, " border=") {
		return tag
	}
	return tag[:len(tag)-1] + ` border="1" cellspacing="0" cellpadding="4">`
}

// Page builds the HTML that LibreOffice converts, and the plain-Markdown text
// of src for indexing. Stats are mdpdf's (images and active content removed).
func Page(title, src string, isHTML bool) (page, text string, st mdpdf.Stats, err error) {
	body, styles, text, st, err := mdpdf.Body(src, isHTML)
	if err != nil {
		return "", "", st, err
	}
	body = pageBreakRE.ReplaceAllString(body, `<p style="page-break-before:always"></p>`)
	body = tableRE.ReplaceAllStringFunc(body, tableAttrs)
	return "<!DOCTYPE html>\n<html><head><meta charset=\"utf-8\"><title>" + html.EscapeString(title) +
		"</title><style>" + baseCSS + "</style><style>" + styles + "</style></head><body>\n" + body + "</body></html>\n", text, st, nil
}

// Renderer converts HTML pages to DOCX with sandboxed headless LibreOffice.
type Renderer struct {
	soffice, bwrap string
	sem            chan struct{} // LibreOffice is heavy: one conversion at a time
}

// New locates LibreOffice (the configured path, else Binary on PATH) and
// bubblewrap. There is deliberately no way to run without the sandbox.
func New(soffice string) (*Renderer, error) {
	if soffice == "" {
		soffice = Binary
	}
	path, err := exec.LookPath(soffice)
	if err != nil {
		return nil, fmt.Errorf("LibreOffice not found (%s): %w", soffice, err)
	}
	if path, err = filepath.EvalSymlinks(path); err != nil {
		return nil, err
	}
	if path, err = filepath.Abs(path); err != nil {
		return nil, err
	}
	// The sandbox shows LibreOffice /usr and nothing else of the host, so an
	// install elsewhere would start here and then fail on every conversion.
	if !strings.HasPrefix(path, "/usr/") {
		return nil, fmt.Errorf("LibreOffice at %s is outside /usr, which is all the sandbox mounts; use the distribution's package", path)
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("bubblewrap (bwrap) not found, and LibreOffice is never run unsandboxed: %w", err)
	}
	return &Renderer{soffice: path, bwrap: bwrap, sem: make(chan struct{}, 1)}, nil
}

// DOCX converts page (from Page) to a DOCX on A4 paper.
func (r *Renderer) DOCX(ctx context.Context, page string, landscape bool) ([]byte, error) {
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	dir, err := os.MkdirTemp("", "hq-docx-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "in.html"), []byte(page), 0o600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, convertTimeout)
	defer cancel()
	// shortcut: assumes a merged-/usr distro, as mdpdf does (New checks that
	// soffice lives under /usr). Limits as in mdpdf: file size, CPU seconds and
	// data segment (4 GB) per process.
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", fmt.Sprintf(`ulimit -f %d && ulimit -t 120 && ulimit -d 4194304 && exec "$@"`, maxDOCX/512), "sh",
		r.bwrap, "--unshare-all", "--die-with-parent", "--new-session", "--clearenv",
		"--setenv", "HOME", "/tmp", "--setenv", "LANG", "C.UTF-8",
		"--ro-bind", "/usr", "/usr", "--ro-bind-try", "/etc/fonts", "/etc/fonts", "--ro-bind-try", "/etc/libreoffice", "/etc/libreoffice",
		"--symlink", "usr/lib", "/lib", "--symlink", "usr/lib64", "/lib64", "--symlink", "usr/bin", "/bin",
		"--proc", "/proc", "--dev", "/dev", "--size", "268435456", "--tmpfs", "/tmp", "--bind", dir, "/job",
		r.soffice, "--headless", "--norestore", "-env:UserInstallation=file:///job/profile",
		"--convert-to", "docx:MS Word 2007 XML", "--infilter=HTML (StarWriter)", "--outdir", "/job", "/job/in.html")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.WaitDelay = 5 * time.Second
	out, runErr := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("converting to DOCX took longer than %s", convertTimeout)
	}
	data, err := os.ReadFile(filepath.Join(dir, "in.docx"))
	if err != nil || !bytes.HasPrefix(data, []byte("PK")) {
		return nil, fmt.Errorf("LibreOffice produced no DOCX (%v): %s", runErr, tail(out, 600))
	}
	return setPage(data, landscape)
}

var (
	pgSzRE = regexp.MustCompile(`<w:pgSz[^>]*/>`)
)

// setPage rewrites the page size (LibreOffice picks Letter) to A4, portrait or
// landscape. The XML is touched only on the one element, so a conversion that
// changes its output simply leaves the page size as it was.
func setPage(docx []byte, landscape bool) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(docx), int64(len(docx)))
	if err != nil {
		return nil, err
	}
	sz := `<w:pgSz w:w="11906" w:h="16838"/>`
	if landscape {
		sz = `<w:pgSz w:orient="landscape" w:w="16838" w:h="11906"/>`
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(rc, maxDOCX))
		rc.Close()
		if err != nil {
			return nil, err
		}
		if f.Name == "word/document.xml" {
			b = pgSzRE.ReplaceAll(b, []byte(sz))
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: zip.Deflate})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(b); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func tail(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		s = "…" + s[len(s)-n:]
	}
	return s
}
