package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sergiumoraru/tirion/internal/diffparse"
	"github.com/sergiumoraru/tirion/internal/runtimeconfig"
)

type impactRange struct {
	Repo      string `json:"repo,omitempty"`
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
}

type impactRequest struct {
	Diff         string        `json:"diff,omitempty"`
	Repo         string        `json:"repo,omitempty"`
	Functions    []string      `json:"functions,omitempty"`
	Ranges       []impactRange `json:"ranges,omitempty"`
	Depth        int           `json:"depth,omitempty"`
	NoTests      bool          `json:"noTests,omitempty"`
	Resolve      bool          `json:"resolve,omitempty"`
	Exclude      []string      `json:"exclude,omitempty"`
	IncludeRepos []string      `json:"includeRepos,omitempty"`
	ExcludeRepos []string      `json:"excludeRepos,omitempty"`
	MaxNodes     int           `json:"maxNodes,omitempty"`
	IncludeTrace bool          `json:"includeTrace,omitempty"`
}

type impactResponse struct {
	Warnings []string      `json:"warnings,omitempty"`
	Roots    []string      `json:"roots"`
	Summary  impactSummary `json:"summary"`
	Report   impactReport  `json:"report"`
	Stats    impactStats   `json:"stats"`
}

type impactStats struct {
	Roots           int    `json:"roots"`
	DownstreamNodes int    `json:"downstreamNodes"`
	UpstreamNodes   int    `json:"upstreamNodes"`
	TotalNodes      int    `json:"totalNodes"`
	TotalEdges      int    `json:"totalEdges"`
	ImpactTime      string `json:"impactTime"`
}

type impactSummary struct {
	Entrypoints []impactEndpoint `json:"entrypoints"`
	HttpCalls   []impactHttpCall `json:"httpCalls"`
	Queues      []impactQueue    `json:"queues"`
	Repos       []impactRepo     `json:"repos"`
}

type impactReport struct {
	Nodes []impactNode `json:"nodes"`
	Edges []impactEdge `json:"edges"`
}

type impactNode struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	Repo   string `json:"repo,omitempty"`
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	Queue  string `json:"queue,omitempty"`
}

type impactEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

type impactEndpoint struct {
	Method   string  `json:"method"`
	Path     string  `json:"path"`
	Handler  string  `json:"handler"`
	Repo     string  `json:"repo"`
	MinDepth int     `json:"minDepth"`
	Fanout   int     `json:"fanout"`
	Score    float64 `json:"score"`
}

type impactHttpCall struct {
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	MinDepth int               `json:"minDepth"`
	Fanout   int               `json:"fanout"`
	Score    float64           `json:"score"`
	Matches  []impactHttpMatch `json:"matches"`
}

type impactHttpMatch struct {
	Repo    string `json:"repo"`
	Handler string `json:"handler"`
	File    string `json:"file"`
	Line    int    `json:"line"`
}

type impactQueue struct {
	Name     string  `json:"name"`
	MinDepth int     `json:"minDepth"`
	Fanout   int     `json:"fanout"`
	Score    float64 `json:"score"`
}

type impactRepo struct {
	Name     string  `json:"name"`
	MinDepth int     `json:"minDepth"`
	Fanout   int     `json:"fanout"`
	Score    float64 `json:"score"`
}

type repoListResponse struct {
	Repos []repoInfo `json:"repos"`
}

type repoInfo struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	FileCount int    `json:"file_count"`
}

type reportOptions struct {
	Top         int
	MaxMatches  int
	TopEvidence int
	Title       string
	Resolve     bool
	NoTests     bool
	MaxNodes    int
	Depth       int
	Functions   []string
	RangesCount int
}

type gateOptions struct {
	MaxEntrypoints int
	MaxHttpCalls   int
	MaxQueues      int
	MaxRepos       int
}

