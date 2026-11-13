# Scopes

Harlequin keeps every piece of knowledge — memories, documents, skills — in one
of three **scopes**. The scope decides where the data is stored, who can see it,
and who can change it.

| Scope | Who sees it | Stored in | Ids / labels |
|-------|-------------|-----------|--------------|
| **personal** | only you | `data/users/<id>/user.db` | memory `u.N`, document `u.N` (chunks `d.u.N`) |
| **shared** (organisation) | everyone in the organisation | `data/shared.db` | memory `s.N`, document `s.N` (chunks `d.s.N`) |
| **project** | the project's members | `data/projects/<id>/project.db` | memory `p.N`, document `p.N` (chunks `d.p.N`); from outside the project `p<project>.N`, e.g. `p3.17` |

The product always says **personal**. The API still calls this scope `user` for
skills, memories and MCP servers (documents and hats use `personal`); it is the
same scope, and clients show it as "personal".

## What a search covers

Searching never makes you pick a scope. It covers every scope you can see in the
current workspace, ranks the hits together, and labels each hit with the scope
it came from:

| You are… | Memory and document search covers |
|----------|-----------------------------------|
| outside any project | personal + shared |
| in a project | project + shared + personal |

Shared organisation knowledge is therefore always included, whether or not a
project is active. **Other** projects are never searched by default; the
assistant's `memory_search` and `list_documents` tools take `all_projects: true`
to also cover every project you are a member of. Hits from other projects carry
project-qualified ids (`p3.17`) that mean the same thing from any session, and
are read-only from there.

## Where new things go

The defaults are chosen so the common case needs no decision:

| Writing… | Outside a project | In a project |
|----------|-------------------|--------------|
| a memory (`memory_write`, automatic extraction) | personal | project |
| a memory derived from an uploaded document | where the document went | where the document went |
| a document (upload, `save_doc`) | personal | project |

You can override the default when you need to: owners and admins may write
### Automatic memories

The assistant also remembers things without being asked, in two ways:

- **From a conversation.** After a turn it may keep a lasting fact the *user*
  stated or decided (a preference, something about the organisation, a decision
  or deadline). In a project the project's own matters go to project memory and
  a personal preference still goes to your own memory. What the assistant
  merely looked up in a document or on the web is not remembered.
- **From an uploaded document.** A document's text is searchable as it is, so
  memory keeps only a few lasting facts about the parties it concerns (who an
  organisation is, a standing contract, who signs for whom) — not amounts,
  balances or anything else that belongs to one invoice or period, and nothing
  private about named individuals. Those memories are stored **in the
  document's own scope**: a client's contract uploaded to a project never
  becomes organisation-wide memory. Each one links back to its document (shown
  in the Memory view and by `/memory`), and stays, marked as such, if the
  document is later deleted.

**shared** memories and documents (for ordinary users a shared memory write is
stored as personal instead), and any member may write to their project. In the
web UI's Files page you pick an upload's destination explicitly.

A single attribute (a memory *slot* such as `user.preferred_currency`) may live
in either personal or shared memory, but not in both: a write that would
duplicate an attribute from the other scope is refused, so the two can't
silently disagree.

## Skills and hats

Skills are resolved by name across the same scopes, deepest first:
**project → shared → personal**. A skill in the active project wins over an
organisation skill of the same name, which wins over your own.

A **hat** sits on top of all of that. Hats come in two scopes:

- **Shared** hats are organisation-wide (stored in `shared.db`) and edited by
  owners/admins.
- **Personal** hats are your own (stored in your `user.db`), created and edited
  by you; nobody else sees them. `/hat create` makes a personal hat unless you
  add `--shared` (owners/admins).

If a shared and a personal hat have the same name, the **shared one wins**. A
personal hat can't be created with a name a shared hat already has.

You wear one hat per session. While a hat is worn, its own copies of skills win
outright over every scope, its optional skill list restricts which skills are
visible, and its prompt is added to the default system prompt (or replaces it,
with `replace_prompt: true`). Hats and projects are independent — with one
exception: a **project session is shared**, so only shared hats can be worn
there (other members' turns can't read your personal ones). A personal hat on a
session is dropped when you assign the session to a project.

## Projects

A project is a shared workspace with its own database: members, sessions,
documents, memories, skills and a live team chat. Assigning a session to the
active project moves it (with its messages and its cost history) into the
project, where every member sees it and joins the same live session. A session
belongs to at most one project.

## MCP servers

External MCP tool servers are registered either for the **shared** scope
(owners/admins; available to everyone) or the **personal** scope (just you).
Credentials for OAuth servers are always per user, even for shared servers —
each user signs in with their own account.

## Roles

What you may write per scope depends on your role (everything each role can
do is in [USERS.md](USERS.md)):

| Role | Can additionally |
|------|------------------|
| `owner` | everything an admin can, plus create and edit users |
| `admin` | create/delete shared memories, add shared documents, delete documents, publish skills, manage hats and shared MCP servers |
| `user` | personal data, and project data in projects they belong to |
