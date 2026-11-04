package agent

import (
	"context"
	"fmt"
	"log"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ivoras/harlequin/internal/server/sessionlog"
)

// MarkdownConverter turns a document's bytes into Markdown. It is satisfied by
// docling.Client (and xberg.Client); the filename's extension tells the
// converter which input format it is looking at.
type MarkdownConverter interface {
	Convert(ctx context.Context, filename string, data []byte) (string, error)
}

const (
	markdownFetchDescription = `
- Fetches a URL and converts it to Markdown with Docling (layout analysis, tables, multi-column text) — returns the CONTENT itself, not an AI summary.
- PREFER THIS TOOL whenever the user wants to read the entire, unabridged content of a web page or document ("show me the page", "read it all", "the full text of ...", "what exactly does it say") — WebFetch only returns a short AI-generated answer and drops everything the prompt did not ask about.
- Also use it when you need faithful structure (tables, headings) or the full text to quote, or to save with save_doc.
- Web pages are retrieved through the Zyte browser API when configured (JavaScript-rendered content, bot-protected sites); documents (PDF, DOCX, PPTX, XLSX, Markdown, CSV) are downloaded directly.
- Prefer WebFetch only when you need one specific answer from an ordinary page (cheaper: no conversion service, short answer).
- Input: a valid URL (auto-upgrades HTTP→HTTPS). Redirects are followed.
- The result is the content itself and is now in your context: work from it directly. Do NOT fetch the same URL again (not with MarkdownFetch, WebFetch, WebFetchDOM or fetch() in run_js) — a repeat fetch wastes time and money and returns the same text. Re-fetch only if the page has since changed or the result was truncated.
- Very long results are truncated and say so.
- Optional save_file (e.g. "base.md"): ALSO writes the full converted Markdown to your sandbox as tmp://base.md, so you can change it with edit_file and publish it with save_doc(source=...) — use it when you will edit or republish the document, not for pages you only read.
`
	// markdownFetchMaxRunes caps what is returned to the model.
	markdownFetchMaxRunes = 1000000
	// markdownFetchMaxBytes caps the download handed to the converter.
	markdownFetchMaxBytes = 50 << 20
)

// markdownFetchEntry registers the MarkdownFetch tool (needs a web fetcher and
// a configured document converter).
func (a *Agent) markdownFetchEntry() toolEntry {
	return toolEntry{
		def: fnTool("MarkdownFetch", markdownFetchDescription, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":       map[string]any{"type": "string", "format": "uri", "description": "The URL of the page or document to fetch and convert to Markdown"},
				"save_file": map[string]any{"type": "string", "description": "Optional sandbox file name (e.g. base.md or tmp://base.md) to also write the full Markdown to, for later edit_file / save_doc(source=...)"},
			},
			"required":             []string{"url"},
			"additionalProperties": false,
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			return a.markdownFetch(ctx, rc, args)
		},
	}
}

