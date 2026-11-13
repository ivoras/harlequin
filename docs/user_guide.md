# Harlequin User Guide

Harlequin is a client-server AI agent: a server that talks to LLMs, remembers
things for you (memories and documents), runs skills and scheduled jobs, and
streams chat sessions over WebSockets — plus the clients you talk to it
through: a terminal client (the TUI) and a browser app. Sessions live on the
server, so you can disconnect mid-answer and pick up where you left off, from
the same client or a different one. Harlequin is multi-user: an organisation
shares one server, with shared memories, documents, skills, and projects on
top of each user's private data.

This guide covers day-to-day usage. For installation and configuration, see
the [README](../README.md); for architecture, see [AGENTS.md](../AGENTS.md).

## Working with documents and memory

In the browser, the Memory view lets you edit a memory's content and (for
`user`/`shared` scope) its slots inline, alongside delete; project memories
show a content-only editor since they don't support slots. The Documents view
does the same for uploads: upload or paste text, with a scope picker (personal
by default; owners/admins can ingest into the shared, org-wide corpus).
Document listings show each document's type (PDF, DOCX, MD, TXT) and its
reference id (`u.2`, `s.5`, `p3.19`) alongside its scope, in both the
Documents view and a project's Documents tab in the Projects view. Each
uploaded document also gets a one-line description, written automatically in
your organisation's language and shown under its name; it helps the agent find
"the new supplier contract" without you giving an id.

**Memories from documents and chat.** The agent keeps lasting facts on its own:
what you state or decide in a chat, and a handful of facts (who the parties
are, standing agreements, roles) from each uploaded document. A fact taken from
a document is stored in that document's scope — personal, shared, or its
project — and shows a link to the document in the Memory view and `/memory`.

### Reports and citations

When you ask the agent to produce a report or comparison worth keeping, it
saves it as a document (`saved as p.21`) — searchable later like anything else.
By default a report goes to the project corpus in a project session, otherwise
to your personal documents; owners/admins can ask for it to be saved as shared.

Answers and saved reports cite their sources inline (`[d.p.421]`). In the
browser these citations are clickable: hovering shows the source document,
clicking opens it — PDFs in a new tab at the cited page, text and markdown
documents in a rendered side panel with buttons to view the raw file or
download the original. Citations inside a rendered report are clickable too,
so you can follow a report's sources without leaving it.

### Excel files

