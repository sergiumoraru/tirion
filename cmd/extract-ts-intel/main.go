package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sergiumoraru/tirion/internal/config"
	"github.com/sergiumoraru/tirion/internal/indexer"
	"github.com/sergiumoraru/tirion/internal/parser"
	"github.com/sergiumoraru/tirion/internal/sourceindex"
)

func main() {

	defaultDB := os.Getenv("DATABASE_URL")

	dbURL := flag.String("db", "", "PostgreSQL connection string (required; defaults to DATABASE_URL)")
	workspaceSlug := flag.String("workspace", "default-main", "Tirion workspace slug to scope indexed files")
	repoFilter := flag.String("repo", "", "Optional repository name to limit extraction")
	verbose := flag.Bool("v", false, "Verbose output")
	dryRun := flag.Bool("dry-run", false, "Don't insert, just show what would be extracted")
	candidateID := flag.Int64("candidate", 0, "Internal unpublished snapshot ID")
	flag.Parse()
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "extract-ts-intel: unexpected argument(s) %q; this command takes only flags\n", flag.Args())
		os.Exit(2)
	}
	if err := config.GetEffectivePatterns().Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *dbURL == "" {
		*dbURL = defaultDB
	}
	if *candidateID == 0 && !*dryRun {
		if err := indexer.RunPostParseExtractors(indexer.PostParseOptions{DBURL: *dbURL, Workspace: *workspaceSlug, Repo: *repoFilter, RawList: "ts-intel", Verbose: *verbose}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	fmt.Println("Extracting TypeScript/JavaScript code intelligence from indexed files...")
	startTime := time.Now()

	// Connect to database
	if *dbURL == "" {
		log.Fatal("database is required: set DATABASE_URL or pass -db; .env files are not loaded automatically (see SETUP.md#configuration-reference)")
	}

	pool, err := pgxpool.New(context.Background(), *dbURL)
	if err != nil {
		fmt.Printf("Error connecting to database: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Ensure new tables exist
	if !*dryRun && os.Getenv("CODEBASE_SKIP_SCHEMA_INIT") != "1" {
		if err := createTablesIfNeeded(pool); err != nil {
			fmt.Printf("Error creating tables: %v\n", err)
			os.Exit(1)
		}
	}

	// Query JS/TS files for the active snapshots in this workspace.
	rows, err := pool.Query(context.Background(), `
		SELECT f.id, f.path, r.name, COALESCE(NULLIF(rs.source_path,''), NULLIF(wr.worktree_path, ''), r.path) as repo_path, COALESCE(f.hash, ''), rs.sha
		FROM files f
		JOIN repo_snapshots rs ON rs.id = f.snapshot_id
		JOIN repositories r ON f.repo_id = r.id
		JOIN workspaces w ON w.slug = $1
 LEFT JOIN workspace_repos wr ON wr.repo_name=r.name AND wr.workspace_id=w.id
		WHERE f.language IN ('javascript', 'typescript', 'tsx', 'jsx', 'vue')
		  AND w.slug = $1 AND (($3::bigint=0 AND wr.active_snapshot_id=f.snapshot_id) OR ($3>0 AND rs.id=$3 AND rs.workspace_id=w.id AND rs.status IN ('building','parsed','enriched')))
		  AND ($2 = '' OR r.name = $2)
		ORDER BY r.name, f.path
	`, strings.TrimSpace(*workspaceSlug), strings.TrimSpace(*repoFilter), *candidateID)
	if err != nil {
		fmt.Printf("Error querying files: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	type fileInfo struct {
		hash     string
		commit   string
		id       int64
		path     string
		repoName string
		repoPath string
	}

	var files []fileInfo
	for rows.Next() {
		var f fileInfo
		if err := rows.Scan(&f.id, &f.path, &f.repoName, &f.repoPath, &f.hash, &f.commit); err != nil {
			log.Fatalf("Read indexed file: %v", err)
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("Read indexed files: %v", err)
	}

	fmt.Printf("Found %d JS/TS files to process\n", len(files))

	// Initialize parser
	jsParser := parser.NewJavaScriptParser().ForEnrichment()

	// Statistics
	var filesFailed int
	var filesProcessed, interfacesFound, typeAliasesFound, fieldsFound int
	var annotationsFound, hookCallsFound, vueContractsFound, piniaStoresFound, enumsFound int

	var readDuration, parseDuration, persistDuration time.Duration
	for _, f := range files {
		// Construct full path
		fullPath := filepath.Join(f.repoPath, f.path)

		// Read file content
		readStart := time.Now()
		content, err := sourceindex.Read(context.Background(), f.repoPath, f.path, f.hash, f.commit)
		readDuration += time.Since(readStart)
		if err != nil {
			log.Fatalf("Read %s: %v", fullPath, err)
		}
		content, _ = sourceindex.NormalizeText(content)
		if parser.LooksMinifiedOrVendor(fullPath, content) {
			continue
		}

		// Parse the file
		parseStart := time.Now()
		result := jsParser.ParseFile(fullPath, content)
		parseDuration += time.Since(parseStart)
		if d := result.ParseDiagnostics; d.Failed() {
			if d.Systemic() {
				log.Fatalf("Parse %s: %s: %s", fullPath, d.FailureKind, d.Message)
			}
			if !d.Usable() {
				filesFailed++
				log.Printf("Skipping %s after parse failure [%s]; its TypeScript facts are not refreshed: %s", fullPath, d.FailureKind, d.Message)
				continue
			}
			log.Printf("Partial syntax tree for %s; enrichment may be incomplete", fullPath)
		}
		filesProcessed++

		// Count what we found
		interfacesFound += len(result.Interfaces)
		typeAliasesFound += len(result.TypeAliases)
		hookCallsFound += len(result.HookCalls)
		vueContractsFound += len(result.VueComponentContracts)
		piniaStoresFound += len(result.PiniaStores)

		for _, cls := range result.Classes {
			fieldsFound += len(cls.Fields)
			annotationsFound += len(cls.Annotations)
			if cls.IsEnum {
				enumsFound++
			}
			for _, method := range cls.Methods {
				annotationsFound += len(method.Annotations)
			}
			for _, field := range cls.Fields {
				annotationsFound += len(field.Annotations)
			}
		}

		if *verbose && (len(result.Interfaces) > 0 || len(result.TypeAliases) > 0 || len(result.HookCalls) > 0 || len(result.VueComponentContracts) > 0 || len(result.PiniaStores) > 0) {
			fmt.Printf("  %s/%s: %d interfaces, %d type aliases, %d hooks, %d vue contracts, %d pinia stores\n",
				f.repoName, f.path, len(result.Interfaces), len(result.TypeAliases), len(result.HookCalls), len(result.VueComponentContracts), len(result.PiniaStores))
		}

		if !*dryRun {
			persistStart := time.Now()
			if err := refreshTypeScriptFileFacts(pool, f.id, result, f.hash); err != nil {
				log.Fatalf("Refresh %s/%s: %v", f.repoName, f.path, err)
			}
			persistDuration += time.Since(persistStart)
		}
	}
	fmt.Printf("Enrichment phases: read=%.2fs parse=%.2fs persist=%.2fs\n", readDuration.Seconds(), parseDuration.Seconds(), persistDuration.Seconds())

	elapsed := time.Since(startTime)
	fmt.Printf("\n=== TypeScript/JavaScript Intelligence Extraction Complete ===\n")
	fmt.Printf("Files processed: %d\n", filesProcessed)
	fmt.Printf("Files skipped after parse failure (not refreshed): %d\n", filesFailed)
	fmt.Printf("Interfaces found: %d\n", interfacesFound)
	fmt.Printf("Type aliases found: %d\n", typeAliasesFound)
	fmt.Printf("Enums found: %d\n", enumsFound)
	fmt.Printf("Fields found: %d\n", fieldsFound)
	fmt.Printf("Decorators/annotations found: %d\n", annotationsFound)
	fmt.Printf("Hook calls found: %d\n", hookCallsFound)
	fmt.Printf("Vue component contracts found: %d\n", vueContractsFound)
	fmt.Printf("Pinia stores found: %d\n", piniaStoresFound)
	fmt.Printf("Time: %v\n", elapsed)

	if *dryRun {
		fmt.Println("\n(Dry run - no data inserted)")
	}
}

type factDB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Replace a file's enrichment atomically: failed writes must not erase its facts.
func refreshTypeScriptFileFacts(connection *pgxpool.Pool, fileID int64, result parser.ParsedFile, expectedHash string) error {
	ctx := context.Background()
	pool, err := connection.Begin(ctx)
	if err != nil {
		return err
	}
	defer pool.Rollback(ctx)
	var lockedID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM files WHERE id = $1 AND hash=$2 FOR UPDATE`, fileID, expectedHash).Scan(&lockedID); err != nil {
		return err
	}
	classIDMap, err := classIDsForFile(pool, fileID)
	if err != nil {
		return err
	}
	functionIDMap, err := functionIDsForFile(pool, fileID)
	if err != nil {
		return err
	}
	if err := cleanupTypeScriptFileFacts(pool, fileID, classIDMap, functionIDMap); err != nil {
		return err
	}

	// Insert interfaces (reuse existing interfaces table)
	for _, iface := range parser.MergeInterfaceDeclarations(result.Interfaces) {
		if err := insertInterface(pool, fileID, &iface); err != nil {
			return err
		}
	}

	// Insert type aliases
	for _, ta := range result.TypeAliases {
		if err := insertTypeAlias(pool, fileID, &ta); err != nil {
			return err
		}
	}

	// Insert hook calls
	for _, hook := range result.HookCalls {
		if err := insertHookCall(pool, fileID, &hook); err != nil {
			return err
		}
	}

	for _, contract := range result.VueComponentContracts {
		if err := insertVueComponentContract(pool, fileID, &contract); err != nil {
			return err
		}
	}

	for _, store := range result.PiniaStores {
		if err := insertPiniaStore(pool, fileID, &store); err != nil {
			return err
		}
	}

	// Update class fields and annotations
	for _, cls := range result.Classes {
		classID, err := indexedDeclarationID(ctx, pool, "classes", fileID, []string{cls.Name}, cls.StartLine, cls.EndLine)
		if err != nil {
			return err
		}
		if classID == 0 {
			continue
		}

		// Insert fields
		for _, field := range cls.Fields {
			if err := insertField(pool, classID, &field); err != nil {
				return err
			}
		}

		if cls.IsEnum {
			for _, enumConstant := range cls.EnumConstants {
				if err := insertEnumConstant(pool, classID, &enumConstant); err != nil {
					return err
				}
			}
		}

		// Insert class annotations
		for _, ann := range cls.Annotations {
			if err := insertAnnotation(pool, "class", classID, &ann); err != nil {
				return err
			}
		}

		for _, method := range cls.Methods {
			if len(method.Annotations) == 0 {
				continue
			}
			names := []string{method.Name}
			if !strings.Contains(method.Name, ".") {
				names = append(names, cls.Name+"."+method.Name)
			}
			methodID, err := indexedDeclarationID(ctx, pool, "functions", fileID, names, method.StartLine, method.EndLine)
			if err != nil {
				return err
			}
			if methodID == 0 {
				continue
			}
			for _, ann := range method.Annotations {
				if err := insertAnnotation(pool, "method", methodID, &ann); err != nil {
					return err
				}
			}
		}
	}
	return pool.Commit(ctx)
}

func createTablesIfNeeded(pool *pgxpool.Pool) error {
	// Create new tables
	schema := `
	-- Type aliases (TypeScript type definitions)
	CREATE TABLE IF NOT EXISTS type_aliases (
		id SERIAL PRIMARY KEY,
		file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		definition TEXT,
		type_params JSONB,
		is_exported BOOLEAN DEFAULT FALSE,
		start_line INTEGER,
		UNIQUE(file_id, name)
	);

	-- React hook calls
	CREATE TABLE IF NOT EXISTS hook_calls (
		id SERIAL PRIMARY KEY,
		file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
		function_name TEXT,
		hook_name TEXT NOT NULL,
		dependencies JSONB,
		initial_value TEXT,
		line_number INTEGER,
		is_custom BOOLEAN DEFAULT FALSE,
		origin TEXT NOT NULL DEFAULT 'custom'
	);
	ALTER TABLE hook_calls ADD COLUMN IF NOT EXISTS origin TEXT NOT NULL DEFAULT 'custom';
	CREATE UNIQUE INDEX IF NOT EXISTS idx_hook_calls_unique
		ON hook_calls(file_id, hook_name, COALESCE(function_name, ''), COALESCE(line_number, 0));

	CREATE TABLE IF NOT EXISTS vue_component_contracts (
		id SERIAL PRIMARY KEY,
		file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
		kind TEXT NOT NULL,
		field_name TEXT NOT NULL,
		field_type TEXT,
		is_required BOOLEAN DEFAULT FALSE,
		line_number INTEGER,
		definition TEXT,
		UNIQUE(file_id, kind, field_name)
	);

	CREATE TABLE IF NOT EXISTS pinia_stores (
		id SERIAL PRIMARY KEY,
		file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
		store_id TEXT NOT NULL,
		line_number INTEGER,
		state_fields JSONB,
		getter_names JSONB,
		action_names JSONB,
		UNIQUE(file_id, store_id)
	);

	CREATE INDEX IF NOT EXISTS idx_type_aliases_name ON type_aliases(name);
	CREATE INDEX IF NOT EXISTS idx_type_aliases_file ON type_aliases(file_id);
	CREATE INDEX IF NOT EXISTS idx_hook_calls_hook ON hook_calls(hook_name);
	CREATE INDEX IF NOT EXISTS idx_hook_calls_file ON hook_calls(file_id);
	CREATE INDEX IF NOT EXISTS idx_hook_calls_origin ON hook_calls(origin);
	CREATE INDEX IF NOT EXISTS idx_vue_component_contracts_file ON vue_component_contracts(file_id);
	CREATE INDEX IF NOT EXISTS idx_vue_component_contracts_kind_field ON vue_component_contracts(kind, field_name);
	CREATE INDEX IF NOT EXISTS idx_pinia_stores_file ON pinia_stores(file_id);
	CREATE INDEX IF NOT EXISTS idx_pinia_stores_store ON pinia_stores(store_id);
	CREATE INDEX IF NOT EXISTS idx_fields_class_location ON fields(class_id, name, start_line)
		WHERE class_id IS NOT NULL;
	CREATE INDEX IF NOT EXISTS idx_fields_interface_location ON fields(interface_id, name, start_line)
		WHERE interface_id IS NOT NULL;
	`

	_, err := pool.Exec(context.Background(), schema)
	if err != nil {
		return err
	}

	// Add missing columns to existing interfaces table (if they don't exist)
	alterStatements := []string{
		`ALTER TABLE interfaces ADD COLUMN IF NOT EXISTS methods JSONB`,
		`ALTER TABLE interfaces ADD COLUMN IF NOT EXISTS fields JSONB`,
		`ALTER TABLE interfaces ADD COLUMN IF NOT EXISTS type_params JSONB`,
		`ALTER TABLE fields ADD COLUMN IF NOT EXISTS annotations JSONB`,
	}

	for _, stmt := range alterStatements {
		if _, err := pool.Exec(context.Background(), stmt); err != nil {
			return err
		}
	}

	return nil
}

func cleanupTypeScriptFileFacts(pool factDB, fileID int64, classIDMap map[string]int64, functionIDMap map[string]int64) error {
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DELETE FROM type_aliases WHERE file_id = $1`, fileID); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM hook_calls WHERE file_id = $1`, fileID); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM vue_component_contracts WHERE file_id = $1`, fileID); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM pinia_stores WHERE file_id = $1`, fileID); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM http_interface_methods WHERE interface_id IN (SELECT id FROM interfaces WHERE file_id = $1)`, fileID); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM type_parameters WHERE entity_type = 'interface' AND entity_id IN (SELECT id FROM interfaces WHERE file_id = $1)`, fileID); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM annotations WHERE entity_type = 'interface' AND entity_id IN (SELECT id FROM interfaces WHERE file_id = $1)`, fileID); err != nil {
		return err
	}
	for _, table := range []string{"annotations", "type_parameters"} {
		if _, err := pool.Exec(ctx, `DELETE FROM `+table+` WHERE entity_type='field' AND entity_id IN (SELECT id FROM fields WHERE interface_id IN (SELECT id FROM interfaces WHERE file_id=$1))`, fileID); err != nil {
			return err
		}
	}
	if _, err := pool.Exec(ctx, `DELETE FROM fields WHERE interface_id IN (SELECT id FROM interfaces WHERE file_id = $1)`, fileID); err != nil {
		return err
	}
	classIDs := make([]int64, 0, len(classIDMap))
	for _, classID := range classIDMap {
		classIDs = append(classIDs, classID)
	}
	if len(classIDs) > 0 {
		if _, err := pool.Exec(ctx, `DELETE FROM enum_constants WHERE class_id IN (SELECT id FROM classes WHERE file_id=$1)`, fileID); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM annotations WHERE entity_type = 'class' AND entity_id IN (SELECT id FROM classes WHERE file_id=$1)`, fileID); err != nil {
			return err
		}
	}
	functionIDs := make([]int64, 0, len(functionIDMap))
	for _, functionID := range functionIDMap {
		functionIDs = append(functionIDs, functionID)
	}
	if len(functionIDs) > 0 {
		if _, err := pool.Exec(ctx, `DELETE FROM annotations WHERE entity_type = 'method' AND entity_id IN (SELECT id FROM functions WHERE file_id=$1)`, fileID); err != nil {
			return err
		}
	}
	return nil
}

func classIDsForFile(pool factDB, fileID int64) (map[string]int64, error) {
	rows, err := pool.Query(context.Background(), `
		SELECT id, name
		FROM classes
		WHERE file_id = $1
	`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[name] = id
	}
	return out, rows.Err()
}

func functionIDsForFile(pool factDB, fileID int64) (map[string]int64, error) {
	rows, err := pool.Query(context.Background(), `
		SELECT id, name
		FROM functions
		WHERE file_id = $1
	`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[name] = id
	}
	return out, rows.Err()
}

func methodIDForClassMethod(functionIDMap map[string]int64, className, methodName string) int64 {
	methodName = strings.TrimSpace(methodName)
	if methodName == "" {
		return 0
	}
	if id := functionIDMap[methodName]; id != 0 {
		return id
	}
	className = strings.TrimSpace(className)
	if className == "" || strings.Contains(methodName, ".") {
		return 0
	}
	return functionIDMap[className+"."+methodName]
}

func insertInterface(pool factDB, fileID int64, iface *parser.ParsedInterface) error {
	extendsJSON, _ := json.Marshal(iface.ExtendsInterfaces)
	methodsJSON, _ := json.Marshal(iface.Methods)
	fieldsJSON, _ := json.Marshal(iface.Fields)
	typeParamsJSON, _ := json.Marshal(iface.TypeParameters)

	var interfaceID int64
	err := pool.QueryRow(context.Background(), `
		INSERT INTO interfaces (file_id, name, extends_interfaces, methods, fields, type_params, is_exported, start_line, end_line)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (file_id, name) DO UPDATE SET
			extends_interfaces = EXCLUDED.extends_interfaces,
			methods = EXCLUDED.methods,
			fields = EXCLUDED.fields,
			type_params = EXCLUDED.type_params,
			is_exported = EXCLUDED.is_exported,
			start_line = LEAST(interfaces.start_line, EXCLUDED.start_line),
			end_line = GREATEST(interfaces.end_line, EXCLUDED.end_line)
		RETURNING id
	`, fileID, iface.Name, extendsJSON, methodsJSON, fieldsJSON, typeParamsJSON, iface.IsExported, iface.StartLine, iface.EndLine).Scan(&interfaceID)
	if err != nil {
		return err
	}
	for _, field := range iface.Fields {
		if err := insertOwnedField(pool, nil, &interfaceID, &field); err != nil {
			return err
		}
	}
	for _, param := range iface.TypeParameters {
		bounds, err := json.Marshal(param.Bounds)
		if err != nil {
			return err
		}
		if _, err := pool.Exec(context.Background(), `INSERT INTO type_parameters
			(entity_type, entity_id, param_name, param_index, bounds, bound_type) VALUES ('interface', $1, $2, $3, $4, $5)`,
			interfaceID, param.Name, param.Index, bounds, param.BoundType); err != nil {
			return err
		}
	}
	for _, annotation := range iface.Annotations {
		if err := insertAnnotation(pool, "interface", interfaceID, &annotation); err != nil {
			return err
		}
	}

	return err
}

func insertTypeAlias(pool factDB, fileID int64, ta *parser.ParsedTypeAlias) error {
	typeParamsJSON, _ := json.Marshal(ta.TypeParams)

	_, err := pool.Exec(context.Background(), `
		INSERT INTO type_aliases (file_id, name, definition, type_params, is_exported, start_line)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (file_id, name) DO UPDATE SET
			definition = EXCLUDED.definition,
			type_params = EXCLUDED.type_params,
			is_exported = EXCLUDED.is_exported,
			start_line = EXCLUDED.start_line
	`, fileID, ta.Name, ta.Definition, typeParamsJSON, ta.IsExported, ta.StartLine)

	return err
}

func insertHookCall(pool factDB, fileID int64, hook *parser.ParsedHookCall) error {
	depsJSON, _ := json.Marshal(hook.Dependencies)

	_, err := pool.Exec(context.Background(), `
		INSERT INTO hook_calls (file_id, function_name, hook_name, dependencies, initial_value, line_number, is_custom, origin)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (file_id, hook_name, COALESCE(function_name, ''), COALESCE(line_number, 0)) DO UPDATE SET
			dependencies = EXCLUDED.dependencies,
			initial_value = EXCLUDED.initial_value,
			is_custom = EXCLUDED.is_custom,
			origin = EXCLUDED.origin
	`, fileID, hook.FunctionName, hook.HookName, depsJSON, hook.InitialValue, hook.LineNumber, hook.IsCustomHook, hookOriginForInsert(*hook))

	return err
}

func hookOriginForInsert(hook parser.ParsedHookCall) string {
	if strings.TrimSpace(hook.Origin) != "" {
		return strings.TrimSpace(hook.Origin)
	}
	if hook.IsCustomHook {
		return "custom"
	}
	return "framework"
}

func insertVueComponentContract(pool factDB, fileID int64, contract *parser.ParsedVueComponentContract) error {
	kind := strings.TrimSpace(contract.Kind)
	fieldName := strings.TrimSpace(contract.FieldName)
	if kind == "" || fieldName == "" {
		return nil
	}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO vue_component_contracts (file_id, kind, field_name, field_type, is_required, line_number, definition)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (file_id, kind, field_name) DO UPDATE SET
			field_type = EXCLUDED.field_type,
			is_required = EXCLUDED.is_required,
			line_number = EXCLUDED.line_number,
			definition = EXCLUDED.definition
	`, fileID, kind, fieldName, strings.TrimSpace(contract.FieldType), contract.IsRequired, contract.LineNumber, strings.TrimSpace(contract.Definition))
	return err
}

func insertPiniaStore(pool factDB, fileID int64, store *parser.ParsedPiniaStore) error {
	storeID := strings.TrimSpace(store.StoreID)
	if storeID == "" {
		return nil
	}
	stateJSON, _ := json.Marshal(store.StateFields)
	gettersJSON, _ := json.Marshal(store.Getters)
	actionsJSON, _ := json.Marshal(store.Actions)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO pinia_stores (file_id, store_id, line_number, state_fields, getter_names, action_names)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (file_id, store_id) DO UPDATE SET
			line_number = EXCLUDED.line_number,
			state_fields = EXCLUDED.state_fields,
			getter_names = EXCLUDED.getter_names,
			action_names = EXCLUDED.action_names
	`, fileID, storeID, store.LineNumber, stateJSON, gettersJSON, actionsJSON)
	return err
}

func insertField(pool factDB, classID int64, field *parser.ParsedField) error {
	return insertOwnedField(pool, &classID, nil, field)
}

func insertOwnedField(pool factDB, classID, interfaceID *int64, field *parser.ParsedField) error {
	modifiersJSON, _ := json.Marshal(field.Modifiers)
	annotationsJSON, _ := json.Marshal(field.Annotations)
	typesJSON, _ := json.Marshal(field.TypeParameters)

	_, err := pool.Exec(context.Background(), `
		WITH updated AS (
			UPDATE fields SET field_type = $4, modifiers = $5, is_injected = $7,
				injection_type = $8, annotations = $9, type_parameters = $10
			WHERE ((class_id = $1::integer AND interface_id IS NULL)
				OR (interface_id = $2::integer AND class_id IS NULL)) AND name = $3
			AND start_line IS NOT DISTINCT FROM $6::integer RETURNING id
		)
		INSERT INTO fields (class_id, interface_id, name, field_type, modifiers, start_line,
			is_injected, injection_type, annotations, type_parameters)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10 WHERE NOT EXISTS (SELECT 1 FROM updated)
	`, classID, interfaceID, field.Name, field.FieldType, modifiersJSON, field.StartLine, field.IsInjected, field.InjectionType, annotationsJSON, typesJSON)

	return err
}

func insertEnumConstant(pool factDB, classID int64, enumConstant *parser.ParsedEnumConstant) error {
	name := strings.TrimSpace(enumConstant.Name)
	if name == "" {
		return nil
	}
	argumentsJSON, _ := json.Marshal(enumConstant.Arguments)

	_, err := pool.Exec(context.Background(), `
		INSERT INTO enum_constants (class_id, name, ordinal, arguments)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (class_id, name) DO UPDATE SET
			ordinal = EXCLUDED.ordinal,
			arguments = EXCLUDED.arguments
	`, classID, name, enumConstant.Ordinal, argumentsJSON)
	return err
}

func insertAnnotation(pool factDB, entityType string, entityID int64, ann *parser.ParsedAnnotation) error {
	valuesJSON, _ := json.Marshal(ann.Values)

	_, err := pool.Exec(context.Background(), `
		INSERT INTO annotations (entity_type, entity_id, name, values, line_number)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT DO NOTHING
	`, entityType, entityID, ann.Name, valuesJSON, ann.LineNumber)

	return err
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// Coordinates distinguish overloads and same-named declarations in different scopes.
func indexedDeclarationID(ctx context.Context, pool factDB, table string, fileID int64, names []string, start, end int) (int64, error) {
	if table != "classes" && table != "functions" {
		return 0, fmt.Errorf("unsupported declaration table")
	}
	var id *int64
	err := pool.QueryRow(ctx, `SELECT CASE WHEN COUNT(*)=1 THEN MIN(id) END FROM `+table+` WHERE file_id=$1 AND name=ANY($2) AND start_line=$3 AND end_line=$4`, fileID, names, start, end).Scan(&id)
	if err != nil {
		return 0, err
	}
	if id == nil {
		return 0, fmt.Errorf("indexed %s declaration does not uniquely match source coordinates in file %d", table, fileID)
	}
	return *id, nil
}
