package graph

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Ownership and reference remapping are explicit. Adding a parser fact table
// requires updating this list; repository/global deployment data is never copied.
type snapshotFact struct {
	table, owner string
	references   map[string]string
}

func snapshotFacts() []snapshotFact {
	owned := func(column, table string) string {
		return fmt.Sprintf(`t.%s IN (SELECT old_id FROM snapshot_copy_ids WHERE relation='%s')`, column, table)
	}
	file := owned("file_id", "files")
	class := owned("class_id", "classes")
	function := owned("function_id", "functions")
	iface := owned("interface_id", "interfaces")
	entity := `EXISTS (SELECT 1 FROM snapshot_copy_ids m WHERE m.old_id=t.entity_id AND m.relation=CASE t.entity_type WHEN 'class' THEN 'classes' WHEN 'interface' THEN 'interfaces' WHEN 'method' THEN 'functions' WHEN 'field' THEN 'fields' WHEN 'parameter' THEN 'constructor_params' END)`
	return []snapshotFact{
		{"files", "t.snapshot_id=$1", nil},
		{"functions", file, map[string]string{"file_id": "files"}},
		{"classes", file, map[string]string{"file_id": "files"}},
		{"interfaces", file, map[string]string{"file_id": "files"}},
		{"fields", class + " OR " + iface, map[string]string{"class_id": "classes", "interface_id": "interfaces"}},
		{"constructor_params", class, map[string]string{"class_id": "classes"}},
		{"annotations", entity, nil}, {"type_parameters", entity, nil},
		{"function_calls", owned("caller_function_id", "functions"), map[string]string{"caller_function_id": "functions", "callee_function_id": "functions"}},
		{"endpoints", file + " OR " + owned("handler_function_id", "functions"), map[string]string{"file_id": "files", "handler_function_id": "functions"}},
		{"file_imports", file, map[string]string{"file_id": "files", "imports_file_id": "files"}},
		{"implementations", class, map[string]string{"class_id": "classes", "interface_id": "interfaces"}},
		{"method_signatures", function, map[string]string{"function_id": "functions", "overrides_method_id": "functions"}},
		{"http_interface_methods", iface, map[string]string{"interface_id": "interfaces", "function_id": "functions"}},
		{"bean_definitions", owned("config_class_id", "classes"), map[string]string{"config_class_id": "classes", "method_id": "functions"}},
		{"event_listeners", class, map[string]string{"class_id": "classes", "method_id": "functions"}},
		{"scheduled_methods", class, map[string]string{"class_id": "classes", "method_id": "functions"}},
		{"jpa_entities", class, map[string]string{"class_id": "classes"}},
		{"jpa_relationships", owned("source_class_id", "classes"), map[string]string{"source_class_id": "classes", "target_class_id": "classes"}},
		{"synthetic_methods", class, map[string]string{"class_id": "classes"}},
		{"enum_constants", class, map[string]string{"class_id": "classes"}},
		{"lambda_expressions", file, map[string]string{"file_id": "files"}},
		{"type_aliases", file, map[string]string{"file_id": "files"}},
		{"hook_calls", file, map[string]string{"file_id": "files"}},
		{"vue_component_contracts", file, map[string]string{"file_id": "files"}},
		{"pinia_stores", file, map[string]string{"file_id": "files"}},
		{"graphql_operations", file, map[string]string{"file_id": "files"}},
		{"graphql_operation_usages", file, map[string]string{"file_id": "files"}},
		{"graphql_backend_entrypoints", file, map[string]string{"file_id": "files"}},
		{"graphql_backend_controller_links", owned("entrypoint_id", "graphql_backend_entrypoints"), map[string]string{"entrypoint_id": "graphql_backend_entrypoints", "file_id": "files"}},
		{"graphql_operation_resolvers", file, map[string]string{"file_id": "files"}},
		{"graphql_operation_permissions", file, map[string]string{"file_id": "files"}},
		{"graphql_usage_operation_links", owned("usage_id", "graphql_operation_usages"), map[string]string{"usage_id": "graphql_operation_usages", "operation_id": "graphql_operations"}},
		{"azure_host_configs", file, map[string]string{"file_id": "files"}},
		{"azure_function_triggers", file, map[string]string{"file_id": "files"}},
		{"gateway_routes", file, map[string]string{"file_id": "files"}},
		{"repository_entities", "t.snapshot_id=$1", map[string]string{"file_id": "files"}},
		{"ibmi_exports", "t.snapshot_id=$1", map[string]string{"file_id": "files", "function_id": "functions"}},
		{"ibmi_bindings", "t.snapshot_id=$1", map[string]string{"file_id": "files"}},
		{"http_client_calls", "t.snapshot_id=$1", nil},
		{"sqs_producers", "t.snapshot_id=$1", nil}, {"sqs_consumers", "t.snapshot_id=$1", nil},
		{"resource_aliases", "t.snapshot_id=$1", map[string]string{"file_id": "files"}}, {"data_accesses", "t.snapshot_id=$1", nil},
	}
}

