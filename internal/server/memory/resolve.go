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

// ResolveRef maps a composite id (u.N / s.N) or a normalized slot_key to the
// memory id to act on. Composite ids must exist and be visible to the user.
func (s *Store) ResolveRef(ctx context.Context, userDB *sql.DB, ref string, userID int64) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", ErrNotFound
	}
	if _, _, ok := decodeID(ref); ok {
		if _, err := s.Get(ctx, userDB, ref, userID); err != nil {
			return "", err
		}
		return ref, nil
	}
	var ids []string
	for _, m := range []memDB{s.userMem(userDB), s.sharedMem()} {
		for _, row := range m.slotsForKey(ctx, ref) {
			ids = append(ids, m.encode(row.memoryLocal))
		}
	}
	if len(ids) == 0 {
		return "", ErrNotFound
	}
	if len(ids) > 1 {
		return "", fmt.Errorf("%w: %s", ErrAmbiguousRef, strings.Join(ids, ", "))
	}
	return ids[0], nil
}
