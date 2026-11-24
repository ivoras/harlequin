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

func (a *Agent) registerSkillWriteTools(reg map[string]toolEntry) {
	scopeProp := map[string]any{"type": "string", "enum": []string{"personal", "project", "shared"},
		"description": `Where the skill lives. Default "personal"; "shared" is owner/admin only; "project" needs a project session.`}

	put := func(ctx context.Context, rc *runContext, tool, name, rel, content, scope string) (string, error) {
		name = strings.TrimSpace(name)
		if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
			return "error: name must be a plain skill name (no slashes).", nil
		}
		label, msg := skillScope(rc, scope)
		if msg != "" {
			return msg, nil
		}
		// load_skill/read_doc show .md/.txt files rendered, so a rewrite made from
		// that view would silently drop the file's <?js ?> template code.
		if old, _, err := a.Skills.GetFile(ctx, rc.userDB, rc.projectDB, name, rel); err == nil && strings.Contains(old, "<?js") && !strings.Contains(content, "<?js") {
			return fmt.Sprintf("error: %s of skill %q contains <?js ?> template code that load_skill does not show you, and your version would delete it. Do not rewrite this file; tell the user to change it in the Skills editor instead.", rel, name), nil
		}
		db, _, err := a.Skills.ScopeDBFor(wireScope(label), rc.userDB, rc.projectDB)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		if err := a.Skills.PutFile(ctx, db, rc.userDB, rc.projectDB, name, rel, content, rc.userID); err != nil {
			return "error: " + err.Error(), nil
		}
		a.Audit.Log(ctx, rc.userDB, tool, name+"/"+rel+" ("+label+")", map[string]any{"via": "assistant"})
		return fmt.Sprintf("Saved %s of skill %q (%s scope). It applies from the next message.", rel, name, label), nil
	}

	reg["write_skill"] = toolEntry{
		def: fnTool("write_skill", `Create a skill, or update an existing one by replacing its main file (SKILL.md: frontmatter with name and description, then the instructions). Other files of an existing skill are kept. To update, load_skill it first and send the complete revised SKILL.md: anything you leave out is lost. Only when the user asks to create or change a skill. A skill you write in a scope that doesn't hold it yet becomes a copy of the one you were using, so the original is untouched. Never overwrite a shared skill unless the user explicitly asked for that.`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string", "description": "The full SKILL.md, starting with --- frontmatter (name, description)."},
				"scope":   scopeProp,
			},
			"required": []string{"name", "content"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			return put(ctx, rc, "skill_save", argString(args, "name"), "SKILL.md", argString(args, "content"), argString(args, "scope"))
		},
	}

	reg["write_skill_file"] = toolEntry{
		def: fnTool("write_skill_file", `Add, or update by replacing, one supporting file of an existing skill (a script, template or reference), e.g. path "scripts/run.js" — it is then reachable as skill://<name>/scripts/run.js. To update, read_doc that skill:// path first and send the complete revised file. The skill must already exist (create it with write_skill first). Only when the user asks.`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":    map[string]any{"type": "string"},
				"path":    map[string]any{"type": "string", "description": "Relative path inside the skill, no leading slash or .. segments."},
				"content": map[string]any{"type": "string"},
				"scope":   scopeProp,
			},
			"required": []string{"name", "path", "content"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			return put(ctx, rc, "skill_file_save", argString(args, "name"), argString(args, "path"), argString(args, "content"), argString(args, "scope"))
		},
	}

	reg["delete_skill"] = toolEntry{
		def: fnTool("delete_skill", `Permanently delete a skill from one scope. Only when the user explicitly asks. Deleting a personal copy of a skill reveals the shared one again. Defaults to the personal scope (never the project's).`, map[string]any{
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