// CopySnapshot copies source-backed facts, retaining the old snapshot. Materialized
// Trace rows are rebuilt by publication after every target is known.
func (s *Storage) CopySnapshot(ctx context.Context, from, to int64) error {
	return s.copySnapshot(ctx, from, to, false)
}

// CopySnapshotForResolution copies facts for immediate full resolution. Inferred
// call targets must be reconsidered there, so clear them during insertion instead
// of writing and indexing them only to reset them in publication's next step.
// Partial resolution needs the original cross-repository targets and uses CopySnapshot.
func (s *Storage) CopySnapshotForResolution(ctx context.Context, from, to int64) error {
	return s.copySnapshot(ctx, from, to, true)
}

func (s *Storage) copySnapshot(ctx context.Context, from, to int64, resetInferences bool) error {
	tx, err := s.Executor().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE IF NOT EXISTS snapshot_copy_ids(relation text, old_id bigint, new_id bigint, PRIMARY KEY(relation,old_id)) ON COMMIT DROP; TRUNCATE snapshot_copy_ids`); err != nil {
		return err
	}
	// Row locks serialize copying against standalone enrichment and replacement.
	if _, err := tx.Exec(ctx, `SELECT id FROM files WHERE snapshot_id=$1 ORDER BY id FOR SHARE`, from); err != nil {
		return err
	}
	facts := snapshotFacts()
	pending := []string{"pending_calls_edges", "pending_contains_edges", "pending_imports_edges"}
	tables := append([]string{}, pending...)
	for _, fact := range facts {
		tables = append(tables, fact.table)
	}
	// Discover scalar columns once per copy, including generated-column filtering.
	// Ownership and reference remapping remain explicitly declared above.
	rows, err := tx.Query(ctx, `SELECT relation.name,a.attname FROM unnest($1::text[]) relation(name)
	 JOIN pg_attribute a ON a.attrelid=relation.name::regclass
	 WHERE a.attnum>0 AND NOT a.attisdropped AND a.attgenerated=''
	 ORDER BY relation.name,a.attnum`, tables)
	if err != nil {
		return err
	}
	columnsByTable := make(map[string][]string, len(tables))
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			rows.Close()
			return err
		}
		columnsByTable[table] = append(columnsByTable[table], column)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, fact := range facts {
		columns := append([]string{}, columnsByTable[fact.table]...)
		exists := make(map[string]bool, len(columns))
		for _, column := range columns {
			exists[column] = true
		}
		table := pgx.Identifier{fact.table}.Sanitize()
		query := fmt.Sprintf(`INSERT INTO snapshot_copy_ids SELECT '%s',t.id,nextval(pg_get_serial_sequence('%s','id')) FROM %s t WHERE (%s)`, fact.table, fact.table, table, fact.owner)
		// All ownership expressions accept $1, even when file ownership supplies the filter.
		query += ` AND $1::bigint IS NOT NULL`
		if _, err := tx.Exec(ctx, query, from); err != nil {
			return fmt.Errorf("copy %s identities: %w", fact.table, err)
		}
		// Copy typed values directly, avoiding JSON serialization/deserialization
		// of every scalar field and full source body.
		replacements := map[string]string{"id": "m.new_id"}
		if exists["snapshot_id"] {
			replacements["snapshot_id"] = "$2::bigint"
		}
		for column, relation := range fact.references {
			if !exists[column] {
				return fmt.Errorf("snapshot copy: missing %s.%s", fact.table, column)
			}
			quoted := pgx.Identifier{column}.Sanitize()
			replacements[column] = fmt.Sprintf(`COALESCE((SELECT new_id FROM snapshot_copy_ids WHERE relation='%s' AND old_id=t.%s),t.%s)`, relation, quoted, quoted)
		}
		if fact.table == "annotations" || fact.table == "type_parameters" {
			replacements["entity_id"] = `(SELECT new_id FROM snapshot_copy_ids WHERE old_id=t.entity_id AND relation=CASE t.entity_type WHEN 'class' THEN 'classes' WHEN 'interface' THEN 'interfaces' WHEN 'method' THEN 'functions' WHEN 'field' THEN 'fields' WHEN 'parameter' THEN 'constructor_params' END)`
		}
		if fact.table == "function_calls" && resetInferences {
			// Match publication's inference reset and the UPDATE trigger's reason.
			// Callback evidence and parser bindings are retained; publication still
			// checks them for stale targets after all selected snapshots are known.
			reset := `NOT t.is_callback_argument AND COALESCE(t.callee_resolution_source,'parser') NOT IN ('parser','callback_argument')`
			for _, column := range []string{"callee_function_id", "callee_resolution_source", "callee_resolution_confidence"} {
				value := replacements[column]
				if value == "" {
					value = "t." + column
				}
				replacements[column] = fmt.Sprintf(`CASE WHEN %s THEN NULL ELSE %s END`, reset, value)
			}
			replacements["unresolved_reason"] = fmt.Sprintf(`CASE WHEN %s AND t.callee_function_id IS NOT NULL THEN COALESCE(NULLIF(t.unresolved_reason,''),'callee_removed') ELSE t.unresolved_reason END`, reset)
		}
		projections := make([]string, len(columns))
		for i, column := range columns {
			columns[i] = pgx.Identifier{column}.Sanitize()
			if replacement, ok := replacements[column]; ok {
				projections[i] = replacement
			} else {
				projections[i] = "t." + columns[i]
			}
		}
		query = fmt.Sprintf(`INSERT INTO %s (%s) SELECT %s FROM %s t JOIN snapshot_copy_ids m ON m.relation='%s' AND m.old_id=t.id WHERE $1::bigint IS NOT NULL AND $2::bigint IS NOT NULL`, table, strings.Join(columns, ","), strings.Join(projections, ","), table, fact.table)
		if _, err := tx.Exec(ctx, query, from, to); err != nil {
			return fmt.Errorf("copy %s facts: %w", fact.table, err)
		}
	}
	for _, table := range pending {
		// Textual identities need only a snapshot remap, not a JSON round trip.
		columns := columnsByTable[table]
		names, values := make([]string, len(columns)), make([]string, len(columns))
		for i, column := range columns {
			names[i] = pgx.Identifier{column}.Sanitize()
			values[i] = "t." + names[i]
			if column == "snapshot_id" {
				values[i] = "$2::bigint"
			}
		}
		query := fmt.Sprintf(`INSERT INTO %s (%s) SELECT %s FROM %s t WHERE snapshot_id=$1`,
			pgx.Identifier{table}.Sanitize(), strings.Join(names, ","), strings.Join(values, ","), pgx.Identifier{table}.Sanitize())
		if _, err := tx.Exec(ctx, query, from, to); err != nil {
			return fmt.Errorf("copy %s: %w", table, err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE repo_snapshots dest SET input_manifest=src.input_manifest,include_manifest=src.include_manifest,ignored_manifest=src.ignored_manifest,source_clean=src.source_clean,parser_version=src.parser_version,pipeline_fingerprint=src.pipeline_fingerprint,indexed_at=src.indexed_at FROM repo_snapshots src WHERE dest.id=$2 AND src.id=$1`, from, to); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RebindSnapshotReferences moves optional declaration references away from archived
