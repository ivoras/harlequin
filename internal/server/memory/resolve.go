package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrAmbiguousRef is returned when a slot key matches more than one memory.
var ErrAmbiguousRef = errors.New("ambiguous memory reference")

// ResolveRef maps a composite id (p.N / s.N / u.N) or a normalized slot_key to
// the memory id to act on. Slot keys resolve across scopes in precedence order
// (project → shared → user): the first scope holding the key wins, so a deeper
// scope shadows a shallower one. Composite ids must exist and be visible.
func (s *Store) ResolveRef(ctx context.Context, userDB, projDB *sql.DB, ref string, userID int64) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", ErrNotFound
	}
	if scope, _, ok := decodeID(ref); ok {
		if scope == scopeProject {
			if projDB == nil {
				return "", ErrNotFound
			}
			return ref, nil
		}
		if _, err := s.Get(ctx, userDB, ref, userID); err != nil {
			return "", err
		}
		return ref, nil
	}
	scopes := make([]memDB, 0, 3)
	if projDB != nil {
		scopes = append(scopes, s.projectMem(projDB))
	}
	scopes = append(scopes, s.sharedMem(), s.userMem(userDB))
	for _, m := range scopes {
		var ids []string
		for _, row := range m.slotsForKey(ctx, ref) {
			ids = append(ids, m.encode(row.memoryLocal))
		}
		if len(ids) == 1 {
			return ids[0], nil
		}
		if len(ids) > 1 {
			return "", fmt.Errorf("%w: %s", ErrAmbiguousRef, strings.Join(ids, ", "))
		}
	}
	return "", ErrNotFound
}
