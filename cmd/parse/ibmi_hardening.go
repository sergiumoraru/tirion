package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sergiumoraru/tirion/internal/parser"
	"github.com/sergiumoraru/tirion/internal/sourcepath"
)

type ibmiIncludeResolver struct {
	repoRoot string
	byRel    map[string]string
	byBase   map[string][]string
	inputs   map[string]string
	err      error
	indexed  bool
}

// buildIBMiIncludeResolver prepares include resolution without touching the
// filesystem. The member index walks the repository, which only IBM i sources
// need, so it is built on the first lookup (see ensureIndex).
func buildIBMiIncludeResolver(repoRoot string) *ibmiIncludeResolver {
	return &ibmiIncludeResolver{
		repoRoot: repoRoot,
		byRel:    make(map[string]string),
		byBase:   make(map[string][]string),
		inputs:   make(map[string]string),
	}
}

// ensureIndex indexes every file below the repository, excluding the
// directories the parse walk skips. Walk errors are logged and kept in r.err so
// the run fails visibly rather than resolving includes against a partial index.
func (r *ibmiIncludeResolver) ensureIndex() {
	if r.indexed {
		return
	}
	r.indexed = true
	repoRoot := r.repoRoot
	walkErr := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			fmt.Fprintf(os.Stderr, "IBM i include index: %v\n", err)
			if r.err == nil {
				r.err = fmt.Errorf("index IBM i includes: %w", err)
			}
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			if path != repoRoot && sourcepath.SkipDir(filepath.Dir(path), info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return nil
		}
		rel = normalizeFilePathIdentity(rel)
		if rel == "" {
			return nil
		}
		key := strings.ToLower(rel)
		r.byRel[key] = path
		baseKey := strings.ToLower(parser.NormalizeIBMiObjectName(strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))))
		if baseKey == "" {
			return nil
		}
		r.byBase[baseKey] = append(r.byBase[baseKey], path)
		return nil
	})
	if walkErr != nil && r.err == nil {
		r.err = fmt.Errorf("index IBM i includes: %w", walkErr)
	}
	for key := range r.byBase {
		sort.Strings(r.byBase[key])
	}
}

func (r *ibmiIncludeResolver) Resolve(currentRelPath, importPath string) (string, bool) {
	if r == nil {
		return "", false
	}
	if abs, ok := r.resolveRelativePath(currentRelPath, importPath); ok {
		return abs, true
	}
	r.ensureIndex()

	for _, candidate := range r.relativeCandidates(currentRelPath, importPath) {
		if abs, ok := r.byRel[strings.ToLower(candidate)]; ok {
			return abs, true
		}
	}

	currentDir := normalizeFilePathIdentity(filepath.Dir(currentRelPath))
	bestPath := ""
	bestScore := -1
	ambiguous := false
	for _, key := range importLookupKeys(importPath) {
		candidates := r.byBase[strings.ToLower(key)]
		for _, abs := range candidates {
			score := includeCandidateScore(r.repoRoot, currentDir, abs)
			if score > bestScore {
				bestScore = score
				bestPath = abs
				ambiguous = false
			} else if score == bestScore && abs != bestPath {
				ambiguous = true
			}
		}
	}
	if bestPath == "" || ambiguous {
		return "", false
	}
	return bestPath, true
}

// maxIncludeBytes bounds a single /COPY or /INCLUDE member read.
const maxIncludeBytes = 16 << 20

// includeRoot is the directory that holds the repository and its siblings.
// Explicit relative includes may cross into a sibling repository, never above it.
func (r *ibmiIncludeResolver) includeRoot() string {
	root, err := filepath.Abs(r.repoRoot)
	if err != nil {
		root = r.repoRoot
	}
	return filepath.Dir(filepath.Clean(root))
}

