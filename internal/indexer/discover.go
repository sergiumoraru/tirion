package indexer

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sergiumoraru/tirion/internal/parser"
	"github.com/sergiumoraru/tirion/internal/sourcepath"
)

const (
	// Containers (directories that are not repos themselves) are searched this
	// many levels below each direct child of the repos root.
	nestedRepoDepth = 2
	// Depth used to decide that a manifest-free directory holds indexable source.
	indexableSourceDepth = 4
	// Dropped paths named in a single warning before the list is truncated.
	maxDroppedPathsShown = 8
)

// repoManifests make a directory a repository boundary on their own.
var repoManifests = []string{
	"package.json", "go.mod", "pom.xml",
	"build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts",
}

// DiscoverRepos finds repository boundaries under root. See
// DiscoverReposWithWarnings for the non-fatal conditions it reports.
func DiscoverRepos(root string, excluded map[string]bool) ([]RepoEntry, error) {
	repos, _, err := DiscoverReposWithWarnings(root, excluded)
	return repos, err
}

// DiscoverReposWithWarnings returns the repositories under root plus warnings
// for conditions that did not stop discovery but that an operator should see:
// unreadable or broken directories, symlink loops, and indexable source inside a
// multi-repository container that no discovered repository covers (it will not
// be indexed). Only an unreadable root is an error.
//
// Symlinked directories are followed; the repository path is the resolved
// directory (the parser does not walk a symlink root) and the name is the link
// name. Repositories are deduplicated by real path, with real directories
// winning over links to them. Symlinks inside a repository are never followed,
// and a symlink below the root's direct children is followed only when it
// resolves inside the root: a checked-out container must not pull outside
// directories into the index.
func DiscoverReposWithWarnings(root string, excluded map[string]bool) ([]RepoEntry, []string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, err
	}
	d := &discovery{excluded: excluded, seenRepos: map[string]bool{}, seenWarnings: map[string]bool{}}
	d.probe.warn = d.warnf
	stack := []string{d.realPath(root)}
	for _, child := range d.childDirs(root, "", entries, "") {
		d.visitTopLevel(child, stack)
	}
	d.dropOverlapping()
	slices.SortFunc(d.repos, func(a, b RepoEntry) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return d.repos, d.warnings, nil
}

type discovery struct {
	excluded     map[string]bool
	probe        repoProbe
	repos        []RepoEntry
	warnings     []string
	seenRepos    map[string]bool
	seenWarnings map[string]bool
}

type childDir struct {
	name, path string
}

func (d *discovery) warnf(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	if d.seenWarnings[message] {
		return
	}
	d.seenWarnings[message] = true
	d.warnings = append(d.warnings, message)
}

func (d *discovery) realPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return filepath.Clean(path)
}

// skipped applies the same directory policy as parsing (sourcepath.SkipDir), so
// build/dist/coverage may be repositories or source unless they sit beside a
// build manifest, plus hidden directories and explicit exclusions.
func (d *discovery) skipped(parent, name, prefix string) bool {
	if strings.HasPrefix(name, ".") || d.excluded[name] || sourcepath.SkipDir(parent, name) {
		return true
	}
	return prefix != "" && d.excluded[prefix+"/"+name]
}

// childDirs lists the candidate directories of dir, following symlinks that
// resolve to directories (inside confine, when set). Real directories come first
// so that a link to a directory already present is dropped as a duplicate rather
// than the reverse.
func (d *discovery) childDirs(dir, prefix string, entries []os.DirEntry, confine string) []childDir {
	var real, linked []childDir
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(dir, name)
		switch {
		case entry.IsDir():
			if !d.skipped(dir, name, prefix) {
				real = append(real, childDir{name: name, path: path})
			}
		case entry.Type()&os.ModeSymlink != 0:
			if d.skipped(dir, name, prefix) {
				continue
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				d.warnf("skipping %s: broken or looping symlink: %v", path, err)
				continue
			}
			// An exclusion names a directory; a link must not re-admit it.
			if target := filepath.Base(resolved); d.excluded[target] || (prefix != "" && d.excluded[prefix+"/"+target]) {
				continue
			}
			info, err := os.Stat(resolved)
			if err != nil {
				d.warnf("skipping %s: %v", path, err)
				continue
			}
			if !info.IsDir() {
				continue
			}
			if confine != "" {
				if rel, err := filepath.Rel(confine, resolved); err != nil || !filepath.IsLocal(rel) {
					d.warnf("skipping %s: symlink resolves outside the repository root (%s)", path, resolved)
					continue
				}
			}
			linked = append(linked, childDir{name: name, path: resolved})
		}
	}
	return append(real, linked...)
}

func (d *discovery) add(name, path string) {
	key := d.realPath(path)
	if d.seenRepos[key] {
		return
	}
	d.seenRepos[key] = true
	d.repos = append(d.repos, RepoEntry{Name: name, Path: path})
}

