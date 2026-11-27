# How to reduce test slop

Test slop is test code that costs review, run time and agent context but cannot fail when a rule breaks.
This document lists the patterns that produce it, how to recognise them in Harlequin's Go tests, and how to
cut them without losing any rule. Read it before writing or changing tests (`AGENTS.md`, Conventions).

## What gets a test

- **Extend before adding.** When a change alters behaviour an existing test pins, change that test, and delete
  the tests the change made stale. A new test is for a rule nothing pins yet.
- **One test per rule, not per input.** Inputs that exercise the same rule are rows of one table
  ([Writing tables](#writing-tables)).
- **Keep the test diff smaller than the code diff.** A change whose added test lines exceed its added
  non-test lines needs a reason (a merge-only domain, below, usually is one).
- **No test for what the code declares**: struct tags, constants, enum values, tool schemas, route wiring,
  migration SQL. Test the behaviour they enable, such as a refused request, when a rule depends on them.

## Antipatterns

| Antipattern | What it produces | Fix |
|---|---|---|
| A new test for every change | A new `TestX` per change, beside the old ones the change made stale | Extend the test that already pins the behaviour; delete the stale ones |
| One test per input | Dozens of near-identical functions that differ only in literals | One table test per rule, one `t.Run` per row |
| Testing the helper just written | Tests of unexported helpers that break on every refactor | Test through the entry point callers use ([4.](#4-go-through-the-entry-point)) |
| Pinning declarations | Tests that a tool's schema has a property, a constant has a value, a route exists | No test; test the behaviour the declaration enables |
| Comparing output sentences | Tests that fail on a wording edit and pass when the rule behind it breaks | Assert the id, scope, stored row, amount or `error:` outcome ([5.](#5-assert-outcomes-ids-and-data-not-wording)) |
| Faking a project component and asserting it was called | Tests that pin call structure, not outcome | Fake only external boundaries; assert what changed |
| Line coverage as the goal | Many tests running the same lines with inputs that exercise no new rule | Measure which tests add nothing, then consolidate under a coverage guard ([1.](#1-measure-before-deleting), [2.](#2-consolidate-one-package-at-a-time-under-a-coverage-guard)) |

## Recognising it

The mechanical shapes, for a reviewer (or an agent re-reading its own diff):

- the test calls an unexported function when an exported one, a tool handler or an HTTP route reaches it;
- the test asserts on a declaration (a schema map, a constant, a struct tag);
- the test body is empty or only logs;
- an assertion compares a whole sentence;
- the only assertions are on a fake's recorded calls;
- three or more test functions in a file are identical up to literals.

The worse kind of slop looks right and asserts nothing real. Patterns to watch for in this codebase:

| Vacuous pattern | Why it passes regardless | Fix |
|---|---|---|
| **Tool error read as success** | Tool handlers return failures as an `"error: …"` result string with a nil `error`, so `if err != nil { t.Fatal }` passes on a refused call | Check the result does not start with `error:`, then assert the stored state |
| Swapped function never used | The test replaces a package-level `var` (clock, fetcher), but the code copied it into a struct at construction, so the replacement is never called | Inject through the constructor or field the code reads, or drive the case with real data |
| Fixture short-circuits the rule | Text shorter than one chunk, a judgment below `judge.MinConfidence` that is dropped anyway, an empty corpus before a ranking rule | Build the fixture so only the rule under test decides the outcome |
| Asynchronous effect never awaited | The sessionhub goroutine, the `docevents` bus, the notification poller or post-turn memory extraction has not run yet, so "nothing was published" holds whether or not the code would publish | Wait for the event on a channel with a timeout, or call the synchronous core directly |
| Status-only HTTP assertion | The failure path also returns 200 (`httptest`), or an error body still decodes | Assert the payload: the right record id, with a decoy present |
| Another scope answers | Search fuses personal, shared and project scopes, so a project-scope test passes on a hit from the shared corpus; skill resolution falls through to a shallower copy | Put a decoy in the other scopes and assert the hit's `Scope` / id prefix (`u.`, `s.`, `p.`) |
| Privilege that proves nothing | With `admins_see_all_projects` (default true) an owner/admin is a member of every project, so a membership test run as admin passes | Run access tests as a plain `user`, outside the project, and as admin only when the bypass is the rule |
| Stale handle or object | `user.db` is opened and closed per request; a test that kept a `*sql.DB` or a struct read before the change checks old data | Re-open through `storage.WithUser`/`WithProject`, re-read after the call |
| State absorbs the change | Rows left by an earlier row of the table, or a cache (WebFetch's 15-minute cache, sheet cache), swallow the extra work a broken filter would cause | Fresh `t.TempDir()` data per row, or reset the cache |
| Fake model that agrees with everything | A stub LLM returns the same reply whatever the prompt, so a test of routing, fallback or a prompt rule passes either way | Make the stub's reply depend on what it received, or assert on the request it got |

## Cutting it without losing rules

### 1. Measure before deleting

Per-test coverage shows which tests reach nothing another test does not already reach. From the repo root, for
one package:

```bash
export CGO_ENABLED=1 CGO_CFLAGS=-I$(pwd)/third_party/sqlite/include
pkg=./internal/server/memory; out=$(mktemp -d)
for t in $(go test -tags sqlite_fts5 -list . $pkg | grep '^Test'); do
  go test -tags sqlite_fts5 -run "^$t\$" -coverprofile=$out/$t.cov $pkg >/dev/null
  awk 'NR>1 && $3>0 {print $1}' $out/$t.cov | sort -u > $out/$t.blocks
done
# Test A adds nothing beyond B when this prints nothing:
comm -23 $out/TestA.blocks $out/TestB.blocks
```

Add `-coverpkg=./internal/...` when the package's tests reach code in other packages. A test whose blocks are
a subset of a larger test's is a consolidation candidate; identical block sets are duplicates in all but inputs.
A candidate is not a verdict: two tests can run the same lines with inputs that pin different rules
(two SSRF-guard cases, two scope precedences).

### 2. Consolidate one package at a time, under a coverage guard

Record the blocks the package's tests cover, rewrite the tests, record again, and require that nothing was
lost:

```bash
go test -tags sqlite_fts5 -coverprofile=before.cov $pkg   # before the rewrite
go test -tags sqlite_fts5 -coverprofile=after.cov  $pkg   # after
comm -23 <(awk 'NR>1&&$3>0{print $1}' before.cov|sort -u) <(awk 'NR>1&&$3>0{print $1}' after.cov|sort -u)
```

Blocks are keyed by source line, so do not edit the code under test in the same change, or a moved line reads
as lost coverage. Coverage proves reach, not judgement: also check that each rule the old tests asserted is
still asserted.

### 3. Merge-only domains

In these areas every case asserted today stays asserted, as a row of a table. Nothing is dropped because it
"looks covered":

- usage and cost: recorded `usage` rows, prices, `org_llm_markup_percent` (`Server.markup`), attribution;
- auth, roles (`owner`/`admin`/`user`), project membership, scope rules (who may write shared, personal hats in
  projects, which copy of a skill or hat wins);
- sandbox and trust boundaries: `jsrun`, `sandboxfs` paths, file references (`agent/refs.go`), the WebFetch
  SSRF guard, `/files/tmp/*`, HTML sanitising in the `create_*` pipeline;
- SQL over spreadsheets (`query_sheet`, `sheets.query`) and other untrusted-input handling;
- parsers of user or fetched input: cron specs, skill/hat frontmatter, XML paths, tool arguments;
- edits that can lose data: `edit_doc`/`commitDocEdit`, `.prev` undo, sidecars, `docxedit`/`xlsxedit`;
- autonomous judgments: the `confidence >= 7` gate (`llm/judge`).

Elsewhere, delete what pins implementation, restates a declaration, duplicates a test, or checks only wording.

### 4. Go through the entry point

A test reaches code the way callers do: the REST route (`httptest` against the router), the tool handler, the
exported function of the package, the CLI command. An unexported helper is covered by the entry point that uses
it. Fake only external boundaries: the LLM provider, the embedder, HTTP fetches, SMTP, the Telegram API,
headless Chrome/LibreOffice, Docling.

Test an unexported function directly only when no entry point reaches it, and say why in a comment on the test.
That usually means the branch is dead and the code should lose it. Pure helpers with many edge cases (a
parser, a matcher) are the exception: there the helper is the entry point.

### 5. Assert outcomes, ids and data, not wording

Tool results are text for the model, so a test is tempted to compare them. Assert what the text reports
instead: the document id, the scope, the stored row, the amount, the file written, whether it starts with
`error:`. Two text checks stay legitimate:

- asserting that forbidden content is absent (another user's data, the cost markup, `reasoning_content` in a
  client event, a secret);
- asserting that data the test supplied reaches the output (a name, an amount, an id).

## Writing tables

A table test is one function, one slice of cases, one `t.Run` per row:

```go
func TestWriteSharedMemoryRefused(t *testing.T) {
	cases := []struct {
		name, role, scope string
		wantErr           bool
	}{
		{"user to shared", "user", "shared", true},
		{"admin to shared", "admin", "shared", false},
		{"user to personal", "user", "user", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, c.role) // fresh databases per row
			res := h.call("memory_write", map[string]any{"content": "x", "scope": c.scope})
			if got := strings.HasPrefix(res, "error:"); got != c.wantErr {
				t.Fatalf("error = %v, want %v: %s", got, c.wantErr, res)
			}
			if n := h.countMemories(c.scope); (n == 1) == c.wantErr {
				t.Fatalf("stored %d memories", n)
			}
		})
	}
}
```

- **Name every row.** The `t.Run` name is the only thing that tells a reader which case failed.
- **Isolate rows that write.** Rows sharing a database leak into each other; build the fixture inside the row
  (`t.TempDir()`, a fresh store). Where a row must run after another, say so in a comment next to it.
- **Compare the whole result where it is small.** One comparison of a whole struct or map (`reflect.DeepEqual`,
  with a diff in the failure message) catches more than several field checks and replaces shape-only tests.
- **Keep sentences out of test data.** Short tokens (`"refused"`, `"owner-edit"`) and neutral placeholders
  ("Acme d.o.o.", "Ana Horvat"), never client data (`AGENTS.md`).
- `gofmt` keeps rows on one line each, so a merged table is short; in merge-only domains the test count falls
  far more than the line count, which is expected.

## Keeping it from coming back

| Mechanism | Where |
|---|---|
| The rules above, read before writing tests | `AGENTS.md`, Conventions |
| Reviewers flag surplus and vacuous tests, not only missing ones | code review of every change |
