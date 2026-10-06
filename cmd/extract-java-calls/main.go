package main

import (
	"context"
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

func main() {

	defaultDB := os.Getenv("DATABASE_URL")

	dbURL := flag.String("db", "", "PostgreSQL connection string (required; defaults to DATABASE_URL)")
	workspaceSlug := flag.String("workspace", "default-main", "Tirion workspace slug to scope indexed files")
	repoFilter := flag.String("repo", "", "Optional repository name to limit extraction")
	verbose := flag.Bool("v", false, "Verbose output")
	dryRun := flag.Bool("dry-run", false, "Don't update, just show what would be extracted")
	candidateID := flag.Int64("candidate", 0, "Internal unpublished snapshot ID")
	flag.Parse()
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "extract-java-calls: unexpected argument(s) %q; this command takes only flags\n", flag.Args())
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
		if err := indexer.RunPostParseExtractors(indexer.PostParseOptions{DBURL: *dbURL, Workspace: *workspaceSlug, Repo: *repoFilter, RawList: "java-calls", Verbose: *verbose}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	fmt.Println("Re-extracting Java function calls with receiver names...")
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

	// Query all Java files
	rows, err := pool.Query(context.Background(), `
		SELECT f.id, f.path, f.snapshot_id, r.id, r.name, COALESCE(NULLIF(rs.source_path,''), NULLIF(wr.worktree_path, ''), r.path) as repo_path, COALESCE(f.hash, ''), rs.sha
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
		hash       string
		commit     string
		id         int64
		path       string
		snapshotID *int64
		repoID     int64
		repoName   string
		repoPath   string
	}

	var files []fileInfo
	for rows.Next() {
		var f fileInfo
		if err := rows.Scan(&f.id, &f.path, &f.snapshotID, &f.repoID, &f.repoName, &f.repoPath, &f.hash, &f.commit); err != nil {
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
	var filesProcessed, callsUpdated int
	var filesFailed, filesPartial int

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
				log.Fatalf("Parse %s: %s: %s", fullPath, d.FailureKind, d.Message)
			}
			if !d.Usable() {
				filesFailed++
				log.Printf("Skipping %s after parse failure [%s]; its call facts are not refreshed: %s", fullPath, d.FailureKind, d.Message)
				continue
			}
			filesPartial++
			log.Printf("Partial syntax tree in %s; extracting recovered call facts: %s", fullPath, d.Message)
		}

		// Count calls
		numCalls := 0
		for _, calls := range result.FunctionCalls {
			numCalls += len(calls)
		}

		if *verbose {
			fmt.Printf("  %s/%s: %d function calls\n", f.repoName, f.path, numCalls)
		}
		if !*dryRun {
			persistStart := time.Now()
			if err := replaceJavaCalls(context.Background(), pool, f.repoID, f.snapshotID, f.repoName, f.path, result, f.hash); err != nil {
				log.Fatalf("Refresh %s/%s: %v", f.repoName, f.path, err)
			}
			callsUpdated += numCalls
			persistDuration += time.Since(persistStart)
		}
		filesProcessed++
	}
	fmt.Printf("Enrichment phases: read=%.2fs parse=%.2fs persist=%.2fs\n", readDuration.Seconds(), parseDuration.Seconds(), persistDuration.Seconds())

	elapsed := time.Since(startTime)
	fmt.Printf("Files with partial syntax trees (recovered facts used): %d\n", filesPartial)
	fmt.Printf("Files skipped after parse failure (not refreshed): %d\n", filesFailed)

	fmt.Println("--------------------------------------------------")
	fmt.Printf("Extraction completed in %.2fs\n", elapsed.Seconds())
	fmt.Printf("Files processed:   %d\n", filesProcessed)
	fmt.Printf("Calls updated:     %d\n", callsUpdated)

	if *dryRun {
		fmt.Println("\n(Dry run - no data was updated)")
	}
}

func replaceJavaCalls(ctx context.Context, pool *pgxpool.Pool, repoID int64, snapshotID *int64, repoName, filePath string, result parser.ParsedFile, expectedHash string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := graph.LockIndexedFile(ctx, tx, repoID, snapshotID, filePath, expectedHash); err != nil {
		return fmt.Errorf("indexed file changed before enrichment: %w", err)
	}

	// Replace even an empty result, so removed calls do not survive a refresh.
	prefix := repoName + ":" + filePath + ":"
	if _, err := tx.Exec(ctx, `DELETE FROM pending_calls_edges
		WHERE repo_id = $1 AND snapshot_id IS NOT DISTINCT FROM $2
		AND caller_id LIKE $3 ESCAPE '\'`, pgx.QueryExecModeExec, repoID, snapshotID, graph.LiteralPrefixPattern(prefix)); err != nil {
		return err
	}
	// Insert a bounded set of rows per statement. Sending individual INSERTs in
	// a protocol batch still makes PostgreSQL plan/execute one statement per call.
	var callers, callees []string
	var lines []int
	flush := func() error {
		if len(callers) == 0 {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO pending_calls_edges
			(repo_id,snapshot_id,caller_id,callee_name,line_number)
			SELECT $1,$2,c.caller,c.callee,c.line
			FROM unnest($3::text[],$4::text[],$5::integer[]) AS c(caller,callee,line)
			ON CONFLICT DO NOTHING`, repoID, snapshotID, callers, callees, lines)
		callers, callees, lines = callers[:0], callees[:0], lines[:0]
		return err
	}
	for method, calls := range result.FunctionCalls {
		for _, call := range calls {
			callers = append(callers, prefix+method)
			callees = append(callees, call.CalleeName)
			lines = append(lines, call.LineNumber)
			if len(callers) == 1000 {
				if err := flush(); err != nil {
					return err
				}
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
