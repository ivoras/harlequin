package api

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/docxextract"
	"github.com/ivoras/harlequin/internal/server/email"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/server/sessionlog"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req types.LoginRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	token, user, err := s.Auth.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.Storage.WithUser(r.Context(), user.ID, func(udb *sql.DB) error {
		s.Audit.Log(r.Context(), udb, "login", user.Email, nil)
		s.ensureOnboarding(r.Context(), udb, user.ID)
		return nil
	})
	writeJSON(w, http.StatusOK, types.LoginResponse{Token: token, User: *user})
}

// handleRegistrationStatus reports whether self-registration is enabled, so
// clients can show or hide the registration UI.
func (s *Server) handleRegistrationStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, types.RegistrationStatus{Enabled: s.Cfg.Auth.AllowRegistrationValue()})
}

// handleRegister starts self-registration: it validates the email, stores a
// pending registration, and emails (or logs) a verification magic code.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.Auth.AllowRegistrationValue() {
		writeErr(w, http.StatusForbidden, "registration is disabled")
		return
	}
	var req types.RegisterRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	addr := strings.ToLower(strings.TrimSpace(req.Email))
	if !email.ValidAddress(addr) {
		writeErr(w, http.StatusBadRequest, "invalid email address")
		return
	}
	if len(req.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	code, err := s.Auth.StartRegistration(r.Context(), addr, req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrUserExists) {
			writeErr(w, http.StatusConflict, "an account with that email already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	body := "Your Harlequin verification code is: " + code + "\n\nIt expires in 15 minutes."
	if err := s.Email.Send(addr, "Harlequin verification code", body); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to send verification code")
		return
	}
	writeJSON(w, http.StatusOK, types.RegisterResponse{Status: "verification_sent", Email: addr})
}

// handleVerify completes registration with the emailed code, returning a login
// token on success (auto-login).
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.Auth.AllowRegistrationValue() {
		writeErr(w, http.StatusForbidden, "registration is disabled")
		return
	}
	var req types.VerifyRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	addr := strings.ToLower(strings.TrimSpace(req.Email))
	token, user, err := s.Auth.VerifyRegistration(r.Context(), addr, strings.TrimSpace(req.Code))
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrNoPendingRegistration):
			writeErr(w, http.StatusNotFound, "no pending registration for that email")
		case errors.Is(err, auth.ErrCodeExpired):
			writeErr(w, http.StatusGone, "verification code expired — please register again")
		case errors.Is(err, auth.ErrTooManyAttempts):
			writeErr(w, http.StatusTooManyRequests, "too many attempts — please register again")
		case errors.Is(err, auth.ErrInvalidCredentials):
			writeErr(w, http.StatusUnauthorized, "incorrect verification code")
		case errors.Is(err, auth.ErrUserExists):
			writeErr(w, http.StatusConflict, "an account with that email already exists")
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	_ = s.Storage.WithUser(r.Context(), user.ID, func(udb *sql.DB) error {
		s.Audit.Log(r.Context(), udb, "register", user.Email, nil)
		s.ensureOnboarding(r.Context(), udb, user.ID)
		return nil
	})
	writeJSON(w, http.StatusOK, types.LoginResponse{Token: token, User: *user})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")
	if len(token) > 7 {
		_ = s.Auth.Logout(r.Context(), token[7:])
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireOwner(w, r); !ok {
		return
	}
	var req types.CreateUserRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	user, err := s.Auth.CreateUser(r.Context(), req.Email, req.Password, req.Role)
	if err != nil {
		if errors.Is(err, auth.ErrUserExists) {
			writeErr(w, http.StatusConflict, "user exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var sessions []types.Session
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		var e error
		sessions, e = s.Sessions.List(r.Context(), udb, u.ID, r.URL.Query().Get("q"))
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var req types.CreateSessionRequest
	_ = decode(r, &req)
	var c *types.Session
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		var e error
		c, e = s.Sessions.Create(r.Context(), udb, u.ID, req.Title, req.Hat, types.APIREST, reqInterface(r))
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// handleSessionContext estimates how the session's next request would fill
// the model's context window, broken down by category (system prompt, skills,
// tools, messages). Pass ?project=<id> for a project session, and optionally
// ?model=<id> to size the context window against a specific model (typically
// the one last used, as reported by the client's most recent SSEDone event).
func (s *Server) handleSessionContext(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
	model := r.URL.Query().Get("model")
	if projectID > 0 {
		if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
	} else {
		var ownErr error
		_ = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
			_, ownErr = s.Sessions.Get(r.Context(), udb, id, u.ID)
			return nil
		})
		if ownErr != nil {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
	}
	breakdown, err := s.Agent.ContextBreakdown(r.Context(), projectID, id, u.ID, u.Email, u.Role, model)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, breakdown)
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var msgs []types.Message
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		if _, e := s.Sessions.Get(r.Context(), udb, id, u.ID); e != nil {
			return e
		}
		var e error
		msgs, e = s.Sessions.Messages(r.Context(), udb, id)
		return e
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		return s.Sessions.Delete(r.Context(), udb, id, u.ID)
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleClearSession wipes a session's messages so the next turn starts with a
// fresh context (the session, its title, and its hat survive). For a project
// session pass ?project=<id>; any member may clear it.
func (s *Server) handleClearSession(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
	var err error
	if projectID > 0 {
		if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
		err = s.Storage.WithProject(r.Context(), projectID, func(pdb *sql.DB) error {
			return s.Sessions.ClearMessages(r.Context(), pdb, id)
		})
	} else {
		err = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
			if _, e := s.Sessions.Get(r.Context(), udb, id, u.ID); e != nil {
				return e
			}
			return s.Sessions.ClearMessages(r.Context(), udb, id)
		})
	}
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	// Mark the clear in the trajectory log so a reader understands why the
	// composed context shrinks from here on.
	s.Session.Log(r.Context(), sessionlog.Event{
		SessionID: id, UserID: u.ID, Type: sessionlog.TypeSessionCleared,
		Data: map[string]any{"cleared_by": u.Email, "project_id": projectID},
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleSessionLog(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	// Owners and admins may read the log.
	var ownErr error
	_ = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		_, ownErr = s.Sessions.Get(r.Context(), udb, id, u.ID)
		return nil
	})
	if ownErr != nil && !types.IsElevated(u.Role) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	data, err := s.Session.ReadAll(u.ID, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no log")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	_, _ = w.Write(data)
}

func (s *Server) handleListMemory(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var mems []types.Memory
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		var e error
		mems, e = s.Memory.List(r.Context(), udb, u.ID, r.URL.Query().Get("scope"), 200)
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, mems)
}

func (s *Server) handleListMemoryConflicts(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	conflicts := []types.MemoryConflict{}
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		cs, e := s.Memory.ListConflicts(r.Context(), udb, u.ID, 200)
		if cs != nil {
			conflicts = cs
		}
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, conflicts)
}

func (s *Server) handleResolveMemoryConflict(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id := chi.URLParam(r, "id")
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		return s.Memory.ResolveConflict(r.Context(), udb, id)
	})
	if err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleSearchMemory(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	q := r.URL.Query().Get("q")
	projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
	var res []types.SearchResult
	// When a project is active (?project=<id>), fuse project+shared+personal;
	// otherwise the requested scope (default personal+shared).
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		if projectID > 0 {
			return s.Storage.WithProjectReadOnly(r.Context(), projectID, func(pdb *sql.DB) error {
				var e error
				res, e = s.Memory.SearchFused(r.Context(), udb, pdb, q, u.ID, 10)
				return e
			})
		}
		var e error
		res, e = s.Memory.Search(r.Context(), udb, q, u.ID, r.URL.Query().Get("scope"), 10)
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleFindMemory returns full memory records matching a query (ranked
// best-first, across the user's own and shared memories) for listing.
func (s *Server) handleFindMemory(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var mems []types.Memory
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		var e error
		mems, e = s.Memory.Find(r.Context(), udb, r.URL.Query().Get("q"), u.ID, 25)
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, mems)
}

func (s *Server) handleGetMemory(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id := chi.URLParam(r, "id")
	var m *types.Memory
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		var e error
		m, e = s.Memory.Get(r.Context(), udb, id, u.ID)
		return e
	})
	if err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleCreateMemory(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var req types.CreateMemoryRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Scope == "shared" && !types.IsElevated(u.Role) {
		writeErr(w, http.StatusForbidden, "only owner or admin can create shared memories")
		return
	}
	var m *types.Memory
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		// AddWithConflicts records any conflicts so they surface via
		// /memory/conflicts; the flagged hits themselves are not returned here.
		var e error
		m, _, e = s.Memory.AddWithConflicts(r.Context(), udb, req, u.ID)
		if e != nil {
			return e
		}
		s.Audit.Log(r.Context(), udb, "memory_write", req.Scope, nil)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) handlePatchMemory(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id := chi.URLParam(r, "id")
	var req types.UpdateMemoryRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Pinned != nil {
		err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
			return s.Memory.SetPinned(r.Context(), udb, id, u.ID, *req.Pinned)
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id := chi.URLParam(r, "id")
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		if e := s.Memory.Delete(r.Context(), udb, id, u.ID, types.IsElevated(u.Role)); e != nil {
			return e
		}
		s.Audit.Log(r.Context(), udb, "memory_delete", id, nil)
		return nil
	})
	if err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
	var docs []types.Document
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		list := func(projDB *sql.DB) error {
			var e error
			docs, e = s.Docs.ListScoped(r.Context(), s.Docs.ScopesFor(udb, projDB))
			return e
		}
		if projectID > 0 {
			if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
				return list(nil) // not a member: fall back to personal+shared
			}
			return s.Storage.WithProjectReadOnly(r.Context(), projectID, list)
		}
		return list(nil)
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, docs)
}

