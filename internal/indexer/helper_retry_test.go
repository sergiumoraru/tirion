package indexer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func skipWithoutShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script helpers are unix-only")
	}
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

// countingHelper writes a helper that records each invocation and then behaves
// as body says. It returns the script path and the invocation-count file.
func countingHelper(t *testing.T, body string) (script, counter string) {
	t.Helper()
	dir := t.TempDir()
	counter = filepath.Join(dir, "invocations")
	script = filepath.Join(dir, "helper.sh")
	content := "#!/bin/sh\necho x >> '" + counter + "'\n" + body
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, counter
}

func invocations(t *testing.T, counter string) int {
	t.Helper()
	data, err := os.ReadFile(counter)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	return strings.Count(string(data), "x")
}

func fastRetries(t *testing.T) {
	t.Helper()
	previous := helperRetryDelay
	helperRetryDelay = 10 * time.Millisecond
	t.Cleanup(func() { helperRetryDelay = previous })
}

func TestRunPostParseHelper_RetriesTransientFailure(t *testing.T) {
	skipWithoutShell(t)
	fastRetries(t)
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state")
	script, counter := countingHelper(t, "if [ ! -f '"+stateFile+"' ]; then\n"+
		"  echo 'dial tcp 127.0.0.1:5432: connect: connection refused'\n"+
		"  touch '"+stateFile+"'\n"+
		"  exit 2\n"+
		"fi\n"+
		"echo second-pass\n")

	if err := runPostParseHelper(script, nil, "", false); err != nil {
		t.Fatalf("a transient database failure should be retried once and succeed, got: %v", err)
	}
	if got := invocations(t, counter); got != 2 {
		t.Fatalf("helper ran %d times, want 2", got)
	}
}

func TestRunPostParseHelper_TransientFailureStopsAfterOneRetry(t *testing.T) {
	skipWithoutShell(t)
	fastRetries(t)
	script, counter := countingHelper(t, "echo 'deadlock detected'\nexit 1\n")

	err := runPostParseHelper(script, nil, "", false)
	if err == nil || !strings.Contains(err.Error(), "deadlock detected") {
		t.Fatalf("want the helper output in the error, got %v", err)
	}
	if got := invocations(t, counter); got != 2 {
		t.Fatalf("helper ran %d times, want exactly 2 (one retry)", got)
	}
}

func TestRunPostParseHelper_DeterministicFailureIsNotRetried(t *testing.T) {
	cases := map[string]string{
		"unsupported flag": "flag provided but not defined: -repo",
		"parse failure":    "syntax error near unexpected token",
		"plain failure":    "extract-http failed",
	}
	skipWithoutShell(t)
	// The retry pause would show up as test time if a retry happened.
	previous := helperRetryDelay
	helperRetryDelay = time.Minute
	t.Cleanup(func() { helperRetryDelay = previous })
	for name, output := range cases {
		t.Run(name, func(t *testing.T) {
			script, counter := countingHelper(t, "echo '"+output+"'\nexit 2\n")
			start := time.Now()
			err := runPostParseHelper(script, nil, "", false)
			if err == nil || !strings.Contains(err.Error(), output) {
				t.Fatalf("want the helper output in the error, got %v", err)
			}
			if got := invocations(t, counter); got != 1 {
				t.Fatalf("deterministic failure ran the helper %d times, want 1", got)
			}
			if time.Since(start) > 30*time.Second {
				t.Fatal("deterministic failure waited for a retry")
			}
		})
	}
}

