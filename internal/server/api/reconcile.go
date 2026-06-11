package api

import (
	"context"
	"database/sql"
	"log"
)

// SweepCrossScopeSlots enforces the "an attribute can't live in both shared and
// personal memory" rule for data that predates it: for every user it removes
// personal memories whose slots already exist in shared memory (exact, or
// judge-confirmed same-attribute), keeping the shared value. Best-effort and
// idempotent — once a user has no cross-scope duplicates it does nothing.
//
// The reconcile makes same-attribute judge calls on the chat model, so each
// user's pass runs inside the agent's background-LLM gate: one background job
// at a time, off the model while live turns run, preempted (and retried) if a
// turn starts mid-pass. Ungated, a sweep at server start competed with the
// user's first prompts for the LLM.
func (s *Server) SweepCrossScopeSlots(ctx context.Context) {
	if s.Memory == nil {
		return
	}
	_ = s.Storage.EachUser(ctx, func(userID int64, udb *sql.DB) error {
		reconcile := func(jobCtx context.Context) {
			removed, err := s.Memory.ReconcileUserCrossScope(jobCtx, udb, userID)
			if err != nil {
				log.Printf("memory: cross-scope reconcile for user %d: %v", userID, err)
				return
			}
			if len(removed) > 0 {
				log.Printf("memory: removed %d personal memory(ies) duplicating a shared slot for user %d: %v", len(removed), userID, removed)
			}
		}
		if s.Agent != nil {
			if !s.Agent.RunBackgroundLLM(ctx, reconcile) {
				log.Printf("memory: cross-scope reconcile for user %d skipped, background LLM slot unavailable", userID)
			}
		} else {
			reconcile(ctx)
		}
		return nil
	})
}
