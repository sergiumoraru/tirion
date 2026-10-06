package graph

const Schema = `
-- Enable extensions (safe to run multiple times)
-- pg_trgm: Required for trigram/fuzzy search (--mode=trigram)
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- Repositories
CREATE TABLE IF NOT EXISTS repositories (
  id SERIAL PRIMARY KEY,
  name TEXT NOT NULL,
  path TEXT NOT NULL UNIQUE,
  language TEXT,
  framework TEXT,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_repositories_name_unique
ON repositories(name);

-- Workspace-scoped indexing metadata.
-- Repositories remain logical repo identities; workspaces select branch/SHA contexts
-- and repo_snapshots identify indexed facts for a repo at one commit.
CREATE TABLE IF NOT EXISTS workspaces (
  id BIGSERIAL PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  created_by_label TEXT NOT NULL DEFAULT '',
  is_default BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_workspaces_single_default
ON workspaces (is_default)
WHERE is_default;

INSERT INTO workspaces (slug, name, description, is_default)
VALUES (
  'default-main',
  'Default Main',
  'Migrated shared workspace',
  NOT EXISTS (SELECT 1 FROM workspaces WHERE is_default)
)
ON CONFLICT (slug) DO UPDATE SET
  name = COALESCE(NULLIF(workspaces.name, ''), EXCLUDED.name),
  description = COALESCE(NULLIF(workspaces.description, ''), EXCLUDED.description),
  is_default = CASE
    WHEN NOT EXISTS (SELECT 1 FROM workspaces WHERE is_default) THEN TRUE
    ELSE workspaces.is_default
  END,
  updated_at = CURRENT_TIMESTAMP;

CREATE TABLE IF NOT EXISTS repo_snapshots (
  id BIGSERIAL PRIMARY KEY,
  workspace_id BIGINT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  repo_name TEXT NOT NULL,
  branch TEXT NOT NULL DEFAULT '',
  sha TEXT NOT NULL DEFAULT '',
  indexed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  parser_version TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'ok',
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(workspace_id, repo_id, sha)
);

-- Each indexing attempt owns a distinct unpublished generation, including same-SHA retries.
ALTER TABLE repo_snapshots ADD COLUMN IF NOT EXISTS generation TEXT NOT NULL DEFAULT '';
ALTER TABLE repo_snapshots ADD COLUMN IF NOT EXISTS source_path TEXT NOT NULL DEFAULT '';
ALTER TABLE repo_snapshots ADD COLUMN IF NOT EXISTS pipeline_complete BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE repo_snapshots ADD COLUMN IF NOT EXISTS pipeline_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE repo_snapshots DROP CONSTRAINT IF EXISTS repo_snapshots_workspace_id_repo_id_sha_key;
CREATE UNIQUE INDEX IF NOT EXISTS idx_repo_snapshot_generation ON repo_snapshots(workspace_id,repo_id,sha,generation);

-- NULL identifies snapshots produced before metadata identity was recorded.
ALTER TABLE repo_snapshots ADD COLUMN IF NOT EXISTS input_manifest JSONB;
ALTER TABLE repo_snapshots ADD COLUMN IF NOT EXISTS include_manifest JSONB;
ALTER TABLE repo_snapshots ADD COLUMN IF NOT EXISTS ignored_manifest JSONB;
ALTER TABLE repo_snapshots ADD COLUMN IF NOT EXISTS source_clean BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_repo_snapshots_workspace ON repo_snapshots(workspace_id);
CREATE INDEX IF NOT EXISTS idx_repo_snapshots_repo ON repo_snapshots(repo_id);
CREATE INDEX IF NOT EXISTS idx_repo_snapshots_sha ON repo_snapshots(sha);

CREATE TABLE IF NOT EXISTS workspace_repos (
  workspace_id BIGINT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  repo_name TEXT NOT NULL,
  target_ref TEXT NOT NULL DEFAULT '',
  resolved_branch TEXT NOT NULL DEFAULT '',
  resolved_sha TEXT NOT NULL DEFAULT '',
  worktree_path TEXT NOT NULL DEFAULT '',
  active_snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE SET NULL,
  mainline_snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE SET NULL,
  mainline_resolved_sha TEXT NOT NULL DEFAULT '',
  mainline_last_indexed_at TIMESTAMPTZ,
  last_checkout_at TIMESTAMPTZ,
  last_indexed_at TIMESTAMPTZ,
  index_status TEXT NOT NULL DEFAULT 'unknown',
  last_error TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(workspace_id, repo_name)
);

CREATE INDEX IF NOT EXISTS idx_workspace_repos_repo_name ON workspace_repos(repo_name);
CREATE INDEX IF NOT EXISTS idx_workspace_repos_snapshot ON workspace_repos(active_snapshot_id);

CREATE TABLE IF NOT EXISTS repo_workspace_state (
  repo_id INTEGER PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,
  selected_branch TEXT,
  indexed_branch TEXT,
  indexed_sha TEXT,
  indexed_at TIMESTAMPTZ,
  last_operation TEXT,
  last_operation_status TEXT,
  last_operation_at TIMESTAMPTZ,
  last_error TEXT
);

ALTER TABLE repo_workspace_state
  ADD COLUMN IF NOT EXISTS selected_branch TEXT;

CREATE TABLE IF NOT EXISTS verify_runs (
  id BIGSERIAL PRIMARY KEY,
  workspace_id TEXT NOT NULL DEFAULT '',
  repo TEXT NOT NULL DEFAULT '',
  verdict TEXT NOT NULL DEFAULT '',
  breaking_count INTEGER NOT NULL DEFAULT 0,
  endpoint_count INTEGER NOT NULL DEFAULT 0,
  queue_count INTEGER NOT NULL DEFAULT 0,
  entity_count INTEGER NOT NULL DEFAULT 0,
  consumer_repo_count INTEGER NOT NULL DEFAULT 0,
  query_error_count INTEGER NOT NULL DEFAULT 0,
  change_types JSONB NOT NULL DEFAULT '[]'::jsonb,
  reasons JSONB NOT NULL DEFAULT '[]'::jsonb,
  duration_ms BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

ALTER TABLE verify_runs ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT 'estate_impact';
ALTER TABLE verify_runs ADD COLUMN IF NOT EXISTS work_status TEXT NOT NULL DEFAULT '';
ALTER TABLE verify_runs ADD COLUMN IF NOT EXISTS claim_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE verify_runs ADD COLUMN IF NOT EXISTS unaccounted_change_count INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_verify_runs_created_at ON verify_runs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_verify_runs_workspace ON verify_runs(workspace_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_verify_runs_verdict ON verify_runs(verdict, created_at DESC);

-- Seed the legacy shared estate without overwriting later workspace selections.
INSERT INTO workspace_repos (
  workspace_id,
  repo_name,
  target_ref,
  resolved_branch,
  resolved_sha,
  worktree_path,
  last_indexed_at,
  index_status,
  last_error
)
SELECT
  w.id,
  r.name,
  COALESCE(NULLIF(s.selected_branch, ''), NULLIF(s.indexed_branch, ''), ''),
  COALESCE(s.indexed_branch, ''),
  COALESCE(s.indexed_sha, ''),
  r.path,
  s.indexed_at,
  CASE WHEN COALESCE(s.indexed_sha, '') = '' THEN 'unknown' ELSE 'ok' END,
  COALESCE(s.last_error, '')
FROM repositories r
CROSS JOIN workspaces w
LEFT JOIN repo_workspace_state s ON s.repo_id = r.id
WHERE w.slug = 'default-main'
ON CONFLICT (workspace_id, repo_name) DO NOTHING;

INSERT INTO repo_snapshots (
  workspace_id,
  repo_id,
  repo_name,
  branch,
  sha,
  indexed_at,
  status
)
SELECT
  w.id,
  r.id,
  r.name,
  COALESCE(s.indexed_branch, ''),
  COALESCE(s.indexed_sha, ''),
  COALESCE(s.indexed_at, CURRENT_TIMESTAMP),
  'ok'
FROM repositories r
JOIN repo_workspace_state s ON s.repo_id = r.id
CROSS JOIN workspaces w
WHERE w.slug = 'default-main'
  AND COALESCE(s.indexed_sha, '') <> ''
ON CONFLICT (workspace_id, repo_id, sha, generation) DO NOTHING;

UPDATE workspace_repos wr
SET active_snapshot_id = rs.id,
    updated_at = CURRENT_TIMESTAMP
FROM workspaces w
JOIN repositories r ON TRUE
JOIN repo_snapshots rs ON rs.workspace_id = w.id
  AND rs.repo_id = r.id
WHERE wr.workspace_id = w.id
  AND r.name = wr.repo_name
  AND rs.sha = wr.resolved_sha AND rs.generation = ''
  AND w.slug = 'default-main'
  AND COALESCE(wr.resolved_sha, '') <> ''
  AND wr.active_snapshot_id IS NULL;

CREATE TABLE IF NOT EXISTS repo_dependency_edges (
  source_repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  target_repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  edge_type TEXT NOT NULL,
  queue_name TEXT NOT NULL DEFAULT '',
  count INTEGER NOT NULL DEFAULT 0,
  computed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_repo_dependency_edges_unique
ON repo_dependency_edges (source_repo_id, target_repo_id, edge_type, queue_name);

-- Files
CREATE TABLE IF NOT EXISTS files (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE,
  path TEXT NOT NULL,
  path_canonical TEXT,
  language TEXT,
  hash TEXT,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

ALTER TABLE files
  ADD COLUMN IF NOT EXISTS snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE;

-- NULL means the indexed parser has not recorded the Java package yet.
ALTER TABLE files ADD COLUMN IF NOT EXISTS java_package TEXT;

ALTER TABLE files
  DROP CONSTRAINT IF EXISTS files_repo_id_path_key;

CREATE UNIQUE INDEX IF NOT EXISTS idx_files_repo_path_legacy_unique
ON files(repo_id, path)
WHERE snapshot_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_files_snapshot_path_unique
ON files(snapshot_id, path)
WHERE snapshot_id IS NOT NULL;

-- Functions
CREATE TABLE IF NOT EXISTS functions (
  id SERIAL PRIMARY KEY,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  name_canonical TEXT,
  signature_canonical TEXT,
  simple_name TEXT,
  start_line INTEGER NOT NULL,
  end_line INTEGER NOT NULL,
  params JSONB,
  return_type TEXT,
  is_exported BOOLEAN DEFAULT FALSE,
  is_async BOOLEAN DEFAULT FALSE,
  source_code TEXT
);

-- Classes
CREATE TABLE IF NOT EXISTS classes (
  id SERIAL PRIMARY KEY,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  start_line INTEGER NOT NULL,
  end_line INTEGER NOT NULL,
  extends_class TEXT,
  implements JSONB,
  is_exported BOOLEAN DEFAULT FALSE,
  is_enum BOOLEAN DEFAULT FALSE,
  is_abstract BOOLEAN DEFAULT FALSE,
  is_record BOOLEAN DEFAULT FALSE
);

ALTER TABLE classes
  ADD COLUMN IF NOT EXISTS is_enum BOOLEAN DEFAULT FALSE;
ALTER TABLE classes
  ADD COLUMN IF NOT EXISTS is_abstract BOOLEAN DEFAULT FALSE;
ALTER TABLE classes
  ADD COLUMN IF NOT EXISTS is_record BOOLEAN DEFAULT FALSE;

-- Endpoints
CREATE TABLE IF NOT EXISTS endpoints (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  path TEXT NOT NULL,
  path_canonical TEXT,
  method TEXT NOT NULL,
  method_canonical TEXT,
  handler_function_id INTEGER REFERENCES functions(id) ON DELETE SET NULL,
  file_id INTEGER REFERENCES files(id) ON DELETE CASCADE,
  line_number INTEGER
);

-- Deduplicate effective endpoint identity before enforcing uniqueness.
WITH ranked_endpoints AS (
  SELECT
    id,
    ROW_NUMBER() OVER (
      PARTITION BY repo_id, path, method,
                   COALESCE(handler_function_id, 0),
                   COALESCE(file_id, 0),
                   COALESCE(line_number, -1)
      ORDER BY id
    ) AS rn
  FROM endpoints
)
DELETE FROM endpoints e
USING ranked_endpoints re
WHERE e.id = re.id
  AND re.rn > 1;

CREATE UNIQUE INDEX IF NOT EXISTS idx_endpoints_effective_unique
ON endpoints (
  repo_id,
  path,
  method,
  COALESCE(handler_function_id, 0),
  COALESCE(file_id, 0),
  COALESCE(line_number, -1)
);

-- File imports
CREATE TABLE IF NOT EXISTS file_imports (
  id SERIAL PRIMARY KEY,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  imports_file_id INTEGER REFERENCES files(id) ON DELETE SET NULL,
  import_path TEXT NOT NULL,
  import_names JSONB,
  is_default_import BOOLEAN DEFAULT FALSE
);

-- Function calls
CREATE TABLE IF NOT EXISTS function_calls (
  id SERIAL PRIMARY KEY,
  caller_function_id INTEGER NOT NULL REFERENCES functions(id) ON DELETE CASCADE,
  callee_function_id INTEGER REFERENCES functions(id) ON DELETE SET NULL,
  callee_name TEXT NOT NULL,
  line_number INTEGER,
  is_async BOOLEAN DEFAULT FALSE,
  callee_resolution_source TEXT,
  callee_resolution_confidence TEXT,
  unresolved_reason TEXT
);

-- Trace call edges (denormalized for fast trace expansion)
CREATE TABLE IF NOT EXISTS trace_call_edges (
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE,
  caller_function_id INTEGER,
  callee_function_id INTEGER,
  caller_id TEXT NOT NULL,
  callee_id TEXT,
  callee_name TEXT NOT NULL,
  callee_repo TEXT,
  callee_file TEXT,
  callee_func TEXT,
  line_number INTEGER,
  callee_resolution_source TEXT,
  callee_resolution_confidence TEXT,
  unresolved_reason TEXT
);

ALTER TABLE trace_call_edges
  ADD COLUMN IF NOT EXISTS snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE;
ALTER TABLE trace_call_edges
  ADD COLUMN IF NOT EXISTS caller_function_id INTEGER;
ALTER TABLE trace_call_edges
  ADD COLUMN IF NOT EXISTS callee_function_id INTEGER;
ALTER TABLE trace_call_edges
  ADD COLUMN IF NOT EXISTS callee_resolution_source TEXT;
ALTER TABLE trace_call_edges
  ADD COLUMN IF NOT EXISTS callee_resolution_confidence TEXT;
ALTER TABLE trace_call_edges
  ADD COLUMN IF NOT EXISTS unresolved_reason TEXT;

ALTER TABLE function_calls
  ADD COLUMN IF NOT EXISTS callee_resolution_source TEXT;
ALTER TABLE function_calls
  ADD COLUMN IF NOT EXISTS callee_resolution_confidence TEXT;
ALTER TABLE function_calls
  ADD COLUMN IF NOT EXISTS unresolved_reason TEXT;
ALTER TABLE function_calls
  ADD COLUMN IF NOT EXISTS is_callback_argument BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE functions
  ADD COLUMN IF NOT EXISTS simple_name TEXT;
ALTER TABLE files
  ADD COLUMN IF NOT EXISTS path_canonical TEXT;
ALTER TABLE functions
  ADD COLUMN IF NOT EXISTS name_canonical TEXT;
ALTER TABLE functions
  ADD COLUMN IF NOT EXISTS signature_canonical TEXT;
ALTER TABLE endpoints
  ADD COLUMN IF NOT EXISTS path_canonical TEXT;
ALTER TABLE endpoints
  ADD COLUMN IF NOT EXISTS method_canonical TEXT;

-- Backfill canonical identity columns for pre-existing rows.
UPDATE files
SET path_canonical = lower(trim(BOTH '/' FROM regexp_replace(replace(trim(path), '\', '/'), '/+', '/', 'g')))
WHERE path_canonical IS NULL OR path_canonical = '';

UPDATE files
SET path_canonical = regexp_replace(path_canonical, '^\.codebase-snapshots/[^/]+/', '')
WHERE path_canonical ~ '^\.codebase-snapshots/[^/]+/';

UPDATE files
SET path_canonical = lower(trim(path))
WHERE path_canonical IS NULL OR path_canonical = '';

UPDATE functions
SET name_canonical = regexp_replace(trim(name), '[[:space:]]+', ' ', 'g')
WHERE name_canonical IS NULL OR name_canonical = '';

UPDATE functions
SET signature_canonical = name_canonical
WHERE signature_canonical IS NULL OR signature_canonical = '';

UPDATE endpoints
SET method_canonical = upper(trim(method))
WHERE method_canonical IS NULL OR method_canonical = '';

UPDATE endpoints
SET path_canonical = lower(
  CASE
    WHEN trim(path) = '' THEN '/'
    ELSE
      CASE
        WHEN left(regexp_replace(replace(trim(path), '\', '/'), '/+', '/', 'g'), 1) = '/' THEN regexp_replace(replace(trim(path), '\', '/'), '/+', '/', 'g')
        ELSE '/' || regexp_replace(replace(trim(path), '\', '/'), '/+', '/', 'g')
      END
  END
)
WHERE path_canonical IS NULL OR path_canonical = '';

UPDATE endpoints
SET path_canonical = CASE
  WHEN path_canonical = '/' THEN '/'
  ELSE regexp_replace(path_canonical, '/+$', '')
END
WHERE path_canonical IS NOT NULL;

-- Canonical dedupe before canonical uniqueness enforcement.
-- Repoint dependent rows to deterministic survivors before deleting duplicates.
WITH function_dupes AS (
  SELECT
    id AS duplicate_id,
    MIN(id) OVER (
      PARTITION BY file_id, name_canonical, start_line, end_line
    ) AS survivor_id,
    ROW_NUMBER() OVER (
      PARTITION BY file_id, name_canonical, start_line, end_line
      ORDER BY id
    ) AS rn
  FROM functions
)
UPDATE endpoints e
SET handler_function_id = d.survivor_id
FROM function_dupes d
WHERE d.rn > 1
  AND e.handler_function_id = d.duplicate_id
  AND e.handler_function_id <> d.survivor_id;

WITH function_dupes AS (
  SELECT
    id AS duplicate_id,
    MIN(id) OVER (
      PARTITION BY file_id, name_canonical, start_line, end_line
    ) AS survivor_id,
    ROW_NUMBER() OVER (
      PARTITION BY file_id, name_canonical, start_line, end_line
      ORDER BY id
    ) AS rn
  FROM functions
)
UPDATE function_calls fc
SET caller_function_id = d.survivor_id
FROM function_dupes d
WHERE d.rn > 1
  AND fc.caller_function_id = d.duplicate_id
  AND fc.caller_function_id <> d.survivor_id;

WITH function_dupes AS (
  SELECT
    id AS duplicate_id,
    MIN(id) OVER (
      PARTITION BY file_id, name_canonical, start_line, end_line
    ) AS survivor_id,
    ROW_NUMBER() OVER (
      PARTITION BY file_id, name_canonical, start_line, end_line
      ORDER BY id
    ) AS rn
  FROM functions
)
UPDATE function_calls fc
SET callee_function_id = d.survivor_id
FROM function_dupes d
WHERE d.rn > 1
  AND fc.callee_function_id = d.duplicate_id
  AND fc.callee_function_id <> d.survivor_id;

WITH function_dupes AS (
  SELECT
    id AS duplicate_id,
    MIN(id) OVER (
      PARTITION BY file_id, name_canonical, start_line, end_line
    ) AS survivor_id,
    ROW_NUMBER() OVER (
      PARTITION BY file_id, name_canonical, start_line, end_line
      ORDER BY id
    ) AS rn
  FROM functions
)
UPDATE trace_call_edges t
SET caller_function_id = d.survivor_id
FROM function_dupes d
WHERE d.rn > 1
  AND t.caller_function_id = d.duplicate_id
  AND t.caller_function_id <> d.survivor_id;

WITH function_dupes AS (
  SELECT
    id AS duplicate_id,
    MIN(id) OVER (
      PARTITION BY file_id, name_canonical, start_line, end_line
    ) AS survivor_id,
    ROW_NUMBER() OVER (
      PARTITION BY file_id, name_canonical, start_line, end_line
      ORDER BY id
    ) AS rn
  FROM functions
)
UPDATE trace_call_edges t
SET callee_function_id = d.survivor_id
FROM function_dupes d
WHERE d.rn > 1
  AND t.callee_function_id = d.duplicate_id
  AND t.callee_function_id <> d.survivor_id;

WITH function_dupes AS (
  SELECT
    id,
    ROW_NUMBER() OVER (
      PARTITION BY file_id, name_canonical, start_line, end_line
      ORDER BY id
    ) AS rn
  FROM functions
)
DELETE FROM functions f
USING function_dupes d
WHERE d.rn > 1
  AND f.id = d.id;

WITH ranked_endpoints_canonical AS (
  SELECT
    id,
    ROW_NUMBER() OVER (
      PARTITION BY repo_id, path_canonical, method_canonical,
                   COALESCE(handler_function_id, 0),
                   COALESCE(file_id, 0),
                   COALESCE(line_number, -1)
      ORDER BY id
    ) AS rn
  FROM endpoints
)
DELETE FROM endpoints e
USING ranked_endpoints_canonical re
WHERE e.id = re.id
  AND re.rn > 1;

CREATE UNIQUE INDEX IF NOT EXISTS idx_functions_effective_unique
ON functions (file_id, name_canonical, start_line, end_line);

CREATE UNIQUE INDEX IF NOT EXISTS idx_endpoints_effective_unique_canonical
ON endpoints (
  repo_id,
  path_canonical,
  method_canonical,
  COALESCE(handler_function_id, 0),
  COALESCE(file_id, 0),
  COALESCE(line_number, -1)
);

-- Pending graph edges (for bulk edge creation)
CREATE TABLE IF NOT EXISTS pending_contains_edges (
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE,
  file_id TEXT NOT NULL,
  entity_id TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS pending_calls_edges (
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE,
  caller_id TEXT NOT NULL,
  callee_name TEXT NOT NULL,
  line_number INTEGER
);

CREATE TABLE IF NOT EXISTS pending_imports_edges (
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE,
  file_id TEXT NOT NULL,
  import_path TEXT NOT NULL
);

-- HTTP client calls (for cross-service tracing)
CREATE TABLE IF NOT EXISTS http_client_calls (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE,
  caller_id TEXT NOT NULL,          -- repo:file:function format
  http_method TEXT NOT NULL,        -- GET, POST, PUT, DELETE, etc.
  url_pattern TEXT NOT NULL,        -- /api/pages/:id or /api/users
  line_number INTEGER,
  client_type TEXT                  -- axios, fetch, HttpClient, RestTemplate, etc.
);

-- GraphQL document operations
CREATE TABLE IF NOT EXISTS graphql_operations (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  operation_name TEXT NOT NULL,
  operation_type TEXT NOT NULL,     -- query, mutation, subscription
  line_number INTEGER,
  UNIQUE(repo_id, file_id, operation_name, operation_type, line_number)
);

CREATE TABLE IF NOT EXISTS graphql_operation_usages (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  import_path TEXT NOT NULL,
  imported_as TEXT NOT NULL,
  caller_function TEXT NOT NULL,
  line_number INTEGER,
  UNIQUE(repo_id, file_id, import_path, imported_as, caller_function, line_number)
);

CREATE TABLE IF NOT EXISTS graphql_backend_entrypoints (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  handler_name TEXT NOT NULL DEFAULT '',
  registration_kind TEXT NOT NULL,
  controllers_path TEXT NOT NULL DEFAULT '',
  line_number INTEGER,
  UNIQUE(repo_id, file_id, handler_name, registration_kind, controllers_path, line_number)
);

CREATE TABLE IF NOT EXISTS graphql_backend_controller_links (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  entrypoint_id INTEGER NOT NULL REFERENCES graphql_backend_entrypoints(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  resolution_confidence TEXT NOT NULL,
  UNIQUE(entrypoint_id, file_id)
);

CREATE TABLE IF NOT EXISTS graphql_operation_resolvers (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  operation_name TEXT NOT NULL,
  operation_type TEXT NOT NULL,
  resolver_name TEXT NOT NULL DEFAULT '',
  line_number INTEGER,
  UNIQUE(repo_id, file_id, operation_name, operation_type, resolver_name, line_number)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_graphql_operation_resolvers_unique
  ON graphql_operation_resolvers(repo_id, file_id, operation_name, operation_type, resolver_name, COALESCE(line_number, -1));

CREATE TABLE IF NOT EXISTS graphql_operation_permissions (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  operation_name TEXT NOT NULL,
  operation_type TEXT NOT NULL,
  rule_expression TEXT NOT NULL DEFAULT '',
  line_number INTEGER,
  UNIQUE(repo_id, file_id, operation_name, operation_type, rule_expression, line_number)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_graphql_operation_permissions_unique
  ON graphql_operation_permissions(repo_id, file_id, operation_name, operation_type, rule_expression, COALESCE(line_number, -1));

CREATE TABLE IF NOT EXISTS graphql_usage_operation_links (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  usage_id INTEGER NOT NULL REFERENCES graphql_operation_usages(id) ON DELETE CASCADE,
  operation_id INTEGER NOT NULL REFERENCES graphql_operations(id) ON DELETE CASCADE,
  resolution_confidence TEXT NOT NULL,
  UNIQUE(usage_id, operation_id)
);

CREATE TABLE IF NOT EXISTS azure_host_configs (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  route_prefix TEXT NOT NULL DEFAULT 'api',
  line_number INTEGER,
  UNIQUE(repo_id, file_id)
);

-- Azure Functions triggers parsed from function.json
CREATE TABLE IF NOT EXISTS azure_function_triggers (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  function_name TEXT NOT NULL,
  trigger_type TEXT NOT NULL,
  direction TEXT,
  binding_name TEXT NOT NULL DEFAULT '',
  route TEXT,
  http_methods JSONB,
  auth_level TEXT,
  connection_name TEXT,
  schedule_expression TEXT,
  resource_name TEXT NOT NULL DEFAULT '',
  script_file TEXT,
  line_number INTEGER,
  UNIQUE(repo_id, file_id, function_name, trigger_type, binding_name, resource_name, line_number)
);

ALTER TABLE azure_function_triggers
  ADD COLUMN IF NOT EXISTS auth_level TEXT;

ALTER TABLE azure_function_triggers
  ADD COLUMN IF NOT EXISTS connection_name TEXT;

CREATE TABLE IF NOT EXISTS gateway_routes (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  gateway_type TEXT NOT NULL,
  api_name TEXT NOT NULL DEFAULT '',
  operation_name TEXT NOT NULL DEFAULT '',
  public_method TEXT NOT NULL,
  public_path TEXT NOT NULL,
  backend_method TEXT NOT NULL DEFAULT '',
  backend_url TEXT NOT NULL DEFAULT '',
  backend_path TEXT NOT NULL DEFAULT '',
  backend_id TEXT NOT NULL DEFAULT '',
  line_number INTEGER,
  UNIQUE(repo_id, file_id, gateway_type, api_name, operation_name, public_method, public_path)
);

-- SQS queue producers (sendMessage calls)
CREATE TABLE IF NOT EXISTS sqs_producers (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE,
  caller_id TEXT NOT NULL,          -- repo:file:Class.method format
  queue_name TEXT NOT NULL,         -- queue name or configuration reference
  line_number INTEGER
);

-- SQS queue consumers
CREATE TABLE IF NOT EXISTS sqs_consumers (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE,
  consumer_id TEXT NOT NULL,        -- repo:file:ConsumerClass format
  queue_name TEXT NOT NULL,
  handler_method TEXT               -- parsed consumer handler method
);

-- Runtime resource aliases from config/env JSON, e.g. TASK_QUEUE -> task-queue.
CREATE TABLE IF NOT EXISTS resource_aliases (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  alias_key TEXT NOT NULL,
  alias_value TEXT NOT NULL,
  alias_kind TEXT NOT NULL DEFAULT 'config',
  line_number INTEGER
);

-- EventBridge schedules (AWS console / infrastructure-level cron rules)
CREATE TABLE IF NOT EXISTS eventbridge_schedules (
  id SERIAL PRIMARY KEY,
  rule_name TEXT NOT NULL,
  schedule_expression TEXT NOT NULL,
  target_type TEXT NOT NULL,
  target_name TEXT NOT NULL,
  description TEXT,
  state TEXT DEFAULT 'ENABLED',
  source TEXT DEFAULT 'eventbridge',
  UNIQUE(rule_name, target_name)
);

-- ==========================================
-- PHASE 1: Full Java Code Intelligence Tables
-- ==========================================

-- Interfaces (separate from classes for proper OOP support)
CREATE TABLE IF NOT EXISTS interfaces (
  id SERIAL PRIMARY KEY,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  start_line INTEGER NOT NULL,
  end_line INTEGER NOT NULL,
  extends_interfaces JSONB,         -- ["Comparable", "Serializable"]
  is_functional BOOLEAN DEFAULT FALSE,
  is_exported BOOLEAN DEFAULT FALSE,
  UNIQUE(file_id, name)
);

ALTER TABLE interfaces
  ADD COLUMN IF NOT EXISTS end_line INTEGER;
ALTER TABLE interfaces
  ADD COLUMN IF NOT EXISTS is_functional BOOLEAN DEFAULT FALSE;

-- TypeScript type aliases
CREATE TABLE IF NOT EXISTS type_aliases (
  id SERIAL PRIMARY KEY,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  definition TEXT,
  type_params JSONB,
  is_exported BOOLEAN DEFAULT FALSE,
  start_line INTEGER,
  UNIQUE(file_id, name)
);

-- React/Vue/JS hook calls discovered by the TS/JS extractor
CREATE TABLE IF NOT EXISTS hook_calls (
  id SERIAL PRIMARY KEY,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  function_name TEXT,
  hook_name TEXT NOT NULL,
  dependencies JSONB,
  initial_value TEXT,
  line_number INTEGER,
  is_custom BOOLEAN DEFAULT FALSE,
  origin TEXT NOT NULL DEFAULT 'custom'
);
ALTER TABLE hook_calls ADD COLUMN IF NOT EXISTS origin TEXT NOT NULL DEFAULT 'custom';
CREATE UNIQUE INDEX IF NOT EXISTS idx_hook_calls_unique
  ON hook_calls(file_id, hook_name, COALESCE(function_name, ''), COALESCE(line_number, 0));

-- Vue component public contracts declared by <script setup> macros.
CREATE TABLE IF NOT EXISTS vue_component_contracts (
  id SERIAL PRIMARY KEY,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,                 -- props, emits, expose, model
  field_name TEXT NOT NULL,
  field_type TEXT,
  is_required BOOLEAN DEFAULT FALSE,
  line_number INTEGER,
  definition TEXT,
  UNIQUE(file_id, kind, field_name)
);

-- Pinia store definitions declared with defineStore.
CREATE TABLE IF NOT EXISTS pinia_stores (
  id SERIAL PRIMARY KEY,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  store_id TEXT NOT NULL,
  line_number INTEGER,
  state_fields JSONB,
  getter_names JSONB,
  action_names JSONB,
  UNIQUE(file_id, store_id)
);

-- Interface implementations (class_id implements interface_id)
CREATE TABLE IF NOT EXISTS implementations (
  id SERIAL PRIMARY KEY,
  class_id INTEGER NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
  interface_name TEXT NOT NULL,     -- Interface name (may not be in our DB)
  interface_id INTEGER REFERENCES interfaces(id) ON DELETE SET NULL,  -- NULL if interface not indexed
  UNIQUE(class_id, interface_name)
);

-- Trace interface implementations (denormalized for fast DI resolution)
CREATE TABLE IF NOT EXISTS trace_interface_impls (
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE,
  interface_name TEXT NOT NULL,
  class_id INTEGER,
  class_name TEXT NOT NULL,
  class_repo TEXT,
  class_file TEXT
);

ALTER TABLE pending_contains_edges
  ADD COLUMN IF NOT EXISTS snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE;
ALTER TABLE pending_calls_edges
  ADD COLUMN IF NOT EXISTS snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE;
ALTER TABLE pending_imports_edges
  ADD COLUMN IF NOT EXISTS snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE;
ALTER TABLE http_client_calls
  ADD COLUMN IF NOT EXISTS snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE;
ALTER TABLE sqs_producers
  ADD COLUMN IF NOT EXISTS snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE;
ALTER TABLE sqs_consumers
  ADD COLUMN IF NOT EXISTS snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE;
ALTER TABLE resource_aliases
  ADD COLUMN IF NOT EXISTS snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE;
ALTER TABLE trace_interface_impls
  ADD COLUMN IF NOT EXISTS snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE;

ALTER TABLE pending_contains_edges DROP CONSTRAINT IF EXISTS pending_contains_edges_file_id_entity_id_key;
ALTER TABLE pending_calls_edges DROP CONSTRAINT IF EXISTS pending_calls_edges_caller_id_callee_name_line_number_key;
ALTER TABLE pending_imports_edges DROP CONSTRAINT IF EXISTS pending_imports_edges_file_id_import_path_key;
ALTER TABLE http_client_calls DROP CONSTRAINT IF EXISTS http_client_calls_caller_id_url_pattern_line_number_key;
ALTER TABLE sqs_producers DROP CONSTRAINT IF EXISTS sqs_producers_caller_id_queue_name_line_number_key;
ALTER TABLE sqs_consumers DROP CONSTRAINT IF EXISTS sqs_consumers_consumer_id_queue_name_key;

CREATE UNIQUE INDEX IF NOT EXISTS idx_pending_contains_legacy_unique
ON pending_contains_edges(file_id, entity_id)
WHERE snapshot_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_pending_contains_snapshot_unique
ON pending_contains_edges(snapshot_id, file_id, entity_id)
WHERE snapshot_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_pending_calls_legacy_unique
ON pending_calls_edges(caller_id, callee_name, line_number)
WHERE snapshot_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_pending_calls_snapshot_unique
ON pending_calls_edges(snapshot_id, caller_id, callee_name, line_number)
WHERE snapshot_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_pending_imports_legacy_unique
ON pending_imports_edges(file_id, import_path)
WHERE snapshot_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_pending_imports_snapshot_unique
ON pending_imports_edges(snapshot_id, file_id, import_path)
WHERE snapshot_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_http_client_calls_legacy_unique
ON http_client_calls(caller_id, url_pattern, line_number)
WHERE snapshot_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_http_client_calls_snapshot_unique
ON http_client_calls(snapshot_id, caller_id, url_pattern, line_number)
WHERE snapshot_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_sqs_producers_legacy_unique
ON sqs_producers(caller_id, queue_name, line_number)
WHERE snapshot_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_sqs_producers_snapshot_unique
ON sqs_producers(snapshot_id, caller_id, queue_name, line_number)
WHERE snapshot_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_sqs_consumers_legacy_unique
ON sqs_consumers(consumer_id, queue_name)
WHERE snapshot_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_sqs_consumers_snapshot_unique
ON sqs_consumers(snapshot_id, consumer_id, queue_name)
WHERE snapshot_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_aliases_legacy_unique
ON resource_aliases(file_id, alias_key, alias_value, alias_kind)
WHERE snapshot_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_aliases_snapshot_unique
ON resource_aliases(snapshot_id, file_id, alias_key, alias_value, alias_kind)
WHERE snapshot_id IS NOT NULL;

-- Class/interface fields (properties)
CREATE TABLE IF NOT EXISTS fields (
  id SERIAL PRIMARY KEY,
  class_id INTEGER REFERENCES classes(id) ON DELETE CASCADE,
  interface_id INTEGER REFERENCES interfaces(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  field_type TEXT NOT NULL,
  type_parameters JSONB,            -- ["String", "Integer"] for Map<String, Integer>
  modifiers JSONB,                  -- ["private", "final", "static"]
  start_line INTEGER,
  is_injected BOOLEAN DEFAULT FALSE,
  injection_type TEXT,              -- "autowired", "inject", "value", "resource"
  annotations JSONB,
  CHECK (class_id IS NOT NULL OR interface_id IS NOT NULL)
);

-- Annotations (generic, reusable for any entity)
CREATE INDEX IF NOT EXISTS idx_fields_class_location ON fields(class_id, name, start_line)
WHERE class_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_fields_interface_location ON fields(interface_id, name, start_line)
WHERE interface_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS annotations (
  id SERIAL PRIMARY KEY,
  entity_type TEXT NOT NULL,        -- "class", "interface", "method", "field", "parameter"
  entity_id INTEGER NOT NULL,       -- ID in the respective table
  name TEXT NOT NULL,               -- "GetMapping", "Autowired", "Override"
  values JSONB,                     -- {"value": "/api/users", "method": "GET"}
  line_number INTEGER
);

-- Method signatures (for overload resolution)
CREATE TABLE IF NOT EXISTS method_signatures (
  id SERIAL PRIMARY KEY,
  function_id INTEGER NOT NULL REFERENCES functions(id) ON DELETE CASCADE,
  signature TEXT NOT NULL,          -- "processOrder(Order,User)"
  parameter_types JSONB,            -- ["Order", "User"]
  return_type TEXT,
  is_override BOOLEAN DEFAULT FALSE,
  overrides_method_id INTEGER REFERENCES functions(id) ON DELETE SET NULL,
  UNIQUE(function_id)
);

-- Constructor parameters (for DI tracking)
CREATE TABLE IF NOT EXISTS constructor_params (
  id SERIAL PRIMARY KEY,
  class_id INTEGER NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
  param_name TEXT NOT NULL,
  param_type TEXT NOT NULL,
  param_index INTEGER NOT NULL,
  is_injected BOOLEAN DEFAULT FALSE,
  annotation TEXT,                  -- source annotation name
  annotation_value TEXT,
  line_number INTEGER
);

-- Type parameters (generics on classes/interfaces/methods)
CREATE TABLE IF NOT EXISTS type_parameters (
  id SERIAL PRIMARY KEY,
  entity_type TEXT NOT NULL,        -- "class", "interface", "method"
  entity_id INTEGER NOT NULL,
  param_name TEXT NOT NULL,         -- "T", "K", "V"
  param_index INTEGER NOT NULL,
  bounds JSONB,                     -- ["Comparable", "Serializable"]
  bound_type TEXT                   -- "extends", "super"
);

-- HTTP interface methods (Retrofit, Feign)
CREATE TABLE IF NOT EXISTS http_interface_methods (
  id SERIAL PRIMARY KEY,
  interface_id INTEGER NOT NULL REFERENCES interfaces(id) ON DELETE CASCADE,
  method_name TEXT NOT NULL,
  function_id INTEGER REFERENCES functions(id) ON DELETE CASCADE,
  http_method TEXT NOT NULL,        -- GET, POST, PUT, DELETE
  url_pattern TEXT NOT NULL,
  line_number INTEGER,
  UNIQUE(interface_id, method_name)
);

-- Spring @Bean definitions
CREATE TABLE IF NOT EXISTS bean_definitions (
  id SERIAL PRIMARY KEY,
  config_class_id INTEGER NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
  method_id INTEGER REFERENCES functions(id) ON DELETE CASCADE,
  bean_name TEXT NOT NULL,          -- Bean name (method name or @Bean("name"))
  bean_type TEXT NOT NULL,          -- Return type of the @Bean method
  qualifiers JSONB,                 -- ["primary", "qualifier1"]
  is_primary BOOLEAN DEFAULT FALSE,
  line_number INTEGER
);

-- Spring @EventListener methods
CREATE TABLE IF NOT EXISTS event_listeners (
  id SERIAL PRIMARY KEY,
  class_id INTEGER NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
  method_id INTEGER REFERENCES functions(id) ON DELETE CASCADE,
  method_name TEXT NOT NULL,
  event_types JSONB,                -- ["OrderCreatedEvent", "PaymentEvent"]
  condition TEXT,                   -- SpEL condition from @EventListener(condition="...")
  is_async BOOLEAN DEFAULT FALSE,   -- @Async annotation present
  line_number INTEGER
);

-- Spring @Scheduled methods
CREATE TABLE IF NOT EXISTS scheduled_methods (
  id SERIAL PRIMARY KEY,
  class_id INTEGER NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
  method_id INTEGER REFERENCES functions(id) ON DELETE CASCADE,
  method_name TEXT NOT NULL,
  cron TEXT,                        -- Cron expression
  fixed_rate INTEGER,               -- Fixed rate in ms
  fixed_delay INTEGER,              -- Fixed delay in ms
  initial_delay INTEGER,            -- Initial delay in ms
  line_number INTEGER
);

-- Migrate repo_id into pending/http/sqs tables for fast repo cleanup
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'pending_contains_edges' AND column_name = 'repo_id'
  ) THEN
    ALTER TABLE pending_contains_edges ADD COLUMN repo_id INTEGER;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'pending_calls_edges' AND column_name = 'repo_id'
  ) THEN
    ALTER TABLE pending_calls_edges ADD COLUMN repo_id INTEGER;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'pending_imports_edges' AND column_name = 'repo_id'
  ) THEN
    ALTER TABLE pending_imports_edges ADD COLUMN repo_id INTEGER;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'http_client_calls' AND column_name = 'repo_id'
  ) THEN
    ALTER TABLE http_client_calls ADD COLUMN repo_id INTEGER;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'sqs_producers' AND column_name = 'repo_id'
  ) THEN
    ALTER TABLE sqs_producers ADD COLUMN repo_id INTEGER;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'sqs_consumers' AND column_name = 'repo_id'
  ) THEN
    ALTER TABLE sqs_consumers ADD COLUMN repo_id INTEGER;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'workspace_repos' AND column_name = 'mainline_snapshot_id'
  ) THEN
    ALTER TABLE workspace_repos ADD COLUMN mainline_snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE SET NULL;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'workspace_repos' AND column_name = 'mainline_resolved_sha'
  ) THEN
    ALTER TABLE workspace_repos ADD COLUMN mainline_resolved_sha TEXT NOT NULL DEFAULT '';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'workspace_repos' AND column_name = 'mainline_last_indexed_at'
  ) THEN
    ALTER TABLE workspace_repos ADD COLUMN mainline_last_indexed_at TIMESTAMPTZ;
  END IF;
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'workspace_repos' AND column_name = 'mainline_snapshot_id'
  ) THEN
    CREATE INDEX IF NOT EXISTS idx_workspace_repos_mainline_snapshot ON workspace_repos(mainline_snapshot_id);
  END IF;
END $$;

-- Ensure trace_interface_impls has class_id column after schema changes
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.tables
    WHERE table_name = 'trace_interface_impls'
  ) THEN
    IF NOT EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_name = 'trace_interface_impls' AND column_name = 'class_id'
    ) THEN
      ALTER TABLE trace_interface_impls ADD COLUMN class_id INTEGER;
    END IF;
  END IF;
END $$;

UPDATE pending_contains_edges p
SET repo_id = r.id
FROM repositories r
WHERE p.repo_id IS NULL
  AND split_part(p.file_id, ':', 1) = r.name;

UPDATE pending_calls_edges p
SET repo_id = r.id
FROM repositories r
WHERE p.repo_id IS NULL
  AND split_part(p.caller_id, ':', 1) = r.name;

UPDATE pending_imports_edges p
SET repo_id = r.id
FROM repositories r
WHERE p.repo_id IS NULL
  AND split_part(p.file_id, ':', 1) = r.name;

UPDATE http_client_calls h
SET repo_id = r.id
FROM repositories r
WHERE h.repo_id IS NULL
  AND split_part(h.caller_id, ':', 1) = r.name;

UPDATE sqs_producers s
SET repo_id = r.id
FROM repositories r
WHERE s.repo_id IS NULL
  AND split_part(s.caller_id, ':', 1) = r.name;

UPDATE sqs_consumers s
SET repo_id = r.id
FROM repositories r
WHERE s.repo_id IS NULL
  AND split_part(s.consumer_id, ':', 1) = r.name;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'pending_contains_edges_repo_id_fkey'
  ) THEN
    ALTER TABLE pending_contains_edges
      ADD CONSTRAINT pending_contains_edges_repo_id_fkey
      FOREIGN KEY (repo_id) REFERENCES repositories(id) ON DELETE CASCADE;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'pending_calls_edges_repo_id_fkey'
  ) THEN
    ALTER TABLE pending_calls_edges
      ADD CONSTRAINT pending_calls_edges_repo_id_fkey
      FOREIGN KEY (repo_id) REFERENCES repositories(id) ON DELETE CASCADE;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'pending_imports_edges_repo_id_fkey'
  ) THEN
    ALTER TABLE pending_imports_edges
      ADD CONSTRAINT pending_imports_edges_repo_id_fkey
      FOREIGN KEY (repo_id) REFERENCES repositories(id) ON DELETE CASCADE;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'http_client_calls_repo_id_fkey'
  ) THEN
    ALTER TABLE http_client_calls
      ADD CONSTRAINT http_client_calls_repo_id_fkey
      FOREIGN KEY (repo_id) REFERENCES repositories(id) ON DELETE CASCADE;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'sqs_producers_repo_id_fkey'
  ) THEN
    ALTER TABLE sqs_producers
      ADD CONSTRAINT sqs_producers_repo_id_fkey
      FOREIGN KEY (repo_id) REFERENCES repositories(id) ON DELETE CASCADE;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'sqs_consumers_repo_id_fkey'
  ) THEN
    ALTER TABLE sqs_consumers
      ADD CONSTRAINT sqs_consumers_repo_id_fkey
      FOREIGN KEY (repo_id) REFERENCES repositories(id) ON DELETE CASCADE;
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pending_contains_edges WHERE repo_id IS NULL) THEN
    ALTER TABLE pending_contains_edges ALTER COLUMN repo_id SET NOT NULL;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pending_calls_edges WHERE repo_id IS NULL) THEN
    ALTER TABLE pending_calls_edges ALTER COLUMN repo_id SET NOT NULL;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pending_imports_edges WHERE repo_id IS NULL) THEN
    ALTER TABLE pending_imports_edges ALTER COLUMN repo_id SET NOT NULL;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM http_client_calls WHERE repo_id IS NULL) THEN
    ALTER TABLE http_client_calls ALTER COLUMN repo_id SET NOT NULL;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM sqs_producers WHERE repo_id IS NULL) THEN
    ALTER TABLE sqs_producers ALTER COLUMN repo_id SET NOT NULL;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM sqs_consumers WHERE repo_id IS NULL) THEN
    ALTER TABLE sqs_consumers ALTER COLUMN repo_id SET NOT NULL;
  END IF;
END $$;

-- Create indexes
CREATE INDEX IF NOT EXISTS idx_files_repo ON files(repo_id);
CREATE INDEX IF NOT EXISTS idx_files_snapshot ON files(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_files_repo_snapshot ON files(repo_id, snapshot_id);

-- ==========================================
-- FUZZY SEARCH: Trigram indexes (pg_trgm)
-- ==========================================
-- Note: Requires pg_trgm extension to be enabled:
--   CREATE EXTENSION IF NOT EXISTS pg_trgm;
-- These indexes enable fuzzy/typo-tolerant search
CREATE INDEX IF NOT EXISTS idx_functions_name_trgm ON functions USING gin (name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_classes_name_trgm ON classes USING gin (name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_function_calls_callee_trgm ON function_calls USING gin (callee_name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_annotations_entity ON annotations(entity_type, entity_id);
CREATE INDEX IF NOT EXISTS idx_type_parameters_entity ON type_parameters(entity_type, entity_id);

CREATE INDEX IF NOT EXISTS idx_functions_file ON functions(file_id);
CREATE INDEX IF NOT EXISTS idx_functions_name ON functions(name);
CREATE INDEX IF NOT EXISTS idx_functions_simple_name ON functions(simple_name);
CREATE INDEX IF NOT EXISTS idx_classes_name ON classes(name);
CREATE INDEX IF NOT EXISTS idx_classes_file ON classes(file_id);
CREATE INDEX IF NOT EXISTS idx_classes_enum ON classes(is_enum);
CREATE INDEX IF NOT EXISTS idx_type_aliases_name ON type_aliases(name);
CREATE INDEX IF NOT EXISTS idx_type_aliases_file ON type_aliases(file_id);
CREATE INDEX IF NOT EXISTS idx_hook_calls_hook ON hook_calls(hook_name);
CREATE INDEX IF NOT EXISTS idx_hook_calls_file ON hook_calls(file_id);
CREATE INDEX IF NOT EXISTS idx_hook_calls_origin ON hook_calls(origin);
CREATE INDEX IF NOT EXISTS idx_vue_component_contracts_file ON vue_component_contracts(file_id);
CREATE INDEX IF NOT EXISTS idx_vue_component_contracts_kind_field ON vue_component_contracts(kind, field_name);
CREATE INDEX IF NOT EXISTS idx_pinia_stores_file ON pinia_stores(file_id);
CREATE INDEX IF NOT EXISTS idx_pinia_stores_store ON pinia_stores(store_id);
CREATE INDEX IF NOT EXISTS idx_files_repo_path_canonical ON files(repo_id, path_canonical);
CREATE INDEX IF NOT EXISTS idx_file_imports_file ON file_imports(file_id);
CREATE INDEX IF NOT EXISTS idx_function_calls_caller ON function_calls(caller_function_id);
CREATE INDEX IF NOT EXISTS idx_function_calls_callee ON function_calls(callee_function_id) WHERE callee_function_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_function_calls_callee_name ON function_calls(callee_name);
CREATE INDEX IF NOT EXISTS idx_function_calls_callee_name_null ON function_calls(callee_name) WHERE callee_function_id IS NULL;
CREATE INDEX IF NOT EXISTS idx_trace_call_edges_caller ON trace_call_edges(caller_id);
CREATE INDEX IF NOT EXISTS idx_trace_call_edges_callee ON trace_call_edges(callee_id);
CREATE INDEX IF NOT EXISTS idx_trace_call_edges_repo ON trace_call_edges(repo_id);
CREATE INDEX IF NOT EXISTS idx_trace_call_edges_snapshot ON trace_call_edges(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_trace_call_edges_caller_fn ON trace_call_edges(caller_function_id);
CREATE INDEX IF NOT EXISTS idx_trace_call_edges_callee_fn ON trace_call_edges(callee_function_id) WHERE callee_function_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_trace_call_edges_callee_trgm ON trace_call_edges USING gin (callee_name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_repo_dependency_edges_source ON repo_dependency_edges(source_repo_id);
CREATE INDEX IF NOT EXISTS idx_repo_dependency_edges_target ON repo_dependency_edges(target_repo_id);
CREATE INDEX IF NOT EXISTS idx_repo_dependency_edges_type ON repo_dependency_edges(edge_type);
CREATE INDEX IF NOT EXISTS idx_trace_interface_impls_iface ON trace_interface_impls(interface_name);
CREATE INDEX IF NOT EXISTS idx_trace_interface_impls_repo ON trace_interface_impls(repo_id);
CREATE INDEX IF NOT EXISTS idx_trace_interface_impls_snapshot ON trace_interface_impls(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_endpoints_handler_function ON endpoints(handler_function_id);
CREATE INDEX IF NOT EXISTS idx_method_signatures_overrides ON method_signatures(overrides_method_id);
CREATE INDEX IF NOT EXISTS idx_http_interface_methods_function ON http_interface_methods(function_id);
CREATE INDEX IF NOT EXISTS idx_bean_definitions_method ON bean_definitions(method_id);
CREATE INDEX IF NOT EXISTS idx_event_listeners_method ON event_listeners(method_id);
CREATE INDEX IF NOT EXISTS idx_scheduled_methods_method ON scheduled_methods(method_id);
-- Enrichment replaces facts per source owner; history must not force a full
-- table scan for every file/class being refreshed.
CREATE INDEX IF NOT EXISTS idx_constructor_params_class ON constructor_params(class_id);
CREATE INDEX IF NOT EXISTS idx_bean_definitions_class ON bean_definitions(config_class_id);
CREATE INDEX IF NOT EXISTS idx_event_listeners_class ON event_listeners(class_id);
CREATE INDEX IF NOT EXISTS idx_scheduled_methods_class ON scheduled_methods(class_id);
CREATE INDEX IF NOT EXISTS idx_endpoints_repo ON endpoints(repo_id);
CREATE INDEX IF NOT EXISTS idx_endpoints_file ON endpoints(file_id);
CREATE INDEX IF NOT EXISTS idx_pending_contains ON pending_contains_edges(file_id);
CREATE INDEX IF NOT EXISTS idx_pending_calls ON pending_calls_edges(caller_id);
CREATE INDEX IF NOT EXISTS idx_pending_imports ON pending_imports_edges(file_id);
CREATE INDEX IF NOT EXISTS idx_http_calls_url ON http_client_calls(url_pattern);
CREATE INDEX IF NOT EXISTS idx_http_calls_caller ON http_client_calls(caller_id);
CREATE INDEX IF NOT EXISTS idx_graphql_operations_name ON graphql_operations(operation_name);
CREATE INDEX IF NOT EXISTS idx_graphql_operations_type ON graphql_operations(operation_type);
CREATE INDEX IF NOT EXISTS idx_graphql_operations_repo ON graphql_operations(repo_id);
CREATE INDEX IF NOT EXISTS idx_graphql_operation_usages_import_path ON graphql_operation_usages(import_path);
CREATE INDEX IF NOT EXISTS idx_graphql_operation_usages_caller ON graphql_operation_usages(caller_function);
CREATE INDEX IF NOT EXISTS idx_graphql_operation_usages_repo ON graphql_operation_usages(repo_id);
CREATE INDEX IF NOT EXISTS idx_graphql_backend_entrypoints_kind ON graphql_backend_entrypoints(registration_kind);
CREATE INDEX IF NOT EXISTS idx_graphql_backend_entrypoints_repo ON graphql_backend_entrypoints(repo_id);
CREATE INDEX IF NOT EXISTS idx_graphql_backend_controller_links_entrypoint ON graphql_backend_controller_links(entrypoint_id);
CREATE INDEX IF NOT EXISTS idx_graphql_backend_controller_links_file ON graphql_backend_controller_links(file_id);
CREATE INDEX IF NOT EXISTS idx_graphql_backend_controller_links_repo ON graphql_backend_controller_links(repo_id);
CREATE INDEX IF NOT EXISTS idx_graphql_operation_resolvers_name ON graphql_operation_resolvers(operation_name);
CREATE INDEX IF NOT EXISTS idx_graphql_operation_resolvers_file ON graphql_operation_resolvers(file_id);
CREATE INDEX IF NOT EXISTS idx_graphql_operation_resolvers_repo ON graphql_operation_resolvers(repo_id);
CREATE INDEX IF NOT EXISTS idx_graphql_operation_permissions_name ON graphql_operation_permissions(operation_name);
CREATE INDEX IF NOT EXISTS idx_graphql_operation_permissions_file ON graphql_operation_permissions(file_id);
CREATE INDEX IF NOT EXISTS idx_graphql_operation_permissions_repo ON graphql_operation_permissions(repo_id);
CREATE INDEX IF NOT EXISTS idx_graphql_usage_operation_links_usage ON graphql_usage_operation_links(usage_id);
CREATE INDEX IF NOT EXISTS idx_graphql_usage_operation_links_operation ON graphql_usage_operation_links(operation_id);
CREATE INDEX IF NOT EXISTS idx_graphql_usage_operation_links_repo ON graphql_usage_operation_links(repo_id);
CREATE INDEX IF NOT EXISTS idx_azure_host_configs_repo ON azure_host_configs(repo_id);
CREATE INDEX IF NOT EXISTS idx_azure_function_triggers_type ON azure_function_triggers(trigger_type);
CREATE INDEX IF NOT EXISTS idx_azure_function_triggers_function ON azure_function_triggers(function_name);
CREATE INDEX IF NOT EXISTS idx_azure_function_triggers_repo ON azure_function_triggers(repo_id);
CREATE INDEX IF NOT EXISTS idx_gateway_routes_public ON gateway_routes(public_method, public_path);
CREATE INDEX IF NOT EXISTS idx_gateway_routes_repo ON gateway_routes(repo_id);
CREATE INDEX IF NOT EXISTS idx_gateway_routes_backend_path ON gateway_routes(backend_path);
CREATE INDEX IF NOT EXISTS idx_endpoints_path ON endpoints(path);
CREATE INDEX IF NOT EXISTS idx_pending_callee ON pending_calls_edges(callee_name);
CREATE INDEX IF NOT EXISTS idx_sqs_producers_queue ON sqs_producers(queue_name);
CREATE INDEX IF NOT EXISTS idx_sqs_producers_caller ON sqs_producers(caller_id);
CREATE INDEX IF NOT EXISTS idx_sqs_consumers_queue ON sqs_consumers(queue_name);
CREATE INDEX IF NOT EXISTS idx_resource_aliases_key ON resource_aliases(alias_key);
CREATE INDEX IF NOT EXISTS idx_resource_aliases_value ON resource_aliases(alias_value);
CREATE INDEX IF NOT EXISTS idx_resource_aliases_repo ON resource_aliases(repo_id);
CREATE INDEX IF NOT EXISTS idx_resource_aliases_snapshot ON resource_aliases(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_eb_schedules_target ON eventbridge_schedules(target_name);
CREATE INDEX IF NOT EXISTS idx_eb_schedules_type ON eventbridge_schedules(target_type);
CREATE INDEX IF NOT EXISTS idx_pending_contains_repo ON pending_contains_edges(repo_id);
CREATE INDEX IF NOT EXISTS idx_pending_calls_repo ON pending_calls_edges(repo_id);
CREATE INDEX IF NOT EXISTS idx_pending_contains_snapshot ON pending_contains_edges(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_pending_calls_snapshot ON pending_calls_edges(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_pending_imports_snapshot ON pending_imports_edges(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_pending_calls_repo_part ON pending_calls_edges ((split_part(caller_id, ':', 1)));
CREATE INDEX IF NOT EXISTS idx_pending_calls_file_part ON pending_calls_edges ((split_part(caller_id, ':', 2)));
CREATE INDEX IF NOT EXISTS idx_pending_calls_func_part_trgm ON pending_calls_edges USING gin (split_part(caller_id, ':', 3) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_pending_imports_repo ON pending_imports_edges(repo_id);
CREATE INDEX IF NOT EXISTS idx_http_calls_repo ON http_client_calls(repo_id);
CREATE INDEX IF NOT EXISTS idx_sqs_producers_repo ON sqs_producers(repo_id);
CREATE INDEX IF NOT EXISTS idx_sqs_consumers_repo ON sqs_consumers(repo_id);
CREATE INDEX IF NOT EXISTS idx_http_calls_snapshot ON http_client_calls(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_sqs_producers_snapshot ON sqs_producers(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_sqs_consumers_snapshot ON sqs_consumers(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_pending_calls_caller_like ON pending_calls_edges (caller_id text_pattern_ops);
CREATE INDEX IF NOT EXISTS idx_pending_contains_file_like ON pending_contains_edges (file_id text_pattern_ops);
CREATE INDEX IF NOT EXISTS idx_pending_imports_file_like ON pending_imports_edges (file_id text_pattern_ops);
CREATE INDEX IF NOT EXISTS idx_http_calls_caller_like ON http_client_calls (caller_id text_pattern_ops);
CREATE INDEX IF NOT EXISTS idx_sqs_producers_caller_like ON sqs_producers (caller_id text_pattern_ops);
CREATE INDEX IF NOT EXISTS idx_sqs_consumers_consumer_like ON sqs_consumers (consumer_id text_pattern_ops);
CREATE INDEX IF NOT EXISTS idx_trace_call_edges_caller_like ON trace_call_edges (caller_id text_pattern_ops);
CREATE INDEX IF NOT EXISTS idx_pending_calls_caller_trgm ON pending_calls_edges USING gin (caller_id gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_pending_calls_callee_trgm ON pending_calls_edges USING gin (callee_name gin_trgm_ops);

CREATE INDEX IF NOT EXISTS idx_function_calls_caller ON function_calls(caller_function_id);
CREATE INDEX IF NOT EXISTS idx_http_calls_caller_trgm ON http_client_calls USING gin (caller_id gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_sqs_producers_caller_trgm ON sqs_producers USING gin (caller_id gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_sqs_consumers_consumer_trgm ON sqs_consumers USING gin (consumer_id gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_endpoints_path_trgm ON endpoints USING gin (path gin_trgm_ops);

-- ==========================================
-- FEATURE-COMPLETE JAVA PARSER TABLES
-- ==========================================

-- JPA entities with table metadata
CREATE TABLE IF NOT EXISTS jpa_entities (
  id SERIAL PRIMARY KEY,
  class_id INTEGER NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
  table_name TEXT NOT NULL,
  schema_name TEXT,
  catalog TEXT,
  UNIQUE(class_id)
);

-- JPA relationships between entities
CREATE TABLE IF NOT EXISTS jpa_relationships (
  id SERIAL PRIMARY KEY,
  source_class_id INTEGER NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
  target_entity_name TEXT NOT NULL,
  target_class_id INTEGER REFERENCES classes(id) ON DELETE SET NULL,
  relation_type TEXT NOT NULL,
  source_field TEXT NOT NULL,
  mapped_by TEXT,
  join_column TEXT,
  fetch_type TEXT,
  cascade_types JSONB,
  line_number INTEGER
);

-- Repository -> Entity mappings (Spring Data, etc.)
CREATE TABLE IF NOT EXISTS repository_entities (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  repository_name TEXT NOT NULL,
  entity_name TEXT NOT NULL,
  line_number INTEGER
);

-- Data accesses (SQL/MyBatis/DBAction, etc.)
CREATE TABLE IF NOT EXISTS data_accesses (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE,
  caller_id TEXT NOT NULL,
  entity_name TEXT NOT NULL,
  access TEXT NOT NULL,
  line_number INTEGER
);

ALTER TABLE repository_entities
  ADD COLUMN IF NOT EXISTS snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE;
ALTER TABLE data_accesses
  ADD COLUMN IF NOT EXISTS snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE;

ALTER TABLE repository_entities DROP CONSTRAINT IF EXISTS repository_entities_repo_id_file_id_repository_name_entity_name_key;
ALTER TABLE data_accesses DROP CONSTRAINT IF EXISTS data_accesses_repo_id_caller_id_entity_name_access_line_number_key;
-- PostgreSQL shortens generated constraint names to its identifier limit.
ALTER TABLE repository_entities DROP CONSTRAINT IF EXISTS repository_entities_repo_id_file_id_repository_name_entity__key;
ALTER TABLE data_accesses DROP CONSTRAINT IF EXISTS data_accesses_repo_id_caller_id_entity_name_access_line_num_key;

CREATE UNIQUE INDEX IF NOT EXISTS idx_repository_entities_legacy_unique
ON repository_entities(repo_id, file_id, repository_name, entity_name)
WHERE snapshot_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_repository_entities_snapshot_unique
ON repository_entities(snapshot_id, file_id, repository_name, entity_name)
WHERE snapshot_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_data_accesses_legacy_unique
ON data_accesses(repo_id, caller_id, entity_name, access, line_number)
WHERE snapshot_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_data_accesses_snapshot_unique
ON data_accesses(snapshot_id, caller_id, entity_name, access, line_number)
WHERE snapshot_id IS NOT NULL;

-- IBMi exports/bindings are parser-owned facts and must be snapshot-scoped
-- so release and master branch call resolution cannot share exported symbols.
CREATE TABLE IF NOT EXISTS ibmi_exports (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE,
  file_id INTEGER REFERENCES files(id) ON DELETE CASCADE,
  function_id INTEGER REFERENCES functions(id) ON DELETE CASCADE,
  object_name TEXT,
  object_type TEXT,
  export_name TEXT NOT NULL,
  source_type TEXT NOT NULL,
  line_number INTEGER
);

CREATE TABLE IF NOT EXISTS ibmi_bindings (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE,
  file_id INTEGER REFERENCES files(id) ON DELETE CASCADE,
  owner_object TEXT,
  binding_name TEXT NOT NULL,
  binding_type TEXT NOT NULL,
  target_object TEXT,
  target_object_type TEXT,
  line_number INTEGER
);

ALTER TABLE ibmi_exports
  ADD COLUMN IF NOT EXISTS snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE;
ALTER TABLE ibmi_bindings
  ADD COLUMN IF NOT EXISTS snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE;

DO $$
DECLARE constraint_name text;
BEGIN
  FOR constraint_name IN
    SELECT conname FROM pg_constraint WHERE conrelid = 'ibmi_exports'::regclass AND contype = 'u'
  LOOP
    EXECUTE format('ALTER TABLE ibmi_exports DROP CONSTRAINT IF EXISTS %I', constraint_name);
  END LOOP;
  FOR constraint_name IN
    SELECT conname FROM pg_constraint WHERE conrelid = 'ibmi_bindings'::regclass AND contype = 'u'
  LOOP
    EXECUTE format('ALTER TABLE ibmi_bindings DROP CONSTRAINT IF EXISTS %I', constraint_name);
  END LOOP;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS idx_ibmi_exports_legacy_unique
ON ibmi_exports(repo_id, file_id, function_id, export_name, object_name, source_type, line_number)
WHERE snapshot_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_ibmi_exports_snapshot_unique
ON ibmi_exports(snapshot_id, repo_id, file_id, function_id, export_name, object_name, source_type, line_number)
WHERE snapshot_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_ibmi_bindings_legacy_unique
ON ibmi_bindings(repo_id, file_id, owner_object, binding_name, binding_type, target_object, target_object_type, line_number)
WHERE snapshot_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_ibmi_bindings_snapshot_unique
ON ibmi_bindings(snapshot_id, repo_id, file_id, owner_object, binding_name, binding_type, target_object, target_object_type, line_number)
WHERE snapshot_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_ibmi_exports_snapshot ON ibmi_exports(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_ibmi_bindings_snapshot ON ibmi_bindings(snapshot_id);

-- Synthetic/generated methods (Lombok, records, enums)
CREATE TABLE IF NOT EXISTS synthetic_methods (
  id SERIAL PRIMARY KEY,
  class_id INTEGER NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  return_type TEXT,
  params JSONB,
  source TEXT NOT NULL,
  annotation TEXT,
  field_name TEXT,
  line_number INTEGER
);

-- Enum constants
CREATE TABLE IF NOT EXISTS enum_constants (
  id SERIAL PRIMARY KEY,
  class_id INTEGER NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  ordinal INTEGER NOT NULL,
  arguments JSONB,
  UNIQUE(class_id, name)
);

-- Lambda expressions (for tracking calls within lambdas)
CREATE TABLE IF NOT EXISTS lambda_expressions (
  id SERIAL PRIMARY KEY,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  class_name TEXT,
  method_name TEXT,
  parameters JSONB,
  start_line INTEGER,
  end_line INTEGER
);

-- ==========================================
-- FK CASCADE MIGRATIONS (safe to re-run)
-- ==========================================
ALTER TABLE files DROP CONSTRAINT IF EXISTS files_repo_id_fkey;
ALTER TABLE files ADD CONSTRAINT files_repo_id_fkey FOREIGN KEY (repo_id) REFERENCES repositories(id) ON DELETE CASCADE;
ALTER TABLE functions DROP CONSTRAINT IF EXISTS functions_file_id_fkey;
ALTER TABLE functions ADD CONSTRAINT functions_file_id_fkey FOREIGN KEY (file_id) REFERENCES files(id) ON DELETE CASCADE;
ALTER TABLE classes DROP CONSTRAINT IF EXISTS classes_file_id_fkey;
ALTER TABLE classes ADD CONSTRAINT classes_file_id_fkey FOREIGN KEY (file_id) REFERENCES files(id) ON DELETE CASCADE;
ALTER TABLE endpoints DROP CONSTRAINT IF EXISTS endpoints_repo_id_fkey;
ALTER TABLE endpoints ADD CONSTRAINT endpoints_repo_id_fkey FOREIGN KEY (repo_id) REFERENCES repositories(id) ON DELETE CASCADE;
ALTER TABLE endpoints DROP CONSTRAINT IF EXISTS endpoints_handler_function_id_fkey;
ALTER TABLE endpoints ADD CONSTRAINT endpoints_handler_function_id_fkey FOREIGN KEY (handler_function_id) REFERENCES functions(id) ON DELETE SET NULL;
ALTER TABLE endpoints DROP CONSTRAINT IF EXISTS endpoints_file_id_fkey;
ALTER TABLE endpoints ADD CONSTRAINT endpoints_file_id_fkey FOREIGN KEY (file_id) REFERENCES files(id) ON DELETE CASCADE;
ALTER TABLE file_imports DROP CONSTRAINT IF EXISTS file_imports_file_id_fkey;
ALTER TABLE file_imports ADD CONSTRAINT file_imports_file_id_fkey FOREIGN KEY (file_id) REFERENCES files(id) ON DELETE CASCADE;
ALTER TABLE file_imports DROP CONSTRAINT IF EXISTS file_imports_imports_file_id_fkey;
ALTER TABLE file_imports ADD CONSTRAINT file_imports_imports_file_id_fkey FOREIGN KEY (imports_file_id) REFERENCES files(id) ON DELETE SET NULL;
ALTER TABLE function_calls DROP CONSTRAINT IF EXISTS function_calls_caller_function_id_fkey;
ALTER TABLE function_calls ADD CONSTRAINT function_calls_caller_function_id_fkey FOREIGN KEY (caller_function_id) REFERENCES functions(id) ON DELETE CASCADE;
ALTER TABLE function_calls DROP CONSTRAINT IF EXISTS function_calls_callee_function_id_fkey;
ALTER TABLE function_calls ADD CONSTRAINT function_calls_callee_function_id_fkey FOREIGN KEY (callee_function_id) REFERENCES functions(id) ON DELETE SET NULL;
-- Target identity and callback invocation evidence are separate. Rebinding a
-- callable must not strengthen the evidence that its recipient invokes it.
CREATE OR REPLACE FUNCTION clear_unlinked_call_resolution() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'UPDATE' THEN
    IF OLD.callee_function_id IS NOT NULL AND NEW.callee_function_id IS NULL THEN
      NEW.callee_resolution_source := NULL;
      NEW.callee_resolution_confidence := NULL;
      NEW.unresolved_reason := COALESCE(NULLIF(NEW.unresolved_reason, ''), 'callee_removed');
    END IF;
  END IF;
  -- Identity resolution must never promote a possible callback invocation to
  -- high-confidence execution evidence, including during incremental rebinding.
  IF NEW.is_callback_argument THEN
    NEW.callee_resolution_source := 'callback_argument';
    NEW.callee_resolution_confidence := 'low';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS function_calls_clear_unlinked_resolution ON function_calls;
CREATE TRIGGER function_calls_clear_unlinked_resolution
BEFORE INSERT OR UPDATE ON function_calls
FOR EACH ROW EXECUTE FUNCTION clear_unlinked_call_resolution();
ALTER TABLE interfaces DROP CONSTRAINT IF EXISTS interfaces_file_id_fkey;
ALTER TABLE interfaces ADD CONSTRAINT interfaces_file_id_fkey FOREIGN KEY (file_id) REFERENCES files(id) ON DELETE CASCADE;
ALTER TABLE implementations DROP CONSTRAINT IF EXISTS implementations_class_id_fkey;
ALTER TABLE implementations ADD CONSTRAINT implementations_class_id_fkey FOREIGN KEY (class_id) REFERENCES classes(id) ON DELETE CASCADE;
ALTER TABLE implementations DROP CONSTRAINT IF EXISTS implementations_interface_id_fkey;
ALTER TABLE implementations ADD CONSTRAINT implementations_interface_id_fkey FOREIGN KEY (interface_id) REFERENCES interfaces(id) ON DELETE SET NULL;
ALTER TABLE fields DROP CONSTRAINT IF EXISTS fields_class_id_fkey;
ALTER TABLE fields ADD CONSTRAINT fields_class_id_fkey FOREIGN KEY (class_id) REFERENCES classes(id) ON DELETE CASCADE;
ALTER TABLE fields DROP CONSTRAINT IF EXISTS fields_interface_id_fkey;
ALTER TABLE fields ADD CONSTRAINT fields_interface_id_fkey FOREIGN KEY (interface_id) REFERENCES interfaces(id) ON DELETE CASCADE;
ALTER TABLE method_signatures DROP CONSTRAINT IF EXISTS method_signatures_function_id_fkey;
ALTER TABLE method_signatures ADD CONSTRAINT method_signatures_function_id_fkey FOREIGN KEY (function_id) REFERENCES functions(id) ON DELETE CASCADE;
ALTER TABLE method_signatures DROP CONSTRAINT IF EXISTS method_signatures_overrides_method_id_fkey;
ALTER TABLE method_signatures ADD CONSTRAINT method_signatures_overrides_method_id_fkey FOREIGN KEY (overrides_method_id) REFERENCES functions(id) ON DELETE SET NULL;
ALTER TABLE constructor_params DROP CONSTRAINT IF EXISTS constructor_params_class_id_fkey;
ALTER TABLE constructor_params ADD CONSTRAINT constructor_params_class_id_fkey FOREIGN KEY (class_id) REFERENCES classes(id) ON DELETE CASCADE;
ALTER TABLE http_interface_methods DROP CONSTRAINT IF EXISTS http_interface_methods_interface_id_fkey;
ALTER TABLE http_interface_methods ADD CONSTRAINT http_interface_methods_interface_id_fkey FOREIGN KEY (interface_id) REFERENCES interfaces(id) ON DELETE CASCADE;
ALTER TABLE http_interface_methods DROP CONSTRAINT IF EXISTS http_interface_methods_function_id_fkey;
ALTER TABLE http_interface_methods ADD CONSTRAINT http_interface_methods_function_id_fkey FOREIGN KEY (function_id) REFERENCES functions(id) ON DELETE CASCADE;
ALTER TABLE bean_definitions DROP CONSTRAINT IF EXISTS bean_definitions_config_class_id_fkey;
ALTER TABLE bean_definitions ADD CONSTRAINT bean_definitions_config_class_id_fkey FOREIGN KEY (config_class_id) REFERENCES classes(id) ON DELETE CASCADE;
ALTER TABLE bean_definitions DROP CONSTRAINT IF EXISTS bean_definitions_method_id_fkey;
ALTER TABLE bean_definitions ADD CONSTRAINT bean_definitions_method_id_fkey FOREIGN KEY (method_id) REFERENCES functions(id) ON DELETE CASCADE;
ALTER TABLE event_listeners DROP CONSTRAINT IF EXISTS event_listeners_class_id_fkey;
ALTER TABLE event_listeners ADD CONSTRAINT event_listeners_class_id_fkey FOREIGN KEY (class_id) REFERENCES classes(id) ON DELETE CASCADE;
ALTER TABLE event_listeners DROP CONSTRAINT IF EXISTS event_listeners_method_id_fkey;
ALTER TABLE event_listeners ADD CONSTRAINT event_listeners_method_id_fkey FOREIGN KEY (method_id) REFERENCES functions(id) ON DELETE CASCADE;
ALTER TABLE scheduled_methods DROP CONSTRAINT IF EXISTS scheduled_methods_class_id_fkey;
ALTER TABLE scheduled_methods ADD CONSTRAINT scheduled_methods_class_id_fkey FOREIGN KEY (class_id) REFERENCES classes(id) ON DELETE CASCADE;
ALTER TABLE scheduled_methods DROP CONSTRAINT IF EXISTS scheduled_methods_method_id_fkey;
ALTER TABLE scheduled_methods ADD CONSTRAINT scheduled_methods_method_id_fkey FOREIGN KEY (method_id) REFERENCES functions(id) ON DELETE CASCADE;
ALTER TABLE jpa_entities DROP CONSTRAINT IF EXISTS jpa_entities_class_id_fkey;
ALTER TABLE jpa_entities ADD CONSTRAINT jpa_entities_class_id_fkey FOREIGN KEY (class_id) REFERENCES classes(id) ON DELETE CASCADE;
ALTER TABLE jpa_relationships DROP CONSTRAINT IF EXISTS jpa_relationships_source_class_id_fkey;
ALTER TABLE jpa_relationships ADD CONSTRAINT jpa_relationships_source_class_id_fkey FOREIGN KEY (source_class_id) REFERENCES classes(id) ON DELETE CASCADE;
ALTER TABLE jpa_relationships DROP CONSTRAINT IF EXISTS jpa_relationships_target_class_id_fkey;
ALTER TABLE jpa_relationships ADD CONSTRAINT jpa_relationships_target_class_id_fkey FOREIGN KEY (target_class_id) REFERENCES classes(id) ON DELETE SET NULL;
ALTER TABLE synthetic_methods DROP CONSTRAINT IF EXISTS synthetic_methods_class_id_fkey;
ALTER TABLE synthetic_methods ADD CONSTRAINT synthetic_methods_class_id_fkey FOREIGN KEY (class_id) REFERENCES classes(id) ON DELETE CASCADE;
ALTER TABLE enum_constants DROP CONSTRAINT IF EXISTS enum_constants_class_id_fkey;
ALTER TABLE enum_constants ADD CONSTRAINT enum_constants_class_id_fkey FOREIGN KEY (class_id) REFERENCES classes(id) ON DELETE CASCADE;
ALTER TABLE lambda_expressions DROP CONSTRAINT IF EXISTS lambda_expressions_file_id_fkey;
ALTER TABLE lambda_expressions ADD CONSTRAINT lambda_expressions_file_id_fkey FOREIGN KEY (file_id) REFERENCES files(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_jpa_entities_table ON jpa_entities(table_name);
CREATE INDEX IF NOT EXISTS idx_jpa_entities_class ON jpa_entities(class_id);
CREATE INDEX IF NOT EXISTS idx_jpa_relationships_source ON jpa_relationships(source_class_id);
CREATE INDEX IF NOT EXISTS idx_jpa_relationships_target ON jpa_relationships(target_entity_name);
CREATE INDEX IF NOT EXISTS idx_repository_entities_repo ON repository_entities(repo_id);
CREATE INDEX IF NOT EXISTS idx_repository_entities_snapshot ON repository_entities(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_repository_entities_name ON repository_entities(repository_name);
CREATE INDEX IF NOT EXISTS idx_repository_entities_entity ON repository_entities(entity_name);

CREATE INDEX IF NOT EXISTS idx_data_accesses_repo ON data_accesses(repo_id);
CREATE INDEX IF NOT EXISTS idx_data_accesses_snapshot ON data_accesses(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_data_accesses_caller ON data_accesses(caller_id);
CREATE INDEX IF NOT EXISTS idx_data_accesses_caller_like ON data_accesses(caller_id text_pattern_ops);
CREATE INDEX IF NOT EXISTS idx_data_accesses_entity ON data_accesses(entity_name);
CREATE INDEX IF NOT EXISTS idx_data_accesses_entity_trgm ON data_accesses USING gin (entity_name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_synthetic_methods_class ON synthetic_methods(class_id);
CREATE INDEX IF NOT EXISTS idx_synthetic_methods_name ON synthetic_methods(name);
CREATE INDEX IF NOT EXISTS idx_enum_constants_class ON enum_constants(class_id);
CREATE INDEX IF NOT EXISTS idx_enum_constants_name ON enum_constants(name);
CREATE INDEX IF NOT EXISTS idx_lambda_expressions_file ON lambda_expressions(file_id);

`

