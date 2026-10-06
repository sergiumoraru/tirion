package handlers

import (
	"os"
	"strings"
	"testing"

	"github.com/sergiumoraru/tirion/internal/graph"
)

// liveDBRequired reports whether CI demands the DB-backed tests actually run.
func liveDBRequired() bool { return os.Getenv("TIRION_REQUIRE_LIVE_DB") == "1" }

// skipOrFailLiveDB skips a DB-backed test locally but fails it when
// TIRION_REQUIRE_LIVE_DB=1, so an unreachable or unconfigured database can never
// turn a required gate into a silent pass.
func skipOrFailLiveDB(t *testing.T, format string, args ...any) {
	t.Helper()
	if liveDBRequired() {
		t.Fatalf("TIRION_REQUIRE_LIVE_DB=1 but the live database is unavailable: "+format, args...)
	}
	t.Skipf(format, args...)
}

// liveTestStorage opens the dedicated test database from DATABASE_URL and
// initializes the schema, so the tests also run on an empty database.
func liveTestStorage(t *testing.T) *graph.Storage {
	t.Helper()
	dbURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if os.Getenv("TIRION_WORKSPACE_LIVE_TESTS") != "1" || dbURL == "" {
		skipOrFailLiveDB(t, "set TIRION_WORKSPACE_LIVE_TESTS=1 and DATABASE_URL for a dedicated test database")
	}
	storage, err := graph.NewStorage(dbURL)
	if err != nil {
		skipOrFailLiveDB(t, "local db unavailable: %v", err)
	}
	t.Cleanup(storage.Close)
	return storage
}
