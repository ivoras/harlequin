// Package mdpdf typesets Markdown or HTML as a PDF: Markdown is rendered to
// HTML with a house stylesheet, and headless Chrome prints the page to PDF.
//
// The source comes from the model (and through it from fetched web pages), so
// it is treated as hostile and there are three independent layers:
//   - sanitize rebuilds the page from the parsed tree without scripts, frames,
//     forms, event handlers or external references;
//   - a Content-Security-Policy in the page blocks scripts and every load that
//     is not inline (without it a file:// image is embedded in the PDF);
//   - Chrome runs in a bubblewrap sandbox with no network, an empty
//     environment, resource limits, and a file system of /usr, fonts, its own
//     directory and one scratch directory.
//
// Chrome's own sandbox is off (--no-sandbox): AppArmor denies it unprivileged
// user namespaces on current Ubuntu, and bubblewrap is the boundary instead.
package mdpdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	xhtml "golang.org/x/net/html"
)

// ChromeBinary is the executable looked up on PATH when none is configured.
const ChromeBinary = "chrome-headless-shell"

const (
	renderTimeout = 60 * time.Second
	// maxSource bounds one document (~1000 pages of text); Chrome's memory
	// grows with the page.
	maxSource = 4 << 20
	// maxPDF bounds the result (and, as RLIMIT_FSIZE, any file Chrome writes).
	maxPDF = 200 << 20
)

// Raw HTML in the source is dropped and unsafe URL schemes neutralised
// (goldmark's defaults), so the page holds only what the stylesheet expects.
var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

// csp allows inline styles (the stylesheets, style attributes) and inline
// images and fonts, nothing else: no script, no network, no file://.
const csp = `default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:`

const css = `@page{size:A4 %s;margin:20mm 18mm 22mm;
 @bottom-center{content:counter(page) " / " counter(pages);font:9pt "DejaVu Sans","Liberation Sans",Arial,sans-serif;color:#666}}
html{font:10.5pt/1.5 "DejaVu Sans","Liberation Sans",Arial,sans-serif;color:#1f2328}
body{margin:0}
h1,h2,h3,h4,h5,h6{line-height:1.25;color:#111827;margin:1.3em 0 .5em;break-after:avoid}
h1{font-size:20pt;border-bottom:1px solid #d9dde1;padding-bottom:.25em;margin-top:0}
h2{font-size:15pt}
h3{font-size:12.5pt}
h4,h5,h6{font-size:10.5pt}
p,ul,ol,blockquote,pre,table{margin:0 0 .9em}
p{orphans:3;widows:3}
li{margin:.15em 0}
a{color:#1a5fb4;text-decoration:none}
code{font:9pt "DejaVu Sans Mono","Liberation Mono",monospace;background:#f3f4f6;padding:1px 4px;border-radius:3px}
pre{background:#f3f4f6;padding:8px 10px;border-radius:4px;white-space:pre-wrap;word-break:break-word}
pre code{background:none;padding:0}
blockquote{margin-left:0;padding:1px 12px;border-left:3px solid #c7ccd1;color:#4b5563}
table{border-collapse:collapse;width:100%%;font-size:9.5pt}
th,td{border:1px solid #b9bfc6;padding:4px 7px;text-align:left;vertical-align:top}
th{background:#eef0f2}
tr{break-inside:avoid}
hr{border:0;border-top:1px solid #d9dde1;margin:1.3em 0}
img{max-width:100%%}
.pagebreak{break-after:page}`

// A paragraph holding only \newpage (or \pagebreak) starts a new page.
var pageBreakRE = regexp.MustCompile(`<p>\\(?:newpage|pagebreak)</p>`)

// Stats describes what Page did with the source.
type Stats struct {
	// ImagesDropped counts images that are not inline data: (they could not
	// load, and would print as a broken-image icon); their alt text is kept.
	ImagesDropped int
	// Removed counts active or external content taken out of HTML input:
	// scripts, frames, forms, embeds, event handlers, external references.
	Removed int
}

