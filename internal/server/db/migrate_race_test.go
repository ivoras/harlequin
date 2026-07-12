package db

import (
	"path/filepath"
	"sync"
	"testing"
)

// TestConcurrentFirstOpen reproduces the "UNIQUE constraint failed:
// schema_migrations.name" race: several connections opening (and therefore
// migrating) the same fresh database file at once, as happens when a newly
// created project's db is hit by two request handlers simultaneously.
func TestConcurrentFirstOpen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "project.db")
	const workers = 6
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := Open(path, Project, 8)
			if err != nil {
				errs <- err
				return
			}
			db.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Open: %v", err)
	}
}