// target IDs. Only a unique declaration at the same source identity is accepted;
// unresolved relationships retain their textual import/interface/entity evidence.
func (s *Storage) RebindSnapshotReferences(ctx context.Context, snapshots, callers []int64) error {
	queries := []string{
		`UPDATE file_imports link SET imports_file_id=(SELECT min(n.id) FROM files n WHERE n.repo_id=old.repo_id AND n.path=old.path AND n.snapshot_id=ANY($1) HAVING count(*)=1)
 FROM files owner,files old WHERE owner.id=link.file_id AND owner.snapshot_id=ANY($2) AND old.id=link.imports_file_id AND (old.snapshot_id IS NULL OR NOT(old.snapshot_id=ANY($1)))`,
		`UPDATE implementations link SET interface_id=(SELECT min(n.id) FROM interfaces n JOIN files nf ON nf.id=n.file_id WHERE nf.repo_id=oldf.repo_id AND nf.path=oldf.path AND nf.snapshot_id=ANY($1) AND n.name=old.name HAVING count(*)=1)
 FROM classes owner JOIN files ofile ON ofile.id=owner.file_id,interfaces old JOIN files oldf ON oldf.id=old.file_id
 WHERE owner.id=link.class_id AND ofile.snapshot_id=ANY($2) AND old.id=link.interface_id AND (oldf.snapshot_id IS NULL OR NOT(oldf.snapshot_id=ANY($1)))`,
		`UPDATE method_signatures link SET overrides_method_id=(SELECT min(n.id) FROM functions n JOIN files nf ON nf.id=n.file_id WHERE nf.repo_id=oldf.repo_id AND nf.path=oldf.path AND nf.snapshot_id=ANY($1) AND n.name=old.name AND n.start_line=old.start_line AND n.end_line=old.end_line HAVING count(*)=1)
 FROM functions owner JOIN files ofile ON ofile.id=owner.file_id,functions old JOIN files oldf ON oldf.id=old.file_id
 WHERE owner.id=link.function_id AND ofile.snapshot_id=ANY($2) AND old.id=link.overrides_method_id AND (oldf.snapshot_id IS NULL OR NOT(oldf.snapshot_id=ANY($1)))`,
		`UPDATE jpa_relationships link SET target_class_id=(SELECT min(n.id) FROM classes n JOIN files nf ON nf.id=n.file_id WHERE nf.repo_id=oldf.repo_id AND nf.path=oldf.path AND nf.snapshot_id=ANY($1) AND n.name=old.name AND n.start_line=old.start_line AND n.end_line=old.end_line HAVING count(*)=1)
 FROM classes owner JOIN files ofile ON ofile.id=owner.file_id,classes old JOIN files oldf ON oldf.id=old.file_id
 WHERE owner.id=link.source_class_id AND ofile.snapshot_id=ANY($2) AND old.id=link.target_class_id AND (oldf.snapshot_id IS NULL OR NOT(oldf.snapshot_id=ANY($1)))`,
	}
	for _, query := range queries {
		if _, err := s.Executor().Exec(ctx, query, snapshots, callers); err != nil {
			return err
		}
	}
	return nil
}

