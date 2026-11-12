package memextract

import "strings"

// Memory scopes a candidate can be filed under.
const (
	ScopeUser    = "user"
	ScopeShared  = "shared"
	ScopeProject = "project"
)

// scopeRules tells the model how to file a fact from a chat turn. The same
// distinctions are mirrored in skills/system_prompt.md for in-turn memory_write.
// Outside a project the choices are user | shared; in a project session
// "project" is added and is where anything about the project's own subject
// goes (it is visible to the project's members, not to the whole organisation).
func scopeRules(inProject bool) string {
	s := `Scope of each memory:
- "user": about this person only — their preferences, habits, role, how they want things done, private or contact details.
- "shared": about the user's own organisation as a whole, the same for every colleague — its name, identity, offices, structure, standing policies, the tools and vendors it uses.`
	if inProject {
		s += `
- "project": about the subject of this project — its purpose, the client or other parties it concerns, decisions, deadlines, agreed conventions. Facts about a client, a counterparty or a case are "project", never "shared".
When unsure between "project" and "shared", use "project". When unsure whether a fact is personal, use "user".`
	} else {
		s += `
When unsure, use "user": a fact filed as "shared" is shown to everyone in the organisation.`
	}
	return s
}

// NormalizeScope returns one of the three scopes, defaulting to "user".
func NormalizeScope(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ScopeShared:
		return ScopeShared
	case ScopeProject:
		return ScopeProject
	default:
		return ScopeUser
	}
}
