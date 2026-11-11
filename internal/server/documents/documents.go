// Package documents implements the org RAG corpus: documents are split into
// chunks, embedded, and indexed for hybrid (FTS5 + vector) search.
package documents

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/ivoras/harlequin/internal/server/documents/searchquery"
	"github.com/ivoras/harlequin/internal/server/embed"
	"github.com/ivoras/harlequin/internal/shared/types"
	"golang.org/x/sync/errgroup"
	"golang.org/x/text/unicode/norm"
)

const (
	rrfK = 60.0
	// Chunk size in runes. Kept conservatively below the embedding server's
	// physical batch (llama.cpp ubatch, often 512 tokens) so a single chunk of
	// token-dense text (e.g. multilingual/legal) still embeds in one batch:
	// ~800 runes is well under 512 tokens even at ~0.5 tokens/rune.
	chunkRunes   = 800
	chunkOverlap = 100
)

// ChunkConfig selects the document chunker. "overlap" is the mechanical
// rune-window default; "semadj" is adjacent-sentence semantic chunking (cut
// between two sentences when their embedding cosine distance exceeds SemAdjGate),
// which the embedding-model study found best for retrieval. The gate is
// model-specific (e.g. snowflake-arctic ~0.43). MaxChunkRunes caps a chunk so it
// still fits the embedding server's physical batch.
type ChunkConfig struct {
	Strategy      string
	SemAdjGate    float64
	MaxChunkRunes int
	MinSentences  int
}

func defaultChunkConfig() ChunkConfig {
	return ChunkConfig{Strategy: "overlap", SemAdjGate: 0.4341, MaxChunkRunes: 1600, MinSentences: 2}
}

// Store manages documents and chunks.
type Store struct {
	db         *sql.DB
	embedder   embed.Embedder
	chunk      ChunkConfig
	ftsWeight  float64 // RRF weight of the FTS5 arm (dense arm = 1.0)
	ftsGatePct int     // keep only FTS5 hits at/above this score percentile (0 = all)
	embedPar   int     // concurrent embedding requests during ingestion (<=1: sequential)
}

// SetEmbedConcurrency sets how many embedding batches an ingestion keeps in flight.
func (s *Store) SetEmbedConcurrency(n int) { s.embedPar = n }

// NewStore constructs a documents Store with the default (overlap) chunker and
// equal-weight, ungated FTS5+vector fusion.
func NewStore(db *sql.DB, embedder embed.Embedder) *Store {
	return &Store{db: db, embedder: embedder, chunk: defaultChunkConfig(), ftsWeight: 1.0}
}

// SharedDB exposes the store's shared (org) database handle, for handlers that
// route a citation/file lookup by scope.
func (s *Store) SharedDB() *sql.DB { return s.db }

// SetChunkConfig overrides the chunker; zero-valued fields keep their defaults.
func (s *Store) SetChunkConfig(c ChunkConfig) {
	if c.Strategy != "" {
		s.chunk.Strategy = c.Strategy
	}
	if c.SemAdjGate > 0 {
		s.chunk.SemAdjGate = c.SemAdjGate
	}
	if c.MaxChunkRunes > 0 {
		s.chunk.MaxChunkRunes = c.MaxChunkRunes
	}
	if c.MinSentences > 0 {
		s.chunk.MinSentences = c.MinSentences
	}
}

// SetFusion sets the document-search RRF fusion: the FTS5 arm's weight (dense is
// fixed at 1.0) and an optional FTS5 score gate (keep only hits at/above this
// score percentile per query; 0 disables). weight <= 0 keeps the default 1.0.
func (s *Store) SetFusion(weight float64, gatePct int) {
	if weight > 0 {
		s.ftsWeight = weight
	}
	if gatePct > 0 && gatePct < 100 {
		s.ftsGatePct = gatePct
	}
}

