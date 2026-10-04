-- ============================================================================
-- 0001_init.sql  Warden PoC store schema v1
-- ============================================================================

CREATE TABLE schema_migrations (
  version          INTEGER PRIMARY KEY CHECK (version > 0),
  name             TEXT    NOT NULL,
  checksum         TEXT    NOT NULL CHECK (checksum GLOB 'sha256:*' AND length(checksum) = 71),
  applied_at       TEXT    NOT NULL,
  runtime_version  TEXT    NOT NULL
) STRICT;

CREATE TABLE store_meta (
  key    TEXT PRIMARY KEY CHECK (key IN ('store_id', 'created_at', 'created_by_version', 'health_probe')),
  value  TEXT NOT NULL
) STRICT;

-- Registry of persisted event types (CF-10). New types are added by INSERT in a
-- later migration, never by rebuilding the events table.
CREATE TABLE event_types (
  type             TEXT    PRIMARY KEY CHECK (type GLOB '[a-z]*' AND type NOT GLOB '*[^a-z.]*'),
  chains           TEXT    NOT NULL CHECK (chains IN ('S', 'Y', 'SY')),
  spillable        INTEGER NOT NULL CHECK (spillable IN (0, 1)),
  since_migration  INTEGER NOT NULL
) STRICT;

-- One row per hash chain: 'sys' plus one per session (CF-09).
CREATE TABLE chains (
  chain                    TEXT    PRIMARY KEY
                           CHECK (chain = 'sys' OR (chain GLOB 'ses_*' AND length(chain) = 30)),
  genesis_hash             TEXT    NOT NULL CHECK (genesis_hash GLOB 'sha256:*' AND length(genesis_hash) = 71),
  head_seq                 INTEGER NOT NULL DEFAULT 0 CHECK (head_seq >= 0),
  head_hash                TEXT    NOT NULL CHECK (head_hash GLOB 'sha256:*' AND length(head_hash) = 71),
  event_count              INTEGER NOT NULL DEFAULT 0 CHECK (event_count >= 0),
  last_checkpoint_seq      INTEGER,
  events_since_checkpoint  INTEGER NOT NULL DEFAULT 0 CHECK (events_since_checkpoint >= 0),
  status                   TEXT    NOT NULL CHECK (status IN ('open', 'closed', 'purged')),
  created_at               TEXT    NOT NULL,
  CHECK (event_count > 0 OR head_hash = genesis_hash)
) STRICT;

-- The ledger. `envelope` holds the exact RFC 8785 canonical JSON of the full
-- envelope including "hash"; the other columns are indexed projections of it.
CREATE TABLE events (
  seq             INTEGER PRIMARY KEY CHECK (seq > 0),
  id              TEXT    NOT NULL UNIQUE CHECK (id GLOB 'evt_*' AND length(id) = 30),
  v               INTEGER NOT NULL CHECK (v = 1),
  ts              TEXT    NOT NULL CHECK (ts GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),
  type            TEXT    NOT NULL REFERENCES event_types(type),
  chain           TEXT    NOT NULL,
  session_id      TEXT    CHECK (session_id IS NULL OR (session_id GLOB 'ses_*' AND length(session_id) = 30)),
  workflow_run_id TEXT    CHECK (workflow_run_id IS NULL OR (workflow_run_id GLOB 'wfr_*' AND length(workflow_run_id) = 30)),
  task_id         TEXT    CHECK (task_id IS NULL OR (task_id GLOB 'tsk_*' AND length(task_id) = 30)),
  execution_id    TEXT    CHECK (execution_id IS NULL OR (execution_id GLOB 'exe_*' AND length(execution_id) = 30)),
  workspace_id    TEXT    CHECK (workspace_id IS NULL OR (workspace_id GLOB 'wsp_*' AND length(workspace_id) = 30)),
  classification  TEXT    CHECK (classification IS NULL OR classification IN ('public', 'internal', 'confidential')),
  actor_kind      TEXT    NOT NULL CHECK (actor_kind IN ('user', 'agent', 'runtime', 'harness')),
  actor_name      TEXT    NOT NULL,
  prev_hash       TEXT    NOT NULL CHECK (prev_hash GLOB 'sha256:*' AND length(prev_hash) = 71),
  hash            TEXT    NOT NULL UNIQUE CHECK (hash GLOB 'sha256:*' AND length(hash) = 71),
  envelope        TEXT    NOT NULL CHECK (json_valid(envelope)),
  payload_blob    TEXT    CHECK (payload_blob IS NULL OR (payload_blob GLOB 'sha256:*' AND length(payload_blob) = 71)),
  -- Lookup columns for audit verify --strict (CF-40) and approval joins.
  call_id         TEXT GENERATED ALWAYS AS (json_extract(envelope, '$.payload.call_id')) VIRTUAL,
  approval_id     TEXT GENERATED ALWAYS AS (json_extract(envelope, '$.payload.approval_id')) VIRTUAL,
  CHECK (chain = 'sys' OR (chain GLOB 'ses_*' AND length(chain) = 30)),
  CHECK (chain = 'sys' OR session_id = chain),
  CHECK (chain <> 'sys' OR session_id IS NULL),
  UNIQUE (chain, seq)
) STRICT;

