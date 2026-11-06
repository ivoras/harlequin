package api

import (
	"net/http"
	"testing"

	"github.com/ivoras/harlequin/internal/shared/types"
)

func TestCheckRemoveMember(t *testing.T) {
	p := &types.Project{CreatedBy: 1, Members: []types.ProjectMember{{UserID: 1}, {UserID: 2}, {UserID: 3}}}
	creator := &types.User{ID: 1, Role: types.RoleUser}
	member := &types.User{ID: 2, Role: types.RoleUser}
	admin := &types.User{ID: 9, Role: types.RoleAdmin} // not a member
	cases := []struct {
		name   string
		actor  *types.User
		uid    int64
		status int // 0 = allowed
	}{
		{"creator removes member", creator, 3, 0},
		{"admin non-member removes member", admin, 2, 0},
		{"plain member can't remove others", member, 3, http.StatusForbidden},
		{"creator can't be removed", admin, 1, http.StatusBadRequest},
		{"no self-removal (use depart)", creator, 1, http.StatusBadRequest},
		{"not a member", creator, 42, http.StatusNotFound},
	}
	for _, c := range cases {
		m, status, msg := checkRemoveMember(p, c.actor, c.uid)
		if status != c.status || (c.status == 0) != (msg == "") {
			t.Errorf("%s: status %d (%q), want %d", c.name, status, msg, c.status)
		}
		if c.status == 0 && (m == nil || m.UserID != c.uid) {
			t.Errorf("%s: wrong member %+v", c.name, m)
		}
	}
}
