package skills_test

import (
	"bytes"
	"log"
	"strings"
	"testing"

	harlequin "github.com/ivoras/harlequin"
	"github.com/ivoras/harlequin/internal/server/skills"
)

func TestDeployLogsFiles(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	if err := skills.Deploy(harlequin.BakedFS(), "skills", dir+"/skills", dir); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	t.Logf("log:\n%s", out)
	if !strings.Contains(out, "skills: deployed") {
		t.Errorf("expected a 'skills: deployed' log line, got:\n%s", out)
	}
	if !strings.Contains(out, "system_prompt.md") {
		t.Errorf("expected system_prompt.md in deploy log, got:\n%s", out)
	}
}
