# Users and roles

Every account has one of three roles, highest privilege first: **owner**,
**admin** and **user**. Identity is the account's email address. The role
decides what you may do to *organisation-wide* things — shared data, skills,
hats, other people's accounts; everyone has full control of their own personal
data and of the projects they belong to. Scopes themselves are described in
[SCOPES.md](SCOPES.md), projects in [PROJECTS.md](PROJECTS.md).

Owners and admins together are called **elevated** in the code
(`types.IsElevated`); owners alone, `types.IsOwner`. Every check is enforced by
the server, not just hidden in the clients.

## At a glance

| Capability | user | admin | owner |
|------------|:----:|:-----:|:-----:|
| Chat, own sessions, personal memories and documents | ✓ | ✓ | ✓ |
| Own skills, MCP servers, scheduled jobs, config | ✓ | ✓ | ✓ |
| Create projects; invite to, work in and leave projects they belong to | ✓ | ✓ | ✓ |
| Wear any hat | ✓ | ✓ | ✓ |
| Delete a project they created, or remove its other members | ✓ | ✓ | ✓ |
| Write, change and delete **shared** (org-wide) memories | | ✓ | ✓ |
| Add and delete **shared** documents | | ✓ | ✓ |
| Create, edit and publish **shared** skills | | ✓ | ✓ |
| Create, edit and delete **hats** | | ✓ | ✓ |
| Register shared MCP servers | | ✓ | ✓ |
| Delete **any** project, or remove members from it (without being a member) | | ✓ | ✓ |
| Broadcast an alert to all users | | ✓ | ✓ |
| Read the audit log and the system-prompt template | | ✓ | ✓ |
| Read any session's trace log; view other users' usage | | ✓ | ✓ |
| Create, edit and delete **user accounts** | | | ✓ |

## user

The default role for new accounts (including self-registration). A user:

- owns their sessions, personal memories and documents, user-scope skills, MCP
  servers (if `mcp.allow_user_servers`, default on), scheduled jobs and per-user
  config;
- can read everything shared with the organisation — shared memories,
  documents and skills — but not change it. When the assistant tries to save a
  shared memory for an ordinary user, it is stored as a personal one instead;
- can create projects and, as a member, use and write the project's sessions,
  documents, memories and skills, invite others, and depart. They can delete a
  project, or remove its other members, only if they created it;
- can wear any hat, but not edit hats.

## admin

Everything a user can, plus management of organisation-wide content:

- **Shared memory**: write, correct and delete org-wide facts (the "memory
  surgery" role), including slot values.
- **Shared documents**: upload to the shared corpus and delete shared documents.
- **Skills**: create and edit shared-scope skills, and publish a personal skill
  to the organisation.
- **Hats**: create, edit and delete hats, their skill overlays and system
  prompts.
- **MCP**: register servers for everyone (each user still signs in to OAuth
  servers with their own account).
- **Projects**: delete any project and remove its members, member or not.
- **Oversight**: read the audit log, any session's trace log and the
  system-prompt template, and view other users' token usage and cost.
- **Broadcast**: send an alert to every user (Settings → General → Broadcast).

## owner

Everything an admin can, plus **account management** — the only role that can
create and edit users:

- create accounts with any role, change roles, reset passwords (which signs the
  user out everywhere) and delete accounts with their personal data — from
  **Settings → Users** in the desktop web UI, or the `createuser`,
  `changepassword`, `deleteuser` and `listusers` server commands;
- the server always keeps at least one owner: the last owner can't be demoted
  or deleted, and you can't delete your own account from the web UI.

Deleting an account removes the user's personal database and files (sessions,
memories, documents, skills, scheduled jobs) and their project memberships.
Project and shared content they contributed stays.

## Getting an account

- **First owner**: `./bin/harlequin-server createuser --config server.yaml
  --owner --password <pw> owner@example.com` (see the README).
- **Created by an owner**: Settings → Users, or `createuser` (`--admin` /
  `--owner` for elevated roles).
- **Self-registration** (if `auth.allow_registration`, default on): sign up
  with an email and password, confirm with an emailed 6-digit code. Self-
  registered accounts are always `user`; an owner can promote them later.

Passwords must be at least 8 characters. A role change takes effect on the
user's next request.
