package indexer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sergiumoraru/tirion/internal/graph"
	"github.com/sergiumoraru/tirion/internal/runtimeconfig"
)

type BatchOptions struct {
	Context        context.Context
	DBURL          string
	Root           string
	ParseBinary    string
	Workspace      string
	SkipTests      bool
	GlobalResolve  bool
	DryRun         bool
	Verbose        bool
	Exclude        map[string]bool
	SkipUnchanged  bool // skip only a complete matching pipeline
	SkipExtractors bool
	Extractors     string
	// AllowFileFailures: see PipelineOptions.AllowFileFailures.
	AllowFileFailures bool
}

type RepoEntry struct {
	Name string
	Path string
}

type BatchResult struct {
	Parsed       int
	ParsedRepos  []string
	Skipped      int
	SkippedRepos []string
}

type PostParseOptions struct {
	CandidateID   int64
	DBURL         string
	Workspace     string
	Repo          string
	Repos         []string
	RawList       string
	Verbose       bool
	parseCacheDir string
}

type postParseHelper struct {
	name           string
	args           []string
	workspaceAware bool
}

// helperDirectory is where the parse and extract-* executables are installed:
// beside the running tirion executable, never on a repository-controlled PATH.
// Tests replace it.
var helperDirectory = func() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	// os.Executable is not symlink-resolved on every platform (macOS); helpers
	// live beside the real binary, not beside a link to it.
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}
	return filepath.Dir(exePath), nil
}

func DefaultParseBinary() string {
	dir, err := helperDirectory()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, ParseBinaryName())
}

func ParseBinaryName() string {
	if runtime.GOOS == "windows" {
		return "parse.exe"
	}
	return "parse"
}

// ResolveParseBinary validates an explicit binary path (or looks a bare name up
// on PATH). A missing binary gets a hint on how to build and place the
// executables, since that is the usual cause.
func ResolveParseBinary(candidate string) (string, error) {
	if strings.TrimSpace(candidate) == "" {
		return "", errors.New("empty parse binary path")
	}
	if strings.ContainsRune(candidate, os.PathSeparator) || filepath.IsAbs(candidate) {
		abs, err := filepath.Abs(candidate)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return "", withBuildHint(err)
		}
		if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0) {
			return "", fmt.Errorf("parser must be a regular executable: %s", abs)
		}
		return abs, nil
	}
	path, err := exec.LookPath(candidate)
	if err != nil {
		return "", withBuildHint(err)
	}
	return path, nil
}

// withBuildHint annotates missing-executable errors with the remedy.
func withBuildHint(err error) error {
	if err == nil || !(errors.Is(err, fs.ErrNotExist) || errors.Is(err, exec.ErrNotFound)) {
		return err
	}
	return fmt.Errorf("%w (build every command with `bash scripts/build-local.sh`; tirion, parse and the extract-* helpers must sit in the same directory, because helpers are looked up beside the tirion executable and not on PATH; use -parse-bin only to point at a different parse binary)", err)
}

func ParseExcluded(raw string) map[string]bool {
	out := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name != "" {
			out[name] = true
		}
	}
	return out
}

func RunPostParseExtractors(opts PostParseOptions) error {
	if opts.CandidateID > 0 {
		return runCandidateExtractors(opts)
	}
	repos := []RepoEntry{}
	for _, name := range normalizedPostParseRepos(opts) {
		repos = append(repos, RepoEntry{Name: name})
	}
	_, err := RunRepositories(PipelineOptions{DBURL: opts.DBURL, Workspace: opts.Workspace, Repos: repos, Extractors: opts.RawList, EnrichmentOnly: true, Resolve: true, Verbose: opts.Verbose})
	return err
}

func runCandidateExtractors(opts PostParseOptions) error {
	return runCandidateExtractorsContext(context.Background(), opts)
}

