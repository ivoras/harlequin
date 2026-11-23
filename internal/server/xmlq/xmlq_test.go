package xmlq

import (
	"strings"
	"testing"
)

const invoice = `<?xml version="1.0" encoding="UTF-8"?>
<Invoice xmlns="urn:oasis:names:specification:ubl:schema:xsd:Invoice-2"
 xmlns:cac="urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2"
 xmlns:cbc="urn:oasis:names:specification:ubl:schema:xsd:CommonBasicComponents-2">
  <cbc:CustomizationID>urn:cen.eu:en16931:2017#compliant#urn:mfin.gov.hr:cius-2025:1.0</cbc:CustomizationID>
  <cbc:ID>1-1-1</cbc:ID>
  <cbc:IssueDate>2026-01-21</cbc:IssueDate>
  <cac:AccountingSupplierParty><cac:Party>
    <cbc:EndpointID schemeID="9934">12345678901</cbc:EndpointID>
    <cac:PartyName><cbc:Name>Acme d.o.o.</cbc:Name></cac:PartyName>
  </cac:Party></cac:AccountingSupplierParty>
  <cac:InvoiceLine><cbc:ID>1</cbc:ID><cbc:LineExtensionAmount currencyID="EUR">100.00</cbc:LineExtensionAmount>
    <cac:Item><cac:ClassifiedTaxCategory><cbc:ID>S</cbc:ID><cbc:Percent>25</cbc:Percent></cac:ClassifiedTaxCategory></cac:Item></cac:InvoiceLine>
  <cac:InvoiceLine><cbc:ID>2</cbc:ID><cbc:LineExtensionAmount currencyID="EUR">50.00</cbc:LineExtensionAmount>
    <cac:Item><cac:ClassifiedTaxCategory><cbc:ID>Z</cbc:ID><cbc:Percent>0</cbc:Percent></cac:ClassifiedTaxCategory></cac:Item></cac:InvoiceLine>
</Invoice>`

func values(ms []Match) string {
	var v []string
	for _, m := range ms {
		v = append(v, m.Value)
	}
	return strings.Join(v, ",")
}

func TestQuery(t *testing.T) {
	root, err := Parse([]byte(invoice))
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"/Invoice/cbc:ID":                            "1-1-1",
		"/Invoice/ID":                                "1-1-1",
		"//cac:InvoiceLine/cbc:ID":                   "1,2",
		"cac:InvoiceLine[2]/cbc:LineExtensionAmount": "50.00",
		"//cbc:LineExtensionAmount/@currencyID":      "EUR,EUR",
		"//cbc:EndpointID[@schemeID='9934']":         "12345678901",
		"//cac:InvoiceLine[cbc:ID='2']//cbc:Percent": "0",
		"//cac:ClassifiedTaxCategory[cbc:ID='S']/..": "",
		"//Name":     "Acme d.o.o.",
		"//cbc:Nope": "",
	} {
		ms, err := Query(root, path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if got := values(ms); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	ms, _ := Query(root, "//cac:InvoiceLine[2]/cbc:ID")
	if len(ms) != 1 || ms[0].Path != "/Invoice/cac:InvoiceLine[2]/cbc:ID" {
		t.Fatalf("path: %+v", ms)
	}
	if _, err := Query(root, "//a[@b=c]"); err == nil {
		t.Fatal("unquoted predicate accepted")
	}
}

func TestOutline(t *testing.T) {
	root, _ := Parse([]byte(invoice))
	o := Outline(root, 100)
	for _, w := range []string{"Invoice\n", `  cbc:ID = "1-1-1"`, "  cac:InvoiceLine (×2)", `[schemeID=9934]`, `= "12345678901"`} {
		if !strings.Contains(o, w) {
			t.Errorf("outline lacks %q:\n%s", w, o)
		}
	}
	if _, err := Parse([]byte("<a><b></a>")); err == nil {
		t.Fatal("broken XML accepted")
	}
	win := []byte("<?xml version=\"1.0\" encoding=\"windows-1250\"?><a>\x9aum</a>")
	r, err := Parse(win)
	if err != nil || r.Children[0].Text != "šum" {
		t.Fatalf("windows-1250: %v %q", err, r.Children[0].Text)
	}
}