func logProgress(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func progressPrefix(step, total int) string {
	if total <= 0 {
		return fmt.Sprintf("[%d]", step)
	}
	percent := int(math.Round(float64(step) / float64(total) * 100))
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return fmt.Sprintf("[%d/%d %d%%]", step, total, percent)
}

func main() {

	server := flag.String("server", "http://localhost:8080", "API server base URL")
	diffFile := flag.String("diff-file", "", "Path to unified diff file (or '-' for stdin)")
	functionsFlag := flag.String("functions", "", "Comma-separated function names")
	repo := flag.String("repo", "", "Repo name to apply to diff paths")
	ref := flag.String("ref", "", "Git ref/branch to parse and report against")
	repoPath := flag.String("repo-path", "", "Path to git repo (required with -ref)")
	snapshotDir := flag.String("snapshot-dir", ".codebase-snapshots", "Directory for repo snapshots")
	parseBin := flag.String("parse-bin", "./parse", "Path to parse binary")
	reindex := flag.Bool("reindex", false, "Rebuild snapshot even if present")
	timeout := flag.Duration("timeout", 90*time.Second, "HTTP timeout for impact API")
	depth := flag.Int("depth", 4, "Trace depth for impact analysis")
	noTests := flag.Bool("no-tests", true, "Hide nodes from test files")
	resolve := flag.Bool("resolve", true, "Resolve DI/impl")
	excludePatterns := flag.String("exclude", "", "Comma-separated substrings to exclude (file path or function name)")
	includeRepos := flag.String("include-repo", "", "Comma-separated repo names to include")
	excludeRepos := flag.String("exclude-repo", "", "Comma-separated repo names to exclude")
	maxNodes := flag.Int("max-nodes", 2000, "Max nodes to return")
	out := flag.String("out", "", "Output Markdown file (default stdout)")
	jsonOut := flag.String("json-out", "", "Optional raw JSON output file")
	title := flag.String("title", "Impact Report", "Markdown title")
	top := flag.Int("top", 5, "Max rows per section")
	topEvidence := flag.Int("top-evidence", 3, "Max evidence paths per section")
	maxMatches := flag.Int("max-matches", 3, "Max HTTP matches per call in report")
	failEntrypoints := flag.Int("fail-on-entrypoints", -1, "Fail if entrypoints count exceeds this (disabled if <0)")
	failHttp := flag.Int("fail-on-http", -1, "Fail if HTTP calls count exceeds this (disabled if <0)")
	failQueues := flag.Int("fail-on-queues", -1, "Fail if queues count exceeds this (disabled if <0)")
	failRepos := flag.Int("fail-on-repos", -1, "Fail if repos count exceeds this (disabled if <0)")
	flag.Parse()

	functions := parseList(*functionsFlag)
	effectiveRepo := *repo
	if *ref != "" {
		if *repoPath == "" {
			fmt.Fprintln(os.Stderr, "-repo-path is required when using -ref")
			os.Exit(1)
		}
		if *repo == "" {
			fmt.Fprintln(os.Stderr, "-repo is required when using -ref")
			os.Exit(1)
		}
		effectiveRepo = repoWithRef(*repo, *ref)
		logProgress("%s Snapshot: ensuring %s", progressPrefix(1, 4), effectiveRepo)
		if err := ensureSnapshot(*server, *parseBin, *repoPath, *snapshotDir, effectiveRepo, *ref, *reindex); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		logProgress("%s Snapshot ready", progressPrefix(1, 4))
	}

	diffData, err := readDiffInput(*diffFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ranges, err := parseUnifiedDiff(diffData, effectiveRepo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "diff cannot be parsed: %v\n", err)
		os.Exit(1)
	}
	logProgress("%s Targets: %d range(s), %d function(s)", progressPrefix(2, 4), len(ranges), len(functions))

	if len(functions) == 0 && strings.TrimSpace(diffData) == "" {
		fmt.Fprintln(os.Stderr, "Provide functions via -functions or diff ranges via -diff-file/stdin.")
		flag.Usage()
		os.Exit(1)
	}

	req := impactRequest{
		Functions:    functions,
		Diff:         diffData,
		Repo:         effectiveRepo,
		Depth:        *depth,
		NoTests:      *noTests,
		Resolve:      *resolve,
		Exclude:      parseList(*excludePatterns),
		IncludeRepos: parseList(*includeRepos),
		ExcludeRepos: parseList(*excludeRepos),
		MaxNodes:     *maxNodes,
		IncludeTrace: false,
	}

	logProgress("%s Posting /api/impact (timeout %s)", progressPrefix(3, 4), timeout.String())
	impactStart := time.Now()
	respBytes, resp, err := postImpact(*server, req, *timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	logProgress("%s Impact response received in %s", progressPrefix(3, 4), time.Since(impactStart).Truncate(time.Millisecond))

	if *jsonOut != "" {
		if err := os.WriteFile(*jsonOut, respBytes, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "failed to write json output: %v\n", err)
			os.Exit(1)
		}
	}

	opts := reportOptions{
		Top:         *top,
		MaxMatches:  *maxMatches,
		TopEvidence: *topEvidence,
		Title:       *title,
		Resolve:     *resolve,
		NoTests:     *noTests,
		MaxNodes:    *maxNodes,
		Depth:       *depth,
		Functions:   functions,
		RangesCount: len(ranges),
	}
	report := buildMarkdownReport(resp, opts)
	if err := writeOutput(*out, report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *out != "" {
		logProgress("%s Wrote report to %s", progressPrefix(4, 4), *out)
	} else {
		logProgress("%s Report written to stdout", progressPrefix(4, 4))
	}

	gate := gateOptions{
		MaxEntrypoints: *failEntrypoints,
		MaxHttpCalls:   *failHttp,
		MaxQueues:      *failQueues,
		MaxRepos:       *failRepos,
	}
	if err := enforceGates(resp, gate); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func postImpact(server string, req impactRequest, timeout time.Duration) ([]byte, impactResponse, error) {
	base := strings.TrimRight(server, "/")
	endpoint := base + "/api/impact"
	if strings.HasSuffix(base, "/api") {
		endpoint = base + "/impact"
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, impactResponse{}, err
	}

	token, err := runtimeconfig.ClientToken(server)
	if err != nil {
		return nil, impactResponse{}, err
	}
	client := &http.Client{Timeout: timeout, CheckRedirect: runtimeconfig.NoRedirect}
	httpReq, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, impactResponse{}, err
	}
	httpReq.Header.Set("X-Tirion-Token", token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, impactResponse{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, impactResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, impactResponse{}, fmt.Errorf("impact request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var decoded impactResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, impactResponse{}, err
	}
	return respBody, decoded, nil
}

func buildMarkdownReport(resp impactResponse, opts reportOptions) string {
	var out strings.Builder
	writeHeader(&out, opts, resp)
	groups := groupRootsByFile(resp.Roots, resp.Report.Nodes)
	writeChangedTargets(&out, resp, opts, groups)
	writeWhatChanged(&out, resp, groups)
	writeImpactSummary(&out, resp)

	writeSection(&out, "User-facing entrypoints", opts.Top, resp.Summary.Entrypoints, func(item impactEndpoint) string {
		return fmt.Sprintf("%s %s \u2192 %s.%s (depth %d, fanout %d, score %.2f)",
			item.Method, item.Path, cleanRepoName(item.Repo), item.Handler, item.MinDepth, item.Fanout, item.Score)
	})
	writeEntrypointEvidence(&out, resp, opts)

	writeSection(&out, "Outbound HTTP calls", opts.Top, resp.Summary.HttpCalls, func(item impactHttpCall) string {
		matchSummary := formatHttpMatches(item.Matches, opts.MaxMatches)
		if matchSummary != "" {
			return fmt.Sprintf("%s %s (depth %d, fanout %d, score %.2f) | matches: %s",
				item.Method, item.Path, item.MinDepth, item.Fanout, item.Score, matchSummary)
		}
		return fmt.Sprintf("%s %s (depth %d, fanout %d, score %.2f)",
			item.Method, item.Path, item.MinDepth, item.Fanout, item.Score)
	})
	writeHttpEvidence(&out, resp, opts)

	writeSection(&out, "Queues touched", opts.Top, resp.Summary.Queues, func(item impactQueue) string {
		return fmt.Sprintf("%s (depth %d, fanout %d, score %.2f)",
			item.Name, item.MinDepth, item.Fanout, item.Score)
	})
	writeQueueEvidence(&out, resp, opts)

	writeSection(&out, "Repos touched", opts.Top, resp.Summary.Repos, func(item impactRepo) string {
		return fmt.Sprintf("%s (depth %d, fanout %d, score %.2f)",
			cleanRepoName(item.Name), item.MinDepth, item.Fanout, item.Score)
	})

	out.WriteString("\n")
	return out.String()
}

func writeHeader(out *strings.Builder, opts reportOptions, resp impactResponse) {
	out.WriteString("# ")
	out.WriteString(opts.Title)
	out.WriteString("\n\n")

	targets := summarizeTargets(opts)
	if targets != "" {
		out.WriteString("Targets: ")
		out.WriteString(targets)
		out.WriteString("\n")
	}

	out.WriteString(fmt.Sprintf("Trace: depth %d | resolve %t | hide tests %t | max nodes %d\n",
		opts.Depth, opts.Resolve, opts.NoTests, opts.MaxNodes))
	out.WriteString(fmt.Sprintf("Impact graph: downstream %d, upstream %d, total %d | edges %d | time %s\n\n",
		resp.Stats.DownstreamNodes, resp.Stats.UpstreamNodes, resp.Stats.TotalNodes, resp.Stats.TotalEdges, resp.Stats.ImpactTime))
	for _, warning := range resp.Warnings {
		out.WriteString("Warning: " + strings.ReplaceAll(warning, "\n", " ") + "\n\n")
	}
}

func summarizeTargets(opts reportOptions) string {
	var parts []string
	if len(opts.Functions) > 0 {
		parts = append(parts, fmt.Sprintf("functions: %s", strings.Join(opts.Functions, ", ")))
	}
	if opts.RangesCount > 0 {
		parts = append(parts, fmt.Sprintf("diff ranges: %d", opts.RangesCount))
	}
	return strings.Join(parts, " | ")
}

func writeChangedTargets(out *strings.Builder, resp impactResponse, opts reportOptions, groups []rootGroup) {
	if len(resp.Roots) == 0 || len(groups) == 0 {
		return
	}
	out.WriteString("## Changed code targets\n")
	out.WriteString(fmt.Sprintf("- %d files, %d functions from diff ranges\n", len(groups), len(resp.Roots)))
	top := opts.Top
	if top <= 0 || top > len(groups) {
		top = len(groups)
	}
	for i := 0; i < top; i++ {
		group := groups[i]
		sample := sampleList(group.Functions, 3)
		if extra := len(group.Functions) - len(sample); extra > 0 {
			sample = append(sample, fmt.Sprintf("+%d more", extra))
		}
		out.WriteString(fmt.Sprintf("- %s:%s (%d)\n", group.Repo, group.File, group.Count))
		if len(sample) > 0 {
			out.WriteString(fmt.Sprintf("  functions: %s\n", strings.Join(sample, ", ")))
		}
	}
	out.WriteString("\n")
}

func writeImpactSummary(out *strings.Builder, resp impactResponse) {
	out.WriteString("## Impact summary\n")
	out.WriteString(fmt.Sprintf("- User-facing entrypoints: %d\n", len(resp.Summary.Entrypoints)))
	out.WriteString(fmt.Sprintf("- Outbound HTTP calls: %d\n", len(resp.Summary.HttpCalls)))
	out.WriteString(fmt.Sprintf("- Queues touched: %d\n", len(resp.Summary.Queues)))
	out.WriteString(fmt.Sprintf("- Repos touched: %d\n", len(resp.Summary.Repos)))
	out.WriteString("\n")
}

func writeWhatChanged(out *strings.Builder, resp impactResponse, groups []rootGroup) {
	if len(groups) == 0 {
		return
	}
	areas := summarizeChangeAreas(groups)
	if len(areas) == 0 {
		return
	}
	out.WriteString("## What changed\n")
	out.WriteString(fmt.Sprintf("Primary change areas: %s.\n", strings.Join(areas, ", ")))
	out.WriteString(fmt.Sprintf("Impact signals: %d entrypoints, %d outbound HTTP calls, %d queues.\n\n",
		len(resp.Summary.Entrypoints), len(resp.Summary.HttpCalls), len(resp.Summary.Queues)))
}

type rootGroup struct {
	Repo      string
	File      string
	Count     int
	Functions []string
}

func groupRootsByFile(roots []string, nodes []impactNode) []rootGroup {
	nodeIndex := make(map[string]impactNode, len(nodes))
	for _, node := range nodes {
		nodeIndex[node.ID] = node
	}

	grouped := make(map[string]*rootGroup)
	for _, root := range roots {
		rawRepo, file, fn := splitRoot(root)
		repo := cleanRepoName(rawRepo)
		if repo == "" || file == "" || fn == "" {
			continue
		}
		key := repo + "|" + file
		group := grouped[key]
		if group == nil {
			group = &rootGroup{Repo: repo, File: file}
			grouped[key] = group
		}
		group.Count++
		label := fn
		if node, ok := nodeIndex["func:"+root]; ok && node.Line > 0 {
			label = fmt.Sprintf("%s:%d", fn, node.Line)
		}
		group.Functions = append(group.Functions, label)
	}

	out := make([]rootGroup, 0, len(grouped))
	for _, group := range grouped {
		sort.Strings(group.Functions)
		out = append(out, *group)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].File < out[j].File
	})
	return out
}

func splitRoot(root string) (string, string, string) {
	first := strings.Index(root, ":")
	last := strings.LastIndex(root, ":")
	if first == -1 || last == -1 || first == last {
		return "", "", ""
	}
	return root[:first], root[first+1 : last], root[last+1:]
}

func sampleList(values []string, limit int) []string {
	if limit <= 0 || len(values) <= limit {
		return values
	}
	return values[:limit]
}

type areaSummary struct {
	Label string
	Count int
}

func summarizeChangeAreas(groups []rootGroup) []string {
	repoSet := make(map[string]struct{})
	for _, group := range groups {
		if group.Repo == "" {
			continue
		}
		repoSet[group.Repo] = struct{}{}
	}
	multiRepo := len(repoSet) > 1

	areaCounts := make(map[string]int)
	for _, group := range groups {
		label := summarizePathArea(group.File)
		if label == "" {
			continue
		}
		if multiRepo {
			label = fmt.Sprintf("%s · %s", group.Repo, label)
		}
		areaCounts[label] += group.Count
	}

	areas := make([]areaSummary, 0, len(areaCounts))
	for label, count := range areaCounts {
		areas = append(areas, areaSummary{Label: label, Count: count})
	}
	sort.Slice(areas, func(i, j int) bool {
		if areas[i].Count != areas[j].Count {
			return areas[i].Count > areas[j].Count
		}
		return areas[i].Label < areas[j].Label
	})

	limit := 3
	if len(areas) < limit {
		limit = len(areas)
	}
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, areas[i].Label)
	}
	return out
}

