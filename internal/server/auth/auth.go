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
	"net/http"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/shared/types"
	"golang.org/x/crypto/bcrypt"
)

// Errors returned by the store.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUserExists         = errors.New("user already exists")
	ErrUserNotFound       = errors.New("user not found")
	ErrUnauthorized       = errors.New("unauthorized")
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

// CreateUser creates a user with a bcrypt-hashed password.
func (s *Store) CreateUser(ctx context.Context, username, password, role string) (*types.User, error) {
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
		`INSERT INTO users(username, password_hash, role) VALUES (?, ?, ?)`,
		username, string(hash), role)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrUserExists
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &types.User{ID: id, Username: username, Role: role, CreatedAt: time.Now()}, nil
}

// ChangePassword sets a new bcrypt hash for username and revokes all API tokens.
func (s *Store) ChangePassword(ctx context.Context, username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE username = ?`, string(hash), username)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUserNotFound
	}
	_, _ = s.db.ExecContext(ctx,
		`DELETE FROM api_tokens WHERE user_id = (SELECT id FROM users WHERE username = ?)`, username)
	return nil
}

// Login verifies credentials and issues a new API token (returning the plaintext).
func (s *Store) Login(ctx context.Context, username, password string) (string, *types.User, error) {
	var (
		id   int64
		hash string
		role string
		ct   time.Time
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, password_hash, role, created_at FROM users WHERE username = ?`, username).
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
	user := &types.User{ID: id, Username: username, Role: role, CreatedAt: ct}
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
		`SELECT u.id, u.username, u.role, u.created_at, t.expires_at
		 FROM api_tokens t JOIN users u ON u.id = t.user_id
		 WHERE t.token_sha256 = ?`, sha256Hex(token)).
		Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt, &expiresAt)
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