// maxUploadBytes caps an uploaded document file (covers large PDFs, not media).
const maxUploadBytes = 64 << 20 // 64 MiB

func (s *Server) handleCreateDocument(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())

	var req types.CreateDocumentRequest
	var raw []byte // raw uploaded bytes (nil for raw-text JSON ingests), persisted to disk
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		// File upload: extract text from the file (PDF via PDFium-wasm) and ingest.
		var err error
		req, raw, err = s.documentFromUpload(w, r)
		if err != nil {
			return // documentFromUpload already wrote the error
		}
	} else if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}

	// ingestAndStore ingests into a corpus, then persists the uploaded file under
	// that scope's files/ directory with a 7-bit ASCII name (the original name is
	// kept in the DB) and records the on-disk path.
	ingestAndStore := func(ctx context.Context, db *sql.DB, filesDir string, dirErr error) (*types.Document, error) {
		doc, err := s.Docs.IngestInto(ctx, db, req, u.ID)
		if err != nil || doc == nil || len(raw) == 0 || dirErr != nil || filesDir == "" {
			return doc, err
		}
		name := fmt.Sprintf("%d-%s", doc.ID, documents.AsciiName(req.OriginalName))
		if werr := os.WriteFile(filepath.Join(filesDir, name), raw, 0o600); werr != nil {
			log.Printf("documents: persist upload %q: %v", name, werr)
			return doc, err
		}
		if serr := s.Docs.SetStoredPath(ctx, db, doc.ID, name); serr == nil {
			doc.StoredPath = name
		}
		return doc, err
	}

	// Catalogue description: one small LLM call over the opening text, stored
	// with the document so the assistant can resolve paraphrased references
	// ("the new EEA regulation") from list_documents. Best-effort.
	if req.Description == "" && s.Agent != nil {
		req.Description = s.Agent.DescribeDocument(r.Context(), req.Title, req.Content)
	}

	// Route the ingest to the requested corpus, checking permission for each:
	//   personal (default) — any user, into their own DB
	//   shared             — owners/admins only (org-wide)
	//   project            — members of the project only
	scope := req.Scope
	if scope == "" {
		scope = documents.ScopePersonal
	}
	var d *types.Document
	var err error
	switch scope {
	case documents.ScopePersonal:
		err = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
			dir, dErr := s.Storage.UserFilesDir(u.ID)
			var e error
			d, e = ingestAndStore(r.Context(), udb, dir, dErr)
			return e
		})
	case documents.ScopeShared:
		if !types.IsElevated(u.Role) {
			writeErr(w, http.StatusForbidden, "only owners/admins can add shared documents")
			return
		}
		dir, dErr := s.Storage.SharedFilesDir()
		d, err = ingestAndStore(r.Context(), s.Storage.Shared, dir, dErr)
	case documents.ScopeProject:
		if req.ProjectID == 0 {
			writeErr(w, http.StatusBadRequest, "project_id required for project scope")
			return
		}
		if member, _ := s.Projects.IsMember(r.Context(), req.ProjectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
		err = s.Storage.WithProject(r.Context(), req.ProjectID, func(pdb *sql.DB) error {
			dir, dErr := s.Storage.ProjectFilesDir(req.ProjectID)
			var e error
			d, e = ingestAndStore(r.Context(), pdb, dir, dErr)
			return e
		})
	default:
		writeErr(w, http.StatusBadRequest, "invalid scope (personal|shared|project)")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		s.Audit.Log(r.Context(), udb, "document_ingest", req.Title, nil)
		return nil
	})

	// Optional bridge: distill durable facts from the document into memory.
	if s.Cfg.Memory.ExtractFromDocumentsEnabled() && s.Agent != nil && strings.TrimSpace(req.Content) != "" {
		go s.Agent.ExtractMemoriesFromText(context.Background(), u.ID, req.Title, req.Content, types.IsElevated(u.Role))
	}

	// The synchronous description attempt can lose to model load/contention;
	// retry in the background and repair the stored row.
	if d != nil && req.Description == "" && s.Agent != nil {
		s.describeLater(u.ID, scope, req.ProjectID, d.ID, req.Title, req.Content)
	}

	writeJSON(w, http.StatusCreated, d)
}