// dropOverlapping removes repositories whose real path lies inside another
// discovered repository (only symlinks can produce this). Indexing the same files
// twice would duplicate declarations and break unique-name call resolution.
func (d *discovery) dropOverlapping() {
	real := make([]string, len(d.repos))
	for i, repo := range d.repos {
		real[i] = d.realPath(repo.Path)
	}
	kept := d.repos[:0:0]
	for i, repo := range d.repos {
		container := ""
		for j := range d.repos {
			if i == j {
				continue
			}
			if rel, err := filepath.Rel(real[j], real[i]); err == nil && rel != "." && filepath.IsLocal(rel) {
				container = d.repos[j].Name
				break
			}
		}
		if container != "" {
			d.warnf("skipping %s: it resolves inside repository %s (%s)", repo.Name, container, real[i])
			continue
		}
		kept = append(kept, repo)
	}
	d.repos = kept
}

func onStack(stack []string, real string) bool {
	return slices.Contains(stack, real)
}

func (d *discovery) visitTopLevel(child childDir, stack []string) {
	real := d.realPath(child.path)
	if onStack(stack, real) {
		d.warnf("skipping %s: symlink loops back to an ancestor directory (%s)", child.path, real)
		return
	}
	ok, err := d.probe.looksLikeCodeRepo(child.path)
	if err != nil {
		d.warnf("skipping unreadable directory %s: %v", child.path, err)
		return
	}
	if ok {
		d.add(child.name, child.path)
		return
	}
	nested := d.nested(child.path, child.name, nestedRepoDepth, append(slices.Clone(stack), real))
	// Preserve nested project boundaries before accepting manifest-free source.
	if len(nested) == 0 {
		ok, err := d.probe.hasIndexableRepoFile(child.path)
		if err != nil {
			d.warnf("skipping unreadable directory %s: %v", child.path, err)
			return
		}
		if ok {
			d.add(child.name, child.path)
		}
		return
	}
	for _, repo := range nested {
		d.add(repo.Name, repo.Path)
	}
	d.warnDroppedSource(child, nested)
}

func (d *discovery) nested(dir, prefix string, depth int, stack []string) []RepoEntry {
	if depth <= 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		d.warnf("skipping unreadable directory %s: %v", dir, err)
		return nil
	}
	var repos []RepoEntry
	for _, child := range d.childDirs(dir, prefix, entries, stack[0]) {
		real := d.realPath(child.path)
		if onStack(stack, real) {
			d.warnf("skipping %s: symlink loops back to an ancestor directory (%s)", child.path, real)
			continue
		}
		fullName := prefix + "/" + child.name
		ok, err := d.probe.looksLikeCodeRepo(child.path)
		if err != nil {
			d.warnf("skipping unreadable directory %s: %v", child.path, err)
			continue
		}
		if ok {
			repos = append(repos, RepoEntry{Name: fullName, Path: child.path})
			continue
		}
		repos = append(repos, d.nested(child.path, fullName, depth-1, append(slices.Clone(stack), real))...)
	}
	return repos
}

// warnDroppedSource reports source that sits in a multi-repository container but
// outside every discovered repository. Rooting a repository at the container
// would re-parse the nested repositories (the parser has no per-subtree
// exclusion) and collide with their paths, so the remainder is reported instead
// of silently disappearing.
func (d *discovery) warnDroppedSource(container childDir, nested []RepoEntry) {
	covered := map[string]bool{}
	names := make([]string, 0, len(nested))
	for _, repo := range nested {
		covered[filepath.Clean(repo.Path)] = true
		names = append(names, repo.Name)
	}
	dropped := d.probe.uncoveredSource(container.path, container.name, covered, d.excluded)
	if len(dropped) == 0 {
		return
	}
	shown := dropped
	if len(shown) > maxDroppedPathsShown {
		shown = append(slices.Clone(dropped[:maxDroppedPathsShown]), fmt.Sprintf("... and %d more", len(dropped)-maxDroppedPathsShown))
	}
	d.warnf("%s holds nested repositories (%s) and indexable source outside them that will NOT be indexed: %s; give that source its own manifest (package.json, go.mod, pom.xml, build.gradle) or move it into one of the nested repositories",
		container.name, strings.Join(names, ", "), strings.Join(shown, ", "))
}

// repoProbe answers repository-shape questions about a directory. warn, when set,
// receives non-fatal read problems found below the probed root.
type repoProbe struct {
	warn func(format string, args ...any)
}

func (p *repoProbe) warnf(format string, args ...any) {
	if p.warn != nil {
		p.warn(format, args...)
	}
}

func looksLikeCodeRepo(root string) (bool, error) {
	return (&repoProbe{}).looksLikeCodeRepo(root)
}

func hasIndexableRepoFile(root string) (bool, error) {
	return (&repoProbe{}).hasIndexableRepoFile(root)
}

func findWithinDepth(root string, maxDepth int, match func(path string) bool) (bool, error) {
	return (&repoProbe{}).findWithinDepth(root, maxDepth, match)
}