func TestIsTransientHelperFailure(t *testing.T) {
	cases := []struct {
		output string
		want   bool
	}{
		{"dial tcp 127.0.0.1:5432: connect: connection refused", true},
		{"read tcp: connection reset by peer", true},
		{"FATAL: the database system is starting up (SQLSTATE 57P03)", true},
		{"ERROR: deadlock detected (SQLSTATE 40P01)", true},
		{"ERROR: could not serialize access due to concurrent update (SQLSTATE 40001)", true},
		{"FATAL: sorry, too many clients already (SQLSTATE 53300)", true},
		{"flag provided but not defined: -repo", false},
		{"panic: runtime error: index out of range", false},
		{"no such file or directory", false},
		{"permission denied", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isTransientHelperFailure(tc.output); got != tc.want {
			t.Errorf("isTransientHelperFailure(%q) = %t, want %t", tc.output, got, tc.want)
		}
	}
}

func TestRunPostParseHelper_HungHelperIsKilledAtTheTimeout(t *testing.T) {
	skipWithoutShell(t)
	fastRetries(t)
	previous := helperTimeout
	helperTimeout = 2 * time.Second
	t.Cleanup(func() { helperTimeout = previous })
	// exec replaces the shell, so killing the helper also releases its output pipe.
	script, counter := countingHelper(t, "exec sleep 60\n")

	start := time.Now()
	err := runPostParseHelperContext(context.Background(), script, nil, "", false)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want a timeout error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("hung helper held the run for %s", elapsed)
	}
	if got := invocations(t, counter); got != 1 {
		t.Fatalf("a timed-out helper must not be retried, ran %d times", got)
	}
}

func TestRunPostParseHelper_CallerCancellationWins(t *testing.T) {
	skipWithoutShell(t)
	script, _ := countingHelper(t, "exec sleep 60\n")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := runPostParseHelperContext(ctx, script, nil, "", false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the caller's context error, got %v", err)
	}
}

func TestRunPostParseHelper_MissingBinaryHintsAtBuildScript(t *testing.T) {
	fastRetries(t)
	missing := filepath.Join(t.TempDir(), "extract-http")
	err := runPostParseHelperContext(context.Background(), missing, nil, "", false)
	if err == nil {
		t.Fatal("a missing helper must fail")
	}
	for _, fragment := range []string{"build-local.sh", "beside the tirion executable"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("error %q lacks the hint %q", err, fragment)
		}
	}
}

func TestResolveParseBinaryMissingBinaryHints(t *testing.T) {
	cases := map[string]string{
		"explicit path": filepath.Join(t.TempDir(), "parse"),
		"bare name":     "tirion-surely-not-installed-parse",
	}
	for name, candidate := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ResolveParseBinary(candidate)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), "bash scripts/build-local.sh") || !strings.Contains(err.Error(), "same directory") {
				t.Fatalf("error lacks the build/placement hint: %v", err)
			}
		})
	}
	if _, err := ResolveParseBinary(t.TempDir()); err == nil || strings.Contains(err.Error(), "build-local.sh") {
		t.Fatalf("a non-executable path is a different problem and gets no build hint: %v", err)
	}
}

func fakeExecutable(t *testing.T, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestValidateHelpers(t *testing.T) {
	skipWithoutShell(t)
	dir := t.TempDir()
	useHelperDirectory(t, dir)
	fakeExecutable(t, filepath.Join(dir, "extract-http"), "#!/bin/sh\n")

	names, err := validateHelpers("http", false)
	if err != nil || len(names) != 1 || names[0] != "extract-http" {
		t.Fatalf("validateHelpers(http) = %v, %v", names, err)
	}
	if names, err := validateHelpers("http,sqs,spring", true); err != nil || names != nil {
		t.Fatalf("skipping extractors must not require helpers: %v, %v", names, err)
	}
	_, err = validateHelpers("http,sqs,spring", false)
	if err == nil {
		t.Fatal("missing helpers must be reported")
	}
	for _, fragment := range []string{"extract-sqs", "extract-spring", "build-local.sh"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("error %q does not mention %q (all missing helpers should be listed at once)", err, fragment)
		}
	}
	if strings.Contains(err.Error(), "extract-http:") {
		t.Fatalf("the present helper must not be reported missing: %v", err)
	}
	if _, err := validateHelpers("http,nonsense", false); err == nil || !strings.Contains(err.Error(), "unknown extractor") {
		t.Fatalf("an unknown extractor name must be rejected: %v", err)
	}
}

