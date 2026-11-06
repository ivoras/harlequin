# Projects

A **project** is a shared workspace: a group of members who share AI chat
sessions, a document corpus, a memory, skills, and a members-only team chat.
Everything outside the project — your personal sessions, documents and
memories — stays private to you. How project data relates to personal and
organisation-wide data is described in [SCOPES.md](SCOPES.md); the user-facing
walkthrough (with every client's buttons and commands) is in the
[user guide](user_guide.md).

## The basics

- **Anyone can create a project** and becomes its first member. Names don't
  have to be unique; lists show the creator to tell projects apart.
- **One project is active at a time** per client (or none: your personal
  workspace). The active project is kept in the web UI's URL (`?p=<id>`), so a
  refresh or a shared link reopens it.
- **Leaving** (Leave workspace / Personal in the switcher, TUI `/project leave`)
  only returns you to your personal workspace; you stay a member.
- Projects and [hats](SCOPES.md#skills-and-hats) are independent: you can wear
  any hat in any project.

## Members

| Action | Who can do it | How |
|--------|---------------|-----|
| Create a project | any user | workspace switcher, TUI `/project new <name>` |
| Invite someone | any member | Projects page "Invite by email", TUI `/project invite <email>` |
| Accept / decline an invite | the invitee | Invitations list, TUI `/project invites` |
| Leave the project (depart) | yourself | "Depart project", TUI `/project depart` |
| Delete the project | its creator, or any owner/admin (need not be a member) | "Delete project…" (type the name to confirm), TUI `/project delete <id-or-name> --yes` |

Invitations are by the exact email of an existing account. The invitee gets an
in-app notification, and nothing is shared with them until they accept. The
invite box suggests account emails from the server's user directory (unless
`auth.user_directory: false`).

Limits today: there is no way to add someone without their accepting, to see or
cancel pending invites, or to remove *another* member — members can only remove
themselves. If the last member departs, the project remains (memberless) until
its creator or an owner/admin deletes it. Deleting a project removes its
sessions, documents, memories, chat history and files for **all** members,
closes live connections to it, and notifies the other members; it can't be
undone. Deleting a user account removes that user's memberships.

## Sessions

- A project holds one or more AI chat sessions. New sessions created while the
  project is active belong to it.
- **Assigning** an existing personal session to the active project *moves* it —
  with its whole history and its recorded LLM cost — out of your personal
  storage into the project. A session belongs to at most one project.
- A project session is **one shared live session**: every member who opens it
  sees the same conversation, including a turn already in progress, and can
  reconnect mid-turn without losing it. Prompts from several members are queued
  and run one turn at a time.
- A turn runs with the **acting member's** identity: their role, personal
  memories and documents, their own skills, MCP servers and scheduled jobs. The
  conversation itself, and anything the agent writes to the project, is shared.
- The session's cost shown under Context usage adds up every member's turns.

## Project data

While a project is active, the agent searches the project's documents and
memories together with the shared organisation scope and your personal one,
and new memories and saved reports go to the **project** by default, visible to
every member. Project skills take precedence over organisation and personal
skills of the same name. Details, including how to reach other projects'
content (`all_projects`, references like `p3.17`), are in
[SCOPES.md](SCOPES.md).

Project content is members-only throughout: search by a non-member never sees
it, and references into a project (e.g. `p3.17`) only open for its members.

## Team chat

Each project has a members-only chatroom for the people in it, separate from
the AI sessions:

- **Where:** desktop web — the side panel's *Team chat* tab (opens when you
  enter a project); mobile web — the 💬 toggle; TUI — a split pane, post with
  `/say <message>`. It's only shown while a project is active.
- **Live:** messages are pushed to every connected member instantly over a
  WebSocket (`GET /api/v1/projects/{id}/ws`; post with a
  `{"type":"chat","content":"…"}` frame).
- **History:** messages are stored in the project database; the last 100 are
  sent when you connect (also `GET /api/v1/projects/{id}/messages`). Deleting the
  project deletes the chat history.
- The chat is for people only: the AI assistant doesn't read it, and it isn't
  part of any AI session's context.

## Storage

The project registry — projects, members, invites — lives in the system
database (`data/harlequin.db`). Each project's own data (sessions and
messages, documents, memories, skills, chat) lives in
`data/projects/<id>/project.db`, with uploaded files under
`data/projects/<id>/files/`. Usage and cost records stay in each acting member's own
database, tagged with the project.