// Page builds the complete page that gets printed from src, which is Markdown
// or (isHTML) an HTML document or fragment. title becomes the PDF's document
// title; the visible headings come from src. text is src as plain Markdown,
// for indexing.
//
// Either way the body goes through sanitize and the head is written here, so
// the Content-Security-Policy is always the first thing in the page and nothing
// from the source can precede or replace it. HTML input keeps its own <style>
// rules (that is its point), applied after the house stylesheet.
func Page(title, src string, isHTML, landscape bool) (page, text string, st Stats, err error) {
	body, styles, text, st, err := Body(src, isHTML)
	if err != nil {
		return "", "", st, err
	}
	orient := "portrait"
	if landscape {
		orient = "landscape"
	}
	return "<!DOCTYPE html>\n<html><head><meta charset=\"utf-8\">" +
		`<meta http-equiv="Content-Security-Policy" content="` + csp + `">` +
		"<title>" + html.EscapeString(title) + "</title><style>" + fmt.Sprintf(css, orient) + "</style>" +
		"<style>" + styles + "</style></head><body>\n" + body + "</body></html>\n", text, st, nil
}

// Body is the sanitised part of Page, for other converters (mdword): the body
// HTML (\newpage already turned into <div class="pagebreak">), the text of the
// source's <style> elements, and src as plain Markdown for indexing.
func Body(src string, isHTML bool) (body, styles, text string, st Stats, err error) {
	if strings.TrimSpace(src) == "" {
		return "", "", "", st, errors.New("the document is empty")
	}
	if len(src) > maxSource {
		return "", "", "", st, fmt.Errorf("the document is too long (%d bytes; limit %d)", len(src), maxSource)
	}
	text = strings.TrimSpace(src)
	if !isHTML {
		var b bytes.Buffer
		if err := md.Convert([]byte(src), &b); err != nil {
			return "", "", "", st, err
		}
		src = pageBreakRE.ReplaceAllString(b.String(), `<div class="pagebreak"></div>`)
	}
	body, styles, err = sanitize(src, &st)
	if err != nil {
		return "", "", "", st, err
	}
	if isHTML {
		if text, err = htmltomarkdown.ConvertString(body); err != nil {
			return "", "", "", st, fmt.Errorf("cannot read the HTML: %w", err)
		}
		if text = strings.TrimSpace(text); text == "" {
			return "", "", "", st, errors.New("the HTML has no visible text")
		}
	}
	return body, styles, text, st, nil
}

// dropped elements run code, load or embed something, or submit somewhere.
var dropped = map[string]bool{
	"script": true, "noscript": true, "iframe": true, "frame": true, "frameset": true, "object": true,
	"embed": true, "applet": true, "link": true, "meta": true, "base": true, "form": true, "input": true,
	"button": true, "select": true, "textarea": true, "audio": true, "video": true, "source": true,
	"track": true, "portal": true, "dialog": true, "foreignobject": true, "animate": true, "set": true,
}

// droppedAttrs can only name something to load or somewhere to send.
var droppedAttrs = map[string]bool{
	"srcset": true, "srcdoc": true, "ping": true, "action": true, "formaction": true, "background": true,
	"poster": true, "data": true, "manifest": true, "codebase": true, "archive": true, "classid": true,
	"lowsrc": true, "dynsrc": true,
}

