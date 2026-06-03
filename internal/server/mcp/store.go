package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/server/secrets"
	"golang.org/x/oauth2"
)

// ErrNotFound is returned when a server registration does not exist.
var ErrNotFound = errors.New("mcp: server not found")

// Registry persists MCP server registrations and per-user OAuth tokens. Shared
// registrations live in shared.db (held here); user registrations and all OAuth
// tokens live in the caller's user.db (passed per call).
type Registry struct {
	shared *sql.DB
	cipher *secrets.Cipher // may be nil; secret ops then fail closed
}

// NewRegistry constructs a Registry over the shared DB and an optional cipher.
func NewRegistry(shared *sql.DB, cipher *secrets.Cipher) *Registry {
	return &Registry{shared: shared, cipher: cipher}
}

// HasCipher reports whether credential encryption is available.
func (r *Registry) HasCipher() bool { return r.cipher != nil }

func (r *Registry) dbFor(scope string, userDB *sql.DB) (*sql.DB, error) {
	switch scope {
	case ScopeShared:
		return r.shared, nil
	case ScopeUser:
		if userDB == nil {
			return nil, errors.New("mcp: user database required for user scope")
		}
		return userDB, nil
	default:
		return nil, fmt.Errorf("mcp: invalid scope %q", scope)
	}
}

const serverCols = `name, url, transport, auth_type, auth_secret_enc, oauth_meta, enabled, created_by`

func (r *Registry) scanServer(scope string, sc interface {
	Scan(...any) error
}) (Server, error) {
	var (
		s         Server
		authType  string
		secretEnc []byte
		oauthMeta sql.NullString
		enabled   int
		createdBy sql.NullInt64
	)
	if err := sc.Scan(&s.Name, &s.URL, &s.Transport, &authType, &secretEnc, &oauthMeta, &enabled, &createdBy); err != nil {
		return Server{}, err
	}
	s.Scope = scope
	s.AuthType = AuthType(authType)
	s.Enabled = enabled != 0
	s.CreatedBy = createdBy.Int64

	switch s.AuthType {
	case AuthHeader:
		if len(secretEnc) > 0 && r.cipher != nil {
			plain, err := r.cipher.Decrypt(secretEnc)
			if err != nil {
				return Server{}, fmt.Errorf("mcp: decrypt header credential: %w", err)
			}
			var hc struct{ Header, Value string }
			if err := json.Unmarshal(plain, &hc); err != nil {
				return Server{}, err
			}
			s.HeaderName, s.HeaderValue = hc.Header, hc.Value
		}
	case AuthOAuth:
		var m OAuthMeta
		if oauthMeta.Valid && oauthMeta.String != "" {
			if err := json.Unmarshal([]byte(oauthMeta.String), &m); err != nil {
				return Server{}, err
			}
		}
		if len(secretEnc) > 0 && r.cipher != nil {
			if cs, err := r.cipher.DecryptString(secretEnc); err == nil {
				m.ClientSecret = cs
			}
		}
		s.OAuth = &m
	}
	return s, nil
}

