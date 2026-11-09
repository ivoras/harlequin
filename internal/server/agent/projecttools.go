package agent

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/ivoras/harlequin/internal/server/docevents"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// registerProjectTools adds create_project and move_doc. Both are owner/admin
// only: they are simply absent for ordinary users (who still have the web/TUI
// project UI).
func (a *Agent) registerProjectTools(reg map[string]toolEntry, rc *runContext) {
	if !rc.canShareMemory || a.Projects == nil {
		return
	}
	reg["create_project"] = toolEntry{
		def: fnTool("create_project", `Create a new project (a shared workspace with its own documents, memories, sessions and chatroom). The user becomes its first member; invite others from the Projects screen. Only when the user clearly asks for a new project. It does not switch this session into the project: tell the user to select it in the project picker. Returns the project id (use "p<id>" in references and as move_doc's to_project).`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "Project name, e.g. \"Acme leasing\""},
			},
			"required": []string{"name"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			name := strings.Join(strings.Fields(argString(args, "name")), " ")
			if name == "" || len(name) > 100 {
				return "error: name is required (at most 100 characters).", nil
			}
			existing, _ := a.Projects.List(ctx, rc.userID)
			for _, p := range existing {
				if strings.EqualFold(p.Name, name) {
					return fmt.Sprintf("error: you already have a project named %q (p%d); use it, or pick a different name.", p.Name, p.ID), nil
				}
			}
			p, err := a.Projects.Create(ctx, name, rc.userID)
			if err != nil {
				return "", err
			}
			a.Audit.Log(ctx, rc.userDB, "project_create", p.Name, map[string]any{"project_id": p.ID, "via": "assistant"})
			return fmt.Sprintf("Created project %q (p%d); the user is its first member. It is not the active project of this session — they can select it in the project picker. Refer to it as p%d.", p.Name, p.ID, p.ID), nil
		},
	}

	if a.Docs == nil || a.Storage == nil {
		return
	}
	reg["move_doc"] = toolEntry{
		def: fnTool("move_doc", `Move (or copy) a document into a project the user belongs to. The document may be personal (u.N) or in any of the user's projects (p.N for this session's project, p<id>.N for another); shared (s.N) documents cannot be moved. The text, search index and original file go along; the document gets a NEW id in the target project, and a move deletes the original, so its old reference stops working. Prefer mode "copy" if the user wants to keep the original. To move several documents, call this once per document. Use list_documents to resolve ids and the user's projects.`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"doc":        map[string]any{"type": "string", "description": "Scoped document id, e.g. \"u.12\" or \"p3.7\""},
				"to_project": map[string]any{"type": "string", "description": "Target project: id (\"3\" or \"p3\") or exact name"},
				"mode":       map[string]any{"type": "string", "enum": []string{"move", "copy"}, "description": "move (default) deletes the original after copying"},
			},
			"required": []string{"doc", "to_project"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			return a.moveDoc(ctx, rc, argString(args, "doc"), argString(args, "to_project"), argString(args, "mode"))
		},
	}
}

// resolveProject finds one of the user's projects by id ("3", "p3") or name.
func (a *Agent) resolveProject(ctx context.Context, rc *runContext, ref string) (*types.Project, string) {
	ref = strings.TrimSpace(ref)
	projs, err := a.Projects.List(ctx, rc.userID)
	if err != nil {
		return nil, "error: " + err.Error()
	}
	if id, err := strconv.ParseInt(strings.TrimPrefix(strings.ToLower(ref), "p"), 10, 64); err == nil {
		for i := range projs {
			if projs[i].ID == id {
				return &projs[i], ""
			}
		}
		return nil, fmt.Sprintf("error: project %s is not among the user's projects.", ref)
	}
	var hit []types.Project
	for _, p := range projs {
		if strings.EqualFold(p.Name, ref) {
			hit = append(hit, p)
		}
	}
	switch len(hit) {
	case 1:
		return &hit[0], ""
	case 0:
		return nil, fmt.Sprintf("error: no project named %q among the user's projects.", ref)
	}
	return nil, fmt.Sprintf("error: several projects are named %q; use the id (p<id>).", ref)
}

