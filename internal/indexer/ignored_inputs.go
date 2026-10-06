package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sergiumoraru/tirion/internal/parser"
	"github.com/sergiumoraru/tirion/internal/runtimeconfig"
	"github.com/sergiumoraru/tirion/internal/sourceindex"
	"github.com/sergiumoraru/tirion/internal/sourcepath"
)

// Git's clean/HEAD checks exclude ignored source. Record it separately so a
// changed or newly created local input cannot make skip-unchanged return stale
// graph facts. Dependency/output trees are pruned just as in native parsing.
func captureIgnoredInputs(ctx context.Context, root, revision string) (map[string]string, error) {
	inputs := map[string]string{}
	if strings.HasPrefix(revision, "unversioned-") {
		return inputs, nil // Non-Git repositories are never eligible for HEAD skipping.
	}
	args := []string{"-C", root, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--", "."}
	// Dependency and tool-state directories are excluded by pathspec so the
	// enumeration never lists them; build-output names depend on a sibling
	// manifest and are filtered below with the predicate the parse walk uses.
	for _, dir := range sourcepath.AlwaysSkippedDirs() {
		args = append(args, ":(exclude,glob)**/"+dir+"/**")
	}
	command := exec.CommandContext(ctx, "git", args...)
	command.Env = runtimeconfig.GitEnvironment()
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("enumerate ignored source inputs: %w", err)
	}
	skipper := sourcepath.NewSkipper(root)
	for _, path := range strings.Split(string(output), "\x00") {
		if path == "" || !potentialSourceInput(filepath.Join(root, path)) || skipper.SkipFile(path) {
			continue
		}
		excluded, err := sourceindex.ExcludedLink(root, path)
		if err != nil {
			return nil, err
		}
		if excluded {
			continue
		}
		data, err := sourceindex.ReadCurrent(root, path)
		if err != nil {
			return nil, fmt.Errorf("read ignored source %s: %w", path, err)
		}
		sum := sha256.Sum256(data)
		inputs[filepath.ToSlash(path)] = hex.EncodeToString(sum[:])
	}
	return inputs, nil
}

// CanParse methods only classify paths; these zero-value receivers do not create
// parsers or parse source. Include XML and metadata conservatively: their content
// may decide whether native parsing/mapping consumes them.
func potentialSourceInput(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".tf", ".xml", ".properties", ".csproj", ".edmx", ".tt":
		return true
	}
	switch strings.ToLower(filepath.Base(path)) {
	case "package.json", "lerna.json", "pnpm-workspace.yaml":
		return true
	}
	return (&parser.JavaScriptParser{}).CanParse(path) ||
		(&parser.JavaParser{}).CanParse(path) || (&parser.CSharpParser{}).CanParse(path) ||
		(&parser.RPGParser{}).CanParse(path) || (&parser.CLParser{}).CanParse(path) ||
		(&parser.DDSParser{}).CanParse(path) || (&parser.IBMiBinderParser{}).CanParse(path) ||
		(&parser.SQLParser{}).CanParse(path) || (&parser.PowerShellParser{}).CanParse(path) ||
		(&parser.GraphQLParser{}).CanParse(path) || (&parser.AzureFunctionsParser{}).CanParse(path) ||
		(&parser.AzureHostParser{}).CanParse(path) || (&parser.APIMParser{}).CanParse(path) ||
		(&parser.ResourceConfigParser{}).CanParse(path)
}
