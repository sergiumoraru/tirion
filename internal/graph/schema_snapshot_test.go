package graph

import (
	"regexp"
	"strings"
	"testing"
)

func TestSchemaSnapshotsHighBlastFactTables(t *testing.T) {
	t.Parallel()

	for _, table := range []string{
		"files",
		"trace_call_edges",
		"pending_contains_edges",
		"pending_calls_edges",
		"pending_imports_edges",
		"http_client_calls",
		"sqs_producers",
		"sqs_consumers",
		"trace_interface_impls",
		"repository_entities",
		"data_accesses",
		"ibmi_exports",
		"ibmi_bindings",
	} {
		tableSQL := schemaTableDefinition(table)
		if tableSQL == "" {
			t.Fatalf("schema table %q not found", table)
		}
		if !strings.Contains(tableSQL, "snapshot_id") {
			t.Fatalf("schema table %q is missing snapshot_id", table)
		}
	}
}

func TestSchemaDropsRepoOnlyFactUniqueness(t *testing.T) {
	t.Parallel()

	for _, constraint := range []string{
		"pending_contains_edges_file_id_entity_id_key",
		"pending_calls_edges_caller_id_callee_name_line_number_key",
		"pending_imports_edges_file_id_import_path_key",
		"http_client_calls_caller_id_url_pattern_line_number_key",
		"sqs_producers_caller_id_queue_name_line_number_key",
		"sqs_consumers_consumer_id_queue_name_key",
		"repository_entities_repo_id_file_id_repository_name_entity_name_key",
		"data_accesses_repo_id_caller_id_entity_name_access_line_number_key",
	} {
		if !strings.Contains(Schema, "DROP CONSTRAINT IF EXISTS "+constraint) {
			t.Fatalf("schema does not drop legacy repo-only constraint %q", constraint)
		}
	}
	for _, index := range []string{
		"idx_pending_calls_snapshot_unique",
		"idx_http_client_calls_snapshot_unique",
		"idx_sqs_producers_snapshot_unique",
		"idx_sqs_consumers_snapshot_unique",
		"idx_repository_entities_snapshot_unique",
		"idx_data_accesses_snapshot_unique",
		"idx_ibmi_exports_snapshot_unique",
		"idx_ibmi_bindings_snapshot_unique",
	} {
		if !strings.Contains(Schema, "CREATE UNIQUE INDEX IF NOT EXISTS "+index) {
			t.Fatalf("schema does not create snapshot-scoped unique index %q", index)
		}
	}
}

func TestSchemaKeepsHookCallsIdempotent(t *testing.T) {
	t.Parallel()

	index := "CREATE UNIQUE INDEX IF NOT EXISTS idx_hook_calls_unique"
	if !strings.Contains(Schema, index) {
		t.Fatalf("schema does not create hook-call uniqueness index %q", index)
	}
	for _, want := range []string{
		"ON hook_calls(file_id, hook_name",
		"COALESCE(function_name, '')",
		"COALESCE(line_number, 0)",
	} {
		if !strings.Contains(Schema, want) {
			t.Fatalf("hook-call uniqueness index missing %q", want)
		}
	}
}

func TestSnapshotMigrationManifestCoversSchemaTables(t *testing.T) {
	t.Parallel()

	manifest := snapshotManifestByTable()
	for _, table := range schemaTableNames() {
		entry, ok := manifest[table]
		if !ok {
			t.Fatalf("schema table %q has no snapshot migration manifest entry", table)
		}
		if strings.TrimSpace(entry.Owner) == "" || strings.TrimSpace(entry.Reason) == "" || strings.TrimSpace(entry.QueryFilter) == "" {
			t.Fatalf("manifest entry for %q must document owner, reason, and query filter: %#v", table, entry)
		}
	}
}

func TestSnapshotMigrationManifestScopedTablesHaveSchemaSupport(t *testing.T) {
	t.Parallel()

	for _, entry := range SnapshotMigrationManifest() {
		if entry.Strategy != SnapshotStrategyScoped {
			continue
		}
		if entry.Table == "repo_snapshots" {
			continue
		}
		tableSQL := schemaTableDefinition(entry.Table)
		if tableSQL == "" {
			t.Fatalf("manifest table %q not found in schema", entry.Table)
		}
		if !strings.Contains(tableSQL, "snapshot_id") {
			t.Fatalf("manifest marks %q snapshot-scoped but schema has no snapshot_id", entry.Table)
		}
	}
}

func TestSnapshotMigrationManifestNoDuplicateTables(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, entry := range SnapshotMigrationManifest() {
		if strings.TrimSpace(entry.Table) == "" {
			t.Fatal("manifest contains an entry with empty table name")
		}
		if seen[entry.Table] {
			t.Fatalf("manifest contains duplicate table %q", entry.Table)
		}
		seen[entry.Table] = true
	}
}

func schemaTableDefinition(table string) string {
	marker := "CREATE TABLE IF NOT EXISTS " + table + " ("
	start := strings.Index(Schema, marker)
	if start < 0 {
		return ""
	}
	rest := Schema[start:]
	end := strings.Index(rest, ");")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

func schemaTableNames() []string {
	re := regexp.MustCompile(`CREATE TABLE IF NOT EXISTS\s+([a-zA-Z0-9_]+)\s*\(`)
	matches := re.FindAllStringSubmatch(Schema, -1)
	out := make([]string, 0, len(matches))
	seen := map[string]bool{}
	for _, match := range matches {
		if len(match) < 2 || seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		out = append(out, match[1])
	}
	return out
}

func snapshotManifestByTable() map[string]SnapshotMigrationEntry {
	out := map[string]SnapshotMigrationEntry{}
	for _, entry := range SnapshotMigrationManifest() {
		out[entry.Table] = entry
	}
	return out
}
