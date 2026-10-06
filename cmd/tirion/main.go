package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sergiumoraru/tirion/internal/api"
	"github.com/sergiumoraru/tirion/internal/audit"
	"github.com/sergiumoraru/tirion/internal/buildinfo"
	"github.com/sergiumoraru/tirion/internal/graph"
	"github.com/sergiumoraru/tirion/internal/indexer"
	"github.com/sergiumoraru/tirion/internal/runtimeconfig"
	"github.com/sergiumoraru/tirion/internal/workspace"
)

func main() {
	if err := runtimeconfig.LoadEnvironment(); err != nil {
		log.Fatal(err)
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "index":
		runIndex(os.Args[2:])
	case "workspace":
		runWorkspace(os.Args[2:])
	case "search":
		runSearch(os.Args[2:])
	case "trace":
		runTrace(os.Args[2:])
	case "impact":
		runImpact(os.Args[2:])
	case "verify":
		runVerify(os.Args[2:])
	case "sync-branches":
		runSyncBranches(os.Args[2:])
	case "refresh-repo-graph":
		runRefreshRepoGraph(os.Args[2:])
	case "prune-snapshots":
		runPruneSnapshots(os.Args[2:])
	case "prune":
		runPrune(os.Args[2:])
	case "serve":
		runServe(os.Args[2:])
	case "version":
		fmt.Println(buildinfo.Version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func databaseFlag(fs *flag.FlagSet, fallback string) *string {
	value := fs.String("db", fallback, "PostgreSQL connection string (required; defaults to DATABASE_URL)")
	// Help output must not print credentials supplied through the environment.
	fs.Lookup("db").DefValue = ""
	return value
}

func runIndex(args []string) {

	defaultDB := os.Getenv("DATABASE_URL")

	defaultRoot, _ := os.Getwd()

	fs := flag.NewFlagSet("index", flag.ExitOnError)
	root := fs.String("root", defaultRoot, "Directory whose direct child folders are candidate repos")
	dbURL := databaseFlag(fs, defaultDB)
	skipTests := fs.Bool("skip-tests", true, "Skip test/spec files during parsing")
	globalResolve := fs.Bool("global-resolve", true, "Run cross-repo call resolution during indexing")
	exclude := fs.String("exclude", "", "Comma-separated repo directory names to skip")
	dryRun := fs.Bool("dry-run", false, "Print repos that would be parsed without indexing")
	verbose := fs.Bool("v", false, "Print helper command lines before execution")
	allowFileFailures := fs.Bool("allow-file-failures", false, "Publish even when files lost all facts to a parser timeout or internal error (the snapshot is then never reused by -skip-unchanged)")
	parseBin := fs.String("parse-bin", indexer.DefaultParseBinary(), "Optional explicit path to parse binary used during indexing")
	workspaceSlug := fs.String("workspace", graph.DefaultWorkspaceSlug, "Workspace slug to index")
	skipUnchanged := fs.Bool("skip-unchanged", true, "Skip repos whose HEAD matches last indexed SHA")
	skipExtractors := fs.Bool("skip-extractors", false, "Skip post-parse extractor pipeline")
	extractorList := fs.String("extractors", "http,sqs,java-intel,java-calls,spring,ts-intel", "Comma-separated extractor set to run after indexing")
	fs.Usage = func() {
		fmt.Println("Usage: tirion index [flags] [repos-root]")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	mustPositionals(fs, "index", 0, 1)

	if fs.NArg() > 0 {
		*root = fs.Arg(0)
	}

	_, err := indexer.RunBatchDetailed(indexer.BatchOptions{
		DBURL:             *dbURL,
		Root:              *root,
		ParseBinary:       *parseBin,
		Workspace:         *workspaceSlug,
		SkipTests:         *skipTests,
		GlobalResolve:     *globalResolve,
		DryRun:            *dryRun,
		Verbose:           *verbose,
		Exclude:           indexer.ParseExcluded(*exclude),
		SkipUnchanged:     *skipUnchanged,
		SkipExtractors:    *skipExtractors,
		Extractors:        *extractorList,
		AllowFileFailures: *allowFileFailures,
	})
	if err != nil {
		log.Fatalf("index failed: %v", err)
	}

}

func runWorkspace(args []string) {
	if len(args) == 0 {
		printWorkspaceUsage()
		os.Exit(1)
	}

	defaultDB := os.Getenv("DATABASE_URL")

	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("workspace list", flag.ExitOnError)
		dbURL := databaseFlag(fs, defaultDB)
		_ = fs.Parse(args[1:])
		mustPositionals(fs, "workspace list", 0, 0)
		storage, err := graph.NewStorage(*dbURL)
		if err != nil {
			log.Fatalf("connect db: %v", err)
		}
		defer storage.Close()
		failed, err := printWorkspaceList(os.Stdout, os.Stderr, storage)
		if err != nil {
			log.Fatalf("list workspaces: %v", err)
		}
		if failed > 0 {
			os.Exit(1)
		}
	case "create":
		fs := flag.NewFlagSet("workspace create", flag.ExitOnError)
		dbURL := databaseFlag(fs, defaultDB)
		name := fs.String("name", "", "Workspace display name")
		description := fs.String("description", "", "Workspace description")
		from := fs.String("from", "", "Copy repo selections from an existing workspace")
		makeDefault := fs.Bool("default", false, "Mark workspace as default")
		fs.Usage = func() {
			fmt.Println("Usage: tirion workspace create [flags] <workspace-slug>")
			fs.PrintDefaults()
		}
		_ = fs.Parse(args[1:])
		mustPositionals(fs, "workspace create", 1, 1)
		slug := fs.Arg(0)
		displayName := *name
		if strings.TrimSpace(displayName) == "" {
			displayName = slug
		}
		storage, err := graph.NewStorage(*dbURL)
		if err != nil {
			log.Fatalf("connect db: %v", err)
		}
		defer storage.Close()
		ws, err := storage.EnsureWorkspace(slug, displayName, *description, "", *makeDefault)
		if err != nil {
			log.Fatalf("create workspace: %v", err)
		}
		if strings.TrimSpace(*from) != "" {
			if err := storage.CloneWorkspaceRepoSelections(*from, ws.ID); err != nil {
				log.Fatalf("clone repo selections: %v", err)
			}
		}
		fmt.Printf("workspace %s ready\n", ws.Slug)
	case "set-ref":
		fs := flag.NewFlagSet("workspace set-ref", flag.ExitOnError)
		dbURL := databaseFlag(fs, defaultDB)
		fs.Usage = func() {
			fmt.Println("Usage: tirion workspace set-ref [flags] <workspace-slug> <repo-name> <ref>")
			fs.PrintDefaults()
		}
		_ = fs.Parse(args[1:])
		mustPositionals(fs, "workspace set-ref", 3, 3)
		storage, err := graph.NewStorage(*dbURL)
		if err != nil {
			log.Fatalf("connect db: %v", err)
		}
		defer storage.Close()
		ws, err := storage.ResolveWorkspace(fs.Arg(0))
		if err != nil {
			log.Fatalf("resolve workspace: %v", err)
		}
		if _, err := storage.UpsertWorkspaceRepo(graph.UpsertWorkspaceRepoInput{
			WorkspaceID: ws.ID,
			RepoName:    fs.Arg(1),
			TargetRef:   fs.Arg(2),
			IndexStatus: "unknown",
		}); err != nil {
			log.Fatalf("set workspace repo ref: %v", err)
		}
		fmt.Printf("%s/%s -> %s\n", ws.Slug, fs.Arg(1), fs.Arg(2))
	case "fetch":
		fs := flag.NewFlagSet("workspace fetch", flag.ExitOnError)
		dbURL := databaseFlag(fs, defaultDB)
		fs.Usage = func() {
			fmt.Println("Usage: tirion workspace fetch [flags] <workspace-slug> <repo-name>")
			fs.PrintDefaults()
		}
		_ = fs.Parse(args[1:])
		mustPositionals(fs, "workspace fetch", 2, 2)
		workspaceSlug := fs.Arg(0)
		repoName := fs.Arg(1)
		storage, err := graph.NewStorage(*dbURL)
		if err != nil {
			log.Fatalf("connect db: %v", err)
		}
		defer storage.Close()
		ws, err := storage.ResolveWorkspace(workspaceSlug)
		if err != nil {
			log.Fatalf("resolve workspace: %v", err)
		}
		repo, err := storage.GetRepoByName(repoName)
		if err != nil {
			log.Fatalf("load source repo: %v", err)
		}
		existing, _ := storage.GetWorkspaceRepo(ws.Slug, repoName)
		if err := workspace.Fetch(repo.Path); err != nil {
			_, _ = storage.UpsertWorkspaceRepo(workspaceRepoInputFromExistingCLI(ws.ID, repoName, "", existing, graph.UpsertWorkspaceRepoInput{
				WorkspaceID: ws.ID,
				RepoName:    repoName,
				LastError:   err.Error(),
			}))
			log.Fatalf("fetch refs: %v", err)
		}
		if _, err := storage.UpsertWorkspaceRepo(workspaceRepoInputFromExistingCLI(ws.ID, repoName, "", existing, graph.UpsertWorkspaceRepoInput{
			WorkspaceID: ws.ID,
			RepoName:    repoName,
			LastError:   "",
		})); err != nil {
			log.Fatalf("persist fetch state: %v", err)
		}
		fmt.Printf("%s/%s refs fetched\n", ws.Slug, repoName)
	case "checkout":
		fs := flag.NewFlagSet("workspace checkout", flag.ExitOnError)
		dbURL := databaseFlag(fs, defaultDB)
		worktreeRoot := fs.String("worktree-root", defaultWorkspaceWorktreeRoot(), "Root directory for workspace worktrees")
		fs.Usage = func() {
			fmt.Println("Usage: tirion workspace checkout [flags] <workspace-slug> <repo-name>")
			fs.PrintDefaults()
		}
		_ = fs.Parse(args[1:])
		mustPositionals(fs, "workspace checkout", 2, 2)
		workspaceSlug := fs.Arg(0)
		repoName := fs.Arg(1)
		storage, err := graph.NewStorage(*dbURL)
		if err != nil {
			log.Fatalf("connect db: %v", err)
		}
		defer storage.Close()
		wr, err := storage.GetWorkspaceRepo(workspaceSlug, repoName)
		if err != nil {
			log.Fatalf("load workspace repo: %v", err)
		}
		ref := strings.TrimSpace(wr.TargetRef)
		if ref == "" {
			ref = strings.TrimSpace(wr.ResolvedBranch)
		}
		if ref == "" {
			log.Fatalf("workspace repo has no target ref: %s/%s", workspaceSlug, repoName)
		}
		repo, err := storage.GetRepoByName(repoName)
		if err != nil {
			log.Fatalf("load source repo: %v", err)
		}
		targetPath := filepath.Join(*worktreeRoot, workspaceSlug, "repos", repoName)
		if err := workspace.EnsureWorktree(repo.Path, targetPath, ref); err != nil {
			log.Fatalf("checkout worktree: %v", err)
		}
		status, err := workspace.InspectRepo(targetPath)
		if err != nil {
			log.Fatalf("inspect worktree: %v", err)
		}
		resolvedBranch := status.CurrentBranch
		if resolvedBranch == "" || resolvedBranch == "detached" {
			resolvedBranch = ref
		}
		now := time.Now().UTC()
		if _, err := storage.UpsertWorkspaceRepo(graph.UpsertWorkspaceRepoInput{
			WorkspaceID:    wr.WorkspaceID,
			RepoName:       repoName,
			TargetRef:      ref,
			ResolvedBranch: resolvedBranch,
			ResolvedSHA:    status.HeadSHA,
			WorktreePath:   targetPath,
			LastCheckoutAt: &now,
			IndexStatus:    wr.IndexStatus,
			LastError:      "",
		}); err != nil {
			log.Fatalf("persist worktree state: %v", err)
		}
		fmt.Printf("%s/%s checked out at %s (%s)\n", workspaceSlug, repoName, status.CurrentBranch, shortSHA(status.HeadSHA, 12))
	case "index":
		fs := flag.NewFlagSet("workspace index", flag.ExitOnError)
		root := fs.String("root", "", "Directory whose direct child folders are candidate repos (default: <worktree-root>/<workspace>/repos)")
		worktreeRoot := fs.String("worktree-root", defaultWorkspaceWorktreeRoot(), "Root directory for workspace worktrees")
		dbURL := databaseFlag(fs, defaultDB)
		parseBin := fs.String("parse-bin", indexer.DefaultParseBinary(), "Optional explicit path to parse binary used during indexing")
		repoName := fs.String("repo", "", "Optional single repo in the workspace to index")
		skipTests := fs.Bool("skip-tests", true, "Skip test/spec files during parsing")
		globalResolve := fs.Bool("global-resolve", true, "Run cross-repo call resolution during indexing")
		skipUnchanged := fs.Bool("skip-unchanged", true, "Skip repos whose HEAD matches last indexed SHA in this workspace")
		exclude := fs.String("exclude", "", "Comma-separated repo directory names to skip")
		verbose := fs.Bool("v", false, "Print helper command lines before execution")
		fs.Usage = func() {
			fmt.Println("Usage: tirion workspace index [flags] <workspace-slug>")
			fs.PrintDefaults()
		}
		_ = fs.Parse(args[1:])
		mustPositionals(fs, "workspace index", 1, 1)
		workspaceSlug := fs.Arg(0)
		if strings.TrimSpace(*repoName) != "" {
			if err := runWorkspaceRepoIndex(*dbURL, workspaceSlug, strings.TrimSpace(*repoName), *globalResolve); err != nil {
				log.Fatalf("workspace repo index failed: %v", err)
			}
			return
		}
		indexRoot := strings.TrimSpace(*root)
		if indexRoot == "" {
			indexRoot = filepath.Join(*worktreeRoot, workspaceSlug, "repos")
		}
		_, err := indexer.RunBatchDetailed(indexer.BatchOptions{
			DBURL:         *dbURL,
			Root:          indexRoot,
			ParseBinary:   *parseBin,
			Workspace:     workspaceSlug,
			SkipTests:     *skipTests,
			GlobalResolve: *globalResolve,
			Verbose:       *verbose,
			Exclude:       indexer.ParseExcluded(*exclude),
			SkipUnchanged: *skipUnchanged,
		})
		if err != nil {
			log.Fatalf("workspace index failed: %v", err)
		}
	default:
		printWorkspaceUsage()
		os.Exit(1)
	}
}

type workspaceLister interface {
	ListWorkspaces() ([]graph.Workspace, error)
	ActiveSnapshotsForWorkspace(slug string) ([]graph.WorkspaceSnapshotRef, error)
}

// printWorkspaceList writes one line per workspace. A workspace whose snapshots
// cannot be loaded is reported on errOut and shown as unknown rather than as a
// misleading zero; the others are still listed. failed counts those workspaces.
func printWorkspaceList(out, errOut io.Writer, lister workspaceLister) (failed int, err error) {
	workspaces, err := lister.ListWorkspaces()
	if err != nil {
		return 0, err
	}
	for _, ws := range workspaces {
		defaultMark := ""
		if ws.IsDefault {
			defaultMark = " default"
		}
		snapshots, err := lister.ActiveSnapshotsForWorkspace(ws.Slug)
		if err != nil {
			failed++
			fmt.Fprintf(errOut, "load active snapshots for workspace %s: %v\n", ws.Slug, err)
			fmt.Fprintf(out, "%s%s\t%s\tactive_snapshots=unknown\n", ws.Slug, defaultMark, ws.Name)
			continue
		}
		fmt.Fprintf(out, "%s%s\t%s\tactive_snapshots=%d\n", ws.Slug, defaultMark, ws.Name, len(snapshots))
	}
	return failed, nil
}

func runWorkspaceRepoIndex(dbURL, workspaceSlug, repoName string, globalResolve bool) error {
	storage, err := graph.NewStorage(dbURL)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer storage.Close()

	ws, err := storage.ResolveWorkspace(workspaceSlug)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}
	wr, err := storage.GetWorkspaceRepo(ws.Slug, repoName)
	if err != nil {
		return fmt.Errorf("load workspace repo: %w", err)
	}
	if strings.TrimSpace(wr.WorktreePath) == "" {
		return fmt.Errorf("workspace repo %s/%s has no checked-out worktree", ws.Slug, repoName)
	}
	if err := indexer.RunSingleRepository(dbURL, ws.Slug, repoName, wr.WorktreePath, globalResolve); err != nil {
		return err
	}
	fmt.Printf("%s/%s indexed from %s\n", ws.Slug, repoName, wr.WorktreePath)
	return nil
}

func printWorkspaceUsage() {
	fmt.Println(`Usage:
 tirion workspace list [flags]
  tirion workspace create [flags] <workspace-slug>
  tirion workspace set-ref [flags] <workspace-slug> <repo-name> <ref>
  tirion workspace fetch [flags] <workspace-slug> <repo-name>
  tirion workspace checkout [flags] <workspace-slug> <repo-name>
  tirion workspace index [flags] <workspace-slug>`)
}

func workspaceRepoInputFromExistingCLI(workspaceID int64, repoName, targetRef string, existing *graph.WorkspaceRepo, next graph.UpsertWorkspaceRepoInput) graph.UpsertWorkspaceRepoInput {
	if next.WorkspaceID == 0 {
		next.WorkspaceID = workspaceID
	}
	if next.RepoName == "" {
		next.RepoName = repoName
	}
	if next.TargetRef == "" {
		next.TargetRef = targetRef
	}
	if existing == nil {
		if next.IndexStatus == "" {
			next.IndexStatus = "unknown"
		}
		return next
	}
	if next.TargetRef == "" {
		next.TargetRef = existing.TargetRef
	}
	if next.ResolvedBranch == "" {
		next.ResolvedBranch = existing.ResolvedBranch
	}
	if next.ResolvedSHA == "" {
		next.ResolvedSHA = existing.ResolvedSHA
	}
	if next.WorktreePath == "" {
		next.WorktreePath = existing.WorktreePath
	}
	if next.ActiveSnapshotID == nil {
		next.ActiveSnapshotID = existing.ActiveSnapshotID
	}
	if next.LastCheckoutAt == nil {
		next.LastCheckoutAt = existing.LastCheckoutAt
	}
	if next.LastIndexedAt == nil {
		next.LastIndexedAt = existing.LastIndexedAt
	}
	if next.IndexStatus == "" {
		next.IndexStatus = existing.IndexStatus
	}
	return next
}

func defaultWorkspaceWorktreeRoot() string {
	if root := strings.TrimSpace(os.Getenv("TIRION_WORKTREE_ROOT")); root != "" {
		return root
	}
	if root := strings.TrimSpace(os.Getenv("TIRION_WORKSPACES_ROOT")); root != "" {
		return root
	}
	return filepath.Join(os.TempDir(), "tirion-workspaces")
}

func runSearch(args []string) {

	fs := flag.NewFlagSet("search", flag.ExitOnError)
	apiURL := fs.String("api-url", defaultAPIURL(), "Tirion API base URL")
	workspaceSlug := fs.String("workspace", graph.DefaultWorkspaceSlug, "Workspace slug to query")
	mode := fs.String("mode", "keyword", "Search mode")
	repo := fs.String("repo", "", "Optional repository filter")
	limit := fs.Int("limit", 10, "Max results")
	fs.Usage = func() {
		fmt.Println("Usage: tirion search [flags] <query>")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	mustPositionals(fs, "search", 1, -1)

	params := url.Values{}
	params.Set("q", strings.Join(fs.Args(), " "))
	params.Set("workspaceId", *workspaceSlug)
	params.Set("mode", *mode)
	params.Set("limit", fmt.Sprint(*limit))
	params.Set("sort", "richness")
	params.Set("noNoise", "true")
	if strings.TrimSpace(*repo) != "" {
		params.Set("repo", strings.TrimSpace(*repo))
	}

	if err := getAndPrintJSON(apiEndpoint(*apiURL, "/search") + "?" + params.Encode()); err != nil {
		log.Fatalf("search failed: %v", err)
	}
}

func runTrace(args []string) {

	fs := flag.NewFlagSet("trace", flag.ExitOnError)
	apiURL := fs.String("api-url", defaultAPIURL(), "Tirion API base URL")
	workspaceSlug := fs.String("workspace", graph.DefaultWorkspaceSlug, "Workspace slug to query")
	depth := fs.Int("depth", 4, "Max traversal depth")
	maxNodes := fs.Int("max-nodes", 2000, "Max nodes")
	noTests := fs.Bool("no-tests", true, "Exclude tests")
	resolve := fs.Bool("resolve", false, "Resolve cross-service HTTP/SQS edges")
	fs.Usage = func() {
		fmt.Println("Usage: tirion trace [flags] <function>")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	mustPositionals(fs, "trace", 1, -1)

	req := map[string]any{
		"function":    strings.Join(fs.Args(), " "),
		"workspaceId": *workspaceSlug,
		"depth":       *depth,
		"maxNodes":    *maxNodes,
		"noTests":     *noTests,
		"resolve":     *resolve,
	}
	if err := postAndPrintJSON(apiEndpoint(*apiURL, "/trace"), req); err != nil {
		log.Fatalf("trace failed: %v", err)
	}
}

func runImpact(args []string) {

	fs := flag.NewFlagSet("impact", flag.ExitOnError)
	apiURL := fs.String("api-url", defaultAPIURL(), "Tirion API base URL")
	workspaceSlug := fs.String("workspace", graph.DefaultWorkspaceSlug, "Workspace slug to query")
	diffFile := fs.String("diff-file", "", "Path to unified diff file, or '-' for stdin")
	functionsFlag := fs.String("functions", "", "Comma-separated function names")
	repo := fs.String("repo", "", "Repo name to apply to diff paths")
	depth := fs.Int("depth", 4, "Trace depth for impact analysis")
	noTests := fs.Bool("no-tests", true, "Hide nodes from test files")
	resolve := fs.Bool("resolve", true, "Resolve DI/impl")
	excludePatterns := fs.String("exclude", "", "Comma-separated substrings to exclude")
	includeRepos := fs.String("include-repo", "", "Comma-separated repo names to include")
	excludeRepos := fs.String("exclude-repo", "", "Comma-separated repo names to exclude")
	maxNodes := fs.Int("max-nodes", 2000, "Max nodes to return")
	out := fs.String("out", "", "Output JSON file path (default stdout)")
	fs.Usage = func() {
		fmt.Println("Usage: tirion impact [flags]")
		fmt.Println("  Provide -functions=<csv>, -diff-file=<path>, or pipe a unified diff on stdin.")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	mustPositionals(fs, "impact", 0, 0)

	functions := parseCommaList(*functionsFlag)
	diffData := readDiffInput(*diffFile)
	if len(functions) == 0 && strings.TrimSpace(diffData) == "" {
		fs.Usage()
		os.Exit(1)
	}

	req := map[string]any{
		"workspaceId":  *workspaceSlug,
		"functions":    functions,
		"diff":         diffData,
		"repo":         strings.TrimSpace(*repo),
		"depth":        *depth,
		"noTests":      *noTests,
		"resolve":      *resolve,
		"exclude":      parseCommaList(*excludePatterns),
		"includeRepos": parseCommaList(*includeRepos),
		"excludeRepos": parseCommaList(*excludeRepos),
		"maxNodes":     *maxNodes,
	}
	if err := postJSONAndWrite(apiEndpoint(*apiURL, "/impact"), req, *out); err != nil {
		log.Fatalf("impact failed: %v", err)
	}
}

func runVerify(args []string) {

	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	apiURL := fs.String("api-url", defaultAPIURL(), "Tirion API base URL")
	workspaceSlug := fs.String("workspace", graph.DefaultWorkspaceSlug, "Workspace slug to query")
	diffFile := fs.String("diff-file", "", "Path to unified diff file, or '-' for stdin")
	workFile := fs.String("work-file", "", "Path to agent-work proof JSON (task, rootCause, requirementsComplete, rootCauseEvidence, claims)")
	repo := fs.String("repo", "", "Repo name to apply to diff paths")
	depth := fs.Int("depth", 4, "Trace depth for impact analysis")
	noTests := fs.Bool("no-tests", true, "Hide nodes from test files")
	resolve := fs.Bool("resolve", true, "Resolve DI/impl")
	maxNodes := fs.Int("max-nodes", 2000, "Max nodes to return")
	failOn := fs.String("fail-on", "fail", "Exit non-zero on: fail or warn")
	out := fs.String("out", "", "Output JSON file path (default stdout)")
	fs.Usage = func() {
		fmt.Println("Usage: tirion verify [flags]")
		fmt.Println("  Provide -diff-file=<path> or pipe a unified diff on stdin.")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	mustPositionals(fs, "verify", 0, 0)
	threshold := strings.ToLower(strings.TrimSpace(*failOn))
	if threshold != "fail" && threshold != "warn" && threshold != "" {
		log.Fatalf("invalid -fail-on value %q (want fail or warn)", *failOn)
	}

	diffData := readDiffInput(*diffFile)
	if strings.TrimSpace(diffData) == "" {
		fs.Usage()
		os.Exit(1)
	}
	if strings.TrimSpace(*workFile) == "" {
		log.Fatal("verify requires -work-file with the agent's task, root cause, requirement claims, and source evidence")
	}
	if strings.TrimSpace(*repo) == "" {
		log.Fatal("verify requires -repo so diff paths can be aligned with the active workspace")
	}

	req := map[string]any{
		"workspaceId": *workspaceSlug,
		"diff":        diffData,
		"repo":        strings.TrimSpace(*repo),
		"depth":       *depth,
		"noTests":     *noTests,
		"resolve":     *resolve,
		"maxNodes":    *maxNodes,
		"verify":      true,
	}
	data, err := os.ReadFile(*workFile)
	if err != nil {
		log.Fatalf("read agent-work proof: %v", err)
	}
	var work map[string]any
	if err := json.Unmarshal(data, &work); err != nil {
		log.Fatalf("parse agent-work proof: %v", err)
	}
	req["agentWork"] = work
	payload, err := postJSONBytes(apiEndpoint(*apiURL, "/verify"), req)
	if err != nil {
		log.Fatalf("verify failed: %v", err)
	}
	if err := writeJSONPayload(payload, *out); err != nil {
		log.Fatalf("verify output failed: %v", err)
	}
	verdict := verdictFromImpactPayload(payload)
	if verdict != "pass" && verdict != "info" && verdict != "warn" && verdict != "fail" {
		log.Fatalf("verify response has no recognized verdict: %q", verdict)
	}
	switch threshold {
	case "warn":
		if verdict == "warn" {
			os.Exit(2)
		}
		if verdict == "fail" {
			os.Exit(1)
		}
	case "fail", "":
		if verdict == "fail" {
			os.Exit(1)
		}
	}
}

func defaultAPIURL() string {
	if raw := strings.TrimSpace(os.Getenv("TIRION_API_URL")); raw != "" {
		return raw
	}
	if raw := strings.TrimSpace(os.Getenv("CODEBASE_INTEL_API_URL")); raw != "" {
		return raw
	}
	return "http://localhost:8080"
}

func apiEndpoint(base, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	path = "/" + strings.TrimLeft(path, "/")
	if strings.HasSuffix(base, "/api") {
		return base + path
	}
	return base + "/api" + path
}

func getAndPrintJSON(endpoint string) error {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	return doAndPrintJSON(req)
}

func postAndPrintJSON(endpoint string, body any) error {
	return postJSONAndWrite(endpoint, body, "")
}

func postJSONAndWrite(endpoint string, body any, outPath string) error {
	payload, err := postJSONBytes(endpoint, body)
	if err != nil {
		return err
	}
	return writeJSONPayload(payload, outPath)
}

func postJSONBytes(endpoint string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return doAndReadJSON(req)
}

func doAndPrintJSON(req *http.Request) error {
	return doAndWriteJSON(req, "")
}

func doAndWriteJSON(req *http.Request, outPath string) error {
	payload, err := doAndReadJSON(req)
	if err != nil {
		return err
	}
	return writeJSONPayload(payload, outPath)
}

func doAndReadJSON(req *http.Request) ([]byte, error) {
	token, err := runtimeconfig.ClientToken(req.URL.String())
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Tirion-Token", token)
	client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: runtimeconfig.NoRedirect}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	const maxResponseBytes = 20 * 1024 * 1024
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > maxResponseBytes {
		return nil, fmt.Errorf("API response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if !json.Valid(payload) {
		return nil, fmt.Errorf("API response is not valid JSON")
	}
	return payload, nil
}

func writeJSONPayload(payload []byte, outPath string) error {
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, payload, "", "  "); err != nil {
		pretty.Write(payload)
	} else {
		pretty.WriteByte('\n')
	}
	if strings.TrimSpace(outPath) != "" {
		return os.WriteFile(outPath, pretty.Bytes(), 0o644)
	}
	_, err := os.Stdout.Write(pretty.Bytes())
	return err
}

func verdictFromImpactPayload(payload []byte) string {
	var parsed struct {
		Verify struct {
			Verdict string `json:"verdict"`
		} `json:"verify"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(parsed.Verify.Verdict))
}

func parseCommaList(input string) []string {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	parts := strings.Split(input, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func readDiffInput(path string) string {
	if strings.TrimSpace(path) == "" && !stdinHasData() {
		return ""
	}
	if strings.TrimSpace(path) == "" || path == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			log.Fatalf("failed to read diff from stdin: %v", err)
		}
		return string(data)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("failed to read diff file: %v", err)
	}
	return string(data)
}

func stdinHasData() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice == 0
}

func runServe(args []string) {

	defaultDB := os.Getenv("DATABASE_URL")
	defaultPort := os.Getenv("PORT")
	if defaultPort == "" {
		defaultPort = "8080"
	}

	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dbURL := databaseFlag(fs, defaultDB)
	port := fs.String("port", defaultPort, "HTTP port")
	defaultHost := strings.TrimSpace(os.Getenv("TIRION_HOST"))
	if defaultHost == "" {
		defaultHost = "127.0.0.1"
	}
	host := fs.String("host", defaultHost, "HTTP bind address; use 0.0.0.0 explicitly for shared deployments")
	fs.Usage = func() {
		fmt.Println("Usage: tirion serve [flags]")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	mustPositionals(fs, "serve", 0, 0)

	server, err := api.NewServer(*dbURL)
	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

	addr := net.JoinHostPort(*host, *port)
	if os.Getenv("TIRION_API_TOKEN") == "" {
		if path, err := runtimeconfig.TokenFile(); err == nil {
			log.Printf("API authentication enabled; token file: %s", path)
		}
	}
	log.Printf("Tirion server starting on %s", addr)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		server.Close()
		log.Fatalf("Failed to listen on %s: %v", addr, err)
	}
	httpServer := &http.Server{Addr: addr, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 2*time.Hour + 5*time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		// After the first signal, restore default handling so a second Ctrl+C
		// ends the process instead of waiting out the grace period.
		<-ctx.Done()
		stop()
	}()
	err = serveGracefully(ctx, httpServer, listener, shutdownGracePeriod, func() { server.Close() })
	stop()
	if err != nil {
		log.Fatalf("Server stopped: %v", err)
	}
	log.Print("Tirion server stopped")
}

// shutdownGracePeriod bounds how long in-flight requests may finish after
// SIGINT/SIGTERM before they are cancelled.
const shutdownGracePeriod = 30 * time.Second

// serveGracefully serves until the listener fails or ctx is cancelled (signal).
// On cancellation it stops accepting connections and waits up to grace for
// in-flight requests; requests still running then have their contexts cancelled
// and their connections closed. cleanup (closing storage) always runs last, once
// the server has stopped serving. A clean, signal-driven stop returns nil.
func serveGracefully(ctx context.Context, srv *http.Server, listener net.Listener, grace time.Duration, cleanup func()) error {
	// Request contexts derive from base, so cancelling it aborts handlers (for
	// example a long index run) that outlive the grace period.
	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	srv.BaseContext = func(net.Listener) context.Context { return base }
	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()
	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	log.Printf("Shutdown requested; waiting up to %s for in-flight requests", grace)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		cancelBase()
		_ = srv.Close()
		return fmt.Errorf("graceful shutdown did not finish within %s: %w", grace, err)
	}
	return nil
}

func runSyncBranches(args []string) {

	defaultDB := os.Getenv("DATABASE_URL")

	defaultRoot, _ := os.Getwd()

	fs := flag.NewFlagSet("sync-branches", flag.ExitOnError)
	root := fs.String("root", defaultRoot, "Repos root to constrain selected-branch application")
	dbURL := databaseFlag(fs, defaultDB)
	fetch := fs.Bool("fetch", false, "Fetch selected repos before checkout")
	mainline := fs.Bool("mainline", false, "Checkout each repo under root to its remote default branch instead of DB-selected branches")
	fs.Usage = func() {
		fmt.Println("Usage: tirion sync-branches [flags] [repos-root]")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	mustPositionals(fs, "sync-branches", 0, 1)
	if fs.NArg() > 0 {
		*root = fs.Arg(0)
	}

	rootPath, err := filepath.Abs(*root)
	if err != nil {
		log.Fatalf("resolve root: %v", err)
	}

	storage, err := graph.NewStorage(*dbURL)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer storage.Close()

	if *mainline {
		repos, err := storage.ListRepos()
		if err != nil {
			log.Fatalf("load repos: %v", err)
		}
		applied := 0
		skipped := 0
		failed := 0
		for _, repo := range repos {
			if !pathWithinRoot(rootPath, repo.Path) {
				continue
			}
			fmt.Printf("%s: mainline branch\n", repo.Name)
			status, err := workspace.InspectRepo(repo.Path)
			if err != nil {
				failed++
				fmt.Printf("  FAIL inspect: %v\n", err)
				continue
			}
			if status.Dirty {
				failed++
				fmt.Println("  FAIL dirty checkout blocks sync")
				continue
			}
			if *fetch {
				if err := workspace.Fetch(repo.Path); err != nil {
					failed++
					fmt.Printf("  FAIL fetch: %v\n", err)
					continue
				}
			}
			branch, err := workspace.RemoteDefaultBranch(repo.Path)
			if err != nil {
				failed++
				fmt.Printf("  FAIL resolve mainline: %v\n", err)
				continue
			}
			if status.CurrentBranch == branch {
				skipped++
				fmt.Printf("  SKIP already on %s\n", branch)
				continue
			}
			if err := workspace.Checkout(repo.Path, branch); err != nil {
				failed++
				fmt.Printf("  FAIL checkout: %v\n", err)
				continue
			}
			applied++
			fmt.Printf("  OK checked out %s\n", branch)
		}
		fmt.Println()
		fmt.Printf("Repos: %d\n", len(repos))
		fmt.Printf("Applied: %d\n", applied)
		fmt.Printf("Skipped: %d\n", skipped)
		fmt.Printf("Failed: %d\n", failed)
		if failed > 0 {
			os.Exit(1)
		}
		return
	}

	selected, err := storage.ListSelectedRepoBranches()
	if err != nil {
		log.Fatalf("load selected branches: %v", err)
	}

	applied := 0
	skipped := 0
	failed := 0

	for _, repo := range selected {
		if !pathWithinRoot(rootPath, repo.Path) {
			continue
		}
		fmt.Printf("%s: selected branch %s\n", repo.Name, repo.SelectedBranch)

		status, err := workspace.InspectRepo(repo.Path)
		if err != nil {
			failed++
			fmt.Printf("  FAIL inspect: %v\n", err)
			continue
		}
		if status.Dirty {
			failed++
			fmt.Println("  FAIL dirty checkout blocks sync")
			continue
		}
		if *fetch {
			if err := workspace.Fetch(repo.Path); err != nil {
				failed++
				fmt.Printf("  FAIL fetch: %v\n", err)
				continue
			}
		}
		if status.CurrentBranch == repo.SelectedBranch {
			skipped++
			fmt.Println("  SKIP already on selected branch")
			continue
		}
		if err := workspace.Checkout(repo.Path, repo.SelectedBranch); err != nil {
			failed++
			fmt.Printf("  FAIL checkout: %v\n", err)
			continue
		}
		applied++
		fmt.Println("  OK checked out selected branch")
	}

	fmt.Println()
	fmt.Printf("Selected repos: %d\n", len(selected))
	fmt.Printf("Applied: %d\n", applied)
	fmt.Printf("Skipped: %d\n", skipped)
	fmt.Printf("Failed: %d\n", failed)

	if failed > 0 {
		os.Exit(1)
	}
}

func runRefreshRepoGraph(args []string) {

	defaultDB := os.Getenv("DATABASE_URL")

	fs := flag.NewFlagSet("refresh-repo-graph", flag.ExitOnError)
	dbURL := databaseFlag(fs, defaultDB)
	fs.Usage = func() {
		fmt.Println("Usage: tirion refresh-repo-graph [flags]")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	mustPositionals(fs, "refresh-repo-graph", 0, 0)

	storage, err := graph.NewStorage(*dbURL)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer storage.Close()

	if err := audit.RefreshRepoDependencySnapshot(context.Background(), storage.Pool()); err != nil {
		log.Fatalf("refresh repo dependency graph: %v", err)
	}
	fmt.Println("Repo dependency graph snapshot refreshed.")
}

func runPrune(args []string) {

	defaultDB := os.Getenv("DATABASE_URL")
	defaultRoot, _ := os.Getwd()

	fs := flag.NewFlagSet("prune", flag.ExitOnError)
	root := fs.String("root", defaultRoot, "Directory (at any depth) under which registered repository paths are checked for existence")
	workspaceSlug := fs.String("workspace", graph.DefaultWorkspaceSlug, "Workspace whose selections and snapshots are pruned")
	dbURL := databaseFlag(fs, defaultDB)
	apply := fs.Bool("apply", false, "Remove stale repositories; without this flag, only list what would be removed")
	dryRun := fs.Bool("dry-run", false, "Deprecated: listing without removing is now the default; cannot be combined with -apply")
	fs.Usage = func() {
		fmt.Println("Usage: tirion prune [flags] [repos-root]")
		fmt.Println("  Lists (default) or removes (-apply) repositories of one workspace whose source directory under the root no longer exists.")
		fmt.Println("  Only that workspace's selections and snapshots are removed, and never snapshots another workspace still selects or depends on.")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	mustPositionals(fs, "prune", 0, 1)
	if *apply && *dryRun {
		log.Fatal("choose either -apply or -dry-run, not both")
	}
	if fs.NArg() > 0 {
		*root = fs.Arg(0)
	}

	rootPath, err := filepath.Abs(*root)
	if err != nil {
		log.Fatalf("resolve repo root: %v", err)
	}
	rootInfo, err := os.Stat(rootPath)
	if err != nil {
		log.Fatalf("read repo root: %v", err)
	}
	if !rootInfo.IsDir() {
		log.Fatalf("repo root is not a directory: %s", rootPath)
	}
	// An unmounted volume or an emptied checkout directory makes every
	// registered repository look deleted; never remove on that evidence.
	if entries, err := os.ReadDir(rootPath); *apply && err == nil && len(entries) == 0 {
		log.Fatalf("repo root %s is empty (unmounted?); refusing to remove every repository under it", rootPath)
	}
	roots := []string{rootPath}
	if real, err := filepath.EvalSymlinks(rootPath); err == nil && real != rootPath {
		roots = append(roots, real)
	}

	storage, err := graph.NewStorage(*dbURL)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer storage.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	stale, err := storage.PruneStaleRepos(ctx, *workspaceSlug, func(path string) bool { return pathUnderAnyRoot(roots, path) }, registeredPathMissing, *apply)
	if err != nil {
		log.Fatalf("prune failed: %v", err)
	}
	if len(stale) == 0 {
		fmt.Println("No stale repositories found.")
		return
	}
	for _, repo := range stale {
		fmt.Printf("stale repo: %s (%s)\n", repo.Name, repo.Path)
		if len(repo.RemovedSnapshots) > 0 {
			fmt.Printf("  snapshots %s: %v\n", pruneVerb(*apply, "removed", "to remove"), repo.RemovedSnapshots)
		}
		for _, kept := range repo.KeptSnapshots {
			fmt.Printf("  snapshot %d kept: %s\n", kept.ID, kept.Reason)
		}
		if repo.Selected {
			fmt.Printf("  workspace selection %s\n", pruneVerb(*apply, "removed", "to remove"))
		}
		if repo.RepositoryRemoved {
			fmt.Printf("  repository record and facts %s (no workspace uses it)\n", pruneVerb(*apply, "removed", "to remove"))
		}
	}
	if *apply {
		fmt.Printf("Pruned stale repositories in workspace %s: %d\n", *workspaceSlug, len(stale))
	} else {
		fmt.Printf("Stale repositories in workspace %s: %d (nothing removed; re-run with -apply to remove)\n", *workspaceSlug, len(stale))
	}
}

func pruneVerb(apply bool, done, plan string) string {
	if apply {
		return done
	}
	return plan
}

// pathUnderAnyRoot matches registered absolute paths at any depth below a root,
// so nested repositories (group/service) are considered, not just direct children.
func pathUnderAnyRoot(roots []string, path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	path = filepath.Clean(path)
	for _, root := range roots {
		if path != root && pathWithinRoot(root, path) {
			return true
		}
	}
	return false
}

// registeredPathMissing reports a vanished path. Any other stat failure (for
// example permission denied on a parent) is an error, never "missing".
func registeredPathMissing(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return false, nil
	}
	if os.IsNotExist(err) {
		return true, nil
	}
	return false, err
}

func pathWithinRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func shortSHA(sha string, max int) string {
	if max <= 0 || len(sha) <= max {
		return sha
	}
	return sha[:max]
}

func printUsage() {
	fmt.Println(`Tirion operator CLI

Usage:
  tirion index [flags] [repos-root]
  tirion workspace <list|create|set-ref|fetch|checkout|index> [flags]
  tirion search [flags] <query>
  tirion trace [flags] <function>
  tirion impact [flags]
  tirion verify [flags]
  tirion sync-branches [flags] [repos-root]
  tirion refresh-repo-graph [flags]
  tirion prune-snapshots [-older-than 720h] [-keep 2] [-apply]
  tirion prune [-workspace slug] [-apply] [flags] [repos-root]
  tirion serve [flags]
  tirion version

Examples:
  tirion index --root C:\tirion\repos
  tirion workspace create --from default-main release-investigation
  tirion workspace set-ref release-investigation resource-api release/next
  tirion workspace fetch release-investigation resource-api
  tirion workspace checkout --worktree-root /srv/tirion/workspaces release-investigation resource-api
  tirion workspace index --repo resource-api release-investigation
  tirion search --workspace release-investigation "findById"
  tirion trace --workspace release-investigation "ResourceRepository.findById"
  tirion impact --workspace release-investigation --functions ResourceService.save
  tirion verify --workspace release-investigation --repo resource-api --diff-file change.diff --work-file work.json
  tirion sync-branches --root C:\tirion\repos
  tirion refresh-repo-graph
  tirion prune --workspace release-investigation --root C:\tirion\repos
  tirion prune --workspace release-investigation --root C:\tirion\repos --apply
  tirion serve --port 8080`)
}
