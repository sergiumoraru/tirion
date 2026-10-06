package main

import (
	"os"
	"path/filepath"
)

// liveEnvironment returns the disposable database URL and, when needBin is set,
// the directory with binaries built by scripts/build-local.sh. A live test skips
// when its opt-in variable or resources are absent, unless TIRION_REQUIRE_LIVE_DB=1
// (set in CI), where any such gap fails the test so database-backed gates cannot
// silently stop running.
func liveEnvironment(t skipOrFail, optIn string, needBin bool) (dbURL, bin string) {
	t.Helper()
	if optIn != "" && os.Getenv(optIn) != "1" {
		liveUnavailable(t, "%s is not set to 1", optIn)
	}
	dbURL = os.Getenv("DATABASE_URL")
	if dbURL == "" {
		liveUnavailable(t, "DATABASE_URL is not set")
	}
	if needBin {
		bin = binaryDirectory(t)
	}
	return dbURL, bin
}

// binaryDirectory returns TIRION_TEST_BIN for tests that run the built commands
// but need no database.
func binaryDirectory(t skipOrFail) string {
	t.Helper()
	dir := os.Getenv("TIRION_TEST_BIN")
	if dir == "" {
		liveUnavailable(t, "TIRION_TEST_BIN is not set")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// skipOrFail is the part of *testing.T that liveUnavailable needs.
type skipOrFail interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
	Skipf(format string, args ...any)
}

// liveUnavailable skips, or fails when TIRION_REQUIRE_LIVE_DB=1 demands that
// database-backed tests really run.
func liveUnavailable(t skipOrFail, format string, args ...any) {
	t.Helper()
	if os.Getenv("TIRION_REQUIRE_LIVE_DB") == "1" {
		t.Fatalf("TIRION_REQUIRE_LIVE_DB=1 but "+format, args...)
		return
	}
	t.Skipf(format, args...)
}
