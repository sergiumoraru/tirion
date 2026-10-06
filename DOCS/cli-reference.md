# CLI Reference for Tirion Tools

This document lists every command and flag of the native binaries. Each binary's `-h` output is the authoritative flag set. To build the binaries, see [SETUP.md](../SETUP.md). Database-backed commands read `DATABASE_URL` (or `-db`); API/MCP clients instead connect to the server. For environment variables and defaults, see [configuration](../SETUP.md#environment-variables).

Put flags before positional arguments. Go's flag parser stops at the first positional argument, so `tirion` rejects a flag placed after one (for example `tirion index /repos -skip-unchanged=false`) with exit status 2 and a `flags must come before positional arguments` message instead of silently ignoring it. `parse` likewise rejects anything after its repository path, and the `extract-*` helpers, which take only flags, reject any positional argument. Boolean flags use `-flag=false` to override a true default; `-flag false` is not equivalent with Go's flag parser. A flag may be written with one or two dashes (`-root` or `--root`).

Examples below use `./tirion`, which assumes the current directory contains the built binaries, as in an extracted release bundle. From a source checkout use `./bin/tirion`.

Database selection is explicit for every command that connects: set `DATABASE_URL` or pass `-db`. There is no implicit database.

## Command index

- `tirion`: primary operator CLI (`index`, `serve`, `workspace`, `search`, `trace`, `impact`, `verify`, `sync-branches`, `refresh-repo-graph`, `prune-snapshots`, `prune`, `version`).
- `mcp-intel`: environment-configured MCP stdio server; takes no flags. See [MCP](mcp.md).
- `mcp-guard`: legacy diff-oriented MCP server; takes no flags. See [MCP](mcp.md).
- `parse`: core indexer (PostgreSQL-backed graph).
- `audit-index`: post-parse quality audit (duplicates, unresolved HTTP, queue/data coverage).
- `extract-*`: backfill utilities (Java/Spring/HTTP/SQS/TS) and the EventBridge schedule importer.
- `impact-report`, `contracts-report`: API-backed report exporters.
- `release-sign`: optional release checksum signing utility, not a runtime dependency.

## `tirion` (primary operator CLI)

The primary operator entrypoint.

Usage:

```bash
./tirion <subcommand> [flags]
```

Subcommands:

- `index`: runs the operator indexing pipeline
- `workspace`: manages selected refs, worktrees and indexing per workspace
- `search`, `trace`, `impact`, `verify`: query the running API
- `sync-branches`: applies Admin-selected repo branches to a repos root before indexing
- `refresh-repo-graph`: rebuilds repository dependency materializations from indexed facts
- `prune-snapshots`: lists (default) or deletes superseded and interrupted snapshot generations
- `prune`: lists (default) or deletes index entries for missing registered repository paths
- `serve`: starts the HTTP API; the web UI is served separately
- `version`: prints the Tirion build version (takes no flags)