func (p *repoProbe) looksLikeCodeRepo(root string) (bool, error) {
	for _, name := range repoManifests {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			return true, nil
		}
	}

	match, err := p.findWithinDepth(root, 2, func(path string) bool {
		return strings.HasSuffix(strings.ToLower(path), ".tf")
	})
	if err != nil {
		return false, err
	}
	if match {
		return true, nil
	}

	match, err = p.findWithinDepth(root, 3, func(path string) bool {
		return strings.HasSuffix(strings.ToLower(path), ".csproj")
	})
	if err != nil {
		return false, err
	}
	if match {
		return true, nil
	}

	match, err = p.findWithinDepth(root, 2, func(path string) bool {
		return strings.HasSuffix(strings.ToLower(path), ".sln")
	})
	if err != nil {
		return false, err
	}
	if match {
		return p.hasIndexableRepoFile(root)
	}

	return p.findWithinDepth(root, 3, func(path string) bool {
		return strings.EqualFold(filepath.Ext(path), ".json") && supportedInfraJSON(path)
	})
}

func (p *repoProbe) hasIndexableRepoFile(root string) (bool, error) {
	return p.findWithinDepth(root, indexableSourceDepth, isIndexableRepoFile)
}

func isIndexableRepoFile(path string) bool {
	if (&parser.RPGParser{}).CanParse(path) || (&parser.CLParser{}).CanParse(path) ||
		(&parser.DDSParser{}).CanParse(path) || (&parser.IBMiBinderParser{}).CanParse(path) {
		return true
	}
	normalized := strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(normalized)
	ext := strings.ToLower(filepath.Ext(base))
	switch base {
	case "function.json", "host.json":
		return true
	}
	switch ext {
	case ".go", ".java", ".cs", ".asp", ".aspx", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".vue",
		".gql", ".graphql", ".sql", ".ps1", ".tf",
		".rpg", ".rpgle", ".sqlrpgle", ".cl", ".clle", ".dspf", ".prtf", ".pf", ".lf", ".bnddir":
		return true
	case ".json":
		return supportedInfraJSON(path)
	default:
		return strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".env")
	}
}

func supportedInfraJSON(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, 1024*1024))
	if err != nil {
		return false
	}
	return bytes.Contains(content, []byte(`"Microsoft.ApiManagement/service/apis"`)) ||
		bytes.Contains(content, []byte(`"Microsoft.ApiManagement/service/apis/operations"`)) ||
		bytes.Contains(content, []byte(`"Microsoft.ApiManagement/service/apis/policies"`)) ||
		bytes.Contains(content, []byte(`"Microsoft.ApiManagement/service/apis/operations/policies"`)) ||
		bytes.Contains(content, []byte(`"Microsoft.Logic/workflows"`)) ||
		bytes.Contains(content, []byte(`"Microsoft.Logic/workflows/triggers"`))
}

// findWithinDepth walks root without following symlinks. Only an unreadable root
// is an error; unreadable subdirectories are reported and skipped so a single
// protected folder cannot hide a whole repository from discovery.
func (p *repoProbe) findWithinDepth(root string, maxDepth int, match func(path string) bool) (bool, error) {
	var walk func(current string, depth int) (bool, error)
	walk = func(current string, depth int) (bool, error) {
		entries, err := os.ReadDir(current)
		if err != nil {
			if depth == 0 {
				return false, err
			}
			p.warnf("skipping unreadable directory %s: %v", current, err)
			return false, nil
		}
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			full := filepath.Join(current, name)
			if entry.IsDir() {
				if depth >= maxDepth || sourcepath.SkipDir(current, name) {
					continue
				}
				found, err := walk(full, depth+1)
				if found || err != nil {
					return found, err
				}
				continue
			}
			if match(full) {
				return true, nil
			}
		}
		return false, nil
	}

	return walk(root, 0)
}

// uncoveredSource lists the direct children of container (as prefix/name) that
// contain indexable source outside every covered repository path.
func (p *repoProbe) uncoveredSource(container, prefix string, covered, excluded map[string]bool) []string {
	entries, err := os.ReadDir(container)
	if err != nil {
		p.warnf("skipping unreadable directory %s: %v", container, err)
		return nil
	}
	var out []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(container, name)
		if entry.IsDir() {
			if covered[filepath.Clean(full)] || excluded[name] || excluded[prefix+"/"+name] || sourcepath.SkipDir(container, name) {
				continue
			}
			if p.containsUncoveredSource(full, 1, covered, excluded) {
				out = append(out, prefix+"/"+name)
			}
			continue
		}
		if isIndexableRepoFile(full) {
			out = append(out, prefix+"/"+name)
		}
	}
	return out
}

func (p *repoProbe) containsUncoveredSource(dir string, depth int, covered, excluded map[string]bool) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		p.warnf("skipping unreadable directory %s: %v", dir, err)
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		if entry.IsDir() {
			if depth >= indexableSourceDepth || covered[filepath.Clean(full)] || excluded[name] || sourcepath.SkipDir(dir, name) {
				continue
			}
			if p.containsUncoveredSource(full, depth+1, covered, excluded) {
				return true
			}
			continue
		}
		if isIndexableRepoFile(full) {
			return true
		}
	}
	return false
}
