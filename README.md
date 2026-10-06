# Tirion

Tirion maps connections across repositories so engineers and their coding agents can answer: **how does this part of the system connect to the rest, and what other code should we consider when changing it?**

It indexes source and supported configuration into a PostgreSQL-backed graph, linking callers, service endpoints, queue producers and consumers, schedules, and data accessors. Queries return source references and revision context for investigation and change coordination.

The focus is the connections between codebases, not standalone code search or automated implementation. Search finds a starting point; Trace and Flow follow connections; Impact, Contracts, and Verify help assess changes. Tirion does not run coding agents or certify that an agent fixed a bug. The graph is a static model of indexed sources, not a production execution recording, and relationship coverage varies by language and framework.

Development focuses on the accuracy and usability of existing cross-repository relationships: extraction, resolution, provenance, snapshot alignment, and focused query results. Ticket-specific ranking rules and new product categories are out of scope.

## Quick Start

[SETUP.md](SETUP.md) covers prerequisites (Go, a C compiler, PostgreSQL 16 with `pg_trgm`, Node.js 24 or newer) and explains each step below. Tirion never guesses a database, so `DATABASE_URL` must be set in every terminal that runs it.

```bash
# 1. Point every command at a dedicated database.
export DATABASE_URL='postgres://tirion@localhost:5432/tirion?sslmode=disable'

# 2. Build every native command, including the indexer's required helpers.
bash scripts/build-local.sh

# 3. Index a directory with one repository per child folder. Flags go before the root.
./bin/tirion index -root /path/to/repos -workspace default-main -skip-unchanged=false

# 4. Start the API (the UI is a separate process).
./bin/tirion serve -port 8080
```

In a second terminal, start the UI and open the URL Vite prints:

```bash
cd frontend && npm ci && npm run dev
```

The UI asks for the API token on first load. `tirion serve` creates it at `~/.tirion/api-token` (under `TIRION_HOME` if set); read it with `cat ~/.tirion/api-token`.

