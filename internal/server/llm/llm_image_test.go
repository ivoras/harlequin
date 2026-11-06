package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageMarshalImages(t *testing.T) {
	plain, _ := json.Marshal(Message{Role: "user", Content: "hi"})
	if !strings.Contains(string(plain), `"content":"hi"`) {
		t.Errorf("plain message changed: %s", plain)
	}
	b, _ := json.Marshal(Message{Role: "user", Content: "look", Images: []Image{{Mime: "image/png", Data: []byte{1, 2}}}})
	for _, w := range []string{`"type":"text"`, `"text":"look"`, `"url":"data:image/png;base64,AQI="`} {
		if !strings.Contains(string(b), w) {
			t.Errorf("missing %s in %s", w, b)
		}
	}
	if strings.Count(string(b), `"content"`) != 1 {
		t.Errorf("duplicate content key: %s", b)
	}
}
