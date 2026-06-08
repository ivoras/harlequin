// Package auth handles password hashing, user management, API token issuance and
// verification, and the bearer-token HTTP middleware.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/shared/types"
	"golang.org/x/crypto/bcrypt"
)

// Errors returned by the store.
var (
	ErrInvalidCredentials  = errors.New("invalid credentials")
	ErrUserExists          = errors.New("user already exists")
	ErrUserNotFound        = errors.New("user not found")
	ErrUnauthorized        = errors.New("unauthorized")
	ErrNoPendingRegistration = errors.New("no pending registration")
	ErrCodeExpired         = errors.New("verification code expired")
	ErrTooManyAttempts     = errors.New("too many verification attempts")
)

// Registration tuning.
const (
	registrationCodeTTL = 15 * time.Minute
	maxVerifyAttempts   = 5
)

// ctxKey is the private type for context keys.
type ctxKey int

const userCtxKey ctxKey = iota

// Store provides authentication operations backed by the database.
type Store struct {
	db *sql.DB
}

// NewStore constructs an auth Store.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// CreateUser creates a user (identified by email) with a bcrypt-hashed password.
func (s *Store) CreateUser(ctx context.Context, email, password, role string) (*types.User, error) {
	if role == "" {
		role = types.RoleUser
	}
	if role != types.RoleOwner && role != types.RoleAdmin && role != types.RoleUser {
		return nil, fmt.Errorf("invalid role %q", role)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users(email, password_hash, role) VALUES (?, ?, ?)`,
		email, string(hash), role)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrUserExists
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &types.User{ID: id, Email: email, Role: role, CreatedAt: time.Now()}, nil
}

// ChangePassword sets a new bcrypt hash for the account with this email and
// revokes all its API tokens.
func (s *Store) ChangePassword(ctx context.Context, email, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE email = ?`, string(hash), email)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUserNotFound
	}
	_, _ = s.db.ExecContext(ctx,
		`DELETE FROM api_tokens WHERE user_id = (SELECT id FROM users WHERE email = ?)`, email)
	return nil
}

// ListUsers returns all accounts ordered by id.
func (s *Store) ListUsers(ctx context.Context) ([]types.User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, email, role, created_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.User
	for rows.Next() {
		var u types.User
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteUser removes an account (cascading its API tokens) and returns its id so
// the caller can clean up the user's per-user database and files.
func (s *Store) DeleteUser(ctx context.Context, email string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, email).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrUserNotFound
	}
	if err != nil {
		return 0, err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id); err != nil {
		return 0, err
	}
	return id, nil
}

// Login verifies credentials (by email) and issues a new API token (returning the
// plaintext).
func (s *Store) Login(ctx context.Context, email, password string) (string, *types.User, error) {
	var (
		id   int64
		hash string
		role string
		ct   time.Time
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, password_hash, role, created_at FROM users WHERE email = ?`, email).
		Scan(&id, &hash, &role, &ct)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrInvalidCredentials
	}
	if err != nil {
		return "", nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return "", nil, ErrInvalidCredentials
	}

	token, err := s.issueToken(ctx, id)
	if err != nil {
		return "", nil, err
	}
	user := &types.User{ID: id, Email: email, Role: role, CreatedAt: ct}
	return token, user, nil
}

// issueToken creates a random token, stores its SHA-256, and returns the plaintext.
func (s *Store) issueToken(ctx context.Context, userID int64) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256Hex(token)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO api_tokens(user_id, token_sha256) VALUES (?, ?)`, userID, sum); err != nil {
		return "", err
	}
	return token, nil
}