func runCandidateExtractorsContext(ctx context.Context, opts PostParseOptions) error {
	helpers, err := selectedPostParseExtractors(opts.RawList)
	if err != nil {
		return err
	}
	cacheEnv := "TIRION_ENRICHMENT_CACHE_DIR=" + opts.parseCacheDir
	if opts.parseCacheDir == "" && opts.CandidateID > 0 && len(helpers) > 1 {
		if directory, err := os.MkdirTemp("", "tirion-enrichment-"); err == nil {
			defer os.RemoveAll(directory)
			cacheEnv += directory
		}
	}
	workspaceSlug := strings.TrimSpace(opts.Workspace)
	if workspaceSlug == "" {
		workspaceSlug = graph.DefaultWorkspaceSlug
	}
	repos := normalizedPostParseRepos(opts)
	for _, helper := range helpers {
		helperPath, err := resolveHelperBinary(helper.name)
		if err != nil {
			return fmt.Errorf("resolve %s binary: %w", helper.name, err)
		}
		if len(repos) == 0 {
			helperArgs := append([]string{}, helper.args...)
			helperArgs = append(helperArgs, "-candidate", fmt.Sprint(opts.CandidateID))
			if helper.workspaceAware {
				helperArgs = append(helperArgs, "-workspace", workspaceSlug)
			}
			if err := runPostParseHelperContext(ctx, helperPath, helperArgs, opts.DBURL, opts.Verbose, cacheEnv); err != nil {
				return fmt.Errorf("%s failed: %w", helper.name, err)
			}
			continue
		}
		for _, repo := range repos {
			helperArgs := append([]string{}, helper.args...)
			helperArgs = append(helperArgs, "-candidate", fmt.Sprint(opts.CandidateID))
			if helper.workspaceAware {
				helperArgs = append(helperArgs, "-workspace", workspaceSlug)
			}
			helperArgs = append(helperArgs, "-repo", repo)
			if err := runPostParseHelperContext(ctx, helperPath, helperArgs, opts.DBURL, opts.Verbose, cacheEnv); err != nil {
				if isUnsupportedRepoFlagError(err) {
					return fmt.Errorf("%s does not support repo-scoped extraction; rebuild the helper binaries before indexing %s: %w", helper.name, repo, err)
				}
				return fmt.Errorf("%s failed for %s: %w", helper.name, repo, err)
			}
		}
	}
	return nil
}

func isUnsupportedRepoFlagError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "flag provided but not defined: -repo")
}

func normalizedPostParseRepos(opts PostParseOptions) []string {
	seen := map[string]bool{}
	var repos []string
	add := func(repo string) {
		repo = strings.TrimSpace(repo)
		if repo == "" || seen[repo] {
			return
		}
		seen[repo] = true
		repos = append(repos, repo)
	}
	add(opts.Repo)
	for _, repo := range opts.Repos {
		add(repo)
	}
	slices.Sort(repos)
	return repos
}

func selectedPostParseExtractors(raw string) ([]postParseHelper, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "http,sqs,java-intel,java-calls,spring,ts-intel"
	}
	available := map[string]postParseHelper{
		"http":       {name: "extract-http", workspaceAware: true},
		"sqs":        {name: "extract-sqs", workspaceAware: true},
		"java-intel": {name: "extract-java-intel", workspaceAware: true},
		"java-calls": {name: "extract-java-calls", workspaceAware: true},
		"spring":     {name: "extract-spring", workspaceAware: true},
		"ts-intel":   {name: "extract-ts-intel", workspaceAware: true},
	}

	var helpers []postParseHelper
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(strings.ToLower(part))
		if name == "" {
			continue
		}
		helper, ok := available[name]
		if !ok {
			keys := make([]string, 0, len(available))
			for key := range available {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			return nil, fmt.Errorf("unknown extractor %q (valid: %s)", name, strings.Join(keys, ", "))
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		helpers = append(helpers, helper)
	}
	return helpers, nil
}

func resolveHelperBinary(name string) (string, error) {
	dir, err := helperDirectory()
	if err != nil {
		return "", err
	}
	return ResolveParseBinary(filepath.Join(dir, binaryName(name)))
}

