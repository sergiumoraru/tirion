# Cross-Repository HTTP Fixture

Original synthetic source under the project Apache-2.0 license. These files are
index inputs, not a deployable application; no private repositories or services
are needed.

- storefront: loadCatalog calls GET /api/catalog/items.
- catalog-api: CatalogController.listItems handles that route and calls loadItems.
- Removing the consumed handler or its region parameter should produce a
  contract finding with the storefront call site as evidence.
- A body-only change should retain exposure information without inventing an
  incompatible HTTP contract.

Use a disposable database and copies of these directories initialized as separate
Git repositories when checking diff preimages. Never run reset/cleanup against an
existing indexed estate.

After [source setup](../../SETUP.md), export `DATABASE_URL` for a new disposable
database. From the Tirion source root, prepare clean Git baselines and index them:

```bash
fixture_root="$(mktemp -d)"
for repo in catalog-api storefront; do
  cp -R "testdata/estate-fixtures/$repo" "$fixture_root/$repo"
  git -C "$fixture_root/$repo" init -q
  git -C "$fixture_root/$repo" add .
  git -C "$fixture_root/$repo" -c user.name=Fixture \
    -c user.email=fixture@example.invalid commit -qm 'Fixture baseline'
done
./bin/tirion index -root "$fixture_root" -workspace default-main -skip-unchanged=false
printf 'Fixture root for the second terminal: %s\n' "$fixture_root"
./bin/tirion serve -port 18080
```

Keep that terminal running. In another terminal in the source root, use the
actual temporary path created above (the shell variable is not shared):

```bash
BASE_URL=http://localhost:18080 WORKSPACE_ID=default-main \
  node scripts/estate-fixture-check.mjs /path/to/clean/catalog-api
```

Use another free port if 18080 is occupied, changing `BASE_URL` to match. After
the check, stop only this API process with Ctrl-C. Keep the source copies while
querying the fixture index; its snapshot/preimage checks need those files.

Every API route needs the service token. The check uses `TIRION_API_TOKEN`, else
the file named by `TIRION_API_TOKEN_FILE`, else the token `tirion serve` created at
`~/.tirion/api-token` (or under `TIRION_HOME`; the local file is read only for a
loopback `BASE_URL`). It passes the token to curl through a private temporary
config file, not the command line, and warns if no token is found.

Set HTTP_CLIENT to an approved curl-compatible wrapper when required by your
environment; it must accept curl's `-K` config-file option. The check does not edit
repositories, build code, or reset databases.
It verifies removal, type-change, and body-only cases with cross-repo consumer
evidence and rejects stale or mismatched fixture baselines.