// escapesIncludeRoot reports whether path resolves (through symlinks) outside
// includeRoot. Both sides are resolved so platform aliases such as /var and
// /private/var compare equal.
func (r *ibmiIncludeResolver) escapesIncludeRoot(path string) bool {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false // missing or unreadable: the read reports it
	}
	root, err := filepath.EvalSymlinks(r.includeRoot())
	if err != nil {
		return true
	}
	rel, err := filepath.Rel(root, real)
	return err != nil || !filepath.IsLocal(rel)
}

// includeRel returns path relative to includeRoot, or false when it lies outside.
func (r *ibmiIncludeResolver) includeRel(path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(r.includeRoot(), abs)
	if err != nil || !filepath.IsLocal(rel) {
		return "", false
	}
	return rel, true
}

// readInclude reads a resolved include through an os.Root at includeRoot, so
// neither ".." nor symlinks can reach outside it, and bounds its size.
func (r *ibmiIncludeResolver) readInclude(path string) ([]byte, error) {
	rel, ok := r.includeRel(path)
	if !ok {
		return nil, fmt.Errorf("include is outside the repositories root: %s", path)
	}
	root, err := os.OpenRoot(r.includeRoot())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("include is not a regular file: %s", path)
	}
	content, err := io.ReadAll(io.LimitReader(f, maxIncludeBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxIncludeBytes {
		return nil, fmt.Errorf("include exceeds %d bytes: %s", maxIncludeBytes, path)
	}
	return content, nil
}

func (r *ibmiIncludeResolver) resolveRelativePath(currentRelPath, importPath string) (string, bool) {
	currentRelPath = normalizeFilePathIdentity(currentRelPath)
	importPath = strings.TrimSpace(importPath)
	if currentRelPath == "" || importPath == "" {
		return "", false
	}

	candidates := []string{
		filepath.Clean(filepath.Join(filepath.Dir(filepath.Join(r.repoRoot, filepath.FromSlash(currentRelPath))), filepath.FromSlash(importPath))),
		filepath.Clean(filepath.Join(r.repoRoot, filepath.FromSlash(importPath))),
	}
	for _, candidate := range candidates {
		// Never stat or record paths above the repositories root: include text is
		// repository content, and a manifest entry would reveal host files.
		if _, ok := r.includeRel(candidate); !ok {
			continue
		}
		info, err := os.Stat(candidate)
		if os.IsNotExist(err) {
			if r.inputs == nil {
				r.inputs = make(map[string]string)
			}
			r.inputs[candidate] = ""
			continue
		}
		if err != nil {
			r.err = err
			continue
		}
		if info.Mode().IsRegular() {
			return candidate, true
		}
	}
	return "", false
}

func (r *ibmiIncludeResolver) relativeCandidates(currentRelPath, importPath string) []string {
	currentDir := normalizeFilePathIdentity(filepath.Dir(currentRelPath))
	importPath = strings.TrimSpace(importPath)
	if importPath == "" {
		return nil
	}
	var out []string
	if exact := normalizeFilePathIdentity(importPath); exact != "" {
		out = append(out, exact)
		if currentDir != "" {
			out = append(out, normalizeFilePathIdentity(filepath.Join(currentDir, exact)))
		}
	}
	if strings.Contains(importPath, ",") {
		parts := strings.Split(importPath, ",")
		if len(parts) >= 2 {
			member := strings.TrimSpace(parts[len(parts)-1])
			srcFile := strings.TrimSpace(parts[len(parts)-2])
			if member != "" {
				out = append(out,
					normalizeFilePathIdentity(filepath.Join(srcFile, member)),
					normalizeFilePathIdentity(filepath.Join(srcFile, member+".MBR")),
					normalizeFilePathIdentity(filepath.Join(srcFile, member+".RPGLEINC")),
					normalizeFilePathIdentity(filepath.Join(srcFile, member+".RPGLE")),
				)
			}
		}
	}
	return uniqueStrings(out)
}

func importLookupKeys(importPath string) []string {
	importPath = strings.TrimSpace(importPath)
	if importPath == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		value = parser.NormalizeIBMiObjectName(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		out = append(out, value)
	}

	add(strings.TrimSuffix(filepath.Base(importPath), filepath.Ext(importPath)))
	if strings.Contains(importPath, ",") {
		parts := strings.Split(importPath, ",")
		add(parts[len(parts)-1])
	}
	if strings.Contains(importPath, "/") {
		parts := strings.Split(importPath, "/")
		add(parts[len(parts)-1])
	}
	return out
}

func includeCandidateScore(repoRoot, currentDir, absPath string) int {
	rel, err := filepath.Rel(repoRoot, absPath)
	if err != nil {
		return 0
	}
	rel = normalizeFilePathIdentity(rel)
	score := includeExtensionScore(absPath)
	if currentDir != "" && strings.HasPrefix(strings.ToLower(rel), strings.ToLower(currentDir)+"/") {
		score += 10
	}
	return score
}

func includeExtensionScore(path string) int {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".rpgleinc", ".sqlrpgleinc":
		return 4
	case ".rpgle", ".sqlrpgle", ".rpg":
		return 3
	case ".mbr", ".src", ".txtmbr", ".txt":
		return 2
	default:
		return 1
	}
}

