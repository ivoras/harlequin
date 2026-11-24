package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/ivoras/harlequin/internal/server/skills"
)

// skillScope validates the scope a skill write targets and returns its display
// label. The assistant defaults to personal (the safe scope), unlike the REST
// default (project in a project session); "user" is a silent alias.
func skillScope(rc *runContext, scope string) (label, errMsg string) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "", "personal", skills.ScopeUser:
		return "personal", ""
	case skills.ScopeShared:
		if !rc.canShareMemory {
			return "", `error: shared skills can only be changed by an owner or admin; use scope "personal".`
		}
		return "shared", ""
	case skills.ScopeProject:
		if rc.projectDB == nil {
			return "", `error: no active project in this session; use scope "personal".`
		}
		return "project", ""
	}
	return "", `error: scope must be "personal", "shared" or "project".`
}

// putSkillFile writes one file of a skill into scope (default personal). A
// scope that does not hold the skill yet gets a copy of the effective one first,
// so the original stays untouched.
func (a *Agent) putSkillFile(ctx context.Context, rc *runContext, name, rel, content, scope string) (string, string) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return "", "error: the skill name must be a plain name (no slashes)."
	}
	if a.Skills == nil {
		return "", "error: skills are not available here"
	}
	label, msg := skillScope(rc, scope)
	if msg != "" {
		return "", msg
	}
	eff, effScope, effErr := a.skillSource(ctx, rc, name)
	// load_skill shows .md/.txt files rendered, so a rewrite made from that view
	// would silently drop the file's <?js ?> template code.
	if old, ok := eff[rel]; effErr == nil && ok && strings.Contains(old, "<?js") && !strings.Contains(content, "<?js") {
		return "", fmt.Sprintf("error: %s of skill %q contains <?js ?> template code (load_skill shows it rendered) and your version would delete it. read_doc \"skill://%s/%s\" shows the source; change it with edit_doc, keeping the template code.", rel, name, name, rel)
	}
	db, _, err := a.Skills.ScopeDBFor(wireScope(label), rc.userDB, rc.projectDB)
	if err != nil {
		return "", "error: " + err.Error()
	}
	copied := effErr == nil && !a.Skills.Has(ctx, db, name)
	if err := a.Skills.PutFile(ctx, db, rc.userDB, rc.projectDB, name, rel, content, rc.userID); err != nil {
		return "", "error: " + err.Error()
	}
	tool := "skill_file_save"
	if rel == "SKILL.md" {
		tool = "skill_save"
	}
	a.Audit.Log(ctx, rc.userDB, tool, name+"/"+rel+" ("+label+")", map[string]any{"via": "assistant"})
	out := fmt.Sprintf("Saved skill://%s/%s (%s scope). It applies from the next message.", name, rel, label)
	switch {
	case effScope == "hat":
		out += " Note: the hat being worn has its own copy of this skill, which still wins while the hat is on; tell the user (hat skills are changed in the Hats editor)."
	case copied:
		out += fmt.Sprintf(" This made a %s copy of the skill; the %s one is unchanged and is what others still use.", label, displaySkillScope(effScope))
	}
	return out, ""
}

const writeFileDescription = `Write a FILE (create it, or replace it as a whole): a working file in your sandbox, or a file of a skill. Paths: tmp://name.md is scratch space (deleted after some days; a bare path such as notes.md means tmp://), storage://name.md is kept, and skill://<skill>/<path> is a file of a skill — skill://<skill>/SKILL.md is its main file (frontmatter with name and description, then the instructions), other paths are its scripts and references (reachable as skill://<skill>/<path>). Files are not documents: they do not appear in the user's document list and search_docs does not find them — to give the user a saved, searchable document use save_doc instead (save_doc can also take a file you built here as its source). An existing file is only replaced with overwrite: true; to change part of a file use edit_doc. For an existing skill, read_doc the skill:// file first and send the complete revised file (anything you leave out is lost). Write skill files only when the user asks to create or change a skill. scope (skill files only) says whose copy is written: default "personal"; writing a skill in a scope that doesn't hold it yet makes a copy there, so the original stays untouched; never write a shared skill unless the user explicitly asked for that.`