func (a *Agent) markdownFetch(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
	if a.WebFetcher == nil || a.MarkdownConverter == nil {
		return "error: MarkdownFetch is not enabled on this server", nil
	}
	rawURL, _ := args["url"].(string)
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "error: url is required", nil
	}

	start := time.Now()
	raw, err := a.WebFetcher.FetchRaw(ctx, rawURL) // same anti-bot + SSRF-guarded path as WebFetch
	if err != nil {
		a.logEvent(ctx, rc, sessionlog.TypeWebFetch, map[string]any{
			"tool": "MarkdownFetch", "url": rawURL, "ok": false, "error": err.Error(),
		})
		log.Printf("markdownfetch: GET %s failed: %v", rawURL, err)
		return fmt.Sprintf("error: failed to fetch %s: %v", rawURL, err), nil
	}
	if len(raw.Body) > markdownFetchMaxBytes {
		return fmt.Sprintf("error: %s is too large to convert (%d MiB; limit %d MiB)", rawURL, len(raw.Body)>>20, markdownFetchMaxBytes>>20), nil
	}
	// Web pages go through Zyte's browser API when available: it returns the
	// rendered DOM, so JS-built content and bot walls don't truncate the page.
	// Documents (PDF etc.) must come from the direct download — Zyte only
	// returns HTML. On any Zyte failure the direct body is used.
	if !raw.ViaZyte && strings.Contains(raw.ContentType, "html") && a.WebFetcher.ZyteEnabled() {
		if z, zerr := a.WebFetcher.FetchRawZyte(ctx, rawURL); zerr == nil {
			raw = z
		} else {
			log.Printf("markdownfetch: zyte failed for %s, using the direct fetch: %v", rawURL, zerr)
		}
	}
	fetchMS := time.Since(start).Milliseconds()

	filename, ok := markdownFetchFilename(raw.FinalURL, rawURL, raw.ContentType, raw.Body)
	if !ok {
		return fmt.Sprintf("error: %s has content type %q, which cannot be converted to Markdown (supported: PDF, DOCX, PPTX, XLSX, HTML, Markdown, CSV); use WebFetch or WebFetchGrep for it", rawURL, raw.ContentType), nil
	}

	convStart := time.Now()
	md, err := a.MarkdownConverter.Convert(ctx, filename, raw.Body)
	convMS := time.Since(convStart).Milliseconds()
	if err != nil {
		a.logEvent(ctx, rc, sessionlog.TypeWebFetch, map[string]any{
			"tool": "MarkdownFetch", "url": rawURL, "final_url": raw.FinalURL, "ok": false,
			"error": err.Error(), "fetch_ms": fetchMS, "convert_ms": convMS, "bytes": len(raw.Body),
		})
		log.Printf("markdownfetch: convert %s (%s) failed after %dms: %v", rawURL, filename, convMS, err)
		return fmt.Sprintf("error: failed to convert %s to Markdown: %v", rawURL, err), nil
	}
	a.logEvent(ctx, rc, sessionlog.TypeWebFetch, map[string]any{
		"tool": "MarkdownFetch", "url": rawURL, "final_url": raw.FinalURL, "ok": true,
		"cached": raw.Cached, "via_zyte": raw.ViaZyte, "fetch_ms": fetchMS, "convert_ms": convMS,
		"bytes": len(raw.Body), "md_chars": len(md), "file": filename,
	})
	log.Printf("markdownfetch: %s (%s, %d bytes -> %d chars; fetch %dms, convert %dms)",
		rawURL, filename, len(raw.Body), len(md), fetchMS, convMS)

	savedNote := ""
	if name := markdownFetchSaveName(argString(args, "save_file")); name != "" {
		if err := a.tmpRoot(rc.userID).Write(name, []byte(md)); err != nil {
			savedNote = fmt.Sprintf("\n\n[could not save to tmp://%s: %v]", name, err)
		} else {
			savedNote = fmt.Sprintf("\n\n[full text also saved to tmp://%s — change it with edit_file, publish it with save_doc(source=\"tmp://%s\"); do not fetch the URL again]", name, name)
		}
	}
	if utf8.RuneCountInString(md) > markdownFetchMaxRunes {
		r := []rune(md)
		md = string(r[:markdownFetchMaxRunes]) + fmt.Sprintf("\n\n[content truncated at %d characters of %d]", markdownFetchMaxRunes, len(r))
	}
	return md + savedNote, nil
}

// markdownFetchSaveName normalises a save_file argument to a bare sandbox file
// name ("" = don't save). A tmp:// prefix is accepted; other namespaces are not.
func markdownFetchSaveName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "tmp://")
	if s == "" || strings.Contains(s, "://") {
		return ""
	}
	return s
}

// markdownFetchExts maps the extensions the converter understands.
var markdownFetchExts = map[string]bool{
	".pdf": true, ".docx": true, ".pptx": true, ".xlsx": true,
	".html": true, ".htm": true, ".md": true, ".csv": true,
}

// markdownFetchTypes maps response content types to a converter filename extension.
var markdownFetchTypes = map[string]string{
	"application/pdf": ".pdf",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   ".docx",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": ".pptx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         ".xlsx",
	"text/html":             ".html",
	"application/xhtml+xml": ".html",
	"text/markdown":         ".md",
	"text/csv":              ".csv",
}

// markdownFetchFilename picks the filename (and so the input format) handed to
// the converter. A specific response Content-Type wins; for generic or missing
// ones (URLs like /download?id=7 often serve octet-stream) the URL's extension
// is used, and the body is sniffed as a last resort.
func markdownFetchFilename(finalURL, reqURL, contentType string, body []byte) (string, bool) {
	ct, _, _ := mime.ParseMediaType(contentType)
	if ext, ok := markdownFetchTypes[ct]; ok {
		return "document" + ext, true
	}
	// Generic types ("application/octet-stream", missing, wrong) fall back to the
	// URL path's extension.
	for _, u := range []string{finalURL, reqURL} {
		if p, err := url.Parse(u); err == nil {
			if ext := strings.ToLower(path.Ext(p.Path)); markdownFetchExts[ext] {
				return "document" + ext, true
			}
		}
	}
	if ct == "" || ct == "application/octet-stream" || strings.HasPrefix(ct, "text/") {
		switch sniff := http.DetectContentType(body); {
		case strings.HasPrefix(sniff, "application/pdf"):
			return "document.pdf", true
		case strings.HasPrefix(sniff, "text/html"):
			return "document.html", true
		}
	}
	return "", false
}
