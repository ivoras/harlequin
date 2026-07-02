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

## Slash commands in the TUI

Anything you type that starts with `/` at the beginning of the line is a
command, not a chat message. Typing `/` opens an autocomplete menu; keep
typing to filter it, and pick a command with the arrow keys and Enter.
`/help` shows the built-in summary of everything below.

### Sessions

| Command | What it does |
|---------|--------------|
| `/new` | Start a new session (keeps the currently worn hat). |
| `/resume` | Pick an earlier session to resume from a list. |
| `/resume <id>` | Resume a specific session by id. |
| `/resume <query>` | Filter the session picker by title. |
| `/queue` | List messages you typed while the agent was busy (they are queued, not lost). |
| `/queue del <n>` / `/queue clear` | Remove one queued message / all of them. |
| `/export [raw]` | Save the transcript to `session_YYYYMMDD_HHMM.md` in the current directory. Plain form keeps User+Assistant messages; `raw` includes thinking and tool calls. |
| `/quit` (or `/exit`) | Leave the TUI. The session keeps living on the server. |

### Skills and hats

Skills are instructions (plus optional scripts) the agent can load on demand.
They resolve across scopes: project (when one is active), then shared
(organisation), then your own user scope.

| Command | What it does |
|---------|--------------|
| `/skills` | List the skills available to you, with the scope each comes from. |
| `/skill create <name> <description>` | Create a new skill (add `--user`, `--shared`, or `--project` to pick the scope). |
| `/skill edit <name> [file]` | Edit a skill file in the built-in editor. |
| `/skill download <name> [file]` | Download a skill (or one file) for local editing. |
| `/skill upload <name> [file]` | Upload a skill (or one file); scope flags as above. |
| `/skill reset <name>` | Delete the skill from a scope (scope flags as above). |
| `/skill diff <name>` | Show local edits vs the server version. |
| `/hat` | List hats. A hat is an org-defined system prompt plus a visible-skills list. |
| `/hat show <name>` | Show a hat's details. |
| `/hat wear <name>` | Wear a hat in this session. |
| `/hat off` | Remove the hat (back to the default prompt and skill set). |

### Memory and documents

| Command | What it does |
|---------|--------------|
| `/memory [scope]` | List memories with ids (`user` or `shared`). |
| `/memory find <phrase>` | Search memories (your own + shared) by relevance. |
| `/memory show <id>` | Show one memory. |
| `/memory delete <id>…` | Delete one or more memories by id (shared ones if you're an admin). |
| `/memory conflicts` | List flagged duplicate/conflicting memory pairs. |
| `/memory resolve <id>` | Mark a conflict flag as resolved. |
| `/docs search <query>` | Search documents (personal + shared, + project if active). |
| `/docs list` | List documents across scopes. |
| `/docs add [scope] <path>` | Upload a `.txt`/`.md`/`.html`/`.pdf` for retrieval (same as `/upload`). |
| `/docs delete <scope> <id>` | Delete a document. |
| `/upload [scope] <path>` | Upload a document into `personal`, `shared`, or `project` scope (default personal). |

### Projects

Projects are shared workspaces: members see the same sessions, documents,
memories, and a live chatroom (shown as a side pane while a project is active).

| Command | What it does |
|---------|--------------|
| `/project` | Manage projects: `list`, `new`, `switch`, `invite`, `assign` (move the current session into the project), `leave`, `depart`. |
| `/say <message>` | Post a message to the active project's chatroom. |

### Automation and integrations

| Command | What it does |
|---------|--------------|
| `/cron` | List scheduled jobs. |
| `/cron show <id>` | Show a job, including its last run output. |
| `/cron add "<name>" "<spec>" js "<target>" ["<input-json>"]` | Schedule a JavaScript job. |
| `/cron add "<name>" "<spec>" skill "<skill\|->" "<prompt>"` | Schedule a skill/prompt job. |
| `/cron on\|off <id>` | Enable / disable a job. |
| `/cron run <id>` | Run a job immediately. |
| `/cron rm <id>` | Delete a job. |
| `/mcp` | List MCP servers (shared + your own) with status. |
| `/mcp show <s/name>` | Show one server (`s` is the scope: `shared` or `user`). |
| `/mcp add <s/name> <url> [header Name:"Value" … \| oauth]` | Register an MCP server. |
| `/mcp test <s/name>` | Connect and list the server's tools. |
| `/mcp auth <s/name>` | Authorize an OAuth MCP server (prints a URL to open). |
| `/mcp rm <s/name>` | Remove an MCP server. |
| `/config` | List your per-user config (small key/value settings). |
| `/config set <k> <v>` | Set a key, e.g. `/config set telegram.chat_id 12345`. |
| `/config rm <key>` | Delete a key. |

### Alerts, usage, misc

| Command | What it does |
|---------|--------------|
| `/dismiss [n …\|all]` | Dismiss alert(s) shown above the transcript, by number or all. |
| `/run <n>` | Run the prompt carried by alert *n*. |
| `/alert <message>` | (owner/admin) Broadcast an alert to all users. |
| `/usage` | Show your token/cost usage. |
| `/help` | Show the built-in command summary. |
