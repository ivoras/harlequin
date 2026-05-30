// Package documents implements the org RAG corpus: documents are split into
// chunks, embedded, and indexed for hybrid (FTS5 + vector) search.
package documents

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/ivoras/harlequin/internal/server/embed"
	"github.com/ivoras/harlequin/internal/shared/types"
)

const (
	rrfK         = 60.0
	chunkRunes   = 1200
	chunkOverlap = 150
)

// Store manages documents and chunks.
type Store struct {
	db       *sql.DB
	embedder embed.Embedder
}

// NewStore constructs a documents Store.
func NewStore(db *sql.DB, embedder embed.Embedder) *Store {
	return &Store{db: db, embedder: embedder}
}

// Ingest stores a document, chunks + embeds its content, and indexes the chunks.
func (s *Store) Ingest(ctx context.Context, req types.CreateDocumentRequest, userID int64) (*types.Document, error) {
	mime := req.Mime
	if mime == "" {
		mime = "text/plain"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO documents(title, uri, mime, created_by) VALUES (?, ?, ?, ?)`,
		req.Title, req.URI, mime, userID)
	if err != nil {
		return nil, err
	}
	docID, _ := res.LastInsertId()

	chunks := chunkText(req.Content)
	if len(chunks) > 0 {
		vecs, err := s.embedder.Embed(ctx, chunks)
		if err != nil {
			return nil, fmt.Errorf("embed document: %w", err)
		}
		for i, c := range chunks {
			r, err := tx.ExecContext(ctx,
				`INSERT INTO doc_chunks(document_id, ord, content) VALUES (?, ?, ?)`, docID, i, c)
			if err != nil {
				return nil, err
			}
			chunkID, _ := r.LastInsertId()
			if _, err := tx.ExecContext(ctx, `INSERT INTO doc_chunks_fts(rowid, content) VALUES (?, ?)`, chunkID, c); err != nil {
				return nil, err
			}
			if i < len(vecs) {
				blob, err := sqlite_vec.SerializeFloat32(vecs[i])
				if err != nil {
					return nil, err
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO doc_chunks_vec(rowid, embedding) VALUES (?, ?)`, chunkID, blob); err != nil {
					return nil, err
				}
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &types.Document{ID: docID, Title: req.Title, URI: req.URI, Mime: mime, CreatedBy: userID}, nil
}

// List returns all documents (newest first).
func (s *Store) List(ctx context.Context) ([]types.Document, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, uri, mime, COALESCE(created_by, 0), created_at FROM documents ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Document
	for rows.Next() {
		var d types.Document
		if err := rows.Scan(&d.ID, &d.Title, &d.URI, &d.Mime, &d.CreatedBy, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Delete removes a document and its chunks/index rows.
func (s *Store) Delete(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM doc_chunks WHERE document_id = ?`, id)
	if err != nil {
		return err
	}
	var chunkIDs []int64
	for rows.Next() {
		var cid int64
		if err := rows.Scan(&cid); err != nil {
			rows.Close()
			return err
		}
		chunkIDs = append(chunkIDs, cid)
	}
	rows.Close()
	for _, cid := range chunkIDs {
		_, _ = tx.ExecContext(ctx, `DELETE FROM doc_chunks_fts WHERE rowid = ?`, cid)
		_, _ = tx.ExecContext(ctx, `DELETE FROM doc_chunks_vec WHERE rowid = ?`, cid)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM documents WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Search returns chunks matching the query, fused via RRF.
func (s *Store) Search(ctx context.Context, query string, limit int) ([]types.SearchResult, error) {
	if limit <= 0 {
		limit = 8
	}
	ranks := map[int64]float64{}
	contents := map[int64]string{}

	ftsRows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.content FROM doc_chunks_fts f JOIN doc_chunks c ON c.id = f.rowid
		 WHERE doc_chunks_fts MATCH ? ORDER BY f.rank LIMIT ?`, query, limit*4)
	if err == nil {
		fold(ftsRows, ranks, contents)
	}

	if vecs, err := s.embedder.Embed(ctx, []string{query}); err == nil && len(vecs) == 1 {
		if blob, err := sqlite_vec.SerializeFloat32(vecs[0]); err == nil {
			vecRows, err := s.db.QueryContext(ctx,
				`SELECT c.id, c.content FROM doc_chunks_vec v JOIN doc_chunks c ON c.id = v.rowid
				 WHERE v.embedding MATCH ? AND k = ? ORDER BY v.distance`, blob, limit*4)
			if err == nil {
				fold(vecRows, ranks, contents)
			}
		}
	}

	return topN(ranks, contents, limit), nil
}

func fold(rows *sql.Rows, ranks map[int64]float64, contents map[int64]string) {
	defer rows.Close()
	rank := 0
	for rows.Next() {
		var id int64
		var content string
		if err := rows.Scan(&id, &content); err != nil {
			return
		}
		rank++
		ranks[id] += 1.0 / (rrfK + float64(rank))
		contents[id] = content
	}
}

func topN(ranks map[int64]float64, contents map[int64]string, limit int) []types.SearchResult {
	out := make([]types.SearchResult, 0, len(ranks))
	for id, score := range ranks {
		out = append(out, types.SearchResult{ID: "d." + strconv.FormatInt(id, 10), Content: contents[id], Score: score})
	}
	for i := 0; i < len(out); i++ {
		max := i
		for j := i + 1; j < len(out); j++ {
			if out[j].Score > out[max].Score {
				max = j
			}
		}
		out[i], out[max] = out[max], out[i]
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// chunkText splits text into overlapping rune windows.
func chunkText(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	runes := []rune(text)
	if len(runes) <= chunkRunes {
		return []string{text}
	}
	var chunks []string
	for start := 0; start < len(runes); start += chunkRunes - chunkOverlap {
		end := start + chunkRunes
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
		if end == len(runes) {
			break
		}
	}
	return chunks
}
