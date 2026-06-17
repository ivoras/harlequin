package api

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"

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

// SweepOnboarding queues the onboarding notification for every user who has no
// user-scoped memories and no existing onboarding notification. Run once at
// server startup so users who registered while an older build was running still
// get onboarded. Best-effort: per-user errors are swallowed so one bad DB does
// not abort the sweep.
func (s *Server) SweepOnboarding(ctx context.Context) {
	if s.Notify == nil || s.Memory == nil {
		return
	}
	_ = s.Storage.EachUser(ctx, func(userID int64, udb *sql.DB) error {
		s.ensureOnboarding(ctx, udb, userID)
		return nil
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
	iface := reqInterface(r)
	err := s.Storage.WithUserReadOnly(r.Context(), u.ID, func(udb *sql.DB) error {
		list, err := s.Notify.ListPending(r.Context(), udb, iface)
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

// handleBroadcastAlert (owner/admin only) delivers a text message as an alert to
// every user — including the sender. Each user gets a pending notification in
// their own database, which their connected clients pick up via push and show in
// the alert box.
func (s *Server) handleBroadcastAlert(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireElevated(w, r); !ok {
		return
	}
	var req types.BroadcastAlertRequest
	if err := decode(r, &req); err != nil || strings.TrimSpace(req.Message) == "" {
		writeErr(w, http.StatusBadRequest, "message required")
		return
	}
	n := types.Notification{Kind: types.NotifyKindAlert, Title: strings.TrimSpace(req.Message)}
	var sent int
	err := s.Storage.EachUser(r.Context(), func(_ int64, udb *sql.DB) error {
		if _, err := s.Notify.Create(r.Context(), udb, n); err != nil {
			return err
		}
		sent++
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"sent": sent})
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
