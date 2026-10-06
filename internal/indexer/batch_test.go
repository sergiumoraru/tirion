package indexer

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizedPostParseReposDedupesAndSorts(t *testing.T) {
	got := normalizedPostParseRepos(PostParseOptions{
		Repo:  "resource-api",
		Repos: []string{"web-app", " resource-api ", "", "Data-Api"},
	})
	want := []string{"Data-Api", "resource-api", "web-app"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizedPostParseRepos() = %v, want %v", got, want)
	}
}

func TestNormalizedPostParseReposEmptyMeansWorkspaceWide(t *testing.T) {
	got := normalizedPostParseRepos(PostParseOptions{})
	if len(got) != 0 {
		t.Fatalf("normalizedPostParseRepos() = %v, want empty", got)
	}
}

func TestIsUnsupportedRepoFlagError(t *testing.T) {
	err := errors.New(`exit status 2; helper output tail: flag provided but not defined: -repo`)
	if !isUnsupportedRepoFlagError(err) {
		t.Fatal("expected unsupported -repo flag error")
	}
	if isUnsupportedRepoFlagError(errors.New("extract-http failed")) {
		t.Fatal("did not expect unrelated helper error to match")
	}
}

func TestRunParsePassesDiscoveredRepoName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script argument capture test is unix-only")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	scriptPath := filepath.Join(dir, "parse.sh")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsFile + "\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write parse script: %v", err)
	}

	err := runParse(scriptPath, "postgres://example", "default-main", "Build-Tools/resource-worker", filepath.Join(dir, "resource-worker"), true, false, false)
	if err != nil {
		t.Fatalf("runParse() error: %v", err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read captured args: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(raw)), "\n")
	wantPair := false
	for i := 0; i+1 < len(got); i++ {
		if got[i] == "-repo-name" && got[i+1] == "Build-Tools/resource-worker" {
			wantPair = true
			break
		}
	}
	if !wantPair {
		t.Fatalf("captured args missing qualified -repo-name: %#v", got)
	}
}

