package email

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

// ErrNotConfigured is returned by SendMarkdown when no SMTP server is set up
// (unlike Send, which logs and succeeds: a caller that reports "sent" to a user
// must not be lied to).
var ErrNotConfigured = errors.New("email is not configured on this server (email.smtp_host is empty)")

// md renders GitHub-flavoured Markdown. Raw HTML in the source is dropped and
// unsafe URL schemes (javascript:) are neutralised — goldmark's defaults, kept
// on purpose: the text being mailed may come from fetched web pages.
var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(gmhtml.WithHardWraps()),
)

// emailCSS styles the rendered HTML. A <style> block (not a stylesheet) so it
// survives in webmail clients; kept plain enough for Outlook's Word renderer.
const emailCSS = `body{margin:0;padding:0;background:#f4f4f7}
.wrap{max-width:680px;margin:0 auto;padding:24px 28px;background:#ffffff;color:#1f2328;
 font-family:-apple-system,Segoe UI,Helvetica,Arial,sans-serif;font-size:15px;line-height:1.55}
h1,h2,h3,h4{line-height:1.25;color:#111827;margin:1.4em 0 .5em}
h1{font-size:24px;border-bottom:1px solid #e5e7eb;padding-bottom:.3em}
h2{font-size:20px;border-bottom:1px solid #eef0f2;padding-bottom:.25em}
h3{font-size:17px}
p,ul,ol,blockquote,pre,table{margin:0 0 1em}
a{color:#2f6f4f}
code{font-family:SFMono-Regular,Consolas,Menlo,monospace;font-size:13px;background:#f3f4f6;padding:1px 5px;border-radius:4px}
pre{background:#f3f4f6;padding:12px 14px;border-radius:6px;overflow:auto}
pre code{background:none;padding:0}
blockquote{margin-left:0;padding:2px 14px;border-left:4px solid #c7d2cc;color:#4b5563}
table{border-collapse:collapse;width:100%}
th,td{border:1px solid #d9dde1;padding:6px 10px;text-align:left;vertical-align:top}
th{background:#f3f6f4}
hr{border:0;border-top:1px solid #e5e7eb;margin:1.5em 0}
img{max-width:100%}`

// MarkdownToHTML renders markdown as a complete, email-ready HTML document.
func MarkdownToHTML(title, markdown string) (string, error) {
	var body bytes.Buffer
	if err := md.Convert([]byte(markdown), &body); err != nil {
		return "", err
	}
	return "<!DOCTYPE html>\n<html><head><meta charset=\"utf-8\">" +
		"<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">" +
		"<title>" + html.EscapeString(title) + "</title><style>" + emailCSS + "</style></head>" +
		"<body><div class=\"wrap\">\n" + body.String() + "</div></body></html>\n", nil
}

// ParseRecipient accepts a bare address ("a@b.hr") only — no display name, no
// list — so one tool call can never address several people or smuggle headers.
func ParseRecipient(to string) (string, error) {
	to = strings.TrimSpace(to)
	a, err := mail.ParseAddress(to)
	if err != nil || a.Address != to || a.Name != "" || !ValidAddress(to) {
		return "", fmt.Errorf("%q is not a valid single e-mail address", to)
	}
	return to, nil
}

// BuildMarkdownMessage assembles a multipart/alternative message: the Markdown
// source as the text/plain part and its rendering as the text/html part (last,
// so clients prefer it). Bodies are quoted-printable UTF-8; the subject is
// RFC 2047-encoded, and CR/LF are stripped from every header value. replyTo may
// be empty.
func BuildMarkdownMessage(from, to, replyTo, subject, markdown string) ([]byte, error) {
	htmlDoc, err := MarkdownToHTML(subject, markdown)
	if err != nil {
		return nil, fmt.Errorf("render markdown: %w", err)
	}
	var out bytes.Buffer
	mw := multipart.NewWriter(&out) // random boundary; the headers are written separately
	var head strings.Builder
	writeHeaders(&head, from, to, replyTo, subject)
	fmt.Fprintf(&head, "Content-Type: multipart/alternative; boundary=%s\r\n\r\n", mw.Boundary())

	for _, p := range []struct{ ctype, body string }{
		{"text/plain; charset=utf-8", markdown},
		{"text/html; charset=utf-8", htmlDoc},
	} {
		pw, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {p.ctype},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qw := quotedprintable.NewWriter(pw)
		if _, err := qw.Write([]byte(p.body)); err != nil {
			return nil, err
		}
		if err := qw.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return append([]byte(head.String()), out.Bytes()...), nil
}

// SendMarkdown sends markdown to one recipient as a multipart/alternative
// message (Markdown text + rendered HTML). It fails with ErrNotConfigured
// rather than silently logging when SMTP is not set up.
func (s *Sender) SendMarkdown(to, replyTo, subject, markdown string) error {
	if !s.Configured() {
		return ErrNotConfigured
	}
	to, err := ParseRecipient(to)
	if err != nil {
		return err
	}
	msg, err := BuildMarkdownMessage(s.fromHeader(), to, replyTo, subject, markdown)
	if err != nil {
		return err
	}
	return s.deliver(to, msg)
}
