// Package project manages the project registry — projects, membership, and
// invitations — in the system database, plus each project's chatroom messages in
// that project's own database. Like the session/notify stores it is stateless for
// per-project data (methods take the project DB handle); the system database is
// held since the registry is global.
package project

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/ivoras/harlequin/internal/shared/types"
)

// Invite statuses.
const (
	InvitePending  = "pending"
	InviteAccepted = "accepted"
	InviteDeclined = "declined"
)

var (
	// ErrNotMember is returned when a user is not a member of a project.
	ErrNotMember = errors.New("not a project member")
	// ErrNoInvite is returned when an invite does not exist for the user.
	ErrNoInvite = errors.New("invite not found")
	// ErrUserNotFound is returned when an invited email matches no account.
	ErrUserNotFound = errors.New("user not found")
)

// Store manages the project registry (system DB) and chatroom (project DB).
type Store struct {
	system *sql.DB
}

// NewStore constructs a project Store bound to the system database.
func NewStore(system *sql.DB) *Store { return &Store{system: system} }

// Create inserts a project and makes the creator its first member.
func (s *Store) Create(ctx context.Context, name string, createdBy int64) (*types.Project, error) {
	name = strings.TrimSpace(name)
	tx, err := s.system.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO projects(name, created_by) VALUES (?, ?)`, name, createdBy)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `INSERT INTO project_members(project_id, user_id) VALUES (?, ?)`, id, createdBy); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Get returns a project with its members.
func (s *Store) Get(ctx context.Context, projectID int64) (*types.Project, error) {
	var p types.Project
	err := s.system.QueryRowContext(ctx,
		`SELECT id, name, created_by, created_at FROM projects WHERE id = ?`, projectID).
		Scan(&p.ID, &p.Name, &p.CreatedBy, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, err
	}
	members, err := s.Members(ctx, projectID)
	if err != nil {
		return nil, err
	}
	p.Members = members
	return &p, nil
}

// List returns the projects the user is a member of (most recent first).
func (s *Store) List(ctx context.Context, userID int64) ([]types.Project, error) {
	rows, err := s.system.QueryContext(ctx,
		`SELECT p.id, p.name, p.created_by, p.created_at
		 FROM projects p JOIN project_members m ON m.project_id = p.id
		 WHERE m.user_id = ? ORDER BY LOWER(p.name), p.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Project
	for rows.Next() {
		var p types.Project
		if err := rows.Scan(&p.ID, &p.Name, &p.CreatedBy, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// IsMember reports whether the user belongs to the project.
func (s *Store) IsMember(ctx context.Context, projectID, userID int64) (bool, error) {
	var n int
	err := s.system.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM project_members WHERE project_id = ? AND user_id = ?`, projectID, userID).Scan(&n)
	return n > 0, err
}

// Members returns the project's members with their emails.
func (s *Store) Members(ctx context.Context, projectID int64) ([]types.ProjectMember, error) {
	rows, err := s.system.QueryContext(ctx,
		`SELECT m.user_id, u.email, m.joined_at
		 FROM project_members m JOIN users u ON u.id = m.user_id
		 WHERE m.project_id = ? ORDER BY m.joined_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.ProjectMember
	for rows.Next() {
		var m types.ProjectMember
		if err := rows.Scan(&m.UserID, &m.Email, &m.JoinedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddMember adds a user to a project (idempotent).
func (s *Store) AddMember(ctx context.Context, projectID, userID int64) error {
	_, err := s.system.ExecContext(ctx,
		`INSERT OR IGNORE INTO project_members(project_id, user_id) VALUES (?, ?)`, projectID, userID)
	return err
}

// RemoveMember removes a user's membership of a project (the user departs).
// Idempotent: removing a non-member is a no-op.
func (s *Store) RemoveMember(ctx context.Context, projectID, userID int64) error {
	_, err := s.system.ExecContext(ctx,
		`DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, projectID, userID)
	return err
}

// UserIDByEmail resolves an email to a user id (for invitations).
func (s *Store) UserIDByEmail(ctx context.Context, email string) (int64, error) {
	var id int64
	err := s.system.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, email).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrUserNotFound
	}
	return id, err
}

// Invite records a pending invitation (idempotent on (project, invitee)). Returns
// the invited user id so the caller can notify them.
func (s *Store) Invite(ctx context.Context, projectID, invitedUserID, invitedBy int64) error {
	_, err := s.system.ExecContext(ctx,
		`INSERT INTO project_invites(project_id, invited_user_id, invited_by, status)
		 VALUES (?, ?, ?, 'pending')
		 ON CONFLICT(project_id, invited_user_id) DO UPDATE SET
		   invited_by = excluded.invited_by, status = 'pending', created_at = CURRENT_TIMESTAMP`,
		projectID, invitedUserID, invitedBy)
	return err
}

// ListInvites returns the user's pending invitations (project name + inviter email).
func (s *Store) ListInvites(ctx context.Context, userID int64) ([]types.ProjectInvite, error) {
	rows, err := s.system.QueryContext(ctx,
		`SELECT i.id, i.project_id, p.name, u.email, i.status, i.created_at
		 FROM project_invites i
		 JOIN projects p ON p.id = i.project_id
		 JOIN users u ON u.id = i.invited_by
		 WHERE i.invited_user_id = ? AND i.status = 'pending'
		 ORDER BY i.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.ProjectInvite
	for rows.Next() {
		var inv types.ProjectInvite
		if err := rows.Scan(&inv.ID, &inv.ProjectID, &inv.ProjectName, &inv.InvitedBy, &inv.Status, &inv.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// Accept marks the user's invite accepted and adds them as a member.
func (s *Store) Accept(ctx context.Context, inviteID, userID int64) (int64, error) {
	var projectID int64
	err := s.system.QueryRowContext(ctx,
		`SELECT project_id FROM project_invites WHERE id = ? AND invited_user_id = ? AND status = 'pending'`,
		inviteID, userID).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNoInvite
	}
	if err != nil {
		return 0, err
	}
	tx, err := s.system.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE project_invites SET status = 'accepted' WHERE id = ?`, inviteID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_members(project_id, user_id) VALUES (?, ?)`, projectID, userID); err != nil {
		return 0, err
	}
	return projectID, tx.Commit()
}

// Decline marks the user's invite declined.
func (s *Store) Decline(ctx context.Context, inviteID, userID int64) error {
	_, err := s.system.ExecContext(ctx,
		`UPDATE project_invites SET status = 'declined' WHERE id = ? AND invited_user_id = ? AND status = 'pending'`,
		inviteID, userID)
	return err
}

// MoveSessionToProject moves a personal session (and all its messages) from the
// owner's user DB into the project DB, recording the original owner, and deletes
// it from the user DB. Returns the new project-session id. The two writes aren't
// one atomic transaction (separate sqlite files); the user-side delete runs last,
// so a failure leaves the source intact rather than losing data.
func (s *Store) MoveSessionToProject(ctx context.Context, userDB, projDB *sql.DB, userSessionID, ownerID int64) (int64, error) {
	// Read the session header from the user DB.
	var title, api, iface string
	var hat sql.NullString
	var createdAt sql.NullString
	err := userDB.QueryRowContext(ctx,
		`SELECT title, hat, api, interface, created_at FROM sessions WHERE id = ?`, userSessionID).
		Scan(&title, &hat, &api, &iface, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, sql.ErrNoRows
	}
	if err != nil {
		return 0, err
	}

	// Insert into the project DB (new autoincrement id), preserving created_at.
	res, err := projDB.ExecContext(ctx,
		`INSERT INTO sessions(owner_user_id, title, hat, api, interface, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, COALESCE(?, CURRENT_TIMESTAMP), CURRENT_TIMESTAMP)`,
		ownerID, title, hat, api, iface, createdAt)
	if err != nil {
		return 0, err
	}
	newID, _ := res.LastInsertId()

	// Copy messages in order.
	rows, err := userDB.QueryContext(ctx,
		`SELECT role, content, tool_calls, tool_call_id, name, created_at
		 FROM messages WHERE session_id = ? ORDER BY id ASC`, userSessionID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var role, content string
		var toolCalls, toolCallID, name, createdAt sql.NullString
		if err := rows.Scan(&role, &content, &toolCalls, &toolCallID, &name, &createdAt); err != nil {
			return 0, err
		}
		if _, err := projDB.ExecContext(ctx,
			`INSERT INTO messages(session_id, role, content, tool_calls, tool_call_id, name, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, COALESCE(?, CURRENT_TIMESTAMP))`,
			newID, role, content, toolCalls, toolCallID, name, createdAt); err != nil {
			return 0, err
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Remove from the user DB (messages cascade).
	if _, err := userDB.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, userSessionID); err != nil {
		return 0, err
	}
	return newID, nil
}

// --- chatroom (project DB) ---

// AddChatMessage appends a chatroom message and returns it (with the author email
// resolved from the system DB).
func (s *Store) AddChatMessage(ctx context.Context, projDB *sql.DB, userID int64, content string) (*types.ChatMessage, error) {
	res, err := projDB.ExecContext(ctx, `INSERT INTO chat_messages(user_id, content) VALUES (?, ?)`, userID, content)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	var m types.ChatMessage
	err = projDB.QueryRowContext(ctx, `SELECT id, user_id, content, created_at FROM chat_messages WHERE id = ?`, id).
		Scan(&m.ID, &m.UserID, &m.Content, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	m.Email = s.emailByID(ctx, m.UserID)
	return &m, nil
}

// ChatMessages returns the most recent chatroom messages in chronological order.
func (s *Store) ChatMessages(ctx context.Context, projDB *sql.DB, limit int) ([]types.ChatMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := projDB.QueryContext(ctx,
		`SELECT id, user_id, content, created_at FROM chat_messages ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.ChatMessage
	for rows.Next() {
		var m types.ChatMessage
		if err := rows.Scan(&m.ID, &m.UserID, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.Email = s.emailByID(ctx, m.UserID)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse to chronological order (oldest first).
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (s *Store) emailByID(ctx context.Context, userID int64) string {
	var email string
	_ = s.system.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ?`, userID).Scan(&email)
	return email
}
