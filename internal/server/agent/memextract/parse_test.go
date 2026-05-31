package memextract

import "testing"

func TestParseResponse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		in    string
		want  int
		minCF int
		ok    bool
	}{
		{
			name: "empty memories",
			in:   `{"memories":[]}`,
			want: 0, ok: true,
		},
		{
			name: "two scored",
			in:   `{"memories":[{"content":"Prefers Go","confidence":9},{"content":"Likes tea","confidence":8}]}`,
			want: 2, minCF: 8, ok: true,
		},
		{
			name: "org fact shared scope",
			in:   `{"memories":[{"content":"The company name is WoodChucks Inc.","confidence":9,"scope":"shared"}]}`,
			want: 1, minCF: 9, ok: true,
		},
		{
			name: "fence wrapped",
			in:   "```json\n{\"memories\":[{\"content\":\"Uses vim\",\"confidence\":7}]}\n```",
			want: 1, minCF: 7, ok: true,
		},
		{
			name: "rejects meta phrase in content",
			in:   `{"memories":[{"content":"No durable facts extracted.","confidence":9}]}`,
			want: 0, ok: true,
		},
		{
			name: "prose around json",
			in:   "Here is the result:\n{\"memories\":[{\"content\":\"Works at Acme\",\"confidence\":8}]}\nDone.",
			want: 1, minCF: 8, ok: true,
		},
		{
			name: "invalid json",
			in:   "no durable facts extracted.",
			ok:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseResponse(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok=%v want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if len(got) != tc.want {
				t.Fatalf("len=%d want %d: %#v", len(got), tc.want, got)
			}
			if tc.minCF > 0 {
				for _, c := range got {
					if c.Confidence < tc.minCF {
						t.Fatalf("confidence %d < %d for %q", c.Confidence, tc.minCF, c.Content)
					}
				}
			}
			if tc.name == "org fact shared scope" && len(got) == 1 && got[0].Scope != "shared" {
				t.Fatalf("scope=%q want shared", got[0].Scope)
			}
		})
	}
}

func TestNormalizeFact(t *testing.T) {
	t.Parallel()
	rejects := []string{
		"No durable facts extracted.",
		"  - nothing durable to store",
		"none",
		"There are no facts worth remembering.",
	}
	for _, s := range rejects {
		if got := NormalizeFact(s); got != "" {
			t.Errorf("NormalizeFact(%q) = %q, want empty", s, got)
		}
	}
	if got := NormalizeFact("- User prefers PostgreSQL"); got != "User prefers PostgreSQL" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeScope(t *testing.T) {
	t.Parallel()
	if got := NormalizeScope("SHARED"); got != "shared" {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeScope(""); got != "user" {
		t.Fatalf("got %q", got)
	}
}

func TestShouldStore(t *testing.T) {
	t.Parallel()
	resp, ok := ParseResponse(`{"memories":[
		{"content":"Stable preference for dark mode","confidence":9},
		{"content":"Mentioned today's weather","confidence":4},
		{"content":"Uses Neovim","confidence":7}
	]}`)
	if !ok || len(resp) != 3 {
		t.Fatalf("parse: ok=%v len=%d", ok, len(resp))
	}
	stored := 0
	for _, c := range resp {
		if ShouldStore(c) {
			stored++
		}
	}
	if stored != 2 {
		t.Fatalf("would store %d, want 2", stored)
	}
}
