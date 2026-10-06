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

// Extracts SQS producer/consumer calls from already-indexed Java files
// Populates sqs_producers and sqs_consumers tables without re-parsing repos

func refreshQueueFile(pool *pgxpool.Pool, repoID int64, snapshotID *int64, repoName, filePath string, result parser.ParsedFile, expectedHash string) error {
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := graph.LockIndexedFile(ctx, tx, repoID, snapshotID, filePath, expectedHash); err != nil {
		return err
	}
	prefix := repoName + ":" + filePath + ":"
	if _, err := tx.Exec(ctx, `DELETE FROM sqs_producers WHERE repo_id = $1
		AND snapshot_id IS NOT DISTINCT FROM $2 AND caller_id LIKE $3 ESCAPE '\'`, pgx.QueryExecModeExec, repoID, snapshotID, graph.LiteralPrefixPattern(prefix)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sqs_consumers WHERE repo_id = $1
		AND snapshot_id IS NOT DISTINCT FROM $2 AND consumer_id LIKE $3 ESCAPE '\'`, pgx.QueryExecModeExec, repoID, snapshotID, graph.LiteralPrefixPattern(prefix)); err != nil {
		return err
	}
	for name, producers := range result.SqsProducers {
		for _, producer := range producers {
			if _, err := tx.Exec(ctx, `INSERT INTO sqs_producers (repo_id, snapshot_id, caller_id, queue_name, line_number)
				VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, repoID, snapshotID, prefix+name, producer.QueueName, producer.LineNumber); err != nil {
				return err
			}
		}
	}
	for _, consumer := range result.SqsConsumers {
		if _, err := tx.Exec(ctx, `INSERT INTO sqs_consumers (repo_id, snapshot_id, consumer_id, queue_name, handler_method)
			VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, repoID, snapshotID, prefix+consumer.ClassName, consumer.QueueName, consumer.HandlerMethod); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
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
		fmt.Fprintf(os.Stderr, "extract-sqs: unexpected argument(s) %q; this command takes only flags\n", flag.Args())
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
		if err := indexer.RunPostParseExtractors(indexer.PostParseOptions{DBURL: *dbURL, Workspace: *workspaceSlug, Repo: *repoFilter, RawList: "sqs", Verbose: *verbose}); err != nil {
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

	// Ensure tables exist
	if os.Getenv("CODEBASE_SKIP_SCHEMA_INIT") != "1" {
		_, err = pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS sqs_producers (
			id SERIAL PRIMARY KEY,
			repo_id INTEGER NOT NULL,
			snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE,
			caller_id TEXT NOT NULL,
			queue_name TEXT NOT NULL,
			line_number INTEGER
		);
		CREATE TABLE IF NOT EXISTS sqs_consumers (
			id SERIAL PRIMARY KEY,
			repo_id INTEGER NOT NULL,
			snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE,
			consumer_id TEXT NOT NULL,
			queue_name TEXT NOT NULL,
			handler_method TEXT
		);
		ALTER TABLE sqs_producers ADD COLUMN IF NOT EXISTS snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE;
		ALTER TABLE sqs_consumers ADD COLUMN IF NOT EXISTS snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE;
		ALTER TABLE sqs_producers DROP CONSTRAINT IF EXISTS sqs_producers_caller_id_queue_name_line_number_key;
		ALTER TABLE sqs_consumers DROP CONSTRAINT IF EXISTS sqs_consumers_consumer_id_queue_name_key;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_sqs_producers_legacy_unique
		ON sqs_producers(caller_id, queue_name, line_number)
		WHERE snapshot_id IS NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_sqs_producers_snapshot_unique
		ON sqs_producers(snapshot_id, caller_id, queue_name, line_number)
		WHERE snapshot_id IS NOT NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_sqs_consumers_legacy_unique
		ON sqs_consumers(consumer_id, queue_name)
		WHERE snapshot_id IS NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_sqs_consumers_snapshot_unique
		ON sqs_consumers(snapshot_id, consumer_id, queue_name)
		WHERE snapshot_id IS NOT NULL;
		CREATE INDEX IF NOT EXISTS idx_sqs_producers_repo ON sqs_producers(repo_id);
		CREATE INDEX IF NOT EXISTS idx_sqs_consumers_repo ON sqs_consumers(repo_id);
		CREATE INDEX IF NOT EXISTS idx_sqs_producers_queue ON sqs_producers(queue_name);
		CREATE INDEX IF NOT EXISTS idx_sqs_consumers_queue ON sqs_consumers(queue_name);
	`)
		if err != nil {
			fmt.Printf("Error creating tables: %v\n", err)
			os.Exit(1)
		}
	}

	// Get all Java files from the workspace worktree that produced the active snapshot.
	query := `SELECT r.id, r.name, COALESCE(NULLIF(rs.source_path,''), NULLIF(wr.worktree_path, ''), r.path), f.path as file_path, f.snapshot_id, COALESCE(f.hash, ''), rs.sha
		FROM files f
		JOIN repo_snapshots rs ON rs.id = f.snapshot_id
	          JOIN repositories r ON f.repo_id = r.id
	          JOIN workspaces w ON w.slug = $1
 LEFT JOIN workspace_repos wr ON wr.repo_name=r.name AND wr.workspace_id=w.id
	          WHERE f.language = 'java'
	            AND w.slug = $1 AND (($3::bigint=0 AND wr.active_snapshot_id=f.snapshot_id) OR ($3>0 AND rs.id=$3 AND rs.workspace_id=w.id AND rs.status IN ('building','parsed','enriched')))
	            AND ($2 = '' OR r.name = $2)`

	rows, err := pool.Query(context.Background(), query, strings.TrimSpace(*workspaceSlug), strings.TrimSpace(*repoFilter), *candidateID)
	if err != nil {
		fmt.Printf("Error querying files: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	javaParser := parser.NewJavaParser().ForEnrichment()
	framework := config.GetEffectivePatterns().JavaSQS
	propertyMaps := make(map[string]map[string]string)

	var totalProducers, totalConsumers int
	var filesProcessed, filesFailed int

	type fileInfo struct {
		hash       string
		commit     string
		repoID     int64
		repoName   string
		repoPath   string
		filePath   string
		snapshotID *int64
	}

	var files []fileInfo
	for rows.Next() {
		var f fileInfo
		if err := rows.Scan(&f.repoID, &f.repoName, &f.repoPath, &f.filePath, &f.snapshotID, &f.hash, &f.commit); err != nil {
			fmt.Fprintf(os.Stderr, "Error reading indexed file: %v\n", err)
			os.Exit(1)
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading indexed files: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Found %d Java files to scan for SQS calls\n", len(files))

	var readDuration, parseDuration, persistDuration time.Duration
	for _, f := range files {
		fullPath := filepath.Join(f.repoPath, f.filePath)
		readStart := time.Now()
		content, err := sourceindex.Read(context.Background(), f.repoPath, f.filePath, f.hash, f.commit)
		readDuration += time.Since(readStart)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Read indexed source %s: %v\n", fullPath, err)
			os.Exit(1)
		}
		content, _ = sourceindex.NormalizeText(content)

		parseStart := time.Now()
		result := javaParser.ParseFile(fullPath, content)
		parseDuration += time.Since(parseStart)

		if d := result.ParseDiagnostics; d.Failed() {
			if d.Systemic() {
				fmt.Fprintf(os.Stderr, "Queue extraction failed for %s: %s\n", fullPath, d.Message)
				os.Exit(1)
			}
			if !d.Usable() {
				filesFailed++
				fmt.Fprintf(os.Stderr, "Skipping %s after parse failure [%s]; its queue facts are not refreshed: %s\n", fullPath, d.FailureKind, d.Message)
				continue
			}
			fmt.Fprintf(os.Stderr, "Partial syntax tree in %s; extracting recovered queue facts: %s\n", fullPath, d.Message)
		}
		if parser.HasPropertyQueueConsumer(result, framework) {
			properties, loaded := propertyMaps[f.repoPath]
			if !loaded {
				var err error
				var inputs map[string]string
				err = pool.QueryRow(context.Background(), `SELECT input_manifest FROM repo_snapshots WHERE id=$1`, f.snapshotID).Scan(&inputs)
				if err == nil && inputs == nil {
					err = fmt.Errorf("snapshot has no metadata manifest; reindex before queue enrichment")
				}
				if err == nil {
					paths := make([]string, 0, len(inputs))
					for path := range inputs {
						paths = append(paths, path)
					}
					properties, err = parser.LoadJavaQueuePropertiesFrom(paths, func(path string) ([]byte, error) {
						return sourceindex.ReadInput(context.Background(), f.repoPath, path, f.commit, inputs)
					})
				}
				if err != nil {
					fmt.Fprintf(os.Stderr, "Read queue properties for %s: %v\n", f.repoName, err)
					os.Exit(1)
				}
				propertyMaps[f.repoPath] = properties
			}
			result.SqsConsumers = append(result.SqsConsumers, parser.DerivePropertyQueueConsumers(result, properties, framework)...)
		}
		if !*dryRun {
			persistStart := time.Now()
			if err := refreshQueueFile(pool, f.repoID, f.snapshotID, f.repoName, f.filePath, result, f.hash); err != nil {
				fmt.Fprintf(os.Stderr, "Store queue facts for %s: %v\n", fullPath, err)
				os.Exit(1)
			}
			persistDuration += time.Since(persistStart)
		}
		for _, producers := range result.SqsProducers {
			totalProducers += len(producers)
		}
		totalConsumers += len(result.SqsConsumers)
		if *verbose {
			fmt.Printf("  %s: %d consumers\n", f.filePath, len(result.SqsConsumers))
		}
		filesProcessed++
		if filesProcessed%1000 == 0 {
			fmt.Printf("  Processed %d files, found %d producers, %d consumers...\n",
				filesProcessed, totalProducers, totalConsumers)
		}
	}
	fmt.Printf("Enrichment phases: read=%.2fs parse=%.2fs persist=%.2fs\n", readDuration.Seconds(), parseDuration.Seconds(), persistDuration.Seconds())

	fmt.Println(strings.Repeat("-", 40))
	fmt.Printf("Files skipped after parse failure (not refreshed): %d\n", filesFailed)
	if *dryRun {
		fmt.Printf("Dry run complete. Would extract %d SQS producers and %d consumers from %d files\n",
			totalProducers, totalConsumers, filesProcessed)
	} else {
		fmt.Printf("Done. Extracted %d SQS producers and %d consumers from %d files\n",
			totalProducers, totalConsumers, filesProcessed)
	}
}