// List returns the registrations for a scope (shared, or the user's own).
func (r *Registry) List(ctx context.Context, scope string, userDB *sql.DB) ([]Server, error) {
	db, err := r.dbFor(scope, userDB)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+serverCols+` FROM mcp_servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Server
	for rows.Next() {
		s, err := r.scanServer(scope, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListVisible returns shared registrations followed by the user's own.
func (r *Registry) ListVisible(ctx context.Context, userDB *sql.DB) ([]Server, error) {
	shared, err := r.List(ctx, ScopeShared, nil)
	if err != nil {
		return nil, err
	}
	user, err := r.List(ctx, ScopeUser, userDB)
	if err != nil {
		return nil, err
	}
	return append(shared, user...), nil
}

// Get returns a single registration by scope and name.
func (r *Registry) Get(ctx context.Context, scope, name string, userDB *sql.DB) (Server, error) {
	db, err := r.dbFor(scope, userDB)
	if err != nil {
		return Server{}, err
	}
	row := db.QueryRowContext(ctx, `SELECT `+serverCols+` FROM mcp_servers WHERE name = ?`, name)
	s, err := r.scanServer(scope, row)
	if errors.Is(err, sql.ErrNoRows) {
		return Server{}, ErrNotFound
	}
	return s, err
}

// encodeSecret builds the encrypted auth_secret blob and oauth_meta string for a
// server about to be written.
func (r *Registry) encodeSecret(s Server) (secretEnc []byte, oauthMeta sql.NullString, err error) {
	switch s.AuthType {
	case AuthHeader:
		if s.HeaderValue != "" {
			if r.cipher == nil {
				return nil, oauthMeta, secrets.ErrNoCipher
			}
			b, _ := json.Marshal(struct {
				Header string `json:"header"`
				Value  string `json:"value"`
			}{s.HeaderName, s.HeaderValue})
			if secretEnc, err = r.cipher.Encrypt(b); err != nil {
				return nil, oauthMeta, err
			}
		}
	case AuthOAuth:
		m := s.OAuth
		if m == nil {
			m = &OAuthMeta{}
		}
		if m.ClientSecret != "" {
			if r.cipher == nil {
				return nil, oauthMeta, secrets.ErrNoCipher
			}
			if secretEnc, err = r.cipher.EncryptString(m.ClientSecret); err != nil {
				return nil, oauthMeta, err
			}
		}
		b, _ := json.Marshal(m)
		oauthMeta = sql.NullString{String: string(b), Valid: true}
	}
	return secretEnc, oauthMeta, nil
}

// Create inserts a new registration. Returns ErrExists on a name clash.
func (r *Registry) Create(ctx context.Context, s Server, userDB *sql.DB) error {
	db, err := r.dbFor(s.Scope, userDB)
	if err != nil {
		return err
	}
	if s.Transport == "" {
		s.Transport = "http"
	}
	if s.AuthType == "" {
		s.AuthType = AuthNone
	}
	secretEnc, oauthMeta, err := r.encodeSecret(s)
	if err != nil {
		return err
	}
	enabled := 1
	if !s.Enabled {
		enabled = 0
	}
	_, err = db.ExecContext(ctx, `INSERT INTO mcp_servers
		(name, url, transport, auth_type, auth_secret_enc, oauth_meta, enabled, created_by)
		VALUES (?,?,?,?,?,?,?,?)`,
		s.Name, s.URL, s.Transport, string(s.AuthType), secretEnc, oauthMeta, enabled, s.CreatedBy)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return fmt.Errorf("mcp: server %q already exists", s.Name)
	}
	return err
}

// Update replaces the mutable fields (url, auth, enabled) of an existing server.
func (r *Registry) Update(ctx context.Context, s Server, userDB *sql.DB) error {
	db, err := r.dbFor(s.Scope, userDB)
	if err != nil {
		return err
	}
	secretEnc, oauthMeta, err := r.encodeSecret(s)
	if err != nil {
		return err
	}
	enabled := 1
	if !s.Enabled {
		enabled = 0
	}
	res, err := db.ExecContext(ctx, `UPDATE mcp_servers
		SET url=?, transport=?, auth_type=?, auth_secret_enc=?, oauth_meta=?, enabled=?, updated_at=CURRENT_TIMESTAMP
		WHERE name=?`,
		s.URL, s.Transport, string(s.AuthType), secretEnc, oauthMeta, enabled, s.Name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetEnabled toggles a server on or off.
func (r *Registry) SetEnabled(ctx context.Context, scope, name string, enabled bool, userDB *sql.DB) error {
	db, err := r.dbFor(scope, userDB)
	if err != nil {
		return err
	}
	v := 0
	if enabled {
		v = 1
	}
	res, err := db.ExecContext(ctx, `UPDATE mcp_servers SET enabled=?, updated_at=CURRENT_TIMESTAMP WHERE name=?`, v, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a registration (and, for user scope, its OAuth tokens).
func (r *Registry) Delete(ctx context.Context, scope, name string, userDB *sql.DB) error {
	db, err := r.dbFor(scope, userDB)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `DELETE FROM mcp_servers WHERE name=?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if userDB != nil {
		_, _ = userDB.ExecContext(ctx, `DELETE FROM mcp_oauth_tokens WHERE scope=? AND server_name=?`, scope, name)
	}
	return nil
}

// LoadToken returns the per-user OAuth token for (scope, name), or nil if none.
func (r *Registry) LoadToken(ctx context.Context, userDB *sql.DB, scope, name string) (*oauth2.Token, error) {
	if userDB == nil {
		return nil, errors.New("mcp: user database required")
	}
	var (
		accessEnc, refreshEnc []byte
		tokenType             sql.NullString
		expiry                sql.NullTime
	)
	row := userDB.QueryRowContext(ctx,
		`SELECT access_token_enc, refresh_token_enc, token_type, expiry FROM mcp_oauth_tokens WHERE scope=? AND server_name=?`,
		scope, name)
	if err := row.Scan(&accessEnc, &refreshEnc, &tokenType, &expiry); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if r.cipher == nil {
		return nil, secrets.ErrNoCipher
	}
	tok := &oauth2.Token{TokenType: tokenType.String}
	if len(accessEnc) > 0 {
		s, err := r.cipher.DecryptString(accessEnc)
		if err != nil {
			return nil, err
		}
		tok.AccessToken = s
	}
	if len(refreshEnc) > 0 {
		s, err := r.cipher.DecryptString(refreshEnc)
		if err != nil {
			return nil, err
		}
		tok.RefreshToken = s
	}
	if expiry.Valid {
		tok.Expiry = expiry.Time
	}
	return tok, nil
}

// SaveToken upserts the per-user OAuth token for (scope, name).
func (r *Registry) SaveToken(ctx context.Context, userDB *sql.DB, scope, name string, tok *oauth2.Token) error {
	if userDB == nil {
		return errors.New("mcp: user database required")
	}
	if r.cipher == nil {
		return secrets.ErrNoCipher
	}
	accessEnc, err := r.cipher.EncryptString(tok.AccessToken)
	if err != nil {
		return err
	}
	var refreshEnc []byte
	if tok.RefreshToken != "" {
		if refreshEnc, err = r.cipher.EncryptString(tok.RefreshToken); err != nil {
			return err
		}
	}
	var expiry any
	if !tok.Expiry.IsZero() {
		expiry = tok.Expiry.UTC().Format(time.RFC3339)
	}
	_, err = userDB.ExecContext(ctx, `INSERT INTO mcp_oauth_tokens
		(scope, server_name, access_token_enc, refresh_token_enc, token_type, expiry, updated_at)
		VALUES (?,?,?,?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(scope, server_name) DO UPDATE SET
			access_token_enc=excluded.access_token_enc,
			refresh_token_enc=excluded.refresh_token_enc,
			token_type=excluded.token_type,
			expiry=excluded.expiry,
			updated_at=CURRENT_TIMESTAMP`,
		scope, name, accessEnc, refreshEnc, tok.TokenType, expiry)
	return err
}
