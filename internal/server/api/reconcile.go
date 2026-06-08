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
func (s *Server) SweepCrossScopeSlots(ctx context.Context) {
	if s.Memory == nil {
		return
	}
	_ = s.Storage.EachUser(ctx, func(userID int64, udb *sql.DB) error {
		removed, err := s.Memory.ReconcileUserCrossScope(ctx, udb, userID)
		if err != nil {
			log.Printf("memory: cross-scope reconcile for user %d: %v", userID, err)
			return nil
		}
		if len(removed) > 0 {
			log.Printf("memory: removed %d personal memory(ies) duplicating a shared slot for user %d: %v", len(removed), userID, removed)
		}
		return nil
	})
}