func (a *Agent) registerFileWriteTools(reg map[string]toolEntry) {
	scopeProp := map[string]any{"type": "string", "enum": []string{"personal", "project", "shared"},
		"description": `Skill files only: whose copy of the skill is written. Default "personal"; "shared" is owner/admin only; "project" needs a project session.`}

	reg["write_file"] = toolEntry{
		def: fnTool("write_file", writeFileDescription, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":      map[string]any{"type": "string", "description": `File to write: tmp://notes.md, storage://notes.md, or skill://<skill>/<path>`},
				"content":   map[string]any{"type": "string", "description": "The complete file content."},
				"overwrite": map[string]any{"type": "boolean", "description": "Replace the file if it already exists. Default false: an existing file is an error."},
				"scope":     scopeProp,
			},
			"required": []string{"path", "content"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			p := strings.TrimSpace(argString(args, "path"))
			if p == "" {
				return "error: path is required, e.g. tmp://notes.md", nil
			}
			if docSourceRE.MatchString(p) {
				return fmt.Sprintf("error: %s is a saved document, not a file; change it with edit_doc.", p), nil
			}
			out, errMsg := a.writeFile(ctx, rc, p, []byte(argString(args, "content")), argBool(args, "overwrite", false), argString(args, "scope"))
			if errMsg != "" {
				return errMsg, nil
			}
			return out, nil
		},
	}

	reg["write_skill"] = toolEntry{
		def: fnTool("write_skill", `Create a NEW skill from its main file (SKILL.md: frontmatter with name and description, then the instructions). Only when the user asks to create a skill. Refused if the skill already exists: to change an existing skill, read_doc "skill://<name>/SKILL.md" and edit it with edit_doc (or replace it with write_file). Add scripts and references with write_file at skill://<name>/<path>.`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string", "description": "The full SKILL.md, starting with --- frontmatter (name, description)."},
				"scope":   scopeProp,
			},
			"required": []string{"name", "content"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			name := strings.TrimSpace(argString(args, "name"))
			if _, src, err := a.skillSource(ctx, rc, name); err == nil && name != "" {
				return fmt.Sprintf(`error: skill %q already exists (%s scope). To change it, read_doc "skill://%s/SKILL.md" and edit it with edit_doc, or replace the whole file with write_file (overwrite: true).`, name, displaySkillScope(src), name), nil
			}
			out, errMsg := a.putSkillFile(ctx, rc, name, "SKILL.md", argString(args, "content"), argString(args, "scope"))
			if errMsg != "" {
				return errMsg, nil
			}
			return out, nil
		},
	}

	// Retired name, kept for old sessions and skill texts: write_file on skill://.
	reg["write_skill_file"] = toolEntry{
		hidden: true,
		def:    fnTool("write_skill_file", "Retired: use write_file with path skill://<name>/<path>.", map[string]any{"type": "object"}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			name, rel := strings.TrimSpace(argString(args, "name")), strings.Trim(strings.TrimSpace(argString(args, "path")), "/")
			if name == "" || rel == "" {
				return "error: use write_file with path skill://<name>/<path> and content.", nil
			}
			out, errMsg := a.writeFile(ctx, rc, "skill://"+name+"/"+rel, []byte(argString(args, "content")), true, argString(args, "scope"))
			if errMsg != "" {
				return errMsg, nil
			}
			return out, nil
		},
	}

	reg["delete_skill"] = toolEntry{
		def: fnTool("delete_skill", `Permanently delete a skill from one scope. Only when the user explicitly asks. Deleting a personal copy of a skill reveals the shared one again. Defaults to the personal scope (never the project's). To delete one file of a skill use delete_doc with its skill:// path.`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":  map[string]any{"type": "string"},
				"scope": scopeProp,
			},
			"required": []string{"name"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			name := strings.TrimSpace(argString(args, "name"))
			scope := argString(args, "scope")
			label, msg := skillScope(rc, scope)
			if msg != "" {
				return msg, nil
			}
			db, _, err := a.Skills.ScopeDBFor(wireScope(label), rc.userDB, rc.projectDB)
			if err != nil {
				return "error: " + err.Error(), nil
			}
			if !a.Skills.Has(ctx, db, name) {
				return fmt.Sprintf("error: no skill %q in the %s scope.", name, label), nil
			}
			if err := a.Skills.Delete(ctx, db, name); err != nil {
				return "", err
			}
			a.Audit.Log(ctx, rc.userDB, "skill_delete", name+" ("+label+")", map[string]any{"via": "assistant"})
			return fmt.Sprintf("Deleted skill %q from the %s scope.", name, label), nil
		},
	}
}

// wireScope maps the display label back to the stored scope value.
func wireScope(label string) string {
	if label == "personal" {
		return skills.ScopeUser
	}
	return label
}