CREATE INDEX events_session_seq  ON events (session_id, seq)       WHERE session_id IS NOT NULL;
CREATE INDEX events_session_type ON events (session_id, type, seq) WHERE session_id IS NOT NULL;
CREATE INDEX events_type_ts      ON events (type, ts);
CREATE INDEX events_task_seq     ON events (task_id, seq)          WHERE task_id IS NOT NULL;
CREATE INDEX events_call         ON events (call_id, seq)          WHERE call_id IS NOT NULL;
CREATE INDEX events_approval     ON events (approval_id, seq)      WHERE approval_id IS NOT NULL;
-- (chain, seq) is covered by the automatic index of UNIQUE (chain, seq).

-- Purge bookkeeping; also the only authorization for deleting events (§3.3).
CREATE TABLE purges (
  session_id     TEXT    PRIMARY KEY CHECK (session_id GLOB 'ses_*' AND length(session_id) = 30),
  state          TEXT    NOT NULL CHECK (state IN ('deleting', 'done')),
  tombstone_seq  INTEGER NOT NULL,
  last_hash      TEXT    NOT NULL CHECK (last_hash GLOB 'sha256:*' AND length(last_hash) = 71),
  event_count    INTEGER NOT NULL CHECK (event_count >= 0),
  started_at     TEXT    NOT NULL,
  finished_at    TEXT,
  CHECK ((state = 'done') = (finished_at IS NOT NULL))
) STRICT;

-- ---------------------------------------------------------------------------
-- Append-only enforcement and chain linkage (defense in depth behind the
-- single writer; a violation is a programming error and aborts the tx).
-- ---------------------------------------------------------------------------
CREATE TRIGGER events_link BEFORE INSERT ON events
BEGIN
  SELECT RAISE(ABORT, 'warden: unknown or purged chain')
   WHERE NOT EXISTS (SELECT 1 FROM chains WHERE chain = NEW.chain AND status <> 'purged');
  SELECT RAISE(ABORT, 'warden: prev_hash does not match chain head')
   WHERE NEW.prev_hash <> (SELECT head_hash FROM chains WHERE chain = NEW.chain);
  SELECT RAISE(ABORT, 'warden: seq must increase')
   WHERE NEW.seq <= COALESCE((SELECT max(seq) FROM events), 0);
  SELECT RAISE(ABORT, 'warden: event type not allowed on this chain')
   WHERE NOT EXISTS (
     SELECT 1 FROM event_types t
      WHERE t.type = NEW.type
        AND ((NEW.chain = 'sys' AND t.chains IN ('Y', 'SY'))
          OR (NEW.chain <> 'sys' AND t.chains IN ('S', 'SY'))));
END;

CREATE TRIGGER events_advance_chain AFTER INSERT ON events
BEGIN
  UPDATE chains
     SET head_seq    = NEW.seq,
         head_hash   = NEW.hash,
         event_count = event_count + 1,
         events_since_checkpoint = CASE
           WHEN NEW.type = 'chain.checkpoint'
            AND json_extract(NEW.envelope, '$.payload.chain') = NEW.chain THEN 0
           ELSE events_since_checkpoint + 1 END,
         last_checkpoint_seq = CASE
           WHEN NEW.type = 'chain.checkpoint'
            AND json_extract(NEW.envelope, '$.payload.chain') = NEW.chain THEN NEW.seq
           ELSE last_checkpoint_seq END
   WHERE chain = NEW.chain;