// validateHelpers resolves every executable a run with these options would start
// (extractor names included), so a missing binary or a typo is reported up front
// and, for -dry-run, without indexing anything. All problems are reported at once.
func validateHelpers(extractors string, skipExtractors bool) ([]string, error) {
	if skipExtractors {
		return nil, nil
	}
	helpers, err := selectedPostParseExtractors(extractors)
	if err != nil {
		return nil, err
	}
	var names []string
	var problems []string
	for _, helper := range helpers {
		if _, err := resolveHelperBinary(helper.name); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", helper.name, err))
			continue
		}
		names = append(names, helper.name)
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("extractor helper binaries unavailable:\n  %s", strings.Join(problems, "\n  "))
	}
	return names, nil
}

// helperTimeout bounds one parse or enrichment helper process so a hung helper
// cannot hold the workspace lock indefinitely.
var helperTimeout = 2 * time.Hour

// helperRetryDelay is the pause before the single retry of a transient failure.
var helperRetryDelay = 2 * time.Second

func runPostParseHelper(path string, args []string, dbURL string, verbose bool) error {
	return runPostParseHelperContext(context.Background(), path, args, dbURL, verbose)
}

// transientHelperMarkers identify failures worth one retry: the helper reached
// for PostgreSQL (or the network) and hit a condition that clears by itself.
// Anything else (usage errors, parse failures, unsupported flags, panics, a
// missing binary, a timeout) fails the same way again, so it is not retried;
// retrying would only repeat the work and delay the real error.
var transientHelperMarkers = []string{
	"connection refused", "connection reset", "broken pipe", "server closed the connection",
	"unexpected eof", "i/o timeout", "the database system is starting up",
	"the database system is shutting down", "too many clients", "deadlock detected",
	"could not serialize access", "sqlstate 40001", "sqlstate 40p01", "sqlstate 57p03", "sqlstate 53300",
	"temporary failure in name resolution",
}

func isTransientHelperFailure(output string) bool {
	lower := strings.ToLower(output)
	for _, marker := range transientHelperMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func runPostParseHelperContext(ctx context.Context, path string, args []string, dbURL string, verbose bool, extraEnv ...string) error {
	if verbose {
		fmt.Printf("Running: %s %s\n", path, strings.Join(args, " "))
	}
	const maxAttempts = 2
	var lastErr error
	var lastOutput string
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		var output bytes.Buffer
		writer := io.MultiWriter(os.Stdout, &output)
		attemptCtx, cancel := context.WithTimeout(ctx, helperTimeout)
		cmd := exec.CommandContext(attemptCtx, path, args...)
		cmd.Stdout = writer
		cmd.Stderr = writer
		// A killed helper's children can keep the output pipes open.
		cmd.WaitDelay = 10 * time.Second
		cmd.Env = append(subprocessEnv(dbURL), extraEnv...)
		helperStart := time.Now()
		runErr := cmd.Run()
		timedOut := attemptCtx.Err() == context.DeadlineExceeded
		cancel()
		if runErr == nil {
			fmt.Printf("Index helper %s: %.2fs\n", filepath.Base(path), time.Since(helperStart).Seconds())
			return nil
		}
		fmt.Printf("Index helper %s failed after %.2fs\n", filepath.Base(path), time.Since(helperStart).Seconds())
		lastErr = runErr
		lastOutput = output.String()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if timedOut {
			lastErr = fmt.Errorf("helper %s timed out after %s and was killed: %w", filepath.Base(path), helperTimeout, runErr)
			break
		}
		var startErr *exec.Error
		if errors.As(runErr, &startErr) || errors.Is(runErr, fs.ErrNotExist) {
			lastErr = withBuildHint(runErr)
			break
		}
		if !isTransientHelperFailure(lastOutput) {
			break
		}
		if attempt < maxAttempts {
			fmt.Fprintf(os.Stderr, "Helper %s failed on attempt %d/%d with a transient error: %v. Retrying...\n", filepath.Base(path), attempt, maxAttempts, lastErr)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(helperRetryDelay):
			}
		}
	}
	if trimmed := strings.TrimSpace(lastOutput); trimmed != "" {
		return fmt.Errorf("%w; helper output tail: %s", lastErr, outputTail(trimmed, 1200))
	}
	return lastErr
}

func subprocessEnv(dbURL string) []string {
	env := runtimeconfig.ChildEnvironment()
	env = append(env, "CODEBASE_SKIP_SCHEMA_INIT=1")
	if dbURL != "" {
		env = append(env, "DATABASE_URL="+dbURL)
	}
	return env
}

