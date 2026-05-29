package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req types.LoginRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	token, user, err := s.Auth.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Audit.Log(r.Context(), &user.ID, "login", user.Username, nil)
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
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var req types.CreateUserRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	user, err := s.Auth.CreateUser(r.Context(), req.Username, req.Password, req.Role)
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
	convos, err := s.Conversations.List(r.Context(), u.ID, r.URL.Query().Get("q"))
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
	c, err := s.Conversations.Create(r.Context(), u.ID, req.Title)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if _, err := s.Conversations.Get(r.Context(), id, u.ID); err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	msgs, err := s.Conversations.Messages(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) handleDeleteConversation(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := s.Conversations.Delete(r.Context(), id, u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleConversationLog(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	// Owners and admins may read the log.
	if _, err := s.Conversations.Get(r.Context(), id, u.ID); err != nil && u.Role != "admin" {
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
	mems, err := s.Memory.List(r.Context(), u.ID, r.URL.Query().Get("scope"), 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, mems)
}

func (s *Server) handleListMemoryConflicts(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	conflicts, err := s.Memory.ListConflicts(r.Context(), u.ID, 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conflicts == nil {
		conflicts = []types.MemoryConflict{}
	}
	writeJSON(w, http.StatusOK, conflicts)
}

func (s *Server) handleResolveMemoryConflict(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := s.Memory.ResolveConflict(r.Context(), id, u.ID); err != nil {
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
	res, err := s.Memory.Search(r.Context(), r.URL.Query().Get("q"), u.ID, r.URL.Query().Get("scope"), 10)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleGetMemory(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	m, err := s.Memory.Get(r.Context(), id, u.ID)
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
	m, err := s.Memory.Add(r.Context(), req, u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Audit.Log(r.Context(), &u.ID, "memory_write", req.Scope, nil)
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) handlePatchMemory(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var req types.UpdateMemoryRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Pinned != nil {
		if err := s.Memory.SetPinned(r.Context(), id, u.ID, *req.Pinned); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := s.Memory.Delete(r.Context(), id, u.ID, u.Role == "admin"); err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Audit.Log(r.Context(), &u.ID, "memory_delete", strconv.FormatInt(id, 10), nil)
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

func (s *Server) handleCreateDocument(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var req types.CreateDocumentRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	d, err := s.Docs.Ingest(r.Context(), req, u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Audit.Log(r.Context(), &u.ID, "document_ingest", req.Title, nil)
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) handleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
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
	if q := r.URL.Query().Get("user_id"); q != "" && u.Role == "admin" {
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
	rows, err := s.Usage.Query(r.Context(), target, from, to)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	entries, err := s.Audit.List(r.Context(), 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}
