-- Projects: shared workspaces. The registry (who owns/belongs to what) is
-- system-wide, so it lives in the system database alongside users; each project's
-- own data (sessions, memories, documents, chat) lives in its per-project db.

CREATE TABLE IF NOT EXISTS projects (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    created_by INTEGER NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Membership. Any member may invite others and assign sessions; there is no
-- per-member role for now (creator is just the first member).
CREATE TABLE IF NOT EXISTS project_members (
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL,
    joined_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (project_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_project_members_user ON project_members(user_id);

-- Pending invitations the invitee accepts to join.
CREATE TABLE IF NOT EXISTS project_invites (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id       INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    invited_user_id  INTEGER NOT NULL,
    invited_by       INTEGER NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending', -- pending | accepted | declined
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (project_id, invited_user_id)
);
CREATE INDEX IF NOT EXISTS idx_project_invites_user ON project_invites(invited_user_id, status);
