package indexer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sergiumoraru/tirion/internal/graph"
)

// Exercises native source -> persistence -> enrichment -> resolution, including
// snapshot copying. Requires the same explicit disposable resources as the retry test.
func TestJavaInheritanceAndMetadataPipeline(t *testing.T) {
	dbURL, bin := liveEnvironment(t, "TIRION_PIPELINE_LIVE_TESTS", true)
	ctx := context.Background()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	useHelperDirectory(t, bin)
	sources := map[string]string{
		"Scope.java": `package scope;
class Base { String work() { return "wrong"; } }
class Outer {
  static class Base { String work() { return "right"; } }
  static class Child extends Base { String run() { return work(); } }
}`,
		"Root.java": `package hierarchy;
class Root { void pick() {} void done() {} }`,
		"Middle.java": `package hierarchy;
class Middle extends Root { void pick(int n) {} @Override void done() {} }`,
		"Leaf.java": `package hierarchy;
class Leaf extends Middle { void run() { pick(); } void finish() { done(); } }`,
		"GenericBase.java": `package hierarchy;
class GenericBase<T> { void ping() {} }`,
		"GenericChild.java": `package hierarchy;
class GenericChild extends GenericBase<java.util.List<java.util.List<String>>> { void run() { ping(); } }`,
		"imported/Base.java": `package imported;
public class Base { public void work() {} }`,
		"Imported.java": `package client;
import imported.Base;
class Imported extends Base { void run() { work(); } }`,
		"Qualified.java": `package client;
class Qualified extends imported.Base { void run() { work(); } }`,
		"Wildcard.java": `package client;
import imported.*;
class Wildcard extends Base { void run() { work(); } }`,
		"Missing.java": `package client;
import unavailable.Base;
class Missing extends Base { void run() { work(); } }`,
		"Enclosing.java": `package hierarchy;
class Enclosing extends Root { class Nested { void run() { pick(); } } }`,
		"Host.java": `package metadata;
class Host {
  @Deprecated void run() { Runnable task = new Runnable() { @Override public void run() {
    System.out.println("inner");
  }};
  }
}`,
	}
	for name, source := range sources {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, name), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"add", "."}, {"-c", "user.name=Validation", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-m", "Java regression fixture"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v\n%s", err, out)
		}
	}
	storage, err := graph.NewStorage(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	name := fmt.Sprintf("java-scope-%d", time.Now().UnixNano())
	if _, err := storage.EnsureWorkspace(name, name, "", "", false); err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, query := range []string{`DELETE FROM workspaces WHERE slug=$1`, `DELETE FROM repositories WHERE name=$1`} {
			if _, err := storage.Pool().Exec(ctx, query, name); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	}()
	opts := PipelineOptions{DBURL: dbURL, Workspace: name, ParseBinary: filepath.Join(bin, "parse"), Repos: []RepoEntry{{Name: name, Path: repo}}, Resolve: true}
	index := func() {
		t.Helper()
		if _, err := RunRepositories(opts); err != nil {
			t.Fatal(err)
		}
	}
	selected := func() int64 {
		t.Helper()
		refs, err := storage.ActiveSnapshotsForWorkspace(name)
		if err != nil || len(refs) != 1 {
			t.Fatalf("selection: %+v, %v", refs, err)
		}
		return refs[0].SnapshotID
	}
	check := func(snapshot int64) {
		t.Helper()
		for caller, want := range map[string]string{
			"Outer.Child.run":      "Scope.java:Outer.Base.work",
			"Leaf.run":             "unresolved",
			"Leaf.finish":          "Middle.java:Middle.done",
			"GenericChild.run":     "GenericBase.java:GenericBase.ping",
			"Imported.run":         "imported/Base.java:Base.work",
			"Qualified.run":        "imported/Base.java:Base.work",
			"Wildcard.run":         "imported/Base.java:Base.work",
			"Missing.run":          "unresolved",
			"Enclosing.Nested.run": "Root.java:Root.pick",
		} {
			var got string
			err := storage.Pool().QueryRow(ctx, `SELECT COALESCE(tf.path||':'||target.name,'unresolved')
FROM function_calls fc JOIN functions caller ON caller.id=fc.caller_function_id JOIN files f ON f.id=caller.file_id
LEFT JOIN functions target ON target.id=fc.callee_function_id LEFT JOIN files tf ON tf.id=target.file_id
WHERE f.snapshot_id=$1 AND caller.name=$2`, snapshot, caller).Scan(&got)
			if err != nil || got != want {
				t.Fatalf("%s: got %q, want %q: %v", caller, got, want, err)
			}
		}
		var count int
		err := storage.Pool().QueryRow(ctx, `SELECT count(*) FROM functions fn JOIN files f ON f.id=fn.file_id
JOIN method_signatures ms ON ms.function_id=fn.id JOIN annotations a ON a.entity_type='method' AND a.entity_id=fn.id
WHERE f.snapshot_id=$1 AND fn.name='Host.run' AND ((fn.end_line=6 AND a.name='Deprecated') OR (fn.end_line=5 AND a.name='Override'))`, snapshot).Scan(&count)
		if err != nil || count != 2 {
			t.Fatalf("same-line metadata: count=%d, %v", count, err)
		}
		err = storage.Pool().QueryRow(ctx, `SELECT count(*) FROM files WHERE snapshot_id=$1 AND java_package IS NULL`, snapshot).Scan(&count)
		if err != nil || count != 0 {
			t.Fatalf("package persistence: count=%d, %v", count, err)
		}
	}
	index()
	first := selected()
	check(first)
	if err := RunPostParseExtractors(PostParseOptions{DBURL: dbURL, Workspace: name, Repo: name, RawList: "java-intel"}); err != nil {
		t.Fatal(err)
	}
	check(selected())
	check(first)
	index()
	check(selected())
	check(first)
	opts.SkipUnchanged = true
	before := selected()
	index()
	if selected() != before {
		t.Fatal("unchanged pipeline replaced the snapshot")
	}
}