// AffectedSnapshots includes inferred/name-only callers that a new target can
// change, then follows incoming numeric references to a fixed point. Copying that
// closure preserves already selected snapshot sets without copying unrelated repos.
func (s *Storage) AffectedSnapshots(ctx context.Context, baseline, changed []int64, all bool) (map[int64]bool, error) {
	query := fmt.Sprintf(`WITH RECURSIVE
 target_names AS (
   SELECT lower(fn.name) AS name FROM functions fn JOIN files f ON f.id=fn.file_id WHERE f.snapshot_id=ANY($2)
   UNION SELECT lower(fn.simple_name) FROM functions fn JOIN files f ON f.id=fn.file_id WHERE f.snapshot_id=ANY($2)
   UNION SELECT lower(export_name) FROM ibmi_exports WHERE snapshot_id=ANY($2)
 ), dependencies AS (
   SELECT cf.snapshot_id AS source,tf.snapshot_id AS target FROM function_calls fc JOIN functions c ON c.id=fc.caller_function_id JOIN files cf ON cf.id=c.file_id JOIN functions t ON t.id=fc.callee_function_id JOIN files tf ON tf.id=t.file_id WHERE cf.snapshot_id=ANY($1)
   UNION SELECT cf.snapshot_id,tf.snapshot_id FROM file_imports l JOIN files cf ON cf.id=l.file_id JOIN files tf ON tf.id=l.imports_file_id WHERE cf.snapshot_id=ANY($1)
   UNION SELECT cf.snapshot_id,tf.snapshot_id FROM implementations l JOIN classes c ON c.id=l.class_id JOIN files cf ON cf.id=c.file_id JOIN interfaces t ON t.id=l.interface_id JOIN files tf ON tf.id=t.file_id WHERE cf.snapshot_id=ANY($1)
   UNION SELECT cf.snapshot_id,tf.snapshot_id FROM method_signatures l JOIN functions c ON c.id=l.function_id JOIN files cf ON cf.id=c.file_id JOIN functions t ON t.id=l.overrides_method_id JOIN files tf ON tf.id=t.file_id WHERE cf.snapshot_id=ANY($1)
   UNION SELECT cf.snapshot_id,tf.snapshot_id FROM jpa_relationships l JOIN classes c ON c.id=l.source_class_id JOIN files cf ON cf.id=c.file_id JOIN classes t ON t.id=l.target_class_id JOIN files tf ON tf.id=t.file_id WHERE cf.snapshot_id=ANY($1)
 ), seeds AS (
   SELECT unnest($1::bigint[]) AS id WHERE $3::boolean
   UNION SELECT id FROM unnest($1::bigint[]) id WHERE id=ANY($2)
   UNION SELECT f.snapshot_id FROM function_calls fc JOIN functions fn ON fn.id=fc.caller_function_id JOIN files f ON f.id=fn.file_id
     WHERE f.snapshot_id=ANY($1) AND lower(fc.callee_name) IN (SELECT name FROM target_names)
   UNION SELECT f.snapshot_id FROM files f WHERE f.snapshot_id=ANY($1) AND %s
     AND EXISTS(SELECT 1 FROM files changed WHERE changed.snapshot_id=ANY($2) AND %s)
 ), affected(id) AS (
   SELECT id FROM seeds
   UNION SELECT d.source FROM dependencies d JOIN affected a ON a.id=d.target WHERE d.source=ANY($1)
 ) SELECT DISTINCT id FROM affected`, ibmiLanguageSQL("f"), ibmiLanguageSQL("changed"))
	rows, err := s.Executor().Query(ctx, query, baseline, changed, all)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = true
	}
	return result, rows.Err()
}
