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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sergiumoraru/tirion/internal/config"
	"github.com/sergiumoraru/tirion/internal/graph"
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
		fmt.Fprintf(os.Stderr, "extract-spring: unexpected argument(s) %q; this command takes only flags\n", flag.Args())
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
		if err := indexer.RunPostParseExtractors(indexer.PostParseOptions{DBURL: *dbURL, Workspace: *workspaceSlug, Repo: *repoFilter, RawList: "spring", Verbose: *verbose}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	fmt.Println("Extracting Spring framework patterns from indexed Java files...")
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

	// Ensure tables exist
	tables := []string{
		`CREATE TABLE IF NOT EXISTS bean_definitions (
			id SERIAL PRIMARY KEY,
			config_class_id INTEGER NOT NULL REFERENCES classes(id),
			method_id INTEGER REFERENCES functions(id),
			bean_name TEXT NOT NULL,
			bean_type TEXT NOT NULL,
			qualifiers JSONB,
			is_primary BOOLEAN DEFAULT FALSE,
			line_number INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS event_listeners (
			id SERIAL PRIMARY KEY,
			class_id INTEGER NOT NULL REFERENCES classes(id),
			method_id INTEGER REFERENCES functions(id),
			method_name TEXT NOT NULL,
			event_types JSONB,
			condition TEXT,
			is_async BOOLEAN DEFAULT FALSE,
			line_number INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS scheduled_methods (
			id SERIAL PRIMARY KEY,
			class_id INTEGER NOT NULL REFERENCES classes(id),
			method_id INTEGER REFERENCES functions(id),
			method_name TEXT NOT NULL,
			cron TEXT,
			fixed_rate INTEGER,
			fixed_delay INTEGER,
			initial_delay INTEGER,
			line_number INTEGER
		)`,
	}

	for _, ddl := range tables {
		if !*dryRun && os.Getenv("CODEBASE_SKIP_SCHEMA_INIT") != "1" {
			if _, err := pool.Exec(context.Background(), ddl); err != nil {
				log.Fatalf("Initialize Spring tables: %v", err)
			}
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
	var filesProcessed, beansExtracted, listenersExtracted, scheduledExtracted int

	debugCount := 0
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
				log.Printf("Skipping %s after parse failure [%s]; its Spring facts are not refreshed: %s", f.path, d.FailureKind, d.Message)
				continue
			}
			log.Printf("Partial syntax tree in %s; extracting recovered Spring facts: %s", f.path, d.Message)
		}
		if !*dryRun {
			persistStart := time.Now()
			if err := replaceSpringFacts(context.Background(), pool, f.id, result, f.hash); err != nil {
				log.Fatalf("Refresh %s: %v", f.path, err)
			}
			persistDuration += time.Since(persistStart)
		}

		// Debug: show classes with @Configuration annotation
		if *verbose && debugCount < 20 {
			for _, class := range result.Classes {
				for _, ann := range class.Annotations {
					if ann.Name == "Configuration" {
						fmt.Printf("  Found @Configuration: %s (methods: %d, annotations: %v)\n",
							class.Name, len(class.Methods), class.Annotations)
						debugCount++
						// Show methods and their annotations
						for _, m := range class.Methods {
							if len(m.Annotations) > 0 {
								fmt.Printf("    Method %s: %v\n", m.Name, m.Annotations)
							}
						}
					}
				}
			}
		}

		hasData := len(result.BeanDefinitions) > 0 ||
			len(result.EventListeners) > 0 ||
			len(result.ScheduledMethods) > 0

		if hasData {
			if *verbose {
				fmt.Printf("  %s/%s: %d beans, %d listeners, %d scheduled\n",
					f.repoName, f.path,
					len(result.BeanDefinitions),
					len(result.EventListeners),
					len(result.ScheduledMethods))
			}

			beansExtracted += len(result.BeanDefinitions)
			listenersExtracted += len(result.EventListeners)
			scheduledExtracted += len(result.ScheduledMethods)

			filesProcessed++
		}
	}
	fmt.Printf("Enrichment phases: read=%.2fs parse=%.2fs persist=%.2fs\n", readDuration.Seconds(), parseDuration.Seconds(), persistDuration.Seconds())

	elapsed := time.Since(startTime)

	fmt.Println("--------------------------------------------------")
	fmt.Printf("Extraction completed in %.2fs\n", elapsed.Seconds())
	fmt.Printf("Files with data:    %d\n", filesProcessed)
	fmt.Printf("Files skipped after parse failure (not refreshed): %d\n", filesFailed)
	fmt.Printf("@Bean definitions:  %d\n", beansExtracted)
	fmt.Printf("@EventListeners:    %d\n", listenersExtracted)
	fmt.Printf("@Scheduled methods: %d\n", scheduledExtracted)

	if *dryRun {
		fmt.Println("\n(Dry run - no data was inserted)")
	}
}

