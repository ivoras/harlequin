package api

import (
	"mime"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/documents"
)

// downloadTypes are served with their own type; anything else (notably HTML and
// SVG, which a browser would run) goes out as an opaque download.
var downloadTypes = map[string]string{
	".pdf": "application/pdf", ".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".csv":  "text/csv; charset=utf-8", ".tsv": "text/tab-separated-values; charset=utf-8",
	".txt": "text/plain; charset=utf-8", ".md": "text/markdown; charset=utf-8", ".json": "application/json",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".zip": "application/zip",
}

// handleGetTmpFile downloads a file from the caller's own tmp sandbox
// (tmp://name), the link the agent gives for a working file it made. Always an
// attachment: it is the agent's own output, so it never renders in the page.
func (s *Server) handleGetTmpFile(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	rel := chi.URLParam(r, "*")
	if s.Agent == nil || strings.TrimSpace(rel) == "" {
		writeErr(w, http.StatusNotFound, "no such file")
		return
	}
	data, err := s.Agent.ReadTmp(u.ID, rel)
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "no such file: temporary files are removed after a while; ask for it to be made again")
		} else {
			writeErr(w, http.StatusBadRequest, "cannot read this file")
		}
		return
	}
	ct := downloadTypes[strings.ToLower(path.Ext(rel))]
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": documents.AsciiName(path.Base(rel))}))
	_, _ = w.Write(data)
}
