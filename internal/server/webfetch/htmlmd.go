package webfetch

import (
	"bytes"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// htmlToMarkdown converts an HTML document to Markdown using the
// JohannesKaufmann/html-to-markdown converter and returns the markdown plus the
// page <title>. baseURL absolutizes relative links.
func htmlToMarkdown(htmlBytes []byte, baseURL string) (string, string) {
	doc, err := html.Parse(bytes.NewReader(htmlBytes))
	if err != nil {
		return string(htmlBytes), ""
	}
	title := findTitle(doc)

	mdBytes, err := htmltomarkdown.ConvertNode(doc, converter.WithDomain(baseURL))
	if err != nil {
		return string(htmlBytes), title
	}
	return strings.TrimSpace(string(mdBytes)), title
}

// findTitle returns the document's <title> text, if any.
func findTitle(doc *html.Node) string {
	var title string
	var rec func(*html.Node) bool
	rec = func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.DataAtom == atom.Title {
			title = strings.TrimSpace(textContent(n))
			return true
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if rec(ch) {
				return true
			}
		}
		return false
	}
	rec(doc)
	return title
}

// textContent returns the concatenated raw text of a node's subtree.
func textContent(n *html.Node) string {
	var b strings.Builder
	var rec func(*html.Node)
	rec = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for ch := node.FirstChild; ch != nil; ch = ch.NextSibling {
			rec(ch)
		}
	}
	rec(n)
	return b.String()
}
