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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sergiumoraru/tirion/internal/config"
	"github.com/sergiumoraru/tirion/internal/graph"
	"github.com/sergiumoraru/tirion/internal/indexer"
	"github.com/sergiumoraru/tirion/internal/parser"
	"github.com/sergiumoraru/tirion/internal/sourceindex"
)

type functionMeta struct {
	id        int64
	startLine int
	endLine   int
}

func addFunctionMeta(m map[string][]functionMeta, name string, id int64, startLine, endLine int) {
	m[name] = append(m[name], functionMeta{id: id, startLine: startLine, endLine: endLine})
}

func selectFunctionIDBySpan(metas []functionMeta, start, end int) (int64, bool) {
	var id int64
	count := 0
	for _, meta := range metas {
		if meta.startLine == start && meta.endLine == end {
			id = meta.id
			count++
		}
	}
	return id, count == 1
}

func selectFunctionIDByLine(metas []functionMeta, line int) (int64, bool) {
	// A nested anonymous class may repeat an enclosing method's name. Prefer
	// its declaration line, then the narrowest containing body, never row order.
	var exactID int64
	exactCount := 0
	for _, meta := range metas {
		if line == meta.startLine {
			exactID = meta.id
			exactCount++
		}
	}
	if exactCount > 0 {
		return exactID, exactCount == 1
	}
	var selected functionMeta
	found, ambiguous := false, false
	for _, meta := range metas {
		if line >= meta.startLine && line <= meta.endLine {
			width := meta.endLine - meta.startLine
			if !found || width < selected.endLine-selected.startLine {
				selected, found, ambiguous = meta, true, false
			} else if width == selected.endLine-selected.startLine {
				ambiguous = true
			}
		}
	}
	if found {
		return selected.id, !ambiguous
	}
	if len(metas) == 1 {
		return metas[0].id, true
	}
	return 0, false
}

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
		fmt.Fprintf(os.Stderr, "extract-java-intel: unexpected argument(s) %q; this command takes only flags\n", flag.Args())
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
		if err := indexer.RunPostParseExtractors(indexer.PostParseOptions{DBURL: *dbURL, Workspace: *workspaceSlug, Repo: *repoFilter, RawList: "java-intel", Verbose: *verbose}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	fmt.Println("Extracting Java code intelligence from indexed files...")
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

	// Query all Java files
	rows, err := pool.Query(context.Background(), `
		SELECT f.id, f.path, r.name, COALESCE(NULLIF(rs.source_path,''), NULLIF(wr.worktree_path, ''), r.path) as repo_path, COALESCE(f.hash, ''), rs.sha
		FROM files f
		JOIN repo_snapshots rs ON rs.id = f.snapshot_id
		JOIN repositories r ON f.repo_id = r.id
		JOIN workspaces w ON w.slug = $1
 LEFT JOIN workspace_repos wr ON wr.repo_name=r.name AND wr.workspace_id=w.id
		WHERE f.language = 'java'
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
	fmt.Printf("Found %d Java files to process\n", len(files))

	// Initialize parser
	javaParser := parser.NewJavaParser().ForEnrichment()

	// Statistics
	var filesFailed int
	var filesProcessed, interfacesFound, fieldsFound, annotationsFound int
	var constructorParamsFound, httpMethodsFound int
	var jpaEntitiesFound, jpaRelationshipsFound int
	var syntheticMethodsFound, enumConstantsFound, lambdasFound int

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

		// Parse file
		parseStart := time.Now()
		result := javaParser.ParseFile(fullPath, content)
		parseDuration += time.Since(parseStart)
		if d := result.ParseDiagnostics; d.Failed() {
			if d.Systemic() {
				log.Fatalf("Parse %s: %s: %s", f.path, d.FailureKind, d.Message)
			}
			if !d.Usable() {
				filesFailed++
				log.Printf("Skipping %s after parse failure [%s]; its Java facts are not refreshed: %s", f.path, d.FailureKind, d.Message)
				continue
			}
			log.Printf("Partial syntax tree in %s; extracting recovered Java facts: %s", f.path, d.Message)
		}
		if !*dryRun {
			persistStart := time.Now()
			if err := insertExtractedData(pool, f.id, &result, f.hash); err != nil {
				log.Fatalf("Refresh %s: %v", f.path, err)
			}
			persistDuration += time.Since(persistStart)
		}

		// Count what we found
		numInterfaces := len(result.Interfaces)
		numFields := 0
		numAnnotations := 0
		numConstructorParams := 0

		for _, cls := range result.Classes {
			numFields += len(cls.Fields)
			numAnnotations += len(cls.Annotations)
			for _, ctor := range cls.Constructors {
				numConstructorParams += len(ctor.Parameters)
			}
			for _, fn := range cls.Methods {
				numAnnotations += len(fn.Annotations)
			}
		}

		for _, iface := range result.Interfaces {
			numFields += len(iface.Fields)
			numAnnotations += len(iface.Annotations)
			for _, m := range iface.Methods {
				numAnnotations += len(m.Annotations)
			}
		}

		numHttpMethods := len(result.HttpInterfaceMethods)

		// New feature counts
		numJpaEntities := len(result.JpaEntities)
		numJpaRelationships := len(result.JpaRelationships)
		numSyntheticMethods := len(result.SyntheticMethods)
		numEnumConstants := 0
		for _, cls := range result.Classes {
			numEnumConstants += len(cls.EnumConstants)
		}
		numLambdas := len(result.Lambdas)

		hasData := numInterfaces > 0 || numFields > 0 || numAnnotations > 0 || numConstructorParams > 0 || numHttpMethods > 0 ||
			numJpaEntities > 0 || numJpaRelationships > 0 || numSyntheticMethods > 0 || numEnumConstants > 0 || numLambdas > 0

		if hasData {
			if *verbose {
				fmt.Printf("  %s/%s: %d ifaces, %d fields, %d anns, %d ctors, %d http, %d jpa, %d rels, %d synth, %d enums, %d lambdas\n",
					f.repoName, f.path, numInterfaces, numFields, numAnnotations, numConstructorParams, numHttpMethods,
					numJpaEntities, numJpaRelationships, numSyntheticMethods, numEnumConstants, numLambdas)
			}

			filesProcessed++
			interfacesFound += numInterfaces
			fieldsFound += numFields
			annotationsFound += numAnnotations
			constructorParamsFound += numConstructorParams
			httpMethodsFound += numHttpMethods
			jpaEntitiesFound += numJpaEntities
			jpaRelationshipsFound += numJpaRelationships
			syntheticMethodsFound += numSyntheticMethods
			enumConstantsFound += numEnumConstants
			lambdasFound += numLambdas
		}
	}
	fmt.Printf("Enrichment phases: read=%.2fs parse=%.2fs persist=%.2fs\n", readDuration.Seconds(), parseDuration.Seconds(), persistDuration.Seconds())

	elapsed := time.Since(startTime)

	fmt.Println(strings.Repeat("-", 50))
	fmt.Printf("Extraction completed in %.2fs\n", elapsed.Seconds())
	fmt.Printf("Files with data:       %d\n", filesProcessed)
	fmt.Printf("Files skipped after parse failure (not refreshed): %d\n", filesFailed)
	fmt.Printf("Interfaces:            %d\n", interfacesFound)
	fmt.Printf("Fields:                %d\n", fieldsFound)
	fmt.Printf("Annotations:           %d\n", annotationsFound)
	fmt.Printf("Constructor params:    %d\n", constructorParamsFound)
	fmt.Printf("HTTP interface methods:%d\n", httpMethodsFound)
	fmt.Printf("JPA entities:          %d\n", jpaEntitiesFound)
	fmt.Printf("JPA relationships:     %d\n", jpaRelationshipsFound)
	fmt.Printf("Synthetic methods:     %d\n", syntheticMethodsFound)
	fmt.Printf("Enum constants:        %d\n", enumConstantsFound)
	fmt.Printf("Lambda expressions:    %d\n", lambdasFound)

	if *dryRun {
		fmt.Println("\n(Dry run - no data was inserted)")
	}
}

func createTablesIfNeeded(pool *pgxpool.Pool) error {
	// The tables should already exist from schema.go, but ensure they do
	queries := []string{
		`ALTER TABLE files ADD COLUMN IF NOT EXISTS java_package TEXT`,
		`CREATE TABLE IF NOT EXISTS interfaces (
			id SERIAL PRIMARY KEY,
			file_id INTEGER NOT NULL REFERENCES files(id),
			name TEXT NOT NULL,
			start_line INTEGER NOT NULL,
			end_line INTEGER NOT NULL,
			extends_interfaces JSONB,
			is_functional BOOLEAN DEFAULT FALSE,
			is_exported BOOLEAN DEFAULT FALSE,
			UNIQUE(file_id, name)
		)`,
		`CREATE TABLE IF NOT EXISTS implementations (
			id SERIAL PRIMARY KEY,
			class_id INTEGER NOT NULL REFERENCES classes(id),
			interface_name TEXT NOT NULL,
			interface_id INTEGER REFERENCES interfaces(id),
			UNIQUE(class_id, interface_name)
		)`,
		`CREATE TABLE IF NOT EXISTS fields (
			id SERIAL PRIMARY KEY,
			class_id INTEGER REFERENCES classes(id),
			interface_id INTEGER REFERENCES interfaces(id),
			name TEXT NOT NULL,
			field_type TEXT NOT NULL,
			type_parameters JSONB,
			modifiers JSONB,
			start_line INTEGER,
			is_injected BOOLEAN DEFAULT FALSE,
			injection_type TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS annotations (
			id SERIAL PRIMARY KEY,
			entity_type TEXT NOT NULL,
			entity_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			values JSONB,
			line_number INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS method_signatures (
			id SERIAL PRIMARY KEY,
			function_id INTEGER NOT NULL REFERENCES functions(id),
			signature TEXT NOT NULL,
			parameter_types JSONB,
			return_type TEXT,
			is_override BOOLEAN DEFAULT FALSE,
			overrides_method_id INTEGER REFERENCES functions(id),
			UNIQUE(function_id)
		)`,
		`CREATE TABLE IF NOT EXISTS constructor_params (
			id SERIAL PRIMARY KEY,
			class_id INTEGER NOT NULL REFERENCES classes(id),
			param_name TEXT NOT NULL,
			param_type TEXT NOT NULL,
			param_index INTEGER NOT NULL,
			is_injected BOOLEAN DEFAULT FALSE,
			annotation TEXT,
			annotation_value TEXT,
			line_number INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS type_parameters (
			id SERIAL PRIMARY KEY,
			entity_type TEXT NOT NULL,
			entity_id INTEGER NOT NULL,
			param_name TEXT NOT NULL,
			param_index INTEGER NOT NULL,
			bounds JSONB,
			bound_type TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS http_interface_methods (
			id SERIAL PRIMARY KEY,
			interface_id INTEGER NOT NULL REFERENCES interfaces(id),
			method_name TEXT NOT NULL,
			function_id INTEGER REFERENCES functions(id),
			http_method TEXT NOT NULL,
			url_pattern TEXT NOT NULL,
			line_number INTEGER,
			UNIQUE(interface_id, method_name)
		)`,
		// New tables for JPA, Lombok, enums, lambdas
		`CREATE TABLE IF NOT EXISTS jpa_entities (
			id SERIAL PRIMARY KEY,
			class_id INTEGER NOT NULL REFERENCES classes(id),
			table_name TEXT NOT NULL,
			schema_name TEXT,
			catalog TEXT,
			UNIQUE(class_id)
		)`,
		`CREATE TABLE IF NOT EXISTS jpa_relationships (
			id SERIAL PRIMARY KEY,
			source_class_id INTEGER NOT NULL REFERENCES classes(id),
			target_entity_name TEXT NOT NULL,
			target_class_id INTEGER REFERENCES classes(id),
			relation_type TEXT NOT NULL,
			source_field TEXT NOT NULL,
			mapped_by TEXT,
			join_column TEXT,
			fetch_type TEXT,
			cascade_types JSONB,
			line_number INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS synthetic_methods (
			id SERIAL PRIMARY KEY,
			class_id INTEGER NOT NULL REFERENCES classes(id),
			name TEXT NOT NULL,
			return_type TEXT,
			params JSONB,
			source TEXT NOT NULL,
			annotation TEXT,
			field_name TEXT,
			line_number INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS enum_constants (
			id SERIAL PRIMARY KEY,
			class_id INTEGER NOT NULL REFERENCES classes(id),
			name TEXT NOT NULL,
			ordinal INTEGER NOT NULL,
			arguments JSONB,
			UNIQUE(class_id, name)
		)`,
		`CREATE TABLE IF NOT EXISTS lambda_expressions (
			id SERIAL PRIMARY KEY,
			file_id INTEGER NOT NULL REFERENCES files(id),
			class_name TEXT,
			method_name TEXT,
			parameters JSONB,
			start_line INTEGER,
			end_line INTEGER
		)`,
		// Indexes for new tables
		`CREATE INDEX IF NOT EXISTS idx_jpa_entities_table ON jpa_entities(table_name)`,
		`CREATE INDEX IF NOT EXISTS idx_jpa_entities_class ON jpa_entities(class_id)`,
		`CREATE INDEX IF NOT EXISTS idx_jpa_relationships_source ON jpa_relationships(source_class_id)`,
		`CREATE INDEX IF NOT EXISTS idx_jpa_relationships_target ON jpa_relationships(target_entity_name)`,
		`CREATE INDEX IF NOT EXISTS idx_synthetic_methods_class ON synthetic_methods(class_id)`,
		`CREATE INDEX IF NOT EXISTS idx_synthetic_methods_name ON synthetic_methods(name)`,
		`CREATE INDEX IF NOT EXISTS idx_enum_constants_class ON enum_constants(class_id)`,
	}

	for _, q := range queries {
		if _, err := pool.Exec(context.Background(), q); err != nil {
			return err
		}
	}
	return nil
}

func insertExtractedData(connection *pgxpool.Pool, fileID int64, result *parser.ParsedFile, expectedHash string) error {
	ctx := context.Background()
	pool, err := connection.Begin(ctx)
	if err != nil {
		return err
	}
	defer pool.Rollback(ctx)
	var lockedID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM files WHERE id=$1 AND hash=$2 FOR UPDATE`, fileID, expectedHash).Scan(&lockedID); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `UPDATE files SET java_package=$2 WHERE id=$1`, fileID, result.JavaPackage); err != nil {
		return err
	}

	classes, err := graph.LoadClassDeclarations(ctx, pool, fileID)
	if err != nil {
		return err
	}
	classIDs := make(map[string]int64, len(result.Classes))
	for _, cls := range result.Classes {
		id, err := classes.Match(cls.Name, cls.StartLine, cls.EndLine)
		if err != nil {
			return err
		}
		classIDs[cls.Name] = id
	}

	// Get function IDs for this file
	functionMetas := make(map[string][]functionMeta)
	rows, err := pool.Query(ctx, `SELECT id, name, start_line, end_line FROM functions WHERE file_id = $1`, fileID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var name string
		var startLine int
		var endLine int
		if err := rows.Scan(&id, &name, &startLine, &endLine); err != nil {
			rows.Close()
			return err
		}
		addFunctionMeta(functionMetas, name, id, startLine, endLine)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Get existing interface IDs for this file so rerunning the extractor replaces,
	// rather than duplicates, interface-scoped facts.
	existingInterfaceIDs := make(map[string]int64)
	rows, err = pool.Query(ctx, `SELECT id, name FROM interfaces WHERE file_id = $1`, fileID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return err
		}
		existingInterfaceIDs[name] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if err := cleanupJavaIntelFileFacts(ctx, pool, fileID, classes.IDs, existingInterfaceIDs, functionMetas); err != nil {
		return err
	}

	// Queue dependent metadata only after declaration IDs and cleanup are complete.
	// Bounded protocol batches retain statement order and per-file rollback while
	// avoiding a client/server round trip for every field or annotation.
	batch := &pgx.Batch{}
	flush := func() error {
		if batch.Len() == 0 {
			return nil
		}
		results := pool.SendBatch(ctx, batch)
		err := results.Close()
		batch = &pgx.Batch{}
		return err
	}
	queue := func(query string, args ...any) error {
		batch.Queue(query, args...)
		if batch.Len() >= 256 {
			return flush()
		}
		return nil
	}

	// Insert interfaces
	interfaceIDs := make(map[string]int64)
	for _, iface := range result.Interfaces {
		var id int64
		err := pool.QueryRow(ctx, `
			INSERT INTO interfaces (file_id, name, start_line, end_line, extends_interfaces, is_functional, is_exported)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (file_id, name) DO UPDATE SET
				start_line = EXCLUDED.start_line,
				end_line = EXCLUDED.end_line,
				extends_interfaces = EXCLUDED.extends_interfaces,
				is_functional = EXCLUDED.is_functional,
				is_exported = EXCLUDED.is_exported
			RETURNING id
		`, fileID, iface.Name, iface.StartLine, iface.EndLine,
			toJSON(iface.ExtendsInterfaces), iface.IsFunctional, iface.IsExported).Scan(&id)
		if err != nil {
			return err
		}
		interfaceIDs[iface.Name] = id

		// Insert interface annotations
		for _, ann := range iface.Annotations {
			if err := queue(`
				INSERT INTO annotations (entity_type, entity_id, name, values, line_number)
				VALUES ('interface', $1, $2, $3, $4)
			`, id, ann.Name, toJSON(ann.Values), ann.LineNumber); err != nil {
				return err
			}
		}

		// Insert interface fields
		for _, field := range iface.Fields {
			if err := queue(`
				INSERT INTO fields (interface_id, name, field_type, type_parameters, modifiers, start_line, is_injected, injection_type)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			`, id, field.Name, field.FieldType, toJSON(field.TypeParameters), toJSON(field.Modifiers),
				field.StartLine, field.IsInjected, field.InjectionType); err != nil {
				return err
			}
		}

		// Insert interface type parameters
		for _, tp := range iface.TypeParameters {
			if err := queue(`
				INSERT INTO type_parameters (entity_type, entity_id, param_name, param_index, bounds, bound_type)
				VALUES ('interface', $1, $2, $3, $4, $5)
			`, id, tp.Name, tp.Index, toJSON(tp.Bounds), tp.BoundType); err != nil {
				return err
			}
		}
	}

	// Insert class-related data
	for _, cls := range result.Classes {
		classID, ok := classIDs[cls.Name]
		if !ok {
			continue
		}

		// Insert class annotations
		for _, ann := range cls.Annotations {
			if err := queue(`
				INSERT INTO annotations (entity_type, entity_id, name, values, line_number)
				VALUES ('class', $1, $2, $3, $4)
			`, classID, ann.Name, toJSON(ann.Values), ann.LineNumber); err != nil {
				return err
			}
		}

		// Insert class fields
		for _, field := range cls.Fields {
			if err := queue(`
				INSERT INTO fields (class_id, name, field_type, type_parameters, modifiers, start_line, is_injected, injection_type)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			`, classID, field.Name, field.FieldType, toJSON(field.TypeParameters), toJSON(field.Modifiers),
				field.StartLine, field.IsInjected, field.InjectionType); err != nil {
				return err
			}
		}

		// Insert constructor params
		for _, ctor := range cls.Constructors {
			for _, param := range ctor.Parameters {
				if err := queue(`
					INSERT INTO constructor_params (class_id, param_name, param_type, param_index, is_injected, annotation, annotation_value, line_number)
					VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
				`, classID, param.Name, param.ParamType, param.Index, param.IsInjected,
					param.Annotation, param.AnnotationValue, ctor.StartLine); err != nil {
					return err
				}
			}
		}

		// Insert class type parameters
		for _, tp := range cls.TypeParameters {
			if err := queue(`
				INSERT INTO type_parameters (entity_type, entity_id, param_name, param_index, bounds, bound_type)
				VALUES ('class', $1, $2, $3, $4, $5)
			`, classID, tp.Name, tp.Index, toJSON(tp.Bounds), tp.BoundType); err != nil {
				return err
			}
		}

		// Insert implementations
		for _, ifaceName := range cls.Implements {
			var ifaceID *int64
			for scope := cls.Name; ; {
				name := ifaceName
				if scope != "" {
					name = scope + "." + name
				}
				if id, ok := interfaceIDs[name]; ok {
					ifaceID = &id
					break
				}
				if scope == "" {
					break
				}
				if i := strings.LastIndexByte(scope, '.'); i >= 0 {
					scope = scope[:i]
				} else {
					scope = ""
				}
			}
			if err := queue(`
				INSERT INTO implementations (class_id, interface_name, interface_id)
				VALUES ($1, $2, $3)
				ON CONFLICT (class_id, interface_name) DO UPDATE SET interface_id = EXCLUDED.interface_id
			`, classID, ifaceName, ifaceID); err != nil {
				return err
			}
		}
	}

	// Insert function annotations and signatures
	for _, fn := range result.Functions {
		fnID, ok := selectFunctionIDBySpan(functionMetas[fn.Name], fn.StartLine, fn.EndLine)
		if !ok {
			return fmt.Errorf("indexed method %q at lines %d-%d has no unique declaration; run a full reindex with the current parser and helpers", fn.Name, fn.StartLine, fn.EndLine)
		}

		// Insert function annotations
		for _, ann := range fn.Annotations {
			if err := queue(`
				INSERT INTO annotations (entity_type, entity_id, name, values, line_number)
				VALUES ('method', $1, $2, $3, $4)
			`, fnID, ann.Name, toJSON(ann.Values), ann.LineNumber); err != nil {
				return err
			}
		}

		// Insert method signature
		if fn.Signature != nil {
			if err := queue(`
				INSERT INTO method_signatures (function_id, signature, parameter_types, return_type, is_override)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (function_id) DO UPDATE SET
					signature = EXCLUDED.signature,
					parameter_types = EXCLUDED.parameter_types,
					return_type = EXCLUDED.return_type,
					is_override = EXCLUDED.is_override
			`, fnID, fn.Signature.Signature, toJSON(fn.Signature.ParameterTypes),
				fn.Signature.ReturnType, fn.Signature.IsOverride); err != nil {
				return err
			}
		}

		// Insert function type parameters
		for _, tp := range fn.TypeParameters {
			if err := queue(`
				INSERT INTO type_parameters (entity_type, entity_id, param_name, param_index, bounds, bound_type)
				VALUES ('method', $1, $2, $3, $4, $5)
			`, fnID, tp.Name, tp.Index, toJSON(tp.Bounds), tp.BoundType); err != nil {
				return err
			}
		}
	}

	// Insert HTTP interface methods
	for _, httpMethod := range result.HttpInterfaceMethods {
		ifaceID, ok := interfaceIDs[httpMethod.InterfaceName]
		if !ok {
			continue
		}

		var fnID *int64
		methodFullName := httpMethod.InterfaceName + "." + httpMethod.MethodName
		if id, ok := selectFunctionIDByLine(functionMetas[methodFullName], httpMethod.LineNumber); ok {
			fnID = &id
		}

		if err := queue(`
			INSERT INTO http_interface_methods (interface_id, method_name, function_id, http_method, url_pattern, line_number)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (interface_id, method_name) DO UPDATE SET
				function_id = EXCLUDED.function_id,
				http_method = EXCLUDED.http_method,
				url_pattern = EXCLUDED.url_pattern,
				line_number = EXCLUDED.line_number
		`, ifaceID, httpMethod.MethodName, fnID, httpMethod.HttpMethod, httpMethod.UrlPattern, httpMethod.LineNumber); err != nil {
			return err
		}
	}

	// Insert JPA entities
	for _, entity := range result.JpaEntities {
		classID, ok := classIDs[entity.ClassName]
		if !ok {
			continue
		}
		if err := queue(`
			INSERT INTO jpa_entities (class_id, table_name, schema_name, catalog)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (class_id) DO UPDATE SET
				table_name = EXCLUDED.table_name,
				schema_name = EXCLUDED.schema_name,
				catalog = EXCLUDED.catalog
		`, classID, entity.TableName, nullIfEmpty(entity.Schema), nullIfEmpty(entity.Catalog)); err != nil {
			return err
		}
	}

	// Insert JPA relationships
	for _, rel := range result.JpaRelationships {
		sourceClassID, ok := classIDs[rel.SourceEntity]
		if !ok {
			continue
		}
		var targetClassID *int64
		if id, ok := classIDs[rel.TargetEntity]; ok {
			targetClassID = &id
		}
		if err := queue(`
			INSERT INTO jpa_relationships (source_class_id, target_entity_name, target_class_id, relation_type, source_field, mapped_by, join_column, fetch_type, cascade_types, line_number)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		`, sourceClassID, rel.TargetEntity, targetClassID, rel.RelationType, rel.SourceField,
			nullIfEmpty(rel.MappedBy), nullIfEmpty(rel.JoinColumnName), nullIfEmpty(rel.FetchType),
			toJSON(rel.CascadeTypes), rel.LineNumber); err != nil {
			return err
		}
	}

	// Insert synthetic methods (Lombok, records)
	for _, method := range result.SyntheticMethods {
		classID, ok := classIDs[method.ClassName]
		if !ok {
			continue
		}
		if err := queue(`
			INSERT INTO synthetic_methods (class_id, name, return_type, params, source, annotation, field_name, line_number)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, classID, method.Name, nullIfEmpty(method.ReturnType), toJSON(method.Params),
			method.Source, nullIfEmpty(method.Annotation), nullIfEmpty(method.FieldName), method.LineNumber); err != nil {
			return err
		}
	}

	// Insert enum constants
	for _, cls := range result.Classes {
		if !cls.IsEnum || len(cls.EnumConstants) == 0 {
			continue
		}
		classID, ok := classIDs[cls.Name]
		if !ok {
			continue
		}
		for _, constant := range cls.EnumConstants {
			if err := queue(`
				INSERT INTO enum_constants (class_id, name, ordinal, arguments)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (class_id, name) DO UPDATE SET
					ordinal = EXCLUDED.ordinal,
					arguments = EXCLUDED.arguments
			`, classID, constant.Name, constant.Ordinal, toJSON(constant.Arguments)); err != nil {
				return err
			}
		}
	}

	// Insert lambda expressions
	for _, lambda := range result.Lambdas {
		if err := queue(`
			INSERT INTO lambda_expressions (file_id, class_name, method_name, parameters, start_line, end_line)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, fileID, nullIfEmpty(lambda.ContainingClass), nullIfEmpty(lambda.ContainingMethod),
			toJSON(lambda.Parameters), lambda.StartLine, lambda.EndLine); err != nil {
			return err
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return pool.Commit(ctx)
}

func cleanupJavaIntelFileFacts(ctx context.Context, pool pgx.Tx, fileID int64, classIDList []int64, interfaceIDs map[string]int64, functionMetas map[string][]functionMeta) error {
	interfaceIDList := mapValues(interfaceIDs)
	functionIDList := functionMetaIDs(functionMetas)

	// Delete polymorphic metadata before fields/constructor parameters lose their owners.
	for _, table := range []string{"annotations", "type_parameters"} {
		// Drive deletion from this file's owners. An OR across separate metadata
		// subqueries can scan all retained annotations once for every Java file.
		if _, err := pool.Exec(ctx, `DELETE FROM `+table+` m USING fields owner
 WHERE m.entity_type='field' AND m.entity_id=owner.id
 AND (owner.class_id=ANY($1) OR owner.interface_id=ANY($2))`, classIDList, interfaceIDList); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM `+table+` m USING constructor_params owner
 WHERE m.entity_type='parameter' AND m.entity_id=owner.id AND owner.class_id=ANY($1)`, classIDList); err != nil {
			return err
		}
	}

	if len(interfaceIDList) > 0 {
		if _, err := pool.Exec(ctx, `DELETE FROM fields WHERE interface_id = ANY($1)`, interfaceIDList); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM annotations WHERE entity_type = 'interface' AND entity_id = ANY($1)`, interfaceIDList); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM type_parameters WHERE entity_type = 'interface' AND entity_id = ANY($1)`, interfaceIDList); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM http_interface_methods WHERE interface_id = ANY($1)`, interfaceIDList); err != nil {
			return err
		}
	}
	if len(classIDList) > 0 {
		if _, err := pool.Exec(ctx, `DELETE FROM fields WHERE class_id = ANY($1)`, classIDList); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM annotations WHERE entity_type = 'class' AND entity_id = ANY($1)`, classIDList); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM constructor_params WHERE class_id = ANY($1)`, classIDList); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM type_parameters WHERE entity_type = 'class' AND entity_id = ANY($1)`, classIDList); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM implementations WHERE class_id = ANY($1)`, classIDList); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM jpa_entities WHERE class_id = ANY($1)`, classIDList); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM jpa_relationships WHERE source_class_id = ANY($1)`, classIDList); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM synthetic_methods WHERE class_id = ANY($1)`, classIDList); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM enum_constants WHERE class_id = ANY($1)`, classIDList); err != nil {
			return err
		}
	}
	if len(functionIDList) > 0 {
		if _, err := pool.Exec(ctx, `DELETE FROM annotations WHERE entity_type = 'method' AND entity_id = ANY($1)`, functionIDList); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM method_signatures WHERE function_id = ANY($1)`, functionIDList); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM type_parameters WHERE entity_type = 'method' AND entity_id = ANY($1)`, functionIDList); err != nil {
			return err
		}
	}
	if _, err := pool.Exec(ctx, `DELETE FROM lambda_expressions WHERE file_id = $1`, fileID); err != nil {
		return err
	}
	return nil
}

func mapValues(values map[string]int64) []int64 {
	out := make([]int64, 0, len(values))
	for _, id := range values {
		out = append(out, id)
	}
	return out
}

func functionMetaIDs(functionMetas map[string][]functionMeta) []int64 {
	seen := map[int64]bool{}
	out := []int64{}
	for _, metas := range functionMetas {
		for _, meta := range metas {
			if seen[meta.id] {
				continue
			}
			seen[meta.id] = true
			out = append(out, meta.id)
		}
	}
	return out
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toJSON(v interface{}) []byte {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case []string:
		if len(val) == 0 {
			return nil
		}
	case map[string]interface{}:
		if len(val) == 0 {
			return nil
		}
	}
	b, _ := json.Marshal(v)
	return b
}