// withProjectWrite runs fn on a writable handle for the project, reusing the
// session's own open handle when it is that project.
func (a *Agent) withProjectWrite(ctx context.Context, rc *runContext, pid int64, fn func(*sql.DB) error) error {
	if pid == rc.projectID && rc.projectDB != nil {
		return fn(rc.projectDB)
	}
	return a.Storage.WithProject(ctx, pid, fn)
}

func (a *Agent) moveDoc(ctx context.Context, rc *runContext, ref, to, mode string) (string, error) {
	if mode == "" {
		mode = "move"
	}
	if mode != "move" && mode != "copy" {
		return `error: mode must be "move" or "copy".`, nil
	}
	dst, errMsg := a.resolveProject(ctx, rc, to)
	if errMsg != "" {
		return errMsg, nil
	}
	doc, errMsg := a.loadDocRef(ctx, rc, ref)
	if errMsg != "" {
		return errMsg, nil
	}

	// Where does the source live?
	var srcPID int64 // 0 = personal
	switch {
	case doc.Scope == documents.ScopePersonal:
	case doc.Scope == documents.ScopeProject:
		srcPID = rc.projectID
	case strings.HasPrefix(doc.Scope, documents.ScopeProject+":"):
		srcPID, _ = strconv.ParseInt(strings.TrimPrefix(doc.Scope, documents.ScopeProject+":"), 10, 64)
	default:
		return "error: shared documents cannot be moved into a project; copy its text with read_doc + save_doc if needed.", nil
	}
	if srcPID == dst.ID {
		return fmt.Sprintf("error: %s is already in project %q.", ref, dst.Name), nil
	}
	if srcPID != 0 { // loadDocRef already enforced membership; belt and braces
		if _, errMsg := a.resolveProject(ctx, rc, strconv.FormatInt(srcPID, 10)); errMsg != "" {
			return errMsg, nil
		}
	}

	dstFiles, err := a.Storage.ProjectFilesDir(dst.ID)
	if err != nil {
		return "", err
	}
	var srcFiles string
	if srcPID == 0 {
		srcFiles, err = a.Storage.UserFilesDir(rc.userID)
	} else {
		srcFiles, err = a.Storage.ProjectFilesDir(srcPID)
	}
	if err != nil {
		return "", err
	}

	var newID int64
	run := func(src *sql.DB) error {
		return a.withProjectWrite(ctx, rc, dst.ID, func(dstDB *sql.DB) error {
			var e error
			if newID, e = a.Docs.CopyInto(ctx, src, dstDB, srcFiles, dstFiles, doc.ID); e != nil {
				return e
			}
			if mode == "move" {
				// The copy is committed: a failure here leaves a duplicate, never a loss.
				if e := a.Docs.DeleteFrom(ctx, src, srcFiles, doc.ID); e != nil {
					return fmt.Errorf("copied to %q as p%d.%d, but removing the original failed: %w", dst.Name, dst.ID, newID, e)
				}
			}
			return nil
		})
	}
	if srcPID == 0 {
		err = run(rc.userDB)
	} else {
		err = a.withProjectWrite(ctx, rc, srcPID, run)
	}
	if err != nil {
		return "", err
	}

	a.DocEvents.Publish(docevents.Event{Op: docevents.Created, Scope: documents.ScopeProject, ProjectID: dst.ID, UserID: rc.userID, DocID: newID})
	if mode == "move" {
		del := docevents.Event{Op: docevents.Deleted, Scope: documents.ScopePersonal, UserID: rc.userID, DocID: doc.ID}
		if srcPID != 0 {
			del.Scope, del.ProjectID = documents.ScopeProject, srcPID
		}
		a.DocEvents.Publish(del)
	}
	a.Audit.Log(ctx, rc.userDB, "document_"+mode, fmt.Sprintf("%q -> %s (p%d)", doc.Title, dst.Name, dst.ID), map[string]any{"from": ref, "to_id": newID, "via": "assistant"})
	verb := "Moved"
	tail := fmt.Sprintf(" The old reference %s no longer works.", ref)
	if mode == "copy" {
		verb, tail = "Copied", " The original was kept."
	}
	return fmt.Sprintf("%s %q to project %q as p%d.%d.%s", verb, doc.Title, dst.Name, dst.ID, newID, tail), nil
}