func TestDryRunValidatesHelperAvailability(t *testing.T) {
	skipWithoutShell(t)
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "repos", "svc", "package.json"), []byte("{}"))
	bin := t.TempDir()
	useHelperDirectory(t, bin)
	parse := filepath.Join(bin, ParseBinaryName())
	fakeExecutable(t, parse, "#!/bin/sh\n")
	run := func(skipExtractors bool) error {
		_, err := RunBatchDetailed(BatchOptions{Root: filepath.Join(root, "repos"), ParseBinary: parse, DryRun: true, SkipExtractors: skipExtractors, Extractors: "http,sqs"})
		return err
	}

	err := run(false)
	if err == nil || !strings.Contains(err.Error(), "extract-http") || !strings.Contains(err.Error(), "build-local.sh") {
		t.Fatalf("dry run must fail on missing helpers with the hint, got %v", err)
	}
	if err := run(true); err != nil {
		t.Fatalf("dry run with -skip-extractors needs no helpers: %v", err)
	}
	for _, name := range []string{"extract-http", "extract-sqs"} {
		fakeExecutable(t, filepath.Join(bin, name), "#!/bin/sh\n")
	}
	if err := run(false); err != nil {
		t.Fatalf("dry run with every helper present: %v", err)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	done := make(chan string)
	go func() {
		data, _ := io.ReadAll(reader)
		done <- string(data)
	}()
	defer func() { os.Stdout = original }()
	fn()
	writer.Close()
	os.Stdout = original
	return <-done
}

func TestIndexSummaryReportsDiscoveryWarnings(t *testing.T) {
	skipWithoutShell(t)
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "repos", "mono", "web", "package.json"), []byte("{}"))
	writeTestFile(t, filepath.Join(root, "repos", "mono", "Loose.java"), []byte("class Loose {}"))
	bin := t.TempDir()
	useHelperDirectory(t, bin)
	parse := filepath.Join(bin, ParseBinaryName())
	fakeExecutable(t, parse, "#!/bin/sh\n")

	output := captureStdout(t, func() {
		if _, err := RunBatchDetailed(BatchOptions{Root: filepath.Join(root, "repos"), ParseBinary: parse, DryRun: true, SkipExtractors: true}); err != nil {
			t.Error(err)
		}
	})
	summary := output[strings.Index(output, "Index summary"):]
	if !strings.Contains(summary, "Warning: mono holds nested repositories") || !strings.Contains(summary, "mono/Loose.java") {
		t.Fatalf("the index summary must name dropped source, got:\n%s", summary)
	}
}

func TestResolveModeLabel(t *testing.T) {
	// -global-resolve=true resolves every eligible cross-repository call at
	// publication; false only repairs links that already exist.
	if got := resolveModeLabel(true); got != "full" {
		t.Errorf("resolveModeLabel(true) = %q, want full", got)
	}
	if got := resolveModeLabel(false); got != "repair-only" {
		t.Errorf("resolveModeLabel(false) = %q, want repair-only", got)
	}
}

func TestSameSourcePath(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	other := filepath.Join(base, "other")
	for _, dir := range []string{real, other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(base, "link")
	symlinkOrSkip(t, real, link)
	cases := []struct {
		name              string
		recorded, current string
		want              bool
	}{
		{"identical", real, real, true},
		{"cleaned", real + "/", real, true},
		{"symlink to the same directory", link, real, true},
		{"different directories", real, other, false},
		{"moved away (no longer exists)", filepath.Join(base, "gone"), real, false},
		{"unrecorded legacy snapshot", "", real, false},
		{"empty current", real, "", false},
	}
	for _, tc := range cases {
		if got := sameSourcePath(tc.recorded, tc.current); got != tc.want {
			t.Errorf("%s: sameSourcePath(%q, %q) = %t, want %t", tc.name, tc.recorded, tc.current, got, tc.want)
		}
	}
}