The query subcommands (`search`, `trace`, `impact`, `verify`) and the report tools send the API token on every request: they use `TIRION_API_TOKEN` or `TIRION_API_TOKEN_FILE`, and for a loopback API URL on any port they fall back to the local token file (`~/.tirion/api-token`, or under `TIRION_HOME`). A non-loopback API URL must be HTTPS with an explicit token. See [the HTTP API reference](server-api.md#authentication).

### `tirion index`

Usage:

```bash
./tirion index [flags] [repos-root]
```

| Flag | Default | Description |
| --- | --- | --- |
| `-root` | current directory | Directory whose child folders are candidate repos (a positional `repos-root` also sets it). See [repository discovery](#repository-discovery). |
| `-workspace` | `default-main` | Workspace slug to index. Only `default-main` is created on demand; any other slug must already exist (`tirion workspace create <slug>`), otherwise the command fails and lists the existing slugs. |
| `-skip-unchanged` | `true` | Skip clean repos at their indexed HEAD. Set `-skip-unchanged=false` after parser or extraction-configuration changes. |
| `-db` | `DATABASE_URL` | PostgreSQL connection string. |
| `-parse-bin` | `parse` beside the `tirion` executable | Explicit path to `parse`. |
| `-skip-tests` | `true` | Skip test/spec files during parsing. |
| `-global-resolve` | `true` | Resolve every eligible cross-repo call once all candidates are enriched. With `-global-resolve=false`, publication only repairs cross-repo links that already exist. |
| `-exclude` | none | Comma-separated repo directory names to skip. |
| `-skip-extractors` | `false` | Skip the post-parse extractor pipeline (stop after batch parse and global resolve). |
| `-extractors` | `http,sqs,java-intel,java-calls,spring,ts-intel` | Comma-separated extractor set to run after indexing. |
| `-dry-run` | `false` | Print discovered repos without indexing. It does not connect to the database, but it still resolves the `parse` binary and every selected extractor helper and fails if one is missing or an extractor name is unknown (`-skip-extractors` skips the helper check). |
| `-v` | `false` | Print helper command lines before execution. |
| `-allow-file-failures` | `false` | Publish even when files lost all facts to a parser timeout or internal error. By default such a run fails and the previous snapshot stays active; an allowed snapshot is never reused by `-skip-unchanged`. |

Behavior:

- batch parse via the shared indexer
- extractor pipeline by default
- final workspace cross-repo resolution and materialization

The run prints the repos root, the `parse` binary, an `Extractor helpers:` line naming the resolved helpers, and a mode line such as `Mode: skip-tests=true, workspace-resolve=full` (`repair-only` when `-global-resolve=false`). The closing summary lists a `Warning:` line for each discovery problem described below.

`tirion index` runs the extractor phase through the helper binaries `extract-http`, `extract-sqs`, `extract-java-intel`, `extract-java-calls`, `extract-spring`, and `extract-ts-intel`. Helpers are looked up beside the `tirion` executable, not on `PATH` (`-parse-bin` points at a different `parse`). Keep `tirion`, `parse`, and the `extract-*` helpers in the same directory; a missing `parse` or helper binary stops the run with a message to build every command with `bash scripts/build-local.sh`. A helper that lacks `-repo` or candidate support returns an upgrade error instead of rerunning across the whole workspace, so rebuild the complete helper set together. Database credentials are passed through `DATABASE_URL`, not helper command arguments or verbose command output.

Each `parse` or helper process is bounded to 2 hours. A helper that fails with a transient database or network error (for example connection refused or reset, server starting up, deadlock or serialization failure, name-resolution failure) is retried once after a short pause; any other failure, including a timeout, is not retried. Cross-repo resolution of a bare function name only links functions within the same language family (for example Java/Kotlin/Scala, JavaScript/TypeScript/Vue, C#, or IBM i RPG/CL); a JavaScript `handler()` never binds to a Go function.

Batch indexing builds unpublished snapshot generations and selects them only after the requested pipeline and incoming relationship repair succeed. It never implicitly passes `parse -reset-all`. Previous selected facts remain available on failure; other repositories and workspaces remain in the database. The standalone parser's explicit `-reset-all` option is still destructive.

With `-skip-unchanged`, matching HEAD alone is insufficient: the checkout must also be clean and the selected snapshot must record a completed matching pipeline, binaries, configuration and metadata/include inputs. A partial or failed run cannot satisfy a later default-pipeline skip. Local tracked or untracked changes cause the repository to be parsed again, and so does a repository whose source directory has moved since it was indexed.

Use `-exclude` for worktree containers or other folders you do not want indexed.

Examples:

```bash
./tirion index /srv/tirion/repos
./tirion index -dry-run -root /srv/tirion/repos
./tirion index -root 'C:\tirion\repos' -exclude app,tmp
```

#### Repository discovery

- Each non-hidden child directory of the root is a repository when it has a manifest (`package.json`, `go.mod`, `pom.xml`, a Gradle build or settings file), Terraform or C# project files near its top, or, when it contains no nested repositories, indexable source within four levels. A directory that is not itself a repository is searched two levels deeper for nested ones, which are named `parent/child`.
- Symlinked directories are followed. The stored path is the resolved directory and the name is the link name; a link and its target are indexed once, and links that loop back to an ancestor are skipped with a warning. Below the root's own entries, a link is followed only if it resolves inside the root, so a checked-out folder cannot pull outside directories into the index. A link that resolves inside another discovered repository is skipped with a warning (its files would be indexed twice), and `-exclude` also applies to a link's target name.
- In a folder that contains nested repositories, source outside them is not indexed; the summary warns and names the files. Give such source its own manifest or move it into a nested repository.
- An unreadable or broken directory is reported as a warning and discovery continues. Only an unreadable root is an error.
- `vendor`, `vendors`, `third_party`, `third-party`, `node_modules`, `bower_components`, virtual-environment and cache directories (`.venv`, `venv`, `__pycache__`, `.tox`), `.next`, `.nuxt`, and the version-control and editor directories (`.git`, `.hg`, `.svn`, `.idea`, `.vscode`) are always skipped. The build-output names `build`, `dist`, `target`, `out`, `bin`, `obj`, and `coverage` are skipped only beside a build manifest (`package.json`, `pom.xml`, Gradle, `go.mod`, `Cargo.toml`, `Makefile`, `CMakeLists.txt`, or a `.csproj`/`.fsproj`/`.vbproj`/`.sln`), so Go `internal/build` or a Java `com.acme.external` package is still indexed. `parse` applies the same rule when it walks a repository.

### `tirion workspace`

These commands use PostgreSQL directly, not the API. First index the source clones so their repository names and paths are registered. Creating a workspace or setting a ref does not fetch, check out, or index source automatically. See [Workspaces and snapshots](../README.md#workspaces-and-snapshots) for what a workspace selects.

Usage:

```bash
./tirion workspace <list|create|set-ref|fetch|checkout|index> [flags] [arguments]
```

| Command | Arguments | Flags |
| --- | --- | --- |
| `workspace list` | none | `-db` |
| `workspace create` | `SLUG` | `-db`, `-name NAME`, `-description TEXT`, `-from WORKSPACE`, `-default` |
| `workspace set-ref` | `SLUG REPO REF` | `-db` |
| `workspace fetch` | `SLUG REPO` | `-db` |
| `workspace checkout` | `SLUG REPO` | `-db`, `-worktree-root PATH` |
| `workspace index` | `SLUG` | `-db`, `-root PATH`, `-worktree-root PATH`, `-repo REPO`, `-skip-tests`, `-skip-unchanged`, `-global-resolve`, `-exclude`, `-parse-bin`, `-v` |

What each command does:

- `list`: lists workspaces and active-snapshot counts.
- `create`: creates the workspace. `-name` sets the display name, `-description` the description, `-default` marks it as the default workspace, and `-from` copies the repo selections of an existing workspace (not a fresh index).
- `set-ref`: records the intended ref for a repository.
- `fetch`: fetches the registered source clone.
- `checkout`: prepares the selected ref under `PATH/SLUG/repos/REPO`, where `PATH` is the worktree root.
- `index`: parses, enriches and resolves. `-root` defaults to the workspace's managed `repos` folder (`<worktree-root>/<slug>/repos`). The bulk options `-skip-tests` (default `true`), `-skip-unchanged` (default `true`), `-global-resolve` (default `true`), `-exclude`, `-parse-bin`, and `-v` have the same meaning as for [`tirion index`](#tirion-index). The single-repo `-repo` path uses its recorded worktree and always reparses it; bulk parser-selection and skip options do not apply to that path.

`-db` on every subcommand takes the PostgreSQL connection string (default `DATABASE_URL`). The worktree root defaults to `TIRION_WORKTREE_ROOT`, then legacy `TIRION_WORKSPACES_ROOT`, then the OS temporary directory's `tirion-workspaces`. Set a persistent root for a deployment.

Example for a registered `orders-api` repository with an existing `origin/release` ref:

```bash
export TIRION_WORKTREE_ROOT=/path/to/tirion-worktrees
./tirion workspace create -from default-main release
./tirion workspace set-ref release orders-api origin/release
./tirion workspace fetch release orders-api
./tirion workspace checkout release orders-api
./tirion workspace index -repo orders-api release
./tirion search -workspace release -repo orders-api OrderController
```

Repeat ref/checkout/index for each repository needed in that workspace. For bulk indexing, omit `-repo`.

### `tirion search`

Usage:

```bash
./tirion search [flags] <query>
```

| Flag | Default | Description |
| --- | --- | --- |
| `-api-url` | `http://localhost:8080` | Tirion API base URL. |
| `-workspace` | `default-main` | Workspace slug to query. |
| `-repo` | none | Repository filter. |
| `-limit` | `10` | Maximum results. |
| `-mode` | `keyword` | Search mode: `keyword` or `trigram`. |

Quote multiword queries, or supply them as trailing positional words.

```bash
./tirion search -workspace default-main -repo orders-api -limit 10 OrderController
```

### `tirion refresh-repo-graph`

`./tirion refresh-repo-graph` rebuilds repository dependency edges from existing database facts; it does not parse changed source. It accepts only `-db`. Use the normal index pipeline when source or parser configuration changes.

### `tirion sync-branches`

Usage:

```bash
./tirion sync-branches [flags] [repos-root]
```

| Flag | Default | Description |
| --- | --- | --- |
| `-root` | current directory | Repos root to constrain branch application (a positional `repos-root` also sets it). |
| `-db` | `DATABASE_URL` | PostgreSQL connection string. |
| `-fetch` | `false` | Fetch selected repos before checkout. |
| `-mainline` | `false` | Check out each repo under the root to its remote default branch instead of the DB-selected branch. |

Behavior:

- reads `selected_branch` from Tirion's DB
- finds matching repos under the given root
- checks those repos out to the selected branch
- exits non-zero if any selected repo could not be synced

This bridges Admin repo branch selection and the nightly repo refresh / indexing. Typical nightly usage:

```bash
./scripts/clone-repos.sh --org your-org --root /data/tirion/repos --fetch-existing-branches
./tirion sync-branches -db "$DATABASE_URL" /data/tirion/repos
./tirion index -db "$DATABASE_URL" /data/tirion/repos
```

### `tirion prune`

Lists, or with `-apply` deletes, the index entries of one workspace whose registered source directory no longer exists under the supplied root. It does not remove repository directories from disk.

```bash
./tirion prune -root /srv/tirion/repos
./tirion prune -root /srv/tirion/repos -apply
```

| Flag | Default | Description |
| --- | --- | --- |
| `-root` | current directory | Directory under which registered repository paths are checked, at any depth, so nested repositories (`group/service`) are included. A positional `repos-root` also sets it. |
| `-workspace` | `default-main` | Workspace to prune; it must exist. |
| `-apply` | `false` | Delete. Without it the command only lists what it would remove and prints `nothing removed; re-run with -apply to remove`. |
| `-db` | `DATABASE_URL` | PostgreSQL connection string. |
| `-dry-run` | `false` | Deprecated, because listing is the default. It cannot be combined with `-apply`. |

Only that workspace's selections and snapshots are removed, and never a snapshot that another workspace still selects or depends on; the output names snapshots that were kept and why, and says when the repository record itself is removed because no workspace uses it. Repositories registered outside the root, relative registered paths, and existing paths (including symlinks) are left alone. Inspection errors other than "not found" stop the command instead of treating an unreadable path as a deleted repository, and `-apply` refuses to run when the root is empty (for example an unmounted volume), which would make every repository look deleted. The command fails with a "busy" error while any workspace is indexing or being pruned; retry afterwards.

### `tirion prune-snapshots`

Removes superseded and interrupted snapshot generations. It is a dry run unless `-apply` is given; the dry run prints one tab-separated candidate per line (`id`, workspace, repository, creation time) and a count.

```bash
./tirion prune-snapshots
./tirion prune-snapshots -older-than 720h -keep 2 -apply
```

| Flag | Default | Description |
| --- | --- | --- |
| `-older-than` | `720h` (30 days) | Minimum generation age; at least `24h`. |
| `-keep` | `2` | Successful generations to retain per workspace and repository; at least `1`. |
| `-apply` | `false` | Delete the eligible generations atomically. Without it, only candidates are listed. |
| `-db` | `DATABASE_URL` | PostgreSQL connection string; `DATABASE_URL` must identify the database explicitly. |

Values below the minimums, or positional arguments, exit with an error and change nothing. Active and mainline selections in every workspace, and facts referenced by retained snapshots, are protected. The command refuses to run while a workspace is indexing; run it between indexing runs. For retention guidance, see [operations](../SETUP.md#upgrading-from-earlier-builds).

### `tirion serve`

Usage:

```bash
./tirion serve [flags]
```

| Flag | Default | Description |
| --- | --- | --- |
| `-db` | `DATABASE_URL` | PostgreSQL connection string. |
| `-port` | `PORT`, else `8080` | HTTP port. |
| `-host` | `TIRION_HOST`, else `127.0.0.1` | Bind address. Shared deployments also require an explicit `TIRION_ALLOWED_HOSTS` setting and API authentication; see [access control](../SETUP.md#access-control). |

Example:

```bash
./tirion serve -port 8080
```

On SIGINT or SIGTERM the server stops accepting connections and gives in-flight requests up to 30 seconds to finish; requests still running then are cancelled and the process exits. A second signal ends it immediately.

Startup connects to the database before creating the API token file, and an invalid `TIRION_MAX_CONCURRENT_READS` / `TIRION_MAX_CONCURRENT_ADMIN`, Host, or origin setting stops it with an error.

## `tirion impact` (change impact via API)

Posts the original diff (or function names) to `/api/impact` and writes the JSON report to stdout or a file. See [Assess a change](../README.md#assess-a-change) for the Impact workflow.

Usage:

```bash
./tirion impact [flags]
```

Provide `-functions`, `-diff-file`, or pipe a unified diff on stdin.

| Flag | Default | Description |
| --- | --- | --- |
| `-api-url` | `http://localhost:8080` | API base URL. |
| `-workspace` | `default-main` | Workspace slug to query. |
| `-diff-file` | none | Unified diff file, sent to the server unchanged. `-diff-file=-` reads stdin; omitting it and piping input works too. |
| `-functions` | none | Comma-separated function names (optional alternative to a diff). |
| `-repo` | none | Exact repo name for the diff paths. Without it the server attributes each path to the indexed repo that contains it, and asks for `-repo` only when a path exists in several repos. |
| `-depth` | `4` | Trace depth. |
| `-no-tests` | `true` | Hide nodes from test files. |
| `-resolve` | `true` | Resolve DI/implementation edges. |
| `-exclude` | none | Comma-separated substrings to exclude. |
| `-include-repo` | none | Comma-separated repos to restrict to. |
| `-exclude-repo` | none | Comma-separated repos to exclude. |
| `-max-nodes` | `2000` | Maximum nodes returned. |
| `-out` | stdout | Write output JSON to a file. |

Generate the diff with `git diff main...HEAD > diff.patch`; plain `diff -u` output over several files also works. Context lines are fine. The server maps edits in the diff's pre-change coordinates, so deletions are covered. A diff the server cannot parse is rejected (HTTP 400) and newly added files are reported as informational.

Examples:

```bash
git diff main...HEAD | ./tirion impact -repo=storefront -out impact.json
./tirion impact -diff-file diff.patch -repo orders-api
./tirion impact -functions EmailCampaignJobAction.run,UsersController.create
./tirion impact -functions ProjectService.listResources -depth 3
```

## `tirion verify` (legacy evidence-submission interface)

Legacy evidence-submission interface. It posts a unified diff and a legacy `agentWork` submission to `/api/verify`. It checks source-reference consistency and selected contract changes; it does not determine whether the task was implemented correctly.

This CLI requires `-work-file` and `-repo`, and has no anchor or `--require-anchor` flag. Use `codebase_verify` or the HTTP API for graph-derived boundary obligations. See [change verification](server-api.md#change-verification). Verification results are recorded as metadata in `verify_runs` and can be inspected through `GET /api/verify/runs`.

Usage:

```bash
./tirion verify [flags]
```

Provide `-diff-file` or pipe a unified diff on stdin.

| Flag | Default | Description |
| --- | --- | --- |
| `-api-url` | `http://localhost:8080` | API base URL. |
| `-workspace` | `default-main` | Workspace slug to query. |
| `-diff-file` | none | Unified diff file. `-diff-file=-` reads stdin; omitting it and piping a diff works too. |
| `-work-file` | none (required) | Legacy JSON submission containing `task`, `baseSha`, `rootCause`, `requirementsComplete`, `rootCauseEvidence`, and `claims`. |
| `-repo` | none (required) | Exact repo name that the diff paths are relative to. |
| `-depth` | `4` | Trace depth. |
| `-no-tests` | `true` | Hide nodes from test files. |
| `-resolve` | `true` | Resolve DI/implementation edges. |
| `-max-nodes` | `2000` | Maximum nodes returned. |
| `-fail-on` | `fail` | Exit non-zero on `fail` only, or on both `warn` and `fail`. `info` never exits non-zero. |
| `-out` | stdout | Write output JSON to a file. |

Exit codes:

- `0`: verdict `pass` or `info`, or verdict `warn` with `-fail-on=fail`.
- `1`: verdict `fail`.
- `2`: verdict `warn` when `-fail-on=warn`.

Legacy submission example:

```json
{
  "baseSha": "<indexed-base-sha>",
  "task": "Original story text",
  "rootCause": "Concrete mechanism found during investigation",
  "requirementsComplete": true,
  "rootCauseEvidence": [
    {"kind":"runtime_boundary","repo":"api","file":"src/Controller.cs","symbol":"Controller.Update","line":42}
  ],
  "claims": [
    {
      "id":"REQ-1",
      "requirement":"Requested behavior",
      "status":"covered",
      "evidence":[{"kind":"implementation","repo":"api","file":"src/Controller.cs","symbol":"Controller.Update","line":58}]
    }
  ]
}
```

Examples:

```bash
git diff main...HEAD | ./tirion verify -workspace release-workspace -repo orders-api -work-file agent-work.json
./tirion verify -diff-file change.diff -work-file agent-work.json -repo resource-api -fail-on warn -out verify.json
```

## `tirion trace` (call-chain tracing)

Posts a call-chain trace request to `/api/trace`.

Usage:

```bash
./tirion trace [flags] <function-or-caller-id>
```

| Flag | Default | Description |
| --- | --- | --- |
| `-api-url` | `http://localhost:8080` | API base URL. |
| `-workspace` | `default-main` | Workspace slug to query. |
| `-depth` | `4` | Maximum traversal depth. |
| `-max-nodes` | `2000` | Maximum nodes. |
| `-resolve` | `false` | Resolve cross-service HTTP/SQS edges. |
| `-no-tests` | `true` | Exclude nodes from test files; use `-no-tests=false` to include them. |

Input formats:

- Plain function name: `MyService.handlePayment`
- Full caller ID: `<repo>:<file>:<function>`

Examples:

```bash
./tirion trace generateFailure
./tirion trace -depth=6 public-api:src/services/Payments.java:Payments.handle
./tirion trace -no-tests "EmailSender.send"
```

## `parse` (core indexer)

Indexes a repository into the relational schema.

Usage:

```bash
./parse [flags] <path-to-repo>
```

| Flag | Default | Description |
| --- | --- | --- |
| `-db` | `DATABASE_URL` | PostgreSQL connection string. |
| `-v` | `false` | Verbose output plus timing breakdown. |
| `-incremental` | `false` | Only reindex changed files (hash-based). |
| `-workspace` | `default-main` | Snapshot workspace slug. Only `default-main` is created on demand; another slug must already exist (`tirion workspace create <slug>`), as for `tirion index`. |
| `-repo-name` | repository directory basename | Logical repository name to store. |
| `-reset-all` | `false` | Truncate all tables before indexing (destructive). |
| `-allow-file-failures` | `false` | Exit 0 even when files lost all facts to a timeout or internal error (otherwise exit status 3). |

`parse` also accepts `-candidate` (unpublished snapshot ID) and `-defer-resolution` (resolve and build Trace during pipeline publication). These are internal flags that `tirion index` sets; do not pass them by hand.

Notes:

- Full parse replaces the selected repository snapshot's facts, not other workspace snapshots.
- `-incremental` updates changed files within that snapshot.
- `-reset-all` is a destructive global graph reset, not a workspace reset.
- This is a lower-level parser, not a substitute for `tirion index`'s complete enrichment and cross-repository resolution pipeline.
- Directory skipping follows one rule for the parser and every `extract-*` helper: dependency, tool-state and VCS directories (`node_modules`, `vendor`, `third_party`, `.git`, `.hg`, `.svn`, `.idea`, `.vscode`, ...) are always skipped; build-output names (`build`, `dist`, `target`, `out`, `bin`, `obj`, `coverage`) only beside a build manifest. A directory named `external` is ordinary source.
- Symlinked source files that are absolute, point outside the repository, dangle, or do not resolve to a regular file are excluded and logged (`Skipping non-source symlink`); they never abort the run. Symlinked directories are not followed inside a repository.
- Source files are normalized before parsing: UTF-16 with a byte-order mark is decoded to UTF-8, NUL bytes are removed, a UTF-8 BOM is dropped, and invalid UTF-8 is replaced with U+FFFD. The stored file hash still covers the original bytes.
- Minified and bundled scripts (`*.min.js`, `*.bundle.js`) and well-known vendored libraries copied into a source tree (jQuery, lodash, Angular, Moment, Backbone, d3, React distribution builds, and similar, matched by exact file name) are skipped. A `.js`, `.mjs`, or `.cjs` file of 100 KB or more is also skipped (`generated_or_vendored_js` in the parse summary) when it carries a build or third-party signature: a `sourceMappingURL` comment, a bundler runtime (webpack, SystemJS, AMD `define.amd`), or a license banner with a version near the top. This covers copied Swagger UI, OIDC clients, framework builds, and `dist/` output that sits outside a build manifest.
- Each file has a parse timeout (`CODE_INTEL_PARSE_TIMEOUT_MS`, default 30000 ms; `0` disables it; an invalid value is reported and the default is kept). A timeout or a recovered internal panic loses that file's facts: it is logged and listed under `Files that lost facts (timeout or internal error)` in the parse summary, and `parse` then exits with status 3 so the incomplete result is not published (the previous snapshot stays active). Pass `-allow-file-failures` (to `parse`, or to `tirion index`) to publish anyway; such a snapshot is never reused by `-skip-unchanged`.
- With `PARSE_SKIP_TESTS=1` (set by `tirion index` from its `-skip-tests` flag; the standalone `parse` indexes test files unless it is set), test files are skipped by path: `test`, `tests`, `__tests__`, `__mocks__`, `mocks`, and `testdata` directories; `spec`/`specs` directories except under `src/main` and never for Go or Java; `*_test.go`, `*Test(s)`/`*TestCase`/`*IT`/`*ITCase`/`Test*` Java classes, `*Test`/`*Spec` C# classes, and `*.test.*`/`*.spec.*` scripts including `.mjs`, `.cjs`, `.mts`, `.cts`. A Java `ProductSpec` (a JPA Specification) is not a test.
- `CODEBASE_TRACE_CLEANUP=1` logs per-table cleanup timings during `parse`.

Examples:

```bash
./parse ../storefront
./parse -v ../storefront
./parse -incremental ../storefront
DATABASE_URL=postgres://... ./parse -reset-all ../storefront
```

## `audit-index` (post-parse quality gate)

Runs SQL quality checks after indexing and exits non-zero when configured thresholds fail.

Usage:

```bash
./audit-index [flags]
```

| Flag | Default | Description |
| --- | --- | --- |
| `-db` | `DATABASE_URL` | PostgreSQL connection string. |
| `-timeout` | `60s` | Audit execution timeout (for example `60s`, `3m`). |
| `-max-duplicate-endpoints` | `0` | Fail when duplicate endpoint groups exceed this value. |
| `-max-unresolved-http` | `-1` (disabled) | Fail when unresolved internal HTTP calls exceed this value. |
| `-min-queue-match-rate` | `-1` (disabled) | Fail when the producer-to-consumer queue match rate is below this percent. |
| `-min-cross-repo-data-coverage` | `-1` (disabled) | Fail when cross-repo data-link coverage is below this percent. |

Metric semantics:

- `queue_match_rate` uses internal queue candidates (matched queues plus unresolved queues produced by multiple repos); single-repo no-consumer queues are reported separately.
- `cross_repo_data_link_coverage` uses shared write entities (entities observed across multiple write/read repos); total write entities are still reported for context.

Example:

```bash
./audit-index \
  -max-duplicate-endpoints=0 \
  -max-unresolved-http=200 \
  -min-queue-match-rate=70 \
  -min-cross-repo-data-coverage=25
```

## `impact-report` (CI/PR markdown report)

Generates a Markdown impact report for PR/MR workflows. It sends the diff (or function list) to `/api/impact` and writes a summary plus evidence paths to stdout or a file. The tool also parses the diff locally (pre-change line numbers) to validate it and report the range count; a diff it cannot parse stops the run with `diff cannot be parsed`. Optional thresholds can fail the job.

When `-ref` is provided, the tool creates or reuses a cached snapshot at `.codebase-snapshots/<repo@ref>` and indexes it before running the report.

Usage:

```bash
./impact-report [flags]
```

| Flag | Default | Description |
| --- | --- | --- |
| `-server` | `http://localhost:8080` | API base URL. |
| `-diff-file` | none | Unified diff file (`git diff` or `diff -u`). `-diff-file=-` reads stdin; omitting it and piping input works too. |
| `-functions` | none | Comma-separated function names (optional alternative to a diff). |
| `-repo` | none | Exact repo name for the diff paths. The server infers it per path when omitted, but `-ref` requires it. |
| `-ref` | none | Parse and report against a specific git ref (creates a cached snapshot); requires `-repo` and `-repo-path`. |
| `-repo-path` | none | Path to the git repo (required with `-ref`). |
| `-snapshot-dir` | `.codebase-snapshots` | Snapshot cache directory. |
| `-parse-bin` | `./parse` | Path to the `parse` binary. |
| `-reindex` | `false` | Rebuild the snapshot even if it exists. |
| `-timeout` | `90s` | HTTP timeout for the impact API. |
| `-depth` | `4` | Trace depth. |
| `-no-tests` | `true` | Hide nodes from test files. |
| `-resolve` | `true` | Resolve DI/implementation edges. |
| `-exclude` | none | Comma-separated substrings to exclude (file path or function name). |
| `-include-repo` | none | Comma-separated repos to restrict to. |
| `-exclude-repo` | none | Comma-separated repos to exclude. |
| `-max-nodes` | `2000` | Maximum nodes returned. |
| `-top` | `5` | Rows per section. |
| `-top-evidence` | `3` | Evidence paths per section. |
| `-max-matches` | `3` | HTTP matches per call. |
| `-title` | `Impact Report` | Report heading. |
| `-out` | stdout | Write Markdown to a file. |
| `-json-out` | none | Write the raw JSON response to a file. |
| `-fail-on-entrypoints` | `-1` (disabled) | Fail if the entrypoints count exceeds this value. |
| `-fail-on-http` | `-1` (disabled) | Fail if the HTTP calls count exceeds this value. |
| `-fail-on-queues` | `-1` (disabled) | Fail if the queues count exceeds this value. |
| `-fail-on-repos` | `-1` (disabled) | Fail if the repos count exceeds this value. |

When any `-fail-on-*` gate is set, the gate also fails if the change mapped to no indexed function and the server reported coverage gaps (for example an edit outside every indexed function or a rename with no indexed preimage); zero counts are only meaningful when the change was resolved. Added files alone do not fail it.

Examples:

```bash
git -C ../storefront diff origin/main...HEAD | /path/to/impact-report -repo=storefront -out impact.md
git -C ../storefront diff origin/main...HEAD | /path/to/impact-report -repo=storefront -ref=origin/HEAD -repo-path=../storefront -out impact.md
./impact-report -functions initLogin,submitLogin -server http://localhost:8080
./impact-report -repo=storefront -fail-on-entrypoints 0 -fail-on-http 5
```

Report sections:

- Changed code targets (grouped by file)
- What changed (primary change areas)
- Impact summary
- User-facing entrypoints
- Outbound HTTP calls (with matches + evidence)
- Queues touched
- Repos touched

## `contracts-report` (service contract export)

Exports workspace-scoped service contract summaries or a single repo contract as Markdown or JSON. The report includes HTTP endpoints/calls, GraphQL operations/usages/resolvers, data and side-effect resources, queue producers/consumers, and schedule triggers.

Usage:

```bash
./contracts-report [flags]
```

| Flag | Default | Description |
| --- | --- | --- |
| `-server` | `http://localhost:8080` | API base URL. |
| `-repo` | none | Repo name for a detailed contract. |
| `-workspace` | server default | Workspace to query. |
| `-limit` | `200` | Maximum items per section. |
| `-timeout` | `60s` | HTTP timeout. |
| `-out` | stdout | Write Markdown to a file. |
| `-json-out` | none | Write the raw JSON response to a file. |

Examples:

```bash
./contracts-report
./contracts-report -workspace=default-main
./contracts-report -workspace=default-main -repo=storefront -out contract.md
./contracts-report -workspace=default-main -repo=storefront -json-out contract.json
```

## Extractors (post-processing utilities)

These commands re-scan already indexed files to populate auxiliary tables. Use the normal indexing pipeline to keep parsing, enrichment and resolution consistent.

Commands:

- `extract-java-intel`: JPA, annotations, scheduled methods, etc.
- `extract-java-calls`: rebuild Java call edges with receiver names.
- `extract-http`: backfill `http_client_calls`.
- `extract-sqs`: backfill `sqs_producers` / `sqs_consumers`.
- `extract-ts-intel`: TypeScript intelligence from indexed files.
- `extract-spring`: Spring-specific data from indexed Java files.

The six source extractors above share these flags and take no positional arguments:

| Flag | Default | Description |
| --- | --- | --- |
| `-db` | `DATABASE_URL` | PostgreSQL connection string. |
| `-workspace` | `default-main` | Workspace slug whose active snapshot's server-side source paths are read. |
| `-repo` | all repos | Repository name to limit extraction to. |
| `-dry-run` | `false` | Show what would be extracted without writing facts. It avoids fact replacement but may still initialize schema; it is not a guarantee of zero database writes. |
| `-v` | `false` | Verbose output. |

They also accept `-candidate` (unpublished snapshot ID), an internal flag that `tirion index` sets; do not pass it by hand.

### `extract-eventbridge`

Imports an explicit schedule JSON file, not source snapshots. For the record format, see the schedule import section of [operations](../SETUP.md#running-tirion-for-a-team).

| Flag | Default | Description |
| --- | --- | --- |
| `-file` | none (required) | JSON array of schedule records. |
| `-db` | `DATABASE_URL` | PostgreSQL connection string. |
| `-dry-run` | `false` | Validate and display records without database access. |
| `-v` | `false` | Verbose output. |

## `release-sign` (release checksum signing)

Optional utility for signing release archives with an Ed25519 key. It is not a runtime dependency. Each subcommand takes its own flags.

| Subcommand | Flags | Description |
| --- | --- | --- |
| `keygen` | `-out` (default `keys/release`) | Generate a new Ed25519 release-signing key pair in the output directory. |
| `manifest` | `-dir` (default `dist`), `-out` (default `dist/SHA256SUMS.txt`) | Generate the checksum manifest for the release archives in a directory. |
| `sign` | `-in` (required), `-privkey` (required), `-out` (default `<in>.sig`) | Sign a file, normally the manifest, with a private key. |
| `verify` | `-in`, `-sig`, `-pubkey` (all required) | Verify a detached signature with a public key. |
| `pubkey` | `-privkey`, `-out` (both required) | Derive and write the public key from a private key. |

```bash
release-sign keygen -out keys/release
release-sign manifest -dir dist -out dist/SHA256SUMS.txt
release-sign sign -in dist/SHA256SUMS.txt -privkey keys/release/private.key -out dist/SHA256SUMS.txt.sig
release-sign verify -in dist/SHA256SUMS.txt -sig dist/SHA256SUMS.txt.sig -pubkey keys/release/public.key
```

## Scripts

`scripts/estate-fixture-check.mjs` checks the portable Java/TypeScript HTTP fixture against a live API. `scripts/search-golden-check.sh` accepts an explicit TSV manifest for an operator's own estate. For inputs and coverage, see [Regression checks](../CONTRIBUTING.md#regression-checks).