// AsciiName transliterates a filename to a safe 7-bit ASCII form for on-disk
// storage (the original is kept in the DB). Accents are decomposed and dropped,
// other non-ASCII and unsafe characters become "_", runs of "_" are collapsed.
func AsciiName(name string) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(name) {
		switch {
		case unicode.Is(unicode.Mn, r): // combining mark from decomposition: drop
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_'):
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_.")
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	if out == "" {
		out = "file"
	}
	return out
}

// ChunkInfo resolves one chunk (by its local id in db) to citation metadata.
func (s *Store) ChunkInfo(ctx context.Context, db *sql.DB, localID int64) (*types.DocChunkInfo, error) {
	var info types.DocChunkInfo
	var hasFile bool
	err := db.QueryRowContext(ctx,
		`SELECT c.document_id, c.page, COALESCE(d.title, ''), COALESCE(d.mime, ''), COALESCE(d.stored_path, '') != '', SUBSTR(c.content, 1, 600)
		 FROM doc_chunks c JOIN documents d ON d.id = c.document_id WHERE c.id = ?`, localID).
		Scan(&info.DocumentID, &info.Page, &info.Title, &info.Mime, &hasFile, &info.Text)
	if err != nil {
		return nil, err
	}
	info.HasFile = hasFile
	return &info, nil
}

// StoredFile returns a document's persisted-file name and mime ("" when no
// original file was stored, e.g. raw-text ingests).
func (s *Store) StoredFile(ctx context.Context, db *sql.DB, docID int64) (storedPath, mime, title string, err error) {
	err = db.QueryRowContext(ctx,
		`SELECT COALESCE(stored_path, ''), COALESCE(mime, ''), COALESCE(title, '') FROM documents WHERE id = ?`, docID).
		Scan(&storedPath, &mime, &title)
	return
}

// MissingDescriptions lists documents in a corpus that have no catalogue
// description (for the startup repair sweep).
func (s *Store) MissingDescriptions(ctx context.Context, db *sql.DB) ([]types.Document, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, COALESCE(title, ''), COALESCE(created_by, 0) FROM documents WHERE COALESCE(description, '') = '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Document
	for rows.Next() {
		var d types.Document
		if err := rows.Scan(&d.ID, &d.Title, &d.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// OpeningText returns the concatenated content of a document's first chunks —
// enough context to describe or identify it without loading the whole text.
func (s *Store) OpeningText(ctx context.Context, db *sql.DB, docID int64, maxChunks int) (string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT content FROM doc_chunks WHERE document_id = ? ORDER BY ord LIMIT ?`, docID, maxChunks)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return "", err
		}
		parts = append(parts, c)
	}
	return strings.Join(parts, "\n"), rows.Err()
}

// ChunkContent returns one chunk's raw text by its local (scope-relative) id.
func (s *Store) ChunkContent(ctx context.Context, db *sql.DB, chunkID int64) (string, error) {
	var content string
	err := db.QueryRowContext(ctx, `SELECT content FROM doc_chunks WHERE id = ?`, chunkID).Scan(&content)
	return content, err
}

// FullText returns a document's entire content, all chunks concatenated in
// order. Used to view a document that has no stored original file (plain-text
// ingests, including save_doc reports) — its only representation is the
// chunk table.
func (s *Store) FullText(ctx context.Context, db *sql.DB, docID int64) (string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT content FROM doc_chunks WHERE document_id = ? ORDER BY ord`, docID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return "", err
		}
		parts = append(parts, c)
	}
	return strings.Join(parts, "\n\n"), rows.Err()
}

// FindText scans every chunk of a document for a literal, case-insensitive
// substring match — deterministic and exhaustive, unlike embedding/FTS5
// search, which can miss an exact phrase for unrelated ranking reasons. Use it
// to authoritatively confirm or rule out that specific wording exists
// somewhere in one document (e.g. before reporting something as new/removed).
// Returns up to maxHits (chunk id, page, a short snippet around the match).
type TextHit struct {
	ChunkID int64
	Page    int
	Snippet string
}

func (s *Store) FindText(ctx context.Context, db *sql.DB, docID int64, needle string, maxHits int) ([]TextHit, error) {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, page, content FROM doc_chunks WHERE document_id = ? ORDER BY ord`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	low := strings.ToLower(needle)
	var out []TextHit
	for rows.Next() {
		var id int64
		var page int
		var content string
		if err := rows.Scan(&id, &page, &content); err != nil {
			return nil, err
		}
		idx := strings.Index(strings.ToLower(content), low)
		if idx < 0 {
			continue
		}
		start := max(0, idx-60)
		end := min(len(content), idx+len(needle)+60)
		out = append(out, TextHit{ChunkID: id, Page: page, Snippet: strings.TrimSpace(content[start:end])})
		if len(out) >= maxHits {
			break
		}
	}
	return out, rows.Err()
}

// SetDescription records (or repairs) a document's catalogue description.
func (s *Store) SetDescription(ctx context.Context, db *sql.DB, id int64, desc string) error {
	_, err := db.ExecContext(ctx, `UPDATE documents SET description = ? WHERE id = ?`, desc, id)
	return err
}

// SetTitle renames a document.
func (s *Store) SetTitle(ctx context.Context, db *sql.DB, id int64, title string) error {
	_, err := db.ExecContext(ctx, `UPDATE documents SET title = ? WHERE id = ?`, title, id)
	return err
}

// SetStoredPath records the on-disk path of a document's persisted file.
func (s *Store) SetStoredPath(ctx context.Context, db *sql.DB, id int64, path string) error {
	_, err := db.ExecContext(ctx, `UPDATE documents SET stored_path = ? WHERE id = ?`, path, id)
	return err
}

// Ingest stores a document in the shared (org) corpus.
func (s *Store) Ingest(ctx context.Context, req types.CreateDocumentRequest, userID int64) (*types.Document, error) {
	return s.IngestInto(ctx, s.db, req, userID)
}

// IngestInto stores a document into a specific corpus (personal/shared/project
// DB), chunks + embeds its content, and indexes the chunks there.
func (s *Store) IngestInto(ctx context.Context, db *sql.DB, req types.CreateDocumentRequest, userID int64) (*types.Document, error) {
	return s.IngestIntoProgress(ctx, db, req, userID, nil)
}

// IngestIntoProgress is IngestInto with a progress callback for the embedding
// phase, called after each embedded batch with (done, total) chunk counts.
func (s *Store) IngestIntoProgress(ctx context.Context, db *sql.DB, req types.CreateDocumentRequest, userID int64, progress func(done, total int)) (*types.Document, error) {
	req.Content = StripSoftHyphens(req.Content)
	mime := req.Mime
	if mime == "" {
		mime = "text/plain"
	}
	pl := PhaseLogFrom(ctx)
	// Chunk + embed first, with no transaction open: embedding is slow network
	// work, and holding SQLite's single write lock across it starves every other
	// writer on this database (usage records, memories, parallel uploads).
	chunks, err := s.prepareChunks(ctx, req.Content, req.PageStarts, progress)
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO documents(title, uri, mime, created_by, original_name, description) VALUES (?, ?, ?, ?, ?, ?)`,
		req.Title, req.URI, mime, userID, req.OriginalName, req.Description)
	if err != nil {
		return nil, err
	}
	docID, _ := res.LastInsertId()
	if err := writeChunks(ctx, tx, docID, chunks); err != nil {
		return nil, err
	}
	n := len(chunks)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	pl.Phase("db-write", fmt.Sprintf("%d chunks", n))
	return &types.Document{ID: docID, Title: req.Title, URI: req.URI, Mime: mime, CreatedBy: userID, Chunks: n, OriginalName: req.OriginalName}, nil
}

// preparedChunk is one chunk ready to insert: text, page and serialized vector.
type preparedChunk struct {
	ord      int
	text     string
	page     int
	metadata sql.NullString
	vec      []byte // nil when no embedding was produced
}

// embedTexts embeds texts in sub-batches (no database access).
func (s *Store) embedTexts(ctx context.Context, texts []string, progress func(done, total int)) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	// Embed in sub-batches so a progress callback can report per-batch
	// (one request for everything would be opaque for minutes on big docs).
	// Batches run up to embedPar at a time and land in their own slots, so
	// the result order is the chunk order whatever order they finish in.
	const embedBatch = 32
	nb := (len(texts) + embedBatch - 1) / embedBatch
	results := make([][][]float32, nb)
	var mu sync.Mutex
	doneChunks := 0
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(s.embedPar, 1))
	for b := 0; b < nb; b++ {
		start := b * embedBatch
		end := min(start+embedBatch, len(texts))
		g.Go(func() error {
			bv, err := s.embedder.Embed(gctx, texts[start:end])
			if err != nil {
				return fmt.Errorf("embed document: %w", err)
			}
			results[b] = bv
			mu.Lock()
			defer mu.Unlock()
			doneChunks += end - start // under the lock, so progress never goes backwards
			if progress != nil {
				progress(doneChunks, len(texts))
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	vecs := make([][]float32, 0, len(texts))
	for _, bv := range results {
		vecs = append(vecs, bv...)
	}
	PhaseLogFrom(ctx).Phase("embed-chunks", fmt.Sprintf("%d chunks", len(texts)))
	return vecs, nil
}

// prepareChunks chunks content and embeds the chunks, entirely in memory (no
// database access), so the caller can then insert the result in one short
// transaction via writeChunks.
func (s *Store) prepareChunks(ctx context.Context, content string, pageStarts []int, progress func(done, total int)) ([]preparedChunk, error) {
	pl := PhaseLogFrom(ctx)
	chunks, err := s.chunkContent(ctx, content)
	if err != nil {
		return nil, fmt.Errorf("chunk document: %w", err)
	}
	if len(chunks) == 0 {
		return nil, nil
	}
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.text
	}
	vecs, err := s.embedTexts(ctx, texts, progress)
	if err != nil {
		return nil, err
	}
	out := make([]preparedChunk, len(chunks))
	for i, c := range chunks {
		out[i] = preparedChunk{ord: i, text: c.text, page: pageFor(c.start, pageStarts)} // page 0 when the source has none
		if i < len(vecs) {
			if out[i].vec, err = sqlite_vec.SerializeFloat32(vecs[i]); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// writeChunks inserts prepared chunks (rows, FTS and vector index) under docID
// within tx. Pure SQL, no network or model calls: this is the only part of an
// ingest that runs while the write lock is held.
func writeChunks(ctx context.Context, tx *sql.Tx, docID int64, chunks []preparedChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	ins, err := tx.PrepareContext(ctx, `INSERT INTO doc_chunks(document_id, ord, content, metadata, page) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	fts, err := tx.PrepareContext(ctx, `INSERT INTO doc_chunks_fts(rowid, content) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer fts.Close()
	vec, err := tx.PrepareContext(ctx, `INSERT INTO doc_chunks_vec(rowid, embedding) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer vec.Close()
	for _, c := range chunks {
		r, err := ins.ExecContext(ctx, docID, c.ord, c.text, c.metadata, c.page)
		if err != nil {
			return err
		}
		chunkID, _ := r.LastInsertId()
		if _, err := fts.ExecContext(ctx, chunkID, c.text); err != nil {
			return err
		}
		if c.vec != nil {
			if _, err := vec.ExecContext(ctx, chunkID, c.vec); err != nil {
				return err
			}
		}
	}
	return nil
}

// ReplaceContent is ReplaceContentPaged without page structure.
func (s *Store) ReplaceContent(ctx context.Context, db *sql.DB, docID int64, content string) (int, error) {
	return s.ReplaceContentPaged(ctx, db, docID, content, nil)
}

type oldChunk struct {
	id   int64
	text string
	page int
}

// matchChunks pairs each new chunk text with an unused old chunk of identical
// text (duplicates one-to-one, in order); -1 when unmatched.
func matchChunks(old []oldChunk, texts []string) []int {
	pool := map[string][]int{}
	for i, o := range old {
		pool[o.text] = append(pool[o.text], i)
	}
	m := make([]int, len(texts))
	for i, t := range texts {
		m[i] = -1
		if q := pool[t]; len(q) > 0 {
			m[i], pool[t] = q[0], q[1:]
		}
	}
	return m
}

func loadOldChunks(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, docID int64) ([]oldChunk, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, content, page FROM doc_chunks WHERE document_id = ? ORDER BY ord, id`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []oldChunk
	for rows.Next() {
		var o oldChunk
		if err := rows.Scan(&o.id, &o.text, &o.page); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ReplaceContentPaged re-chunks an existing document with new text, keeping its
// id, title and scope. Chunks whose text is unchanged keep their row id (so
// citations survive), vector and page; only new chunks are embedded and only
// vanished ones deleted. pageStarts (rune offsets, as in
// CreateDocumentRequest.PageStarts) recomputes every chunk's page; without it a
// kept chunk keeps its page and a new one takes its neighbour's.
func (s *Store) ReplaceContentPaged(ctx context.Context, db *sql.DB, docID int64, content string, pageStarts []int) (int, error) {
	content = StripSoftHyphens(content)
	// Embed before taking the write lock (see IngestIntoProgress); the existence
	// check up front avoids embedding for a missing document, and is repeated in
	// the transaction in case it was deleted meanwhile.
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT 1 FROM documents WHERE id = ?`, docID).Scan(&exists); err != nil {
		return 0, fmt.Errorf("document %d: %w", docID, err)
	}
	chunks, err := s.chunkContent(ctx, content)
	if err != nil {
		return 0, fmt.Errorf("chunk document: %w", err)
	}
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.text
	}
	old, err := loadOldChunks(ctx, db, docID)
	if err != nil {
		return 0, err
	}
	var fresh []string // unmatched texts, deduplicated
	vecOf := map[string][]byte{}
	for i, m := range matchChunks(old, texts) {
		if _, seen := vecOf[texts[i]]; m < 0 && !seen {
			vecOf[texts[i]] = nil
			fresh = append(fresh, texts[i])
		}
	}
	vecs, err := s.embedTexts(ctx, fresh, nil)
	if err != nil {
		return 0, err
	}
	for i, t := range fresh {
		if i < len(vecs) {
			if vecOf[t], err = sqlite_vec.SerializeFloat32(vecs[i]); err != nil {
				return 0, err
			}
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM documents WHERE id = ?`, docID).Scan(&exists); err != nil {
		return 0, fmt.Errorf("document %d: %w", docID, err)
	}
	// Re-read: the chunks may have changed since the embedding started.
	if old, err = loadOldChunks(ctx, tx, docID); err != nil {
		return 0, err
	}
	match := matchChunks(old, texts)
	used := make([]bool, len(old))
	pages := make([]int, len(chunks))
	havePage := make([]bool, len(chunks))
	for i, m := range match {
		switch {
		case pageStarts != nil:
			pages[i], havePage[i] = pageFor(chunks[i].start, pageStarts), true
		case m >= 0:
			pages[i], havePage[i] = old[m].page, true
		}
		if m >= 0 {
			used[m] = true
		} else if _, ok := vecOf[texts[i]]; !ok {
			return 0, fmt.Errorf("document %d changed while it was being edited; retry", docID)
		}
	}
	for i := range pages { // new chunk: nearest preceding page...
		if !havePage[i] && i > 0 {
			pages[i], havePage[i] = pages[i-1], true
		}
	}
	for i := len(pages) - 2; i >= 0; i-- { // ...or following, when none precedes
		if !havePage[i] {
			pages[i], havePage[i] = pages[i+1], true
		}
	}
	for i, o := range old {
		if used[i] {
			continue
		}
		for _, q := range []string{`DELETE FROM doc_chunks_fts WHERE rowid = ?`, `DELETE FROM doc_chunks_vec WHERE rowid = ?`, `DELETE FROM doc_chunks WHERE id = ?`} {
			if _, err := tx.ExecContext(ctx, q, o.id); err != nil {
				return 0, fmt.Errorf("delete chunk %d: %w", o.id, err)
			}
		}
	}
	var add []preparedChunk
	for i, m := range match {
		if m < 0 {
			add = append(add, preparedChunk{ord: i, text: texts[i], page: pages[i], vec: vecOf[texts[i]]})
			continue
		}
		// ord has no unique constraint, so in-place updates cannot collide.
		if _, err := tx.ExecContext(ctx, `UPDATE doc_chunks SET ord = ?, page = ? WHERE id = ?`, i, pages[i], old[m].id); err != nil {
			return 0, err
		}
	}
	if err := writeChunks(ctx, tx, docID, add); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(chunks), nil
}

// ReindexChunkVectors re-embeds every chunk's content and rewrites its
// doc_chunks_vec row. Use after recreating the vec0 table (e.g. a metric
// change). Returns the number of chunks reindexed.
// progress, if non-nil, is called after each embedded batch with (done, total).
func (s *Store) ReindexChunkVectors(ctx context.Context, progress func(done, total int)) (int, error) {
	if s.db == nil {
		return 0, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, content FROM doc_chunks`)
	if err != nil {
		return 0, err
	}
	type chunk struct {
		id      int64
		content string
	}
	var all []chunk
	for rows.Next() {
		var c chunk
		if err := rows.Scan(&c.id, &c.content); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	n := 0
	const batch = 32 // chunks per embeddings request (the endpoint round-trip dominates)
	for start := 0; start < len(all); start += batch {
		end := min(start+batch, len(all))
		texts := make([]string, end-start)
		for i, c := range all[start:end] {
			texts[i] = c.content
		}
		vecs, err := s.embedder.Embed(ctx, texts)
		if err != nil || len(vecs) != len(texts) {
			continue // skip the failing batch, keep going (matches the old per-item behavior)
		}
		for i, c := range all[start:end] {
			blob, err := sqlite_vec.SerializeFloat32(vecs[i])
			if err != nil {
				return n, err
			}
			if _, err := s.db.ExecContext(ctx,
				`INSERT INTO doc_chunks_vec(rowid, embedding) VALUES (?, ?)`, c.id, blob); err != nil {
				return n, err
			}
			n++
		}
		if progress != nil {
			progress(end, len(all))
		}
	}
	return n, nil
}

// List returns all documents in the shared corpus (newest first).
func (s *Store) List(ctx context.Context) ([]types.Document, error) {
	return s.ListScoped(ctx, []ScopeDB{{ScopeShared, s.db}})
}

// ListScoped returns the documents across several corpora, each tagged with its
// scope (newest first within each, scopes concatenated in the given order).
func (s *Store) ListScoped(ctx context.Context, scopes []ScopeDB) ([]types.Document, error) {
	var out []types.Document
	for _, sc := range scopes {
		if sc.DB == nil {
			continue
		}
		rows, err := sc.DB.QueryContext(ctx,
			`SELECT id, title, uri, mime, COALESCE(created_by, 0), created_at,
			        COALESCE(original_name, ''), COALESCE(stored_path, ''), COALESCE(description, ''),
			        (SELECT COUNT(*) FROM doc_chunks c WHERE c.document_id = documents.id)
			 FROM documents ORDER BY id DESC`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var d types.Document
			if err := rows.Scan(&d.ID, &d.Title, &d.URI, &d.Mime, &d.CreatedBy, &d.CreatedAt,
				&d.OriginalName, &d.StoredPath, &d.Description, &d.Chunks); err != nil {
				rows.Close()
				return nil, err
			}
			d.Scope = sc.Scope
			out = append(out, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CopyInto copies document id from corpus src into corpus dst — its row,
// chunks, FTS and vector index rows (vectors are copied as-is, no re-embedding;
// all corpora share one embedding dimension) and its stored original file
// (srcFiles -> dstFiles, renamed for the new id). Returns the new id. The
// source is left untouched; a move deletes it afterwards, so a failure part-way
// leaves the original rather than losing it.
func (s *Store) CopyInto(ctx context.Context, src, dst *sql.DB, srcFiles, dstFiles string, id int64) (int64, error) {
	var title, uri, mime, createdAt, original, stored, desc string
	var createdBy sql.NullInt64
	err := src.QueryRowContext(ctx,
		`SELECT COALESCE(title,''), COALESCE(uri,''), COALESCE(mime,''), created_by, COALESCE(created_at,''),
		        COALESCE(original_name,''), COALESCE(stored_path,''), COALESCE(description,'')
		 FROM documents WHERE id = ?`, id).Scan(&title, &uri, &mime, &createdBy, &createdAt, &original, &stored, &desc)
	if err != nil {
		return 0, err
	}
	type chunk struct {
		ord, page int
		content   string
		metadata  sql.NullString
		vec       []byte
	}
	rows, err := src.QueryContext(ctx,
		`SELECT c.ord, c.page, c.content, c.metadata, v.embedding
		 FROM doc_chunks c LEFT JOIN doc_chunks_vec v ON v.rowid = c.id
		 WHERE c.document_id = ? ORDER BY c.ord, c.id`, id)
	if err != nil {
		return 0, err
	}
	var chunks []chunk
	for rows.Next() {
		var c chunk
		if err := rows.Scan(&c.ord, &c.page, &c.content, &c.metadata, &c.vec); err != nil {
			rows.Close()
			return 0, err
		}
		chunks = append(chunks, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Disk I/O stays out of the transaction: read the original now, write it after
	// the commit. Only the inserts run under the write lock.
	var fileData []byte
	var haveFile bool
	var rest string
	if stored != "" && srcFiles != "" && dstFiles != "" {
		data, err := os.ReadFile(filepath.Join(srcFiles, filepath.Base(stored)))
		if err != nil && !os.IsNotExist(err) {
			return 0, fmt.Errorf("read stored file: %w", err)
		}
		if err == nil {
			fileData, haveFile = data, true
			rest = filepath.Base(stored)
			if i := strings.IndexByte(rest, '-'); i > 0 && strings.Trim(rest[:i], "0123456789") == "" {
				rest = rest[i+1:]
			}
		}
	}
	prepared := make([]preparedChunk, len(chunks))
	for i, c := range chunks {
		prepared[i] = preparedChunk{ord: c.ord, text: c.content, page: c.page, metadata: c.metadata, vec: c.vec}
	}

	tx, err := dst.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO documents(title, uri, mime, created_by, created_at, original_name, description)
		 VALUES (?, ?, ?, ?, COALESCE(NULLIF(?, ''), CURRENT_TIMESTAMP), ?, ?)`,
		title, uri, mime, createdBy, createdAt, sql.NullString{String: original, Valid: original != ""}, desc)
	if err != nil {
		return 0, err
	}
	newID, _ := res.LastInsertId()
	if err := writeChunks(ctx, tx, newID, prepared); err != nil {
		return 0, fmt.Errorf("copy chunks: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if haveFile {
		// A failure here must not leave a half-copied document behind (a move
		// would then delete the only complete one), so undo the copy.
		newStored := fmt.Sprintf("%d-%s", newID, rest)
		path := filepath.Join(dstFiles, newStored)
		err := os.WriteFile(path, fileData, 0o600)
		if err != nil {
			err = fmt.Errorf("write stored file: %w", err)
		} else if _, uerr := dst.ExecContext(ctx, `UPDATE documents SET stored_path = ? WHERE id = ?`, newStored, newID); uerr != nil {
			_ = os.Remove(path)
			err = uerr
		}
		if err != nil {
			_ = s.DeleteFrom(context.WithoutCancel(ctx), dst, "", newID)
			return 0, err
		}
	}
	return newID, nil
}

// deleteChunkBatch removes the given chunks and their FTS and vector rows in one
// short transaction.
func deleteChunkBatch(ctx context.Context, conn *sql.Conn, ids []int64) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, cid := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM doc_chunks_fts WHERE rowid = ?`, cid); err != nil {
			return fmt.Errorf("delete fts row: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM doc_chunks_vec WHERE rowid = ?`, cid); err != nil {
			return fmt.Errorf("delete vec row: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM doc_chunks WHERE id = ?`, cid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteFrom removes a document, its chunks/index rows and (after the commit)
// its stored file from a specific corpus. filesDir is that corpus' files
// directory; "" skips the file.
//
// The extracted Markdown/text exists only as doc_chunks.content (plus its FTS
// and vector index rows), so deleting those is deleting the Markdown. Deletion
// runs with secure_delete on, so the freed database pages are zeroed instead of
// keeping the text recoverable in the file. A stored-file removal failure is
// returned (the rows are already gone by then) rather than swallowed.
func (s *Store) DeleteFrom(ctx context.Context, db *sql.DB, filesDir string, id int64) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// secure_delete is per-connection: pin one, and switch it back off before
	// the connection returns to the pool.
	if _, err := conn.ExecContext(ctx, `PRAGMA secure_delete = ON`); err != nil {
		return err
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA secure_delete = OFF`)
	var stored string
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(stored_path, '') FROM documents WHERE id = ?`, id).Scan(&stored); err != nil && err != sql.ErrNoRows {
		return err
	}
	rows, err := conn.QueryContext(ctx, `SELECT id FROM doc_chunks WHERE document_id = ?`, id)
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
	// Index rows go in small batches, each its own transaction, so a big
	// document (vec0 deletes plus secure_delete are slow) never holds the write
	// lock for seconds and starves other writers. Each chunk's chunk/FTS/vec rows
	// leave together, so an interrupted delete leaves a smaller but consistent
	// document that a retry finishes. Index errors abort: a stale FTS/vec row
	// must not outlive its chunk.
	// SQLite's busy handler polls (sleeping up to 100 ms), so a deleter that
	// re-takes the lock the instant it releases it would starve waiting writers
	// all the same: pause between batches so they get in.
	const batch, yield = 25, 15 * time.Millisecond
	for start := 0; start < len(chunkIDs); start += batch {
		if err := deleteChunkBatch(ctx, conn, chunkIDs[start:min(start+batch, len(chunkIDs))]); err != nil {
			return err
		}
		select {
		case <-time.After(yield):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Explicit, not left to ON DELETE CASCADE: the text must go even if a
	// connection ever runs without foreign keys.
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM doc_chunks WHERE document_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM documents WHERE id = ?`, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if stored != "" && filesDir != "" {
		// Base(): stored_path is a bare filename; never let it escape filesDir.
		if err := os.Remove(filepath.Join(filesDir, filepath.Base(stored))); err != nil && !os.IsNotExist(err) {
			log.Printf("documents: delete %d: remove stored file %q: %v", id, stored, err)
			return fmt.Errorf("document %d removed from the index, but deleting its stored file %q failed: %w", id, stored, err)
		}
	}
	return nil
}

type scoredHit struct {
	id      int64
	content string
	title   string // source document title
	ord     int    // chunk ordinal within the document
	page    int    // 1-based source page (0 = no page structure)
	docID   int64  // owning document id (for citations)
	mime    string
	hasFile bool // an original file is stored (servable via /documents/{id}/file)
}

// Scope labels surfaced in results and used to qualify chunk ids across corpora.
const (
	ScopePersonal = "personal"
	ScopeShared   = "shared"
	ScopeProject  = "project"
)

// ScopeDB pairs a database with the scope label of the documents it holds.
type ScopeDB struct {
	Scope string
	DB    *sql.DB
}

func scopePrefix(scope string) string {
	// A qualified project scope ("project:<id>", from an all-projects search)
	// renders as p<id> so references stay unambiguous across projects; the
	// session's own project keeps the plain p.
	if id, ok := strings.CutPrefix(scope, ScopeProject+":"); ok {
		return "p" + id
	}
	switch scope {
	case ScopePersonal:
		return "u"
	case ScopeProject:
		return "p"
	default:
		return "s"
	}
}

// ProjectScope returns the qualified scope label for a specific project's
// corpus, used when searching projects beyond the session's own. Chunk and
// document references under it render as d.p<id>.N / p<id>.N.
func ProjectScope(projectID int64) string {
	return ScopeProject + ":" + strconv.FormatInt(projectID, 10)
}

// ScopesFor builds the corpus list for a request: the active project (if any),
// the shared/org corpus, and the user's personal corpus (if a user DB is open).
// nil DBs are skipped.
func (s *Store) ScopesFor(userDB, projDB *sql.DB) []ScopeDB {
	out := make([]ScopeDB, 0, 3)
	if projDB != nil {
		out = append(out, ScopeDB{ScopeProject, projDB})
	}
	out = append(out, ScopeDB{ScopeShared, s.db})
	if userDB != nil {
		out = append(out, ScopeDB{ScopePersonal, userDB})
	}
	return out
}

// Search fuses the dense (vector) and FTS5 (lexical) arms of the shared corpus
// via weighted RRF (back-compatible single-scope search).
func (s *Store) Search(ctx context.Context, query string, limit int) ([]types.SearchResult, error) {
	return s.SearchScoped(ctx, []ScopeDB{{ScopeShared, s.db}}, query, limit)
}

// SearchScoped fuses the dense+FTS5 arms across several scope corpora and labels
// each hit with the scope it came from. The FTS5 arm carries ftsWeight (dense =
// 1.0) and, if ftsGatePct > 0, is gated per corpus to its strongest hits.
func (s *Store) SearchScoped(ctx context.Context, scopes []ScopeDB, query string, limit int) ([]types.SearchResult, error) {
	return s.SearchScopedDocs(ctx, scopes, query, limit, nil)
}

// SearchScopedDocs is SearchScoped restricted to specific documents. docsByScope
// maps a scope label to the document ids to search within it; scopes without an
// entry are skipped entirely. A nil map searches everything.
func (s *Store) SearchScopedDocs(ctx context.Context, scopes []ScopeDB, query string, limit int, docsByScope map[string][]int64) ([]types.SearchResult, error) {
	if limit <= 0 {
		limit = 8
	}
	// When the FTS5 arm is score-gated it keeps only its top decile, so fuse
	// from a deeper pool to recover recall; ungated fusion stays at limit*4.
	depth := limit * 4
	if s.ftsGatePct > 0 && depth < 200 {
		depth = 200
	}
	ftsQuery := searchquery.FTS(query)
	focusedFTS := searchquery.FocusedFTS(query)
	embedQuery := searchquery.Embed(query)
	// Embed the query once for the dense arm of every scope.
	var blob any
	if vecs, err := s.embedder.EmbedQuery(ctx, []string{embedQuery}); err == nil && len(vecs) == 1 {
		if b, err := sqlite_vec.SerializeFloat32(vecs[0]); err == nil {
			blob = b
		}
	}
	ranks := map[string]float64{}
	contents := map[string]string{}
	scopeOf := map[string]string{}
	sourceOf := map[string]string{}
	metaOf := map[string]docMeta{}
	for _, sc := range scopes {
		if sc.DB == nil {
			continue
		}
		var docFilter []int64
		if docsByScope != nil {
			docFilter = docsByScope[sc.Scope]
			if len(docFilter) == 0 {
				continue // a filter is set and this corpus isn't in it
			}
		}
		s.searchInto(ctx, sc, ftsQuery, focusedFTS, blob, depth, docFilter, ranks, contents, scopeOf, sourceOf, metaOf)
	}
	return topN(ranks, contents, scopeOf, sourceOf, metaOf, limit), nil
}

// searchInto runs the gated/weighted dense+FTS5 arms against one corpus, folding
// scope-qualified RRF contributions into the shared maps.
// docMeta carries a hit's citation metadata (document id, page, mime, file).
type docMeta struct {
	docID   int64
	page    int
	mime    string
	hasFile bool
}

func (s *Store) searchInto(ctx context.Context, sc ScopeDB, ftsQuery, focusedFTS string, blob any, depth int,
	docFilter []int64, ranks map[string]float64, contents, scopeOf, sourceOf map[string]string, metaOf map[string]docMeta) {
	key := func(local int64) string {
		return "d." + scopePrefix(sc.Scope) + "." + strconv.FormatInt(local, 10)
	}
	// Restricting to specific documents post-filters the ranked arms, so fetch
	// deeper to keep recall.
	inClause := ""
	if len(docFilter) > 0 {
		ids := make([]string, len(docFilter))
		for i, id := range docFilter {
			ids[i] = strconv.FormatInt(id, 10)
		}
		inClause = " AND c.document_id IN (" + strings.Join(ids, ",") + ")"
		depth = min(depth*4, 400)
	}
	runFTS := func(q string, weight float64) {
		if q == "" || weight <= 0 {
			return
		}
		if rows, err := sc.DB.QueryContext(ctx,
			`SELECT c.id, c.content, COALESCE(d.title, ''), c.ord, c.page,
			        d.id, COALESCE(d.mime, ''), COALESCE(d.stored_path, '') != ''
			 FROM doc_chunks_fts f JOIN doc_chunks c ON c.id = f.rowid
			 JOIN documents d ON d.id = c.document_id
			 WHERE doc_chunks_fts MATCH ?`+inClause+` ORDER BY f.rank LIMIT ?`, q, depth); err == nil {
			hits := collect(rows)
			if s.ftsGatePct > 0 {
				hits = gateTop(hits, s.ftsGatePct)
			}
			foldKeyed(hits, weight, sc.Scope, key, ranks, contents, scopeOf, sourceOf, metaOf)
		}
	}
	runFTS(ftsQuery, s.ftsWeight)
	if focusedFTS != "" && focusedFTS != ftsQuery {
		runFTS(focusedFTS, s.ftsWeight)
	}
	if blob != nil {
		if rows, err := sc.DB.QueryContext(ctx,
			`SELECT c.id, c.content, COALESCE(d.title, ''), c.ord, c.page,
			        d.id, COALESCE(d.mime, ''), COALESCE(d.stored_path, '') != ''
			 FROM doc_chunks_vec v JOIN doc_chunks c ON c.id = v.rowid
			 JOIN documents d ON d.id = c.document_id
			 WHERE v.embedding MATCH ? AND k = ?`+inClause+` ORDER BY v.distance`, blob, depth); err == nil {
			foldKeyed(collect(rows), 1.0, sc.Scope, key, ranks, contents, scopeOf, sourceOf, metaOf)
		}
	}
}

// collect drains a (id, content, title, ord) result set, preserving its order.
func collect(rows *sql.Rows) []scoredHit {
	defer rows.Close()
	var out []scoredHit
	for rows.Next() {
		var h scoredHit
		if err := rows.Scan(&h.id, &h.content, &h.title, &h.ord, &h.page, &h.docID, &h.mime, &h.hasFile); err != nil {
			return out
		}
		out = append(out, h)
	}
	return out
}

// gateTop keeps the strongest hits — the top (100-pct)% of a best-first list —
// dropping weaker lexical matches below the pct-th score percentile.
func gateTop(hits []scoredHit, pct int) []scoredHit {
	if len(hits) == 0 {
		return hits
	}
	keep := int(math.Ceil(float64(len(hits)) * float64(100-pct) / 100.0))
	if keep < 1 {
		keep = 1
	}
	if keep > len(hits) {
		keep = len(hits)
	}
	return hits[:keep]
}

// foldKeyed adds weighted RRF contributions (weight/(k+rank)) for a best-first
// list, keyed by a scope-qualified id and tagged with the source scope and a
// human-readable source ("<title> · chunk <ord>").
func foldKeyed(hits []scoredHit, weight float64, scope string, key func(int64) string,
	ranks map[string]float64, contents, scopeOf, sourceOf map[string]string, metaOf map[string]docMeta) {
	for i, h := range hits {
		k := key(h.id)
		ranks[k] += weight / (rrfK + float64(i+1))
		contents[k] = h.content
		scopeOf[k] = scope
		metaOf[k] = docMeta{docID: h.docID, page: h.page, mime: h.mime, hasFile: h.hasFile}
		title := h.title
		if title == "" {
			title = "untitled"
		}
		if h.page > 0 {
			sourceOf[k] = fmt.Sprintf("%s · p.%d", title, h.page)
		} else {
			sourceOf[k] = fmt.Sprintf("%s · chunk %d", title, h.ord)
		}
	}
}

func topN(ranks map[string]float64, contents, scopeOf, sourceOf map[string]string, metaOf map[string]docMeta, limit int) []types.SearchResult {
	out := make([]types.SearchResult, 0, len(ranks))
	for id, score := range ranks {
		m := metaOf[id]
		out = append(out, types.SearchResult{
			ID: id, Content: contents[id], Score: score, Scope: scopeOf[id], Source: sourceOf[id],
			DocumentID: m.docID, Page: m.page, Mime: m.mime, HasFile: m.hasFile,
		})
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

// chunkContent splits a document into chunks per the configured strategy.
// "semadj" embeds each sentence and cuts where adjacent sentences diverge;
// it falls back to mechanical rune-window chunking when the text has too little
// sentence structure or embedding fails, and hard-splits any oversized chunk so
// every chunk stays within the embedding server's batch.
// chunkRec is a chunk plus its rune offset in the source text (for page mapping).
type chunkRec struct {
	text  string
	start int
}

// sentRec is a sentence plus its rune offset in the source text.
type sentRec struct {
	text  string
	start int
}

func (s *Store) chunkContent(ctx context.Context, text string) ([]chunkRec, error) {
	pl := PhaseLogFrom(ctx)
	if s.chunk.Strategy != "semadj" {
		out := chunkText(text, 0)
		pl.Phase("chunk-plain", fmt.Sprintf("%d chunks", len(out)))
		return out, nil
	}
	sents := splitSentences(text)
	if len(sents) <= s.chunk.MinSentences {
		out := chunkText(text, 0)
		pl.Phase("chunk-plain", fmt.Sprintf("%d chunks (too few sentences for semadj)", len(out)))
		return out, nil
	}
	texts := make([]string, len(sents))
	for i, sr := range sents {
		texts[i] = sr.text
	}
	pl.Phase("split-sentences", fmt.Sprintf("%d sentences", len(sents)))
	// The costlier of the two embedding passes: semadj embeds every sentence,
	// then the caller embeds every chunk. Timing them separately is the only way
	// to see that one ingestion pays for embeddings twice.
	vecs, err := s.embedder.Embed(ctx, texts)
	pl.Phase("embed-sentences", fmt.Sprintf("%d sentences", len(texts)))
	if err != nil || len(vecs) != len(sents) {
		// Graceful degradation, but never a silent one: the document still
		// ingests with plain chunking, minus the semantic boundaries.
		log.Printf("documents: semantic chunking fell back to plain chunks (%d sentences): %v", len(sents), err)
		return chunkText(text, 0), nil
	}
	raw := semAdjacentChunks(sents, vecs, s.chunk.SemAdjGate, s.chunk.MaxChunkRunes, s.chunk.MinSentences)
	var out []chunkRec
	for _, c := range raw {
		if len([]rune(c.text)) > s.chunk.MaxChunkRunes {
			out = append(out, chunkText(c.text, c.start)...) // single mega-sentence: hard-split
		} else {
			out = append(out, c)
		}
	}
	pl.Phase("chunk-semadj", fmt.Sprintf("%d chunks from %d sentences", len(out), len(sents)))
	return out, nil
}

// pageFor maps a rune offset to a 1-based page number given the rune offsets at
// which each page starts. Returns 0 when the source has no page structure.
func pageFor(offset int, pageStarts []int) int {
	if len(pageStarts) == 0 {
		return 0
	}
	page := 1
	for i, st := range pageStarts {
		if offset >= st {
			page = i + 1
		} else {
			break
		}
	}
	return page
}

// semAdjacentChunks groups consecutive sentences, cutting before sentence i when
// its embedding diverges from the previous sentence by more than gate
// (drift = 1 - cosine), or when the chunk would exceed maxRunes. minSent avoids
// singleton fragments. vecs is parallel to sents.
func semAdjacentChunks(sents []sentRec, vecs [][]float32, gate float64, maxRunes, minSent int) []chunkRec {
	var chunks []chunkRec
	var cur []string
	curRunes, curStart := 0, 0
	flush := func() {
		if len(cur) > 0 {
			chunks = append(chunks, chunkRec{text: strings.Join(cur, " "), start: curStart})
		}
	}
	for i, sr := range sents {
		rl := len([]rune(sr.text))
		if len(cur) > 0 {
			overCap := curRunes+1+rl > maxRunes
			drift := 1.0 - cosine(vecs[i-1], vecs[i])
			bigShift := drift > gate && len(cur) >= minSent
			if overCap || bigShift {
				flush()
				cur, curRunes = nil, 0
			}
		}
		if len(cur) == 0 {
			curStart = sr.start
		}
		cur = append(cur, sr.text)
		curRunes += rl + 1
	}
	flush()
	return chunks
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// splitSentences is a lightweight sentence segmenter: it breaks after . ! or ?
// when the next non-space character starts a new sentence (capital, digit, quote
// or opening paren) or at end of text. Good enough for general prose; it does not
// special-case abbreviations.
// citeAbbrevRE matches any bracketed reference abbreviation of the general
// shape "[x.y." (e.g. "[d.p.", "[d.u.", or any other single-letter.single-
// letter. namespace, not just the current chunk-citation scopes) right at
// the end of the text seen so far — its trailing period looks like a
// sentence end (period, space, digit: "[d.p. 3651]") but isn't one. Without
// this guard, splitSentences cuts the reference in half, and FullText later
// rejoins the pieces with a paragraph break, visibly mangling it.
var citeAbbrevRE = regexp.MustCompile(`\[[a-zA-Z]\.[a-zA-Z]\.$`)

func splitSentences(text string) []sentRec {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	runes := []rune(text) // NOT trimmed, so recorded offsets align with the source
	var out []sentRec
	var b strings.Builder
	segStart := 0
	for i, r := range runes {
		if b.Len() == 0 {
			segStart = i
		}
		b.WriteRune(r)
		if r == '.' || r == '!' || r == '?' {
			if r == '.' && citeAbbrevRE.MatchString(b.String()) {
				continue
			}
			j := i + 1
			for j < len(runes) && unicode.IsSpace(runes[j]) {
				j++
			}
			if j >= len(runes) || isSentenceStart(runes[j]) {
				if s := strings.TrimSpace(b.String()); s != "" {
					out = append(out, sentRec{text: s, start: segStart})
				}
				b.Reset()
			}
		}
	}
	if s := strings.TrimSpace(b.String()); s != "" {
		out = append(out, sentRec{text: s, start: segStart})
	}
	return out
}

func isSentenceStart(r rune) bool {
	return unicode.IsUpper(r) || unicode.IsDigit(r) || r == '"' || r == '\'' ||
		r == '(' || r == '“' || r == '‘'
}

// chunkText splits text into overlapping rune windows. base is the rune offset
// of `text` within the source (so the returned chunk offsets are source-relative).
func chunkText(text string, base int) []chunkRec {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	runes := []rune(text)
	if len(runes) <= chunkRunes {
		return []chunkRec{{text: text, start: base}}
	}
	var chunks []chunkRec
	for start := 0; start < len(runes); start += chunkRunes - chunkOverlap {
		end := start + chunkRunes
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, chunkRec{text: string(runes[start:end]), start: base + start})
		if end == len(runes) {
			break
		}
	}
	return chunks
}

// StripSoftHyphens removes U+00AD (invisible hyphenation hints) from text. Sources
// such as legal-gazette HTML embed them mid-word ("Hrvat\u00adski"); left in, they
// break lexical search and exact-quote verification while looking identical.
func StripSoftHyphens(s string) string { return strings.ReplaceAll(s, "\u00ad", "") }
