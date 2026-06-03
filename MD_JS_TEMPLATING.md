# Markdown JS templating

Every Markdown (and `.txt`) file the Harlequin **server** renders is run through a
small JavaScript template engine before use. This applies uniformly to:

- skills (`SKILL.md` and other `.md`/`.txt` files in a skill),
- the default **system prompt** (`skills/system_prompt.md`, deployed to
  `data/skills/system_prompt.md`),
- **hat** system prompts (`hats/<name>/system_prompt.md`).

All of them are rendered with the **same context** per-user.

## Syntax

Anything between `<?js` and `?>` is executed as JavaScript (ES5, sandboxed). Text
outside the tags is emitted verbatim. Inside a block, whatever you pass to
`print(...)` / `println(...)` is spliced into the output at that position:

```md
Hello <?js print(ctx.user); ?>, welcome.
Today is <?js print(ctx.date); ?>.
```

Rendering **fails closed**: a JavaScript error or timeout aborts the whole render.

## Context (`ctx`)

| Expression            | Type     | Value                                                        |
|-----------------------|----------|--------------------------------------------------------------|
| `ctx.user`            | string   | The logged-in username.                                      |
| `ctx.date`            | string   | The current date, `YYYY-MM-DD`.                              |
| `ctx.now()`           | string   | The current timestamp, RFC 3339.                             |
| `ctx.skill`           | string   | The skill being rendered (empty for the system/hat prompt).  |
| `ctx.memorySearch(q)` | string[] | Top matches from the user's + shared memory for query `q`.   |
| `ctx.searchDocs(q)`   | string[] | Top matches from the organisation document corpus.           |
| `ctx.memoryGlob(g)`   | object[] | Memories whose slot key matches GLOB `g` (e.g. `"user.*"`); each item has `id`, `key`, `value`, `content`. |

The context is defined in one place; to add a variable, extend
`internal/server/mdtmpl` and the `jstmpl` shim — every templated `.md` then sees it.

## Examples

Print the date (used in the default system prompt — see below):

```md
Today's date is <?js print(ctx.date); ?>.
```

Loop and inject retrieved facts:

```md
Relevant notes:
<?js
  var hits = ctx.memorySearch("project deadline");
  for (var i = 0; i < hits.length; i++) { println("- " + hits[i]); }
?>
```

Greet the current user:

```md
Hi <?js print(ctx.user); ?> 👋
```

## The default system prompt

`skills/system_prompt.md` (built into the binary, deployed to
`data/skills/system_prompt.md`) includes this templated line so every
conversation knows the date:

```md
Today's date is <?js print(ctx.date); ?>.
```

Because only the source is cached (by mtime) and the template is re-evaluated on
every turn, the printed date is always current. Editing the deployed file is
preserved across upgrades (see the hash-manifest sync in
`internal/server/skills/deploy.go`); reverting it lets the baked-in version take
over again.
