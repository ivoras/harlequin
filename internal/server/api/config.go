package api

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// handleGetConfig returns the caller's full per-user config (key -> value), e.g.
// for registering a Telegram connection.
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	out := map[string]string{}
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		m, err := s.UserConfig.All(r.Context(), udb)
		if err == nil && m != nil {
			out = m
		}
		return err
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetConfig upserts one config key.
func (s *Server) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	u, key, ok := s.configReqKey(w, r)
	if !ok {
		return
	}
	var req types.SetConfigRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		return s.UserConfig.Set(r.Context(), udb, key, req.Value)
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": key, "value": req.Value})
}

// handleDeleteConfig removes one config key.
func (s *Server) handleDeleteConfig(w http.ResponseWriter, r *http.Request) {
	u, key, ok := s.configReqKey(w, r)
	if !ok {
		return
	}
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		return s.UserConfig.Delete(r.Context(), udb, key)
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) configReqKey(w http.ResponseWriter, r *http.Request) (*types.User, string, bool) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return nil, "", false
	}
	key := strings.TrimSpace(chi.URLParam(r, "key"))
	if key == "" {
		writeErr(w, http.StatusBadRequest, "key is required")
		return nil, "", false
	}
	return u, key, true
}
