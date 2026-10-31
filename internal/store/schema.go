package store

// metaDDL 单独建表：Open 必须先读出 schema_version 完成校验，
// 再执行 schemaSQL，避免在版本不兼容的库上改动表结构。
const metaDDL = `
CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

const schemaSQL = `
CREATE TABLE IF NOT EXISTS project (
  id           INTEGER PRIMARY KEY,
  key          TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL DEFAULT '',
  git_remote   TEXT NOT NULL DEFAULT '',
  primary_path TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS project_path (
  id         INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  agent      TEXT NOT NULL,
  slug       TEXT NOT NULL DEFAULT '',
  path       TEXT NOT NULL,
  confidence TEXT NOT NULL DEFAULT 'high',
  UNIQUE(project_id, agent, path)
);

-- project_alias 把 agent 的低置信记忆桶（agent:slug）固定归属到某个项目。
-- 这类 slug 目录没有可解析的工作区路径，重扫时只能按 key 建项目；
-- 没有别名时会把已知项目的记忆复制成孤立项目。
CREATE TABLE IF NOT EXISTS project_alias (
  id         INTEGER PRIMARY KEY,
  agent      TEXT NOT NULL,
  slug       TEXT NOT NULL,
  project_id INTEGER NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  UNIQUE(agent, slug)
);

CREATE TABLE IF NOT EXISTS source (
  id            INTEGER PRIMARY KEY,
  agent         TEXT NOT NULL,
  kind          TEXT NOT NULL,
  path          TEXT NOT NULL,
  content_hash  TEXT NOT NULL,
  prev_hash     TEXT NOT NULL DEFAULT '',
  changed_at    INTEGER NOT NULL DEFAULT 0,
  mtime         INTEGER NOT NULL DEFAULT 0,
  size          INTEGER NOT NULL DEFAULT 0,
  last_error    TEXT NOT NULL DEFAULT '',
  last_seen_run INTEGER NOT NULL DEFAULT 0,
  updated_at    INTEGER NOT NULL,
  UNIQUE(agent, kind, path)
);

CREATE TABLE IF NOT EXISTS memory_item (
  id             INTEGER PRIMARY KEY,
  scope          TEXT NOT NULL,
  project_id     INTEGER NOT NULL DEFAULT 0,
  type           TEXT NOT NULL,
  text           TEXT NOT NULL,
  text_norm_hash TEXT NOT NULL,
  stale          INTEGER NOT NULL DEFAULT 0,
  first_seen_at  INTEGER NOT NULL,
  last_seen_at   INTEGER NOT NULL,
  embedding      BLOB,
  embed_model    TEXT NOT NULL DEFAULT '',
  embed_dim      INTEGER NOT NULL DEFAULT 0,
  embedded_at    INTEGER NOT NULL DEFAULT 0,
  UNIQUE(scope, project_id, type, text_norm_hash)
);

CREATE TABLE IF NOT EXISTS memory_source (
  id        INTEGER PRIMARY KEY,
  memory_id INTEGER NOT NULL REFERENCES memory_item(id) ON DELETE CASCADE,
  agent     TEXT NOT NULL,
  path      TEXT NOT NULL,
  quote     TEXT NOT NULL DEFAULT '',
  UNIQUE(memory_id, agent, path)
);

CREATE TABLE IF NOT EXISTS session (
  id          INTEGER PRIMARY KEY,
  agent       TEXT NOT NULL,
  external_id TEXT NOT NULL,
  project_id  INTEGER NOT NULL DEFAULT 0,
  title       TEXT NOT NULL DEFAULT '',
  started_at  INTEGER NOT NULL DEFAULT 0,
  updated_at  INTEGER NOT NULL DEFAULT 0,
  event_count INTEGER NOT NULL DEFAULT 0,
  path        TEXT NOT NULL DEFAULT '',
  UNIQUE(agent, external_id)
);

CREATE TABLE IF NOT EXISTS session_event (
  id         INTEGER PRIMARY KEY,
  session_id INTEGER NOT NULL REFERENCES session(id) ON DELETE CASCADE,
  seq        INTEGER NOT NULL,
  role       TEXT NOT NULL,
  kind       TEXT NOT NULL,
  text       TEXT NOT NULL,
  tool       TEXT NOT NULL DEFAULT '',
  files_json TEXT NOT NULL DEFAULT '[]',
  ts         INTEGER NOT NULL DEFAULT 0,
  UNIQUE(session_id, seq)
);

CREATE TABLE IF NOT EXISTS task_card (
  id             INTEGER PRIMARY KEY,
  session_id     INTEGER NOT NULL REFERENCES session(id) ON DELETE CASCADE,
  project_id     INTEGER NOT NULL DEFAULT 0,
  goal           TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL DEFAULT 'unknown',
  decisions_json TEXT NOT NULL DEFAULT '[]',
  files_json     TEXT NOT NULL DEFAULT '[]',
  commands_json  TEXT NOT NULL DEFAULT '[]',
  errors_json    TEXT NOT NULL DEFAULT '[]',
  open_questions TEXT NOT NULL DEFAULT '',
  next_steps     TEXT NOT NULL DEFAULT '',
  confidence     TEXT NOT NULL DEFAULT 'rule',
  generated_by   TEXT NOT NULL DEFAULT 'rule',
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL,
  UNIQUE(session_id)
);

CREATE TABLE IF NOT EXISTS handoff (
  id            INTEGER PRIMARY KEY,
  project_id    INTEGER NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  body          TEXT NOT NULL,
  content_hash  TEXT NOT NULL,
  budget_tokens INTEGER NOT NULL DEFAULT 0,
  generated_at  INTEGER NOT NULL,
  UNIQUE(project_id)
);

CREATE TABLE IF NOT EXISTS agent_link (
  id             INTEGER PRIMARY KEY,
  project_id     INTEGER NOT NULL DEFAULT 0,
  agent          TEXT NOT NULL,
  block_file     TEXT NOT NULL,
  installed_hash TEXT NOT NULL,
  installed_at   INTEGER NOT NULL,
  UNIQUE(project_id, agent)
);

CREATE TABLE IF NOT EXISTS task (
  id                  INTEGER PRIMARY KEY,
  project_id          INTEGER NOT NULL DEFAULT 0,
  goal                TEXT NOT NULL DEFAULT '',
  status              TEXT NOT NULL DEFAULT 'doing',
  owner_agent         TEXT NOT NULL DEFAULT '',
  claimed_at          INTEGER NOT NULL DEFAULT 0,
  next_steps          TEXT NOT NULL DEFAULT '',
  decisions_json      TEXT NOT NULL DEFAULT '[]',
  files_json          TEXT NOT NULL DEFAULT '[]',
  commands_json       TEXT NOT NULL DEFAULT '[]',
  errors_json         TEXT NOT NULL DEFAULT '[]',
  open_questions      TEXT NOT NULL DEFAULT '',
  source_agent        TEXT NOT NULL DEFAULT '',
  source_path         TEXT NOT NULL DEFAULT '',
  manual_edited_at    INTEGER NOT NULL DEFAULT 0,
  completed_at        INTEGER NOT NULL DEFAULT 0,
  archived_at         INTEGER NOT NULL DEFAULT 0,
  merged_into_task_id INTEGER NOT NULL DEFAULT 0,
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS task_step (
  id          INTEGER PRIMARY KEY,
  task_id     INTEGER NOT NULL REFERENCES task(id) ON DELETE CASCADE,
  seq         INTEGER NOT NULL,
  subject     TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  active_form TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'pending',
  source_key  TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  UNIQUE(task_id, seq)
);

CREATE TABLE IF NOT EXISTS task_snippet (
  id      INTEGER PRIMARY KEY,
  task_id INTEGER NOT NULL REFERENCES task(id) ON DELETE CASCADE,
  seq     INTEGER NOT NULL,
  role    TEXT NOT NULL DEFAULT '',
  kind    TEXT NOT NULL DEFAULT '',
  text    TEXT NOT NULL DEFAULT '',
  ts      INTEGER NOT NULL DEFAULT 0,
  UNIQUE(task_id, seq)
);

CREATE TABLE IF NOT EXISTS task_session (
  task_id    INTEGER NOT NULL REFERENCES task(id) ON DELETE CASCADE,
  session_id INTEGER NOT NULL REFERENCES session(id) ON DELETE CASCADE,
  role       TEXT NOT NULL DEFAULT 'source',
  created_at INTEGER NOT NULL,
  PRIMARY KEY(task_id, session_id)
);

CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
  text_seg,
  tokenize = 'unicode61'
);

CREATE INDEX IF NOT EXISTS idx_memory_source_memory ON memory_source(memory_id);
CREATE INDEX IF NOT EXISTS idx_memory_source_agent ON memory_source(agent);
CREATE INDEX IF NOT EXISTS idx_memory_item_project ON memory_item(project_id, stale);
CREATE INDEX IF NOT EXISTS idx_session_project ON session(project_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_task_card_project ON task_card(project_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_task_project ON task(project_id, archived_at, status);
CREATE INDEX IF NOT EXISTS idx_task_owner ON task(owner_agent, status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_task_step_source ON task_step(task_id, source_key) WHERE source_key <> '';
CREATE INDEX IF NOT EXISTS idx_task_snippet_task ON task_snippet(task_id, seq);
`

const schemaVersion = "1"