// sanitize parses src (a document or a fragment; malformed markup is repaired by
// the parser) and returns its body, re-serialised, with everything active or
// external removed, plus the text of its <style> elements. It works on the
// parsed tree, not the text, so markup tricks cannot hide an element from it.
func sanitize(src string, st *Stats) (body, styles string, err error) {
	doc, err := xhtml.Parse(strings.NewReader(src))
	if err != nil {
		return "", "", fmt.Errorf("cannot parse the HTML: %w", err)
	}
	var css strings.Builder
	var bodyNode *xhtml.Node
	var clean func(n *xhtml.Node)
	clean = func(n *xhtml.Node) {
		for c, next := n.FirstChild, (*xhtml.Node)(nil); c != nil; c = next {
			next = c.NextSibling
			if c.Type == xhtml.CommentNode || c.Type == xhtml.DoctypeNode {
				n.RemoveChild(c)
				continue
			}
			if c.Type != xhtml.ElementNode {
				continue
			}
			name := strings.ToLower(c.Data)
			switch {
			case dropped[name]:
				st.Removed++
				n.RemoveChild(c)
				continue
			case name == "style":
				for t := c.FirstChild; t != nil; t = t.NextSibling {
					css.WriteString(scrubCSS(t.Data))
					css.WriteByte('\n')
				}
				n.RemoveChild(c)
				continue
			case name == "title":
				n.RemoveChild(c)
				continue
			case name == "img" && !isDataImage(attr(c, "src")):
				st.ImagesDropped++
				label := "[image]"
				if alt := strings.TrimSpace(attr(c, "alt")); alt != "" {
					label = "[image: " + alt + "]"
				}
				em := &xhtml.Node{Type: xhtml.ElementNode, Data: "em"}
				em.AppendChild(&xhtml.Node{Type: xhtml.TextNode, Data: label})
				n.InsertBefore(em, c)
				n.RemoveChild(c)
				continue
			case name == "body" && bodyNode == nil:
				bodyNode = c
			}
			kept := c.Attr[:0]
			for _, a := range c.Attr {
				key := strings.ToLower(a.Key)
				switch {
				case strings.HasPrefix(key, "on"), droppedAttrs[key]:
					st.Removed++
					continue
				case key == "src":
					if !isDataImage(a.Val) {
						st.Removed++
						continue
					}
				case key == "href":
					if !safeHref(a.Val, name == "a" && c.Namespace == "") {
						st.Removed++
						continue
					}
				case key == "style":
					a.Val = scrubCSS(a.Val)
				}
				kept = append(kept, a)
			}
			c.Attr = kept
			clean(c)
		}
	}
	clean(doc)
	if bodyNode == nil {
		return "", "", errors.New("the HTML has no body")
	}
	var out bytes.Buffer
	for c := bodyNode.FirstChild; c != nil; c = c.NextSibling {
		if err := xhtml.Render(&out, c); err != nil {
			return "", "", err
		}
	}
	return out.String(), css.String(), nil
}