To connect a coding agent, point your MCP client at the absolute path of `bin/mcp-intel` and set `TIRION_API_URL` and `TIRION_WORKSPACE` in its `env` block. See [MCP](DOCS/mcp.md#connect).

## How Tirion Works

Search, Trace, Flow, Impact, Contracts, and Verify are views and checks over the same indexed model. MCP, the web UI, the CLI, and the HTTP API are access mechanisms for them.

### Surfaces

#### Find a starting point

Search locates indexed symbols, endpoints, queues, and entities. It supports keyword and trigram matching, not semantic task interpretation.

- API: `GET /api/search`
- CLI: `tirion search`
- MCP: `codebase_search`

Use it to choose a concrete starting point for traversal, not as evidence that the highest-ranked result is the cause of a bug.

#### Follow connections

Trace exposes detailed upstream and downstream relationships. Flow emphasizes service and resource boundaries. Supported extraction paths include HTTP, queues, GraphQL, schedules, and data-access relationships; coverage depends on the indexed language and framework.

- API: `POST /api/trace`, `POST /api/flow`
- CLI: `tirion trace`
- MCP: `codebase_trace`, `codebase_flow`, `codebase_graphql_flow`
- UI: Trace and Flow

These are static relationships, not recorded production executions. Inspect source references, relationship evidence, resolution confidence, and clipping when following a path.

#### Assess a change

Impact gathers reachable entrypoints and dependencies for symbols, files, or a diff. Contracts lists a repository's inbound and outbound boundaries and indexed counterparties.

- API: `POST /api/impact`, `GET /api/contracts`
- CLI: `tirion impact`, `impact-report`, `contracts-report`
- MCP: `codebase_impact`, `codebase_contracts`
- UI: Impact and Contracts

Use these to identify related code, coordinate changes, and decide what to inspect or exercise. A listed dependency is exposure, not an assertion that it will break.

#### Check specific relationships

Verify applies selected contract checks and graph-derived obligations to a repository diff. With a boundary anchor, it checks indexed connectivity and whether direct predecessors were changed or explicitly waived.

- API: `POST /api/verify`
- MCP: `codebase_verify`
- Legacy submission interfaces: `tirion verify`, guard `verify_diff`
- Result metadata: `GET /api/verify/runs`

Changing a predecessor satisfies a symbol-level coverage check, not a behavioral assertion. Sharing a boundary does not establish that all callers need the same fix. Verify does not certify task completion or replace source review and behavior checks.

See [change verification](DOCS/server-api.md#change-verification) for the current contract and interface differences.

### Workspaces and snapshots

Workspaces select the repository refs and snapshots used by a query.

- UI workspace selector and Admin
- API `workspaceId` or `X-Tirion-Workspace`
- CLI `-workspace` (see [workspace commands](DOCS/cli-reference.md#tirion-workspace))
- MCP `TIRION_WORKSPACE`

Use the workspace that corresponds to the release being investigated. An indexed workspace is not automatically a record of what is deployed.

### Indexed inputs

The native pipeline includes Java, JavaScript/TypeScript (including Vue), C#, Go, RPG/SQLRPGLE, CL, DDS and IBM i binder source. It also extracts selected facts from GraphQL documents, SQL, PowerShell, Terraform, MyBatis XML, Entity Framework metadata, Azure Functions/host/APIM configuration and resource configuration. These are language/framework-specific extractors, not uniform whole-program compilation for every input.

Public framework handling includes Spring/JPA, supported HTTP clients, JavaScript callbacks, Vue/Pinia, GraphQL and queue integrations. Custom wrappers and routing aliases must be configured from the deployment's actual source; see [`patterns.yaml`](SETUP.md#patterns-yaml). An unsupported syntax diagnostic or unresolved target should guide source inspection, not be treated as an absent dependency. Reindex after changing extraction configuration.

#### Route and client detection by language

- **JavaScript/TypeScript and Vue.** A call such as `x.get('/path', handler)` is a route registration or an outbound HTTP call depending on its receiver. The receiver is classified from imports and factory calls of known server libraries (Express, Fastify, Koa router, Hono, Elysia, Restify, Polka, itty-router), with a call-shape fallback when it cannot be resolved in the file. A name such as `api` is not treated as a router, a registration is never also recorded as an outbound call, and settings getters like `app.get('port')` are not endpoints. Chained `.route(path)` verbs, `fastify.route({method, url, handler})`, and template-literal paths that do not start with a substitution are recognized; handler names are found through wrappers, `.bind`, and middleware arrays. Vue single-file components are parsed in both `<script>` and `<script setup>`, with the TSX grammar for `lang="tsx"` and `lang="jsx"`. File extensions match case-insensitively. Minified or bundled scripts and well-known vendored libraries (jQuery, lodash, Angular, Moment, Backbone, d3, React builds) are skipped.
- **C#.** Minimal-API `MapGroup` prefixes are applied to the routes beneath them; local functions are named `Class.Method.Local`; an attributed constructor is not an endpoint.
- **Java.** `@FeignClient`, `@HttpExchange`, `@RegisterRestClient`, Retrofit, and Micronaut `@Client` interfaces describe outbound clients only. Their method paths include the interface-level base path (`@HttpExchange("/api")`, `@Path`, `@RequestMapping`, or `path=`), so `@GetExchange("/users")` records `/api/users`. An API interface that a controller implements attributes its endpoints to the controller.
- **SQL.** Function-call `FROM` clauses are not table reads, dollar-quoted bodies and several routines per file (or per `GO` batch) are separated, `DELETE` is a write only, and quoted or bracketed identifiers are resolved.

#### Directories and test paths

Directory skipping is one rule for the parser, the extractors, and repository discovery: dependency and tool directories (`node_modules`, `vendor`, `third_party`, `.git`, `.hg`, `.svn`, `.idea`, `.vscode`) are always skipped, and build-output names (`build`, `dist`, `target`, `out`, `bin`, `obj`, `coverage`) only beside a build manifest. Test paths are recognized by directory and file-name conventions (including `TestFoo.java`, `*IT`, `*.test.mjs`, and `__mocks__`); a Java `ProductSpec` is not a test. See [`parse`](DOCS/cli-reference.md#parse-core-indexer) for the details of `-skip-tests`, timeouts, and text decoding, and [repository discovery](DOCS/cli-reference.md#repository-discovery) for the full skip list.

### Workflows

#### Investigate a cross-repository path

1. Select the workspace for the relevant repository revisions.
2. Start from a known function, route, queue, or entity. Use Search if you need to locate it.
3. Use Flow for service boundaries or Trace for detailed callers and callees.
4. Read the cited source and configuration to determine what each connection means.
5. Follow additional connections where the evidence requires it.

A graph path describes indexed connectivity. Runtime conditions, configuration, and the actual request determine which path executes.

#### Coordinate a change

1. Run Impact from the affected symbols or diff.
2. Inspect callers, endpoint consumers, queue counterparties, and shared data accessors.
3. Use Contracts to inspect the boundaries of the related repositories.
4. Determine which consumers need changes and which behavior needs checking.
5. Use Verify for supported contract and connectivity checks.

Exposure does not require an edit by itself. A caller can remain compatible with a change, and two callers of the same boundary can have different behavior.

#### Investigate an incident

Use concrete functions, files, or routes from logs as Search/Trace starting points. Follow the indexed connections into other repositories and inspect their source.

Tirion does not ingest the production request or identify a root cause from a stack trace alone, and it has no Triage workflow.

#### Work with an IDE agent

The agent can use [MCP](DOCS/mcp.md) to retrieve cross-repository evidence, read relevant source, and implement changes in its own checkout.

Example requests:

- "Use Tirion to find the consumers of this endpoint across the workspace."
- "Use Tirion to follow this queue to its indexed handlers and their downstream calls."
- "Use Tirion to assess the dependencies affected by this diff."

Do not impose a fixed number of tool calls or require the agent to fill in artifacts until a verdict passes. Follow the evidence needed for the task; use local reads for details already located.

For Verify, prefer the graph-oriented API or `codebase_verify`. The CLI and guard use the legacy evidence-submission format. Neither format checks the semantic correctness of the fix.

### Interpreting results

- Confirm the repository, revision, file, and symbol.
- Distinguish resolved relationships from heuristic matches.
- Check traversal depth, truncation, and query errors before treating a result set as exhaustive.
- Use source and configuration to assess branches and values.
- An absent indexed edge is not proof that no runtime dependency exists.
- A Verify verdict covers its reported checks, not the entire task.

## Documentation

- [SETUP.md](SETUP.md): install from source, the
  [configuration reference](SETUP.md#configuration-reference),
  [running Tirion for a team](SETUP.md#running-tirion-for-a-team),
  [upgrading](SETUP.md#updating-and-upgrading), and troubleshooting.
- [DOCS/cli-reference.md](DOCS/cli-reference.md): every command and flag.
- [DOCS/server-api.md](DOCS/server-api.md): HTTP API, including the
  [change verification](DOCS/server-api.md#change-verification) contract.
- [DOCS/mcp.md](DOCS/mcp.md): MCP client setup and tools.
- [CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md): contributing and vulnerability reporting.

## License

Tirion is licensed under [Apache-2.0](LICENSE). It requires no license key or activation. Third-party components retain their own licenses; see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