END;

CREATE TRIGGER events_no_update BEFORE UPDATE ON events
BEGIN
  SELECT RAISE(ABORT, 'warden: events are append-only');
END;

CREATE TRIGGER events_no_delete BEFORE DELETE ON events
WHEN NOT EXISTS (SELECT 1 FROM purges p WHERE p.session_id = OLD.chain AND p.state = 'deleting')
BEGIN
  SELECT RAISE(ABORT, 'warden: events are append-only (purge not authorized)');
END;

-- ---------------------------------------------------------------------------
-- Projections
-- ---------------------------------------------------------------------------
CREATE TABLE workspaces (
  id                         TEXT PRIMARY KEY CHECK (id GLOB 'wsp_*' AND length(id) = 30),
  root                       TEXT NOT NULL UNIQUE,  -- canonical absolute path, symlinks resolved; checked in Go (filepath.IsAbs), not by a POSIX-only GLOB (CONFLICTS C-47)
  name                       TEXT NOT NULL,                                 -- basename, for display
  classification             TEXT NOT NULL DEFAULT 'confidential'
                             CHECK (classification IN ('public', 'internal', 'confidential')),
  classification_changed_at  TEXT,
  created_at                 TEXT NOT NULL,
  last_opened_at             TEXT NOT NULL
) STRICT;
CREATE INDEX workspaces_recent ON workspaces (last_opened_at DESC);

CREATE TABLE sessions (
  id                   TEXT PRIMARY KEY CHECK (id GLOB 'ses_*' AND length(id) = 30),
  workspace_id         TEXT NOT NULL REFERENCES workspaces(id),
  workspace_root       TEXT NOT NULL,
  classification       TEXT NOT NULL CHECK (classification IN ('public', 'internal', 'confidential')),
  sandbox_level        TEXT NOT NULL CHECK (sandbox_level IN ('L1', 'L2')),
  sandbox_backend      TEXT NOT NULL CHECK (sandbox_backend IN ('seatbelt', 'bwrap', 'docker')),
  branch               TEXT NOT NULL CHECK (branch GLOB 'warden/*' AND length(branch) = 33),
  base_commit          TEXT NOT NULL CHECK (length(base_commit) IN (40, 64)),
  worktree_path        TEXT NOT NULL,
  gitdir_path          TEXT NOT NULL,
  pin_model            TEXT,
  budget_session_usd   REAL NOT NULL CHECK (budget_session_usd > 0),
  status               TEXT NOT NULL CHECK (status IN ('open', 'closed', 'purged')),
  close_reason         TEXT CHECK (close_reason IS NULL OR close_reason IN ('user', 'retention')),
  created_at           TEXT NOT NULL,
  last_activity_at     TEXT NOT NULL,
  closed_at            TEXT,
  worktree_removed_at  TEXT,
  purged_at            TEXT,
  CHECK ((status = 'open') = (closed_at IS NULL)),
  CHECK ((status = 'purged') = (purged_at IS NOT NULL))
) STRICT;
CREATE INDEX sessions_by_workspace ON sessions (workspace_id, status, last_activity_at DESC);
CREATE INDEX sessions_closed       ON sessions (closed_at) WHERE status = 'closed';

