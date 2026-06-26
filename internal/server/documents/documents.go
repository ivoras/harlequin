// Package documents implements the org RAG corpus: documents are split into
// chunks, embedded, and indexed for hybrid (FTS5 + vector) search.
package documents

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/ivoras/harlequin/internal/server/embed"
	"github.com/ivoras/harlequin/internal/shared/types"
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
}

// NewStore constructs a documents Store with the default (overlap) chunker and
// equal-weight, ungated FTS5+vector fusion.
func NewStore(db *sql.DB, embedder embed.Embedder) *Store {
	return &Store{db: db, embedder: embedder, chunk: defaultChunkConfig(), ftsWeight: 1.0}
}

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
	mime := req.Mime
	if mime == "" {
		mime = "text/plain"
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO documents(title, uri, mime, created_by, original_name) VALUES (?, ?, ?, ?, ?)`,
		req.Title, req.URI, mime, userID, req.OriginalName)
	if err != nil {
		return nil, err
	}
	docID, _ := res.LastInsertId()

	chunks, err := s.chunkContent(ctx, req.Content)
	if err != nil {
		return nil, fmt.Errorf("chunk document: %w", err)
	}
	if len(chunks) > 0 {
		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.text
		}
		vecs, err := s.embedder.Embed(ctx, texts)
		if err != nil {
			return nil, fmt.Errorf("embed document: %w", err)
		}
		for i, c := range chunks {
			page := pageFor(c.start, req.PageStarts) // 0 when the source has no pages
			r, err := tx.ExecContext(ctx,
				`INSERT INTO doc_chunks(document_id, ord, content, page) VALUES (?, ?, ?, ?)`, docID, i, c.text, page)
			if err != nil {
				return nil, err
			}
			chunkID, _ := r.LastInsertId()
			if _, err := tx.ExecContext(ctx, `INSERT INTO doc_chunks_fts(rowid, content) VALUES (?, ?)`, chunkID, c.text); err != nil {
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
	return &types.Document{ID: docID, Title: req.Title, URI: req.URI, Mime: mime, CreatedBy: userID, Chunks: len(chunks), OriginalName: req.OriginalName}, nil
}

// ReindexChunkVectors re-embeds every chunk's content and rewrites its
// doc_chunks_vec row. Use after recreating the vec0 table (e.g. a metric
// change). Returns the number of chunks reindexed.
func (s *Store) ReindexChunkVectors(ctx context.Context) (int, error) {
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
	for _, c := range all {
		vecs, err := s.embedder.Embed(ctx, []string{c.content})
		if err != nil || len(vecs) != 1 {
			continue
		}
		blob, err := sqlite_vec.SerializeFloat32(vecs[0])
		if err != nil {
			return n, err
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO doc_chunks_vec(rowid, embedding) VALUES (?, ?)`, c.id, blob); err != nil {
			return n, err
		}
		n++
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
			        COALESCE(original_name, ''), COALESCE(stored_path, '')
			 FROM documents ORDER BY id DESC`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var d types.Document
			if err := rows.Scan(&d.ID, &d.Title, &d.URI, &d.Mime, &d.CreatedBy, &d.CreatedAt,
				&d.OriginalName, &d.StoredPath); err != nil {
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

// Delete removes a document (and its chunks/index rows) from the shared corpus.
func (s *Store) Delete(ctx context.Context, id int64) error {
	return s.DeleteFrom(ctx, s.db, id)
}

// DeleteFrom removes a document and its chunks/index rows from a specific corpus.
func (s *Store) DeleteFrom(ctx context.Context, db *sql.DB, id int64) error {
	tx, err := db.BeginTx(ctx, nil)
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

type scoredHit struct {
	id      int64
	content string
	title   string // source document title
	ord     int    // chunk ordinal within the document
	page    int    // 1-based source page (0 = no page structure)
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
	switch scope {
	case ScopePersonal:
		return "u"
	case ScopeProject:
		return "p"
	default:
		return "s"
	}
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
	if limit <= 0 {
		limit = 8
	}
	// When the FTS5 arm is score-gated it keeps only its top decile, so fuse
	// from a deeper pool to recover recall; ungated fusion stays at limit*4.
	depth := limit * 4
	if s.ftsGatePct > 0 && depth < 200 {
		depth = 200
	}
	// Embed the query once for the dense arm of every scope.
	var blob any
	if vecs, err := s.embedder.EmbedQuery(ctx, []string{query}); err == nil && len(vecs) == 1 {
		if b, err := sqlite_vec.SerializeFloat32(vecs[0]); err == nil {
			blob = b
		}
	}
	ranks := map[string]float64{}
	contents := map[string]string{}
	scopeOf := map[string]string{}
	sourceOf := map[string]string{}
	for _, sc := range scopes {
		if sc.DB != nil {
			s.searchInto(ctx, sc, query, blob, depth, ranks, contents, scopeOf, sourceOf)
		}
	}
	return topN(ranks, contents, scopeOf, sourceOf, limit), nil
}

// searchInto runs the gated/weighted dense+FTS5 arms against one corpus, folding
// scope-qualified RRF contributions into the shared maps.
func (s *Store) searchInto(ctx context.Context, sc ScopeDB, query string, blob any, depth int,
	ranks map[string]float64, contents, scopeOf, sourceOf map[string]string) {
	key := func(local int64) string {
		return "d." + scopePrefix(sc.Scope) + "." + strconv.FormatInt(local, 10)
	}
	if rows, err := sc.DB.QueryContext(ctx,
		`SELECT c.id, c.content, COALESCE(d.title, ''), c.ord, c.page
		 FROM doc_chunks_fts f JOIN doc_chunks c ON c.id = f.rowid
		 JOIN documents d ON d.id = c.document_id
		 WHERE doc_chunks_fts MATCH ? ORDER BY f.rank LIMIT ?`, query, depth); err == nil {
		hits := collect(rows)
		if s.ftsGatePct > 0 {
			hits = gateTop(hits, s.ftsGatePct)
		}
		foldKeyed(hits, s.ftsWeight, sc.Scope, key, ranks, contents, scopeOf, sourceOf)
	}
	if blob != nil {
		if rows, err := sc.DB.QueryContext(ctx,
			`SELECT c.id, c.content, COALESCE(d.title, ''), c.ord, c.page
			 FROM doc_chunks_vec v JOIN doc_chunks c ON c.id = v.rowid
			 JOIN documents d ON d.id = c.document_id
			 WHERE v.embedding MATCH ? AND k = ? ORDER BY v.distance`, blob, depth); err == nil {
			foldKeyed(collect(rows), 1.0, sc.Scope, key, ranks, contents, scopeOf, sourceOf)
		}
	}
}

// collect drains a (id, content, title, ord) result set, preserving its order.
func collect(rows *sql.Rows) []scoredHit {
	defer rows.Close()
	var out []scoredHit
	for rows.Next() {
		var h scoredHit
		if err := rows.Scan(&h.id, &h.content, &h.title, &h.ord, &h.page); err != nil {
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
	ranks map[string]float64, contents, scopeOf, sourceOf map[string]string) {
	for i, h := range hits {
		k := key(h.id)
		ranks[k] += weight / (rrfK + float64(i+1))
		contents[k] = h.content
		scopeOf[k] = scope
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

func topN(ranks map[string]float64, contents, scopeOf, sourceOf map[string]string, limit int) []types.SearchResult {
	out := make([]types.SearchResult, 0, len(ranks))
	for id, score := range ranks {
		out = append(out, types.SearchResult{ID: id, Content: contents[id], Score: score, Scope: scopeOf[id], Source: sourceOf[id]})
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
	if s.chunk.Strategy != "semadj" {
		return chunkText(text, 0), nil
	}
	sents := splitSentences(text)
	if len(sents) <= s.chunk.MinSentences {
		return chunkText(text, 0), nil
	}
	texts := make([]string, len(sents))
	for i, sr := range sents {
		texts[i] = sr.text
	}
	vecs, err := s.embedder.Embed(ctx, texts)
	if err != nil || len(vecs) != len(sents) {
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
