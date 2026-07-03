package docling

import (
	"strings"
	"testing"
	"unicode/utf8"
)

const sampleDoclingJSON = `{
  "schema_name": "DoclingDocument",
  "body": {"children": [
    {"$ref": "#/texts/0"},
    {"$ref": "#/groups/0"},
    {"$ref": "#/tables/0"},
    {"$ref": "#/texts/3"}
  ]},
  "texts": [
    {"label": "section_header", "prov": [{"page_no": 1}], "text": "Article 1", "children": []},
    {"label": "text", "prov": [{"page_no": 1}], "text": "First body paragraph.", "children": []},
    {"label": "list_item", "prov": [{"page_no": 2}], "text": "a bullet", "children": []},
    {"label": "text", "prov": [{"page_no": 4}], "text": "Last paragraph on page four.", "children": []}
  ],
  "groups": [
    {"children": [{"$ref": "#/texts/1"}, {"$ref": "#/texts/2"}]}
  ],
  "tables": [
    {"prov": [{"page_no": 2}], "children": [], "data": {"grid": [
      [{"text": "fee"}, {"text": "0.50 EUR"}],
      [{"text": "waiver"}, {"text": "below 20 EUR"}]
    ]}}
  ]
}`

func TestFlattenDoclingJSON(t *testing.T) {
	text, starts, err := flattenDoclingJSON([]byte(sampleDoclingJSON))
	if err != nil {
		t.Fatal(err)
	}
	want := "## Article 1\n\nFirst body paragraph.\n\n- a bullet\n\nfee | 0.50 EUR\nwaiver | below 20 EUR\n\nLast paragraph on page four."
	if text != want {
		t.Fatalf("text:\n%q\nwant:\n%q", text, want)
	}
	// Pages 1-4: page 1 starts at 0; page 2 at the bullet; page 3 (empty) and
	// page 4 both point at the last paragraph.
	if len(starts) != 4 {
		t.Fatalf("want 4 page starts, got %v", starts)
	}
	if starts[0] != 0 {
		t.Fatalf("page 1 must start at 0, got %v", starts)
	}
	bullet := utf8.RuneCountInString(strings.Split(want, "- a bullet")[0])
	if starts[1] != bullet {
		t.Fatalf("page 2 should start at the bullet (%d), got %v", bullet, starts)
	}
	last := utf8.RuneCountInString(strings.Split(want, "Last paragraph")[0])
	if starts[2] != last || starts[3] != last {
		t.Fatalf("pages 3 and 4 should both start at the last paragraph (%d), got %v", last, starts)
	}
}

func TestFlattenCycleGuard(t *testing.T) {
	const cyclic = `{
	  "body": {"children": [{"$ref": "#/groups/0"}]},
	  "texts": [{"label": "text", "prov": [{"page_no": 1}], "text": "once", "children": []}],
	  "groups": [{"children": [{"$ref": "#/texts/0"}, {"$ref": "#/groups/0"}]}],
	  "tables": []
	}`
	text, _, err := flattenDoclingJSON([]byte(cyclic))
	if err != nil {
		t.Fatal(err)
	}
	if text != "once" {
		t.Fatalf("cycle should not duplicate or hang: %q", text)
	}
}