// describeLater regenerates a document's missing catalogue description in the
// background and stores it. It runs behind the background-LLM gate, so it waits
// out the post-upload memory-bridge job and any live turns instead of timing
// out against them.
func (s *Server) describeLater(userID int64, scope string, projectID, docID int64, title, content string) {
	go func() {
		ctx := context.Background()
		desc := s.Agent.DescribeDocumentBackground(ctx, title, content)
		if desc == "" {
			return
		}
		var err error
		switch scope {
		case documents.ScopeShared:
			err = s.Docs.SetDescription(ctx, s.Storage.Shared, docID, desc)
		case documents.ScopeProject:
			err = s.Storage.WithProject(ctx, projectID, func(pdb *sql.DB) error {
				return s.Docs.SetDescription(ctx, pdb, docID, desc)
			})
		default:
			err = s.Storage.WithUser(ctx, userID, func(udb *sql.DB) error {
				return s.Docs.SetDescription(ctx, udb, docID, desc)
			})
		}
		if err != nil {
			log.Printf("documents: store retried description for %q: %v", title, err)
			return
		}
		log.Printf("documents: described %q on background retry", title)
	}()
}

// documentFromUpload reads a multipart "file" field, extracts its text (PDF and
// DOCX go through Docling when configured, else PDFium-wasm / docxextract;
// text-like files are used as-is), and builds an ingest request. On failure it
// writes the HTTP error and returns a non-nil error.
// It also returns the raw uploaded bytes so the caller can persist the file.
func (s *Server) documentFromUpload(w http.ResponseWriter, r *http.Request) (types.CreateDocumentRequest, []byte, error) {
	var req types.CreateDocumentRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid upload: "+err.Error())
		return req, nil, err
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "missing file field")
		return req, nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read file: "+err.Error())
		return req, nil, err
	}

	title := r.FormValue("title")
	if title == "" {
		title = header.Filename
	}
	// Namespace to ingest into (permission-checked by the caller).
	scope := r.FormValue("scope")
	projectID, _ := strconv.ParseInt(r.FormValue("project_id"), 10, 64)

	const docxMime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	lowName := strings.ToLower(header.Filename)
	isPDF := strings.HasSuffix(lowName, ".pdf") ||
		header.Header.Get("Content-Type") == "application/pdf" ||
		bytes.HasPrefix(data, []byte("%PDF-"))
	isDOCX := !isPDF && (strings.HasSuffix(lowName, ".docx") ||
		header.Header.Get("Content-Type") == docxMime)

	var pageStarts []int
	switch {
	case isPDF, isDOCX:
		mime := "application/pdf"
		if isDOCX {
			mime = docxMime
		}
		// Docling first when configured: structured text (layout, tables, OCR).
		// PDFs go through Docling's JSON format so page citations survive; DOCX
		// (unpaged) uses plain Markdown. Any failure falls back to the built-in
		// extractors below.
		var text string
		if s.Docling != nil {
			var md string
			var err error
			if isPDF {
				md, pageStarts, err = s.Docling.ConvertPaged(r.Context(), header.Filename, data)
			} else {
				md, err = s.Docling.Convert(r.Context(), header.Filename, data)
			}
			if err != nil {
				pageStarts = nil
				log.Printf("documents: docling convert %q failed, using built-in extractor: %v", header.Filename, err)
			} else {
				log.Printf("documents: docling converted %q (%d chars, %d pages)", header.Filename, len(md), len(pageStarts))
				text = md
			}
		}
		switch {
		case text != "":
			// Extracted above (PDFs keep their page mapping via pageStarts).
		case isPDF:
			if s.PDFExtract == nil {
				writeErr(w, http.StatusUnsupportedMediaType, "PDF extraction is not available on this server")
				return req, nil, fmt.Errorf("no pdf extractor")
			}
			pages, err := s.PDFExtract.Pages(data)
			if err != nil {
				writeErr(w, http.StatusUnprocessableEntity, "PDF extraction failed: "+err.Error())
				return req, nil, err
			}
			// Join pages (matching Text's "\n\n" separator) and record the rune
			// offset at which each page begins so chunks can be mapped to a page.
			var sb strings.Builder
			runeOff := 0
			for i, pg := range pages {
				if i > 0 {
					sb.WriteString("\n\n")
					runeOff += 2
				}
				pageStarts = append(pageStarts, runeOff)
				sb.WriteString(pg)
				runeOff += utf8.RuneCountInString(pg)
			}
			text = sb.String()
		default: // DOCX
			t, err := docxextract.Text(data)
			if err != nil {
				writeErr(w, http.StatusUnprocessableEntity, "DOCX extraction failed: "+err.Error())
				return req, nil, err
			}
			text = t
		}
		if strings.TrimSpace(text) == "" {
			writeErr(w, http.StatusUnprocessableEntity, "no extractable text in the file (it may be scanned images)")
			return req, nil, fmt.Errorf("empty extracted text")
		}
		log.Printf("documents: extracted %d chars from %q for user", len(text), header.Filename)
		req = types.CreateDocumentRequest{Title: title, URI: "upload://" + header.Filename, Mime: mime, Content: text}
	case utf8.Valid(data):
		req = types.CreateDocumentRequest{Title: title, URI: "upload://" + header.Filename, Mime: "text/plain", Content: string(data)}
	default:
		writeErr(w, http.StatusUnsupportedMediaType, "unsupported file type (only PDF, DOCX and text are supported)")
		return req, nil, fmt.Errorf("unsupported file type")
	}
	req.Scope = scope
	req.ProjectID = projectID
	req.OriginalName = header.Filename
	req.PageStarts = pageStarts
	return req, data, nil
}