CREATE TABLE workflow_runs (
  id                        TEXT PRIMARY KEY CHECK (id GLOB 'wfr_*' AND length(id) = 30),
  session_id                TEXT NOT NULL REFERENCES sessions(id),
  template                  TEXT NOT NULL CHECK (template IN ('poc-coding', 'poc-readonly')),
  template_version          TEXT NOT NULL,
  kind                      TEXT NOT NULL CHECK (kind IN ('change', 'readonly')),
  status                    TEXT NOT NULL CHECK (status IN ('running', 'waiting', 'succeeded', 'failed', 'cancelled')),
  reason                    TEXT CHECK (reason IS NULL OR reason IN (
                              'deps_met','scheduled','approval_pending','approved','input_needed','input_provided',
                              'output_valid','retry','budget','policy_denied','schema','verification','interrupted',
                              'provider','tool','resource','timeout','cancelled','rejected','approval_expired',
                              'no_admissible_model','upstream_failed')),
  request_text_hash         TEXT NOT NULL CHECK (request_text_hash GLOB 'sha256:*'),
  pin_model                 TEXT,
  interactive               INTEGER NOT NULL DEFAULT 1 CHECK (interactive IN (0, 1)),
  client_request_id         TEXT CHECK (client_request_id IS NULL OR length(client_request_id) BETWEEN 1 AND 64),
  resumed_from_run_id       TEXT REFERENCES workflow_runs(id),
  resumed_from_gate         TEXT CHECK (resumed_from_gate IS NULL OR resumed_from_gate IN ('gate-plan', 'gate-final')),
  final_result_artifact_id  TEXT REFERENCES artifacts(id) DEFERRABLE INITIALLY DEFERRED,
  created_at                TEXT NOT NULL,
  ended_at                  TEXT,
  CHECK ((status IN ('running', 'waiting')) = (ended_at IS NULL)),
  CHECK ((resumed_from_run_id IS NULL) = (resumed_from_gate IS NULL)),
  UNIQUE (session_id, client_request_id)
) STRICT;
CREATE INDEX runs_by_session ON workflow_runs (session_id, created_at);
-- One worktree per session, tasks sequential: at most one active run per session.
CREATE UNIQUE INDEX runs_one_active ON workflow_runs (session_id) WHERE status IN ('running', 'waiting');

CREATE TABLE tasks (
  id                   TEXT PRIMARY KEY CHECK (id GLOB 'tsk_*' AND length(id) = 30),
  run_id               TEXT NOT NULL REFERENCES workflow_runs(id),
  task_key             TEXT NOT NULL CHECK (task_key IN
                         ('plan', 'gate-plan', 'implement', 'verify', 'repair-1', 'verify-2', 'gate-final', 'summarize')),
  kind                 TEXT NOT NULL CHECK (kind IN ('agent', 'approval_gate')),
  agent                TEXT CHECK (agent IS NULL OR agent IN ('coder', 'verifier')),
  agent_version        TEXT,
  agent_digest         TEXT CHECK (agent_digest IS NULL OR agent_digest GLOB 'sha256:*'),
  mode                 TEXT CHECK (mode IS NULL OR mode IN ('plan', 'implement', 'repair', 'summarize')),
  task_class           TEXT CHECK (task_class IS NULL OR task_class IN ('plan', 'implement', 'verify', 'summarize')),
  state                TEXT NOT NULL CHECK (state IN ('created', 'queued', 'running', 'waiting_for_approval',
                         'waiting_for_input', 'succeeded', 'failed', 'cancelled', 'timed_out', 'skipped', 'blocked')),
  reason               TEXT CHECK (reason IS NULL OR reason IN (
                         'deps_met','scheduled','approval_pending','approved','input_needed','input_provided',
                         'output_valid','retry','budget','policy_denied','schema','verification','interrupted',
                         'provider','tool','resource','timeout','cancelled','rejected','approval_expired',
                         'no_admissible_model','upstream_failed')),
  attempts             INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  max_attempts         INTEGER NOT NULL DEFAULT 1 CHECK (max_attempts >= 1),
  depends_on           TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(depends_on) AND json_type(depends_on) = 'array'),
  wall_clock_used_ms   INTEGER NOT NULL DEFAULT 0 CHECK (wall_clock_used_ms >= 0),  -- excludes waiting time (CF-38)
  gate_expires_at      TEXT,
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL,
  CHECK ((kind = 'agent') = (agent IS NOT NULL)),
  CHECK ((kind = 'approval_gate') = (task_key IN ('gate-plan', 'gate-final'))),
  UNIQUE (run_id, task_key)
) STRICT;
CREATE INDEX tasks_open ON tasks (state)
  WHERE state NOT IN ('succeeded', 'failed', 'cancelled', 'skipped', 'blocked');

