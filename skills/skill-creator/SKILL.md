---
name: skill-creator
description: Create a new skill or improve an existing one (its instructions, files, tools or description). load_skill this whenever the user asks to make, write, change or fix a skill, or says "turn this into a skill", before calling write_skill.
---

# Creating and improving skills

A skill is a set of instructions another agent turn will follow later, with no
memory of this conversation. Write for that reader: a capable model that knows
the tools but not the user, the task or the decisions made here.

Find where the user is and start from there: an idea ("I want a skill for X"),
a conversation to capture ("turn this into a skill"), or an existing skill that
misbehaves. Users range from technical to not at all: say "instructions" and
"test", not "frontmatter" and "assertion", unless they use those words first.

## 1. Capture what the skill is for

If the conversation already shows the workflow, take it from there first: the
tools used, the order of steps, the user's corrections, the input and output
formats. Then fill the gaps with the user:

1. What should the skill let the agent do?
2. When should it be used: what would a user say or have in hand?
3. What should come out: a reply, a document (PDF, Word, Excel, HTML), a saved
   memory, an email, a scheduled job?
4. What inputs vary from run to run, and which are fixed?
5. Edge cases: missing data, the wrong kind of file, ambiguous requests.

Ask the questions together in one `ask_user` (it ends your turn), not one at a
time. Do not ask what you can find out yourself: `list_skills` for similar or
clashing skills, `search_docs`/`memory_search` for the user's material.

## 2. How skills work here

- **Files.** A skill is `SKILL.md` plus optional files at
  `skill://<name>/<path>` (e.g. `skill://<name>/templates/letter.md`,
  `skill://<name>/lib/check.js`). `load_skill` returns SKILL.md **and every
  other non-script file in full**; `.js` files are only listed. So there is no
  "read this reference only when needed": everything that is not a script costs
  context on every load. Keep the total short, and split a large rarely-needed
  part into its own skill.
- **Frontmatter.** Only `name`, `description` and `tools` are read. `name` is
  lowercase-with-hyphens and must match the skill's name; it becomes the
  `skill://` host and the prefix of its tools.
- **The description is the trigger.** Every visible skill's name and
  description are in the system prompt of every turn, for everyone in that
  scope; the agent loads a skill only when its description matches the
  request. Write one or two sentences: what it does, then when to use it, with
  the words users actually say. Make it a little pushy ("load_skill this
  whenever …"): agents tend to under-use skills. Keep it short: it is paid for
  on every turn.
- **Scopes.** `write_skill` saves to `personal` by default. `shared` (whole
  organisation) is for owners/admins; `project` needs a project session and is
  seen by every project member. When the same name exists in several scopes,
  **project wins over shared, shared over personal**, so a personal copy of a
  shared skill is invisible: pick a new name instead (or, for a role-specific
  variant, a hat overlay). Ask the user which scope only when it is not obvious;
  personal is the safe default.
- **Templates.** `<?js … ?>` blocks in `.md`/`.txt` files run when the skill is
  loaded and are replaced by what they `print()`. Available: `ctx.user`,
  `ctx.skill`, `ctx.date` (YYYY-MM-DD), `ctx.now()`, `ctx.memorySearch(q)`,
  `ctx.searchDocs(q)`, `ctx.memoryGlob("pattern.*")`. Use them only for what
  must be fresh at load time (today's date, the user's standing preferences);
  a template error makes the skill fail to load.

## 3. Write SKILL.md

- Imperative steps in the order the agent will do them, naming the real tools
  (`search_docs`, `read_doc`, `query_sheet`, `create_pdf`, `save_doc`,
  `send_email`, `cron_create`, `ask_user`, `WebFetch`, …).
- Explain **why** a step matters rather than shouting ALWAYS/NEVER: the reader
  is smart and handles cases you did not foresee once it knows the reason. If
  you catch yourself writing capitals, write the reason instead.
- Give an exact template when the output format matters (headings, columns,
  file title), and one short example of input → output.
- Say when to stop and `ask_user` (missing input, a choice only the user can
  make) and when to proceed on a default.
- Say where the result goes: a reply, a saved document (which scope), a
  `tmp://` file for download.
- Text the skill produces for people is in their language; the instructions
  themselves can be in English.
- Stay general. A skill tuned to the one example in front of you fails on the
  next request; describe the pattern, use the example to illustrate it.
- Nothing surprising: a skill must do what its description says, and nothing
  that would alarm the user if described to them.

Then create it with `write_skill(name, content, scope)`, and add any extra
files with `write_file` at `skill://<name>/<path>`.

## 4. Tools (only when they pay off)

A skill can declare its own tools, which the agent calls as `<skill>__<tool>`.
Add one when a step is deterministic and repeated (a calculation, a fixed
fetch-and-parse, a format conversion); otherwise instructions plus existing
tools are simpler to change.

```yaml
tools:
  - name: vat_breakdown
    description: Split a gross amount into net and VAT at the given rate.
    parameters:
      type: object
      properties:
        gross: { type: number, description: Gross amount in EUR }
        rate:  { type: number, description: VAT rate in percent, e.g. 25 }
      required: [gross, rate]
    run: |
      var net = args.gross / (1 + args.rate / 100);
      return { net: Math.round(net * 100) / 100,
               vat: Math.round((args.gross - net) * 100) / 100 };
```

- `run` is the body of a function in the same sandbox as `run_js` (goja,
  ES5.1 + much of ES6): the call's arguments are the global `args`; printed
  output plus the JSON of the returned value is the result. The `run_js`
  helpers are there too (`fetch`, `dom`, `tmp`, `storage`, `read_doc`,
  `sheets.query`, `xml.query`). No other file or network access.
- Put longer code in `skill://<name>/lib/<file>.js` and start `run` with
  `include("skill://<name>/lib/<file>.js");`.
- Test the code with `run_js` (same `args` object) **before** saving it.
- The tool list is built at the start of each turn: a new or changed tool is
  callable from the user's next message, not in this turn.

## 5. Test it

You cannot run a fresh agent yourself, so:

1. `load_skill("<name>")` right after saving: it must load (template errors
   show up here) and read sensibly to someone who has not seen this chat.
2. Run each declared tool's code through `run_js` with realistic `args`.
3. Propose 2–3 realistic test requests, the way a user would actually phrase
   them (with names, files, numbers), plus one near-miss that should **not**
   use the skill. Ask the user to try them in a **new** session (this one
   already knows everything, so it proves nothing) and tell you what went
   wrong.
4. If a dry run is cheap and harmless (no email sent, no job scheduled, no
   shared document saved), you may follow the skill once yourself on a test
   request, keeping in mind that you know more than the real reader will.

## 6. Improve it

- Read the current text first: `read_doc("skill://<name>/SKILL.md")` shows the
  source (`load_skill` shows templates rendered). Change it with `edit_doc` on
  that path, keeping any `<?js ?>` blocks; `write_skill` refuses an existing
  name.
- Turn each complaint into the general rule behind it, not a patch for that
  one case. If the agent keeps doing something unproductive, look for the
  sentence that invites it and cut it before adding more.
- If the skill often skips or misreads a step, explain why the step matters,
  move it earlier, or give it an example.
- If the skill is not being used when it should be (or is used when it
  should not), the description is the thing to fix.
- Keep it lean: remove whatever is not changing the agent's behaviour.
- A shared skill that ships with the server can be edited by owners/admins;
  their edit is kept, but the edited file then stops receiving the server's
  updates.
  Tell the user that before editing one.

When done, tell the user the skill's name and scope, how to start it (a phrase
that triggers it), and the test requests to try.
