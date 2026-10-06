# Synthetic RPG Estate

Synthetic IBM i / RPG estate used to validate cross-codebase navigation with realistic linkage patterns.

## Source Walkthrough

This bundled sample requires no external repositories.
It is source for Tirion to index, not an IBM i application deployment. No IBM i
compiler or host is required to parse it.
The sample source directories are in the source checkout; binary release bundles
include this guide but do not include the sample source corpus.

After following [source setup](../../SETUP.md), use a separate local demo database and
set `DATABASE_URL` to it. From the Tirion source root:

```bash
./bin/tirion index -root ./testdata/rpg-estate -workspace default-main -skip-unchanged=false
./bin/tirion serve -port 8080
```

Start the UI as described in the setup guide, or query the running API via CLI:

```bash
./bin/tirion search -workspace default-main CBIBILLP
./bin/tirion trace -workspace default-main -depth 6 CBIBILLP
```

The source relationships to investigate are:

| Source | Connection | Destination |
| --- | --- | --- |
| Billing `CBIBILLDRV.CLLE` | `CALL PGM(CBIBILLP)` | Billing program `CBIBILLP` |
| Billing `CBIBILLP.RPGLE` | `UpdateLedger`, declared `EXTPGM('CBIARUPD')` | AR ledger program `CBIARUPD` |
| Billing `CBIBILLP.RPGLE` | `ComputeTax`, declared in the shared copy member | Shared procedure `CBITAXSRV` |
| Billing and AR ledger sources | `AuditEvent` / `AuditLedger` declarations | Shared procedure `CBIAUDLOG` |
| Billing writes and AR ledger reads | `INVHDR` file access | Shared data boundary |

This walkthrough was exercised on a fresh PostgreSQL database: five repos, 19 files, and 12 functions indexed. API and MCP Trace
returned the billing-to-ledger, tax, and audit calls; Impact returned the shared
INVHDR read/write boundary. This validates the listed sample relationships, not
every supported language or framework.

## Parser Diagnostics

The optional `scripts/index-rpg-custom-estate.sh` and
`scripts/index-rpg-member-export.sh` run `bin/parse` directly for parser-level
inspection. They accept a database URL as their first argument, otherwise use
`DATABASE_URL`, and fail when neither is set; there is no default database.
`PARSE_BIN` can point to a different native parser build. Use `tirion index` above for the complete
workspace indexing workflow rather than these lower-level diagnostics.

The optional public-corpus scripts require separately obtained upstream repos
under an explicit `RPG_PUBLIC_ROOT` (or their second argument); they do not clone
anything or scan adjacent directories by default. `rpg-hardening-check.sh` builds
the parser, indexes that corpus and this sample, and prints diagnostic SQL. It is
not an automated pass/fail gate and must use a disposable database.

## Sample Layout

Repos in dependency order:

1. `cbi-rpg-shared-services`
2. `cbi-rpg-ar-ledger`
3. `cbi-rpg-billing-core`
4. `cbi-rpg-order-entry`

Additional validation corpus:

5. `cbi-ibmi-member-export`
   IBM i source-member export using `QRPGLESRC`, `QCLSRC`, `QDSPSRC`, and `QDDSSRC`

Linkage patterns modeled here:

- `CLLE` orchestration into RPG programs and shared procedures
- `EXTPROC` service-style procedure calls
- `EXTPGM` program-style calls
- shared `/COPY` contracts
- shared file/table access across repos
- DDS artifacts for physical files and display files

Key symbols:

- `CBIAUDLOG` -> shared audit procedure
- `CBITAXSRV` -> shared tax procedure
- `CBIARUPD` -> AR ledger update program
- `CBIBILLDRV` -> CLLE billing driver entrypoint
- `CBIBILLP` -> billing post program
- `SubmitOrder` -> order entry workflow
- `ORDERDSP`, `ORDHDR`, `INVHDR`, `ARLEDGER`, `AUDITLOG` -> DDS artifacts used by the programs
