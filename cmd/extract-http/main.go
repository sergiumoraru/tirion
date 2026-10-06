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

// Refreshes HTTP client calls for files in an indexed workspace.

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
		fmt.Fprintf(os.Stderr, "extract-http: unexpected argument(s) %q; this command takes only flags\n", flag.Args())
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
		if err := indexer.RunPostParseExtractors(indexer.PostParseOptions{DBURL: *dbURL, Workspace: *workspaceSlug, Repo: *repoFilter, RawList: "http", Verbose: *verbose}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if *dbURL == "" {
		log.Fatal("database is required: set DATABASE_URL or pass -db; .env files are not loaded automatically (see SETUP.md#configuration-reference)")
	}

	pool, err := pgxpool.New(context.Background(), *dbURL)
	if err != nil {
		fmt.Printf("Error connecting: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Get all indexed files with the workspace worktree path that produced the
	// active snapshot. The logical repository path may point at another branch.
	query := `SELECT r.id, r.name, COALESCE(NULLIF(rs.source_path,''), NULLIF(wr.worktree_path, ''), r.path), f.path as file_path, f.language, f.snapshot_id, COALESCE(f.hash, ''), rs.sha
		FROM files f
		JOIN repo_snapshots rs ON rs.id = f.snapshot_id
	          JOIN repositories r ON f.repo_id = r.id
	          JOIN workspaces w ON w.slug = $1
 LEFT JOIN workspace_repos wr ON wr.repo_name=r.name AND wr.workspace_id=w.id
	          WHERE f.language IN ('javascript', 'typescript', 'tsx', 'jsx', 'vue', 'java')
	            AND w.slug = $1 AND (($3::bigint=0 AND wr.active_snapshot_id=f.snapshot_id) OR ($3>0 AND rs.id=$3 AND rs.workspace_id=w.id AND rs.status IN ('building','parsed','enriched')))
	            AND ($2 = '' OR r.name = $2)`

	rows, err := pool.Query(context.Background(), query, strings.TrimSpace(*workspaceSlug), strings.TrimSpace(*repoFilter), *candidateID)
	if err != nil {
		fmt.Printf("Error querying files: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	jsParser := parser.NewJavaScriptParser().ForEnrichment()
	javaParser := parser.NewJavaParser().ForEnrichment()

	var totalCalls int
	var filesProcessed int
	var filesFailed, filesPartial int

	type fileInfo struct {
		hash       string
		commit     string
		repoID     int64
		repoName   string
		repoPath   string
		filePath   string
		language   string
		snapshotID *int64
	}

	var files []fileInfo
	for rows.Next() {
		var f fileInfo
		var lang *string
		if err := rows.Scan(&f.repoID, &f.repoName, &f.repoPath, &f.filePath, &lang, &f.snapshotID, &f.hash, &f.commit); err != nil {
			log.Fatalf("Read indexed file: %v", err)
		}
		if lang != nil {
			f.language = *lang
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("Read indexed files: %v", err)
	}

	fmt.Printf("Found %d files to scan for HTTP calls\n", len(files))

	var readDuration, parseDuration, persistDuration time.Duration
	for _, f := range files {
		fullPath := filepath.Join(f.repoPath, f.filePath)
		readStart := time.Now()
		content, err := sourceindex.Read(context.Background(), f.repoPath, f.filePath, f.hash, f.commit)
		readDuration += time.Since(readStart)
		if err != nil {
			log.Fatalf("Read %s: %v", fullPath, err)
		}
		content, _ = sourceindex.NormalizeText(content)
		if parser.LooksMinifiedOrVendor(fullPath, content) {
			continue
		}

		parseStart := time.Now()
		var result parser.ParsedFile
		switch f.language {
		case "javascript", "typescript", "tsx", "jsx", "vue":
			result = jsParser.ParseFile(fullPath, content)
		case "java":
			result = javaParser.ParseFile(fullPath, content)
		default:
			continue
		}
		parseDuration += time.Since(parseStart)
		if d := result.ParseDiagnostics; d.Failed() {
			if d.Systemic() {
				log.Fatalf("Parse %s: %s: %s", fullPath, d.FailureKind, d.Message)
			}
			if !d.Usable() {
				// A timeout or recovered panic is specific to this file: record it,
				// leave its previously indexed facts untouched and continue.
				filesFailed++
				log.Printf("Skipping %s after parse failure [%s]; its HTTP facts are not refreshed: %s", fullPath, d.FailureKind, d.Message)
				continue
			}
			// cmd/parse stores the facts recovered from a partial tree; do the same.
			filesPartial++
			log.Printf("Partial syntax tree in %s; extracting recovered HTTP facts: %s", fullPath, d.Message)
		}

		if !*dryRun {
			persistStart := time.Now()
			if err := replaceHTTPCalls(context.Background(), pool, f.repoID, f.snapshotID, f.repoName, f.filePath, result, f.hash); err != nil {
				log.Fatalf("Refresh %s/%s: %v", f.repoName, f.filePath, err)
			}
			persistDuration += time.Since(persistStart)
		}

		// Report only after the complete file has been persisted.
		for funcName, calls := range result.HttpCalls {
			for _, call := range calls {
				callerID := fmt.Sprintf("%s:%s:%s", f.repoName, f.filePath, funcName)
				if *dryRun {
					if *verbose {
						fmt.Printf("  [dry-run] %s %s -> %s (line %d)\n", call.HttpMethod, call.UrlPattern, callerID, call.LineNumber)
					}
					totalCalls++
				} else {
					totalCalls++
					if *verbose {
						fmt.Printf("  %s %s -> %s\n", call.HttpMethod, call.UrlPattern, callerID)
					}
				}
			}
		}

		filesProcessed++
		if filesProcessed%1000 == 0 {
			fmt.Printf("  Processed %d files, found %d HTTP calls...\n", filesProcessed, totalCalls)
		}
	}
	fmt.Printf("Enrichment phases: read=%.2fs parse=%.2fs persist=%.2fs\n", readDuration.Seconds(), parseDuration.Seconds(), persistDuration.Seconds())

	fmt.Println(strings.Repeat("-", 40))
	fmt.Printf("Files with partial syntax trees (recovered facts used): %d\n", filesPartial)
	fmt.Printf("Files skipped after parse failure (not refreshed): %d\n", filesFailed)
	if *dryRun {
		fmt.Printf("Dry run complete. Would extract %d HTTP client calls from %d files\n", totalCalls, filesProcessed)
	} else {
		fmt.Printf("Done. Extracted %d HTTP client calls from %d files\n", totalCalls, filesProcessed)
	}
}

func replaceHTTPCalls(ctx context.Context, pool *pgxpool.Pool, repoID int64, snapshotID *int64, repoName, filePath string, result parser.ParsedFile, expectedHash string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := graph.LockIndexedFile(ctx, tx, repoID, snapshotID, filePath, expectedHash); err != nil {
		return fmt.Errorf("indexed file changed before enrichment: %w", err)
	}

	// Prefix comparison is literal: underscores and percent signs in paths are not SQL wildcards.
	prefix := repoName + ":" + filePath + ":"
	if _, err := tx.Exec(ctx, `DELETE FROM http_client_calls
		WHERE repo_id = $1 AND snapshot_id IS NOT DISTINCT FROM $2
		AND caller_id LIKE $3 ESCAPE '\'`, pgx.QueryExecModeExec, repoID, snapshotID, graph.LiteralPrefixPattern(prefix)); err != nil {
		return err
	}
	for funcName, calls := range result.HttpCalls {
		callerID := prefix + funcName
		for _, call := range calls {
			if _, err := tx.Exec(ctx, `INSERT INTO http_client_calls
				(repo_id, snapshot_id, caller_id, http_method, url_pattern, line_number, client_type)
				VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING`,
				repoID, snapshotID, callerID, call.HttpMethod, call.UrlPattern, call.LineNumber, call.ClientType); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}
