# Security

## Reporting

The responsible maintainer is [@sergiumoraru](https://github.com/sergiumoraru). Submit vulnerability
reports through [GitHub private vulnerability reporting](https://github.com/sergiumoraru/tirion/security/advisories/new).
This is the primary and preferred channel; reports there are visible only to the
maintainers. If GitHub does not offer private reporting for the repository, open an
issue addressed to @sergiumoraru containing only “Security report: please provide a private
contact channel.” Wait for a private channel before sending technical details.

Never put credentials, private source, personal data, index dumps, or exploit
details in a public issue. Include the affected version, deployment configuration,
a minimal synthetic reproduction, impact, and any proposed mitigation in the
private report. No response-time guarantee or long-term release support window is
promised; use the latest release and check its release notes for security changes.

## Private Contact

Private reports that are not vulnerabilities, such as Code of Conduct reports
(see [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)), use the same private channel. Open
the private vulnerability reporting form linked above and begin the title with
“Conduct report”. The form is not publicly visible, and its contents reach only
the maintainers. This remains the only private contact until a dedicated address
is listed here.

## Deployment Boundary

Every API request requires the service token. The server creates a private token
file under `~/.tirion` by default; explicit token and file settings are supported.
This is shared service authentication: possession of the token grants graph and
repository administration access. It does not provide separate per-user roles.

The listener defaults to `127.0.0.1`. Shared deployments must use an explicit Host
allowlist and HTTPS through an authenticated reverse proxy. Keep the raw app port
behind network access controls. Browser CORS settings do not replace authentication.

Tirion can read repository source, retain it in PostgreSQL, and perform Git/indexing
operations. Use a dedicated service account, database, and repository clones.
Treat database backups, source evidence, API tokens, and logs as sensitive data.
Only load an explicitly selected trusted environment file; never load configuration
from a repository supplied by someone else. MCP local file operations require
explicit allowed roots.

Release signatures authenticate artifacts, not API requests. See [SETUP.md](SETUP.md),
[configuration](SETUP.md#configuration-reference) for settings, and [operations](SETUP.md#running-tirion-for-a-team) for
deployment and upgrades.