type Repository struct {
	ID        int64
	Name      string
	Path      string
	Language  *string
	Framework *string
}

type File struct {
	ID       int64
	RepoID   int64
	Path     string
	Language *string
	Hash     *string
}

type Function struct {
	ID         int64
	FileID     int64
	Name       string
	StartLine  int
	EndLine    int
	Params     *string
	ReturnType *string
	IsExported bool
	IsAsync    bool
	SourceCode *string
}

type Class struct {
	ID           int64
	FileID       int64
	Name         string
	StartLine    int
	EndLine      int
	ExtendsClass *string
	Implements   *string
	IsExported   bool
}

type Endpoint struct {
	ID                int64
	RepoID            int64
	Path              string
	Method            string
	HandlerFunctionID *int64
	FileID            *int64
	LineNumber        *int
}

type FileImport struct {
	ID              int64
	FileID          int64
	ImportsFileID   *int64
	ImportPath      string
	ImportNames     *string
	IsDefaultImport bool
}

type FunctionCall struct {
	ID               int64
	CallerFunctionID int64
	CalleeFunctionID *int64
	CalleeName       string
	LineNumber       *int
	IsAsync          bool
}

type Stats struct {
	Repos     int `json:"repos"`
	Files     int `json:"files"`
	Functions int `json:"functions"`
	Classes   int `json:"classes"`
	Endpoints int `json:"endpoints"`
}

type HttpClientCall struct {
	CallerID   string
	HttpMethod string
	UrlPattern string
	LineNumber int
	ClientType string
}

type SqsProducer struct {
	CallerID   string
	QueueName  string
	LineNumber int
}

type SqsConsumer struct {
	ConsumerID    string
	QueueName     string
	HandlerMethod string
}