func summarizePathArea(file string) string {
	if file == "" {
		return ""
	}
	base := filepath.Base(file)
	dir := filepath.Dir(file)
	parent := filepath.Base(dir)
	if parent == "." || parent == string(filepath.Separator) {
		return base
	}
	if parent == "" || parent == "." || parent == string(filepath.Separator) {
		return base
	}
	return parent + "/" + base
}

func cleanRepoName(name string) string {
	if name == "" {
		return ""
	}
	if idx := strings.Index(name, "@"); idx > 0 {
		return name[:idx]
	}
	return name
}

func writeSection[T any](out *strings.Builder, title string, top int, items []T, format func(T) string) {
	count := len(items)
	if top <= 0 {
		top = count
	}
	if top > count {
		top = count
	}
	header := title
	if count > 0 {
		header = fmt.Sprintf("%s (top %d of %d)", title, top, count)
	}
	out.WriteString("## ")
	out.WriteString(header)
	out.WriteString("\n")

	if count == 0 {
		out.WriteString("- None\n\n")
		return
	}

	for idx := 0; idx < top; idx++ {
		out.WriteString("- ")
		out.WriteString(format(items[idx]))
		out.WriteString("\n")
	}
	out.WriteString("\n")
}

func formatHttpMatches(matches []impactHttpMatch, maxMatches int) string {
	if len(matches) == 0 {
		return ""
	}
	if maxMatches <= 0 {
		maxMatches = len(matches)
	}
	preview := matches
	if len(preview) > maxMatches {
		preview = matches[:maxMatches]
	}
	var parts []string
	for _, match := range preview {
		if match.Repo == "" && match.Handler == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s.%s", cleanRepoName(match.Repo), match.Handler))
	}
	sort.Strings(parts)
	if len(matches) > maxMatches {
		parts = append(parts, fmt.Sprintf("+%d more", len(matches)-maxMatches))
	}
	return strings.Join(parts, ", ")
}

