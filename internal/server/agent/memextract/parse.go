package memextract

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/ivoras/harlequin/internal/server/llm/judge"
)

// MaxDocumentMemories caps what one document may add to memory: its text stays
// searchable, so memory only needs the few facts that outlive it.
const MaxDocumentMemories = 5

// notWorthRemembering is shared by both prompts: the things a model is most
// tempted to store and that make memory worse (they duplicate searchable
// documents, go stale, or expose people).
const notWorthRemembering = `Never record:
- amounts, balances, totals, quantities, prices, rates, line items, or any figure that belongs to one invoice, statement, payslip, report or period;
- facts that are true only for one document, one transaction or one month;
- anything about a private individual beyond their name and their role: no personal identification number, no home address, no health, sick leave, salary or family details (employees, a client's staff, any natural person);
- general knowledge any assistant already has (definitions, geography, how a standard works);
- instructions, questions, the task at hand, or what the assistant did.`

const factRules = `How to write each memory:
- "content" is ONE self-contained fact in the third person, short, with names and identifiers written exactly as given.
- Call a number what the source calls it (tax number, contract number, customer number). If the source does not say what a number is, leave the number out.
- Write it in the language of the source text.
- Give dates as dates ("31.10.2026"); skip a fact whose date is only relative ("next week").
- One fact per memory. Do not join several facts with "and" or ";".`

// TurnPrompt is the system prompt for extracting memories from one chat turn.
// inProject adds the "project" scope (see scopeRules).
func TurnPrompt(inProject bool) string {
	scopes := `"user"|"shared"`
	if inProject {
		scopes = `"user"|"project"|"shared"`
	}
	return `You decide whether one chat turn contains facts worth remembering in future conversations. Most turns contain none; then answer {"memories":[]}.

Record only what the USER stated, decided or confirmed, and only if it will still matter weeks from now:
- about themselves: preferences, role, how they want work done;
- about their organisation: its identity, structure, standing policies, tools and vendors;
- about an ongoing matter: who the parties are, decisions taken, deadlines agreed, conventions to follow.

What the ASSISTANT said is context, not a source: do not record anything the assistant found in a document, a web page or a tool result, or calculated. Those can be looked up again.

` + notWorthRemembering + `

Respond with JSON only (no markdown, no commentary):
{"memories":[{"content":"...","confidence":N,"scope":` + scopes + `}, ...]}

` + scopeRules(inProject) + `

` + factRules + `
` + judge.PromptRules() + `
- If nothing qualifies, respond exactly: {"memories":[]}
- Do not explain your reasoning. Do not output placeholder or meta text like "no facts found".`
}

// DocumentPrompt is the system prompt for the document→memory bridge. A
// document's text is already searchable, so this is deliberately not a summary
// and not an index of its contents: it asks only for facts about the parties
// that stay true after the document is out of date. Where the memories are
// stored is decided by where the document lives, not by the model, so there is
// no scope field.
var DocumentPrompt = `You read the beginning of a document that has just been added to a searchable document library. Its full text can always be searched and quoted, so do NOT copy its contents into memory. Decide whether it reveals lasting facts about the organisations or the agreement it concerns that are worth knowing without opening it. For a routine document — an invoice, a statement, a payslip or payroll list, a ledger extract, a receipt — there is usually nothing to record, or only who the parties are; then answer {"memories":[]} or one or two facts.

Worth recording (at most ` + strconv.Itoa(MaxDocumentMemories) + ` memories, fewer is better):
- an organisation's identity: full legal name together with its registration or tax number, its registered address, its bank account;
- a standing relationship or agreement: its number, the parties, what it covers, when it started, how long it runs, recurring obligations or deadlines;
- a role: who represents or signs for an organisation — the name and the role, nothing else about the person;
- a rule, definition or policy that the document itself establishes and that will keep applying.

` + notWorthRemembering + `
- what kind of document this is, its number, its date or its layout (the library already catalogues that).

Examples (invented):
- good: "Uzorak d.o.o. has tax number 12345678901 and is registered at Ulica 1, Zagreb."
- good: "Ana Primjer signs for Uzorak d.o.o. as its director."
- bad, a person's number: "Ana Primjer (personal number 98765432109) is the responsible person." Write it without the number: "Ana Primjer is the responsible person for payroll at Uzorak d.o.o."
- bad, a figure of one document: "The invoice totals 1,435.97 EUR."
- bad, a guessed label: "Uzorak d.o.o. has tax number 069601", when the document shows 069601 without calling it a tax number. A number in a reference, a customer code or an account line is not a tax number.

The input may end with a list headed "Already in memory". Those facts are known: do not record them again, in the same or in other words, and do not record a less complete version of one. If the document adds nothing to them, answer {"memories":[]}.

Respond with JSON only (no markdown, no commentary):
{"memories":[{"content":"...","confidence":N}, ...]}

` + factRules + `
` + judge.PromptRules() + `
- If nothing qualifies, respond exactly: {"memories":[]}
- Do not explain your reasoning. Do not output placeholder or meta text like "no facts found".`

type Candidate struct {
	Content    string
	Scope      string // ScopeUser | ScopeShared | ScopeProject (ScopeUser when the model gave none)
	Confidence int
}

type response struct {
	Memories []struct {
		Content    string `json:"content"`
		Scope      string `json:"scope"`
		Confidence int    `json:"confidence"`
	} `json:"memories"`
}

// ParseResponse decodes the LLM extraction JSON and normalizes candidates.
func ParseResponse(text string) ([]Candidate, bool) {
	raw, ok := judge.ParseJSONObject(text)
	if !ok {
		return nil, false
	}
	var resp response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, false
	}
	return filterCandidates(resp.Memories), true
}

func filterCandidates(in []struct {
	Content    string `json:"content"`
	Scope      string `json:"scope"`
	Confidence int    `json:"confidence"`
}) []Candidate {
	out := make([]Candidate, 0, len(in))
	for _, c := range in {
		content := NormalizeFact(c.Content)
		if content == "" {
			continue
		}
		out = append(out, Candidate{
			Content:    content,
			Scope:      NormalizeScope(c.Scope),
			Confidence: judge.Clamp(c.Confidence),
		})
	}
	return out
}

// NormalizeFact trims and rejects meta/placeholder extraction output.
func NormalizeFact(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "-* \t")
	s = strings.TrimSpace(s)
	if s == "" || len(s) < 4 {
		return ""
	}
	if isRejectedFact(s) {
		return ""
	}
	return s
}

func isRejectedFact(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	lower = strings.Trim(lower, "\"'`")
	if lower == "" || lower == "none" || lower == "n/a" || lower == "null" {
		return true
	}
	rejects := []string{
		"no durable facts",
		"no facts",
		"nothing to extract",
		"nothing durable",
		"no memories",
		"no memory",
		"not applicable",
	}
	for _, r := range rejects {
		if strings.Contains(lower, r) {
			return true
		}
	}
	if strings.HasPrefix(lower, "there are no ") || strings.HasPrefix(lower, "there is no ") {
		return true
	}
	return false
}

// ShouldStore returns true when a candidate meets the autonomous judgment threshold.
func ShouldStore(c Candidate) bool {
	return judge.Accept(c.Confidence) && NormalizeFact(c.Content) != ""
}
