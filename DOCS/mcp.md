# MCP Reference

Tirion exposes its indexed graph to coding agents over local stdio MCP servers.
The binaries call a Tirion HTTP API, which can run on the same machine or a shared
host. They do not spawn a coding agent or require a hosted model account.

## Connect

Follow [SETUP.md](../SETUP.md) to build the binaries and index repositories. Use
`mcp-intel` for the current investigation and graph-derived verification tools.
It reads configuration from environment variables and serves stdio immediately;
there is no `config set` subcommand or persistent configuration helper.

A client that uses the `mcpServers` JSON convention can use this structure:

```json
{
  "mcpServers": {
    "tirion": {
      "command": "/absolute/path/to/tirion/bin/mcp-intel",
      "env": {
        "TIRION_API_URL": "http://localhost:8080",
        "TIRION_WORKSPACE": "default-main",
        "TIRION_REPOS_ROOT": "/absolute/path/to/repos",
        "TIRION_WORKDIR": "/absolute/path/to/repos/your-repo"
      }
    }
  }
}
```

Use your client's documented configuration location and format; do not assume
every IDE reads the same JSON file. On Windows, use the absolute `.exe` path with
JSON-escaped backslashes. No command arguments are required. Keep local paths and
credentials out of committed MCP configuration.

For a shared deployment, use its protected HTTPS API origin rather
than a raw backend port. The client appends `/api/...` to that origin. Authenticated
`mcp-intel` requests support Basic Auth or an explicit Authorization header, and
all requests additionally require the Tirion API token. The variables are listed in
[MCP and clients](../SETUP.md#mcp-and-clients); the token rules (local token
file for loopback URLs, HTTPS plus an explicit token otherwise, no redirects) are in
[Authentication and state paths](../SETUP.md#authentication-and-state-paths).

`mcp-intel` resolves the token lazily, on each API call, and caches it once found.
It therefore starts and answers `initialize` and `tools/list` even if `tirion serve`
has never run and no token file exists yet. Until a token can be resolved, each API
tool returns an error result (`isError: true`) that explains the fix: start
`tirion serve` once on this machine so it creates the token file, or set
`TIRION_API_TOKEN` / `TIRION_API_TOKEN_FILE` in the MCP server's environment. Fix
it and retry the tool call; no restart is needed. If the API answers 401, the
cached token is dropped and re-read on the next call, so a rotated token file is
picked up. The index stays on the Tirion host, but query
results can include source code and repository metadata. The connected agent
receives that content; its own provider and data-handling settings apply.

## Agent Workflow

Search for a starting point, follow relevant connections with Trace or Flow, and
inspect dependencies through Impact or Contracts. Read cited source/configuration
before reasoning about behavior. Choose the workspace matching the revisions being
investigated.

Use `codebase_verify` for supported change checks. Its verdict is not a task-completion
certificate; do not change unrelated code just to obtain a passing result. The
agent owns reasoning, tool selection, and implementation. There is no required
phase loop or fixed number of calls.

See [concepts](../README.md#how-tirion-works) for the investigation workflows and the
[verification contract](server-api.md#change-verification).

## Tools

The server's `tools/list` response is authoritative for argument schemas. The
tables below describe the main graph tools. `mcp-intel` also exposes source/diff
helpers (`code_search`, `code_trace`, `code_read`, `git_diff`) and GraphQL flow
lookup; workspace/source availability still determines which evidence can be read.

All graph tools accept `workspaceId`; an explicit tool argument overrides
`TIRION_WORKSPACE`, otherwise the API default applies when both are absent.
This does not change the local checkout used by source/diff helpers.

`code_read` and `git_diff` need local roots. `code_read` reads local source, not a
remote indexed snapshot. All paths, including
absolute `path` arguments and `repoPath` + `file`, must stay inside explicitly
configured `TIRION_REPOS_ROOT` or `TIRION_WORKDIR` (absolute directories, not a
filesystem root). Symlinks cannot escape those
roots. When both settings are absent, `code_read` and `git_diff` (and Verify's
automatic local diff) return an error naming those variables; the graph tools do
not need them. Relative paths require `TIRION_WORKDIR`. An invalid root (relative,
missing, or a filesystem root) stops `mcp-intel` at startup. Only regular files up to 16 MiB are accepted; the output
byte limit applies to the selected line range.

`git_diff` requires an actual Git worktree root within those same directories.
It rejects outside/parent paths and Git pathspec magic, disables external diff,
text conversion, hooks and fsmonitor, and runs with a 30-second deadline. Its
`maxBytes` limit retains a bounded output prefix, marked `truncated: true` when
clipped. Request narrower file selections for a complete diff. Verify's automatic
local diff requires explicit `TIRION_WORKDIR` and rejects diffs above 2 MiB rather
than validating a partial patch. Git's own repository configuration is trusted;
use normal clones and do not adopt untrusted prebuilt `.git` directories.
MCP messages are limited to 8 MiB.

### `tirion-intel` — Code Exploration

These tools query the selected workspace's indexed repositories with bounded results.

#### `codebase_search` — Find code

Ask about functions, classes, endpoints, or EventBridge schedules by name.

**Example prompts:**
- "Search for `UserService.createUser`"
- "Find the endpoint that handles `/api/projects/{id}/members`"
- "Search for anything related to `billing` in `billing-worker`"

**What it returns:** Matching symbols and other result buckets with repository and
source locations, connectivity metadata and any available indexed source. Text
summaries and structured results are bounded; use a source read for the full body.

**Parameters:**
| Param | Required | Default | Description |
|-------|----------|---------|-------------|
| `query` | Yes | — | Function name, class name, endpoint path, or keyword |
| `repo` | No | all repos | Repository name, matched case-insensitively and exactly, or as a prefix with a trailing `*` (`orders-*`). Never a substring. A name that matches no repository in the workspace is an error, not an empty result |
| `limit` | No | 10 | Max results (up to 50) |
| `mode` | No | `keyword` | Search mode: `keyword` or `trigram` |
| `noNoise` | No | true | Hide generated and test boilerplate from function and endpoint results; `false` includes every indexed match |

Per-bucket `capped` and `filteredCount` statistics and the response `warnings`
(for example, matches hidden by `noNoise`) are part of the structured results; a
`capped` bucket's total is a lower bound.

Structured results retain the API's requested result limit. The text summary
shows a smaller selection; use the structured results for the remaining hits.
Repository-wide context metadata is omitted from search responses.

#### `codebase_trace` — Follow call chains

See who calls a function (upstream) and what it calls (downstream), including across service boundaries via HTTP and SQS.

**Example prompts:**
- "Trace `CatalogController.listItems` — show me all callers and callees"
- "What calls `EmailCampaignService.sendCampaign`?"
- "Trace `BillingService.processPayment` with depth 6"

**What it returns:** A tree of upstream callers and downstream callees, with cross-service edges (HTTP calls, SQS queues) marked. Shows DI resolution for Java (interface → implementation). Upstream callers are listed breadth-first (a caller appears at its shortest depth) with a bounded fan-out per node; when that bound or a failed lookup cut the result, `completeness.upstream.truncated` is true.

**Parameters:**
| Param | Required | Default | Description |
|-------|----------|---------|-------------|
| `function` | Yes | — | Fully qualified function name (e.g., `CatalogController.listItems`) — use `codebase_search` first to find the exact name |
| `depth` | No | 4 | How many levels deep to trace |
| `noTests` | No | true | Exclude test files from results |
| `resolve` | No | false | Resolve cross-service HTTP/SQS edges (richer but slower) |
| `maxNodes` | No | 2000 | Cap the trace graph size |

#### `codebase_flow` — End-to-end business flows

See how a request flows from entry point through services, queues, and downstream handlers.

**Example prompts:**
- "Show the end-to-end flow for forgot password"
- "What happens when a resource is created through the web form?"
- "Show the flow starting from `ResourceController.create`"

**What it returns:** An ordered list of hops showing how data flows across services — through direct calls, HTTP endpoints, and SQS queues.

**Parameters:**
| Param | Required | Default | Description |
|-------|----------|---------|-------------|
| `start` | Yes | — | Starting function name (e.g., `ResourceController.create`) |
| `depth` | No | 3 | Max traversal depth (use 5 for richer results) |
| `maxHops` | No | 200 | Max number of hops to return |
| `noTests` | No | true | Exclude test files |

#### `codebase_impact` — Blast radius analysis

Before changing code, see what could break — affected entrypoints, repos, queues, and HTTP calls.

**Example prompts:**
- "What's the impact of changing `UserService.createUser`?"
- "Show the blast radius of modifying `ProjectService.java` lines 100-200"
- "What gets affected if I change `orders-api/src/main/java/com/example/billing/BillingService.java`?"

**What it returns:** Affected entrypoints (API endpoints that could be impacted), repositories touched, SQS queues in the blast radius, HTTP calls affected, and EventBridge schedules.

**Parameters (provide at least one of `functions`, `ranges`, `files`, or `diff`):**
| Param | Required | Default | Description |
|-------|----------|---------|-------------|
| `functions` | One of four | — | Function names to analyze, e.g., `["UserService.createUser"]` |
| `ranges` | One of four | — | File ranges: `[{"path": "src/Foo.java", "startLine": 10, "endLine": 50, "repo": "my-repo"}]` |
| `files` | One of four | — | Whole files: `[{"path": "src/Foo.java", "repo": "my-repo"}]` |
| `diff` | One of four | — | Unified diff (`git diff` or plain `diff -u`, one or many files); changed hunks are mapped to indexed functions. A diff that cannot be parsed is an error. Newly added files are not mapped: documentation and static assets are informational notes, other added files are listed as not indexed yet |
| `repo` | No | inferred from diff paths | Repository name for the paths, matched case-insensitively (no `*` wildcard); supply it only if a diff path is ambiguous across indexed repositories, in which case the error lists the candidates |
| `depth` | No | 4 | Traversal depth |
| `maxNodes` | No | 2000 | Cap the impact graph |
| `noTests` | No | true | Exclude test files |

#### `codebase_graphql_flow` - GraphQL boundaries

Requires `operation`, the exact indexed operation or resolver name. Optional
`frontendRepo` and `backendRepo` narrow either side; `limit` defaults to 25
(maximum 100 per section). It returns indexed usages, resolvers, registrations
and controller links, not a runtime GraphQL execution trace.

#### `codebase_verify` - Check a diff against indexed relationships

With an `anchor`, the tool derives boundary-connectivity and direct-predecessor obligations. Without one, it checks selected contract deltas and reports dependency exposure. It does not validate the semantic correctness of a fix.

| Param | Required | Default | Description |
|-------|----------|---------|-------------|
| `repo` | Yes | - | Repository name for diff paths |
| `diff` | No | local git diff | Unified diff; the local fallback reads unstaged changes from the MCP workdir |
| `baseSha` | Recommended | - | Diff base SHA (at least 7 hex characters, case-insensitive) for workspace alignment checks |
| `workspaceId` | No | server default | Indexed revision context |
| `anchor` | No | - | Object with `kind` (`symbol`, `endpoint`, `queue`, `entity`), `ref`, and optional `repo` |
| `requireAnchor` | No | false | Return `not_verified` when no anchor is supplied |
| `waivers` | No | - | Objects with `subject`, `justification`, and optional `class` |
| `depth` | No | 4 | Traversal depth |
| `maxNodes` | No | 2000 | Graph budget |
| `noTests` | No | true | Exclude test files |

Legacy `task`, `rootCause`, `requirementsComplete`, `rootCauseEvidence`, and `claims` fields are still accepted. Without an anchor, supplying these fields invokes the legacy evidence checks in addition to contract checks.

The response contains `verify.verdict`, `changeValidation`, `breakingChanges`, `exposedSurface`, and reasons. `agentWork` is included for legacy submissions. A `pass` means the reported checks passed, not that all behavior or dependencies have been verified.

See [change verification](server-api.md#change-verification) for obligation and verdict semantics. Result metadata is available through `GET /api/verify/runs`.

#### `codebase_contracts` — Service boundaries

See a repo's HTTP endpoints, outgoing HTTP calls, GraphQL operations/usages/resolvers/permission rules, data and side-effect accesses, SQS queues produced/consumed, EventBridge/Azure timer triggers, and counterparty services.

**Example prompts:**
- "Show the contracts for `billing-worker`"
- "What SQS queues does `resource-api` produce and consume?"
- "What GraphQL operations does `web-app` use and which backend repos resolve them?"
- "What blob, document, or database side effects does `event-worker` touch?"
- "What services does `auth-service` talk to?"

**What it returns:** HTTP endpoints exposed, outgoing HTTP calls and matched target repos, GraphQL operations/usages/resolvers/permission rules and matched caller/target repos, data/side-effect accesses with caller/file/line evidence, SQS queues (produced and consumed), EventBridge schedules and Azure timer triggers, and counterparty services.

**Parameters:**
| Param | Required | Default | Description |
|-------|----------|---------|-------------|
| `repo` | Yes | — | Repository name (e.g., `event-worker`, `resource-api`), matched case-insensitively. Two repositories differing only by case produce an ambiguity error unless one matches exactly |

### `tirion-guard` — Change Risk Analysis

These tools analyze your local changes against the code graph.
Their fallback is plain `git diff`: unstaged tracked changes only. Staged changes
and new untracked files are not included automatically. Pass explicit diff text
from `git diff --cached` for staged changes or `git diff HEAD` for staged plus
unstaged tracked changes. The guard has no staged-mode switch.

#### `get_changed_ranges` — Parse your diff

Extract file/line ranges from a unified diff. If no diff is provided, the tool reads the current local `git diff`. Ranges use pre-change line numbers (the coordinates the index holds); newly added files yield no ranges, and a diff that cannot be parsed returns an error result.

**Example prompts:**
- "Show the changed ranges in my current diff"
- "Read this explicit staged diff and show its changed file/line ranges."

**Parameters:**
| Param | Required | Default | Description |
|-------|----------|---------|-------------|
| `diff` | No | — | Paste a unified diff. If omitted, reads from local git |
| `repo` | No | — | Repo name for tagging results |

#### `impact_from_diff` — Impact of your changes

Run graph impact analysis from a unified diff. If no diff is provided, the tool reads the current local `git diff` and posts it to `/api/impact`.

**Example prompts:**
- "What's the impact of this explicit staged diff?"
- "Analyze the blast radius of my current working tree changes"

**Parameters:**
| Param | Required | Default | Description |
|-------|----------|---------|-------------|
| `diff` | No | — | Unified diff text |
| `repo` | No | inferred from diff paths | Repository name; needed only when a diff path exists in several indexed repositories |
| `workspaceId` | No | active server default | Workspace context |
| `depth` | No | 4 | Traversal depth |
| `maxNodes` | No | 2000 | Cap the graph |
| `noTests` | No | true | Exclude tests |

#### `verify_diff` - Legacy evidence checks from the local checkout

This guard tool posts the legacy task/root-cause/claims submission to `/api/verify`, defaulting to the local git diff. It does not expose `anchor`, `waivers`, or `requireAnchor`; use `codebase_verify` for those inputs.

**Interpretation:** source-reference checks and dependency findings are inputs to review, not a task-completion certificate.

**Parameters:**
| Param | Required | Default | Description |
|-------|----------|---------|-------------|
| `diff` | No | — | Unified diff text |
| `repo` | Yes | — | Repository name |
| `task` | Yes | — | Original task/story text |
| `baseSha` | Recommended | — | Diff base Git SHA |
| `rootCause` | Yes | — | Concrete diagnosed mechanism |
| `requirementsComplete` | Yes | — | Whether all task requirements are represented |
| `rootCauseEvidence` | Yes | — | Root-cause source locations |
| `claims` | Yes | — | Requirement coverage/preservation claims |
| `workspaceId` | No | active server default | Workspace context |
| `depth` | No | 4 | Traversal depth |
| `maxNodes` | No | 2000 | Cap the graph |
| `noTests` | No | true | Exclude tests |

## Configuration

`mcp-intel` is configured entirely through environment variables, listed in
[MCP and clients](../SETUP.md#mcp-and-clients) along with the rules for
supplying them. Native MCP commands never read repository-local `.env` files; use
explicit process settings or `TIRION_ENV_FILE` (see
[Supplying Settings](../SETUP.md#supplying-settings)).

MCP-specific behavior:

- `TIRION_REPOS_ROOT` and `TIRION_WORKDIR` are the only local roots. Without one of
  them `code_read`, `git_diff` and Verify's automatic local diff return an error,
  and an invalid root stops `mcp-intel` at startup (see [Tools](#tools)).
- The API token is resolved lazily on each API call (see [Connect](#connect)).
- `TIRION_MCP_TIMEOUT_SECONDS` sets the HTTP timeout; see the variable table for its
  range and default.
- Attribution is opt-in: no actor header is sent unless `TIRION_ACTOR` or
  `TIRION_USER` is set.

The optional `mcp-guard` binary retains the three diff tools above. It follows
JSON-RPC 2.0 framing: requests without an `id` (notifications such as
`notifications/initialized`) receive no response, input that is not valid JSON is
answered with a `-32700` error whose `id` is `null`, and a batch (a JSON array on
one line) is answered with an array of responses, or nothing when it contained
only notifications. It uses
`TIRION_API_URL`, the shared API token settings, a fixed 60-second HTTP timeout,
and explicit `TIRION_WORKDIR` for automatic local Git diffs. Supply workspace through tool arguments. Use `mcp-intel` when the
shared proxy also requires Basic Auth or another Authorization header.

## Troubleshooting

- Tools absent: confirm the executable path and your client's stdio configuration.
  The binary must be launched by an MCP client; it waits for JSON-RPC on stdin.
- Requests fail: check the API origin and connectivity. The health endpoint is
  `/api/health`; the Tirion token is always required, in addition to any proxy authentication.
- "Tirion API token unavailable": the server started but could not find a token.
  Start `tirion serve` once (it creates `~/.tirion/api-token`), or set
  `TIRION_API_TOKEN` / `TIRION_API_TOKEN_FILE` in the MCP server's environment, then
  call the tool again. A non-loopback `TIRION_API_URL` must be HTTPS and needs an
  explicit token; the local token file is never sent there.
- Auth errors: verify credentials in the client process environment. An explicit
  Authorization header overrides Basic Auth. A 401 means the API rejected the token.
- Wrong or empty results: check workspace selection, indexed revisions, and query
  evidence before modifying code.
- Local diff missing: configure `TIRION_WORKDIR` to the intended repository, or
  supply an explicit diff.
- Timeouts: inspect the query and API logs; `mcp-intel` allows an HTTP timeout
  override through `TIRION_MCP_TIMEOUT_SECONDS`.

A developer connecting to an existing shared index does not need local PostgreSQL
or all estate repositories. A developer hosting their own index does: use the
[source setup guide](../SETUP.md). Native/release packaging and platform coverage
are documented in [operations](../SETUP.md#running-tirion-for-a-team).
