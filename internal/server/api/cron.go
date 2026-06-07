package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// handleListCron returns the caller's cron jobs.
func (s *Server) handleListCron(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	out := []types.CronJob{}
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		list, err := s.Cron.List(r.Context(), udb)
		if err != nil {
			return err
		}
		if list != nil {
			out = list
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateCron creates a cron job.
func (s *Server) handleCreateCron(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req types.CreateCronJobRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	var job types.CronJob
	var createErr error
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		job, createErr = s.Cron.Create(r.Context(), udb, req)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if createErr != nil {
		writeErr(w, http.StatusBadRequest, createErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// handleGetCron returns one cron job.
func (s *Server) handleGetCron(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.cronReqID(w, r)
	if !ok {
		return
	}
	var job types.CronJob
	var getErr error
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		job, getErr = s.Cron.Get(r.Context(), udb, id)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if errors.Is(getErr, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if getErr != nil {
		writeErr(w, http.StatusInternalServerError, getErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// handleUpdateCron applies a partial update (enable/disable/edit).
func (s *Server) handleUpdateCron(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.cronReqID(w, r)
	if !ok {
		return
	}
	var req types.UpdateCronJobRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	var job types.CronJob
	var updErr error
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		job, updErr = s.Cron.Update(r.Context(), udb, id, req)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if errors.Is(updErr, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if updErr != nil {
		writeErr(w, http.StatusBadRequest, updErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// handleDeleteCron removes a cron job.
func (s *Server) handleDeleteCron(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.cronReqID(w, r)
	if !ok {
		return
	}
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		return s.Cron.Delete(r.Context(), udb, id)
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleRunCron dispatches a job immediately (asynchronously).
func (s *Server) handleRunCron(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.cronReqID(w, r)
	if !ok {
		return
	}
	if s.CronSched == nil {
		writeErr(w, http.StatusServiceUnavailable, "scheduler not running")
		return
	}
	if err := s.CronSched.RunNow(r.Context(), u.ID, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

// cronReqID extracts the authenticated user and the {id} path param.
func (s *Server) cronReqID(w http.ResponseWriter, r *http.Request) (*types.User, int64, bool) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return nil, 0, false
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return nil, 0, false
	}
	return u, id, true
}
