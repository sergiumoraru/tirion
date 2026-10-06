package indexer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sergiumoraru/tirion/internal/config"
	"github.com/sergiumoraru/tirion/internal/graph"
	"github.com/sergiumoraru/tirion/internal/sourceindex"
	"golang.org/x/sync/errgroup"
)

// PipelineOptions covers one publication, regardless of which CLI/API requested it.
type PipelineOptions struct {
	Context                                                        context.Context
	DBURL, Workspace, ParseBinary, Extractors                      string
	Repos                                                          []RepoEntry
	SkipTests, SkipUnchanged, SkipExtractors, Resolve, Incremental bool
	Verbose                                                        bool
	EnrichmentOnly                                                 bool
	// AllowFileFailures publishes even when parse lost files to timeouts or
	// recovered parser panics; such snapshots are never reused by skip-unchanged.
	AllowFileFailures bool
	timings           *indexTimings
}

type candidate struct {
	ID, RepoID              int64
	Name, Path, SHA, Branch string
	Clean                   bool
}

func pipelineFingerprint(opts PipelineOptions) (string, error) {
	cfg := config.GetEffectivePatterns()
	if err := cfg.Err(); err != nil {
		return "", err
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	hash.Write(data)
	fmt.Fprintf(hash, "\nparse-tests=%t;incremental=%t;resolve=%t;extract=%t;enrichment-only=%t\n", opts.SkipTests, opts.Incremental, opts.Resolve, !opts.SkipExtractors, opts.EnrichmentOnly)
	binaries := []string{}
	if !opts.EnrichmentOnly {
		binaries = append(binaries, opts.ParseBinary)
	}
	if !opts.SkipExtractors {
		helpers, err := selectedPostParseExtractors(opts.Extractors)
		if err != nil {
			return "", err
		}
		for _, helper := range helpers {
			fmt.Fprintf(hash, "helper=%s\n", helper.name)
			path, err := resolveHelperBinary(helper.name)
			if err != nil {
				return "", fmt.Errorf("resolve %s binary: %w", helper.name, err)
			}
			binaries = append(binaries, path)
		}
	}
	for _, path := range binaries {
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(hash, f)
		closeErr := f.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// RunRepositories publishes only after parsing, requested enrichment and resolution
// succeed. Failed attempts never become the workspace's active snapshots.
func RunRepositories(opts PipelineOptions) (BatchResult, error) {
	result := BatchResult{}
	preparationDone := opts.timings.begin("Preparation", "Check helpers/configuration, initialize database, and lock workspace", false)
	defer preparationDone()
	if strings.TrimSpace(opts.Workspace) == "" {
		opts.Workspace = graph.DefaultWorkspaceSlug
	}
	var err error
	if !opts.EnrichmentOnly {
		if opts.ParseBinary == "" {
			opts.ParseBinary = DefaultParseBinary()
		}
		opts.ParseBinary, err = ResolveParseBinary(opts.ParseBinary)
		if err != nil {
			return result, fmt.Errorf("resolve parse binary: %w", err)
		}
	}
	fingerprint, err := pipelineFingerprint(opts)
	if err != nil {
		return result, err
	}
	storageStart := time.Now()
	storage, err := graph.NewStorage(opts.DBURL)
	if err != nil {
		return result, err
	}
	defer storage.Close()
	fmt.Printf("Index database initialization: %.2fs\n", time.Since(storageStart).Seconds())
	ws, err := storage.ResolveWorkspace(opts.Workspace)
	if errors.Is(err, pgx.ErrNoRows) {
		ws, err = missingWorkspace(storage, opts)
	}
	if err != nil {
		return result, err
	}
	opts.Workspace = ws.Slug
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	lock, err := pgx.ConnectConfig(ctx, storage.Pool().Config().ConnConfig.Copy())
	if err != nil {
		return result, err
	}
	defer lock.Close(context.Background())
	// A session lock spans subprocess connections; it does not hide published reads.
	if _, err = lock.Exec(ctx, `SELECT pg_advisory_lock(741926,$1::int)`, ws.ID); err != nil {
		return result, err
	}
	defer lock.Exec(context.Background(), `SELECT pg_advisory_unlock(741926,$1::int)`, ws.ID)
	selections, err := selectionVersions(storage, ws.Slug)
	if err != nil {
		return result, err
	}
	baseline, err := storage.ActiveSnapshotsForWorkspace(ws.Slug)
	if err != nil {
		return result, err
	}
	prior := map[string]graph.WorkspaceSnapshotRef{}
	for _, ref := range baseline {
		prior[ref.RepoName] = ref
	}
	if opts.EnrichmentOnly && len(opts.Repos) == 0 {
		for _, ref := range baseline {
			opts.Repos = append(opts.Repos, RepoEntry{Name: ref.RepoName})
		}
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return result, err
	}
	generation := hex.EncodeToString(nonce[:])
	var candidates []candidate
	// Persist an explicit failed state on ordinary errors; abrupt process death leaves
	// a building generation, still unselected and therefore eligible for retry.
	committed := false
	defer func() {
		if !committed {
			_, _ = storage.Pool().Exec(context.Background(), `UPDATE repo_snapshots SET status='failed' WHERE workspace_id=$1 AND generation=$2 AND status IN ('building','parsed','enriched')`, ws.ID, generation)
		}
	}()
	seen := map[string]bool{}
	for _, repo := range opts.Repos {
		if strings.TrimSpace(repo.Name) == "" || seen[repo.Name] {
			return result, fmt.Errorf("empty or duplicate repository name: %q", repo.Name)
		}
		seen[repo.Name] = true
	}
	build := func(ctx context.Context, repo RepoEntry, timing *repositoryTimings) (candidate, bool, error) {
		var err error
		old, hasOld := prior[repo.Name]
		c := candidate{Name: repo.Name, Path: repo.Path}
		if opts.EnrichmentOnly {
			if !hasOld {
				return c, false, fmt.Errorf("repository %s has no active snapshot", repo.Name)
			}
			c.RepoID = old.RepoID
			c.SHA = old.SHA
			c.Branch = old.Branch
			if err := storage.Pool().QueryRow(ctx, `SELECT COALESCE(NULLIF(rs.source_path,''),NULLIF(wr.worktree_path,''),r.path) FROM repo_snapshots rs JOIN repositories r ON r.id=rs.repo_id JOIN workspace_repos wr ON wr.active_snapshot_id=rs.id AND wr.workspace_id=$2 WHERE rs.id=$1`, old.SnapshotID, ws.ID).Scan(&c.Path); err != nil {
				return c, false, err
			}
		} else {
			c.Path, err = filepath.Abs(c.Path)
			if err != nil {
				return c, false, err
			}
			info, statErr := os.Stat(c.Path)
			if statErr != nil {
				return c, false, statErr
			}
			if !info.IsDir() {
				return c, false, fmt.Errorf("repository path is not a directory: %s", c.Path)
			}
			c.Clean = gitWorktreeClean(c.Path)
			c.SHA = gitHeadSHA(c.Path)
			c.Branch = gitCurrentBranch(c.Path)
			if c.SHA == "" {
				sum := sha256.Sum256([]byte(filepath.Clean(c.Path)))
				c.SHA = "unversioned-" + hex.EncodeToString(sum[:])[:16]
			}
			if c.Branch == "" || c.Branch == "HEAD" || c.Branch == "detached" {
				if wr, e := storage.GetWorkspaceRepo(ws.Slug, c.Name); e == nil {
					c.Branch = firstNonEmpty(wr.TargetRef, wr.ResolvedBranch, c.Branch)
				}
			}
			if opts.SkipUnchanged && hasOld && old.SHA == c.SHA && c.Clean {
				var current bool
				if err := storage.Pool().QueryRow(ctx, `SELECT rs.pipeline_complete AND rs.source_clean AND rs.pipeline_fingerprint=$2
 AND NOT EXISTS(SELECT 1 FROM repo_snapshots attempt WHERE attempt.workspace_id=$3 AND attempt.repo_id=rs.repo_id
   AND attempt.created_at>rs.indexed_at AND attempt.status IN ('building','parsed','enriched','failed'))
 FROM repo_snapshots rs WHERE rs.id=$1`, old.SnapshotID, fingerprint, ws.ID).Scan(&current); err != nil {
					return c, false, err
				}
				// Re-rooting the repositories directory (or repointing a link) changes
				// what would be read even when HEAD is unchanged; the new snapshot
				// then records the new source_path.
				var oldPath string
				if current {
					if err := storage.Pool().QueryRow(ctx, `SELECT COALESCE(source_path,'') FROM repo_snapshots WHERE id=$1`, old.SnapshotID).Scan(&oldPath); err != nil {
						return c, false, err
					}
				}
				if current && sameSourcePath(oldPath, c.Path) && unchangedAuxiliaryInputs(ctx, storage, old.SnapshotID, c.Path) {
					return c, true, nil
				}
			}
			c.RepoID, err = storage.InsertRepositoryWithPathPolicy(c.Name, c.Path, nil, nil, false)
			if err != nil {
				return c, false, err
			}
		}
		err = storage.Pool().QueryRow(ctx, `INSERT INTO repo_snapshots(workspace_id,repo_id,repo_name,branch,sha,generation,source_path,status,pipeline_fingerprint) VALUES($1,$2,$3,$4,$5,$6,$7,'building',$8) RETURNING id`, ws.ID, c.RepoID, c.Name, c.Branch, c.SHA, generation, c.Path, fingerprint).Scan(&c.ID)
		if err != nil {
			return c, false, err
		}
		// Keep raw parse results alive through this candidate's enrichment only.
		// Each helper still verifies source bytes before reading the cache.
		var parseCacheDir string
		if !opts.SkipExtractors {
			if directory, err := os.MkdirTemp("", "tirion-enrichment-"); err == nil {
				parseCacheDir = directory
				defer os.RemoveAll(directory)
			}
		}
		incremental := false
		if opts.Incremental && hasOld && unchangedAuxiliaryInputs(ctx, storage, old.SnapshotID, c.Path) {
			if err := storage.Pool().QueryRow(ctx, `SELECT pipeline_fingerprint=$2 FROM repo_snapshots WHERE id=$1`, old.SnapshotID, fingerprint).Scan(&incremental); err != nil {
				return c, false, err
			}
		}
		if (incremental || opts.EnrichmentOnly) && hasOld {
			copyStart := time.Now()
			copyErr := storage.CopySnapshot(ctx, old.SnapshotID, c.ID)
			timing.copy += time.Since(copyStart)
			if err := copyErr; err != nil {
				return c, false, err
			}
			fmt.Printf("Index candidate copy %s: %.2fs\n", c.Name, time.Since(copyStart).Seconds())
		}
		if !opts.EnrichmentOnly {
			ignored, err := captureIgnoredInputs(ctx, c.Path, c.SHA)
			if err != nil {
				return c, false, err
			}
			if _, err := storage.Pool().Exec(ctx, `UPDATE repo_snapshots SET ignored_manifest=$2 WHERE id=$1`, c.ID, ignored); err != nil {
				return c, false, err
			}
			args := []string{"-workspace", ws.Slug, "-repo-name", c.Name, "-candidate", fmt.Sprint(c.ID)}
			// Full publication resolves all candidate targets after enrichment. The
			// earlier pass is discarded there; partial modes retain their old behavior.
			if opts.Resolve {
				args = append(args, "-defer-resolution")
			}
			if incremental {
				args = append(args, "-incremental")
			}
			if opts.AllowFileFailures {
				args = append(args, "-allow-file-failures")
			}
			args = append(args, c.Path)
			parseCtx, cancel := context.WithTimeout(ctx, helperTimeout)
			command := exec.CommandContext(parseCtx, opts.ParseBinary, args...)
			command.Stdout = os.Stdout
			command.Stderr = os.Stderr
			command.WaitDelay = 10 * time.Second
			command.Env = append(subprocessEnv(opts.DBURL), fmt.Sprintf("PARSE_SKIP_TESTS=%d", boolToInt(opts.SkipTests)), "TIRION_ENRICHMENT_CACHE_DIR="+parseCacheDir)
			parseStart := time.Now()
			runErr := command.Run()
			timing.parse += time.Since(parseStart)
			timedOut := ctx.Err() == nil && parseCtx.Err() == context.DeadlineExceeded
			cancel()
			if err := runErr; err != nil {
				if timedOut {
					return c, false, fmt.Errorf("parse %s timed out after %s and was killed: %w", c.Name, helperTimeout, err)
				}
				return c, false, fmt.Errorf("parse %s: %w", c.Name, withBuildHint(err))
			}
			if head := gitHeadSHA(c.Path); head != "" && head != c.SHA {
				return c, false, fmt.Errorf("repository %s changed revision during indexing", c.Name)
			}
		}
		if _, err := storage.Pool().Exec(ctx, `UPDATE repo_snapshots SET status='parsed' WHERE id=$1`, c.ID); err != nil {
			return c, false, err
		}
		if !opts.SkipExtractors {
			enrichmentStart := time.Now()
			enrichmentErr := runCandidateExtractorsContext(ctx, PostParseOptions{DBURL: opts.DBURL, Workspace: ws.Slug, Repo: c.Name, RawList: opts.Extractors, Verbose: opts.Verbose, CandidateID: c.ID, parseCacheDir: parseCacheDir})
			timing.enrichment += time.Since(enrichmentStart)
			if err := enrichmentErr; err != nil {
				return c, false, err
			}
		}
		if _, err := storage.Pool().Exec(ctx, `UPDATE repo_snapshots SET status='enriched' WHERE id=$1`, c.ID); err != nil {
			return c, false, err
		}
		return c, false, nil
	}
	// Repositories write distinct unpublished snapshots. Keep each repository's
	// parser/enrichment order, and publish only after every worker has succeeded.
	// Bound memory/process pressure and honor a caller's reduced Go CPU budget.
	workers := min(4, runtime.GOMAXPROCS(0))
	preparationDone()
	repositoriesDone := opts.timings.begin("Repository processing", "Check revisions; build candidate snapshots with parsing and enrichment in parallel", false)
	defer repositoriesDone()
	if opts.timings != nil {
		opts.timings.workers = workers
	}
	group, buildCtx := errgroup.WithContext(ctx)
	group.SetLimit(workers)
	type builtRepo struct {
		c                 candidate
		skipped, complete bool
		timing            repositoryTimings
	}
	built := make([]builtRepo, len(opts.Repos))
	for i, repo := range opts.Repos {
		if buildCtx.Err() != nil {
			break
		}
		group.Go(func() error {
			if err := buildCtx.Err(); err != nil {
				return err
			}
			var timing repositoryTimings
			c, skipped, err := build(buildCtx, repo, &timing)
			built[i] = builtRepo{c: c, skipped: skipped, complete: err == nil, timing: timing}
			return err
		})
	}
	buildErr := group.Wait()
	// Results stay in request order regardless of process completion order.
	for _, entry := range built {
		if opts.timings != nil {
			opts.timings.worker.parse += entry.timing.parse
			opts.timings.worker.enrichment += entry.timing.enrichment
			opts.timings.worker.copy += entry.timing.copy
		}
		if !entry.complete {
			continue
		}
		if entry.skipped {
			result.Skipped++
			result.SkippedRepos = append(result.SkippedRepos, entry.c.Name)
		} else {
			candidates = append(candidates, entry.c)
			result.Parsed++
			result.ParsedRepos = append(result.ParsedRepos, entry.c.Name)
		}
	}
	repositoriesDone()
	if buildErr != nil {
		return result, buildErr
	}

	if len(candidates) == 0 {
		committed = true
		return result, nil
	}
	// Fail fast on a concurrent set-ref/fetch/checkout or other selection change
	// instead of after validating every file; publication re-checks under lock.
	if _, err := verifyWorkspaceSelection(ctx, storage.Pool(), ws.ID, selections, baseline, false); err != nil {
		return result, err
	}
	validationStart := time.Now()
	validationDone := opts.timings.begin("Input validation", "Recheck source inputs, revisions, helpers, and configuration before publication", false)
	defer validationDone()
	for _, c := range candidates {
		if !opts.EnrichmentOnly {
			if err := validateCandidateInputs(ctx, storage, c); err != nil {
				return result, err
			}
			if _, err := storage.Pool().Exec(ctx, `UPDATE repo_snapshots SET source_clean=$2 WHERE id=$1`, c.ID, c.Clean && gitWorktreeClean(c.Path)); err != nil {
				return result, err
			}
		}
	}
	finalFingerprint, err := pipelineFingerprint(opts)
	if err != nil {
		return result, err
	}
	if fingerprint != finalFingerprint {
		return result, fmt.Errorf("parser binaries or configuration changed during indexing")
	}
	fmt.Printf("Index input validation: %.2fs\n", time.Since(validationStart).Seconds())
	validationDone()
	complete := !opts.EnrichmentOnly && !opts.SkipExtractors && opts.Resolve && fullExtractorSet(opts.Extractors) && !opts.AllowFileFailures
	publicationStart := time.Now()
	if err := publishCandidates(ctx, storage, ws, baseline, selections, candidates, generation, fingerprint, complete, opts.Resolve, opts.timings); err != nil {
		return result, err
	}
	fmt.Printf("Index publication: %.2fs\n", time.Since(publicationStart).Seconds())
	committed = true
	return result, nil
}

type selectionQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// verifyWorkspaceSelection compares the workspace's current selections with those
// read when indexing began and returns the active snapshot per repository. With
// lock it also row-locks the selections for the rest of the transaction.
func verifyWorkspaceSelection(ctx context.Context, q selectionQuerier, workspaceID int64, selections map[string]time.Time, baseline []graph.WorkspaceSnapshotRef, lock bool) (map[string]int64, error) {
	query := `SELECT repo_name,active_snapshot_id,updated_at FROM workspace_repos WHERE workspace_id=$1 ORDER BY repo_name`
	if lock {
		query += ` FOR UPDATE`
	}
	rows, err := q.Query(ctx, query, workspaceID)
	if err != nil {
		return nil, err
	}
	active := map[string]int64{}
	selectionCount := 0
	for rows.Next() {
		var name string
		var id *int64
		var updated time.Time
		if err := rows.Scan(&name, &id, &updated); err != nil {
			rows.Close()
			return nil, err
		}
		selectionCount++
		if previous, ok := selections[name]; !ok || !previous.Equal(updated) {
			rows.Close()
			return nil, fmt.Errorf("workspace selection changed for %s; retry", name)
		}
		if id != nil {
			active[name] = *id
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if selectionCount != len(selections) {
		return nil, fmt.Errorf("workspace selection changed during indexing; retry")
	}
	expected := map[string]int64{}
	for _, ref := range baseline {
		expected[ref.RepoName] = ref.SnapshotID
	}
	if len(active) != len(expected) {
		return nil, fmt.Errorf("workspace selection changed during indexing; retry")
	}
	for name, id := range expected {
		if active[name] != id {
			return nil, fmt.Errorf("workspace selection changed for %s; retry", name)
		}
	}
	return active, nil
}

// missingWorkspace handles an unknown workspace slug. Only the default workspace
// is created on demand; any other slug must already exist (tirion workspace
// create), so a typo cannot silently mint a workspace and index into it.
func missingWorkspace(storage *graph.Storage, opts PipelineOptions) (*graph.Workspace, error) {
	slug := strings.ToLower(strings.TrimSpace(opts.Workspace))
	if slug == graph.DefaultWorkspaceSlug && !opts.EnrichmentOnly {
		return storage.EnsureWorkspace(slug, slug, "", "", false)
	}
	existing := "none"
	if workspaces, err := storage.ListWorkspaces(); err == nil && len(workspaces) > 0 {
		slugs := make([]string, len(workspaces))
		for i, ws := range workspaces {
			slugs[i] = ws.Slug
		}
		existing = strings.Join(slugs, ", ")
	}
	return nil, fmt.Errorf("workspace %q does not exist (existing workspaces: %s); create it first with `tirion workspace create %s`", opts.Workspace, existing, opts.Workspace)
}

// sameSourcePath reports whether two recorded source directories are the same
// place. Symlinks resolve when the paths still exist; otherwise the cleaned
// strings decide, so a moved or removed directory never compares equal to a
// different one.
func sameSourcePath(recorded, current string) bool {
	if recorded == "" || current == "" {
		return false
	}
	if filepath.Clean(recorded) == filepath.Clean(current) {
		return true
	}
	a, errA := filepath.EvalSymlinks(recorded)
	b, errB := filepath.EvalSymlinks(current)
	return errA == nil && errB == nil && a == b
}

func fullExtractorSet(raw string) bool {
	helpers, err := selectedPostParseExtractors(raw)
	return err == nil && len(helpers) == 6
}

func publishCandidates(ctx context.Context, storage *graph.Storage, ws *graph.Workspace, baseline []graph.WorkspaceSnapshotRef, selections map[string]time.Time, candidates []candidate, generation, fingerprint string, complete, resolve bool, timings *indexTimings, resolutionRoots ...int64) error {
	publicationDone := timings.begin("Publication", "Repair/resolve the graph, refresh Trace, and atomically select snapshots", false)
	defer publicationDone()
	tx, err := storage.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(741927,0)`); err != nil {
		return err
	}
	scoped := storage.WithTransaction(tx)
	// Lock selections and compare IDs to reject a checkout/index selection race.
	active, err := verifyWorkspaceSelection(ctx, tx, ws.ID, selections, baseline, true)
	if err != nil {
		return err
	}
	expected := map[string]int64{}
	for _, ref := range baseline {
		expected[ref.RepoName] = ref.SnapshotID
	}
	changed := map[string]candidate{}
	for _, c := range candidates {
		changed[c.Name] = c
	}
	baselineIDs := make([]int64, 0, len(baseline))
	changedIDs := make([]int64, 0, len(candidates)*2)
	for _, ref := range baseline {
		baselineIDs = append(baselineIDs, ref.SnapshotID)
		if _, ok := changed[ref.RepoName]; ok {
			changedIDs = append(changedIDs, ref.SnapshotID)
		}
	}
	for _, c := range candidates {
		changedIDs = append(changedIDs, c.ID)
	}
	changedIDs = append(changedIDs, resolutionRoots...)
	copyStart := time.Now()
	copyDone := timings.begin("Affected callers", "Discover and copy unchanged callers whose graph relationships need refreshing", true)
	defer copyDone()
	// No old callers need copying when every baseline repository has a candidate.
	// Avoid walking the entire old graph just to discard the resulting closure.
	affected := map[int64]bool{}
	for _, ref := range baseline {
		if _, replaced := changed[ref.RepoName]; !replaced {
			affected, err = scoped.AffectedSnapshots(ctx, baselineIDs, changedIDs, len(candidates) == 0 && len(resolutionRoots) == 0)
			if err != nil {
				return err
			}
			break
		}
	}
	// Published facts and relationships remain immutable for in-flight readers and
	// inherited/mainline selections. Copy affected callers, including their callers.
	for _, ref := range baseline {
		if _, ok := changed[ref.RepoName]; ok {
			continue
		}
		if !affected[ref.SnapshotID] {
			continue
		}
		c := candidate{RepoID: ref.RepoID, Name: ref.RepoName, SHA: ref.SHA, Branch: ref.Branch}
		err := tx.QueryRow(ctx, `INSERT INTO repo_snapshots(workspace_id,repo_id,repo_name,branch,sha,generation,source_path,status,pipeline_complete,pipeline_fingerprint) SELECT $2,repo_id,repo_name,branch,sha,$3,source_path,'enriched',pipeline_complete,pipeline_fingerprint FROM repo_snapshots WHERE id=$1 RETURNING id,source_path`, ref.SnapshotID, ws.ID, generation).Scan(&c.ID, &c.Path)
		if err != nil {
			return err
		}
		copySnapshot := scoped.CopySnapshot
		if resolve {
			copySnapshot = scoped.CopySnapshotForResolution
		}
		if err := copySnapshot(ctx, ref.SnapshotID, c.ID); err != nil {
			return err
		}
		changed[c.Name] = c
		if timings != nil {
			timings.copied++
		}
	}
	fmt.Printf("Index affected-caller discovery/copy: %.2fs\n", time.Since(copyStart).Seconds())
	copyDone()
	names := make([]string, 0, len(changed))
	for name := range changed {
		names = append(names, name)
	}
	sort.Strings(names)
	refs := make([]graph.WorkspaceSnapshotRef, 0, len(active)+len(changed))
	for _, name := range names {
		c := changed[name]
		original := false
		for _, candidate := range candidates {
			if candidate.ID == c.ID {
				original = true
				break
			}
		}
		if original {
			tag, err := tx.Exec(ctx, `UPDATE repo_snapshots SET status='ok',pipeline_complete=$2,pipeline_fingerprint=$3,indexed_at=CURRENT_TIMESTAMP WHERE id=$1 AND status='enriched'`, c.ID, complete, fingerprint)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return fmt.Errorf("candidate %d is not ready to publish", c.ID)
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE repo_snapshots SET status='ok' WHERE id=$1`, c.ID); err != nil {
				return err
			}
		}
		if !original {
			if _, err := tx.Exec(ctx, `UPDATE workspace_repos SET active_snapshot_id=$3,updated_at=CURRENT_TIMESTAMP WHERE workspace_id=$1 AND repo_name=$2`, ws.ID, c.Name, c.ID); err != nil {
				return err
			}
			active[name] = c.ID
			continue
		}
		_, err := tx.Exec(ctx, `INSERT INTO workspace_repos(workspace_id,repo_name,target_ref,resolved_branch,resolved_sha,worktree_path,active_snapshot_id,last_indexed_at,index_status,mainline_snapshot_id,mainline_resolved_sha,mainline_last_indexed_at)
   VALUES($1,$2,$3,$3,$4,$5,$6,CURRENT_TIMESTAMP,'ok',CASE WHEN $3 IN ('main','master') THEN $6::bigint END,CASE WHEN $3 IN ('main','master') THEN $4 ELSE '' END,CASE WHEN $3 IN ('main','master') THEN CURRENT_TIMESTAMP END)
   ON CONFLICT(workspace_id,repo_name) DO UPDATE SET active_snapshot_id=EXCLUDED.active_snapshot_id,resolved_branch=EXCLUDED.resolved_branch,resolved_sha=EXCLUDED.resolved_sha,worktree_path=COALESCE(NULLIF(EXCLUDED.worktree_path,''),workspace_repos.worktree_path),last_indexed_at=EXCLUDED.last_indexed_at,index_status='ok',last_error='',updated_at=CURRENT_TIMESTAMP,
   mainline_snapshot_id=CASE WHEN EXCLUDED.mainline_snapshot_id IS NOT NULL THEN EXCLUDED.mainline_snapshot_id ELSE workspace_repos.mainline_snapshot_id END,
   mainline_resolved_sha=CASE WHEN EXCLUDED.mainline_snapshot_id IS NOT NULL THEN EXCLUDED.mainline_resolved_sha ELSE workspace_repos.mainline_resolved_sha END,
   mainline_last_indexed_at=CASE WHEN EXCLUDED.mainline_snapshot_id IS NOT NULL THEN EXCLUDED.mainline_last_indexed_at ELSE workspace_repos.mainline_last_indexed_at END`, ws.ID, c.Name, c.Branch, c.SHA, c.Path, c.ID)
		if err != nil {
			return err
		}
		active[name] = c.ID
		if ws.Slug == graph.DefaultWorkspaceSlug && original {
			if _, err := tx.Exec(ctx, `UPDATE repositories SET path=$2 WHERE id=$1`, c.RepoID, c.Path); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO repo_workspace_state(repo_id,indexed_branch,indexed_sha,indexed_at) VALUES($1,$2,$3,CURRENT_TIMESTAMP) ON CONFLICT(repo_id) DO UPDATE SET indexed_branch=EXCLUDED.indexed_branch,indexed_sha=EXCLUDED.indexed_sha,indexed_at=EXCLUDED.indexed_at`, c.RepoID, c.Branch, c.SHA); err != nil {
				return err
			}
		}
	}
	for _, ref := range baseline {
		if c, ok := changed[ref.RepoName]; ok {
			ref.SnapshotID = c.ID
		}
		refs = append(refs, ref)
	}
	for _, c := range candidates {
		if _, ok := expected[c.Name]; !ok {
			refs = append(refs, graph.WorkspaceSnapshotRef{RepoID: c.RepoID, RepoName: c.Name, SnapshotID: c.ID, SHA: c.SHA})
		}
	}
	// Even partial extraction modes need incoming identity repair when IDs change.
	// Their freshness remains partial; resolution here protects existing graph paths.
	resolutionStart := time.Now()
	if err := resolveSelectedSnapshots(ctx, scoped, refs, changed, resolve, timings); err != nil {
		return err
	}
	fmt.Printf("Index publication resolution/Trace: %.2fs\n", time.Since(resolutionStart).Seconds())
	return tx.Commit(ctx)
}

// RunSingleRepository is the shared CLI/API entry point for repository indexing.
func RunSingleRepository(dbURL, workspace, name, path string, resolve bool) error {
	return RunSingleRepositoryContext(context.Background(), dbURL, workspace, name, path, resolve)
}

func RunSingleRepositoryContext(ctx context.Context, dbURL, workspace, name, path string, resolve bool) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	_, err := RunRepositories(PipelineOptions{Context: ctx, DBURL: dbURL, Workspace: workspace, Repos: []RepoEntry{{Name: name, Path: path}}, SkipTests: true, Resolve: resolve})
	return err
}

func unchangedAuxiliaryInputs(ctx context.Context, storage *graph.Storage, snapshotID int64, path string) bool {
	var metadata, includes, ignored map[string]string
	var revision string
	if err := storage.Pool().QueryRow(ctx, `SELECT input_manifest,include_manifest,ignored_manifest,sha FROM repo_snapshots WHERE id=$1`, snapshotID).Scan(&metadata, &includes, &ignored, &revision); err != nil {
		return false
	}
	if metadata == nil || includes == nil || ignored == nil || sourceindex.CheckInputs(path, metadata) != nil || sourceindex.CheckIncludes(includes) != nil {
		return false
	}
	current, err := captureIgnoredInputs(ctx, path, revision)
	return err == nil && reflect.DeepEqual(current, ignored)
}

func validateCandidateInputs(ctx context.Context, storage *graph.Storage, c candidate) error {
	if head := gitHeadSHA(c.Path); head != "" && head != c.SHA {
		return fmt.Errorf("repository %s changed revision during indexing", c.Name)
	}
	if !unchangedAuxiliaryInputs(ctx, storage, c.ID, c.Path) {
		return fmt.Errorf("repository %s metadata, includes or ignored source changed during indexing", c.Name)
	}
	rows, err := storage.Pool().Query(ctx, `SELECT path,COALESCE(hash,'') FROM files WHERE snapshot_id=$1`, c.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var path, expected string
		if err := rows.Scan(&path, &expected); err != nil {
			return err
		}
		data, err := sourceindex.ReadCurrent(c.Path, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != expected {
			return fmt.Errorf("source changed during indexing: %s/%s", c.Name, path)
		}
	}
	return rows.Err()
}

func selectionVersions(storage *graph.Storage, workspace string) (map[string]time.Time, error) {
	repos, err := storage.ListWorkspaceRepos(workspace)
	if err != nil {
		return nil, err
	}
	versions := make(map[string]time.Time, len(repos))
	for _, repo := range repos {
		versions[repo.RepoName] = repo.UpdatedAt
	}
	return versions, nil
}