func applyRPGCopyAliasExpansion(currentRelPath string, result *parser.ParsedFile, resolver *ibmiIncludeResolver) {
	if result == nil || resolver == nil || len(result.Imports) == 0 {
		return
	}
	aliases := collectRPGImportAliases(currentRelPath, result.Imports, resolver, 0, make(map[string]bool))
	if len(aliases) == 0 {
		return
	}
	for fnName, calls := range result.FunctionCalls {
		for i := range calls {
			if exportName, ok := aliases[strings.ToLower(calls[i].CalleeName)]; ok && exportName != "" {
				result.FunctionCalls[fnName][i].CalleeName = exportName
				result.FunctionCalls[fnName][i].MethodName = exportName
			}
		}
	}
}

func collectRPGImportAliases(currentRelPath string, imports []parser.ParsedImport, resolver *ibmiIncludeResolver, depth int, visited map[string]bool) map[string]string {
	if depth > 8 {
		return nil
	}
	aliases := make(map[string]string)
	for _, imp := range imports {
		resolvedPath, ok := resolver.Resolve(currentRelPath, imp.Path)
		if !ok {
			continue
		}
		if visited[resolvedPath] {
			continue
		}
		visited[resolvedPath] = true

		// An explicit relative include may cross into a sibling repository. Confine
		// the read to the repositories root and record its bytes for publication.
		if resolver.escapesIncludeRoot(resolvedPath) {
			// Repository content must not make the indexer read, or fail on, host files.
			fmt.Fprintf(os.Stderr, "IBM i include skipped: %s resolves outside the repositories root\n", resolvedPath)
			continue
		}
		content, err := resolver.readInclude(resolvedPath)
		if err != nil {
			resolver.err = err
			continue
		}
		sum := sha256.Sum256(content)
		hash := hex.EncodeToString(sum[:])
		if resolver.inputs == nil {
			resolver.inputs = make(map[string]string)
		}
		if previous, ok := resolver.inputs[resolvedPath]; ok && previous != hash {
			resolver.err = fmt.Errorf("include changed while parsing: %s", resolvedPath)
		}
		resolver.inputs[resolvedPath] = hash
		for localName, exportName := range parser.ExtractRPGPrototypeAliases(content) {
			aliases[localName] = exportName
		}
		rel, err := filepath.Rel(resolver.repoRoot, resolvedPath)
		if err != nil {
			continue
		}
		nestedImports := make([]parser.ParsedImport, 0)
		for _, path := range parser.ExtractRPGCopyImports(content) {
			nestedImports = append(nestedImports, parser.ParsedImport{Path: path})
		}
		for localName, exportName := range collectRPGImportAliases(normalizeFilePathIdentity(rel), nestedImports, resolver, depth+1, visited) {
			aliases[localName] = exportName
		}
	}
	return aliases
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
