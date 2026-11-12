package api

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/project"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// handleChatUnread lists, per project the caller belongs to, the team-chat
// messages posted by others since the caller's read marker (kept in their own
// user.db). Projects with nothing unread are left out.
func (s *Server) handleChatUnread(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx := r.Context()
	projects, err := s.Projects.List(ctx, u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	marks := map[int64]int64{}
	if err := s.Storage.WithUser(ctx, u.ID, func(udb *sql.DB) error {
		var e error
		marks, e = project.ChatReadMarks(ctx, udb)
		return e
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := []types.ChatUnreadProject{}
	for _, p := range projects {
		last, hasMark := marks[p.ID]
		joined := s.Projects.JoinedAt(ctx, p.ID, u.ID)
		var count int
		var msgs []types.ChatUnreadMessage
		if err := s.Storage.WithProjectReadOnly(ctx, p.ID, func(pdb *sql.DB) error {
			var e error
			count, msgs, e = s.Projects.ChatUnread(ctx, pdb, u.ID, last, hasMark, joined)
			return e
		}); err != nil || count == 0 {
			continue // a project whose database cannot be read has nothing to show
		}
		out = append(out, types.ChatUnreadProject{ProjectID: p.ID, ProjectName: p.Name, Count: count, Messages: msgs})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleChatRead records that the caller has seen the project's chat up to
// last_id (clamped to the newest message, so a bad id cannot hide later ones).
func (s *Server) handleChatRead(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.projectMember(w, r)
	if !ok {
		return
	}
	var in struct {
		LastID int64 `json:"last_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&in); err != nil || in.LastID < 0 {
		writeErr(w, http.StatusBadRequest, "last_id is required")
		return
	}
	ctx := r.Context()
	err := s.Storage.WithProjectReadOnly(ctx, id, func(pdb *sql.DB) error {
		max, e := project.MaxChatID(ctx, pdb)
		if in.LastID > max {
			in.LastID = max
		}
		return e
	})
	if err == nil {
		err = s.Storage.WithUser(ctx, u.ID, func(udb *sql.DB) error {
			return project.MarkChatRead(ctx, udb, id, in.LastID)
		})
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"last_read_id": in.LastID})
}
