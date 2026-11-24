package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ivoras/harlequin/internal/server/xmlq"
)

const (
	xmlMaxMatches  = 200 // per path, shown to the model
	xmlOutlineRows = 300
)

const queryXMLPrompt = `Read an XML document — an e-invoice (UBL Invoice/CreditNote, e-Račun), a bank statement (camt.053), any XML export — without reading the raw XML yourself. Two ways:
1. Only doc: the outline — every element path once, in document order and indented, with how often it occurs (×N), its attributes and its first value. Start here to learn the structure.
2. doc plus path (one) or paths (many at once, e.g. every field you need to check): the values at those paths, each with its exact position, e.g. /Invoice/cac:InvoiceLine[2]/cbc:LineExtensionAmount = "50.00" [currencyID=EUR].

Path syntax (a small XPath):
- /Invoice/cbc:ID — from the root; //cbc:ID or just cbc:ID — anywhere in the document
- names match with or without their prefix: ID matches cbc:ID; * is any element; .. is the parent
- cac:InvoiceLine[2] — the second one; [@schemeID='9934'] — with that attribute value; [@schemeID] — that has the attribute
- [cbc:ID='S'] — whose child has that text (one level down; to test a deeper element, go down and come back up: //cac:TaxCategory[cbc:ID='S']/.. is the TaxSubtotal of the S category)
- .../@currencyID — the attribute's value instead of the element's text
A path that matches nothing says so: that is how you check that a required element is missing. Embedded attachments (base64) are shown shortened.`

func (a *Agent) queryXMLEntry() toolEntry {
	return toolEntry{
		def: fnTool("query_xml", queryXMLPrompt, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"doc":   map[string]any{"type": "string", "description": `The XML document: a document reference (u.12, or as attached, e.g. @"u.18/invoice.xml") or a sandbox file (tmp://invoice.xml)`},
				"path":  map[string]any{"type": "string", "description": "One path to read, e.g. //cbc:PayableAmount. Omit (with paths) to get the outline."},
				"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": `Several paths in one call, e.g. ["/Invoice/cbc:ID", "//cbc:IssueDate", "//cac:AccountingSupplierParty//cbc:EndpointID"]`},
			},
			"required": []string{"doc"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			ref := strings.TrimSpace(argString(args, "doc"))
			root, label, errMsg := a.loadXML(ctx, rc, ref)
			if errMsg != "" {
				return errMsg, nil
			}
			paths := argStrings(args, "paths")
			if p := strings.TrimSpace(argString(args, "path")); p != "" {
				paths = append([]string{p}, paths...)
			}
			if len(paths) == 0 {
				return fmt.Sprintf("XML document %s. Structure (each element path once; ×N = occurrences; [attributes]; = first value):\n\n%s\nNext: query_xml with path or paths, e.g. %q.", label, xmlq.Outline(root, xmlOutlineRows), "//"+firstLeaf(root)), nil
			}
			var sb strings.Builder
			for _, p := range paths {
				ms, err := xmlq.Query(root, p)
				switch {
				case err != nil:
					fmt.Fprintf(&sb, "%s → error: %v\n", p, err)
				case len(ms) == 0:
					fmt.Fprintf(&sb, "%s → not found\n", p)
				default:
					if len(paths) > 1 || len(ms) > 1 {
						fmt.Fprintf(&sb, "%s → %d match(es):\n", p, len(ms))
					}
					for i, m := range ms {
						if i == xmlMaxMatches {
							fmt.Fprintf(&sb, "  … %d more not shown; narrow the path\n", len(ms)-xmlMaxMatches)
							break
						}
						fmt.Fprintf(&sb, "  %s = %s%s\n", m.Path, strconv.Quote(xmlq.Short(m.Value, 300)), attrText(m.Attrs))
					}
				}
			}
			return sb.String(), nil
		},
	}
}

func attrText(attrs map[string]string) string {
	var as []string
	for k, v := range attrs {
		if k != "xmlns" && !strings.HasPrefix(k, "xmlns:") {
			as = append(as, k+"="+v)
		}
	}
	if len(as) == 0 {
		return ""
	}
	return " [" + strings.Join(as, " ") + "]"
}

func firstLeaf(n *xmlq.Node) string {
	for _, c := range n.Children {
		if len(c.Children) == 0 && c.Text != "" {
			return c.Name
		}
		if s := firstLeaf(c); s != "" {
			return s
		}
	}
	return ""
}

// loadXML reads an XML document: a document's stored original (exact bytes,
// not the indexed text) or a sandbox file.
func (a *Agent) loadXML(ctx context.Context, rc *runContext, ref string) (*xmlq.Node, string, string) {
	if ref == "" {
		return nil, "", `error: doc is required: the XML document's reference (u.12) or a sandbox file (tmp://invoice.xml)`
	}
	var data []byte
	label := ref
	if isFilePath(ref) {
		var err error
		if data, err = a.readFile(ctx, rc, ref); err != nil {
			return nil, "", fmt.Sprintf("error: cannot read %s: %v", ref, err)
		}
	} else {
		doc, errMsg := a.loadDocRef(ctx, rc, ref)
		if errMsg != "" {
			return nil, "", errMsg
		}
		label = fmt.Sprintf("%s %q", docRef(doc), doc.Title)
		if d, _, _, ok := a.storedOriginal(ctx, rc, doc); ok {
			data = d
		} else {
			text, errMsg := a.docText(ctx, rc, doc)
			if errMsg != "" {
				return nil, "", errMsg
			}
			data = []byte(text)
		}
	}
	root, err := xmlq.Parse(data)
	if err != nil {
		return nil, "", fmt.Sprintf("error: %s cannot be read as XML: %v", label, err)
	}
	return root, label, ""
}

// docJSGlobals are the document helpers of run_js and skill-defined tools:
// read_doc, sheets.*, xml.*.
func (a *Agent) docJSGlobals(ctx context.Context, rc *runContext) map[string]any {
	g := a.sheetJSGlobals(ctx, rc)
	g["xml"] = a.xmlJSGlobals(ctx, rc)
	return g
}

// xmlJSGlobals is run_js's xml.query(doc, path) / xml.outline(doc).
func (a *Agent) xmlJSGlobals(ctx context.Context, rc *runContext) map[string]any {
	load := func(ref string) (*xmlq.Node, error) {
		root, _, errMsg := a.loadXML(ctx, rc, strings.TrimSpace(ref))
		if errMsg != "" {
			return nil, errors.New(strings.TrimPrefix(errMsg, "error: "))
		}
		return root, nil
	}
	return map[string]any{
		"query": func(ref, path string) ([]any, error) {
			root, err := load(ref)
			if err != nil {
				return nil, err
			}
			ms, err := xmlq.Query(root, path)
			if err != nil {
				return nil, err
			}
			out := make([]any, len(ms))
			for i, m := range ms {
				attrs := map[string]any{}
				for k, v := range m.Attrs {
					attrs[k] = v
				}
				out[i] = map[string]any{"path": m.Path, "value": m.Value, "attrs": attrs, "children": len(m.Node.Children)}
			}
			return out, nil
		},
		"outline": func(ref string) (string, error) {
			root, err := load(ref)
			if err != nil {
				return "", err
			}
			return xmlq.Outline(root, 2000), nil
		},
	}
}
