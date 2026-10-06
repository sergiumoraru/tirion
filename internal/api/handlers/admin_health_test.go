package handlers

import (
	"testing"
	"time"
)

func TestBuildAdminHealthReposUsesDriftAwareFreshness(t *testing.T) {
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	oldIndexedAt := now.Add(-14 * 24 * time.Hour).Format(time.RFC3339)
	recentIndexedAt := now.Add(-2 * time.Hour).Format(time.RFC3339)

	repos, _, _, recent, aging, stale := buildAdminHealthRepos([]adminRepoRow{
		{
			ID:              1,
			Name:            "current-but-old",
			Path:            "/repos/current-but-old",
			UpdatedAt:       oldIndexedAt,
			FreshnessStatus: "recent",
			FileCount:       1,
		},
		{
			ID:              2,
			Name:            "drifted-and-old",
			Path:            "/repos/drifted-and-old",
			UpdatedAt:       oldIndexedAt,
			FreshnessStatus: "stale",
			FileCount:       1,
		},
		{
			ID:              3,
			Name:            "recent-but-drifted",
			Path:            "/repos/recent-but-drifted",
			UpdatedAt:       recentIndexedAt,
			FreshnessStatus: "recent",
			ReparseNeeded:   true,
			DriftStatus:     "drift",
			FileCount:       1,
		},
	}, now)

	if recent != 1 || aging != 0 || stale != 2 {
		t.Fatalf("counts recent=%d aging=%d stale=%d, want 1/0/2", recent, aging, stale)
	}
	if repos[0].FreshnessStatus != "recent" {
		t.Fatalf("old but current repo freshness = %q, want recent", repos[0].FreshnessStatus)
	}
	if repos[1].FreshnessStatus != "stale" {
		t.Fatalf("drifted old repo freshness = %q, want stale", repos[1].FreshnessStatus)
	}
	if repos[2].FreshnessStatus != "stale" {
		t.Fatalf("recent drifted repo freshness = %q, want stale", repos[2].FreshnessStatus)
	}
}
