package skills

import "testing"

const sample = `---
name: example-greeter
description: Greets a person.
tools:
  - name: roll_dice
    description: Roll dice.
    parameters:
      type: object
      properties:
        n:
          type: integer
    run: |
      return 1 + args.n;
---
# Example Greeter
Body here with <?js print(ctx.user); ?>.
`

func TestParseFrontmatterTools(t *testing.T) {
	fm, body, err := parseFrontmatter(sample)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if fm.Name != "example-greeter" {
		t.Fatalf("name = %q", fm.Name)
	}
	if len(fm.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(fm.Tools))
	}
	if fm.Tools[0].Name != "roll_dice" {
		t.Fatalf("tool name = %q", fm.Tools[0].Name)
	}
	if fm.Tools[0].Run == "" {
		t.Fatalf("tool run is empty")
	}
	t.Logf("body starts: %.20q", body)
}