func writeHttpEvidence(out *strings.Builder, resp impactResponse, opts reportOptions) {
	if len(resp.Summary.HttpCalls) == 0 {
		return
	}
	paths := buildEvidencePaths(resp)
	if len(paths.http) == 0 {
		return
	}
	out.WriteString("### HTTP Evidence (sample paths)\n")
	limit := evidenceLimit(opts.TopEvidence)
	shown := 0
	for _, call := range resp.Summary.HttpCalls {
		if shown >= limit {
			break
		}
		key := httpKey(call.Method, call.Path)
		path, ok := paths.http[key]
		if !ok || path == "" {
			continue
		}
		out.WriteString("- ")
		out.WriteString(call.Method)
		out.WriteString(" ")
		out.WriteString(call.Path)
		out.WriteString(": ")
		out.WriteString(path)
		out.WriteString("\n")
		shown++
	}
	out.WriteString("\n")
}

func writeQueueEvidence(out *strings.Builder, resp impactResponse, opts reportOptions) {
	if len(resp.Summary.Queues) == 0 {
		return
	}
	paths := buildEvidencePaths(resp)
	if len(paths.queues) == 0 {
		return
	}
	out.WriteString("### Queue Evidence (sample paths)\n")
	limit := evidenceLimit(opts.TopEvidence)
	shown := 0
	for _, queue := range resp.Summary.Queues {
		if shown >= limit {
			break
		}
		path, ok := paths.queues[queue.Name]
		if !ok || path == "" {
			continue
		}
		out.WriteString("- ")
		out.WriteString(queue.Name)
		out.WriteString(": ")
		out.WriteString(path)
		out.WriteString("\n")
		shown++
	}
	out.WriteString("\n")
}

