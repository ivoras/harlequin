package memextract

import "strings"

// ScopeRules guides when auto-extracted facts should be org-wide (shared) vs
// personal (user). The same rules are mirrored in skills/system_prompt.md for
// in-turn memory_write decisions.
const ScopeRules = `Scope for each memory (use "user" or "shared"; default "user" when omitted):

Prefer "shared" for durable facts any colleague (or future turn) should treat the
same way — not tied to one person's private life:
- Legal or trading name, brand, tagline, primary domain or public website
- HQ, office locations, or geography when stated as org fact (not "user lives in …")
- Org-wide technical standards, platforms, or vendors ("we use …", "our stack is …")
- Products, services, codebases, or customers named as the organisation's own
- Published policies, compliance regimes, or org structure that is not personal taste
- Generic factual statements about the world as it exists outside the user's
  personal concerns — public knowledge, definitions, standards, geography, science,
  or other objective facts worth remembering that are not about this individual
  (e.g. "HTTP status 404 means not found", "Paris is the capital of France")

Prefer "user" for facts about this individual only:
- Personal preferences, habits, communication style, tools they personally prefer
- Private, sensitive, or contact information; health, family, compensation
- Their individual role or title when it is about them, not a directory of the org
- Facts phrased about "the user" / "I" / "my" when they describe the person, not
  the whole company (e.g. "User prefers dark mode" → user; "The company name is X" → shared)

When the user states an org fact plainly ("The company name is …", "We are …",
"Our product …") without asking to keep it private, use scope "shared".
If a fact could reasonably be either scope, prefer "user" unless it clearly
describes the organisation for everyone.`

// NormalizeScope returns "shared" or "user".
func NormalizeScope(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "shared":
		return "shared"
	default:
		return "user"
	}
}
