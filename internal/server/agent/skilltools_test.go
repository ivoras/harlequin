package agent

import (
	"strings"
	"testing"

	"database/sql"
)

func TestSkillScope(t *testing.T) {
	user := &runContext{}
	admin := &runContext{canShareMemory: true, projectDB: &sql.DB{}}
	for _, c := range []struct {
		rc    *runContext
		scope string
		want  string // label, or "!" for an error
	}{
		{user, "", "personal"}, {user, "user", "personal"}, {user, "shared", "!"}, {user, "project", "!"},
		{admin, "shared", "shared"}, {admin, "project", "project"}, {admin, "bogus", "!"},
	} {
		label, msg := skillScope(c.rc, c.scope)
		if (c.want == "!") != (msg != "") || (c.want != "!" && label != c.want) {
			t.Errorf("skillScope(%q) = %q, %q; want %s", c.scope, label, msg, c.want)
		}
	}
	if _, msg := skillScope(user, "shared"); !strings.Contains(msg, "owner or admin") {
		t.Errorf("unexpected message %q", msg)
	}
}