func writeEntrypointEvidence(out *strings.Builder, resp impactResponse, opts reportOptions) {
	if len(resp.Summary.Entrypoints) == 0 {
		return
	}
	paths := buildEvidencePaths(resp)
	if len(paths.entrypoints) == 0 {
		return
	}
	out.WriteString("### Entrypoint Evidence (sample paths)\n")
	limit := evidenceLimit(opts.TopEvidence)
	shown := 0
	for _, entry := range resp.Summary.Entrypoints {
		if shown >= limit {
			break
		}
		key := entrypointNodeKey(entry.Repo, entry.Handler)
		path, ok := paths.entrypoints[key]
		if !ok || path == "" {
			continue
		}
		out.WriteString("- ")
		out.WriteString(entry.Method)
		out.WriteString(" ")
		out.WriteString(entry.Path)
		out.WriteString(": ")
		out.WriteString(path)
		out.WriteString("\n")
		shown++
	}
	out.WriteString("\n")
}

func evidenceLimit(value int) int {
	if value <= 0 {
		return 3
	}
	return value
}

type evidencePaths struct {
	http        map[string]string
	queues      map[string]string
	entrypoints map[string]string
}

func buildEvidencePaths(resp impactResponse) evidencePaths {
	graph := buildGraph(resp.Report)
	if len(graph.nodes) == 0 {
		return evidencePaths{
			http:        map[string]string{},
			queues:      map[string]string{},
			entrypoints: map[string]string{},
		}
	}
	roots := rootNodeIDs(resp.Roots, graph.nodes)
	parentDown := bfsParentsFromEdges(graph.edges, roots)
	parentUp := bfsParentsFromEdges(reverseEdges(graph.edges), roots)

	httpPaths := make(map[string]string)
	queuePaths := make(map[string]string)

	httpIndex := indexHttpNodes(graph.nodes)
	for _, call := range resp.Summary.HttpCalls {
		key := httpKey(call.Method, call.Path)
		nodeIDs := httpIndex[key]
		for _, nodeID := range nodeIDs {
			path := buildPath(nodeID, parentDown, graph.nodes)
			if path != "" {
				httpPaths[key] = path
				break
			}
		}
	}

	queueIndex := indexQueueNodes(graph.nodes)
	for _, queue := range resp.Summary.Queues {
		nodeIDs := queueIndex[queue.Name]
		for _, nodeID := range nodeIDs {
			path := buildPath(nodeID, parentDown, graph.nodes)
			if path != "" {
				queuePaths[queue.Name] = path
				break
			}
		}
	}

	entrypointPaths := make(map[string]string)
	entryIndex := indexEntrypointNodes(graph.nodes)
	for _, entry := range resp.Summary.Entrypoints {
		key := entrypointNodeKey(entry.Repo, entry.Handler)
		nodeIDs := entryIndex[key]
		for _, nodeID := range nodeIDs {
			path := buildPath(nodeID, parentUp, graph.nodes)
			if path != "" {
				entrypointPaths[key] = path
				break
			}
		}
	}

	return evidencePaths{
		http:        httpPaths,
		queues:      queuePaths,
		entrypoints: entrypointPaths,
	}
}