func outputTail(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}

func binaryName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// RunBatchDetailed discovers repositories and publishes the requested pipeline
// atomically. The returned names describe the published or skipped repositories.
func RunBatchDetailed(opts BatchOptions) (result BatchResult, runErr error) {
	started := time.Now()
	timings := &indexTimings{}
	workspaceSlug := strings.TrimSpace(opts.Workspace)
	if workspaceSlug == "" {
		workspaceSlug = graph.DefaultWorkspaceSlug
	}
	defer func() { timings.print(time.Since(started), workspaceSlug, result, opts.DryRun, runErr) }()
	discoveryDone := timings.begin("Discovery", "Find repositories and locate the parser binary", false)
	defer discoveryDone()
	rootPath, err := filepath.Abs(opts.Root)
	if err != nil {
		return BatchResult{}, fmt.Errorf("resolve root path: %w", err)
	}
	info, err := os.Stat(rootPath)
	if err != nil {
		return BatchResult{}, fmt.Errorf("access root path: %w", err)
	}
	if !info.IsDir() {
		return BatchResult{}, fmt.Errorf("root path is not a directory: %s", rootPath)
	}

	if strings.TrimSpace(opts.ParseBinary) == "" {
		opts.ParseBinary = DefaultParseBinary()
	}
	parseBinary, err := ResolveParseBinary(opts.ParseBinary)
	if err != nil {
		return BatchResult{}, fmt.Errorf("resolve parse binary: %w", err)
	}

	repos, warnings, err := DiscoverReposWithWarnings(rootPath, opts.Exclude)
	timings.warnings = append(timings.warnings, warnings...)
	if err != nil {
		return BatchResult{}, fmt.Errorf("discover repos: %w", err)
	}
	if len(repos) == 0 {
		return BatchResult{}, fmt.Errorf("no code repositories found under %s", rootPath)
	}
	timings.discovered = len(repos)
	helpers, err := validateHelpers(opts.Extractors, opts.SkipExtractors)
	if err != nil {
		return BatchResult{}, err
	}

	fmt.Printf("Repos root: %s\n", rootPath)
	fmt.Printf("Parse binary: %s\n", parseBinary)
	if len(helpers) > 0 {
		fmt.Printf("Extractor helpers: %s\n", strings.Join(helpers, ", "))
	}
	fmt.Printf("Found %d repositories to parse\n", len(repos))
	fmt.Printf("Mode: skip-tests=%t, workspace-resolve=%s\n", opts.SkipTests, resolveModeLabel(opts.GlobalResolve))
	fmt.Printf("Workspace: %s\n", workspaceSlug)
	if len(opts.Exclude) > 0 {
		fmt.Printf("Excluded: %s\n", strings.Join(sortedKeys(opts.Exclude), ", "))
	}
	fmt.Println("========================================")

	for _, repo := range repos {
		fmt.Printf("- %s (%s)\n", repo.Name, repo.Path)
	}
	discoveryDone()

	if opts.DryRun {
		return BatchResult{}, nil
	}

	return RunRepositories(PipelineOptions{Context: opts.Context, DBURL: opts.DBURL, Workspace: workspaceSlug, ParseBinary: parseBinary, Repos: repos, SkipTests: opts.SkipTests, SkipUnchanged: opts.SkipUnchanged, SkipExtractors: opts.SkipExtractors, Extractors: opts.Extractors, Resolve: opts.GlobalResolve, Verbose: opts.Verbose, AllowFileFailures: opts.AllowFileFailures, timings: timings})
}

// resolveModeLabel describes what -global-resolve selects. With it, publication
// resolves every eligible cross-repository call once all candidates are enriched;
// without it, publication only repairs cross-repository links that already exist.
func resolveModeLabel(full bool) string {
	if full {
		return "full"
	}
	return "repair-only"
}

func repoNames(repos []RepoEntry) []string {
	out := make([]string, 0, len(repos))
	for _, repo := range repos {
		if strings.TrimSpace(repo.Name) != "" {
			out = append(out, repo.Name)
		}
	}
	slices.Sort(out)
	return out
}

