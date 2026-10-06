package sourcepath

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// alwaysSkipped are dependency, tool-state, and VCS directories. They never hold
// first-party source, whatever their depth.
var alwaysSkipped = map[string]bool{
	".git":                true,
	".hg":                 true,
	".svn":                true,
	".idea":               true,
	".vscode":             true,
	".next":               true,
	".nuxt":               true,
	".codebase-snapshots": true,
	".tox":                true,
	".venv":               true,
	"venv":                true,
	"__pycache__":         true,
	"node_modules":        true,
	"bower_components":    true,
	"vendor":              true,
	"vendors":             true,
	"third_party":         true,
	"third-party":         true,
}

// buildOutputs are generated-artifact names that are also legitimate package
// names (Go internal/build, Java com/acme/out). They are skipped only beside a
// build manifest, where the toolchain writes its output.
var buildOutputs = map[string]bool{
	"dist":     true,
	"build":    true,
	"target":   true,
	"out":      true,
	"bin":      true,
	"obj":      true,
	"coverage": true,
}

var buildManifests = map[string]bool{
	"package.json":        true,
	"pom.xml":             true,
	"build.gradle":        true,
	"build.gradle.kts":    true,
	"settings.gradle":     true,
	"settings.gradle.kts": true,
	"go.mod":              true,
	"Cargo.toml":          true,
	"Makefile":            true,
	"CMakeLists.txt":      true,
}

var buildManifestExts = map[string]bool{
	".csproj": true,
	".fsproj": true,
	".vbproj": true,
	".sln":    true,
}

// AlwaysSkippedDir reports directories excluded at any depth.
func AlwaysSkippedDir(name string) bool {
	return alwaysSkipped[name]
}

// AlwaysSkippedDirs lists the names AlwaysSkippedDir accepts, sorted, for callers
// that must express the exclusion in another language (VCS pathspecs).
func AlwaysSkippedDirs() []string {
	names := make([]string, 0, len(alwaysSkipped))
	for name := range alwaysSkipped {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SkipDir reports whether a walk should skip the directory name inside parent.
// Every source walk (discovery, parse, input capture, ignored-input capture)
// must use this so the indexed file set and its fingerprints agree.
func SkipDir(parent, name string) bool {
	if alwaysSkipped[name] {
		return true
	}
	if !buildOutputs[strings.ToLower(name)] {
		return false
	}
	return hasBuildManifest(parent)
}

// Skipper applies SkipDir to the directory components of root-relative paths.
// It serves callers that receive file lists (git ls-files) rather than walking,
// so they exclude exactly the files a walk would never have visited. Manifest
// lookups are cached for the lifetime of the Skipper, which is one enumeration.
type Skipper struct {
	root   string
	cached map[string]bool
}

func NewSkipper(root string) *Skipper {
	return &Skipper{root: root, cached: map[string]bool{}}
}

// SkipFile reports whether a walk of the root would have pruned a directory
// above the slash- or OS-separated root-relative file path.
func (s *Skipper) SkipFile(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	parent := s.root
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." {
			continue
		}
		key := parent + "\x00" + part
		skip, ok := s.cached[key]
		if !ok {
			skip = SkipDir(parent, part)
			s.cached[key] = skip
		}
		if skip {
			return true
		}
		parent = filepath.Join(parent, part)
	}
	return false
}

func hasBuildManifest(dir string) bool {
	// Not cached: a long-running server re-indexes trees whose manifests change.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if buildManifests[name] || buildManifestExts[strings.ToLower(filepath.Ext(name))] {
			return true
		}
	}
	return false
}