func attr(n *xhtml.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// urlHead normalises the start of a URL the way a browser reads it (it ignores
// whitespace and control characters there), for scheme checks.
func urlHead(u string) string {
	u = strings.Map(func(r rune) rune {
		if r <= ' ' {
			return -1
		}
		return r
	}, u)
	return strings.ToLower(u)
}

func isDataImage(u string) bool {
	u = urlHead(u)
	// SVG can itself carry script and references; raster images cannot.
	return strings.HasPrefix(u, "data:image/") && !strings.HasPrefix(u, "data:image/svg")
}

// safeHref allows what a PDF link may point at: the web or mail for a real
// link, and only same-document fragments or inline images for anything else
// (SVG <use>, <image>, ...).
func safeHref(u string, link bool) bool {
	h := urlHead(u)
	if strings.HasPrefix(h, "#") {
		return true
	}
	if link {
		return strings.HasPrefix(h, "http://") || strings.HasPrefix(h, "https://") || strings.HasPrefix(h, "mailto:")
	}
	return isDataImage(u)
}

var (
	cssImportRE = regexp.MustCompile(`(?i)@import[^;]*;?`)
	cssURLRE    = regexp.MustCompile(`(?i)url\s*\([^)]*\)`)
)

// scrubCSS removes what CSS can load from elsewhere. It is a courtesy (a load
// that fails cleanly beats a hole in the page), not the guard: escapes can spell
// url() in ways a regexp does not see, and the CSP and the sandbox stop those.
// "<" is escaped so the text cannot end the <style> element it is written into.
func scrubCSS(s string) string {
	s = cssImportRE.ReplaceAllString(s, "")
	s = cssURLRE.ReplaceAllStringFunc(s, func(u string) string {
		arg := strings.Trim(u[strings.IndexByte(u, '(')+1:len(u)-1], " \t\r\n'\"")
		if isDataImage(arg) || strings.HasPrefix(urlHead(arg), "data:font/") {
			return u
		}
		return "none"
	})
	return strings.ReplaceAll(s, "<", `\3c `)
}

// Renderer prints HTML pages to PDF with a sandboxed headless Chrome.
type Renderer struct {
	chromeDir, chromeName, bwrap string
	sem                          chan struct{} // bounds concurrent Chromes (memory)
}

// New locates Chrome (the configured path, else ChromeBinary on PATH) and
// bubblewrap. There is deliberately no way to run without the sandbox.
func New(chrome string) (*Renderer, error) {
	if chrome == "" {
		chrome = ChromeBinary
	}
	path, err := exec.LookPath(chrome)
	if err != nil {
		return nil, fmt.Errorf("headless Chrome not found (%s): %w", chrome, err)
	}
	// The sandbox mounts Chrome's real directory: it needs the files beside it.
	if path, err = filepath.EvalSymlinks(path); err != nil {
		return nil, err
	}
	if path, err = filepath.Abs(path); err != nil {
		return nil, err
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("bubblewrap (bwrap) not found, and Chrome is never run unsandboxed: %w", err)
	}
	return &Renderer{chromeDir: filepath.Dir(path), chromeName: filepath.Base(path), bwrap: bwrap, sem: make(chan struct{}, 2)}, nil
}

// PDF prints page (a document from Page) and returns the PDF bytes.
func (r *Renderer) PDF(ctx context.Context, page string) ([]byte, error) {
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	dir, err := os.MkdirTemp("", "hq-pdf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "in.html"), []byte(page), 0o600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()
	// shortcut: assumes a merged-/usr distro (/lib, /bin are links into /usr);
	// bind the real directories too if this ever runs on one that is not.
	//
	// The shell only sets limits bubblewrap cannot: file size (the scratch
	// directory is on the host's disk), CPU seconds and data segment (4 GB) per
	// process. Chrome needs a real /proc and far more address space than it uses,
	// so RLIMIT_AS is not an option.
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", fmt.Sprintf(`ulimit -f %d && ulimit -t 120 && ulimit -d 4194304 && exec "$@"`, maxPDF/512), "sh",
		r.bwrap, "--unshare-all", "--die-with-parent", "--new-session", "--clearenv",
		"--setenv", "HOME", "/tmp", "--setenv", "LANG", "C.UTF-8",
		"--ro-bind", "/usr", "/usr", "--ro-bind-try", "/etc/fonts", "/etc/fonts",
		"--symlink", "usr/lib", "/lib", "--symlink", "usr/lib64", "/lib64", "--symlink", "usr/bin", "/bin",
		"--proc", "/proc", "--dev", "/dev", "--size", "268435456", "--tmpfs", "/tmp",
		"--ro-bind", r.chromeDir, "/chrome", "--bind", dir, "/job",
		"/chrome/"+r.chromeName, "--no-sandbox", "--disable-gpu", "--no-pdf-header-footer",
		"--print-to-pdf=/job/out.pdf", "file:///job/in.html")
	// The server's environment holds its secrets (.env); none of it goes along.
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.WaitDelay = 5 * time.Second
	log, runErr := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("rendering the PDF took longer than %s", renderTimeout)
	}
	// Chrome can exit 0 without writing anything, so the file is the verdict.
	pdf, err := os.ReadFile(filepath.Join(dir, "out.pdf"))
	if err != nil || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		return nil, fmt.Errorf("Chrome produced no PDF (%v): %s", runErr, tail(log, 600))
	}
	return pdf, nil
}

var pageObjRE = regexp.MustCompile(`/Type\s*/Page\b`)

// Pages counts the pages of a PDF written by PDF (0 if it cannot tell).
func Pages(pdf []byte) int { return len(pageObjRE.FindAll(pdf, -1)) }

func tail(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		s = "…" + s[len(s)-n:]
	}
	return s
}
