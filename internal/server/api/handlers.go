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
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/email"
	"github.com/ivoras/harlequin/internal/server/memory"
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

func (s *Server) handleListConversations(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var convos []types.Conversation
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		var e error
		convos, e = s.Conversations.List(r.Context(), udb, u.ID, r.URL.Query().Get("q"))
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, convos)
}

func (s *Server) handleCreateConversation(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var req types.CreateConversationRequest
	_ = decode(r, &req)
	var c *types.Conversation
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		var e error
		c, e = s.Conversations.Create(r.Context(), udb, u.ID, req.Title, req.Hat, types.APIREST, reqInterface(r))
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var msgs []types.Message
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		if _, e := s.Conversations.Get(r.Context(), udb, id, u.ID); e != nil {
			return e
		}
		var e error
		msgs, e = s.Conversations.Messages(r.Context(), udb, id)
		return e
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) handleDeleteConversation(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		return s.Conversations.Delete(r.Context(), udb, id, u.ID)
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleConversationLog(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	// Owners and admins may read the log.
	var ownErr error
	_ = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		_, ownErr = s.Conversations.Get(r.Context(), udb, id, u.ID)
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
	var res []types.SearchResult
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		var e error
		res, e = s.Memory.Search(r.Context(), udb, r.URL.Query().Get("q"), u.ID, r.URL.Query().Get("scope"), 10)
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
	docs, err := s.Docs.List(r.Context())
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
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		// File upload: extract text from the file (PDF via PDFium-wasm) and ingest.
		var err error
		req, err = s.documentFromUpload(w, r)
		if err != nil {
			return // documentFromUpload already wrote the error
		}
	} else if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}

	d, err := s.Docs.Ingest(r.Context(), req, u.ID)
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

	writeJSON(w, http.StatusCreated, d)
}

// documentFromUpload reads a multipart "file" field, extracts its text (PDFs go
// through PDFium-wasm; text-like files are used as-is), and builds an ingest
// request. On failure it writes the HTTP error and returns a non-nil error.
func (s *Server) documentFromUpload(w http.ResponseWriter, r *http.Request) (types.CreateDocumentRequest, error) {
	var req types.CreateDocumentRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid upload: "+err.Error())
		return req, err
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "missing file field")
		return req, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read file: "+err.Error())
		return req, err
	}

	title := r.FormValue("title")
	if title == "" {
		title = header.Filename
	}

	isPDF := strings.HasSuffix(strings.ToLower(header.Filename), ".pdf") ||
		header.Header.Get("Content-Type") == "application/pdf" ||
		bytes.HasPrefix(data, []byte("%PDF-"))

	switch {
	case isPDF:
		if s.PDFExtract == nil {
			writeErr(w, http.StatusUnsupportedMediaType, "PDF extraction is not available on this server")
			return req, fmt.Errorf("no pdf extractor")
		}
		text, pages, err := s.PDFExtract.Text(data)
		if err != nil {
			writeErr(w, http.StatusUnprocessableEntity, "PDF extraction failed: "+err.Error())
			return req, err
		}
		if strings.TrimSpace(text) == "" {
			writeErr(w, http.StatusUnprocessableEntity, "no extractable text in PDF (it may be scanned images)")
			return req, fmt.Errorf("empty pdf text")
		}
		log.Printf("documents: extracted %d chars from %q (%d pages) for user", len(text), header.Filename, pages)
		req = types.CreateDocumentRequest{Title: title, URI: "upload://" + header.Filename, Mime: "application/pdf", Content: text}
	case utf8.Valid(data):
		req = types.CreateDocumentRequest{Title: title, URI: "upload://" + header.Filename, Mime: "text/plain", Content: string(data)}
	default:
		writeErr(w, http.StatusUnsupportedMediaType, "unsupported file type (only PDF and text are supported)")
		return req, fmt.Errorf("unsupported file type")
	}
	return req, nil
}

func (s *Server) handleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireElevated(w, r); !ok {
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := s.Docs.Delete(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleSearchDocuments(w http.ResponseWriter, r *http.Request) {
	res, err := s.Docs.Search(r.Context(), r.URL.Query().Get("q"), 10)
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
