package agent

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/llm"
)

const (
	maxVisionImages = 6       // newest images win; older ones stay as text references
	maxImageBytes   = 8 << 20 // shortcut: no downscaling, oversized images are skipped; upgrade if users hit it
)

// attachRefRE matches an attachment reference, @"u.18/photo.png", capturing the scoped id.
var attachRefRE = regexp.MustCompile(`@"([^"/\s]+\.\d+)/[^"]*"`)

// attachImages turns image attachments into vision input: for each user
// message listing @"<ref>/<name>" whose document is an image, the file is sent
// inline with that message. Derived from the stored text every turn, so
// nothing extra is persisted and history keeps its images (newest
// maxVisionImages only, to bound the prompt).
func (a *Agent) attachImages(ctx context.Context, rc *runContext, msgs []llm.Message) {
	if !a.Vision || a.Docs == nil {
		return
	}
	budget := maxVisionImages
	for i := len(msgs) - 1; i >= 0 && budget > 0; i-- {
		if msgs[i].Role != llm.RoleUser {
			continue
		}
		for _, m := range attachRefRE.FindAllStringSubmatch(msgs[i].Content, -1) {
			if budget == 0 {
				break
			}
			if im, ok := a.imageForRef(ctx, rc, m[1]); ok {
				msgs[i].Images = append(msgs[i].Images, im)
				budget--
			}
		}
	}
}

// imageForRef loads the stored file behind a scoped document id when it is an image.
func (a *Agent) imageForRef(ctx context.Context, rc *runContext, ref string) (llm.Image, bool) {
	tok, idStr, ok := strings.Cut(ref, ".")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if !ok || err != nil {
		return llm.Image{}, false
	}
	scope, db, errMsg := a.scopeForToken(ctx, rc, tok)
	if errMsg != "" || db == nil {
		return llm.Image{}, false
	}
	stored, mime, _, err := a.Docs.StoredFile(ctx, db, id)
	if err != nil || !strings.HasPrefix(mime, "image/") || stored == "" || !filepath.IsLocal(stored) {
		return llm.Image{}, false
	}
	var dir string
	switch {
	case scope == documents.ScopePersonal:
		dir, err = a.Storage.UserFilesDir(rc.userID)
	case scope == documents.ScopeShared:
		dir, err = a.Storage.SharedFilesDir()
	case scope == documents.ScopeProject:
		dir, err = a.Storage.ProjectFilesDir(rc.projectID)
	default: // "project:<id>"
		pid, _ := strconv.ParseInt(strings.TrimPrefix(scope, documents.ScopeProject+":"), 10, 64)
		dir, err = a.Storage.ProjectFilesDir(pid)
	}
	if err != nil {
		return llm.Image{}, false
	}
	path := filepath.Join(dir, stored)
	if fi, err := os.Stat(path); err != nil || fi.Size() > maxImageBytes {
		return llm.Image{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return llm.Image{}, false
	}
	return llm.Image{Mime: mime, Data: data}, true
}