func TestLooksLikeCodeRepoAcceptsAPIMOnlyRepos(t *testing.T) {
	cases := []struct {
		name string
		path string
		file string
		body string
	}{
		{
			name: "APIM API resource",
			path: "Resource-APIM",
			file: "templates/main.json",
			body: `{"resources":[{"type":"Microsoft.ApiManagement/service/apis","name":"svc/resources","properties":{"path":"resources"}}]}`,
		},
		{
			name: "APIM operation resource",
			path: "Resource-Infrastructure",
			file: "infra/apim-deploy.json",
			body: `{"resources":[{"type":"Microsoft.ApiManagement/service/apis/operations","name":"svc/resources/list","properties":{"method":"GET","urlTemplate":"/resources"}}]}`,
		},
		{
			name: "Logic App workflow resource",
			path: "Resource-Infrastructure",
			file: "infra/workflow.json",
			body: `{"resources":[{"type":"Microsoft.Logic/workflows","name":"nightly","properties":{"definition":{"triggers":{}}}}]}`,
		},
		{
			name: "APIM resource in neutral directory",
			path: "Resource-Infrastructure",
			file: "infra/deploy.json",
			body: `{"resources":[{"type":"Microsoft.ApiManagement/service/apis/policies","name":"svc/resources/policy","properties":{"value":"<policies />"}}]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), tc.path)
			writeTestFile(t, filepath.Join(root, tc.file), []byte(tc.body))

			ok, err := looksLikeCodeRepo(root)
			if err != nil {
				t.Fatalf("looksLikeCodeRepo() error: %v", err)
			}
			if !ok {
				t.Fatalf("expected %s to be discovered as a code repo", root)
			}
		})
	}
}

func TestLooksLikeCodeRepoRejectsAPIMNameWithoutSupportedContent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Resource-APIM")
	writeTestFile(t, filepath.Join(root, "infra", "config.json"), []byte(`{"resources":[]}`))

	ok, err := looksLikeCodeRepo(root)
	if err != nil {
		t.Fatalf("looksLikeCodeRepo() error: %v", err)
	}
	if ok {
		t.Fatal("expected APIM-looking repo without supported APIM/Logic resources to be skipped")
	}
}

func TestLooksLikeCodeRepoRejectsPlainJSONOnlyRepo(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Deploy-Website")
	writeTestFile(t, filepath.Join(root, "README.md"), []byte("# deploy"))
	writeTestFile(t, filepath.Join(root, "template.json"), []byte(`{"resources":[]}`))

	ok, err := looksLikeCodeRepo(root)
	if err != nil {
		t.Fatalf("looksLikeCodeRepo() error: %v", err)
	}
	if ok {
		t.Fatal("expected plain JSON-only repo without APIM markers to be skipped")
	}
}

func TestLooksLikeCodeRepoRejectsSolutionOnlyDeploymentRepo(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Deploy-Website")
	writeTestFile(t, filepath.Join(root, "Deploy-Website.sln"), []byte("Microsoft Visual Studio Solution File"))
	writeTestFile(t, filepath.Join(root, "README.md"), []byte("# deploy"))
	writeTestFile(t, filepath.Join(root, "template.json"), []byte(`{"resources":[]}`))

	ok, err := looksLikeCodeRepo(root)
	if err != nil {
		t.Fatalf("looksLikeCodeRepo() error: %v", err)
	}
	if ok {
		t.Fatal("expected .sln-only deployment repo without indexable files to be skipped")
	}
}

func TestLooksLikeCodeRepoAcceptsSolutionWithSourceFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Legacy-Web")
	writeTestFile(t, filepath.Join(root, "Legacy-Web.sln"), []byte("Microsoft Visual Studio Solution File"))
	writeTestFile(t, filepath.Join(root, "src", "Controllers", "HomeController.cs"), []byte("public class HomeController {}"))

	ok, err := looksLikeCodeRepo(root)
	if err != nil {
		t.Fatalf("looksLikeCodeRepo() error: %v", err)
	}
	if !ok {
		t.Fatal("expected .sln repo with indexable source to be discovered")
	}
}

func TestDiscoverReposIncludesManifestFreeSource(t *testing.T) {
	for _, source := range []string{"src/post.RPGLE", "QRPGLESRC/POST.MBR", "src/worker.go"} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "service", source), []byte("source"))
			repos, err := DiscoverRepos(root, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(repos) != 1 || repos[0].Name != "service" {
				t.Fatalf("expected source repo boundary, got %#v", repos)
			}
		})
	}
}

func TestDiscoverReposIncludesAPIMOnlyRepo(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "Resource-APIM", "apim-deploy.json"), []byte(`{"resources":[{"type":"Microsoft.ApiManagement/service/apis","name":"svc/resources","properties":{"path":"resources"}}]}`))
	writeTestFile(t, filepath.Join(root, "Plain-Docs", "README.md"), []byte("# docs"))

	repos, err := DiscoverRepos(root, nil)
	if err != nil {
		t.Fatalf("DiscoverRepos() error: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "Resource-APIM" {
		t.Fatalf("DiscoverRepos() = %#v, want only Resource-APIM", repos)
	}
}

func TestDiscoverReposIncludesNestedProjectContainers(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "Resource-Functions", "resource-functions", "package.json"), []byte(`{"scripts":{"build":"tsc"}}`))
	writeTestFile(t, filepath.Join(root, "Resource-Functions", "resource-functions", "ProcessResource", "function.json"), []byte(`{"bindings":[{"type":"httpTrigger"}]}`))
	writeTestFile(t, filepath.Join(root, "Build-Tools", "resource-worker", "package.json"), []byte(`{"dependencies":{}}`))
	writeTestFile(t, filepath.Join(root, "Build-Tools", "resource-worker", "index.js"), []byte(`module.exports = async function run() {}`))
	writeTestFile(t, filepath.Join(root, "Docs", "README.md"), []byte("# docs"))

	repos, err := DiscoverRepos(root, nil)
	if err != nil {
		t.Fatalf("DiscoverRepos() error: %v", err)
	}
	got := make([]string, 0, len(repos))
	for _, repo := range repos {
		got = append(got, repo.Name)
	}
	want := []string{"Build-Tools/resource-worker", "Resource-Functions/resource-functions"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiscoverRepos() names = %#v, want %#v (repos=%#v)", got, want, repos)
	}
}

func TestDiscoverReposUsesExplicitExclusions(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "scratch", "worker", "package.json"), []byte(`{"scripts":{"build":"tsc"}}`))
	writeTestFile(t, filepath.Join(root, "experimental", "package.json"), []byte(`{"scripts":{"build":"tsc"}}`))
	writeTestFile(t, filepath.Join(root, "Real-Service", "package.json"), []byte(`{"scripts":{"build":"tsc"}}`))

	repos, err := DiscoverRepos(root, map[string]bool{"scratch": true, "experimental": true})
	if err != nil {
		t.Fatalf("DiscoverRepos() error: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "Real-Service" {
		t.Fatalf("DiscoverRepos() = %#v, want only Real-Service", repos)
	}
}

func TestDiscoverReposDoesNotSplitNestedProjectsInsideRecognizedRepo(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "Component-Library", "package.json"), []byte(`{"workspaces":["libs/*"]}`))
	writeTestFile(t, filepath.Join(root, "Component-Library", "libs", "component", "package.json"), []byte(`{"name":"component"}`))

	repos, err := DiscoverRepos(root, nil)
	if err != nil {
		t.Fatalf("DiscoverRepos() error: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "Component-Library" {
		t.Fatalf("DiscoverRepos() = %#v, want only top-level Component-Library", repos)
	}
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
