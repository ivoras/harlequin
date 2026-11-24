package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"unicode/utf8"
)

// File references. Besides saved documents (u.12, s.3, p.7) the tools work on
// files: the per-user sandbox (tmp://… scratch, storage://… kept; a bare path
// means tmp://) and the files of skills (skill://<skill>/<path>). Every file tool
// resolves a reference through the helpers here, so all of them accept the same
// paths.

func isSkillPath(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), "skill://") }

// splitSkillPath splits skill://<skill>/<path>; rel is "" for skill://<skill>.
func splitSkillPath(p string) (name, rel string, ok bool) {
	name, rel, _ = strings.Cut(strings.TrimPrefix(strings.TrimSpace(p), "skill://"), "/")
	return name, strings.Trim(rel, "/"), name != ""
}

const skillPathHelp = `a skill file is named skill://<skill>/<path>, e.g. "skill://weekly-report/SKILL.md"`

// fileStore is what Grep and file listings need from a namespace.
type fileStore interface {
	List(glob string) ([]string, error)
	Read(rel string) ([]byte, error)
}

// skillStore serves one skill's stored (unrendered) files.
type skillStore map[string]string

func (s skillStore) List(string) ([]string, error) {
	out := make([]string, 0, len(s))
	for rel := range s {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, nil
}

func (s skillStore) Read(rel string) ([]byte, error) {
	c, ok := s[rel]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return []byte(c), nil
}

// skillSource returns a skill's stored files as the session resolves them (worn
// hat's overlay, then project → shared → personal) and the scope they came from.
// Only load_skill renders <?js ?> templates; everything else sees the source.
func (a *Agent) skillSource(ctx context.Context, rc *runContext, name string) (map[string]string, string, error) {
	if a.Skills == nil {
		return nil, "", errors.New("skills are not available here")
	}
	files, source, err := a.Skills.ResolveEffectiveRaw(ctx, rc.userDB, rc.projectDB, name, rc.hat)
	if err != nil {
		return nil, "", fmt.Errorf("no skill %q (list_skills shows the skills)", name)
	}
	return files, source, nil
}

// fileRoot maps a file reference to its store, the URI prefix that rebuilds a
// reference from a path in the store, and the path within it ("" = the root).
func (a *Agent) fileRoot(ctx context.Context, rc *runContext, p string) (fileStore, string, string, error) {
	p = strings.TrimSpace(p)
	if isSkillPath(p) {
		name, rel, ok := splitSkillPath(p)
		if !ok {
			return nil, "", "", errors.New(skillPathHelp)
		}
		files, _, err := a.skillSource(ctx, rc, name)
		if err != nil {
			return nil, "", "", err
		}
		return skillStore(files), "skill://" + name + "/", rel, nil
	}
	root, prefix, rel, err := a.resolveNamespaceRoot(rc, p)
	if err != nil {
		return nil, "", "", err
	}
	return root, prefix, rel, nil
}

// readFile reads a sandbox or skill file.
func (a *Agent) readFile(ctx context.Context, rc *runContext, p string) ([]byte, error) {
	st, _, rel, err := a.fileRoot(ctx, rc, p)
	if err != nil {
		return nil, err
	}
	if rel == "" {
		return nil, fmt.Errorf("%s is a folder, not a file", p)
	}
	data, err := st.Read(rel)
	if sk, ok := st.(skillStore); ok && err != nil {
		have, _ := sk.List("")
		name, _, _ := splitSkillPath(p)
		return nil, fmt.Errorf("skill %q has no file %q; its files are: %s", name, rel, strings.Join(have, ", "))
	}
	return data, err
}

// fileExists reports whether a sandbox or skill file exists.
func (a *Agent) fileExists(ctx context.Context, rc *runContext, p string) bool {
	st, _, rel, err := a.fileRoot(ctx, rc, p)
	if err != nil || rel == "" {
		return false
	}
	_, err = st.Read(rel)
	return err == nil
}

// writeFile writes a sandbox or skill file and returns what it did. scope
// applies to skill files only (default personal). An existing file is replaced
// only with overwrite.
func (a *Agent) writeFile(ctx context.Context, rc *runContext, p string, data []byte, overwrite bool, scope string) (string, string) {
	p = strings.TrimSpace(p)
	if !overwrite && a.fileExists(ctx, rc, p) {
		return "", fmt.Sprintf("error: %s already exists. To change part of it use edit_doc; to replace it as a whole, read it first and call again with overwrite: true.", p)
	}
	if isSkillPath(p) {
		name, rel, ok := splitSkillPath(p)
		if !ok || rel == "" {
			return "", "error: " + skillPathHelp
		}
		if !utf8.Valid(data) {
			return "", fmt.Sprintf("error: skill files are text; %s would not be UTF-8 text", p)
		}
		if _, _, err := a.skillSource(ctx, rc, name); err != nil && rel != "SKILL.md" {
			return "", fmt.Sprintf("error: there is no skill %q; create it first with write_skill (or write_file at skill://%s/SKILL.md).", name, name)
		}
		return a.putSkillFile(ctx, rc, name, rel, string(data), scope)
	}
	root, _, rel, err := a.resolveNamespaceRoot(rc, p)
	if err != nil {
		return "", "error: " + err.Error()
	}
	if rel == "" {
		return "", fmt.Sprintf("error: %s is a folder; name a file, e.g. tmp://notes.md", p)
	}
	if err := root.Write(rel, data); err != nil {
		return "", fmt.Sprintf("error: cannot write %s: %v", p, err)
	}
	return fmt.Sprintf("Wrote %s (%d bytes).", p, len(data)), ""
}

// deleteFile removes a sandbox file or one file of a skill (from scope, default
// personal; SKILL.md is the skill itself, which delete_skill removes).
func (a *Agent) deleteFile(ctx context.Context, rc *runContext, p, scope string) string {
	p = strings.TrimSpace(p)
	if isSkillPath(p) {
		name, rel, ok := splitSkillPath(p)
		if !ok || rel == "" {
			return "error: " + skillPathHelp + ". To delete a whole skill use delete_skill."
		}
		if rel == "SKILL.md" {
			return fmt.Sprintf("error: SKILL.md is the skill itself; to delete skill %q use delete_skill.", name)
		}
		label, msg := skillScope(rc, scope)
		if msg != "" {
			return msg
		}
		db, _, err := a.Skills.ScopeDBFor(wireScope(label), rc.userDB, rc.projectDB)
		if err != nil {
			return "error: " + err.Error()
		}
		if !a.Skills.Has(ctx, db, name) {
			where := "no scope"
			if _, src, err := a.skillSource(ctx, rc, name); err == nil {
				where = "the " + displaySkillScope(src) + " scope"
			}
			return fmt.Sprintf("error: there is no %s copy of skill %q (it is in %s); pass that scope to delete the file there.", label, name, where)
		}
		if err := a.Skills.DeleteFile(ctx, db, name, rel, rc.userID); err != nil {
			return "error: " + err.Error()
		}
		a.Audit.Log(ctx, rc.userDB, "skill_file_delete", name+"/"+rel+" ("+label+")", map[string]any{"via": "assistant"})
		return fmt.Sprintf("Deleted %s from skill %q (%s scope).", rel, name, label)
	}
	root, _, rel, err := a.resolveNamespaceRoot(rc, p)
	if err != nil {
		return "error: " + err.Error()
	}
	if ok, _ := root.Exists(rel); rel == "" || !ok {
		return fmt.Sprintf("error: %s does not exist (list_documents with path %q lists the files)", p, "tmp://")
	}
	if err := root.Remove(rel); err != nil {
		return fmt.Sprintf("error: cannot delete %s: %v", p, err)
	}
	return "Deleted " + p + "."
}

// listFiles lists the files under a sandbox or skill location.
func (a *Agent) listFiles(ctx context.Context, rc *runContext, p string) string {
	st, prefix, rel, err := a.fileRoot(ctx, rc, p)
	if err != nil {
		return "error: " + err.Error()
	}
	all, err := st.List("")
	if err != nil {
		return "error: " + err.Error()
	}
	var sb strings.Builder
	n := 0
	for _, f := range all {
		if rel != "" && f != rel && !strings.HasPrefix(f, rel+"/") {
			continue
		}
		fmt.Fprintf(&sb, "- %s%s\n", prefix, f)
		n++
	}
	if n == 0 {
		return "(no files under " + p + ")"
	}
	return sb.String()
}

// displaySkillScope names a stored skill scope for the model ("user" → personal).
func displaySkillScope(s string) string {
	if s == "user" {
		return "personal"
	}
	return s
}
