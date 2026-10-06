# Set Up And Run Tirion

Tirion runs locally against repositories you can read. PostgreSQL stores the index;
your coding agent connects through MCP. No hosted LLM service is required for
indexing or querying the graph. Tirion uses [Apache-2.0](LICENSE); no license key or
activation is required.

This guide has five parts:

- [Install from source](#prerequisites): prerequisites and steps 1-7.
- [Configuration reference](#configuration-reference): every environment variable,
  `TIRION_ENV_FILE`, and the optional YAML files.
- [Running Tirion for a team](#running-tirion-for-a-team): release bundles, services,
  reverse proxy, access control, scheduled refresh, and snapshot retention.
- [Updating and upgrading](#updating-and-upgrading): routine updates, backups, and
  notes for moving from earlier builds.
- [Troubleshooting](#troubleshooting).

Commands and flags are in [DOCS/cli-reference.md](DOCS/cli-reference.md), the HTTP API
in [DOCS/server-api.md](DOCS/server-api.md), and MCP tools in [DOCS/mcp.md](DOCS/mcp.md).

## Prerequisites

- Git and Bash.
- Go at the version specified in `go.mod` or a compatible newer toolchain.
- A C compiler for tree-sitter/CGO. On macOS use Xcode Command Line Tools; on Linux
  install the distribution's C build tools. Windows native builds need a
  CGO-compatible MinGW toolchain and Git Bash for the examples below.
- PostgreSQL 16 (the portable CI baseline) with `pg_trgm` available (install the
  distribution's contrib package where it is packaged separately).
- Node.js 24 or newer and npm for the UI. Dependencies are locked in
  `frontend/package-lock.json`.

Clone the repository and enter the checkout:

```bash
git clone https://github.com/sergiumoraru/tirion.git
cd tirion
```

The Go module path is `github.com/sergiumoraru/tirion`.

## 1. Build The Commands

```bash
bash scripts/build-local.sh
```

This builds all native Go commands into `bin/`, including `tirion`, `mcp-intel`,
`parse`, and the extractors used by `tirion index`. Building only `cmd/tirion` is
not sufficient for a fresh indexing installation. Keep the sibling binaries together:
server indexing loads helpers from the executable's directory. The script does not
install software, create databases, run tests, or build the frontend.

The examples in these docs use `./bin/tirion`. In an extracted release bundle the
binaries sit in the current directory, so use `./tirion` instead. To rebuild a single
command while developing, use `go build -o <binary> ./cmd/<binary>` (for example
`go build -o tirion ./cmd/tirion`), or `go install ./cmd/<binary>` to place it on your
`$GOBIN`.

## 2. Prepare PostgreSQL

Using a PostgreSQL administrator connection, create a dedicated login and database.
Choose a password when prompted instead of committing one in a command or file:

```bash
createuser --pwprompt tirion
createdb --owner=tirion tirion
psql --dbname=tirion --command='CREATE EXTENSION IF NOT EXISTS pg_trgm;'
```

If your installation requires an explicit administrator identity, supply its normal
`-U` and `-h` options. Tirion creates tables and indexes on connection, so the runtime
role needs schema creation privileges in this database. Use a dedicated database
rather than an application's production database. The name `tirion` is a convention,
not a requirement.

## 3. Configure

Export the connection URL in every terminal that runs a backend command:

```bash
export DATABASE_URL='postgres://tirion@localhost:5432/tirion?sslmode=disable'
```

Supply the password through your local PostgreSQL credential mechanism, such as
`.pgpass`, rather than putting it in Git. `sslmode=disable` is for this loopback
example, not a remote-database recommendation.

Tirion does not choose a database for you: without `DATABASE_URL` (or `-db`) a
command stops with an error. It also never loads `.env` or `.env.local` from the
working directory. To keep settings in a file, point `TIRION_ENV_FILE` at a trusted
absolute path outside your repositories; only `tirion`, `mcp-intel`, and `mcp-guard`
read it. The API token needs no setup, because `tirion serve` generates it on first
start (step 5). Every setting, the `TIRION_ENV_FILE` rules, and the optional YAML
files are in [Configuration Reference](#configuration-reference).

## 4. Index Repositories

Use a parent directory with one repository per direct child:

```text
/path/to/repos/
  storefront/
  orders-api/
  billing-worker/
```

Symlinked repository directories are followed, and a folder that groups several
repositories (`group/service-a`, `group/service-b`) is searched two levels deep. The
[CLI reference](DOCS/cli-reference.md#repository-discovery) lists the rules, including
the directories that are always skipped. Run `tirion index -dry-run` first to see
which repositories would be indexed; it also checks that `parse` and the `extract-*`
helpers sit beside `tirion`.

Clone with your normal Git credentials. Tirion does not need GitHub credentials to
parse source already on disk. Use dedicated clones if you intend to use branch
management: checkout operations change those local repository files.

```bash
./bin/tirion index -root /path/to/repos -workspace default-main -skip-unchanged=false
```

`default-main` is the initial workspace and the only one created automatically. Create
any other workspace first (`tirion workspace create <slug>`); indexing a workspace that
does not exist fails and lists the existing slugs. Put flags before the repos root,
because a flag after it is rejected. For release and branch workspaces see the
[workspace commands](DOCS/cli-reference.md#tirion-workspace). Graph results describe
indexed snapshots, and a failed indexing attempt leaves the previous selected graph
available.

To try Tirion without your own estate, index the
[synthetic RPG estate](testdata/rpg-estate/README.md) (program, procedure, and data
relationships across repository-like directories) or the Java/TypeScript
[HTTP estate fixture](testdata/estate-fixtures/README.md); neither needs private
repositories.

Optional GitHub organization cloning is separate from indexing:

```bash
bash scripts/clone-repos.sh --org your-org --root /path/to/repos
```

This requires your own GitHub access and an explicit organization; there are no default
repositories. Existing clones are skipped unless fetching is requested. An existing path
without a matching origin, a symlink, or a linked worktree is left untouched and
reported as a conflict. Resolve conflicts explicitly: the script does not adopt an
unrelated checkout or overwrite its origin.

## 5. Start The API

In a terminal with `DATABASE_URL` configured:

```bash
./bin/tirion serve -port 8080
```

The listener is `127.0.0.1:8080` by default (`-host` or `TIRION_HOST` change it). Every
API route requires the service token, including health and repository administration.
On first start, once it has connected to PostgreSQL, the server creates
`~/.tirion/api-token` (or `api-token` under `TIRION_HOME`) with private permissions and
never prints it. Read it locally to connect the UI:

```bash
cat "$HOME/.tirion/api-token"
```

MCP and native HTTP clients running as the same user read this file automatically when
the API URL is loopback (`localhost`, `127.0.0.1`, or `[::1]`, any port). For a remote
server or a service account, supply `TIRION_API_TOKEN` or `TIRION_API_TOKEN_FILE`
explicitly; a non-loopback API URL must use HTTPS. Token format, rotation, trusted
hosts and origins, and concurrency limits are in
[Authentication and state paths](#authentication-and-state-paths) and the
[HTTP API reference](DOCS/server-api.md#authentication). Keep the backend on loopback
and put TLS and your organization's authentication at a proxy for shared access
([Authenticated Reverse Proxy](#authenticated-reverse-proxy)).

The API process does not serve frontend files. API clients cannot choose the `root`,
`parseBinary`, or `worktreeRoot` of an index request: install matching `parse` and
`extract-*` helpers beside `tirion` and configure `TIRION_REPOS_ROOT` and
`TIRION_WORKTREE_ROOT` as described in
[Repositories and worktrees](#repositories-and-worktrees).

In another terminal with the same environment, confirm the API is reachable before
configuring the UI or MCP:

```bash
curl --fail --silent --show-error -H "X-Tirion-Token: $(cat "$HOME/.tirion/api-token")" http://localhost:8080/api/health
curl --fail --silent --show-error -H "X-Tirion-Token: $(cat "$HOME/.tirion/api-token")" http://localhost:8080/api/workspaces
./bin/tirion search -workspace default-main -repo orders-api OrderController
```

Replace the repository and symbol in the last command with ones you indexed. Health
confirms the API is up; a known search result confirms you are querying the intended
index. Follow a known cross-repo call in Trace or Flow to inspect its source evidence.

## 6. Start The Web UI

In another terminal:

```bash
cd frontend
npm ci
npm run dev
```

Open the URL Vite prints (normally `http://localhost:3000`) and paste the token when
asked; the browser keeps it for that tab's session only. The development proxy sends
`/api` to `http://127.0.0.1:8080`, matching the API's default IPv4 listener; if you
change the API port, update the Vite proxy target. No production frontend build is
needed for local use.

Select the indexed workspace, search for a known endpoint or function, and follow its
connections with Trace or Flow ([workflows](README.md#how-tirion-works)).

## 7. Connect Your Agent

Point your MCP client at the absolute path of `bin/mcp-intel` and give it
`TIRION_API_URL`, `TIRION_WORKSPACE`, and, for local source and diff access,
`TIRION_REPOS_ROOT` and `TIRION_WORKDIR`. The client configuration format, variables,
and tools are in the [MCP reference](DOCS/mcp.md#connect) and
[MCP and clients](#mcp-and-clients). Keep local wiring and
credentials out of Git.

Your agent owns reasoning and implementation. Tirion supplies graph evidence and
supported checks; it does not launch a coding-agent run.

## Configuration Reference

Tirion is configured through process environment variables and three optional
YAML files in its state directory. This section is the reference for both.

### Supplying Settings

Native commands read settings from their process environment: your shell, a
service manager (for example systemd `EnvironmentFile=`), or your MCP client's
`env` block. They never load `.env` or `.env.local` from the working directory or
any parent directory, and nothing loads `.env.example`, which is a reference only.
Standalone helpers (`parse`, `extract-*`, `audit-index`, `impact-report`,
`contracts-report`) need exported variables or service-manager configuration;
`tirion index` and `tirion serve` pass the database URL to the helpers they start.

#### `TIRION_ENV_FILE`

Set `TIRION_ENV_FILE` to the absolute path of a trusted file to load settings from it.

```bash
export TIRION_ENV_FILE="$HOME/.config/tirion/runtime.env"
```

| Binary | Reads `TIRION_ENV_FILE` |
| --- | --- |
| `tirion` (every subcommand) | Yes |
| `mcp-intel` | Yes |
| `mcp-guard` | Yes |
| `parse`, `extract-*`, `audit-index`, `impact-report`, `contracts-report`, `release-sign` | No |

File rules:

- The path must be absolute (`TIRION_ENV_FILE must be an absolute path`), and the
  file must be a regular file of at most 1 MiB. Keep it outside cloned repositories
  and restrict it to your user (`chmod 600`); Tirion does not check the mode.
- One `KEY=value` per line. Blank lines and lines starting with `#` are skipped, an
  `export ` prefix is accepted, a matching pair of single or double quotes around the
  value is removed, and `\n` becomes a newline. Trailing comments are not stripped.
  If a key repeats, the first occurrence wins.
- Allowed keys: `DATABASE_URL`, `PORT`, `GH_TOKEN`, `GITHUB_TOKEN`, `REPOS_ROOT`,
  `PARSE_SKIP_TESTS`, `CODE_INTEL_PARSE_TIMEOUT_MS`, any `TIRION_*` key except
  `TIRION_ENV_FILE`, and any `CODEBASE_*` key (which includes `CODEBASE_INTEL_*`).
  Any other key stops startup with `unsupported environment key on line N`. In
  particular `PATH`, `HOME`, `GIT_*`, loader (`LD_*`, `DYLD_*`) variables, and the
  proxy and certificate-trust variables cannot be set through this file.

#### Precedence

1. A variable already in the process environment wins, including one set to an
   empty value. The file only fills variables that are not set at all.
2. Command-line flags override their corresponding variables (for example
   `-db` over `DATABASE_URL`, `-port` over `PORT`, `-host` over `TIRION_HOST`).
3. Where a `TIRION_*` variable has a legacy alias, the first non-empty value in the
   order `TIRION_*`, then the alias is used.
4. Built-in defaults.

#### What helper processes receive

`tirion index` and the workspace commands start `parse` and the `extract-*`
helpers with a filtered environment: `DATABASE_URL`, `PARSE_SKIP_TESTS`,
`CODE_INTEL_PARSE_TIMEOUT_MS`, `TIRION_HOME`, `TIRION_ENRICHMENT_CACHE_DIR`, basic
process and locale variables (`PATH`, `HOME`, `TMPDIR`, `LANG`, `TZ`, `USER`/`LOGNAME` on Unix, and the Windows
profile variables), the PostgreSQL client variables (`PGHOST`, `PGPORT`, `PGUSER`,
`PGPASSWORD`, `PGDATABASE`, `PGPASSFILE`, `PGSERVICE`, `PGSERVICEFILE`,
`PGSSLMODE`, `PGSSLROOTCERT`, `PGSSLCERT`, `PGSSLKEY`, `PGSSLCRL`,
`PGCONNECT_TIMEOUT`, `PGTARGETSESSIONATTRS`), and the outbound proxy and
certificate-trust variables (`HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`, `ALL_PROXY` in
upper or lower case, `SSL_CERT_FILE`, `SSL_CERT_DIR`). API tokens and GitHub tokens
are never passed on. Git commands additionally drop the `PG*` variables and every
inherited `GIT_*` override, keep `SSH_AUTH_SOCK`, `SSH_AGENT_PID`, and
`XDG_CONFIG_HOME`, and run with `GIT_TERMINAL_PROMPT=0`. Repository fetches and
database connections therefore work behind corporate proxies and private
certificate authorities when those variables are exported in the process
environment.

### Environment Variables

Names starting with `CODEBASE_INTEL_` are legacy aliases of the `TIRION_*` variable
with the same suffix and are still read. Prefer the `TIRION_*` names. Other
`CODEBASE_*` and `CODE_INTEL_*` variables have no `TIRION_*` equivalent and keep
their names. "Read by" lists the binaries that read the variable; `tirion` means the
`tirion` binary, including `tirion serve`.

#### Database

| Variable | Purpose | Default | Read by |
| --- | --- | --- | --- |
| `DATABASE_URL` | PostgreSQL connection URL. There is no default: when it is unset and `-db` is not passed, the command stops with `database is required: set DATABASE_URL or pass -db; .env files are not loaded automatically (see SETUP.md#configuration-reference)`. `tirion search`, `trace`, `impact`, and `verify` query the API and do not need it. | None | `tirion`, `parse`, `audit-index`, `extract-*`, `scripts/nightly-refresh.sh` (required), `scripts/build-windows-release.ps1` (database check), the RPG scripts |
| `CODEBASE_SKIP_SCHEMA_INIT` | `1` skips schema creation and migration when connecting. `tirion index` sets it for the helpers it starts so only one process migrates the database. Not an operator setting. | Unset | Every binary that opens the database |
| `PG*` | The PostgreSQL driver honors the standard client variables; they are also passed to helper processes (see above). | Unset | Database clients |

#### Server, network, and limits

| Variable | Purpose | Default | Read by |
| --- | --- | --- | --- |
| `PORT` | Port for `tirion serve`; `-port` overrides. | `8080` | `tirion serve` |
| `TIRION_HOST` | Bind address for `tirion serve`; `-host` overrides. Use `0.0.0.0` explicitly for shared deployments. | `127.0.0.1` | `tirion serve` |
| `TIRION_ALLOWED_HOSTS` | Comma-separated exact hostnames or IPs, optionally with a port, accepted in the `Host` header. Replaces the default list; wildcards, schemes, paths, and an empty list stop startup. A custom bind address does not trust its hostname automatically. Forwarded headers never establish trust, so behind a TLS proxy list the external hostname. | `localhost,127.0.0.1,[::1]` | `tirion serve` |
| `TIRION_ALLOWED_ORIGINS` | Comma-separated exact `http(s)://host[:port]` browser origins allowed in addition to same-origin requests. Replaces the default list; wildcards and paths are rejected. Set but empty means same-origin only. | `http://localhost:3000,http://127.0.0.1:3000` | `tirion serve` |
| `TIRION_MAX_CONCURRENT_READS` | Concurrent graph requests (1-1024). When all slots are busy the API answers 503 `BUSY`. A value outside the range or not an integer stops startup; unset or blank selects the default. | `16` | `tirion serve` |
| `TIRION_MAX_CONCURRENT_ADMIN` | Concurrent administrative requests such as index, fetch, and checkout (1-64); same behavior as above. | `2` | `tirion serve` |
| `TIRION_REFRESH_STATUS_FILE` | File the API reads to report the latest scheduled-refresh status; `scripts/nightly-refresh.sh` writes it. | `/var/log/tirion/refresh-status.env` | `tirion serve`, `scripts/nightly-refresh.sh` |
| `CODEBASE_TRACE_EXPAND_CACHE_SIZE` | Entries in the API's trace-expansion cache. An unparsable value keeps the default. | `200` | `tirion serve` |
| `CODEBASE_TRACE_EXPAND_CACHE_TTL` | Lifetime of a trace-expansion cache entry, as a Go duration (for example `10m`). An unparsable value keeps the default. | `5m` | `tirion serve` |

Request limits, timeouts, and error codes are API behavior: see
[server-api.md](DOCS/server-api.md#hosts-origins-and-limits). A reverse proxy in front of
the API is described in [Authenticated Reverse Proxy](#authenticated-reverse-proxy).

#### Authentication and state paths

Every API route requires the shared service token. The server resolves it in the
order below; clients use the same variables.

| Variable | Purpose | Default | Read by |
| --- | --- | --- | --- |
| `TIRION_API_TOKEN` | The token itself: 32-512 printable, non-whitespace ASCII characters. Overrides the token file. A variable that is set but invalid (including empty) is an error, not a fallback. | Unset | `tirion serve` and every API client |
| `TIRION_API_TOKEN_FILE` | Absolute path of a private file holding the token. Required, or `TIRION_API_TOKEN`, for any API origin that is not loopback. | `<state dir>/api-token` | `tirion serve` and every API client |
| `TIRION_HOME` | Absolute state directory. | `~/.tirion` | All Tirion binaries; scripts that look for the token |
| `TIRION_ENV_FILE` | Absolute path of a settings file; see [Supplying Settings](#supplying-settings). | Unset | `tirion`, `mcp-intel`, `mcp-guard` |

"API client" means `tirion` search, trace, impact, and verify, `mcp-intel`,
`mcp-guard`, `impact-report`, and `contracts-report`.

- **State directory.** `~/.tirion` (or `$TIRION_HOME`) holds `api-token`,
  `patterns.yaml`, `trace.yaml`, `owners.yaml`, and the API's default `workspaces/`
  directory. `TIRION_HOME` must be absolute and disables the legacy fallback. Without
  it, a file that is missing from `~/.tirion` but present in the legacy
  `~/.codebase-intel` directory is read from there (checked per file) and a log
  message asks you to migrate it. The new path always wins when both exist. For
  migration steps see
  [Upgrading From Earlier Builds](#upgrading-from-earlier-builds).
- **Token file.** The server creates it on first start, after it has connected to
  PostgreSQL, with 256 random bits (hex) and private permissions (mode 0600 on Unix;
  use a user-only ACL on Windows). Its contents are never logged. On Unix an
  existing file must be a regular file with no group or other permissions and at
  most 1024 bytes. Clients discover the local file only for a loopback API URL
  (`localhost`, `127.0.0.1`, `[::1]`, any port); any other origin must use HTTPS with
  an explicit `TIRION_API_TOKEN` or `TIRION_API_TOKEN_FILE`, and redirects are never
  followed. See [server-api.md](DOCS/server-api.md#authentication) for how requests are
  authenticated.
- **Rotation.** A token grants full API access, including checkout and index
  operations. To rotate, replace the file or environment value, restart the API, and
  reconnect clients with the new token.

#### Repositories and worktrees

| Variable | Purpose | Default | Read by |
| --- | --- | --- | --- |
| `TIRION_REPOS_ROOT` | Absolute directory of source clones. The API indexes the `default-main` workspace from it and uses it to recover clone paths after a move; `mcp-intel` and `mcp-guard` treat it as a root for local source and diff access. Request bodies cannot set the root. | Unset | `tirion serve`, `mcp-intel`, `mcp-guard`, `scripts/nightly-refresh.sh` |
| `REPOS_ROOT` | Legacy fallback clone root, consulted after `TIRION_REPOS_ROOT` when the API recovers a moved clone. `scripts/nightly-refresh.sh` uses `TIRION_REPOS_ROOT`, then `REPOS_ROOT`, as its clone root. | Refresh script: `/data/tirion/repos` | `tirion serve`, `scripts/nightly-refresh.sh` |
| `TIRION_WORKTREE_ROOT` | Directory for managed workspace worktrees (`<root>/<workspace>/repos/<repo>`). Existing registered worktree paths stay usable. | API: `<state dir>/workspaces`. CLI `-worktree-root`: `<OS temp dir>/tirion-workspaces` | `tirion serve`, `tirion workspace` |
| `TIRION_WORKSPACES_ROOT` | Legacy alias of `TIRION_WORKTREE_ROOT`, used only when that is unset. | Unset | `tirion serve`, `tirion workspace` |
| `GH_TOKEN`, `GITHUB_TOKEN` | Credential for HTTPS GitHub fetches in workspace operations (`GH_TOKEN` first). Without either, Git uses the service account's own configuration; SSH and other hosts keep their own authentication. Never put a token in a repository URL. Both are also accepted by `clone-repos.sh` and are never passed to helper processes. | Unset | `tirion serve`, `tirion workspace fetch`, `tirion sync-branches`, `scripts/clone-repos.sh` |
| `TIRION_GITHUB_ORGS` | Space-separated GitHub organizations to clone during the `default-main` nightly refresh. Unset means existing repositories only. | Unset | `scripts/nightly-refresh.sh` |

The CLI and API worktree defaults differ: a long-running API should have a
persistent `TIRION_WORKTREE_ROOT` and the same value should be exported when you use
`tirion workspace` from a shell. Workspace operations use the repository path
recorded by indexing; no adjacent directories are searched.

#### Parsing and indexing

| Variable | Purpose | Default | Read by |
| --- | --- | --- | --- |
| `PARSE_SKIP_TESTS` | `1` makes `parse` skip test and spec files. `tirion index` sets it from its `-skip-tests` flag for the helpers. | Unset (tests indexed) | `parse` |
| `CODE_INTEL_PARSE_TIMEOUT_MS` | Per-file parse timeout in milliseconds; `0` disables it. An invalid value is reported and the default kept. | `30000` | `parse`, `extract-*` |
| `CODEBASE_TRACE_CLEANUP` | `1` logs per-table cleanup timings during `parse`. | Unset | Binaries that open the database |
| `TIRION_ENRICHMENT_CACHE_DIR` | Directory for the parser's enrichment cache. `tirion index` sets it for the helpers; leave it unset. | Unset | `parse`, `extract-*` |

#### MCP and clients

`mcp-intel` is configured entirely through these variables. A non-empty `TIRION_*`
value wins over its `CODEBASE_INTEL_*` alias. Behavior specific to MCP (the local-root
requirement, lazy token resolution, timeouts) is in [mcp.md](DOCS/mcp.md).

| Variable | Alias | Purpose | Default | Read by |
| --- | --- | --- | --- | --- |
| `TIRION_API_URL` | `CODEBASE_INTEL_API_URL` | API origin, without a trailing `/api`. A non-loopback origin must use HTTPS. | `http://localhost:8080` | `tirion` client subcommands, `mcp-intel`, `mcp-guard` |
| `TIRION_API_TOKEN`, `TIRION_API_TOKEN_FILE` | None | Credentials; see above. The token is resolved on each call, so the client can start before the API has run. | Local token file when the URL is loopback | `mcp-intel`, `mcp-guard` |
| `TIRION_WORKSPACE` | `CODEBASE_INTEL_WORKSPACE` | Workspace slug for queries. | Server default | `mcp-intel` |
| `TIRION_API_AUTH_HEADER` | `CODEBASE_INTEL_API_AUTH_HEADER` | Authorization value for a protected proxy; takes precedence over Basic Auth. | Unset | `mcp-intel` |
| `TIRION_API_USERNAME` | `CODEBASE_INTEL_API_USERNAME` | Basic Auth username for a protected proxy. | Unset | `mcp-intel` |
| `TIRION_API_PASSWORD` | `CODEBASE_INTEL_API_PASSWORD` | Basic Auth password. | Unset | `mcp-intel` |
| `TIRION_MCP_TIMEOUT_SECONDS` | `CODEBASE_INTEL_MCP_TIMEOUT_SECONDS` | HTTP timeout, 1-600 seconds; other values keep the default. `mcp-guard` uses a fixed 60 seconds. | `60` | `mcp-intel` |
| `TIRION_REPOS_ROOT` | None | Absolute directory allowing local source and diff access (`code_read`, `git_diff`). | Unset | `mcp-intel`, `mcp-guard` |
| `TIRION_WORKDIR` | `CODEBASE_INTEL_WORKDIR` | Absolute local checkout used for relative reads and automatic diffs. The process working directory is never used. | Unset | `mcp-intel`, `mcp-guard` |
| `TIRION_MCP_LOG_FILE` | `CODEBASE_INTEL_MCP_LOG_FILE` | Local diagnostic log (mode 0600). Treat it as sensitive. | Unset | `mcp-intel` |
| `TIRION_USER` | `CODEBASE_INTEL_USER` | Attribution label sent as the actor. Takes precedence over `TIRION_ACTOR`. No operating-system user name or hostname is ever derived; no actor is sent when all are unset. | Unset | `mcp-intel` |
| `TIRION_ACTOR` | None | Attribution label used when `TIRION_USER` is unset. | Unset | `mcp-intel` |

#### Scripts, frontend, and CI

These apply to the helper scripts in `scripts/` and to the build and test
workflows. They are not read by the Tirion binaries.

| Variable | Used by | Purpose | Default |
| --- | --- | --- | --- |
| `INSTALL_ROOT` | `nightly-refresh.sh` | Install directory | Parent of `scripts/` |
| `TIRION_BIN` | `nightly-refresh.sh` | Path of the `tirion` binary | `$INSTALL_ROOT/tirion` |
| `GIT_PROTOCOL` | `nightly-refresh.sh`, `clone-repos.sh` | Clone protocol (`--protocol` overrides in `clone-repos.sh`) | `https` for refresh, `auto` for cloning |
| `TIRION_REFRESH_WORKSPACE` | `nightly-refresh.sh` | Workspace to refresh | `default-main` |
| `TIRION_REFRESH_SKIP_UNCHANGED` | `nightly-refresh.sh` | Pass `-skip-unchanged`; set `false` after parser or configuration upgrades | `true` |
| `TIRION_REFRESH_MAX_ATTEMPTS` | `nightly-refresh.sh` | Attempts before the refresh fails | `3` |
| `TIRION_REFRESH_STOP_API_DURING_REFRESH` | `nightly-refresh.sh` | `1` stops and restarts the API service during refresh | `1` |
| `TIRION_REFRESH_API_SERVICE_NAME` | `nightly-refresh.sh` | systemd unit operated on | `tirion.service` |
| `TIRION_BACKUP_ON_FAIL` | `nightly-refresh.sh` | `1` takes a temporary pre-refresh dump and restores it after a failed attempt | `1` |
| `TIRION_BACKUP_DIR` | `nightly-refresh.sh` | Directory for the temporary dump and step logs | `/tmp` |
| `CLONE_RETRY_FAILURES` | `clone-repos.sh` | End-of-run retry passes (`--retry-failures` overrides) | `1` |
| `GIT_SSH_COMMAND` | `clone-repos.sh` | SSH command for SSH clones | `ssh -o ConnectTimeout=10 -o ConnectionAttempts=1` |
| `DOCKER_IMAGE`, `DOCKER_PLATFORM`, `DOCKER_CONTEXT_NAME` | `build-linux-dist-local.sh` | Container image, platform, and Docker context for a Linux build from another host | `golang:<version from go.mod>`, `linux/amd64`, current context |
| `RPG_PUBLIC_ROOT`, `PARSE_BIN` | `index-rpg-*.sh`, `rpg-hardening-check.sh` | Public RPG corpus directory (or second argument); `parse` binary | None; `bin/parse` |
| `SEARCH_GOLDEN_MANIFEST`, `SEARCH_API_URL`, `HTTP_CLIENT` | `search-golden-check.sh` | Query manifest (or first argument), search endpoint, HTTP client binary | None; `http://localhost:8080/api/search`; `curl` |
| `BASE_URL`, `WORKSPACE_ID`, `HTTP_CLIENT` | `estate-fixture-check.mjs` | API origin, workspace, HTTP client binary | `http://localhost:8080`; `default-main`; `curl` |
| `VITE_API_URL` | `frontend` build | API base URL compiled into the UI; release bundles use `/api` | `/api` |
| `TIRION_WORKSPACE_LIVE_TESTS`, `TIRION_PIPELINE_LIVE_TESTS`, `TIRION_TEST_BIN`, `TIRION_REQUIRE_LIVE_DB` | Go test suites | Opt-in switches for the live-database suites; see [CONTRIBUTING.md](CONTRIBUTING.md) | Unset |
| `IMPACT_SERVER`, `IMPACT_REPO`, `TIRION_API_TOKEN` | `.github/workflows/impact-report.yml` | Repository secrets: API origin (required), repository name (optional), API token | None |
| `RELEASE_REPOSITORY` (variable), `RELEASE_REPO_TOKEN`, `RELEASE_SIGNING_PRIVATE_KEY`, `WINDOWS_CODESIGN_PFX_B64`, `WINDOWS_CODESIGN_PASSWORD`, `WINDOWS_CODESIGN_TIMESTAMP_URL` | `.github/workflows/release.yml` | Release repository, upload token, checksum signing key, and optional Windows code signing; see [Running Tirion For A Team](#running-tirion-for-a-team) | Release repository defaults to the current repository |

`search-golden-check.sh` and `estate-fixture-check.mjs` resolve the API token the same
way the clients do (`TIRION_API_TOKEN`, then `TIRION_API_TOKEN_FILE`, then the local
file for a loopback API, under `TIRION_HOME` or `~/.tirion`, with the legacy
`~/.codebase-intel` fallback). `build-local.sh` and `build-dist.sh` take no environment
settings: they build natively for the host with CGO enabled and ignore any `GOOS` or
`GOARCH` you export.

### Configuration Files

The YAML files below live in the state directory (`~/.tirion` or `$TIRION_HOME`) of
the account that runs Tirion. Each is optional; omitting one applies the defaults.
They are read when a process starts or first needs them and are not reloaded, so
restart the indexing process or the API after editing one. Put the same file on every
host that needs it: `patterns.yaml` is used by `parse`, the `extract-*` helpers, and
the API's source enrichment; `trace.yaml` by Trace, Impact, and the index audit; and
`owners.yaml` by the API's ownership resolver.

#### Patterns YAML

`patterns.yaml` declares application-specific conventions the parsers cannot know.
Unknown keys, a file with more than one YAML document, an `http_clients` entry whose
`language` is not `java` or `javascript`, and an unsupported HTTP verb in `methods`
produce an `invalid patterns configuration` error, and `parse` and the `extract-*`
helpers refuse to run until it is fixed. Supported top-level keys:

| Key | Purpose |
| --- | --- |
| `http_clients` | Custom HTTP client wrappers ([JavaScript](#custom-javascript-http-wrappers) and Java) |
| `java_sqs` | [Custom Java queue frameworks](#custom-java-queue-frameworks) |
| `javascript_queues` | [Custom JavaScript queue wrappers](#custom-javascript-queue-wrappers) |
| `graphql_registrations` | [Custom GraphQL registration wrappers](#custom-graphql-registration-wrappers) |
| `disable` | Names of built-in HTTP client patterns to disable |
| `context_path_variables` | Additional context-path variable names |

After changing the file, reindex with `-skip-unchanged=false`; existing indexed facts
are replaced by reindexing, not by changing the MCP client configuration. Omitting a
section leaves its behavior off: no custom wrapper, queue framework, or
registration convention is enabled by default.

##### Custom JavaScript HTTP Wrappers

A helper's name or suffix alone does not establish its HTTP method. Declare wrappers
with `http_clients`:

```yaml
http_clients:
  - name: application-http
    language: javascript
    objects: [requestClient]
    methods:
      fetchrecords: GET
      submitrecord: POST
  - name: application-submit
    language: javascript
    objects: [submitRequest]
    methods:
      "": POST
```

The first pattern describes calls such as `requestClient.fetchRecords(url)`; method
keys are lowercase. The second describes `submitRequest(url)` without an object
receiver. The wrappers must use the supported URL argument shape; configuration does not
interpret arbitrary wrapper bodies. Use the actual names and methods from your
implementation. Built-in public HTTP client support stays available without custom
patterns.

Java source enrichment uses the same section instead of a fixed list of application
wrapper methods. Configure it on both the indexing and API hosts when they are separate
deployments:

```yaml
http_clients:
  - name: application-java-http
    language: java
    contains: ["resourceClient."]
    methods:
      fetchResource: GET
      saveResource: POST
```

Java method names are case-sensitive. Enrichment matches the parsed invocation's
receiver and method, not text inside its arguments, comments, or string literals.
Conflicting method mappings do not select an arbitrary HTTP verb. The wrapper's URL
expressions still have to be resolvable from indexed source; method configuration alone
does not establish a target endpoint.

##### Custom Java Queue Frameworks

Queue injection conventions such as `@QueueBinding(QueueNames.EVENTS)` with
`MessageWorker.handle` are not AWS SDK conventions and are not built into the parser.
Declare them under `java_sqs`:

```yaml
java_sqs:
  queue_annotation: QueueBinding
  queue_enum: QueueNames
  consumer_types: [MessageWorker, BaseMessageWorker]
  handler_method: handle
  constant_suffix: _QUEUE
  queue_url_fields: [queueUrl]
  send_methods: [sendDelayed]
```

Use the annotation, enum, base class or interface, and handler names from your source.

- `constant_suffix` is optional and removes that suffix from injected queue constants.
- `consumer_types` match declared interfaces or direct base classes.
- `queue_url_fields` optionally identifies fields assigned a queue constant, including
  URL-prefix concatenations. It is unnecessary for annotation-only injection.
- `send_methods` adds exact wrapper method names to the standard `sendMessage` and
  `sendMessageBatch`; arbitrary methods containing "send" are not queue sends.
- For constructor injection through a property key, set `property_annotation` to the
  annotation name (for example `Named`). The indexer resolves its value against
  repository `.properties` files, only for configured consumer types with the
  configured handler. Keys with conflicting values are not resolved, and no
  production, staging, or development directory is implicitly preferred.

Producer lookup follows the send expression and local initializers, not unrelated queue
references elsewhere in the method. Parameters and block-local variables shadow
injected class bindings, and explicit `this.field` references use the current class.
After reassignment, a local request resolves only when every non-null assigned value
before the send identifies the same queue; unknown values, conflicting queues, and
self-referential updates stay unresolved. Imported and fully qualified references to
the configured enum are both supported. This is not arbitrary Java control-flow or
interprocedural request analysis. An omitted `java_sqs` section disables
custom-framework inference; it does not change Java class, method, HTTP, or database
extraction.

##### Custom JavaScript Queue Wrappers

SDK method handling is built in; custom method names and queue-name prefixes are not
treated as platform conventions. Declare deployment-specific wrappers under
`javascript_queues`:

```yaml
javascript_queues:
  load_receivers:
    - messageClient
  send_methods:
    - enqueueTask
  read_methods:
    - readPendingTasks
  delete_methods:
    - removeTask
```

`load_receivers` matches exact source expressions. In this example
`messageClient.load("tasks")` establishes a queue target from its first argument, and
both later calls on `messageClient` and a variable assigned the load result can use that
target. Custom method names match exactly and still require a queue receiver or target.
Configure only wrappers whose implementation has these semantics.

##### Custom GraphQL Registration Wrappers

GraphQL does not specify a JavaScript call for registering a resolver directory.
Declare application-specific wrappers under `graphql_registrations`:

```yaml
graphql_registrations:
  - receiver: resolverRegistry
    method: load
    controllers_path_key: directory
```

This recognizes `resolverRegistry.load({directory: ...})`. Receiver and method names
match exactly. Omit `controllers_path_key` for a positional path argument, such as
`resolverRegistry.load(path.join(__dirname, 'resolvers'))`. No registration wrappers
are enabled by default, and this setting does not disable GraphQL operation extraction.

##### C# Entity Mapping

C# table mapping needs no configuration. Table identity comes from explicit `[Table]`
attributes or Entity Framework project and model metadata, not directory names or
capitalization. For database-first models the indexer follows `.csproj` `DependentUpon`
entries from a generated source file through its template to its `.edmx` model; XML
storage sets and mapping fragments supply the table and schema. A defining query
contributes a mapping only when SQL extraction identifies one read relation, so joined
queries and conflicting model mappings are not collapsed into an arbitrary table.

The metadata refresh runs for incremental indexing even when the `.cs` content is
unchanged. Project dependencies must stay inside the repository, including symlink
targets. Conditional or wildcard project entries are not evaluated as MSBuild, and
custom template renaming is not inferred. Explicit source attributes take precedence
over generated-model mappings.

#### Trace YAML

`trace.yaml` holds Trace heuristics. Its top-level keys are `default_profile`,
`profiles`, `name_based`, `http`, `sqs`, and `repo_overrides`.

##### Trace Route Prefixes

Route-prefix aliases are deployment configuration, not universal routing rules. Trace
does not add or remove `/api` or `/action` by default and does not strip `/vN` prefixes
automatically. Exact routes, parameter syntax, and indexed gateway routes match without
aliases.

For a deployment that deliberately exposes the same routes under a prefix, set
`http.context_path_prefixes`. Prefer the calling repository's override to a global
alias:

```yaml
repo_overrides:
  example-client:
    http:
      context_path_prefixes:
        - /api
```

Use the actual repository name and only prefixes its routing configuration supports.
Version prefixes distinguish contracts unless the deployment explicitly rewrites them;
do not add them to increase match counts.

Index audit HTTP classification and HTTP repository-edge collection use the same
`http.context_path_prefixes` setting for the calling repository, so configure real
deployment aliases before comparing results across installations. The audit does not
insert a slash into literal routes such as `/apiusers`, add `/api` or `/action`
implicitly, or drop leading path parameters. Parameter-syntax matching is supported,
and a leading tenant parameter is part of the route, not evidence of a disposable base
URL.

#### Owners YAML

Create `owners.yaml` to supply fallback ownership when a repository's CODEOWNERS does
not match a file:

```yaml
default_owners:
  - "@your-org/platform"
repo_overrides:
  catalog-api:
    owners:
      - "@your-org/catalog"
```

Repository keys match the indexed repository name exactly. Resolution order is:
matching CODEOWNERS rule, repository override, then default owners. CODEOWNERS follows
GitHub's rules: the last matching line wins, `docs/*` covers files directly in `docs/`
but not nested ones, and a matching line with no owners marks the path as explicitly
unowned, with no fallback. Tirion searches `CODEOWNERS`, `.github/CODEOWNERS`, and
`docs/CODEOWNERS` in that order and uses the first file that exists. Without
configuration there is no fallback ownership. The resolver caches these values, so
restart the API after changing `owners.yaml` or CODEOWNERS.

## Running Tirion For A Team

Deployment shape, packaging and distribution, services, access, scheduled refresh,
and snapshot retention for a shared installation. Settings are in the
[configuration reference](#configuration-reference).

### Runtime Shape

Recommended deployment:

- one Linux VM
- PostgreSQL on the same VM
- `tirion serve` on the same VM
- static frontend served by a reverse proxy
- target repos cloned on disk

`tirion serve` exposes the HTTP API. The web UI is a separate static bundle.

### Build And Package

#### Release bundles

Release packaging is split by use:

| Bundle | Contents |
| --- | --- |
| `tirion-server` | Hosted VM/server runtime: `tirion`, `parse`, the `extract-*` helpers (including the optional `extract-eventbridge` importer), `bin/nightly-refresh.sh`, `bin/clone-repos.sh`, the service templates under `scripts/`, and `.env.example` |
| `tirion-mcp` | Developer-local MCP binaries: `mcp-intel` and `mcp-guard` |
| `tirion-tools` | Support and debug tools: `impact-report`, `contracts-report`, `audit-index` |
| `tirion-frontend` | The built static web UI (`index.html` and assets), published as `tirion-frontend-<tag>.tar.gz` |

The native server, MCP and tools archives are named `tirion-<bundle>-<version>-<os>-<arch>` (`.tar.gz`, or
`.zip` on Windows). Every native bundle includes the project LICENSE,
THIRD_PARTY_NOTICES.md and the documents listed in `scripts/release-documents.txt`.
The frontend bundle includes LICENSE and its own dependency notices, copied from
`frontend/public/THIRD_PARTY_NOTICES.md`. Keep these files with redistributed assets.

No license key or activation is required. MCP binaries connect to the API;
`audit-index` requires authorized access to the index database. See the CLI
reference for each tool's connection options.

#### Building

`bash scripts/build-dist.sh <version>` packages the host's native OS/architecture
with CGO enabled. It does not cross-compile, and a failed target is never reported
as a successful partial release. Run it on the target platform or use the Linux
Docker wrapper below. `dist/` is generated output and is replaced.

On a Windows host with Go and MinGW GCC installed, the MCP-only builder is:

```powershell
pwsh -File scripts/build-windows-release.ps1 -Version dev
```

It does not require PostgreSQL or a running API. `-DatabaseUrl` explicitly opts
into the optional deployment connectivity check; `-SkipDbCheck` still skips it.
`-OutputDir` must be a generated subdirectory of this checkout's `dist/`, without
symlinks or junctions. Its contents are replaced, so do not store source or user
data there. The default is `dist/windows-amd64-mcp`.

The bundled documentation includes source-development references as well as
runtime instructions. Sample corpora under `testdata/`, contributor checks, and
`scripts/estate-fixture-check.mjs` are distributed in the source checkout, not the
native archives. Use a checkout at the release's tag for those workflows, and run
their commands from that checkout with a separate disposable database. The
prebuilt installation sequence below does not require them. The runtime scripts in
the server archive are the clone/refresh helpers under `bin/` and the service
templates under `scripts/`.

`scripts/release-documents.txt` is the documentation manifest; keep it aligned with
documentation links. Packaging must not recursively copy local documentation
directories because they may contain ignored local material.

#### Linux builds from another host

`bash scripts/build-linux-dist-local.sh <version>` uses the current Docker context,
not a specific Docker Desktop or Colima installation. It builds natively inside the
selected Linux container platform. `DOCKER_CONTEXT_NAME`, `DOCKER_PLATFORM`
(default `linux/amd64`) and `DOCKER_IMAGE` are optional overrides; the default
image version comes from `go.mod`. The build mounts this checkout and replaces its
generated `dist/` output. Cross-architecture execution requires support from the
selected Docker engine.

#### Release workflow

The tag-triggered release workflow builds Linux x86-64 server/MCP/tools bundles
and the frontend bundle, plus a Windows x86-64 MCP bundle. Other native builds are
possible through the script but are not advertised as CI-produced release assets.
Go versions come from `go.mod`.

- The workflow publishes to the repository running it, using its built-in GitHub
  token. Only the final publication job receives `contents: write`; build jobs
  keep read access. No personal token or separate release repository is required.
- An optional `RELEASE_REPOSITORY` variable (`owner/repository`) selects an
  external destination. That path requires `RELEASE_REPO_TOKEN` with
  release-upload access to the destination. Artifact-signing credentials stay
  optional and separate from upload authorization. Review the destination before
  pushing a release tag; source cleanup itself does not publish a release.
- Portable Checks run at the exact tagged commit, including native Windows MCP
  execution, and the build and signing jobs depend on them. Action implementations
  are pinned to immutable upstream commits; update the pins and review their
  release notes together. `SOURCE_COMMIT.txt` and the release notes identify the
  source revision and workflow run.
- Build jobs stage artifacts without uploading. Only after every job succeeds does
  the publisher generate checksums, optionally sign them, upload a draft, and
  publish it. The workflow cannot overwrite an already-published version. A failed
  upload may leave a draft; a retry checks for stale assets before replacing
  matching files, so remove obsolete draft assets before retrying.

#### Windows MCP trust

Windows MCP binaries may be signed in CI. Internal rollout can use a self-signed
certificate, but each Windows machine must trust that certificate before Windows
treats the binaries as trusted.

#### Distribution

Tirion uses [Apache-2.0](LICENSE). Runtime license files are not required.
Preserve dependency notices and fixture provenance when redistributing source or
binaries. Release signatures verify artifact integrity. Never include credentials,
private source, index dumps, or local validation output in a release.

### Install Sequence

For a source checkout, use [SETUP.md](SETUP.md). The sequence below installs
prebuilt release bundles.

1. Install PostgreSQL 16 with `pg_trgm` (the portable CI baseline).
2. Create a dedicated role and `tirion` database with `pg_trgm`, as described in
   [Prepare PostgreSQL](SETUP.md#2-prepare-postgresql).
3. Place the server bundle under `/opt/tirion`.
4. Place the frontend bundle under `/opt/tirion-frontend`.
5. Set `DATABASE_URL` (see [Supplying Settings](#supplying-settings)).
6. Start `tirion serve` behind the deployment's access boundary.
7. Configure Caddy or nginx in front of the UI and API.
8. Clone repos under a stable repo root.
9. Run `tirion index -root <repo-root> -workspace <workspace>`.

Flag order, workspace creation, `-dry-run`, the run summary, and discovery rules
are described under [`tirion index`](DOCS/cli-reference.md#tirion-index) and
[repository discovery](DOCS/cli-reference.md#repository-discovery). Keep `parse` and the
`extract-*` helpers beside the `tirion` executable.

The API does not serve static frontend files. Configure the reverse proxy to serve
the frontend bundle and forward `/api` to the API process.

The frontend uses same-origin `/api` requests and history-based routes. Configure
the static server to fall back to `index.html` for UI routes, but never for `/api`
requests. Preserve the `/api` prefix when proxying; do not strip it. Serve the
directory containing the built `index.html`, not the source `frontend/` directory.
Protect both UI and API with your deployment authentication, and prevent clients
from bypassing the proxy through port 8080. Vite's development proxy is not
included in the production bundle.

Before enabling shared access, check the protected `/api/health` endpoint, open a
UI route directly (including browser reload), select a workspace, and search for a
known indexed symbol. Confirm unauthenticated requests are rejected by the proxy
and direct backend access is blocked from client machines.

### Service Templates

`scripts/tirion.service` is the API unit template; install it as `tirion.service`
to match the refresh script's service name. The template runs
`/opt/tirion/tirion serve` as the `tirion` user and group and permits writes under
`/data/tirion/repos` for repository operations. Create that user and directory and
give it ownership of its repository clones before enabling the service. Workspace
Git operations disable hooks and fsmonitor; credential helpers remain available for
fetch. If repos live elsewhere, update `ReadWritePaths` and the configured repo root
together. Both service templates are included under `scripts/` in the server
archive; the refresh and clone executables are under `bin/`.

The templates are starting points, not installers. Place protected environment
settings in `/opt/tirion/.env` using systemd `KEY=value` syntax, without shell
`export` commands. Enable the API unit only after configuring its paths and
permissions. Read API logs with `journalctl -u tirion.service`.

`ProtectHome=true` makes ordinary home-directory configuration unavailable to the
API unit, so both templates set `HOME=/var/lib/tirion`. The API unit uses
`StateDirectory=tirion` (mode 0700) to create a writable state directory for the
API token and worktrees. Provision the same directory for standalone indexing when
you keep `.tirion` configuration there, and give the indexing service the same
parser and trace configuration.

Workspace operations use the repository path recorded by indexing. After moving
clones, re-index from their new location or set `TIRION_REPOS_ROOT` on the API
service to the explicit clone root. Tirion does not search beside its executable,
beside its working directory, or under an implicit `/data` path. Managed checkout
destinations are configured separately with `TIRION_WORKTREE_ROOT`; both variables
are listed in [Environment Variables](#environment-variables).

Use a persistent writable worktree root and add it to the API unit's
`ReadWritePaths` when it lies outside `/data/tirion/repos`. The API default is
`~/.tirion/workspaces`; existing registered worktrees remain usable. Request-body
worktree paths are rejected.

`tirion serve` drains in-flight requests for up to 30 seconds on SIGTERM and SIGINT
(see [`tirion serve`](DOCS/cli-reference.md#tirion-serve)), so a systemd stop does not
cut a request short unless it runs longer than that.

`scripts/tirion-refresh.service` uses the same service identity and also reads the
optional `/etc/tirion/github.env`. Provision `/var/log/tirion` for that user. The
refresh script stops and restarts the API by default and requires a narrowly scoped,
passwordless service-control permission; configure that explicitly, or set
`TIRION_REFRESH_STOP_API_DURING_REFRESH=0` if your deployment intentionally
refreshes with the API running. Neither template creates users, schedules a timer,
installs credentials, or changes a running service.

### Authenticated Reverse Proxy

For example, nginx can serve the static UI and proxy the API on the same origin:

```nginx
server {
    listen 443 ssl;
    server_name tirion.example.com;
    ssl_certificate /etc/tirion/tls/fullchain.pem;
    ssl_certificate_key /etc/tirion/tls/privkey.pem;
    auth_basic "Tirion";
    auth_basic_user_file /etc/tirion/htpasswd;
    root /opt/tirion-frontend;

    location /api/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
    location / {
        try_files $uri $uri/ /index.html;
    }
}
```

Provision your real hostname, a trusted TLS certificate, and a protected password
file (for example with interactive `htpasswd -c /etc/tirion/htpasswd <user>`). Do
not commit the password file or certificate private key. Keep `tirion serve` on its
`127.0.0.1` default; allow client traffic only to the proxy's HTTPS port.

On the backend set `TIRION_ALLOWED_HOSTS=tirion.example.com` and
`TIRION_ALLOWED_ORIGINS=https://tirion.example.com`. TLS termination does not make
forwarded headers trusted; see
[Hosts, Origins, And Limits](DOCS/server-api.md#hosts-origins-and-limits).

Every API request must carry the Tirion token in `X-Tirion-Token` in addition to
the proxy credentials. The UI prompts for it; configure `TIRION_API_TOKEN` or
`TIRION_API_TOKEN_FILE` on remote clients. `mcp-intel` connects to the HTTPS origin
with its Basic Auth variables or authorization header; see
[MCP clients](#mcp-and-clients) and [mcp.md](DOCS/mcp.md#connect).
Remote PostgreSQL connections require an appropriate TLS and credential
configuration; the local `sslmode=disable` example is not intended for them.

### Access Control

Use:

- an internal network boundary
- a reverse proxy
- Basic Auth or OIDC/SSO appropriate to the deployment
- firewall rules that prevent direct access to the raw app port

None of these replaces the Tirion service token, which every API route requires
(`X-Tirion-Token` or `Authorization: Bearer`; see
[the HTTP API reference](DOCS/server-api.md#authentication)). Set `TIRION_API_TOKEN` or
`TIRION_API_TOKEN_FILE` in the unit's `/opt/tirion/.env` so the token is stable and
managed by your secret process, or let the server create `api-token` under its
state directory on first start. Tune capacity with `TIRION_MAX_CONCURRENT_READS`
(default 16) and `TIRION_MAX_CONCURRENT_ADMIN` (default 2); exhausted slots answer
503 `BUSY`.

Repository fetches run with a filtered environment. Behind a corporate proxy or a
private certificate authority, set `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`,
`ALL_PROXY`, `SSL_CERT_FILE`, or `SSL_CERT_DIR` in the service environment. They
are passed through to Git and parser subprocesses, whereas `GIT_*` overrides,
loader variables, and application credentials are not.

### Repository Refresh

Refresh is an optional scheduled deployment operation, not required for queries.
It needs Bash, Git, the complete native bundle and PostgreSQL client utilities
(`pg_dump`, `psql`). GitHub discovery additionally needs authenticated `gh`, or
`curl` and `jq` with a token. Configure scheduling yourself; no timer is installed.
`scripts/nightly-refresh.sh` is shipped as `bin/nightly-refresh.sh` in the server
bundle.

| Setting | Default | Effect |
| --- | --- | --- |
| `DATABASE_URL` | none | Required; refresh has no default connection URL |
| `REPOS_ROOT` | `/data/tirion/repos` | Refresh clone root; distinct from the API's `TIRION_REPOS_ROOT` |
| `TIRION_BIN` | `<install root>/tirion` | `tirion` executable the script runs |
| `TIRION_REFRESH_WORKSPACE` | `default-main` | Workspace to refresh |
| `TIRION_REFRESH_SKIP_UNCHANGED` | `true` | Set `false` after parser or configuration upgrades |
| `TIRION_REFRESH_MAX_ATTEMPTS` | `3` | Attempts before the refresh is reported as failed |
| `TIRION_REFRESH_STOP_API_DURING_REFRESH` | `1` | Stop and restart the selected API service |
| `TIRION_REFRESH_API_SERVICE_NAME` | `tirion.service` | Exact service operated on |
| `TIRION_BACKUP_ON_FAIL` | `1` | Take a temporary pre-refresh dump and restore it after failed attempts |
| `TIRION_BACKUP_DIR` | `/tmp` | Temporary dump and log directory |
| `TIRION_REFRESH_STATUS_FILE` | `/var/log/tirion/refresh-status.env` | Latest refresh status |
| `TIRION_GITHUB_ORGS` | unset | Space-separated organizations to discover and clone (default-main only) |

Configure the actual runtime identity through the service environment and local
credential mechanism, not a baked-in example password.

The default-main path applies remote default branches (`tirion sync-branches
-mainline`) before indexing. A named workspace indexes its existing managed
worktrees; it does not fetch or check out new target refs automatically. Use
workspace fetch and checkout first when advancing that baseline. A refresh backup
is deleted on script exit and is not a retained disaster-recovery backup. Do not run
concurrent writers during a refresh that can restore its pre-refresh database
state; their changes could be overwritten.

For default-main nightly discovery, set `TIRION_GITHUB_ORGS="your-org another-org"`
in the service environment. Without it, nightly refresh operates on existing
repositories without discovering or cloning an organization. Authentication uses
`gh`, `GH_TOKEN`, or `GITHUB_TOKEN`, with access to the configured organizations.
When organizations require different credentials, invoke
`clone-repos.sh --org ...` separately under each credential before indexing; do
not put those credentials in source control.

`scripts/clone-repos.sh` requires an explicit `--org`; repeat the flag for multiple
organizations. There is no default organization. The clone script supplies GitHub
HTTPS credentials through a command-local Git credential helper restricted to
`https://github.com`. It does not embed tokens in clone URLs or persist them in
repository configuration. Only the selected origin is fetched; unrelated remotes
are not modified, and existing credential-bearing remotes are not rewritten
automatically. A failure to list an organization's repositories is reported as a
failure, not as a successful empty organization. Existing paths must be dedicated
clones with the expected origin. Symlinks, linked worktrees, missing origins, and
mismatched origins are conflicts: they remain untouched and the command exits
nonzero, including in dry-run mode.

Workspace API fetches use `GH_TOKEN`, then `GITHUB_TOKEN`, for HTTPS GitHub
authentication. The helper is command-local and URL-scoped; no token is inserted
into command arguments or remote URLs. Without either variable, Git uses the
service account's configured authentication, so repositories that need different
identities can use a credential helper for the service account. SSH and other hosts
keep their own Git authentication. See
[Git credential contexts](https://git-scm.com/docs/gitcredentials).

### Estate-Backed PR Reporting

The Impact Report workflow is optional. Set the repository variable
`ENABLE_ESTATE_IMPACT=true` and configure `IMPACT_SERVER` and `TIRION_API_TOKEN`
(and optionally `IMPACT_REPO`) to enable it for same-repository PRs. It is skipped
for fork PRs and for repositories without that opt-in. The portable parser fixture
workflow remains separate; contributing must not require access to a private
indexed estate.

The [GitLab example](DOCS/snippets/impact-report.gitlab-ci.yml) follows the same
opt-in: `ENABLE_ESTATE_IMPACT=true`, a reachable HTTPS `IMPACT_SERVER`, a masked
`TIRION_API_TOKEN`, and a masked `GITLAB_TOKEN` with permission to post
merge-request notes. Fork merge requests are excluded. Keep its Go image aligned
with `go.mod`.

### Importing External Schedules

The optional `extract-eventbridge` importer accepts a JSON array in Tirion's
format, not AWS console HTML. Convert external exports to this structure:

```json
[
  {
    "ruleName": "resource-refresh",
    "scheduleExpression": "rate(5 minutes)",
    "targetType": "sqs",
    "targetName": "resource-events",
    "state": "ENABLED",
    "source": "eventbridge"
  }
]
```

Run `./bin/extract-eventbridge -file schedules.json -dry-run` from a built source
checkout to inspect the records without connecting to the database. Omit `-dry-run`
to import into the database selected by `DATABASE_URL` or `-db`, after normal
schema setup. `state` must be `ENABLED` or `DISABLED`; `source` must be
`eventbridge` or `scheduler`. Unknown fields and duplicate rule/target pairs are
rejected. In an extracted server bundle use `./extract-eventbridge` instead.

Imports atomically upsert the supplied rule/target pairs. Unspecified schedules are
preserved; this command does not delete stale records. The schedule table is
database-global, not workspace- or AWS-account-scoped. Do not combine exports with
colliding rule/target names from separate accounts in that table.

### Indexing Runs And Snapshot Retention

Each indexing attempt builds into its own snapshot generation, so a failed or
interrupted run never replaces the graph that queries are reading.

**Publication.** Indexing creates an unpublished generation for every attempt,
including a rebuild at the same SHA. The requested parse and enrichment stages run
there. A final transaction checks the workspace selection, publishes the
candidates, reconsiders inferred calls, repairs incoming targets and rebuilds Trace
relationships. An error rolls back publication, and a failed attempt remains
unselected. Batch CLI and API indexing publishes the batch together. The UI bulk
operation still reports and publishes one repository at a time through the same
coordinator. Checkout and status changes keep the last selected graph until a
replacement is available, so the selected graph revision can differ from the
checkout.

**Shared snapshots and disk.** A workspace may share snapshots with another
workspace. Resolution copies the affected caller snapshots, including incoming
callers that reference their declaration IDs, before changing their relationships;
published graph rows stay intact for in-flight readers and inherited or mainline
selections. Unrelated repositories stay selected without copying. Publication and
workspace inheritance serialize, while source parsing can still proceed
independently in different workspaces. An explicit whole-workspace resolve copies
the full selected set; ordinary indexing copies the affected incoming dependency
closure, which can be large in a closely connected estate. Allow disk headroom and
measure this in focused validation. A selection change during an attempt makes
publication fail with a retry instruction.

**What `pipeline_complete` means.** It means the default parse, the six enrichers
and the requested workspace resolution finished successfully. It does not certify
complete language coverage or correct semantics.

**Skipping unchanged repositories.** `-skip-unchanged` also requires the recorded
clean source state, a matching binary/configuration fingerprint, and unchanged
metadata, include and ignored-source inputs. Snapshots written by older builds lack
this evidence and are reindexed. Intentional partial modes (`parse`, standalone
enrichment, `-skip-extractors`, selected extractors or `-global-resolve=false`)
remain usable but cannot satisfy a later full-pipeline skip. Partial modes repair
existing incoming paths; disabling cross-repository resolution does not infer new
generic cross-repository calls. Non-Git repositories cannot use HEAD-based
skipping.

**Incremental parsing.** An incremental parse copies the old facts into a
candidate, explicitly remapping file and declaration IDs, then replaces changed and
deleted files. A changed fingerprint or changed metadata, include or ignored-source
inputs force a full parse. RPG sources are revisited during incremental parsing
because copybooks can change their aliases. Source files and metadata are checked
again before publication. Explicit sibling-repository RPG copybooks remain
supported; their paths, content hashes and missing-path probes are recorded
separately. Keep these external sources stable during indexing. Symlinks cannot
supply bytes outside the selected read root.

**Ignored inputs.** Git-ignored files such as local resource configuration can
still be parser inputs. Their paths and hashes are captured in `ignored_manifest`;
new, removed or changed ignored inputs invalidate skipping. The dependency and
output directories pruned by native parsing are excluded from this inventory.
Manifests contain hashes and paths, not configuration values.

**Failures and timeouts.** Broken, absolute, directory, and out-of-repository
source symlinks are excluded from parsing and input manifests and are logged; they
never abort a run. Ordinary source read failures still prevent publication. A file
that times out (`CODE_INTEL_PARSE_TIMEOUT_MS`, default 30000 ms per file, `0`
disables) or hits a recovered internal error loses its facts; the parse summary
lists such files under `Files that lost facts`, and the run then fails without
publishing, so the previous snapshot stays active. Fix or exclude those files, or
pass `-allow-file-failures` to `tirion index` to publish anyway (the snapshot is
then not reused by `-skip-unchanged`). Retry interrupted indexing normally; never mark incomplete
generations complete.

**Enricher source checks.** Enrichers require source bytes matching the indexed
SHA-256 hash, using the indexed Git revision as a fallback when the checkout has
moved. Dirty or non-Git source works while its indexed bytes remain available.
Enrichers fail on missing or mismatched bytes. Native parser reads are confined to
regular files under the repository root. Keep clones and worktrees stable during
indexing.

**Per-file replacement.** Java and Spring enrichment replace a file's facts in one
transaction. A successful empty extraction removes obsolete facts; unsupported
syntax keeps the previous file facts and is reported. Read, database, unexpected
parser and commit failures stop the command. This per-file boundary does not by
itself make a multi-repository pipeline atomic; the generation publication above
supplies that boundary. Validate publication and rollback against an isolated
database before upgrading a shared deployment.

**Pruning old generations.** Superseded and interrupted snapshots can be removed
with `tirion prune-snapshots`. Run it between indexing runs; it refuses to proceed
while a workspace is indexing. The default is a dry run, a minimum age of 30 days,
and retention of the latest two successful generations per repository and
workspace:

```bash
./bin/tirion prune-snapshots
./bin/tirion prune-snapshots -older-than 720h -keep 2 -apply
```

Review the candidate list before adding `-apply`. Cleanup is transactional and
protects active and mainline selections in every workspace and generations still
referenced by retained graph facts, so more than `-keep` generations may remain.
It also removes polymorphic annotation and generic metadata before cascading file
deletion. Pruning removes historical source and graph evidence from the listed
generations; saved reports are not a replacement for that evidence. Database space
becomes reusable after deletion; PostgreSQL does not necessarily shrink the
physical files immediately. No automatic cleanup runs during indexing. Flags are
listed under [`tirion prune-snapshots`](DOCS/cli-reference.md#tirion-prune-snapshots).

Parser and extraction corrections require rebuilding the native helpers and
reindexing the affected repositories before their stored graph facts change.

### Validation Checklist

1. Build the complete command set:

   ```bash
   bash scripts/build-local.sh
   ```

2. Parser sanity against disposable source and database inputs:

   ```bash
   ./bin/parse -v ../storefront
   ./bin/parse -v -incremental ../storefront
   ```

3. Parser regression tests:

   ```bash
   GOCACHE=/tmp/go-build go test ./internal/parser -run TestJavaScriptHttpCallsFromConfig
   ```

4. Inspect an indexed relationship through the running API:

   ```bash
   ./bin/tirion trace -resolve ResourceController.create
   ```

Use an actual symbol from your index. For asserted cross-repo behavior, use the
portable estate check described in [CONTRIBUTING.md](CONTRIBUTING.md), not a
successful CLI exit alone.

### Performance Notes

- Repo cleanup is fast because `pending_*`, `http_client_calls`, and `sqs_*` include
  `repo_id` and are deleted by `repo_id`, not by string prefix.

## Updating And Upgrading

Indexing reads local checkouts; it does not pull new commits. Fetch or update your
dedicated clones, then rerun `tirion index`. A named workspace needs its selected ref
checked out before indexing. After changing parser code or `patterns.yaml`, use
`-skip-unchanged=false` even when Git HEAD is unchanged. After updating Tirion's own
source, rebuild the whole command set together, reinstall UI dependencies if the
lockfile changed, and back up the database first: schema initialization may apply
changes on startup. See the [workspace sequence](DOCS/cli-reference.md#tirion-workspace)
and [backup and upgrade](#backup-and-upgrade). To reclaim space, run
`./bin/tirion prune-snapshots` to preview eligible snapshot generations and add
`-apply` to remove them; retention and protection rules are in
[Running Tirion For A Team](#running-tirion-for-a-team).

### Backup And Upgrade

Use a PostgreSQL client compatible with the server version. Store backups outside
the source checkout and release directories, with restricted permissions. A dump
contains indexed source and repository metadata, not just table definitions.

```bash
# Use your configured PostgreSQL credentials; do not paste passwords here.
pg_dump --dbname="$DATABASE_URL" --format=custom --file=/private/backup/tirion.dump
pg_restore --list /private/backup/tirion.dump
```

A listing only checks that the archive can be read. Rehearse restoration into a
new, disposable database before relying on it:

```bash
createdb tirion_restore_check
pg_restore --exit-on-error --no-owner --no-acl \
  --dbname=tirion_restore_check /private/backup/tirion.dump
```

Use administrator `-h`/`-U` options appropriate to your installation. Never point
this rehearsal at the live database. Confirm the restored workspace and repository
records and representative graph queries. Repository clones, custom patterns, trace
configuration and service credentials require separate protected backups; the
database does not replace them.

For an upgrade:

1. Arrange a maintenance window and pause scheduled indexing.
2. Back up the database first, as above.
3. Stop only the deployment's API, and replace the whole native command set
   together, including `parse` and every `extract-*` helper. Starting the new API
   can migrate the schema.
4. Read [Upgrading From Earlier Builds](#upgrading-from-earlier-builds) for the
   configuration and behavior changes that apply to your starting version.
5. Reindex with `-skip-unchanged=false` after parser or configuration changes, then
   check workspace alignment, known cross-repo paths and UI/MCP access before
   resuming refreshes.

Rollback means restoring a compatible database backup and matching binaries, not
merely putting an older executable against a migrated database. Keep the
pre-upgrade backup and its matching binaries until the upgrade is validated.

### Upgrading From Earlier Builds

Read this section before moving an existing installation to a newer build. Notes
are grouped by topic and say what changed and what to do. General procedure:

- Back up first, replace the whole native command set together, and keep the
  backup and matching binaries for rollback ([Backup And Upgrade](#backup-and-upgrade)).
- Parser and extraction behavior changes only reach stored graph facts after you
  rebuild the helpers and reindex the affected repositories with
  `-skip-unchanged=false`.
- Schema changes apply during normal schema initialization when a new binary
  starts, not when `CODEBASE_SKIP_SCHEMA_INIT=1` is set.

#### Configuration and environment

- **No default database.** Set `DATABASE_URL` or pass `-db` before any database
  command; Tirion does not choose a database when both are missing, and nightly
  refresh likewise requires `DATABASE_URL`. Schema migrations apply only to the
  explicitly selected database. The name `tirion` is a convention: existing
  `codebase_intel` databases keep working when named explicitly, with no rename or
  data copy.
- **`.env` files in the working directory are no longer loaded.** Native commands
  read the process environment and, if set, the absolute file named by
  `TIRION_ENV_FILE`; a `.env` or `.env.local` beside the command is ignored. Move
  needed settings into the service environment or a trusted file outside
  repositories. See [Supplying Settings](#supplying-settings).
- **State directory moved from `~/.codebase-intel` to `~/.tirion`.** New
  installations keep parser, trace and owner configuration, the API token and
  managed worktrees in `~/.tirion`. `TIRION_HOME` selects another absolute directory
  and disables the legacy fallback. Without it, an existing
  `~/.codebase-intel/<file>` is still used, with a migration message in the log,
  while the corresponding `~/.tirion` path is absent; the new path wins when both
  exist. To migrate, during a maintenance window copy `patterns.yaml`,
  `trace.yaml`, `owners.yaml` and `api-token` into `~/.tirion`, preserving
  permissions (`0700` directory, `0600` token). Keep existing worktrees in place
  and set `TIRION_WORKTREE_ROOT` to their absolute legacy location, because
  database rows retain those paths; moving worktrees also requires updating or
  reindexing their registered paths. Never run old and new service units against
  the same worktrees.
- **Service unit name.** Install `scripts/tirion.service` as `tirion.service`, the
  name the refresh script expects; migrate an older, differently named unit during
  maintenance.
- **Repository root variable.** `TIRION_REPOS_ROOT` is the API setting for the
  clone root; `REPOS_ROOT` is read only as a legacy fallback by the API (the refresh
  script uses `REPOS_ROOT` for its own clone root).
- **Worktree root variable.** `TIRION_WORKTREE_ROOT` is preferred; the legacy
  `TIRION_WORKSPACES_ROOT` is still read by the CLI and API as a fallback.
- **GitHub credentials for refresh and clone.** The former organization-specific
  token variables are no longer read, and `clone-repos.sh` no longer has a
  default organization or organization-specific wrapper. Migrate to one standard token
  (`GH_TOKEN` or `GITHUB_TOKEN`) with the required repository access, or leave the
  variables unset and configure a credential helper for the service account when
  repositories need different identities. Pass `--org` explicitly (repeat it for
  several) and set `TIRION_GITHUB_ORGS` for nightly discovery. Existing
  credentials and remote configuration are not modified automatically.
- **Packaging.** `build-dist.sh` no longer guesses cross-compilation targets or
  treats a failed target as a successful partial release; build on the target
  platform or use the Linux Docker wrapper.
- **Estate check wrappers.** The former organization-specific release and latency
  wrappers are not distributed as public gates. `scripts/estate-fixture-check.mjs`
  and `scripts/search-golden-check.sh` (with an explicit TSV manifest) remain.
- **Parser configuration.** Parser conventions live in `patterns.yaml` for the
  process account (see [Patterns YAML](#patterns-yaml)). An
  absent file uses generic defaults; malformed YAML, unknown keys, unreadable files
  and unsupported HTTP-client values fail before extraction, and the API refuses
  requests (503 `CONFIG_INVALID`) while supplied pattern configuration is invalid.
  Configure the API and indexing helpers under the same service `HOME`, protect
  the configuration permissions, restart long-lived processes after changes, and
  run a full reindex with `-skip-unchanged=false` after changing conventions.

#### Database and indexing

- **Existing database.** Schema initialization seeds missing `default-main`
  repository rows from the legacy repository state, and it leaves existing
  workspace selections and snapshot status unchanged on later starts. A missing
  legacy branch stays unset rather than defaulting to `master`; select the intended
  ref through workspace controls before checkout and indexing when an old database
  has no branch metadata. Upgrades also add trace identity columns before canonical
  deduplication uses them and add snapshot support to older resource-alias tables.
  This does not repair workspace selections already overwritten by an earlier
  version.
- **Snapshot generations.** Snapshot identity now includes a generation, and old
  parsers and helpers do not understand candidate IDs or the changed uniqueness
  key. An old helper that lacks `-repo` or candidate support returns an upgrade
  error instead of silently rerunning across the whole workspace. Replace the whole
  command set together. See [Indexing Runs And Snapshot Retention](#indexing-runs-and-snapshot-retention)
  for the publication model. Snapshots written before this evidence existed are
  reindexed even with `-skip-unchanged`.
- **Snapshot retention.** Superseded and interrupted generations are never removed
  automatically, so a database that has been through several upgrades and
  reindexes accumulates them. Preview with `tirion prune-snapshots` and remove with
  `-apply`, between indexing runs; the age, retention and protection rules are
  under [Pruning old generations](#indexing-runs-and-snapshot-retention).
- **Input manifest.** Schema initialization adds a nullable
  `repo_snapshots.input_manifest` with hashes for property files, C# projects,
  models and templates, and workspace manifests. Parsing records the input set and
  rejects metadata changes during the run, and queue enrichment reads its property
  inputs from that set with the same hash and revision check as source files. Older
  snapshots need reindexing before property-derived queue enrichment; their
  manifest is deliberately not invented from a new checkout. The additive column
  can remain during a binary rollback, but old binaries do not enforce these checks;
  restore the pre-upgrade database if reverting facts written after the upgrade.
- **Java package column.** Java inheritance resolution records the source-declared
  package on each file in a nullable `files.java_package`. `NULL` means old or
  unrecorded metadata; an empty string means the unnamed package. Snapshot copies
  preserve it. Full indexing populates it, and Java enrichment can refresh it only
  from source bytes matching the indexed file hash. Fully reindex Java
  repositories when upgrading, including for package-qualified superclasses that
  older parsers did not capture.
- **Orphan metadata.** File and snapshot replacement now deletes annotations and
  type parameters before removing their owning declarations, but existing orphan
  rows are not removed on startup. After a backup, with indexing paused, this
  optional maintenance transaction removes only known owner types whose IDs do
  not exist anywhere in the database, so retained snapshots keep their metadata.
  Unknown owner types are left untouched. Review the affected count with
  `ROLLBACK` before repeating with `COMMIT`. It is an operator procedure that
  nothing runs for you:

  ```sql
  BEGIN;
  CREATE TEMP TABLE orphan_metadata AS
  WITH owners AS (
    SELECT 'class' AS kind, id FROM classes
    UNION ALL SELECT 'interface', id FROM interfaces
    UNION ALL SELECT 'method', id FROM functions
    UNION ALL SELECT 'field', id FROM fields
    UNION ALL SELECT 'parameter', id FROM constructor_params
  ), metadata AS (
    SELECT 'annotations' AS source, id, entity_type, entity_id FROM annotations
    UNION ALL SELECT 'type_parameters', id, entity_type, entity_id FROM type_parameters
  )
  SELECT m.source, m.id FROM metadata m
  WHERE m.entity_type IN ('class', 'interface', 'method', 'field', 'parameter')
    AND NOT EXISTS (SELECT 1 FROM owners o WHERE o.kind=m.entity_type AND o.id=m.entity_id);
  SELECT source, COUNT(*) FROM orphan_metadata GROUP BY source;
  DELETE FROM annotations WHERE id IN (SELECT id FROM orphan_metadata WHERE source='annotations');
  DELETE FROM type_parameters WHERE id IN (SELECT id FROM orphan_metadata WHERE source='type_parameters');
  ROLLBACK;
  ```

- **Default exclusions.** The directory names of the removed agent harness are no
  longer built-in exclusions. Use `-exclude` for worktree containers or other
  folders you do not want indexed.
- **`tirion prune`.** Pruning previously deleted by default and compared repository
  names against one directory listing. It now lists by default, requires `-apply`
  to delete, and uses each repository's registered absolute path. `-dry-run` is
  deprecated (listing is the default) and cannot be combined with `-apply`.

#### API and MCP behavior

- **The API requires a token on every route.** Every route, including
  `/api/health`, requires the service token (`X-Tirion-Token` or
  `Authorization: Bearer`). The server creates `api-token` on first start unless
  `TIRION_API_TOKEN` or `TIRION_API_TOKEN_FILE` supplies one. The UI prompts for
  it; `tirion` subcommands, `mcp-intel`, `mcp-guard` and `impact-report` read the
  same variables, discover the local file only for loopback API URLs, and require
  HTTPS plus an explicit token for any other origin. Update scripts, proxies and
  health checks that called the API anonymously. See
  [Authentication](DOCS/server-api.md#authentication).
- **`/api/stats` keys.** The response is a flat object with lowercase keys:
  `repos`, `files`, `functions`, `classes`, `endpoints`. Earlier builds capitalized
  them (`Repos`, `Files`, and so on); update clients that read the old spelling.
- **Server-controlled paths.** Request bodies can no longer set `root` or
  `parseBinary` (workspace index) or `worktreeRoot` (repository checkout and bulk
  checkout); such requests are 400 `VALIDATION_ERROR`. Configure `TIRION_REPOS_ROOT`
  and `TIRION_WORKTREE_ROOT` on the server and install the helpers beside `tirion`.
  Existing registered worktrees remain usable.
- **Verify breaking-change fields.** `breakingChanges[].attributes` is no longer
  emitted. Clients should present structural findings from `changeType`,
  `severity` and `evidence`. Severity no longer escalates on annotation names and
  carries no implied HTTP 400 guarantee: parameter removals and type changes are
  `high`, handler deletions `critical`.
- **MCP actor attribution.** MCP requests no longer derive an actor label from the
  operating-system username or hostname. To keep attributed local usage records,
  set `TIRION_ACTOR` (or `TIRION_USER`; legacy `CODEBASE_INTEL_USER` is still
  accepted). No actor header is sent when these are unset.
- **MCP tool list.** Placeholder tools from the removed run harness are no longer
  advertised. Reconnect the MCP client to refresh its tool list; the active graph
  tools and the source/diff helpers keep their names.
- **Flow lookup.** Supply the actual indexed route when starting a Flow. Lookup no
  longer prepends deployment-specific route directories or invents missing path
  arguments. Routes shared by several repositories remain separate root candidates
  rather than choosing one repository by caller count; workspace filtering still
  applies. Queue lookup no longer removes `test_`, `_queue` or `.fifo` from names:
  use the actual resource name or an indexed resource alias, because a test queue
  and its production counterpart, or a standard and a FIFO queue, are distinct
  resources. Queue, scheduled-method and GraphQL root lookups use the selected
  workspace's snapshots. Queue reads inferred from data-access records require the
  parser's explicit `queue:` entity marker, so ordinary table reads are not queue
  consumers. For GraphQL, use the indexed operation name or exact import alias,
  including case; filename fragments and removal of `Query`, `Mutation` or
  `Subscription` suffixes no longer establish execution roots. Unresolved
  narrative calls keep their call name without borrowing a source location from
  the first same-named function in another repository.

#### Parsing and trace behavior

- **Route-prefix aliases.** Trace no longer adds or removes `/api` or `/action` by
  default, and no longer strips `/vN` prefixes automatically. Exact routes,
  parameter syntax and indexed gateway routes still match without aliases. A
  deployment that deliberately exposes the same routes under a prefix declares it
  in `trace.yaml` ([Trace YAML](#trace-yaml)); version
  prefixes distinguish contracts unless the deployment rewrites them. Index audit
  HTTP classification and HTTP repository-edge collection use the same
  `http.context_path_prefixes` setting for the calling repository, so configure
  real aliases before comparing results with an older installation. Audit no
  longer inserts a slash into literal routes such as `/apiusers`, adds `/api` or
  `/action` implicitly, or drops leading path parameters; a leading tenant
  parameter is part of the route, not evidence of a disposable base URL.
- **Java nested declarations.** Java member types retain their enclosing scopes:
  `A.Builder` and `B.Builder` are distinct declarations, including on the same
  source line, and their method names carry the same scope. Top-level type names
  are unchanged. Local types include an enclosing-block byte position in their
  identity, so separate blocks can declare the same name without merging metadata.
  Replace the parser and all enrichment helpers together, then fully reindex
  affected Java repositories in each workspace
  (`tirion index -root <repos> -workspace <slug> -skip-unchanged=false`). Existing
  nested-type search and trace names change to the scoped form. Running only an
  enrichment helper against old simple-name snapshots can report a declaration
  mismatch and request a full reindex; it does not guess which old class should
  receive the new facts. Retained snapshots keep their old identities until that
  workspace is reindexed.
- **Java base-type lookup.** Inheritance resolution checks enclosing declarations,
  explicit imports, the declared package, qualified names and wildcard imports.
  A missing explicit import does not fall back to an unrelated same-named class.
  Different method signatures across an inheritance chain remain unresolved, and
  equal-signature overrides keep the nearest target; general Java overload
  selection is not implemented.
- **Java queue frameworks.** Application-specific queue injection names are no
  longer built into the parser, and the previous hardcoded names have no implicit
  fallback. Configure your framework in `patterns.yaml` (`java_sqs`, see
  [Patterns YAML](#patterns-yaml)) before reindexing; an
  omitted section disables custom-framework inference without affecting Java
  class, method, HTTP or database extraction. Reindex Java repositories to remove
  edges that were previously inferred only from a method-wide queue reference.
- **JavaScript queue wrappers.** The old built-in private method list and
  queue-name-prefix heuristic were removed. SDK method handling stays built in;
  existing custom deployments must supply their actual wrapper names under
  `javascript_queues` before reindexing.
  Existing indexed facts are replaced by reindexing, not by changing the MCP client
  configuration.
- **JavaScript and Java HTTP wrappers.** A helper's name or suffix alone no longer
  establishes its HTTP method. Declare wrappers under `http_clients`. Java source
  enrichment uses those patterns too, rather than recognizing a fixed list of
  application wrapper methods, so configure them on both the indexing and API hosts
  when those are separate deployments, and reindex.
- **GraphQL registration wrappers.** No registration wrappers are enabled by
  default; declare existing ones under `graphql_registrations` before reindexing.
  This does not disable GraphQL operation extraction.

## Troubleshooting

- `database is required: set DATABASE_URL or pass -db; .env files are not loaded automatically (see SETUP.md#configuration-reference)`:
  no database was selected. Export `DATABASE_URL` in the terminal that runs the command, pass `-db`, or set
  `TIRION_ENV_FILE` to a file containing `DATABASE_URL=...` (`tirion`, `mcp-intel`, and
  `mcp-guard` can read it; `parse`, `extract-*`, and `audit-index` need an exported
  variable). A `.env` file in the working directory is never read.
- `TIRION_ENV_FILE must be an absolute path` or `unsupported environment key on line N`:
  use an absolute path and only the keys listed in
  [TIRION_ENV_FILE](#tirion_env_file).
- Missing `parse` or `extract-*`: rebuild all commands with `bash scripts/build-local.sh`
  and keep them together beside `tirion`.
- `workspace "x" does not exist`: create it with `tirion workspace create x`, or correct
  the `-workspace` spelling.
- `flags must come before positional arguments`: move every flag ahead of the repos root.
- `Warning:` lines in the index summary: unreadable directories, symlink loops, or
  source outside the nested repositories of a grouping folder, which is not indexed.
  Give that source its own manifest or move it into a repository.
- CGO or compiler errors: install a native C toolchain; disabling CGO does not build the
  tree-sitter parsers.
- Database authentication errors: check the role, password mechanism, and environment of
  the terminal running the command.
- `pg_trgm` permission errors: enable the extension through the database administrator;
  do not make the application role a superuser as a workaround.
- HTTP 401 `UNAUTHORIZED`: the request has no token or the wrong one. Send
  `X-Tirion-Token` or `Authorization: Bearer`, and check that the client reads the same
  token file as the server (`TIRION_HOME`).
- `API token file must be a regular private file (chmod 600)`: restrict the token file to
  your user (mode 0600 on Unix, a user-only ACL on Windows).
- HTTP 403 `UNTRUSTED_HOST` or `ORIGIN_NOT_ALLOWED`: the `Host` or browser `Origin` is not
  allowed; see `TIRION_ALLOWED_HOSTS` and `TIRION_ALLOWED_ORIGINS` in
  [Server, network, and limits](#server-network-and-limits).
- Empty results: check the workspace and the indexing output. Unindexed repositories
  cannot contribute graph relationships.
- Verify preimage mismatch: the server's indexed checkout must contain the diff base,
  not your modified files. Use a separate clean baseline checkout or workspace and
  submit the diff explicitly; see
  [verification inputs](DOCS/server-api.md#inputs-and-preconditions).
- UI loads but API requests fail: confirm the API port matches Vite's proxy target. A
  production UI needs the reverse proxy's `/api` route; `npm run preview` alone does not
  configure a production API proxy.
- MCP tools report "Tirion API token unavailable": see
  [MCP troubleshooting](DOCS/mcp.md#troubleshooting).

Do not include credentials, private source, or full index dumps in public reports. Record
the command, version, and a minimal redistributable example instead.