func (s *Server) handleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = documents.ScopeShared // back-compatible default
	}
	var err error
	switch scope {
	case documents.ScopePersonal:
		err = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
			return s.Docs.DeleteFrom(r.Context(), udb, id)
		})
	case documents.ScopeShared:
		if _, ok := requireElevated(w, r); !ok {
			return
		}
		err = s.Docs.Delete(r.Context(), id)
	case documents.ScopeProject:
		projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
		if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
		err = s.Storage.WithProject(r.Context(), projectID, func(pdb *sql.DB) error {
			return s.Docs.DeleteFrom(r.Context(), pdb, id)
		})
	default:
		writeErr(w, http.StatusBadRequest, "invalid scope (personal|shared|project)")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGetDocChunk resolves a chunk citation id (d.u.N / d.s.N / d.p.N) to
// its document, title, page, and file availability, so clients can render
// citations as links. Project chunks need ?project=<id> (member only).
func (s *Server) handleGetDocChunk(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	cid := chi.URLParam(r, "cid")
	parts := strings.Split(cid, ".")
	if len(parts) != 3 || parts[0] != "d" {
		writeErr(w, http.StatusBadRequest, "invalid chunk id (want d.u.N / d.s.N / d.p.N)")
		return
	}
	local, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid chunk id")
		return
	}
	var info *types.DocChunkInfo
	switch parts[1] {
	case "u":
		err = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
			var e error
			info, e = s.Docs.ChunkInfo(r.Context(), udb, local)
			return e
		})
		if info != nil {
			info.Scope = documents.ScopePersonal
		}
	case "s":
		info, err = s.Docs.ChunkInfo(r.Context(), s.Docs.SharedDB(), local)
		if info != nil {
			info.Scope = documents.ScopeShared
		}
	case "p":
		projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
		if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
		err = s.Storage.WithProjectReadOnly(r.Context(), projectID, func(pdb *sql.DB) error {
			var e error
			info, e = s.Docs.ChunkInfo(r.Context(), pdb, local)
			return e
		})
		if info != nil {
			info.Scope = documents.ScopeProject
		}
	default:
		writeErr(w, http.StatusBadRequest, "invalid chunk scope")
		return
	}
	if err != nil || info == nil {
		writeErr(w, http.StatusNotFound, "chunk not found")
		return
	}
	info.ID = cid
	writeJSON(w, http.StatusOK, info)
}

