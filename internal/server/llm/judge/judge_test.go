package judge

import "testing"

func TestClamp(t *testing.T) {
	t.Parallel()
	cases := map[int]int{-5: 1, 0: 1, 1: 1, 7: 7, 10: 10, 99: 10}
	for in, want := range cases {
		if got := Clamp(in); got != want {
			t.Fatalf("Clamp(%d)=%d want %d", in, got, want)
		}
	}
}

func TestAccept(t *testing.T) {
	t.Parallel()
	if Accept(6) || !Accept(7) || !Accept(10) {
		t.Fatal("unexpected Accept results")
	}
}

func TestParseJSONObject(t *testing.T) {
	t.Parallel()
	raw, ok := ParseJSONObject("```json\n{\"memories\":[]}\n```")
	if !ok || string(raw) != `{"memories":[]}` {
		t.Fatalf("got %q ok=%v", raw, ok)
	}
	_, ok = ParseJSONObject("no json here")
	if ok {
		t.Fatal("expected false")
	}
}
