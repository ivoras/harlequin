package memory

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ivoras/harlequin/internal/shared/types"
)

// Project memories live in a project's own database with the "p." composite-id
// scope. They reuse the generic per-file memDB machinery (insert/list/delete/
// search); a project session fuses them with the user + shared scopes via
// SearchFused. Anyone in the project may read and write them.

func (s *Store) projectMem(projDB *sql.DB) memDB { return memDB{db: projDB, scope: scopeProject} }

// ProjectAdd stores a memory in the project database (FTS + vector indexed) and
// returns it. userID is the acting member (recorded as provenance via source).
func (s *Store) ProjectAdd(ctx context.Context, projDB *sql.DB, content, source string) (*types.Memory, error) {
	if source == "" {
		source = "manual"
	}
	blob, err := s.embed(ctx, content)
	if err != nil {
		return nil, fmt.Errorf("embed memory content: %w", err)
	}
	id, err := s.projectMem(projDB).insert(ctx, content, source, nil, blob)
	if err != nil {
		return nil, fmt.Errorf("insert memory row: %w", err)
	}
	return &types.Memory{
		ID: encodeID(scopeProject, id), Scope: scopeProject, Content: content,
		Source: source, CreatedAt: time.Now().UTC(),
	}, nil
}

// ProjectList returns the project's memories (newest/pinned first).
func (s *Store) ProjectList(ctx context.Context, projDB *sql.DB, limit int) ([]types.Memory, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.projectMem(projDB).list(ctx, 0, limit)
}

// ProjectDelete removes a project memory by its composite id ("p.<n>").
func (s *Store) ProjectDelete(ctx context.Context, projDB *sql.DB, id string) error {
	scope, local, ok := decodeID(id)
	if !ok || scope != scopeProject {
		return ErrNotFound
	}
	found, err := s.projectMem(projDB).deleteMemory(ctx, local)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	return nil
}