// StartRegistration records (or replaces) a pending self-registration for email
// and returns a freshly generated plaintext magic code for the caller to deliver.
// It fails with ErrUserExists if an account already uses that email. The caller is
// responsible for validating the email's format and for emailing the returned code.
func (s *Store) StartRegistration(ctx context.Context, email, password string) (string, error) {
	var existing int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, email).Scan(&existing)
	if err == nil {
		return "", ErrUserExists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	pwHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	code, err := generateCode()
	if err != nil {
		return "", err
	}
	codeHash := sha256Hex(code)
	expires := time.Now().Add(registrationCodeTTL)

	// Upsert so re-requesting before verifying refreshes the code, password and
	// expiry, and resets the attempt counter.
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO pending_registrations(email, password_hash, code_hash, attempts, expires_at)
		 VALUES (?, ?, ?, 0, ?)
		 ON CONFLICT(email) DO UPDATE SET
		   password_hash = excluded.password_hash,
		   code_hash     = excluded.code_hash,
		   attempts      = 0,
		   expires_at    = excluded.expires_at,
		   created_at    = CURRENT_TIMESTAMP`,
		email, string(pwHash), codeHash, expires)
	if err != nil {
		return "", err
	}
	return code, nil
}

// VerifyRegistration completes a pending registration: on a correct, unexpired
// code it creates the user (role "user"), removes the pending row, and issues a
// login token. Wrong codes increment the attempt counter and return
// ErrInvalidCredentials; the row is locked out after maxVerifyAttempts.
func (s *Store) VerifyRegistration(ctx context.Context, email, code string) (string, *types.User, error) {
	var (
		pwHash   string
		codeHash string
		attempts int
		expires  time.Time
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT password_hash, code_hash, attempts, expires_at FROM pending_registrations WHERE email = ?`, email).
		Scan(&pwHash, &codeHash, &attempts, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNoPendingRegistration
	}
	if err != nil {
		return "", nil, err
	}
	if attempts >= maxVerifyAttempts {
		return "", nil, ErrTooManyAttempts
	}
	if time.Now().After(expires) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM pending_registrations WHERE email = ?`, email)
		return "", nil, ErrCodeExpired
	}
	if sha256Hex(code) != codeHash {
		_, _ = s.db.ExecContext(ctx,
			`UPDATE pending_registrations SET attempts = attempts + 1 WHERE email = ?`, email)
		return "", nil, ErrInvalidCredentials
	}

	// Code is correct: materialise the account from the stored password hash.
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users(email, password_hash, role) VALUES (?, ?, ?)`,
		email, pwHash, types.RoleUser)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return "", nil, ErrUserExists
		}
		return "", nil, err
	}
	id, _ := res.LastInsertId()
	_, _ = s.db.ExecContext(ctx, `DELETE FROM pending_registrations WHERE email = ?`, email)

	token, err := s.issueToken(ctx, id)
	if err != nil {
		return "", nil, err
	}
	user := &types.User{ID: id, Email: email, Role: types.RoleUser, CreatedAt: time.Now()}
	return token, user, nil
}

// generateCode returns a random 6-digit numeric verification code.
func generateCode() (string, error) {
	max := big.NewInt(1000000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// Logout deletes the token used in this request.
func (s *Store) Logout(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE token_sha256 = ?`, sha256Hex(token))
	return err
}

// UserForToken returns the user owning the given plaintext token.
func (s *Store) UserForToken(ctx context.Context, token string) (*types.User, error) {
	var (
		u         types.User
		expiresAt sql.NullTime
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT u.id, u.email, u.role, u.created_at, t.expires_at
		 FROM api_tokens t JOIN users u ON u.id = t.user_id
		 WHERE t.token_sha256 = ?`, sha256Hex(token)).
		Scan(&u.ID, &u.Email, &u.Role, &u.CreatedAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	if expiresAt.Valid && expiresAt.Time.Before(time.Now()) {
		return nil, ErrUnauthorized
	}
	return &u, nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Middleware returns an http middleware that authenticates the bearer token and
// injects the user into the request context.
func (s *Store) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeUnauthorized(w)
			return
		}
		user, err := s.UserForToken(r.Context(), token)
		if err != nil {
			writeUnauthorized(w)
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[len("bearer "):])
	}
	return ""
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
}

// UserFromContext returns the authenticated user from the request context.
func UserFromContext(ctx context.Context) (*types.User, bool) {
	u, ok := ctx.Value(userCtxKey).(*types.User)
	return u, ok
}