type graphData struct {
	nodes map[string]impactNode
	edges map[string][]string
}

func buildGraph(report impactReport) graphData {
	nodes := make(map[string]impactNode, len(report.Nodes))
	for _, node := range report.Nodes {
		nodes[node.ID] = node
	}
	edges := make(map[string][]string, len(report.Edges))
	for _, edge := range report.Edges {
		edges[edge.Source] = append(edges[edge.Source], edge.Target)
	}
	for _, targets := range edges {
		sort.Strings(targets)
	}
	return graphData{nodes: nodes, edges: edges}
}

func rootNodeIDs(roots []string, nodes map[string]impactNode) []string {
	var out []string
	for _, root := range roots {
		id := "func:" + root
		if _, ok := nodes[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

func bfsParentsFromEdges(edges map[string][]string, roots []string) map[string]string {
	parent := make(map[string]string)
	visited := make(map[string]bool)
	queue := make([]string, 0, len(roots))
	for _, root := range roots {
		if visited[root] {
			continue
		}
		visited[root] = true
		queue = append(queue, root)
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range edges[current] {
			if visited[next] {
				continue
			}
			visited[next] = true
			parent[next] = current
			queue = append(queue, next)
		}
	}
	return parent
}

func reverseEdges(edges map[string][]string) map[string][]string {
	reversed := make(map[string][]string)
	for source, targets := range edges {
		for _, target := range targets {
			reversed[target] = append(reversed[target], source)
		}
	}
	for _, sources := range reversed {
		sort.Strings(sources)
	}
	return reversed
}

func buildPath(target string, parent map[string]string, nodes map[string]impactNode) string {
	if _, ok := nodes[target]; !ok {
		return ""
	}
	var pathIDs []string
	current := target
	pathIDs = append(pathIDs, current)
	for {
		next, ok := parent[current]
		if !ok {
			break
		}
		current = next
		pathIDs = append(pathIDs, current)
	}
	if len(pathIDs) < 2 {
		return ""
	}
	for i, j := 0, len(pathIDs)-1; i < j; i, j = i+1, j-1 {
		pathIDs[i], pathIDs[j] = pathIDs[j], pathIDs[i]
	}
	return formatPath(pathIDs, nodes)
}

func formatPath(pathIDs []string, nodes map[string]impactNode) string {
	if len(pathIDs) == 0 {
		return ""
	}
	labels := make([]string, 0, len(pathIDs))
	for _, id := range pathIDs {
		node := nodes[id]
		label := formatNodeLabel(node)
		if label != "" {
			labels = append(labels, label)
		}
	}
	if len(labels) <= 4 {
		return strings.Join(labels, " → ")
	}
	return strings.Join([]string{labels[0], "…", labels[len(labels)-2], labels[len(labels)-1]}, " → ")
}

func formatNodeLabel(node impactNode) string {
	switch node.Type {
	case "function":
		return fmt.Sprintf("%s (%s:%d)", node.Name, shortFile(node), node.Line)
	case "http_call":
		return fmt.Sprintf("HTTP %s %s", node.Method, node.Path)
	case "sqs":
		queue := node.Queue
		if queue == "" {
			queue = node.Name
		}
		return fmt.Sprintf("SQS %s", queue)
	default:
		if node.Name != "" {
			return node.Name
		}
	}
	return ""
}

func shortFile(node impactNode) string {
	if node.Repo == "" && node.File == "" {
		return ""
	}
	if node.Repo == "" {
		return node.File
	}
	if node.File == "" {
		return cleanRepoName(node.Repo)
	}
	return fmt.Sprintf("%s:%s", cleanRepoName(node.Repo), node.File)
}

func indexHttpNodes(nodes map[string]impactNode) map[string][]string {
	index := make(map[string][]string)
	for id, node := range nodes {
		if node.Type != "http_call" {
			continue
		}
		key := httpKey(node.Method, node.Path)
		if key == "" {
			name := strings.TrimSpace(node.Name)
			if strings.HasPrefix(name, "[") && strings.HasSuffix(name, "]") {
				name = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(name, "["), "]"))
			}
			key = name
		}
		if key == "" {
			continue
		}
		index[key] = append(index[key], id)
	}
	for _, ids := range index {
		sort.Strings(ids)
	}
	return index
}

func indexQueueNodes(nodes map[string]impactNode) map[string][]string {
	index := make(map[string][]string)
	for id, node := range nodes {
		if node.Type != "sqs" {
			continue
		}
		queue := node.Queue
		if queue == "" {
			queue = node.Name
		}
		if queue == "" {
			continue
		}
		index[queue] = append(index[queue], id)
	}
	for _, ids := range index {
		sort.Strings(ids)
	}
	return index
}

func httpKey(method, path string) string {
	if method == "" && path == "" {
		return ""
	}
	return strings.TrimSpace(method + " " + path)
}

func indexEntrypointNodes(nodes map[string]impactNode) map[string][]string {
	index := make(map[string][]string)
	for id, node := range nodes {
		if node.Type != "function" || node.Name == "" {
			continue
		}
		key := entrypointNodeKey(node.Repo, node.Name)
		index[key] = append(index[key], id)
	}
	for _, ids := range index {
		sort.Strings(ids)
	}
	return index
}

func entrypointNodeKey(repo, handler string) string {
	if repo == "" || handler == "" {
		return ""
	}
	return fmt.Sprintf("%s|%s", repo, handler)
}

func enforceGates(resp impactResponse, gate gateOptions) error {
	gated := gate.MaxEntrypoints >= 0 || gate.MaxHttpCalls >= 0 || gate.MaxQueues >= 0 || gate.MaxRepos >= 0
	// A gate fails closed: zero counts mean nothing only when the change mapped
	// to indexed code. Informational notes (added files) do not count as gaps.
	if gated && len(resp.Roots) == 0 {
		var gaps []string
		for _, warning := range resp.Warnings {
			if !strings.HasSuffix(warning, "(informational)") && !strings.HasPrefix(warning, "no matching functions for impact targets") {
				gaps = append(gaps, warning)
			}
		}
		if len(gaps) > 0 {
			return fmt.Errorf("impact could not be determined; no indexed functions matched the change: %s", strings.Join(gaps, "; "))
		}
	}
	if gate.MaxEntrypoints >= 0 && len(resp.Summary.Entrypoints) > gate.MaxEntrypoints {
		return fmt.Errorf("entrypoints %d exceed limit %d", len(resp.Summary.Entrypoints), gate.MaxEntrypoints)
	}
	if gate.MaxHttpCalls >= 0 && len(resp.Summary.HttpCalls) > gate.MaxHttpCalls {
		return fmt.Errorf("http calls %d exceed limit %d", len(resp.Summary.HttpCalls), gate.MaxHttpCalls)
	}
	if gate.MaxQueues >= 0 && len(resp.Summary.Queues) > gate.MaxQueues {
		return fmt.Errorf("queues %d exceed limit %d", len(resp.Summary.Queues), gate.MaxQueues)
	}
	if gate.MaxRepos >= 0 && len(resp.Summary.Repos) > gate.MaxRepos {
		return fmt.Errorf("repos %d exceed limit %d", len(resp.Summary.Repos), gate.MaxRepos)
	}
	return nil
}

func writeOutput(path, content string) error {
	if path == "" {
		_, err := io.Copy(os.Stdout, strings.NewReader(content))
		return err
	}
	return os.WriteFile(path, []byte(content), 0644)
}

func parseList(input string) []string {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	parts := strings.Split(input, ",")
	var out []string
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func readDiffInput(path string) (string, error) {
	if path == "" {
		if stdinHasData() {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return "", fmt.Errorf("failed to read diff from stdin: %w", err)
			}
			return string(data), nil
		}
		return "", nil
	}
	if path == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("failed to read diff from stdin: %w", err)
		}
		return string(data), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read diff file: %w", err)
	}
	return string(data), nil
}

