# Contributing To Tirion

Tirion is distributed under [Apache-2.0](LICENSE). Contributions intentionally submitted for inclusion are licensed under those terms unless explicitly stated otherwise. Only submit work you have the right to contribute; retain third-party attribution and identify its applicable license. Participation is governed by [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

## Scope

Contributions should improve cross-repository evidence: extraction, relationship resolution, source provenance, revision and snapshot alignment, traversal, useful result sizes, and the usability of existing tools. Search, Trace, Flow, Impact, Contracts, Verify, and MCP consume that shared model, so an improvement to it should benefit all of them. Ticket-specific ranking rules and new product categories are outside this scope.

For substantial changes, describe the source pattern or user workflow first. Explain the missing graph fact and which existing consumers benefit. Avoid customer-specific repository lists, ticket vocabulary, or special-case ranking rules. Do not weaken checks or remove coverage to obtain a passing result.

Dependency changes must update the corresponding notices in `THIRD_PARTY_NOTICES.md` and `frontend/public/THIRD_PARTY_NOTICES.md`. Preserve complete copyright and license texts, including bundled components. The frontend notice file is copied into web builds by Vite's public-directory handling; do not remove it from distributed assets.

For concepts and terminology, see [How Tirion Works](README.md#how-tirion-works); the documentation map is in the [README](README.md#documentation).

## Work Locally

Follow [SETUP.md](SETUP.md) to build all native commands, prepare PostgreSQL, and start the API/UI or MCP client. `go.mod` and `frontend/package-lock.json` define the toolchain and dependency inputs. Do not rely on pre-existing root binaries.

Use a dedicated database and clones for integration work. Workspace checkout operations modify those clones; do not point them at someone else's active work. No access to the maintainer's private repositories or index is required to propose a change.

### Development Commands

```bash
# Build every native command into bin/ (tirion indexes with the parse and extract-* helpers beside it).
bash scripts/build-local.sh

# Backend compile check.
go build ./cmd/...

# Build a single command.
go build -o <binary> ./cmd/<binary>

# Focused backend tests.
go test ./internal/parser ./internal/trace ./internal/mcpintel ./internal/api/handlers

# Full Go suite (needs no database).
go test ./...

# Frontend production build; it also type-checks.
cd frontend && npm ci && npm run build
```

These are contributor commands, not steps performed during setup.

### Repository Map

- `cmd/tirion` — primary CLI: index, serve, workspace controls, search, trace.
- `cmd/mcp-intel` — environment-configured MCP stdio server.
- `cmd/` — the remaining native commands (`parse`, `extract-*`, `audit-index`, `impact-report`, `contracts-report`, `mcp-guard`, `release-sign`); see the [CLI reference](DOCS/cli-reference.md).
- `internal/parser` — source parsers and extractors.
- `internal/graph` — PostgreSQL schema/storage.
- `internal/trace` — graph traversal and boundary following.
- `internal/api/handlers` — HTTP API surfaces.
- `internal/mcpintel` — MCP tool schemas and handlers.
- `frontend` — web UI.
- `scripts` — build, packaging, refresh, and regression scripts.
- `testdata` — parser fixtures and the portable estate fixture.
- `DOCS` — CLI, HTTP API, and MCP references (setup, configuration, and operations are in `SETUP.md`).

## Prepare A Change

Keep the implementation focused and update the affected API/CLI/MCP documentation in the same change. Preserve existing interfaces unless the change explains a migration. Parser fixtures must be synthetic or have documented redistribution permission; never paste private customer code into a public fixture or issue.

Describe behavior with a small source example and expected graph relationships, not only an implementation narrative. For example, identify the caller and endpoint that should connect, their repository boundaries, and the source evidence.

Some integration tests and operator scripts need PostgreSQL or an indexed estate. State exactly which checks ran, skipped, or were unavailable. A successful build does not establish cross-repository behavior. In agent-assisted work, explicit owner restrictions on test creation and execution take precedence over the commands above.

### Live Database Tests

The database-backed Go suites are opt-in; without the variables below they skip, and `go test ./...` needs no database. They create and modify tables, so point them only at a disposable database created for this purpose, never at a database holding an index you care about:

| Variable | Purpose |
| --- | --- |
| `DATABASE_URL` | Connection URL of the disposable database (required by every live suite) |
| `TIRION_WORKSPACE_LIVE_TESTS=1` | Enables the workspace isolation, search, and contracts suites in `internal/api/handlers` |
| `TIRION_PIPELINE_LIVE_TESTS=1` | Enables the end-to-end indexing pipeline suites in `internal/indexer` |
| `TIRION_TEST_BIN` | Absolute path of the directory holding the native commands (`parse`, `extract-*`, ...) built by `scripts/build-local.sh`, normally `$PWD/bin`; required by the pipeline suites |
| `TIRION_REQUIRE_LIVE_DB=1` | Turns a skipped live suite into a failure, so a missing opt-in, database, or binary directory cannot pass silently |

```bash
bash scripts/build-local.sh
export DATABASE_URL='postgres://tirion@localhost:5432/tirion_tests?sslmode=disable'
export TIRION_WORKSPACE_LIVE_TESTS=1 TIRION_PIPELINE_LIVE_TESTS=1
export TIRION_TEST_BIN="$PWD/bin" TIRION_REQUIRE_LIVE_DB=1
go test -p 1 ./...
```

Run the packages serially (`-p 1`): they share the one database. CI does the same in the `parser-fixtures` job of `parser-fixture-gate.yml`, against a throwaway PostgreSQL 16 service container, with all of the variables above set (including `TIRION_REQUIRE_LIVE_DB=1`), after building the helpers with `scripts/build-local.sh`. Leave `TIRION_REQUIRE_LIVE_DB` unset for ordinary local runs where skips are acceptable, and say in your pull request which suites ran.

The `estate` CI job and `scripts/estate-fixture-check.mjs` are a separate live check against a running API; they read the API token from `TIRION_API_TOKEN`, `TIRION_API_TOKEN_FILE`, or the local token file (see [the fixture README](testdata/estate-fixtures/README.md) and [Regression Checks](#regression-checks)).

## Regression Checks

### Portable Contribution Checks

The `Portable Checks` workflow runs the Go suite, frontend build, and a disposable PostgreSQL-backed estate. No customer repositories, credentials, or paid model calls are required. The local commands are above.

The [HTTP fixture](testdata/estate-fixtures/README.md) contains a Java API and a TypeScript consumer in separate repositories. After indexing clean copies into a dedicated database and starting its API, run:

```bash
BASE_URL=http://localhost:8080 WORKSPACE_ID=default-main \
  node scripts/estate-fixture-check.mjs /path/to/clean/catalog-api
```

It checks:

- A cross-repo HTTP Flow hop from the consumer to the provider.
- Contract failures for parameter removal, parameter type change, and handler deletion.
- Informational exposure, not a contract failure, for a body-only edit.
- External consumer file/line evidence and aligned diff preimages.

The check does not mutate the fixture repositories. It covers these behaviors, not every supported framework or every product surface. Add a redistributable reproduction for the behavior being changed; do not weaken assertions to pass.

### Estate-Specific Search Checks

Operators can check their own indexed corpus with the reusable search checker:

```bash
SEARCH_API_URL=http://localhost:8080/api/search \
  bash scripts/search-golden-check.sh /private/operator-data/queries.tsv
```

Alternatively set `SEARCH_GOLDEN_MANIFEST` to that path. Each tab-separated row contains `id`, `mode`, `query`, `bucket`, `target`, and `max_rank`; comment lines start with `#`. Supported buckets are functions, classes, endpoints, and schedules. The target is the exact symbol name, endpoint path, or schedule rule name expected within the requested rank. `max_rank` is sent as the search `limit`, so use an integer from 1 to 50 (the API rejects non-integers and clamps larger values). Keep customer identifiers and source outside this repo.

Both runners use curl by default. Set `HTTP_CLIENT` to a curl-compatible wrapper (one that accepts curl's `-K` config-file option) when required by the execution environment. Both send the API token: `TIRION_API_TOKEN`, else the file named by `TIRION_API_TOKEN_FILE`, else the local file `tirion serve` created at `~/.tirion/api-token` (or under `TIRION_HOME`). The local file is used only when the API URL is loopback; remote estates need an explicit token variable. The API must point at the intended estate; a missing expected result is a failure, not a reason to silently skip it.

### Private Operator Coverage

The portable fixture does not replace every private scenario. Maintain private dogfood separately, with explicit input paths and credentials outside Git. Do not make fork contributions depend on access to that estate.

When reporting validation, name the command, indexed baseline, and asserted behavior. A build, an empty result, or an unexecuted script is not evidence of cross-repository correctness.

## Submit A Pull Request

Include the problem, the behavioral change, evidence, and any compatibility or schema implications. Explain new dependencies. Do not include credentials, private source, database dumps, local MCP settings, or generated binaries.

Portable Checks configures a disposable PostgreSQL service and enables the opt-in workspace and pipeline Go suites. Test packages run serially against that shared disposable database. It also runs the Go suite, frontend build, Linux/Windows native packaging, MCP executable startup/protocol smoke checks, and a fresh PostgreSQL-backed synthetic estate check. Packaging checks do not sign or publish artifacts. The MCP smoke checks require no database, API server or model. These jobs need no repository secrets. Estate-backed Impact reporting is an optional integration and is not required for fork PRs. Do not introduce secrets or private-service dependencies into ordinary contribution checks.

Use factual validation results. Verify reports graph/contract findings; it is not proof that every requirement of a bug report was implemented correctly.

## Reports And Support

Use a minimal redistributable example for ordinary bugs. See [SECURITY.md](SECURITY.md) for sensitive reports. The project does not promise a support SLA or a commitment to merge every proposal.