// handleGetDocumentFile serves a document's stored original file (inline, with
// its mime type) so citations can open it — e.g. a PDF at #page=N. Scope rules
// mirror the corpora: personal = the caller's own, shared = any authenticated
// user, project = members.
func (s *Server) handleGetDocumentFile(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	scope := r.URL.Query().Get("scope")
	var dir, stored, mime, title string
	var err error
	switch scope {
	case documents.ScopePersonal:
		err = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
			var e error
			stored, mime, title, e = s.Docs.StoredFile(r.Context(), udb, id)
			return e
		})
		if err == nil {
			dir, err = s.Storage.UserFilesDir(u.ID)
		}
	case documents.ScopeShared, "":
		stored, mime, title, err = s.Docs.StoredFile(r.Context(), s.Docs.SharedDB(), id)
		if err == nil {
			dir, err = s.Storage.SharedFilesDir()
		}
	case documents.ScopeProject:
		projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
		if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
		err = s.Storage.WithProjectReadOnly(r.Context(), projectID, func(pdb *sql.DB) error {
			var e error
			stored, mime, title, e = s.Docs.StoredFile(r.Context(), pdb, id)
			return e
		})
		if err == nil {
			dir, err = s.Storage.ProjectFilesDir(projectID)
		}
	default:
		writeErr(w, http.StatusBadRequest, "invalid scope")
		return
	}
	if err != nil || stored == "" || !filepath.IsLocal(stored) {
		writeErr(w, http.StatusNotFound, "no stored file for this document")
		return
	}
	if mime != "" {
		w.Header().Set("Content-Type", mime)
	}
	w.Header().Set("Content-Disposition", "inline; filename=\""+documents.AsciiName(title)+"\"")
	http.ServeFile(w, r, filepath.Join(dir, stored))
}

