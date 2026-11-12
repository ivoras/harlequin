package project

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/shared/types"
)

const (
	// unreadScan bounds how many unread messages are counted per project.
	unreadScan = 200
	// UnreadShown is how many of the newest unread messages are returned.
	UnreadShown = 10
	// snippetWords is the abridged length of an unread message.
	snippetWords = 15
)

// Abridge cuts text to its first n words (whitespace collapsed), adding "…".
func Abridge(text string, n int) string {
	w := strings.Fields(text)
	if len(w) <= n {
		return strings.Join(w, " ")
	}
	return strings.Join(w[:n], " ") + "…"
}

// ChatReadMarks returns the user's last-read chat message id per project
// (userDB); projects without a row are absent.
func ChatReadMarks(ctx context.Context, userDB *sql.DB) (map[int64]int64, error) {
	rows, err := userDB.QueryContext(ctx, `SELECT project_id, last_read_id FROM project_chat_reads`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var p, id int64
		if err := rows.Scan(&p, &id); err != nil {
			return nil, err
		}
		out[p] = id
	}
	return out, rows.Err()
}

// ChatUnread returns the count and the newest few (chronological, abridged)
// chat messages in projDB that userID has not read: those after lastRead and
// not their own. Without a read mark (hasMark false) only messages from the
// time they joined the project count, so a new marker table does not flag the
// whole history as unread.
func (s *Store) ChatUnread(ctx context.Context, projDB *sql.DB, userID, lastRead int64, hasMark bool, joined time.Time) (int, []types.ChatUnreadMessage, error) {
	rows, err := projDB.QueryContext(ctx,
		`SELECT id, user_id, content, created_at FROM chat_messages WHERE id > ? AND user_id != ? ORDER BY id DESC LIMIT ?`,
		lastRead, userID, unreadScan)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	var newest []types.ChatUnreadMessage // newest first
	count := 0
	for rows.Next() {
		var m types.ChatUnreadMessage
		var uid int64
		var content string
		if err := rows.Scan(&m.ID, &uid, &content, &m.CreatedAt); err != nil {
			return 0, nil, err
		}
		if !hasMark && m.CreatedAt.Before(joined) {
			continue
		}
		count++
		if len(newest) < UnreadShown {
			m.Snippet = Abridge(content, snippetWords)
			m.Email = s.emailByID(ctx, uid)
			newest = append(newest, m)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	for i, j := 0, len(newest)-1; i < j; i, j = i+1, j-1 {
		newest[i], newest[j] = newest[j], newest[i]
	}
	return count, newest, nil
}

// MaxChatID is the id of the project's newest chat message (0 if none).
func MaxChatID(ctx context.Context, projDB *sql.DB) (int64, error) {
	var id int64
	err := projDB.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM chat_messages`).Scan(&id)
	return id, err
}

// MarkChatRead raises the user's read marker for the project to lastID. It
// never lowers it, so an out-of-order or stale client call is harmless.
func MarkChatRead(ctx context.Context, userDB *sql.DB, projectID, lastID int64) error {
	_, err := userDB.ExecContext(ctx,
		`INSERT INTO project_chat_reads(project_id, last_read_id) VALUES (?, ?)
		 ON CONFLICT(project_id) DO UPDATE SET
		   last_read_id = MAX(last_read_id, excluded.last_read_id), updated_at = CURRENT_TIMESTAMP`,
		projectID, lastID)
	return err
}

// JoinedAt is when userID joined the project (zero if not a member).
func (s *Store) JoinedAt(ctx context.Context, projectID, userID int64) time.Time {
	var t time.Time
	_ = s.system.QueryRowContext(ctx,
		`SELECT joined_at FROM project_members WHERE project_id = ? AND user_id = ?`, projectID, userID).Scan(&t)
	return t
}
