package api

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// onboardingKind identifies the one-shot onboarding notification.
const onboardingKind = "harlequin-onboarding"

// ensureOnboarding queues the onboarding notification the first time a user with
// no user-scoped memories logs in. The ExistsKind guard makes it one-shot even
// if the user later skips onboarding (memory stays empty). Best-effort: failures
// are swallowed so they never block login.
func (s *Server) ensureOnboarding(ctx context.Context, udb *sql.DB, userID int64) {
	if s.Notify == nil || s.Memory == nil {
		return
	}
	if exists, err := s.Notify.ExistsKind(ctx, udb, onboardingKind); err != nil || exists {
		return
	}
	mems, err := s.Memory.List(ctx, udb, userID, "user", 1)
	if err != nil || len(mems) > 0 {
		return
	}
	_, _ = s.Notify.Create(ctx, udb, types.Notification{
		Kind:        onboardingKind,
		AutoRun:     true,
		Title:       "Welcome to Harlequin",
		Description: "Let's get you set up — I'll ask a few quick questions.",
		Prompt:      "Load the harlequin-onboarding skill with load_skill and follow it to get to know me.",
	})
}

// handleListNotifications returns the caller's pending notifications.
func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	out := []types.Notification{}
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		list, err := s.Notify.ListPending(r.Context(), udb)
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

// handleAckNotification marks a notification delivered (handled by the client).
func (s *Server) handleAckNotification(w http.ResponseWriter, r *http.Request) {
	s.updateNotification(w, r, false)
}

// handleDismissNotification marks a notification dismissed.
func (s *Server) handleDismissNotification(w http.ResponseWriter, r *http.Request) {
	s.updateNotification(w, r, true)
}

func (s *Server) updateNotification(w http.ResponseWriter, r *http.Request, dismiss bool) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	err = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		if dismiss {
			return s.Notify.Dismiss(r.Context(), udb, id)
		}
		return s.Notify.MarkDelivered(r.Context(), udb, id)
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
