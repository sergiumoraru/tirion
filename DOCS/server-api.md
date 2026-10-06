# HTTP API

`tirion serve` exposes the indexed graph through JSON HTTP endpoints. The web UI
is served separately. See [SETUP.md](../SETUP.md) for setup,
the [configuration reference](../SETUP.md#environment-variables) for the settings this
page refers to, and [access control](../SETUP.md#access-control) before exposing
the API to other machines. Behavior changes between builds are listed under
[Upgrading From Earlier Builds](../SETUP.md#upgrading-from-earlier-builds).

The source checkout's route registry is `internal/api/server.go`.
Request and response types live with their handlers. Examples use synthetic
identifiers; substitute values returned by your own index.

## Workspace Context

Choose the workspace whose indexed revisions match the source being examined.
Graph requests accept `workspaceId` where exposed by their request type; the UI
also sends `X-Tirion-Workspace`. Workspace-management routes identify their
workspace in the path.

A workspace that does not exist is a 404 `NOT_FOUND` (`workspace "x" not found`);
a malformed slug is a 400 `INVALID_WORKSPACE`. Neither falls back to the default
workspace.

Responses may contain `workspace`, `repoContext`, `workspaceHints`, and `warnings`.
Repository snapshots describe indexed revisions, not production deployments.
Source returned by this API can contain sensitive application code.

For example, from a machine permitted to reach your API (the token is described
under [Authentication](#authentication)):

```bash
TOKEN="$(cat "$HOME/.tirion/api-token")"
curl --fail --silent --show-error --get http://localhost:8080/api/search \
  -H "X-Tirion-Token: $TOKEN" \
  --data-urlencode 'q=ResourceController.create' \
  --data-urlencode 'workspaceId=default-main' --data-urlencode 'limit=10'
curl --fail --silent --show-error http://localhost:8080/api/trace \
  -H "X-Tirion-Token: $TOKEN" -H 'Content-Type: application/json' \
  --data '{"workspaceId":"default-main","function":"ResourceController.create","depth":4,"resolve":true}'
```

Replace the symbol with one returned by your index. A shared deployment is
reached through its protected proxy origin, which may require its own credentials
in addition to the Tirion token. Passing the token on a command line exposes it to
other local users through the process list; prefer a curl config file or your
client's secret handling for shared machines.

### Repository filters

A `repo` filter (search, `/api/endpoints`, and the GraphQL and Azure routes)
matches a repository name of the selected workspace
case-insensitively and exactly, or by prefix when it ends in `*` (`orders-*`).
It is never a substring match, and `%`, `_`, and `\` are literal. A filter that
matches no repository of the workspace is a 404 `NOT_FOUND`; a matching repository
with no results returns an empty successful response. `GET /api/contracts/{repo}`
resolves its path segment case-insensitively too, but a `*` is not a wildcard
there and returns 404. If two repositories differ only by case, the exact spelling
wins when it is one of them; otherwise the request fails with 400
`AMBIGUOUS_REPO`.

## Authentication

Every route, including `/api/health`, requires the service token. Send it as
either header:

```text
X-Tirion-Token: <token>
Authorization: Bearer <token>
```

If both are present, `X-Tirion-Token` is used; a wrong value there is not
rescued by a correct `Authorization` header. The `Bearer` scheme name is
case-insensitive. A missing or incorrect token returns HTTP 401 with
`WWW-Authenticate: Bearer realm="tirion"` and the standard error envelope:

```json
{"error":{"code":"UNAUTHORIZED","message":"A valid Tirion API token is required"}}
```

Query-string tokens are not accepted. Browser `OPTIONS` preflight requests are
answered before authentication (they carry no credentials) but are still subject
to the Host and Origin checks below; the actual request that follows needs the
token. Unknown paths and unsupported methods also return 401 without a token.

The token is one shared service credential: possession grants graph reads and
repository administration. There are no per-user roles. How the server and its
clients obtain the token (`TIRION_API_TOKEN`, `TIRION_API_TOKEN_FILE`, the
`api-token` file in the state directory, and the loopback-only discovery rule for
clients) is described in
[Authentication and state paths](../SETUP.md#authentication-and-state-paths).
Clients never follow redirects, so the token cannot be forwarded to another origin.

## Hosts, Origins, And Limits

Trusted Host. The `Host` header must match an entry of `TIRION_ALLOWED_HOSTS`
(exact hostnames or IP addresses, optionally with a port). Matching is
case-insensitive. An entry without a port accepts any port for that host; an entry
with a port accepts only that exact `host:port`. Other Hosts get HTTP 403
`UNTRUSTED_HOST`. `X-Forwarded-*` and `Forwarded` headers are never used to
establish trust, so behind a TLS proxy list the external hostname explicitly.
Responses carry `Cache-Control: no-store` and `X-Content-Type-Options: nosniff`.

CORS. A browser `Origin` is accepted when it equals the request's own origin or is
listed in `TIRION_ALLOWED_ORIGINS`. Other origins get HTTP 403
`ORIGIN_NOT_ALLOWED`. Allowed methods are `GET`, `POST`, and `OPTIONS`; allowed
request headers are `Content-Type`, `Authorization`, `X-Tirion-Token`,
`X-Tirion-Workspace`, and `X-Tirion-Actor`. Requests without an `Origin` header
(such as curl) are not affected. CORS settings are not authentication.

Both lists, their defaults, and the other server settings are in
[Server, network, and limits](../SETUP.md#server-network-and-limits).

Limits applied by the server and middleware:

| Limit | Value |
| --- | --- |
| Request body | 8 MiB; larger bodies get 413 `PAYLOAD_TOO_LARGE` |
| Request headers | 32 KiB |
| Header read / request read deadline | 10 s / 30 s |
| Graph (read) request budget | 60 s; an expired budget returns 503 `TIMEOUT` |
| Administrative budget | 2 h for `POST` under `/api/workspaces` and `/api/admin/` |
| Concurrent graph requests | 16 (`TIRION_MAX_CONCURRENT_READS`) |
| Concurrent administrative requests | 2 (`TIRION_MAX_CONCURRENT_ADMIN`) |
| `depth` (Trace, Impact, Flow) | at most 32, otherwise 400 `VALIDATION_ERROR` |
| Trace expansion | `depth` below 32 and `maxDepth` at most 32 |
| Flow `maxHops` | at most 10000, otherwise 400 `VALIDATION_ERROR` |
| `maxNodes` (Trace, Trace expand, Impact, Verify) | larger values are clamped to 10000 |
| Graph path `maxDepth` | default 5, integer 1-8; larger values are clamped to 8 |

Read and administrative requests have separate concurrency slots, so a long index
cannot starve graph reads. When the matching slots are all in use the server
answers 503 `BUSY` with `Retry-After: 1`; retry later. A request counts as
administrative only if it is a `POST` under `/api/workspaces` or `/api/admin/`; every `GET`
counts as a read. Defaults when a field is omitted: Trace and Impact `depth` 4,
Flow `depth` 3, `maxNodes` 2000, Flow `maxHops` 200. Individual handlers add
their own deadlines (for example 15 s for Flow lookups and 10 s for graph paths)
and return 504 `TIMEOUT` when they expire first.

### Pagination

`limit` must be an integer of at least 1 and `offset` an integer of at least 0;
anything else (`limit=abc`, `limit=0`, `offset=-1`) is a 400 `VALIDATION_ERROR`
whose `details.parameter` names the field. A `limit` above the route's maximum is
clamped, not rejected.

| Route | Default `limit` | Maximum `limit` | Offsets |
| --- | --- | --- | --- |
| `GET /api/search` | 10 | 50 | `offset` and bucket offsets |
| `GET /api/endpoints` | 100 | 500 | `offset` |
| `GET /api/graphql/operations`, `usages`, `entrypoints`, `controllers` | 100 | 500 | `offset` |
| `GET /api/azure/functions` | 100 | 500 | `offset` |
| `GET /api/azure/flow` | 100 | 500 | none |
| `GET /api/graphql/flow` | 50 | 200 | none |
| `GET /api/contracts` | 200 | 1000 | none |
| `GET /api/contracts/{repo}` | 200 | 2000 | none |
| `GET /api/verify/runs` | 50 | 200 | none |

## Errors

Errors use this envelope, whether raised by a handler or by the middleware
before a request reaches one:

```json
{
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "A required input is missing",
    "details": {}
  }
}
```

`details` is optional. Responses are `application/json`. Routing errors from the
Go mux (unknown path 404, unsupported method 405) and errors from a proxy or
transport need not use this envelope; clients must also check the HTTP status.

| Status | Code | Raised when |
| --- | --- | --- |
| 400 | `VALIDATION_ERROR` | Missing or invalid input, malformed JSON, a non-integer or out-of-range `limit`/`offset`/`maxDepth` (`details.parameter` names it), a non-numeric graph-path `from`/`to`, `depth`/`maxHops` over their caps, a diff that cannot be parsed, a diff path that exists in several repositories (`details.file`, `details.candidates`), or a rejected server-controlled field |
| 400 | `INVALID_WORKSPACE` | `workspaceId` / `X-Tirion-Workspace` is not a valid workspace slug |
| 400 | `AMBIGUOUS_REPO` | A repository path segment matches several repositories that differ only by case, and none is spelled exactly |
| 400 | `INVALID_BODY` | The request body could not be read |
| 401 | `UNAUTHORIZED` | Missing or wrong token |
| 403 | `UNTRUSTED_HOST` | `Host` is not in `TIRION_ALLOWED_HOSTS` |
| 403 | `ORIGIN_NOT_ALLOWED` | Browser `Origin` is not same-origin or in `TIRION_ALLOWED_ORIGINS` |
| 404 | `NOT_FOUND` | The workspace, repository (including a `repo` filter that matches nothing in the workspace), caller ID, class, or other named resource does not exist in the selected workspace |
| 409 | `REPO_OPERATION_BUSY` | Another repository operation is running (`details` describes it) |
| 409 | `WORKSPACE_INDEX_RUNNING` | A workspace bulk index job is already running |
| 409 | `DIRTY_REPO` | A checkout was refused because the worktree has local changes |
| 413 | `PAYLOAD_TOO_LARGE` | Request body over 8 MiB |
| 500 | `INTERNAL_ERROR`, `QUERY_ERROR` | Unexpected storage or query failure |
| 500 | `WORKSPACE_INDEX_FAILED`, `WORKSPACE_FETCH_FAILED`, `WORKTREE_CHECKOUT_FAILED`, `WORKTREE_INSPECT_FAILED`, `REPO_STATE_WRITE_FAILED`, `REPO_OPERATION_FAILED` | An administrative operation failed |
| 501 | `GRAPH_PATH_UNSUPPORTED` | `/api/graph/path` was asked for a non function-to-function path |
| 503 | `BUSY` | All concurrency slots for this kind of request are in use; retry later |
| 503 | `TIMEOUT` | The request exceeded its 60 s (or 2 h administrative) budget |
| 503 | `LOOKUP_FAILED` | A resource lookup failed on the server (also reported per item by the integrations batch) |
| 503 | `SEARCH_INCOMPLETE` | Search could not complete; do not read a failure as zero matches |
| 503 | `IMPACT_INCOMPLETE` | Entrypoint resolution or server-side diff mapping failed; not an empty impact |
| 503 | `VERIFY_INCOMPLETE` | Server-side diff symbol resolution failed during Verify |
| 503 | `CONTRACTS_INCOMPLETE` | Contract queries (for example queue relationships) failed |
| 503 | `CONFIG_INVALID` | The server's `patterns.yaml` became invalid after startup |
| 504 | `TIMEOUT` | A handler's own deadline (Trace, Flow, Impact, graph path) expired |
| 504 | `LOOKUP_TIMEOUT` | A lookup was cancelled or hit its deadline |
| 504 | `SEARCH_INCOMPLETE`, `IMPACT_INCOMPLETE` | The search or impact deadline expired |

An incomplete-result code means the answer is unknown, not empty. A malformed or
ambiguous diff is the caller's problem and returns 400 `VALIDATION_ERROR`, never an
`*_INCOMPLETE` code.

## Health And Repositories

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/health` | Process health |
| GET | `/api/stats` | Index counts |
| GET | `/api/repos` | Repository list |
| GET | `/api/repos/{id}` | Repository detail |
| GET | `/api/admin/health` | Index health, audit metrics, refresh status |
| GET | `/api/admin/repos` | Repository worktree/index alignment |
| POST | `/api/admin/repos/{id}/fetch` | Fetch repository refs |
| POST | `/api/admin/repos/{id}/checkout` | Select a branch with `{ "branch": "main" }` |
| POST | `/api/admin/repos/{id}/parse` | Re-index; optional `resolve` |

Admin mutations operate on server-side repositories. Restrict access accordingly.
Alignment fields distinguish checked-out `currentBranch`/`headSha` from
`indexedBranch`/`indexedSha`. `workspaceBranch`/`workspaceCommit` and
`indexedCommit` are compatibility aliases. `indexAlignmentStatus` is `fresh`,
`stale`, or `unknown`; inspect its reasons rather than timestamp age alone.

`/api/stats` and repository detail/file counts use `workspaceId` or
`X-Tirion-Workspace` (default `default-main`) and count selected graph facts.
Archived or unpublished generations are excluded. `/api/stats` returns a flat
object, for example `{"repos":3,"files":412,"functions":5230,"classes":380,"endpoints":96}`;
`repos` counts repositories with at least one selected file in the workspace.
`/api/repos` remains the logical repository catalog, including entries with zero
selected files, so its length can exceed `stats.repos`. `GET /api/repos/{id}` is a
404 for a repository the selected workspace does not contain. `GET /api/admin/repos`
honors the workspace selection; Admin Health describes the default workspace,
including its cached HTTP/queue/data audit. Standalone offline audit
commands retain database-wide scope and are not evidence about one workspace's
currently selected graph.

## Workspaces

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/workspaces` | Workspace definitions, repos, active snapshots |
| POST | `/api/workspaces` | Create; `slug`, optional `name`, `description`, `from`, `default` |
| POST | `/api/workspaces/{slug}/default` | Select the default workspace |
| POST | `/api/workspaces/{slug}/index` | Index a workspace |
| POST | `/api/workspaces/{slug}/bulk-checkout` | Apply a ref or mainline mode across repos |
| POST | `/api/workspaces/{slug}/bulk-index` | Start a selected-repository index job |
| GET | `/api/workspaces/{slug}/bulk-index/active` | Read bulk-index job status |
| POST | `/api/workspaces/{slug}/repos/{repo}/ref` | Set target `ref` |
| POST | `/api/workspaces/{slug}/repos/{repo}/fetch` | Fetch refs |
| POST | `/api/workspaces/{slug}/repos/{repo}/checkout` | Prepare selected worktree |
| POST | `/api/workspaces/{slug}/repos/{repo}/index` | Index selected repository |

Indexing and checkout are mutations, not discovery queries. Request structs and
job status fields are defined in
`internal/api/handlers/workspaces.go` in the source checkout.

Filesystem locations are server configuration, not request input. A request body
that sets `root` or `parseBinary` (workspace index) or `worktreeRoot` (repository
checkout and bulk checkout) is rejected with 400 `VALIDATION_ERROR`. The server
reads repositories under `TIRION_REPOS_ROOT`, loads the `parse` and `extract-*`
helpers from beside the `tirion` executable, and creates worktrees under
`TIRION_WORKTREE_ROOT`; both are described under
[Repositories and worktrees](../SETUP.md#repositories-and-worktrees).

## Search And Source Relationships

| Method | Path | Inputs |
| --- | --- | --- |
| GET | `/api/search` | Required `q`; optional `repo`, `mode`, `limit`, offsets, `noNoise` |
| POST | `/api/search/integrations` | `functionCallerIds`, `classIds` |
| GET | `/api/functions/data-access` | `callerId`; unknown in the workspace is a 404 `NOT_FOUND` |
| GET | `/api/functions/integrations` | `callerId`; unknown in the workspace is a 404 `NOT_FOUND` |
| GET | `/api/classes/integrations` | Class `id` |
| GET | `/api/endpoints` | Optional `repo`, `method`, `path` (substring), `q` (substring of path or handler name), `limit`, `offset` |

Search modes are `keyword` (default) and `trigram`; any other value is a 400
`VALIDATION_ERROR`. `limit` defaults to 10 and is capped at 50. Result buckets
cover functions, classes, type symbols, endpoints, data entities, external symbols,
schedules, GraphQL operations, Azure triggers, and queue hits. Bucket-specific
offsets are `functionsOffset`, `classesOffset`, `typeSymbolsOffset`,
`endpointsOffset`, `dataEntitiesOffset`, `externalSymbolsOffset`,
`schedulesOffset`, `graphqlOperationsOffset`, `azureTriggersOffset`, and
`queueHitsOffset`; `offset` is the fallback.

`noNoise` defaults to `true`, which hides generated and test boilerplate from the
function and endpoint buckets. Pass `noNoise=false` (or `0`) to include every
indexed match. `repo` follows the [repository filter](#repository-filters) rules:
an unknown repository is a 404 `NOT_FOUND`, while a known one with no matches
returns an empty successful result. A search that cannot complete returns
`SEARCH_INCOMPLETE` (503, or 504 on a deadline) rather than an empty result.
`%`, `_`, and `\` in `q` match literally in every bucket, and the schedules bucket
is searched in `trigram` mode as well as `keyword` mode. In `keyword` mode, a
query with no matches falls back to trigram matching; `mode` in the response
reports the mode that produced the results.

Read each bucket's `returned`, `total`, `exactTotal`, `hasMore`, and `truncated`.
Two further fields can appear in `stats.buckets.*`: `filteredCount`, the matches
`noNoise` removed from that bucket, and `capped`, set when the bucket's source
query stopped at its fetch limit. Type-symbol, queue-hit, and schedule buckets
read at most 500 rows per source; when that limit is reached, `capped` is true,
`exactTotal` is false, `truncated` and `hasMore` are true, and `total` is a lower
bound. Messages about hidden `noNoise` matches and capped buckets are listed in
the response `warnings`. `workspaceHints` (matches that exist only in other
workspaces) come from a separate bounded lookup and do not consume result caps.
A result page is not the entire matching estate.

The integrations batch returns `functions` and `classes` keyed by caller/class
ID. Successful empty lookups are explicit empty arrays. Failed items are omitted
from those maps and listed in `errors` as `{kind, id, code: "LOOKUP_FAILED"}`;
do not interpret a missing item as proof of no integrations. Successful items
remain usable when another item fails.

## Trace, Flow, And Impact

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/api/trace` | Upstream and downstream function traversal |
| POST | `/api/trace/expand` | Expand a node using `callerId` and `direction` |
| POST | `/api/flow` | Cross-service hops and source-linked narrative steps |
| POST | `/api/impact` | Dependencies around changed functions/files/ranges |
| GET | `/api/graph/path` | Paths between indexed nodes |
| GET | `/api/graph/repo-dependencies` | Repository dependency graph |

Example trace request:

```json
{
  "workspaceId": "default-main",
  "function": "ResourceController.create",
  "depth": 4,
  "resolve": true,
  "noTests": true,
  "maxNodes": 2000
}
```

Use `match` to select a `qualified_id` from an ambiguous trace result.
Trace returns `matches`, `downstream`, `upstream`, `stats`, and `completeness`.
Node evidence distinguishes direct, resolved, and inferred relationships.
Upstream traversal is breadth-first, so a caller reachable by several paths
appears at its shortest depth. It lists at most 50 callers at the first level and
20 per parent below it; when that fan-out cap or a failed caller lookup cuts
results, `completeness.upstream.truncated` is true. Check it before reading the
absence of a caller as meaningful. `POST /api/trace/expand` returns 404
`NOT_FOUND` for a `callerId` outside the selected workspace.

Flow requires `start`: a function, endpoint path, `METHOD /path`, `queue:NAME`,
`job:ClassName`, or `scheduled:ClassOrMethod`. `%`, `_`, and `\` in `start` are
matched literally. Controls include `depth`,
`maxHops`, `strictMode`, and `includeRelatedEntities`. Results include `roots`,
`hops`, optional `narratives`, `stats`, `completeness`, and `assumptions`.
Data hops connect indexed accesses; they do not establish runtime value flow.
When a written entity has no resolved reader endpoint, `candidates` preserves
route/repository-name discovery within the selected snapshots. Each item includes
`entity`, the writer `from`, a source-located `endpoint`, and a match `reason`.
Candidates are separate from `hops`, hop counts, narratives, and traversal roots:
name similarity is not a reader connection. An indexed entity read and a caller
path to an endpoint put that endpoint in data hops on a subsequent lookup.
Candidate lookup truncation or failure is reported in `warnings`.
Repository-receiver/read-method name matches are also returned as candidates
unless an indexed read/caller path already resolves the endpoint. They do not
become data hops merely because their method name resembles a read operation.

Impact accepts functions, caller IDs, function IDs, files, ranges, or a diff. The
`tirion impact` and `tirion verify` commands and `impact-report` forward the
original diff to the API, which maps it to indexed functions.
File/range entries include `path` and optional `repo`; ranges also include
`startLine` and `endLine`. An entry without its own `repo` uses the top-level
`repo`; an entry's `repo` resolves case-insensitively like the top-level one
(unknown is a 404). With no repo at all, a path matches every repository of the
workspace that indexes it. Controls include `depth`, `maxNodes`, `noTests`,
`resolve`, repository filters, and `includeTrace`. Results include `roots`,
`summary`, `report`, `stats`, and `completeness`.

The top-level `repo` is optional for Impact and Verify. It names one repository:
it resolves case-insensitively to the stored name, like `GET /api/contracts/{repo}`
(no match or a `*` wildcard is a 404 `NOT_FOUND`; two names differing only by case
are a 400 `AMBIGUOUS_REPO` unless one is spelled exactly), and file, range, and diff
entries are then matched against that stored name.
With a `diff` and no `repo`, each diff path is attributed to the indexed
repository that contains it. A path found in several repositories is resolved by
the repositories that the rest of the diff identifies uniquely; if that still
leaves several, the request fails with 400 `VALIDATION_ERROR` and
`details.file` / `details.candidates`, and the caller passes `repo`.

A diff may be `git diff` output or plain `diff -u` output covering several files,
with or without `diff --git` headers; blank context lines whose leading space was
stripped, `\ No newline at end of file` markers, and tab-separated timestamps are
accepted. Hunk line counts are enforced. Text that is not a unified diff, a hunk
before any file header, or a hunk body that disagrees with its header is a 400
`VALIDATION_ERROR`; `IMPACT_INCOMPLETE` (503) is reserved for server-side
failures. Edits are mapped in pre-change (index) coordinates. A newly added file
has no indexed counterpart. Documentation and static assets add
`added file X not indexed (informational)`; any other added file adds `added file
X not indexed yet; it has no indexed consumers (informational)`. Neither causes a
coverage gap for Impact, since new code has no indexed consumers yet. Binary, mode-only, and
rename-only changes, and edits outside indexed functions, still produce coverage
warnings.

Impact resolves function names and endpoint function IDs against active workspace
snapshots. Endpoint query or row-decoding failures return HTTP 503 with code
`IMPACT_INCOMPLETE`; callers should not treat that response as an empty impact.
Azure timer details follow their source file's snapshot. Imported EventBridge
schedules are global records, not repository-snapshot facts.

Graph path queries require `from` and `to`, which must be numeric function IDs
(a non-numeric value is a 400 `VALIDATION_ERROR` with `details.parameter`), with
optional `fromType`, `toType`, and `maxDepth`. Only function-to-function paths are
supported. Use identifiers from graph results rather than display labels.

For bounded responses, inspect `truncated`, truncation reasons, and
`exactAvailableNodes` before treating counts as totals. Increasing traversal
limits cannot repair missing indexed edges.

## Contracts And Framework Views

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/contracts` | Per-repository summaries; optional `q`, `limit`, `includeEmpty` |
| GET | `/api/contracts/{repo}` | Service boundary; optional `limit`. `{repo}` resolves case-insensitively; no match is a 404 `NOT_FOUND`, two names differing only by case a 400 `AMBIGUOUS_REPO` |
| GET | `/api/graphql/operations` | GraphQL documents |
| GET | `/api/graphql/usages` | Document usage sites |
| GET | `/api/graphql/entrypoints` | Backend registrations |
| GET | `/api/graphql/controllers` | Controller links |
| GET | `/api/graphql/flow` | Operation/frontend/backend lookup; at least one of `operation`, `frontendRepo`, `backendRepo` |
| GET | `/api/azure/functions` | Azure trigger declarations |
| GET | `/api/azure/flow` | Trigger flow lookup |

Contracts return `services` for summaries or `service` for repository detail.
Details include HTTP endpoints/calls, counterparties, GraphQL declarations,
data accesses, queue producers/consumers, and schedules. An unmatched HTTP call
is not proof of an external service. GraphQL entrypoint token matching is an
inference, not a resolved call edge.

Queue relationship query failures return HTTP 503 with `CONTRACTS_INCOMPLETE`
instead of an apparently empty queue boundary.

The GraphQL and Azure list routes take the `repo` filter described under
[repository filters](#repository-filters) and the `limit`/`offset` bounds in
[Pagination](#pagination); their `q`, `name`, and similar text filters are
case-insensitive literal substrings. `GET /api/graphql/flow` adds `truncated`, the
names of lists cut short (`operations`, `usages`, `entrypoints`, `resolvers`,
`controllers`) by the response limit or an internal scan ceiling, and a matching
entry in `warnings`. When `truncated` is absent every list is complete for the
filters used; when present, narrow the filters or raise `limit` before concluding
that a usage or resolver does not exist.

## Change Verification

`POST /api/verify` requires `repo` and a unified `diff`; `workspaceId` and
`baseSha` identify the intended indexed context. `baseSha` is compared with the
workspace's indexed commit and must be at least 7 hexadecimal characters
(either side may be abbreviated; case is ignored); a shorter or non-hex value is
reported as an alignment problem. The diff formats, error handling, and
added-file behavior are those described for Impact above. Optional `anchor`
requests graph-derived boundary checks:

```json
{
  "workspaceId": "release",
  "repo": "resource-api",
  "baseSha": "<indexed-base-sha>",
  "diff": "<unified-diff>",
  "anchor": {
    "kind": "symbol",
    "repo": "resource-api",
    "ref": "resource-api:src/Repository.cs:Repository.Save"
  },
  "requireAnchor": true,
  "waivers": []
}
```

Anchor kinds are `symbol`, `endpoint`, `queue`, and `entity`. A symbol/endpoint
anchor must be outside the changed symbol set. Waivers record `subject`,
`justification`, and optional `class`. Legacy `agentWork` submissions remain
accepted. Input semantics, obligations, compatibility behavior, and verdicts are
defined in [change verification](#change-verification).

The `verify` block contains `verdict`, `mode`, `reasons`, `changeValidation`,
`breakingChanges`, `exposedSurface`, and optional legacy `agentWork` results.
These are structural checks, not certification that a task was implemented.
Parameter removals and type changes carry `severity: "high"`; detected handler
deletions carry `severity: "critical"`. Annotation names do not escalate severity
or establish an HTTP response code. Queue lookup failures and truncated entity
accessor results mark verification incomplete through `queryErrors` and reasons.
Present a structural contract finding from `changeType`, `severity`, and
`evidence`, not from custom annotation names.

`GET /api/verify/runs` returns `{"workspace": {...}, "runs": [...]}` with stored
result metadata for one workspace, chosen like every other route (`workspaceId`,
then `X-Tirion-Workspace`, then `default-main`; an unknown workspace is a 404).
`runs` is `[]` when there are none, and `limit` is bounded as described under
[Pagination](#pagination). It does not replay full diffs or obligation ledgers.
Activity counts do not measure bugs prevented.

Implementation: `internal/api/handlers/verify.go`, `verify_diff.go`, `verify_frontier.go`, and `verify_obligations.go`.

### Interfaces

| Interface | Current input model |
|---|---|
| `POST /api/verify` ([HTTP API](server-api.md#change-verification)) | Repository diff; optional boundary anchor, waivers, and legacy `agentWork` submission |
| MCP `codebase_verify` ([MCP](mcp.md#codebase_verify---check-a-diff-against-indexed-relationships)) | Graph-oriented API fields, including `anchor` and `requireAnchor` |
| `tirion verify` ([CLI](cli-reference.md#tirion-verify-legacy-evidence-submission-interface)) | Diff plus required legacy `-work-file`; no anchor flag |
| Guard `verify_diff` ([MCP](mcp.md#verify_diff---legacy-evidence-checks-from-the-local-checkout)) | Legacy task/root-cause/claims fields; no anchor field |
| `GET /api/verify/runs` ([HTTP API](server-api.md#change-verification)) | Stored verdict and exposure metadata |

The interfaces do not yet expose identical inputs. Documentation must not imply that the CLI or guard supports anchor-based checks.

### Inputs And Preconditions

A request supplies a repository, workspace, and unified diff. `baseSha` identifies the diff base when provided; it is compared with the workspace's indexed commit, either side may be abbreviated, and it must be at least 7 hexadecimal characters (case-insensitive). A shorter or non-hex value, or a commit that differs from the indexed one, is an alignment problem and yields `warn`.

#### Diff Input

The diff may come from `git diff` or from plain `diff -u`, covering one or many files, with or without `diff --git` headers. Blank context lines whose leading space was stripped, `\ No newline at end of file` markers, and tab-separated timestamps are accepted. Hunk line counts are enforced exactly.

- Text that is not a unified diff, a hunk with no file header, an invalid hunk header, or a hunk body that disagrees with its declared counts is rejected with HTTP 400 `VALIDATION_ERROR`. `VERIFY_INCOMPLETE` and `IMPACT_INCOMPLETE` (HTTP 503) are returned only for server-side failures such as a database error.
- Verify requires `repo`. Impact does not: with a diff and no `repo`, each path is attributed to the indexed repository of the selected workspace that contains it, using paths found in only one repository to disambiguate the rest. A path that remains ambiguous is a 400 `VALIDATION_ERROR` with `details.file` and `details.candidates`; pass `repo` to choose.
- Edits are mapped to indexed functions in pre-change coordinates. An edit that no indexed function fully covers, or a path no indexed repository contains, is a coverage gap and makes the check `warn`.
- A newly added file has no pre-change counterpart and nothing is indexed for it yet, so none of its code is checked. Added documentation and static assets (`.md`, `.txt`, images, fonts, `LICENSE`, and similar) are reported as `added file X not indexed (informational)` and do not lower the verdict. Any other added file is a coverage gap, `added file X is not indexed yet; its code was not checked`, so the verdict is at most `warn` until the file is indexed. A diff that only adds documentation can pass; supply `baseSha` so workspace alignment can still be checked.
- Binary, mode-only, and rename-only changes carry no text edits to map; each is reported as a coverage gap and warns.

Verify checks the workspace worktree state, compares available SHA metadata, and compares context/removed lines against the worktree preimage. Changed symbols are derived from actual edits in preimage coordinates, not caller-supplied Impact targets or unchanged hunk context.

Preimage file access is confined to the workspace directory through `os.Root`.
For symbolic links, validation reads the recorded link target rather than the
destination file. New-file preimages use `Lstat`, so an existing dangling link is
not mistaken for an absent file.

The graph is the indexed representation of those sources. Alignment checks do not measure the completeness of extraction or establish production deployment state.

Use a separate clean server-side baseline checkout when validating edits from an
agent's working tree. Keep that workspace indexed at the diff base, generate the
diff from the agent checkout, and send its full base SHA. Do not reindex the
modified checkout as the baseline before submitting the diff. In particular,
`git diff --cached` covers staged edits and `git diff HEAD` covers both staged and
unstaged tracked edits; neither includes new untracked files. Generate new-file
diffs explicitly or stage those files in your own development workflow.

The MCP `diff` fallback is an unstaged local diff, not a diff fetched from the
server's workspace. An explicit diff avoids ambiguity when client and server
have different checkout paths.

### Graph-Oriented Checks

Without an anchor, the mode is `contracts_only` and `fixValidation` is `not_performed`. `requireAnchor=true` makes an absent anchor a `not_verified` result.

With an anchor, the mode is `fix_validation`. An anchor identifies an indexed symbol, endpoint, queue, or entity. A symbol/endpoint anchor inside the changed symbol set is rejected as a validation boundary.

The engine collects direct predecessors independently and follows a bounded reverse frontier through supported indexed relationships. Traversal uses resolved function identities rather than the exploratory trace's pending-name fallback. HTTP, queue, DI, schedule, and entity-write handling depend on the available indexed facts.

| Check | What it establishes |
|---|---|
| O1: `fix_connects_to_boundary` | At least one changed symbol lies in the indexed reverse frontier of the anchor |
| O2: `equivalent_predecessor` | Each direct typed predecessor was changed or has a caller-supplied waiver |
| O5: `expected_boundary_coverage` | Whether changed code reaches the supplied boundary under the current check |

These are existing API identifiers. "Equivalent" means sharing a typed edge into the boundary, not equivalent business behavior. "Covered" means symbol-level accounting, not a correct implementation. Read the actual `class` fields in returned obligations.

### Contract And Exposure Checks

The contract layer detects supported endpoint-handler deletions, parameter removals, and parameter-type changes from a diff. Indexed external consumers affect the resulting verdict.

Handler deletions have `critical` severity; parameter removals and type changes
have `high` severity. Annotation names do not escalate severity or imply an
HTTP 400 response, and framework or custom attribute names alone do not
establish request-binding behavior.

It also reports endpoint consumers, queue counterparties, and other readers/writers of shared entities. Exposure alone is informational.

The current engine does not provide general response-schema compatibility, queue-payload compatibility, coordinated multi-repository patch validation, field-value propagation, or behavioral validation.

### Obligations And Verdicts

Obligation statuses are `satisfied`, `missing`, `contradicted`, `waived`, and `not_verified`.

- `satisfied`: the current structural check is met.
- `waived`: a justification was recorded; Tirion does not judge its business correctness.
- `not_verified`: the check could not be completed, including invalid anchors, clipping, or query failures.

Verdicts summarize the checks performed:

- `fail`: a missing/contradicted obligation, a supported contract break with indexed consumers, or a contradictory legacy submission.
- `warn`: an incomplete check (including a coverage gap in the diff), alignment problem, or supported contract delta without indexed external consumers. Informational notes, such as added documentation, never cause `warn`; added source or configuration files do.
- `info`: dependency exposure or a waiver, without a higher-severity result.
- `pass`: no reported check requires a higher-severity result.

No verdict certifies that a bug is fixed, all callers are known, or an application behaves correctly. A change to a symbol can satisfy O2 without fixing its behavior. Conversely, an untouched predecessor may be compatible and warrant accounting rather than an edit.

### Legacy Evidence Submissions

`agentWork` contains task text, a root-cause statement, source references, and requirement claims.

The legacy checks resolve references, compare them with changed ranges, check graph connectivity, and report unaccounted production files. They do not evaluate the meaning of the task, the claimed root cause, or the correctness of a requirement implementation.

When no anchor is supplied, legacy submission checks participate in the verdict if `agentWork` is present. With an anchor, graph-derived obligations own that part of the verdict. Legacy fields remain for compatibility, not as the product's primary workflow.

### Stored Results

`verify_runs` records verdict, mode, counts, reasons, and duration metadata. It is not a complete replay artifact and does not store the full obligation ledger or diff.

Run counts and failures are activity measurements, not measurements of bugs prevented or engineering time saved.

### Validation Status

Existing test and regression sources document particular cases; their presence does not establish complete graph or detector coverage. Consult actual execution results for a revision before claiming it has passed validation. This reference does not authorize running tests or builds contrary to repository instructions.