// Replace all facts for a successfully parsed file, including an empty result.
func replaceSpringFacts(ctx context.Context, connection *pgxpool.Pool, fileID int64, result parser.ParsedFile, expectedHash string) error {
	pool, err := connection.Begin(ctx)
	if err != nil {
		return err
	}
	defer pool.Rollback(ctx)
	var lockedID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM files WHERE id=$1 AND hash=$2 FOR UPDATE`, fileID, expectedHash).Scan(&lockedID); err != nil {
		return err
	}
	classes, err := graph.LoadClassDeclarations(ctx, pool, fileID)
	if err != nil {
		return err
	}
	classIDMap := make(map[string]int64, len(result.Classes))
	for _, cls := range result.Classes {
		id, err := classes.Match(cls.Name, cls.StartLine, cls.EndLine)
		if err != nil {
			return err
		}
		classIDMap[cls.Name] = id
	}
	classIDs := classes.IDs
	if len(classIDs) > 0 {
		if _, err := pool.Exec(ctx, `DELETE FROM bean_definitions WHERE config_class_id = ANY($1)`, classIDs); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM event_listeners WHERE class_id = ANY($1)`, classIDs); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM scheduled_methods WHERE class_id = ANY($1)`, classIDs); err != nil {
			return err
		}
	}

	// Insert bean definitions
	for _, bean := range result.BeanDefinitions {
		classID, ok := classIDMap[bean.ConfigClassName]
		if !ok {
			return fmt.Errorf("class for bean at line %d is missing", bean.LineNumber)
		}

		var methodID *int64
		if err := pool.QueryRow(ctx, `SELECT CASE
 WHEN COUNT(*) FILTER (WHERE start_line <= $3 AND end_line >= $3) = 1
 THEN MIN(id) FILTER (WHERE start_line <= $3 AND end_line >= $3)
 WHEN COUNT(*) = 1 THEN MIN(id) END
 FROM functions WHERE file_id=$1 AND name=$2`, fileID, bean.ConfigClassName+"."+bean.MethodName, bean.LineNumber).Scan(&methodID); err != nil {
			return err
		}

		var qualifiersJSON []byte
		if len(bean.Qualifiers) > 0 {
			qualifiersJSON, _ = json.Marshal(bean.Qualifiers)
		}

		if _, err := pool.Exec(ctx, `
						INSERT INTO bean_definitions (config_class_id, bean_name, bean_type, qualifiers, is_primary, line_number, method_id)
						VALUES ($1, $2, $3, $4, $5, $6, $7)
					`, classID, bean.BeanName, bean.BeanType, qualifiersJSON, bean.IsPrimary, bean.LineNumber, methodID); err != nil {
			return err
		}

	}

	// Insert event listeners
	for _, listener := range result.EventListeners {
		classID, ok := classIDMap[listener.ClassName]
		if !ok {
			return fmt.Errorf("class for listener at line %d is missing", listener.LineNumber)
		}

		var methodID *int64
		if err := pool.QueryRow(ctx, `SELECT CASE
 WHEN COUNT(*) FILTER (WHERE start_line <= $3 AND end_line >= $3) = 1
 THEN MIN(id) FILTER (WHERE start_line <= $3 AND end_line >= $3)
 WHEN COUNT(*) = 1 THEN MIN(id) END
 FROM functions WHERE file_id=$1 AND name=$2`, fileID, listener.ClassName+"."+listener.MethodName, listener.LineNumber).Scan(&methodID); err != nil {
			return err
		}

		var eventTypesJSON []byte
		if len(listener.EventTypes) > 0 {
			eventTypesJSON, _ = json.Marshal(listener.EventTypes)
		}

		if _, err := pool.Exec(ctx, `
						INSERT INTO event_listeners (class_id, method_name, event_types, condition, is_async, line_number, method_id)
						VALUES ($1, $2, $3, $4, $5, $6, $7)
					`, classID, listener.MethodName, eventTypesJSON, listener.Condition, listener.IsAsync, listener.LineNumber, methodID); err != nil {
			return err
		}

	}

	// Insert scheduled methods
	for _, sched := range result.ScheduledMethods {
		classID, ok := classIDMap[sched.ClassName]
		if !ok {
			return fmt.Errorf("class for sched at line %d is missing", sched.LineNumber)
		}

		var methodID *int64
		if err := pool.QueryRow(ctx, `SELECT CASE
 WHEN COUNT(*) FILTER (WHERE start_line <= $3 AND end_line >= $3) = 1
 THEN MIN(id) FILTER (WHERE start_line <= $3 AND end_line >= $3)
 WHEN COUNT(*) = 1 THEN MIN(id) END
 FROM functions WHERE file_id=$1 AND name=$2`, fileID, sched.ClassName+"."+sched.MethodName, sched.LineNumber).Scan(&methodID); err != nil {
			return err
		}

		var cronPtr interface{}
		var fixedRatePtr, fixedDelayPtr, initialDelayPtr interface{}
		if sched.Cron != "" {
			cronPtr = sched.Cron
		}
		if sched.FixedRate > 0 {
			fixedRatePtr = sched.FixedRate
		}
		if sched.FixedDelay > 0 {
			fixedDelayPtr = sched.FixedDelay
		}
		if sched.InitialDelay > 0 {
			initialDelayPtr = sched.InitialDelay
		}

		if _, err := pool.Exec(ctx, `
						INSERT INTO scheduled_methods (class_id, method_name, cron, fixed_rate, fixed_delay, initial_delay, line_number, method_id)
						VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
					`, classID, sched.MethodName, cronPtr, fixedRatePtr, fixedDelayPtr, initialDelayPtr, sched.LineNumber, methodID); err != nil {
			return err
		}

	}
	return pool.Commit(ctx)
}