func stdinHasData() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice == 0
}

// parseUnifiedDiff returns the edited line ranges in pre-image coordinates (what
// the index holds). Only hunk bodies are interpreted, so added content that looks
// like a header ("+++ x") cannot switch files. repo may be empty: the server then
// attributes each path to the repository that indexes it.
func parseUnifiedDiff(diff string, repo string) ([]impactRange, error) {
	if strings.TrimSpace(diff) == "" {
		return nil, nil
	}
	files, err := diffparse.Parse(diff)
	if err != nil {
		return nil, err
	}
	var ranges []impactRange
	for _, file := range files {
		if file.New {
			continue
		}
		for _, edit := range file.Edits() {
			start := max(edit.Start, 1)
			ranges = append(ranges, impactRange{
				Repo:      repo,
				Path:      edit.Path,
				StartLine: start,
				EndLine:   max(edit.End, start),
			})
		}
	}
	return ranges, nil
}

func repoWithRef(repo, ref string) string {
	return fmt.Sprintf("%s@%s", repo, sanitizeRef(ref))
}

func sanitizeRef(ref string) string {
	if ref == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range ref {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func ensureSnapshot(server, parseBin, repoPath, snapshotDir, repoNameRef, ref string, reindex bool) error {
	if !reindex {
		exists, err := repoExists(server, repoNameRef)
		if err == nil && exists {
			return nil
		}
	}

	repoRoot, err := gitRepoRoot(repoPath)
	if err != nil {
		return err
	}
	if err := gitRefExists(repoRoot, ref); err != nil {
		return err
	}

	if snapshotDir == "" {
		snapshotDir = ".codebase-snapshots"
	}
	snapshotBase := snapshotDir
	if !filepath.IsAbs(snapshotBase) {
		snapshotBase = filepath.Join(repoRoot, snapshotBase)
	}
	snapshotPath := filepath.Join(snapshotBase, repoNameRef)
	if err := os.MkdirAll(snapshotBase, 0755); err != nil {
		return fmt.Errorf("failed to create snapshot dir: %w", err)
	}

	if _, err := os.Stat(snapshotPath); err == nil {
		ok, err := isGitWorktree(snapshotPath)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("snapshot path exists but is not a git worktree: %s", snapshotPath)
		}
		if err := gitCheckout(snapshotPath, ref); err != nil {
			return err
		}
	} else if os.IsNotExist(err) {
		if err := gitWorktreeAdd(repoRoot, snapshotPath, ref); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("failed to access snapshot path: %w", err)
	}

	return runParse(parseBin, snapshotPath)
}

func repoExists(server, repoName string) (bool, error) {
	req, err := http.NewRequest("GET", strings.TrimRight(server, "/")+"/api/repos", nil)
	if err != nil {
		return false, err
	}
	token, err := runtimeconfig.ClientToken(server)
	if err != nil {
		return false, err
	}
	req.Header.Set("X-Tirion-Token", token)
	client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: runtimeconfig.NoRedirect}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("failed to fetch repos: %s", resp.Status)
	}
	var payload repoListResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return false, err
	}
	for _, repo := range payload.Repos {
		if repo.Name == repoName && repo.FileCount > 0 {
			return true, nil
		}
	}
	return false, nil
}

