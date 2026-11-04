package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ivoras/harlequin/internal/server/db"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	d, err := db.Open(":memory:", db.System, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return NewStore(d)
}

// Re-registering must not hand out fresh code guesses: attempts carry over
// while the previous code is live, and codes can't be requested back to back.
func TestReRegisterKeepsAttemptsAndCooldown(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.StartRegistration(ctx, "a@x.io", "password1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRegistration(ctx, "a@x.io", "password1"); !errors.Is(err, ErrCodeTooSoon) {
		t.Fatalf("immediate re-register = %v, want ErrCodeTooSoon", err)
	}
	// Age the pending row past the cooldown, with 4 failed guesses recorded.
	if _, err := s.db.Exec(`UPDATE pending_registrations SET attempts = 4, expires_at = ?`, time.Now().Add(registrationCodeTTL-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRegistration(ctx, "a@x.io", "password1"); err != nil {
		t.Fatal(err)
	}
	var attempts int
	s.db.QueryRow(`SELECT attempts FROM pending_registrations`).Scan(&attempts)
	if attempts != 4 {
		t.Fatalf("attempts = %d, want 4 (not reset by re-register)", attempts)
	}
	// Once the old code has expired the counter starts over.
	s.db.Exec(`UPDATE pending_registrations SET attempts = 5, expires_at = ?`, time.Now().Add(-time.Minute))
	if _, err := s.StartRegistration(ctx, "a@x.io", "password1"); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow(`SELECT attempts FROM pending_registrations`).Scan(&attempts)
	if attempts != 0 {
		t.Fatalf("attempts after expiry = %d, want 0", attempts)
	}
}

func TestTokensExpireAndArePruned(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "u@x.io", "password1", "user"); err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.Login(ctx, "u@x.io", "password1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserForToken(ctx, tok); err != nil {
		t.Fatalf("fresh token rejected: %v", err)
	}
	s.db.Exec(`UPDATE api_tokens SET expires_at = ?`, time.Now().Add(-time.Hour))
	if _, err := s.UserForToken(ctx, tok); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired token = %v, want ErrUnauthorized", err)
	}
	s.PruneTokens(ctx)
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM api_tokens`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d tokens left after prune", n)
	}
}
