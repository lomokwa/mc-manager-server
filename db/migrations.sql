CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username VARCHAR(50) UNIQUE NOT NULL,
  email VARCHAR(255) UNIQUE,
  password_hash VARCHAR(255) NOT NULL,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS invitations (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  token VARCHAR(64) UNIQUE NOT NULL,
  email VARCHAR(255),
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  expires_at TIMESTAMP NOT NULL,
  used_at TIMESTAMP
);

-- Single-row table: there is one backup schedule per server instance.
CREATE TABLE IF NOT EXISTS backup_config (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  enabled BOOLEAN NOT NULL DEFAULT 0,
  interval_minutes INTEGER NOT NULL DEFAULT 1440,
  keep INTEGER NOT NULL DEFAULT 7
);

-- A named bundle of permissions. The five built-in roles (Owner, Admin,
-- Moderator, Operator, Viewer) are seeded on boot (services.EnsureBuiltinRoles)
-- and marked is_system so the UI can't rename/delete them. "Owner" is further
-- special-cased in handlers/roles.go: it can never be assigned or edited
-- through the API, only via the seed file, so there is exactly one path to
-- unrestricted access and it can't be revoked through a UI mistake.
CREATE TABLE IF NOT EXISTS roles (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name VARCHAR(50) UNIQUE NOT NULL,
  permissions TEXT NOT NULL DEFAULT '[]',
  is_system BOOLEAN NOT NULL DEFAULT 0
);

-- One row per user who has been assigned a role. A user with no row here has
-- no permissions at all (deny by default) -- registering an account alone
-- grants no access beyond logging in.
CREATE TABLE IF NOT EXISTS user_roles (
  user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  role_id INTEGER NOT NULL REFERENCES roles(id),
  overrides TEXT NOT NULL DEFAULT '{}'
);

-- A verified Minecraft account linked to a website user.
CREATE TABLE IF NOT EXISTS minecraft_links (
  user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  mc_username VARCHAR(16) NOT NULL,
  mc_uuid VARCHAR(36) NOT NULL DEFAULT '',
  linked_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- A pending, unconfirmed link attempt's one-time code. One in-flight attempt
-- per user -- starting a new one overwrites whatever was pending.
CREATE TABLE IF NOT EXISTS mc_link_codes (
  user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  mc_username VARCHAR(16) NOT NULL,
  code VARCHAR(6) NOT NULL,
  expires_at TIMESTAMP NOT NULL
);

-- Multi-server registry (Phase 1 of PLAN-multi-server.md). Seeded with
-- exactly one row by services.EnsureDefaultServer, pointing at whatever
-- directory the single server already lived in -- this table describes
-- servers, it never causes one to be moved, copied, or renamed.
CREATE TABLE IF NOT EXISTS servers (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  dir        TEXT NOT NULL UNIQUE,
  port       INTEGER NOT NULL,
  voice_port INTEGER,
  jar        TEXT NOT NULL DEFAULT 'server.jar',
  xms        TEXT NOT NULL DEFAULT '1G',
  xmx        TEXT NOT NULL DEFAULT '2G',
  sort       INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
-- Audit trail: who did what on the panel, so "who banned this player?" is a
-- lookup rather than a hunt through the console log.
--
-- username is denormalised on purpose. The whole value of an audit row is that
-- it still reads correctly later, and a JOIN to users would blank out the
-- moment an account is removed -- exactly when you most want to know what it
-- did. user_id is kept alongside it for grouping, and is NULL for API-key
-- callers, which have no user.
--
-- No query strings are ever stored (see services/activity.go): the API key and
-- the console JWT both travel as query params, so the only way to guarantee
-- this table can't become a credential leak is to never put a URL in it.
CREATE TABLE IF NOT EXISTS activity_log (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  user_id    INTEGER,
  username   TEXT NOT NULL,
  category   TEXT NOT NULL,
  action     TEXT NOT NULL,
  detail     TEXT,
  status     INTEGER NOT NULL DEFAULT 0,
  server_id  TEXT
);

-- Every read of this table is "newest first, optionally one category".
CREATE INDEX IF NOT EXISTS idx_activity_recent ON activity_log(id DESC);
CREATE INDEX IF NOT EXISTS idx_activity_category ON activity_log(category, id DESC);