// gitCommand runs git with inherited Git overrides and credentials removed.
func gitCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Env = runtimeconfig.GitEnvironment()
	return cmd
}

func runParse(parseBin, snapshotPath string) error {
	cmd := exec.Command(parseBin, "-v", snapshotPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// The parse child needs the database URL, never API or GitHub credentials.
	cmd.Env = runtimeconfig.ChildEnvironment()
	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		cmd.Env = append(cmd.Env, "DATABASE_URL="+dbURL)
	}
	return cmd.Run()
}

func gitRepoRoot(repoPath string) (string, error) {
	out, err := gitCommand("-C", repoPath, "rev-parse", "--show-toplevel").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to resolve git repo root: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func gitRefExists(repoRoot, ref string) error {
	out, err := gitCommand("-C", repoRoot, "rev-parse", "--verify", ref).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git ref not found (%s): %s", ref, strings.TrimSpace(string(out)))
	}
	return nil
}

func isGitWorktree(path string) (bool, error) {
	out, err := gitCommand("-C", path, "rev-parse", "--is-inside-work-tree").CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to check worktree at %s: %s", path, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

func gitWorktreeAdd(repoRoot, snapshotPath, ref string) error {
	out, err := gitCommand("-C", repoRoot, "worktree", "add", snapshotPath, ref).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create worktree: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func gitCheckout(path, ref string) error {
	out, err := gitCommand("-C", path, "checkout", ref).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to checkout %s in worktree: %s", ref, strings.TrimSpace(string(out)))
	}
	return nil
}