// handleGetDocumentContent returns a document's full text content — every
// chunk concatenated in order — for documents with no stored original file
// (plain-text ingests, including save_doc reports). PDF/DOCX uploads should
// use /documents/{id}/file instead (their content lives in an actual file);
// this endpoint works for any document type but exists specifically to view
// TXT-type documents that /file cannot serve.
func (s *Server) handleGetDocumentContent(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	scope := r.URL.Query().Get("scope")
	// FullText must run inside the WithUser/WithProjectReadOnly callback: both
	// close their *sql.DB when the callback returns, so capturing the handle
	// for use afterward (as an earlier version of this handler did) queries an
	// already-closed connection and every lookup fails as "not found".
	var content string
	var err error
	switch scope {
	case documents.ScopePersonal:
		err = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
			var e error
			content, e = s.Docs.FullText(r.Context(), udb, id)
			return e
		})
	case documents.ScopeShared, "":
		content, err = s.Docs.FullText(r.Context(), s.Docs.SharedDB(), id)
	case documents.ScopeProject:
		projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
		if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
		err = s.Storage.WithProjectReadOnly(r.Context(), projectID, func(pdb *sql.DB) error {
			var e error
			content, e = s.Docs.FullText(r.Context(), pdb, id)
			return e
		})
	default:
		writeErr(w, http.StatusBadRequest, "invalid scope")
		return
	}
	if err != nil || content == "" {
		writeErr(w, http.StatusNotFound, "document not found or has no content")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": content})
}

