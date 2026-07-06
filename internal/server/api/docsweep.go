package api

import (
	"context"
	"database/sql"
	"log"

	"github.com/ivoras/harlequin/internal/server/documents"
)

// sweepOpeningChunks bounds how much of a document the description repair
// reads — the opening identifies it.
const sweepOpeningChunks = 8

// SweepDocumentDescriptions backfills the catalogue description of any
// document that has none (its at-upload attempt lost to model contention, or
// it predates descriptions). Runs behind the background-LLM gate per document;
// intended for a goroutine at startup.
func (s *Server) SweepDocumentDescriptions(ctx context.Context) {
	if s.Agent == nil || s.Docs == nil {
		return
	}
	repair := func(db *sql.DB, scope string) {
		missing, err := s.Docs.MissingDescriptions(ctx, db)
		if err != nil || len(missing) == 0 {
			return
		}
		for _, d := range missing {
			text, err := s.Docs.OpeningText(ctx, db, d.ID, sweepOpeningChunks)
			if err != nil || text == "" {
				continue
			}
			desc := s.Agent.DescribeDocumentBackground(ctx, d.Title, text)
			if desc == "" {
				continue
			}
			if err := s.Docs.SetDescription(ctx, db, d.ID, desc); err == nil {
				log.Printf("documents: description backfilled for %s doc %d %q", scope, d.ID, d.Title)
			}
		}
	}
	repair(s.Storage.Shared, documents.ScopeShared)
	_ = s.Storage.EachUser(ctx, func(userID int64, udb *sql.DB) error {
		repair(udb, documents.ScopePersonal)
		return nil
	})
	_ = s.Storage.EachProject(ctx, func(projectID int64, pdb *sql.DB) error {
		repair(pdb, documents.ScopeProject)
		return nil
	})
}