CREATE TABLE executions (
  id             TEXT PRIMARY KEY CHECK (id GLOB 'exe_*' AND length(id) = 30),
  task_id        TEXT NOT NULL REFERENCES tasks(id),
  attempt        INTEGER NOT NULL CHECK (attempt >= 1),
  sandbox_level  TEXT NOT NULL CHECK (sandbox_level IN ('L1', 'L2')),
  sandbox_id     TEXT CHECK (sandbox_id IS NULL OR (sandbox_id GLOB 'sb_*' AND length(sandbox_id) = 29)),
  harness_id     TEXT,
  model_id       TEXT,
  provider_id    TEXT,
  tier           TEXT CHECK (tier IS NULL OR tier IN ('T0', 'T1', 'T2', 'T3', 'T4')),
  routing_id     TEXT CHECK (routing_id IS NULL OR (routing_id GLOB 'rt_*' AND length(routing_id) = 29)),
  status         TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'cancelled', 'timed_out')),
  reason         TEXT,
  started_at     TEXT NOT NULL,
  ended_at       TEXT,
  CHECK ((status = 'running') = (ended_at IS NULL)),
  UNIQUE (task_id, attempt)
) STRICT;
CREATE INDEX executions_running ON executions (status) WHERE status = 'running';

CREATE TABLE blobs (
  hash        TEXT    PRIMARY KEY CHECK (hash GLOB 'sha256:*' AND length(hash) = 71),
  size_bytes  INTEGER NOT NULL CHECK (size_bytes >= 0),
  created_at  TEXT    NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE blob_refs (
  hash        TEXT NOT NULL REFERENCES blobs(hash),
  owner_kind  TEXT NOT NULL CHECK (owner_kind IN ('artifact', 'artifact_file', 'event_payload', 'tool_output')),
  owner_id    TEXT NOT NULL,             -- art_… | art_…:<path> | evt_… | call_…
  session_id  TEXT,                      -- NULL for sys-chain owners
  PRIMARY KEY (hash, owner_kind, owner_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX blob_refs_session ON blob_refs (session_id);

CREATE TABLE artifacts (
  id              TEXT    PRIMARY KEY CHECK (id GLOB 'art_*' AND length(id) = 30),
  session_id      TEXT    NOT NULL REFERENCES sessions(id),
  run_id          TEXT    REFERENCES workflow_runs(id),
  task_id         TEXT    REFERENCES tasks(id),
  execution_id    TEXT    REFERENCES executions(id),
  type            TEXT    NOT NULL CHECK (type IN ('plan', 'code-diff', 'test-report', 'repo-map', 'final-result', 'checkpoint')),
  schema          TEXT    NOT NULL,
  content_hash    TEXT    NOT NULL REFERENCES blobs(hash),
  size_bytes      INTEGER NOT NULL CHECK (size_bytes >= 0),
  media_type      TEXT    NOT NULL CHECK (media_type IN ('application/json', 'text/x-diff')),
  summary         TEXT    NOT NULL CHECK (length(summary) <= 500),
  partial         INTEGER NOT NULL DEFAULT 0 CHECK (partial IN (0, 1)),
  version         INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
  supersedes      TEXT    REFERENCES artifacts(id),
  classification  TEXT    NOT NULL CHECK (classification IN ('public', 'internal', 'confidential')),
  metadata        TEXT    CHECK (metadata IS NULL OR json_valid(metadata)),
  provenance      TEXT    NOT NULL CHECK (json_valid(provenance)),
  record          TEXT    NOT NULL CHECK (json_valid(record)),   -- full record as returned by artifact.get (§7.1)
  created_at      TEXT    NOT NULL,
  created_by      TEXT    NOT NULL CHECK (created_by IN ('agent', 'runtime', 'user')),
  edited_by       TEXT,
  event_seq       INTEGER NOT NULL UNIQUE,                        -- seq of artifact.created / artifact.edited
  CHECK ((version = 1) = (supersedes IS NULL)),
  CHECK ((type = 'code-diff') = (media_type = 'text/x-diff')),
  CHECK ((type = 'code-diff') = (metadata IS NOT NULL))
) STRICT;
CREATE INDEX artifacts_by_session ON artifacts (session_id, type, created_at);
CREATE INDEX artifacts_by_run     ON artifacts (run_id, type, version);
CREATE INDEX artifacts_by_hash    ON artifacts (content_hash);
CREATE UNIQUE INDEX artifacts_successor ON artifacts (supersedes) WHERE supersedes IS NOT NULL;  -- linear version chain

CREATE TRIGGER artifacts_immutable BEFORE UPDATE ON artifacts
BEGIN
  SELECT RAISE(ABORT, 'warden: artifact records are immutable; create a new version');
END;

-- Provenance edges (WRD-09 §5 provenance.inputs), queryable in both directions.
CREATE TABLE artifact_inputs (
  artifact_id        TEXT NOT NULL REFERENCES artifacts(id),
  input_artifact_id  TEXT NOT NULL REFERENCES artifacts(id),
  role               TEXT NOT NULL CHECK (role IN ('plan', 'test_report', 'code_diff', 'edited_from', 'previous_attempt')),
  PRIMARY KEY (artifact_id, input_artifact_id),
  CHECK (artifact_id <> input_artifact_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX artifact_inputs_reverse ON artifact_inputs (input_artifact_id);

CREATE TABLE approvals (
  id                TEXT PRIMARY KEY CHECK (id GLOB 'apr_*' AND length(id) = 30),
  kind              TEXT NOT NULL CHECK (kind IN ('action', 'gate', 'question')),   -- question: model's approval.request (ID-05)
  session_id        TEXT NOT NULL REFERENCES sessions(id),
  workspace_id      TEXT NOT NULL REFERENCES workspaces(id),
  run_id            TEXT REFERENCES workflow_runs(id),
  task_id           TEXT REFERENCES tasks(id),
  execution_id      TEXT REFERENCES executions(id),
  decision_id       TEXT CHECK (decision_id IS NULL OR (decision_id GLOB 'dec_*' AND length(decision_id) = 30)),
  call_id           TEXT CHECK (call_id IS NULL OR (call_id GLOB 'call_*' AND length(call_id) = 31)),
  pattern           TEXT NOT NULL CHECK (json_valid(pattern)),
  pattern_key       TEXT NOT NULL CHECK (pattern_key GLOB 'sha256:*' AND length(pattern_key) = 71),
  pattern_tool      TEXT GENERATED ALWAYS AS (json_extract(pattern, '$.tool')) VIRTUAL,
  pattern_operation TEXT GENERATED ALWAYS AS (json_extract(pattern, '$.operation')) VIRTUAL,
  risk_class        TEXT NOT NULL CHECK (risk_class IN ('R0', 'R1', 'R2', 'R3', 'R4', 'R5', 'R6')),
  scope_max         TEXT NOT NULL CHECK (scope_max IN ('once', 'task', 'session', 'workspace')),
  scopes_allowed    TEXT NOT NULL CHECK (json_valid(scopes_allowed) AND json_type(scopes_allowed) = 'array'),
  rule_ids          TEXT NOT NULL CHECK (json_valid(rule_ids) AND json_type(rule_ids) = 'array'),
  reason            TEXT NOT NULL,
  display           TEXT NOT NULL CHECK (json_valid(display)),
  status            TEXT NOT NULL CHECK (status IN ('pending', 'granted', 'consumed', 'answered', 'rejected', 'expired', 'cancelled', 'revoked')),
  decision          TEXT CHECK (decision IS NULL OR decision IN ('approve', 'reject', 'expire', 'cancel')),
  scope             TEXT CHECK (scope IS NULL OR scope IN ('once', 'task', 'session', 'workspace')),
  approver          TEXT,
  answer            TEXT CHECK (answer IS NULL OR length(answer) <= 4000),  -- redacted before persistence (ID-05)
  requested_at      TEXT NOT NULL,
  expires_at        TEXT NOT NULL,                  -- pending expiry (24 h inline, gate timeout for gates)
  resolved_at       TEXT,
  grant_expires_at  TEXT,                           -- NULL = bounded by task/session lifetime
  revoked_at        TEXT,
  revoked_by        TEXT,
  requested_seq     INTEGER NOT NULL,
  resolved_seq      INTEGER,
  revoked_seq       INTEGER,
  CHECK ((status = 'pending') = (decision IS NULL)),
  CHECK (status NOT IN ('granted', 'consumed', 'revoked') OR (decision = 'approve' AND scope IS NOT NULL AND kind <> 'question')),
  CHECK ((status = 'answered') = (kind = 'question' AND decision = 'approve')),
  CHECK (answer IS NULL OR status = 'answered'),
  CHECK (kind <> 'question' OR scope IS NULL),
  CHECK (status <> 'rejected'  OR decision = 'reject'),
  CHECK (status <> 'expired'   OR decision = 'expire'),
  CHECK (status <> 'cancelled' OR decision = 'cancel'),
  CHECK ((status = 'revoked') = (revoked_at IS NOT NULL)),
  CHECK (kind <> 'gate' OR (scope_max = 'once' AND call_id IS NULL)),
  CHECK (kind <> 'question' OR (scope_max = 'once' AND risk_class = 'R0')),
  CHECK (risk_class <> 'R5' OR scope_max = 'once'),
  CHECK (scope IS NULL OR decision = 'approve'),
  CHECK (status <> 'revoked' OR scope IN ('task', 'session', 'workspace')),
  CHECK (status <> 'consumed' OR scope = 'once')
) STRICT;
CREATE INDEX approvals_pending ON approvals (session_id, requested_at) WHERE status = 'pending';
CREATE INDEX approvals_expiry  ON approvals (expires_at)                WHERE status = 'pending';
CREATE INDEX approvals_grants  ON approvals (workspace_id, pattern_key, scope) WHERE status = 'granted';
CREATE INDEX approvals_by_task ON approvals (task_id) WHERE task_id IS NOT NULL;

CREATE TABLE deliveries (
  id                 INTEGER PRIMARY KEY,
  run_id             TEXT NOT NULL REFERENCES workflow_runs(id),
  action             TEXT NOT NULL CHECK (action IN ('apply_branch', 'commit', 'push', 'export_patch')),
  status             TEXT NOT NULL CHECK (status IN ('approval_pending', 'running', 'done', 'failed', 'rejected')),
  delivered_seq      INTEGER,          -- seq of workflow.delivered (set when status becomes done)
  client_request_id  TEXT CHECK (client_request_id IS NULL OR length(client_request_id) BETWEEN 1 AND 64),
  approval_id        TEXT REFERENCES approvals(id),
  call_id            TEXT NOT NULL CHECK (call_id GLOB 'call_*' AND length(call_id) = 31),
  branch             TEXT,
  remote             TEXT,
  commit_id          TEXT CHECK (commit_id IS NULL OR length(commit_id) IN (40, 64)),
  patch_path         TEXT,
  error              TEXT,
  created_at         TEXT NOT NULL,
  finished_at        TEXT,
  CHECK ((action = 'push') = (approval_id IS NOT NULL)),
  CHECK ((status = 'done') = (delivered_seq IS NOT NULL)),
  UNIQUE (run_id, client_request_id)
) STRICT;
CREATE INDEX deliveries_by_run ON deliveries (run_id, created_at);

-- Event type registry (design core §5, CF-10).
INSERT INTO event_types (type, chains, spillable, since_migration) VALUES
  ('runtime.start','Y',0,1), ('runtime.stop','Y',0,1), ('policy.reload','Y',1,1),
  ('provider.configured','Y',0,1), ('workspace.classification','Y',0,1), ('session.purged','Y',0,1),
  ('session.open','S',0,1), ('session.resume','S',0,1), ('session.request','S',1,1), ('session.close','S',0,1),
  ('budget.changed','S',0,1),
  ('workflow.start','S',0,1), ('workflow.end','S',0,1), ('workflow.gate.presented','S',0,1), ('workflow.gate.resolved','S',0,1),
  ('workflow.delivered','S',0,1),
  ('task.state','S',0,1),
  ('routing.decision','S',1,1), ('routing.fallback','S',0,1),
  ('model.call.start','S',0,1), ('model.call.end','S',0,1),
  ('context.assembled','S',1,1), ('context.compacted','S',0,1),
  ('policy.decision','S',0,1),
  ('approval.requested','S',0,1), ('approval.resolved','S',0,1), ('approval.revoked','S',0,1),
  ('tool.exec.start','S',0,1), ('tool.exec.end','S',0,1),
  ('sandbox.create','S',1,1), ('sandbox.destroy','S',0,1), ('sandbox.violation','S',0,1),
  ('proxy.connect','S',0,1), ('proxy.denied','S',0,1),
  ('secret.access','SY',0,1), ('redaction','SY',0,1),
  ('artifact.created','S',0,1), ('artifact.edited','S',0,1),
  ('worktree.create','S',0,1), ('worktree.checkpoint','S',0,1), ('worktree.remove','S',0,1),
  ('harness.session.start','S',0,1), ('harness.hook','S',0,1), ('harness.session.end','S',0,1),
  ('chain.checkpoint','SY',0,1);
