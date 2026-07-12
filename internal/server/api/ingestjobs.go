package api

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/ivoras/harlequin/internal/shared/types"
)

// ingestJob tracks one asynchronous document ingestion so the client can poll
// its stage ("extracting" / "describing" / "embedding" with chunk counts)
// instead of holding an HTTP request open for the whole conversion. Jobs are
// in-memory only: a server restart loses them (the poll then 404s and the
// client reports the upload as failed — re-upload is idempotent enough).
type ingestJob struct {
	mu        sync.Mutex
	id        string
	userID    int64
	stage     string
	done      int
	total     int
	finished  bool
	errMsg    string
	doc       *types.Document
	updatedAt time.Time
}

func (j *ingestJob) set(stage string) {
	j.mu.Lock()
	j.stage, j.done, j.total, j.updatedAt = stage, 0, 0, time.Now()
	j.mu.Unlock()
}

// progress records embedded-chunk counts; the first call also flips the stage
// from "chunking" to "embedding" (chunking itself reports no counts, but with
// the semantic chunker it does noticeable embedding work of its own first).
func (j *ingestJob) progress(done, total int) {
	j.mu.Lock()
	j.stage, j.done, j.total, j.updatedAt = "embedding", done, total, time.Now()
	j.mu.Unlock()
}

func (j *ingestJob) fail(msg string) {
	j.mu.Lock()
	j.finished, j.errMsg, j.updatedAt = true, msg, time.Now()
	j.mu.Unlock()
}

func (j *ingestJob) succeed(doc *types.Document) {
	j.mu.Lock()
	j.finished, j.doc, j.stage, j.updatedAt = true, doc, "done", time.Now()
	j.mu.Unlock()
}

func (j *ingestJob) status() types.IngestJobStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return types.IngestJobStatus{
		ID: j.id, Stage: j.stage, Done: j.done, Total: j.total,
		Finished: j.finished, Error: j.errMsg, Document: j.doc,
	}
}

// ingestJobs is the server's in-memory job registry. Finished jobs are evicted
// lazily 15 minutes after their last update.
type ingestJobs struct {
	mu sync.Mutex
	m  map[string]*ingestJob
}

const ingestJobRetention = 15 * time.Minute

func (r *ingestJobs) start(userID int64) *ingestJob {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	j := &ingestJob{id: hex.EncodeToString(b), userID: userID, stage: "starting", updatedAt: time.Now()}
	r.mu.Lock()
	if r.m == nil {
		r.m = map[string]*ingestJob{}
	}
	for id, old := range r.m { // lazy eviction
		old.mu.Lock()
		expired := old.finished && time.Since(old.updatedAt) > ingestJobRetention
		old.mu.Unlock()
		if expired {
			delete(r.m, id)
		}
	}
	r.m[j.id] = j
	r.mu.Unlock()
	return j
}

// get returns the job only for the user who started it.
func (r *ingestJobs) get(id string, userID int64) *ingestJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	j := r.m[id]
	if j == nil || j.userID != userID {
		return nil
	}
	return j
}
