# Tirion Agent Notes

Guidance for coding agents and contributors working in this repository. Human
contributors should also read [CONTRIBUTING.md](CONTRIBUTING.md); setup is in
[SETUP.md](SETUP.md).

## Ground Rules

- Never put tokens, credentials, private source, or index dumps in the repository,
  its documentation, or issue text. Supply them through environment variables or a
  credential store outside Git.
- Tirion maps cross-repository connections and exposes source evidence. The
  engineer or agent owns reasoning and implementation.
- When Tirion MCP tools are configured, use them for evidence and validation, then
  edit code yourself in the active checkout.
- Use `codebase_verify` for supported contract and connectivity checks when assessing
  a change. Supply the relevant workspace, repository, diff base, and an independent
  boundary anchor when appropriate. Read the findings; do not edit code merely to
  obtain a passing verdict. A verdict does not establish behavioral correctness or
  task completion.

## Current Product

Tirion indexes source repositories into PostgreSQL and exposes graph-backed evidence:

- Search: find functions, classes, endpoints, files, and symbols.
- Trace: follow upstream/downstream call paths and cross-service hops.
- Flow: inspect service/runtime paths across HTTP, queue, and resolver boundaries.
- Impact: answer "what can this change affect?"
- Verify: check selected contract deltas, boundary connectivity, and direct-predecessor accounting.
- Contracts: inspect service boundary contracts.
- MCP: expose the same surfaces to IDE agents.

Not part of the product: Explain-AI, Ask/semantic embeddings, an AGE/Cypher runtime,
Triage/PR-review routes, and Tirion-owned agent orchestration.

## Main Commands

```bash
bash scripts/build-local.sh

export DATABASE_URL='postgres://tirion@localhost:5432/tirion?sslmode=disable'
./bin/tirion index -root /path/to/repos -workspace default-main -skip-unchanged=false
./bin/tirion serve -port 8080
```

Every API route requires the service token (`X-Tirion-Token` or
`Authorization: Bearer`). `tirion serve` creates it at `~/.tirion/api-token`; see
[the HTTP API reference](DOCS/server-api.md#authentication). For local API checks,
use `curl` or another HTTP client with that header.

## Important Paths

- `cmd/tirion` — primary CLI: `index`, `serve`, workspace controls, search/trace helpers.
- `cmd/mcp-intel` — environment-configured MCP stdio server.
- `internal/api` — route registry and HTTP middleware (authentication, Host/CORS, limits).
- `internal/api/handlers` — HTTP API handlers for retained graph surfaces.
- `internal/mcpintel` — MCP tool schemas and handlers.
- `internal/runtimeconfig` — environment loading, API token handling, subprocess environment filtering.
- `internal/parser` — language parsers and extraction logic.
- `internal/trace` — call graph and cross-boundary traversal.
- `internal/graph` — SQL storage/schema layer.
- `frontend` — web UI.

## Development Notes

- Keep scope centered on cross-repository relationships. Search and presentation
  surfaces support that job; they are not separate product initiatives.
- Prefer improving graph extraction and resolver quality over adding story-specific
  text heuristics.
- Keep new behavior source-backed: store facts in the graph, then let
  Search/Trace/Flow/Impact consume the same facts.
- Do not add new tests before the implementation is confirmed to behave as
  intended, unless the task explicitly asks for converting/removing test-only
  tooling. Database-backed suites are opt-in; see "Live Database Tests" in
  [CONTRIBUTING.md](CONTRIBUTING.md).
- Use `rg` for source searches.
- Keep distribution docs and scripts aligned with the actual shipped binaries, and
  update the affected documentation in the same change as the code.