Ask for a spreadsheet ("make an Excel file of these prices", "export this table
to Excel") and the agent creates a downloadable `.xlsx` in the same place a
report would go — one sheet per section, a bold header row, real numbers, and
working formulas such as totals. Only the data goes into a workbook the agent
makes — not colours, merged cells, charts or column formats. To change a
spreadsheet you uploaded, see "Changing a document" below: it is edited in place
and keeps its formatting; ask for a *new* workbook if you want the original left
unchanged.

### PDF and Word files

Ask for "a PDF of this report" or "a Word version of that letter" and the agent
typesets a new `.pdf` or `.docx`, saved where a report would go and
downloadable from the file list. It can also cut, split and merge the pages of
PDFs you already have ("pages 3–5 of the contract as a separate PDF", "merge
these two into one") and e-mail documents as attachments. These depend on the
server's setup: if PDF or Word creation or e-mail isn't configured, the agent
says so.

### Changing a document

Ask the agent to change a document that already exists and it edits it **in
place**, keeping the same document: "in the contract, change the notice period
from 30 to 60 days", "add a paragraph after the one about payment terms", "add a
row for the new employee", "set Ana's gross in the October sheet to 1700". This
works on notes and Markdown documents, on Word (`.docx`) and Excel (`.xlsx`) files
you uploaded, and on PDF, Word and Excel files the agent made for you earlier.

- **Uploaded Word and Excel files keep their formatting**: fonts, styles, headers
  and footers, numbering, images, number formats and formulas are untouched; only
  the change is made. Word files with tracked changes, and Excel files with
  macros or protected by a password, are refused — the agent tells you why.
- **Files the agent made** are rebuilt from the edited text (so their formatting is
  the agent's standard layout).
- **Uploaded PDFs cannot be edited.** The agent can make a new document from the
  PDF's text, or cut pages out of it.
- **The last change can be undone**: say "undo that". Only one step back is kept;
  the next edit replaces it.

## The web app

The browser app comes in two layouts over the same account and data:

- **Desktop** (`/`) — for keyboard-and-mouse use.
- **Mobile** (`/m/`) — touch-first: a tab bar, a ☰ sessions drawer, bottom sheets.

Phones are sent to the mobile layout automatically. To switch by hand, use
**Mobile version** in the desktop's account menu (bottom of the sidebar) or
**🖥 Desktop version** in the mobile ⋮ menu; the choice is remembered in that
browser. The address bar keeps the session and project (`?s=…&p=…`), so a
copied link opens the same session in either layout.

**Empty sessions.** Starting a new session (any client) reuses your most recent
empty, unnamed "New session" instead of adding another blank one, and empty
unnamed sessions untouched for 7 days are deleted automatically (checked hourly).

**Desktop layout.** The left sidebar holds, top to bottom: **New session**,
**Search** (the command palette), the **workspace switcher** (Personal or one
of your projects; it also lists invitations and creates projects), the pages
(Chat, Files, Memory, Skills, Scheduled jobs, Projects), the current
workspace's **files** (the project's files in a project, otherwise your
personal ones; click to open), and its **sessions**, grouped by last activity
(hover a personal session to rename or delete it). Your account menu at the
bottom has Settings, Hats, the theme (System / Light / Dark), the mobile
switch, and Log out.

The chat header shows the workspace, the session title (click to rename a
personal session), and the **hat** worn by the session — click it to wear a
different hat or take it off. A new, empty session also shows the available
hats as cards under "What are we working on?". The composer's attach button adds documents to the message you are writing:
each is uploaded and indexed (to the active project, else your personal
files), shown as a chip, and listed at the end of the sent message as
`@"u.18/report.pdf"`; the agent reads them with `read_doc`. Send waits for
uploads to finish. Dropping files on the chat attaches them the same way.
**Images** are shown to the model directly when the chat model supports vision
(`vision:` on the model in `model_suites`; default on for hosted models, off
for `local: true` ones).

The top bar shows a count of unread
team-chat messages across your projects; click it for the list and jump to a
project's chat. After each turn a context meter shows the
tokens used, the context window and the model; click it for the full context
breakdown.

The **side panel** on the right (toggle with the header's panel button; drag
its left edge to resize it) has up to three tabs:

- **Team chat** — the project chatroom, while a project is active.
- **Document** — opens whenever you click a citation, a document reference, or
  a file. Text and reports render there; PDFs and DOCX open in a new browser
  tab.
- **Session** — session details and the context-usage breakdown, including the
  session's LLM cost so far (the TUI's `/context` shows the same). The cost is
  the provider's reported figure where available, otherwise an estimate from
  configured prices, marked ≈. It covers every LLM call made for the session,
  including web-page analysis, automatic memory extraction and titling; a
  project session's cost covers every member's turns.

**Settings** (account menu, or `Ctrl/⌘+,`) has these tabs:

- **General** — account, theme, mobile switch, and for owners/admins a
  broadcast alert.
- **Login** — change your password. You confirm the current one, and are
  signed out everywhere else.
- **Users** (owners only) — add accounts, change roles, reset passwords,
  delete accounts. When the server can send e-mail, a checkbox e-mails a new or
  reset password straight to the user. The server always keeps at least one
  owner, and you can't delete your own account.
- **Hats** — view hats and what they overlay; create your own personal hats
  (owners/admins can also create shared ones) and edit the ones you may edit.
- **MCP servers** — register external tool servers (see **MCP tools** below).
- **All projects** (owners only) — every project on the server with its
  members and files; join one, or add and remove members.
- **Config** — your small per-user settings (the TUI's `/config`).
- **Usage** — LLM cost (completions and document/memory embeddings) as three bar charts: per day for the last 30 days, per
  week for the last year, and per month since the first recorded use. Hover a
  bar for its cost, calls and tokens. Above the charts is the number of
  sessions with a client connected right now. Owners/admins see the whole
  server's usage with a per-user breakdown of the last 30 days, and can switch
  to just their own.
- **Audit** (owners only) — search the audit log (logins, user changes, new
  and cleared sessions, uploads, and memory, skill, hat and project changes),
  filtered by period, action and user, with counts per action, user and day.

**MCP tools.** External MCP (Model Context Protocol) servers add tools the
assistant uses on its own whenever a task needs them. The left sidebar's
**MCP tools** entry (below Scheduled jobs) lists each connected server and its
tools; its gear button opens Settings → MCP servers, where you register a
server by name and URL, with no authentication, an API key / request header,
or OAuth. For an OAuth
server you're sent to the provider to sign in right after registering, and
then returned to Harlequin. If a sign-in later expires or is revoked, or an
admin shares a new OAuth server, you get an alert with a **Sign in** button
(TUI: `/mcp auth <scope/name>`), and the assistant tells you to sign in if you
ask for something that server provides. Owners/admins can register a server for everyone in the
organisation; each user still signs in to it with their own account. On the
mobile UI the same page is under the menu's **MCP servers**. MCP must be
enabled on the server (`mcp.enabled: true`), and OAuth and API-key servers also need
`HARLEQUIN_SECRET_KEY` set so credentials can be encrypted at rest.

**Files and uploads.** Drop files anywhere in the window to add them: by
default they go to the active project, otherwise to your personal files. On
the Files page you can pick the destination (owners/admins can also add to
the shared org corpus) and drops there go to it; on the Projects page, drops
go to the project you have selected. Progress shows in the bottom-left
corner; failed uploads stay there until you dismiss them. The Files page also
filters by scope, searches inside documents, adds pasted text as a document,
and compares two documents side by side. Each row's **…** menu has
**View extracted text** (for PDFs, Word files and images), which shows the
Markdown text taken from the file — what search and the assistant read — and
**Delete**, which permanently removes the file, its extracted Markdown text and
its search-index entries (deleted data is also wiped from the database file).

**Scheduled jobs.** In the browser, *+ Add* opens a form where you pick a
common schedule (every 15 minutes … every month, with a time-of-day and
weekday picker where relevant) and choose a skill from a list; the job type
defaults to *skill* (an agent turn), with *js* (a script, no AI) as the
alternative. Tick **Custom** to type a raw cron expression instead. Times are
the server's clock.

**Keyboard shortcuts** (`⌘` on macOS):

| Shortcut | Action |
|---|---|
| `Ctrl+K` | Command palette — jump to any session, file, project, page or action |
| `Ctrl+Shift+O` | New session |
| `Ctrl+B` | Show / hide the sidebar |
| `Ctrl+.` | Show / hide the side panel |
| `Ctrl+,` | Settings |
| `Enter` / `Shift+Enter` | Send / new line in the composer |
| `↑` in an empty composer | Recall earlier messages |

Slash commands work in the web composer too: type `/` for the menu
(`/help`, `/project`, `/export`, `/clear`, and `/alert` for owners/admins).

**Rewinding a session.** Hover a message in the chat (mobile: the small buttons
under it) for **Rewind**. On a reply it removes everything after that reply;
on one of your messages it removes that message and everything after it and
puts the text back in the composer, so you can edit and resend it. It isn't
available while a turn is running, and in a project session it changes the
session for every member. Removed turns' effects outside the conversation
(saved memories or documents, sent e-mail, usage cost) stay.

## Projects

A project is a shared workspace. Its members see the same chat sessions with
the agent (a project can hold several), the same documents and project memory,
and a members-only chatroom. Everything outside the project — your personal
sessions, documents, and memories — stays private to you.

**Creating and switching.** Anyone can create a project. In the desktop web
app, use the workspace switcher at the top of the sidebar to create one and
switch to it; the Projects page manages a project's members and files (mobile:
the project selector in the header, the **Manage…** sheet, and the Projects
tab). In the TUI, `/project new <name>` and `/project switch <name>`.
While a project is active, chatting talks to the project's shared session —
every member sees the same live conversation, including a turn already in
progress — and the project chatroom appears (desktop: the side panel's Team
chat tab; mobile: the 💬 toggle; TUI: split pane, post with `/say`).

**Sessions.** A new project starts with one AI session; create more as usual
(`/new`) while the project is active, or move an existing personal session —
and its whole history — into a project: on desktop, hover the session in the
sidebar (in your personal workspace), open its **…** menu and pick the project
under **Move to project…** (mobile: the 📁 button on a session in the sessions
drawer of your personal workspace; TUI: `/project assign <project>` while in
the personal session). A session belongs to at most one project. To switch
between a project's sessions: on desktop, the sidebar lists the active
project's sessions; on mobile, open the sessions drawer (☰); in the TUI, `/resume`
opens the same project-aware picker (`/project sessions` prints the ids, and
`/resume <id>` jumps directly).

**Documents.** Drop files into the window while the project is active, or
upload from the Files or Projects page (mobile: the Projects tab; TUI:
`/upload project <path>`); ingestion shows live progress. In a
project session the agent reads the project corpus alongside shared and
personal scopes, and reports it saves (`save_doc`) land in the project corpus
by default, citations pinned, so every member can find and open them. To move or
copy a project file into another of your projects, open its **…** menu — in
the sidebar or on the Files page (while in the project), or on the Projects
page — and pick **Move to project…** or **Copy to project…**, then the target
project. Its text, search index and original file
go along; after a move, old citations and `p<project>.<id>` references to it
no longer resolve.

**Inviting members.** Any member can invite others. In the browser, the
Projects page (mobile: Projects tab) shows the picked project's members and an
"Invite by email" box (mobile's Manage… sheet has the same box for the active
project); as you type, the
field suggests account emails from the server's user directory — current
members are left out of the suggestions. In the TUI, use `/project invite
<email>`. The invitee gets an alert with **Accept** and **Decline** buttons,
and the invitation also appears in their Invitations list (desktop: the
workspace switcher or the Projects page; mobile: the Manage… sheet; TUI:
`/project invites`, then `/project accept <id>` or `/project decline <id>`).
Answering in any of those places clears it everywhere; nothing is shared with
them until they accept. Invites are
by exact account email — the person must already have an account on the
server. If email suggestions don't appear, the server may have the user
directory disabled (`auth.user_directory: false`); typed emails still work.

**Project memory.** In a project session, facts the agent remembers go to the
project's shared memory by default, visible to every member; you can still ask
it to keep something personal. Browse and edit project memories in the web
Memory view's project tab, or with `/memory project` in the TUI.

**Searching across projects.** Normally the agent searches your personal +
shared scopes, plus the active project. Ask it to look "across all my
projects" and it searches every project you belong to; hits from other
projects carry qualified references — `p3.17` for a document, `d.p3.421` for a
cited passage, where `3` is the project id — which open against the right
project when clicked, and which you can hand back to the agent later even from
a different session. Saved reports pin their citations the same way, so a
report keeps pointing at its true sources no matter where it's opened from.
Project content is members-only throughout: another user's search never sees
projects you belong to unless they are members too.

**Leaving.** `/project leave` (web: "Leave workspace" on the desktop Projects
page or picking Personal in the workspace switcher; mobile: "Leave project")
just returns you to your
personal workspace — you stay a member. `/project depart` (web: "Depart
project") removes your membership; the project and its content remain for the
other members.

**Removing members.** The project's creator and owners/admins can remove other
members: the ✕ on a member in the Projects page (mobile: the Projects tab), or
`/project remove <email>` in the TUI. The removed member is disconnected from
the project's sessions and team chat right away, a turn they were running is
stopped, and they get a notification; they can be invited again later. The
creator can't be removed (delete the project instead).

**Deleting.** `/project delete <id-or-name> --yes` (web: "Delete project…",
which asks you to type the project name) permanently deletes a project for
**all** members: its sessions, documents, memories, chat history and uploaded
files. Only the project's creator or an owner/admin may do it (an admin need
not be a member). Live connections to the project are closed, and the other
members get a notification. This cannot be undone.

## How skills resolve (scopes, projects, hats)

Skills live in three scopes at once — your **personal** scope, the organisation's
**shared** scope, and a **project** scope when you have one active. Several
copies of the same skill can exist; when the agent loads one, exactly one copy
wins. A worn **hat** sits on top of all of that: it carries its own copies of
skills (its *overlay*), and those take precedence over anything the scopes
would resolve. Hats and projects are independent — you can have neither, either,
or both active.

```
        Skill resolution — which copy of "foo" wins
        ═══════════════════════════════════════════

               ┌────────────────────────────────┐
   need foo ─▶ │ 1  HAT overlay      (if worn)  │─ has skills/foo/… ─▶ WINS
               └───────────────┬────────────────┘
                               │ no hat, or foo not in the overlay
               ┌───────────────▼────────────────┐
               │ 2  PROJECT scope  (if active)  │─ has foo ──────────▶ WINS
               └───────────────┬────────────────┘
                               │ no project, or not there
               ┌───────────────▼────────────────┐
               │ 3  SHARED scope   (the org)    │─ has foo ──────────▶ WINS
               └───────────────┬────────────────┘
                               │ not there
               ┌───────────────▼────────────────┐
               │ 4  PERSONAL scope (yours)      │─ has foo ──────────▶ WINS
               └───────────────┬────────────────┘
                               │
                               ▼
                          skill not found
```

The four situations this produces:

| Project active | Hat worn | Resolution order |
|:-:|:-:|---|
| no | no | shared → personal |
| yes | no | project → shared → personal |
| no | yes | **hat overlay** → shared → personal |
| yes | yes | **hat overlay** → project → shared → personal |

Further hat effects, independent of the overlay:

- **Visibility** — a hat may declare a `skills:` list in its frontmatter; while
  worn, only those skills are offered to the agent (empty list = all skills).
- **System prompt** — a hat's `system_prompt.md` body is *added to* the default
  system prompt, so a short role description keeps all the default rules
  (citations, documents, memory). Put `replace_prompt: true` in the frontmatter
  to replace the default prompt entirely instead, or `use_prompt: false` to keep
  the body but switch it off.

Because a *deeper* scope wins (shared beats user, project beats shared), a copy
you edit in a shallower scope can be silently invisible. `/skills` flags this:
`web-extractor [shared, shadows personal]` means a personal copy exists but the
shared one is being served.

A worn hat shadows the same way, only harder: its copy of a skill wins over
every scope. While you wear a hat, `/skills` marks those skills
`overridden by hat <name>`, and `/skill edit` (and the web skill editor) warn
that your edit won't take effect until you take the hat off. Shared hats are
edited only by owners and admins, personal hats by their owner; anyone can read
what a hat they can wear runs with
`/hat view <name> skills/<skill>/SKILL.md`, or in the web Hats page (**view** next to an
overlay, or **Files** → **View**). To get a hat's skill changed
for everyone, ask an admin.

### Hats

A **hat** is a working mode for a session. Wear one and the agent gets a role
(the hat's prompt is added to its normal instructions) and, optionally, its own
variants of some skills. Two hats ship with the server: **analyst** (compare and
reconcile documents, tabular output, everything cited, saved as a report) and
**researcher** (outside-world research, source and date for each fact, findings
kept apart from inference).

- **Wearing**: click the hat in the chat header (or a card on an empty
  session), `/hat wear <name>` in the TUI, or just ask the agent ("put on the
  analyst hat"). The agent mentions a fitting hat when your request clearly
  matches one, but never switches on its own. A change applies from your next
  message and shows up in every client attached to the session.
- **It stays on**: the hat you last wore is remembered and new sessions start
  with it. Take it off (`/hat off`, or the chip) to go back to no hat.
- **Scheduled jobs**: a skill job can wear a hat (`hat=<name>` in `/cron add`,
  `/cron hat <id> [<name>]`, the Cron page, or the agent's `cron_create`, which
  uses the hat you are wearing). If the hat is deleted later, the job fails with
  an error instead of silently running without it.
- **Personal and shared hats**: you can create personal hats (only you see
  them) and, as owner/admin, shared ones for everyone. A shared hat wins over a
  personal one of the same name. Personal hats can't be worn in project
  sessions, because other members couldn't see them.

## Slash commands in the TUI

Anything you type that starts with `/` at the beginning of the line is a
command, not a chat message. Typing `/` opens an autocomplete menu; keep
typing to filter it, and pick a command with the arrow keys and Enter.
`/help` shows the built-in summary of everything below.

### Sessions

| Command | What it does |
|---------|--------------|
| `/new` | Start a new session (it starts with the hat you last wore; a tip lists the available hats). |
| `/clear` | Clear the current session's messages so the next turn starts with a fresh context. The session, its title, and its hat survive; works in project sessions too (clears for all members). |
| `/resume` | Pick an earlier session to resume from a list. |
| `/resume <id>` | Resume a specific session by id. |
| `/resume <query>` | Filter the session picker by title. |
| `/queue` | List messages you typed while the agent was busy (they are queued, not lost). |
| `/queue del <n>` / `/queue clear` | Remove one queued message / all of them. |
| `/export [raw]` | Save the transcript to `session_YYYYMMDD_HHMM.md` in the current directory. Plain form keeps User+Assistant messages; `raw` includes thinking and tool calls. |
| `/context` | Show what fills the context window (system prompt, skills, messages, tool results) and the session's LLM cost so far. |
| `/quit` (or `/exit`) | Leave the TUI. The session keeps living on the server. |

Start the TUI with `--session <id>` to resume a session directly.

### Skills and hats

Skills are instructions (plus optional scripts) the agent can load on demand.
They resolve across scopes: project (when one is active), then shared
(organisation), then your own personal scope.

| Command | What it does |
|---------|--------------|
| `/skills` | List the skills available to you, with the scope each comes from. Copies in shallower scopes are flagged (`shadows personal`), since edits there are invisible while a deeper copy exists. |
| `/skill create <name> <description>` | Create a new skill (add `--personal`, `--shared`, or `--project` to pick the scope; `--user` still works). |
| `/skill edit <name> [file]` | Edit a skill file in the built-in editor. On save you are asked which scope to save into when more than one is writable (Enter = the scope the file came from). |
| `/skill download <name> [file]` | Download a skill (or one file) for local editing. |
| `/skill upload <name> [file]` | Upload a skill (or one file); scope flags as above. |
| `/skill del <name>` | Delete the skill from a scope (scope flags as above). |
| `/skill diff <name>` | Show local edits vs the server version. |
| `/hat` | List hats (shared ones, plus your personal ones marked `(personal)`). A hat is a working mode: a role added to the system prompt plus optional specialised skills that overlay normal resolution (see [How skills resolve](#how-skills-resolve-scopes-projects-hats)). |
| `/hat show <name>` | Show a hat's details: its visible-skills list and the skills it overlays. |
| `/hat wear <name>` | Wear a hat in this session; it is remembered for new sessions. Personal hats can't be worn in a project session. |
| `/hat off` | Remove the hat (back to the default prompt and skill set). |
| `/hat create <name> [--shared] [description]` | Create a personal hat; with `--shared` (owners/admins) an organisation-wide one. |
| `/hat edit <name> [file]` | Edit a hat file you may edit (your own hats; shared ones need owner/admin) in the built-in editor (default `system_prompt.md`; overlays live at `skills/<skill>/…`). |
| `/hat files <name>` | List a hat's files (prompt + skill overlays). |
| `/hat view <name> [file]` | Show a hat file read-only (default `system_prompt.md`; overlays are `skills/<skill>/SKILL.md` etc.). Anyone can read a hat they can wear. |
| `/hat addskill <hat> <skill>` | Copy the currently-resolved skill into the hat's overlay, ready for specialising. |
| `/hat rmskill <hat> <skill>` | Remove a skill overlay from the hat. |
| `/hat prompt <name> on\|off` | Toggle the hat's custom prompt body; `off` keeps its content but the hat adds nothing to the default prompt. |
| `/hat del <name>` | Delete a hat (your own, or a shared one as owner/admin). |


### Memory and documents

| Command | What it does |
|---------|--------------|
| `/memory [scope]` | List memories with ids (`personal`, `shared`, or `project` for the active project's). |
| `/memory find <phrase>` | Search memories (your own + shared) by relevance. |
| `/memory show <id>` | Show one memory (including the document it was taken from, if any). |
| `/memory edit <id>` | Edit a memory's content (and, for personal/shared memories, its slots) in a built-in text editor overlay. Project memories don't support slots — the editor omits the slots section for them. |
| `/memory del <id>…` | Delete one or more memories by id (shared ones if you're an admin). |
| `/memory conflicts` | List flagged duplicate/conflicting memory pairs. |
| `/memory resolve <id>` | Mark a conflict flag as resolved. |
| `/docs search <query>` | Search documents (personal + shared, + project if active). |
| `/docs list` | List documents across scopes, each shown under its reference id (`u.2`, `s.5`, `p3.19`) — the same form `/docs view`, `/docs del`, and chat references accept. Project ids are qualified (`p<projectID>.<id>`) so a document stays unambiguous outside the context of "the active project". |
| `/docs add [scope] <path>` | Upload a document; the same as `/upload` below. |
| `/docs view <ref> [text\|file]` | Show a document's text, or with `file` save the original (PDF, DOCX) to a temporary file to open in your own viewer (e.g. `/docs view p3.19 file`; the long form `<scope> <id>` also works). |
| `/docs del <ref>` | Delete a document by its reference (e.g. `/docs del p3.19`; the long form `<scope> <id>` still works). |
| `/upload [scope] <path>` | Upload a document into `personal`, `shared`, or `project` scope (defaults to the active project, else personal). Accepted: `.txt`, `.md`, `.html`, `.pdf`, `.docx`, `.doc`, `.pptx`, `.ppt`, `.xlsx`, `.xls`, `.ods`, `.odt`, `.rtf`, `.epub`, and images (`.png`, `.jpg`, `.gif`, `.webp`). Ingestion runs server-side with a live status line (stage, percent, elapsed) — in the browser too. |
| `/attach <path>` | Upload a document or image (same formats as `/upload`, to the active project else your personal files) and attach it to your **next message**: once ingested it is listed at the end of that message as `@"u.18/report.pdf"`, and the agent reads it (images are shown to vision models directly). Repeat for several files; `/attach` alone lists what is pending, `/attach clear` drops it. Sending waits for uploads to finish. |

### Projects

| Command | What it does |
|---------|--------------|
| `/project` | Manage projects: `list`, `new`, `switch`, `invites`, `accept <id>`, `decline <id>`, `invite <email>`, `remove <email>` (remove a member from the active project; creator or owner/admin only), `members`, `sessions` (list the active project's sessions), `assign <id-or-name>` (move the current **personal** session, with its history, into that project; run it from your personal workspace), `leave`, `depart`, `delete` (permanently delete a project, creator/admin only; needs `--yes`). |
| `/say <message>` | Post a message to the active project's chatroom. |

See the [Projects](#projects-1) chapter below for the full workflow.

### Automation and integrations

| Command | What it does |
|---------|--------------|
| `/cron` | List scheduled jobs. |
| `/cron show <id>` | Show a job, including its last run output. |
| `/cron add "<name>" "<spec>" js "<target>" ["<input-json>"]` | Schedule a JavaScript job. |
| `/cron add "<name>" "<spec>" skill "<skill\|->" "<prompt>" [hat=<name>]` | Schedule a skill/prompt job, optionally run wearing a hat. |
| `/cron hat <id> [<name>]` | Make a skill job wear a hat; without a name, take the hat off. |
| `/cron on\|off <id>` | Enable / disable a job. |
| `/cron run <id>` | Run a job immediately. |
| `/cron del <id>` | Delete a job. |
| `/mcp` | List MCP servers (organisation + your personal ones) with status. |
| `/mcp show <s/name>` | Show one server (`s` is the scope: `organisation` or `personal`; the older `shared`/`user` still work). |
| `/mcp add <s/name> <url> [header Name:"Value" … \| oauth]` | Register an MCP server. |
| `/mcp test <s/name>` | Connect and list the server's tools. |
| `/mcp auth <s/name>` | Authorize an OAuth MCP server (prints a URL to open). |
| `/mcp del <s/name>` | Remove an MCP server. |
| `/config` | List your per-user config (small key/value settings). |
| `/config set <k> <v>` | Set a key, e.g. `/config set telegram.chat_id 12345`. |
| `/config del <key>` | Delete a key. |

### Alerts, usage, misc

| Command | What it does |
|---------|--------------|
| `/dismiss [n …\|all]` | Dismiss alert(s) shown above the transcript, by number or all. |
| `/run <n>` | Run the prompt carried by alert *n*. |
| `/alert <message>` | (owner/admin) Broadcast an alert to all users. |
| `/usage` | Show your token/cost usage. |
| `/help` | Show the built-in command summary. |
