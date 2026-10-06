# Parser Fixture Corpus

This directory is kept in the repo for parser regression tests only. It is not runtime product data.

Third-party excerpts retain their upstream licenses; see
[public fixture attribution](public-ibmi/NOTICE.md). Original synthetic fixtures
use the project license. The two formerly imported, uncleared SQL/CL examples
were replaced with original invoice fixtures preserving their assertion categories.

Fixture groups:

- `core/`
  - stable non-IBM i fixtures that cover baseline parser behavior for TypeScript and Java
- `public-ibmi/`
  - locked excerpts from public IBM i / RPG repositories used to harden parser behavior against real source patterns
- `synthetic-ibmi/`
  - small custom IBM i fixtures used to cover targeted parser behaviors that are easier to express in focused examples

Representative fixtures:

- `core/resource_routes.ts` + `core/resource_routes.expected.json`
  - validates TypeScript endpoint extraction, import alias/namespace handling, and type-alias/interface extraction
- `core/resource_controller.java` + `core/resource_controller.expected.json`
  - validates Java annotation-based endpoint extraction, lambda extraction, modifiers/inheritance, and method signatures/generics
- `synthetic-ibmi/rpg_lock_demo.rpgle` + `synthetic-ibmi/rpg_lock_demo.expected.json`
  - validates free-form RPG parsing, `EXTPGM` alias normalization, subroutine extraction, and opcode-based file access
- `synthetic-ibmi/rpg_config_loader.rpgle` + `synthetic-ibmi/rpg_config_loader.expected.json`
  - validates fixed-form RPG parsing, `/COPY` imports, `CALLP(E)` handling, and opcode extenders like `READ(N)`
- `synthetic-ibmi/invoice_scan.sqlrpgle` + `synthetic-ibmi/invoice_scan.expected.json`
  - original SQLRPGLE fixture validating `/INCLUDE`, cursor SQL table extraction, and subroutine/service-procedure calls
- `public-ibmi/public_configr4.rpgle` + `public-ibmi/public_configr4.expected.json`
  - excerpted public RPGLE fixture from `httpapi`, validating fixed-form display-file imports, `CALLP(E) QCMDEXC`, and mixed file/display data access extraction
- `public-ibmi/public_qshonisrv.rpgle` + `public-ibmi/public_qshonisrv.expected.json`
  - excerpted public RPGLE fixture from `QshOni`, validating `Ctl-Opt NoMain`, exported procedures, and `EXTPGM` alias normalization without a synthetic `_MAIN`
- `synthetic-ibmi/invoice_batch.clle` + `synthetic-ibmi/invoice_batch.expected.json`
  - original CLLE fixture validating program entry naming and `CALL PGM(*LIBL/...)` extraction
- `public-ibmi/public_qshcallc.clle` + `public-ibmi/public_qshcallc.expected.json`
  - excerpted public CLLE fixture from `QshOni`, validating program-call extraction in a noisy operational CL command body
- `public-ibmi/public_package.clle` + `public-ibmi/public_package.expected.json`
  - excerpted public CLLE fixture from `httpapi`, validating command-heavy packaging scripts that still end in `CALL PGM(...)`
- `synthetic-ibmi/resource_list.dspf` + `synthetic-ibmi/resource_list.expected.json`
  - original minimal DSPF fixture validating four record formats and field extraction across a header, subfile, control and footer
- `public-ibmi/public_configs.dspf` + `public-ibmi/public_configs.expected.json`
  - excerpted public DSPF fixture from `httpapi`, validating multi-record installer screens that match the `CONFIGR4` RPG fixture

Any intentional parser behavior change must update both source fixture and expected output in the same PR.