func (s *Server) handleSearchDocuments(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	q := r.URL.Query().Get("q")
	projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
	var res []types.SearchResult
	// Fuse the user's personal corpus and (when ?project=<id> is set, i.e. the
	// user has switched to a project) the project corpus with the shared corpus.
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		search := func(projDB *sql.DB) error {
			var e error
			res, e = s.Docs.SearchScoped(r.Context(), s.Docs.ScopesFor(udb, projDB), q, 10)
			return e
		}
		if projectID > 0 {
			return s.Storage.WithProjectReadOnly(r.Context(), projectID, search)
		}
		return search(nil)
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	target := u.ID
	if q := r.URL.Query().Get("user_id"); q != "" && types.IsElevated(u.Role) {
		if id, err := strconv.ParseInt(q, 10, 64); err == nil {
			target = id
		}
	}
	var from, to time.Time
	if v := r.URL.Query().Get("from"); v != "" {
		from, _ = time.Parse(time.RFC3339, v)
	}
	if v := r.URL.Query().Get("to"); v != "" {
		to, _ = time.Parse(time.RFC3339, v)
	}
	var rows []types.UsageRecord
	err := s.Storage.WithUser(r.Context(), target, func(udb *sql.DB) error {
		var e error
		rows, e = s.Usage.Query(r.Context(), udb, target, from, to)
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireElevated(w, r); !ok {
		return
	}
	// Audit entries live in each user's database; aggregate across users.
	var entries []types.AuditEntry
	err := s.Storage.EachUser(r.Context(), func(userID int64, udb *sql.DB) error {
		es, e := s.Audit.List(r.Context(), udb, userID, 200)
		entries = append(entries, es...)
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].CreatedAt.After(entries[j].CreatedAt)
	})
	if len(entries) > 200 {
		entries = entries[:200]
	}
	writeJSON(w, http.StatusOK, entries)
}
