package agent

import (
	"context"
	"strings"
	"testing"
)

const testInvoiceXML = `<?xml version="1.0" encoding="UTF-8"?>
<Invoice xmlns="urn:oasis:names:specification:ubl:schema:xsd:Invoice-2" xmlns:cac="urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2" xmlns:cbc="urn:oasis:names:specification:ubl:schema:xsd:CommonBasicComponents-2">
<cbc:ID>1-1-1</cbc:ID><cbc:IssueDate>2026-01-21</cbc:IssueDate>
<cac:InvoiceLine><cbc:ID>1</cbc:ID><cbc:LineExtensionAmount currencyID="EUR">100.00</cbc:LineExtensionAmount></cac:InvoiceLine>
<cac:InvoiceLine><cbc:ID>2</cbc:ID><cbc:LineExtensionAmount currencyID="EUR">50.00</cbc:LineExtensionAmount></cac:InvoiceLine>
</Invoice>`

func TestQueryXML(t *testing.T) {
	a, rc := sheetTestAgent(t)
	_ = a.tmpRoot(1).Write("racun.xml", []byte(testInvoiceXML))
	run := func(args map[string]any) string {
		out, err := a.queryXMLEntry().handler(context.Background(), rc, args)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := run(map[string]any{"doc": "tmp://racun.xml"})
	if !strings.Contains(out, "cac:InvoiceLine (×2)") || !strings.Contains(out, `cbc:ID = "1-1-1"`) {
		t.Fatalf("outline:\n%s", out)
	}
	out = run(map[string]any{"doc": "tmp://racun.xml", "paths": []any{"/Invoice/cbc:ID", "//cbc:LineExtensionAmount", "//cbc:DueDate"}})
	for _, w := range []string{`/Invoice/cbc:ID = "1-1-1"`, `/Invoice/cac:InvoiceLine[2]/cbc:LineExtensionAmount = "50.00" [currencyID=EUR]`, "//cbc:DueDate → not found"} {
		if !strings.Contains(out, w) {
			t.Errorf("paths lack %q:\n%s", w, out)
		}
	}
	_ = a.tmpRoot(1).Write("bad.xml", []byte("<a><b></a>"))
	if out := run(map[string]any{"doc": "tmp://bad.xml"}); !strings.Contains(out, "cannot be read as XML") {
		t.Fatalf("bad xml: %s", out)
	}
	rcx := a.jsRunContext(context.Background(), rc)
	rcx.Globals = map[string]any{"xml": a.xmlJSGlobals(context.Background(), rc)}
	res, err := a.Runner.Run(`var s = 0; xml.query("tmp://racun.xml", "//cbc:LineExtensionAmount").forEach(function(m){ s += parseFloat(m.value); }); println(s, xml.query("tmp://racun.xml", "//cbc:LineExtensionAmount")[0].attrs.currencyID);`, rcx)
	if err != nil || strings.TrimSpace(res.Output) != "150 EUR" {
		t.Fatalf("run_js xml: %v %q", err, res.Output)
	}
}