func gitHeadSHA(repoPath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-parse", "HEAD")
	cmd.Env = runtimeconfig.GitEnvironment()
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitWorktreeClean(repoPath string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "status", "--porcelain", "--untracked-files=normal")
	cmd.Env = runtimeconfig.GitEnvironment()
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	return err == nil && len(bytes.TrimSpace(out)) == 0
}

func gitCurrentBranch(repoPath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Env = runtimeconfig.GitEnvironment()
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func runParse(parseBinary, dbURL, workspaceSlug, repoName, repoPath string, skipTests, resetAll, verbose bool) error {
	var args []string
	if strings.TrimSpace(workspaceSlug) != "" {
		args = append(args, "-workspace", workspaceSlug)
	}
	if strings.TrimSpace(repoName) != "" {
		args = append(args, "-repo-name", repoName)
	}
	if resetAll {
		args = append(args, "-reset-all")
	}
	args = append(args, repoPath)

	cmd := exec.CommandContext(context.Background(), parseBinary, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(runtimeconfig.ChildEnvironment(),
		fmt.Sprintf("PARSE_SKIP_TESTS=%d", boolToInt(skipTests)),
		"DATABASE_URL="+dbURL,
	)
	if !resetAll {
		cmd.Env = append(cmd.Env, "CODEBASE_SKIP_SCHEMA_INIT=1")
	}

	if verbose {
		fmt.Printf("Running: %s %s\n", parseBinary, strings.Join(args, " "))
	}
	return cmd.Run()
}

func RunGlobalResolve(dbURL string) error {
	storage, err := graph.NewStorage(dbURL)
	if err != nil {
		return err
	}
	defer storage.Close()
	rows, err := storage.Pool().Query(context.Background(), `SELECT slug FROM workspaces ORDER BY id`)
	if err != nil {
		return err
	}
	var slugs []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			rows.Close()
			return err
		}
		slugs = append(slugs, slug)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, slug := range slugs {
		if err := RunWorkspaceResolve(dbURL, slug); err != nil {
			return err
		}
	}
	return nil
}

func RunWorkspaceResolve(dbURL, workspaceSlug string) error {
	return RunWorkspaceResolveForRepos(dbURL, workspaceSlug, nil)
}

func RunWorkspaceResolveForRepo(dbURL, workspaceSlug, repoName string) error {
	return RunWorkspaceResolveForRepos(dbURL, workspaceSlug, []string{repoName})
}

func RunWorkspaceResolveForRepos(dbURL, workspaceSlug string, repoNames []string) error {
	storage, err := graph.NewStorage(dbURL)
	if err != nil {
		return err
	}
	defer storage.Close()
	ws, err := storage.ResolveWorkspace(workspaceSlug)
	if err != nil {
		return err
	}
	ctx := context.Background()
	lock, err := pgx.ConnectConfig(ctx, storage.Pool().Config().ConnConfig.Copy())
	if err != nil {
		return err
	}
	defer lock.Close(context.Background())
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_lock(741926,$1::int)`, ws.ID); err != nil {
		return err
	}
	defer lock.Exec(context.Background(), `SELECT pg_advisory_unlock(741926,$1::int)`, ws.ID)
	selections, err := selectionVersions(storage, ws.Slug)
	if err != nil {
		return err
	}
	refs, err := storage.ActiveSnapshotsForWorkspace(ws.Slug)
	if err != nil {
		return err
	}
	var roots []int64
	for _, name := range repoNames {
		name = strings.TrimSpace(name)
		found := false
		for _, ref := range refs {
			if ref.RepoName == name {
				roots = append(roots, ref.SnapshotID)
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("repository %q has no active snapshot in workspace %s", name, ws.Slug)
		}
	}
	return publishCandidates(ctx, storage, ws, refs, selections, nil, fmt.Sprintf("resolve-%d", time.Now().UnixNano()), "", false, true, nil, roots...)
}

func resolveSelectedSnapshots(ctx context.Context, storage *graph.Storage, refs []graph.WorkspaceSnapshotRef, changed map[string]candidate, resolve bool, timings *indexTimings) error {
	rebindDone := timings.begin("Reference repair", "Reset inferred targets and rebind references to selected snapshots", true)
	defer rebindDone()
	snapshotIDs := make([]int64, 0, len(refs))
	callerIDs := make([]int64, 0, len(changed))
	for _, ref := range refs {
		snapshotIDs = append(snapshotIDs, ref.SnapshotID)
		if _, ok := changed[ref.RepoName]; ok {
			callerIDs = append(callerIDs, ref.SnapshotID)
		}
	}
	if len(snapshotIDs) == 0 {
		return nil
	}
	// Partial modes repair existing cross-repository paths without adding new ones.
	if _, err := storage.Executor().Exec(ctx, `CREATE TEMP TABLE publication_cross_calls (id bigint) ON COMMIT DROP`); err != nil {
		return err
	}
	// Full resolution considers every eligible call; collecting the old cross-repo
	// subset is needed only to constrain partial repair modes.
	if !resolve {
		if _, err := storage.Executor().Exec(ctx, `INSERT INTO publication_cross_calls
 SELECT fc.id FROM function_calls fc JOIN functions caller ON caller.id=fc.caller_function_id JOIN files cf ON cf.id=caller.file_id
 JOIN functions target ON target.id=fc.callee_function_id JOIN files tf ON tf.id=target.file_id
 WHERE cf.snapshot_id=ANY($1) AND tf.repo_id<>cf.repo_id`, callerIDs); err != nil {
			return err
		}
	}
	// Reconsider inference even while the old callee is still active: additions can
	// introduce ambiguity. Parser bindings (including callbacks) retain ownership.
	if _, err := storage.Executor().Exec(ctx, `UPDATE function_calls fc SET callee_function_id=NULL,callee_resolution_source=NULL,callee_resolution_confidence=NULL
 FROM functions caller,files f WHERE caller.id=fc.caller_function_id AND f.id=caller.file_id AND f.snapshot_id=ANY($2)
 AND (COALESCE(fc.callee_resolution_source,'parser') NOT IN ('parser','callback_argument') OR (fc.is_callback_argument AND EXISTS(SELECT 1 FROM functions target WHERE target.id=fc.callee_function_id AND target.file_id<>caller.file_id)) OR EXISTS(SELECT 1 FROM functions target JOIN files tf ON tf.id=target.file_id WHERE target.id=fc.callee_function_id AND (tf.snapshot_id IS NULL OR NOT(tf.snapshot_id=ANY($1)))))`, snapshotIDs, callerIDs); err != nil {
		return err
	}
	if err := storage.RebindSnapshotReferences(ctx, snapshotIDs, callerIDs); err != nil {
		return err
	}
	rebindDone()
	localDone := timings.begin("Repository resolution", "Resolve function and IBM i calls within affected repositories", true)
	defer localDone()
	for _, ref := range refs {
		if _, ok := changed[ref.RepoName]; !ok {
			continue
		}
		snapshotID := ref.SnapshotID
		repoStart := time.Now()
		if err := storage.ResolveFunctionCallCalleesForSnapshot(ref.RepoID, &snapshotID); err != nil {
			return err
		}
		if err := storage.ResolveIBMiCalls(ref.RepoID, &snapshotID, snapshotIDs); err != nil {
			return err
		}
		fmt.Printf("Index resolution %s: %.2fs\n", ref.RepoName, time.Since(repoStart).Seconds())
	}
	localDone()
	crossDone := timings.begin("Cross-repository resolution", "Resolve calls across the selected workspace and clear resolved diagnostics", true)
	defer crossDone()
	crossRepoStart := time.Now()
	if err := resolveWorkspaceCrossRepoCalls(ctx, storage, snapshotIDs, callerIDs, resolve); err != nil {
		return err
	}
	fmt.Printf("Index cross-repository resolution: %.2fs\n", time.Since(crossRepoStart).Seconds())
	if _, err := storage.Executor().Exec(ctx, `UPDATE function_calls fc SET unresolved_reason=NULL FROM functions fn JOIN files f ON f.id=fn.file_id WHERE fc.caller_function_id=fn.id AND f.snapshot_id=ANY($1) AND fc.callee_function_id IS NOT NULL AND fc.unresolved_reason IS NOT NULL`, callerIDs); err != nil {
		return err
	}
	crossDone()
	traceDone := timings.begin("Trace refresh", "Refresh call and interface-implementation edges for affected snapshots", true)
	defer traceDone()
	traceStart := time.Now()
	for _, ref := range refs {
		if _, ok := changed[ref.RepoName]; !ok {
			continue
		}
		snapshotID := ref.SnapshotID
		if err := storage.RefreshTraceCallEdgesForSnapshot(ref.RepoID, &snapshotID); err != nil {
			return err
		}
		if err := storage.RefreshTraceInterfaceImplsForSnapshot(ref.RepoID, &snapshotID); err != nil {
			return err
		}
	}
	fmt.Printf("Index Trace refresh: %.2fs\n", time.Since(traceStart).Seconds())
	return nil
}

func resolveWorkspaceCrossRepoCalls(ctx context.Context, storage *graph.Storage, snapshotIDs, callerSnapshotIDs []int64, resolve bool) error {
	if len(snapshotIDs) == 0 {
		return nil
	}
	if len(callerSnapshotIDs) == 0 {
		callerSnapshotIDs = snapshotIDs
	}
	// Bare names carry no language, so a callee is only a candidate when its file
	// is in the caller's language family; case folds only where the language is
	// case-insensitive. See language_family.go.
	scopedFamily := languageFamilySQL("f.language")
	callerFamily := languageFamilySQL("f.language")
	simpleKey := nameKeySQL("family", "simple_name")
	requestedKey := nameKeySQL("caller_family", "callee_name")
	matchedKey := nameKeySQL("u.caller_family", "u.callee_name")
	// As with repository resolution, statistics on fresh snapshot IDs cannot
	// estimate the number of unique names. Build and analyze this workspace's
	// lookup once so matching calls does not rescan the whole caller set per name.
	if _, err := storage.Executor().Exec(ctx, `CREATE TEMP TABLE tirion_workspace_function_names
	 (kind text,repo_id bigint,family text,name text,callee_id bigint,declaration_count bigint) ON COMMIT DROP`); err != nil {
		return err
	}
	if _, err := storage.Executor().Exec(ctx, `WITH scoped AS MATERIALIZED (
	 SELECT fn.id,fn.name,fn.simple_name,f.repo_id,f.language,`+scopedFamily+` AS family FROM functions fn JOIN files f ON f.id=fn.file_id
	 WHERE f.snapshot_id=ANY($1)
	),qualified AS (
	 SELECT id,family,name FROM scoped
	 UNION
	 SELECT fn.id,fn.family,array_to_string(parts.name[n.start:],'.') FROM scoped fn
	 CROSS JOIN LATERAL (SELECT string_to_array(fn.name,'.') AS name) parts
	 CROSS JOIN LATERAL generate_series(2,cardinality(parts.name)-1) n(start)
	 WHERE fn.language='java'
	)
	INSERT INTO tirion_workspace_function_names(kind,repo_id,family,name,callee_id,declaration_count)
	SELECT 'qualified',NULL,family,name,MIN(id),COUNT(*) FROM qualified GROUP BY family,name HAVING COUNT(*)=1
	UNION ALL SELECT 'simple',repo_id,family,`+simpleKey+`,MIN(id),COUNT(*) FROM scoped GROUP BY repo_id,family,`+simpleKey, snapshotIDs); err != nil {
		return err
	}
	if _, err := storage.Executor().Exec(ctx, `CREATE INDEX ON tirion_workspace_function_names(kind,family,name,repo_id);
	 ANALYZE tirion_workspace_function_names`); err != nil {
		return err
	}

	if _, err := storage.Executor().Exec(ctx, `
		WITH unresolved AS (
			SELECT fc.id AS fc_id, fc.callee_name, f.repo_id AS caller_repo_id, `+callerFamily+` AS caller_family
			FROM function_calls fc
			JOIN functions cfn ON cfn.id = fc.caller_function_id
			JOIN files f ON f.id = cfn.file_id
			WHERE fc.callee_function_id IS NULL
			  AND f.snapshot_id = ANY($2)
			  AND ($3::boolean OR fc.id IN (SELECT id FROM publication_cross_calls))
			  AND fc.callee_name LIKE '%.%'
		),
		workspace_unique AS (
			SELECT family,name,callee_id FROM tirion_workspace_function_names WHERE kind='qualified'
		)
		UPDATE function_calls fc
		SET callee_function_id = u.callee_id,
		    callee_resolution_source = 'qualified_workspace',
		    callee_resolution_confidence = 'medium'
		FROM unresolved unresolved_call
		JOIN workspace_unique u ON u.name = unresolved_call.callee_name AND u.family = unresolved_call.caller_family
		JOIN functions tfn ON tfn.id = u.callee_id
		JOIN files tf ON tf.id = tfn.file_id
		WHERE fc.id = unresolved_call.fc_id
		  AND fc.callee_function_id IS NULL
		  AND tf.snapshot_id = ANY($1)
		  AND tf.repo_id <> unresolved_call.caller_repo_id
	`, snapshotIDs, callerSnapshotIDs, resolve); err != nil {
		return err
	}
	if _, err := storage.Executor().Exec(ctx, `
		WITH unresolved AS (
			SELECT fc.id AS fc_id, fc.callee_name, f.repo_id AS caller_repo_id, `+callerFamily+` AS caller_family
			FROM function_calls fc
			JOIN functions cfn ON cfn.id = fc.caller_function_id
			JOIN files f ON f.id = cfn.file_id
		WHERE fc.callee_function_id IS NULL
			  AND f.snapshot_id = ANY($2)
			  AND ($3::boolean OR fc.id IN (SELECT id FROM publication_cross_calls))
			  AND fc.callee_name NOT LIKE '%.%'
			  AND $1::bigint[] IS NOT NULL
		),
		requested AS (
			SELECT DISTINCT caller_repo_id, caller_family, `+requestedKey+` AS name FROM unresolved
		),
		declarations AS (
			SELECT repo_id,family,name,declaration_count,callee_id FROM tirion_workspace_function_names WHERE kind='simple'
		),
		unique_targets AS (
			-- Count every same-family declaration, but only once per requested
			-- name/repo, rather than expanding every call against all matches.
			SELECT r.caller_repo_id, r.caller_family, r.name, MIN(d.callee_id) AS callee_id
			FROM requested r JOIN declarations d ON d.family=r.caller_family AND d.name=r.name AND d.repo_id<>r.caller_repo_id
			GROUP BY r.caller_repo_id, r.caller_family, r.name
			HAVING SUM(d.declaration_count)=1
		)
		UPDATE function_calls fc
		SET callee_function_id = c.callee_id,
		    callee_resolution_source = 'unique_workspace',
		    callee_resolution_confidence = 'medium'
		FROM unresolved u JOIN unique_targets c ON c.caller_repo_id=u.caller_repo_id AND c.caller_family=u.caller_family AND c.name=`+matchedKey+`
	WHERE fc.id = u.fc_id
	  AND fc.callee_function_id IS NULL
	`, snapshotIDs, callerSnapshotIDs, resolve); err != nil {
		return err
	}
	return nil
}

func PrintStats(dbURL string) error {
	storage, err := graph.NewStorageNoSchemaInit(dbURL)
	if err != nil {
		return err
	}
	defer storage.Close()

	var repos, files, functions, classes, endpoints int
	err = storage.Pool().QueryRow(context.Background(), `
		SELECT
			(SELECT COUNT(*) FROM repositories),
			(SELECT COUNT(*) FROM files),
			(SELECT COUNT(*) FROM functions),
			(SELECT COUNT(*) FROM classes),
			(SELECT COUNT(*) FROM endpoints)
	`).Scan(&repos, &files, &functions, &classes, &endpoints)
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("Database stats:")
	fmt.Printf("  repos: %d\n", repos)
	fmt.Printf("  files: %d\n", files)
	fmt.Printf("  functions: %d\n", functions)
	fmt.Printf("  classes: %d\n", classes)
	fmt.Printf("  endpoints: %d\n", endpoints)
	return nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func sortedKeys(items map[string]bool) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
