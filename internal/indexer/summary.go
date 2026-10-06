package indexer

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"
)

// Top-level phases partition wall time; nested phases break down publication.
// Workers collect overlapping subprocess times independently, and the
// coordinator sums them after group.Wait.
type indexTimings struct {
	phases     []*indexPhase
	discovered int
	warnings   []string
	copied     int
	workers    int
	worker     repositoryTimings
}

type indexPhase struct {
	name, detail string
	nested       bool
	duration     time.Duration
}

type repositoryTimings struct {
	parse, enrichment, copy time.Duration
}

// begin is also safe on an absent collector for callers that don't print a
// batch summary. The returned stop function is idempotent for error-path defers.
func (t *indexTimings) begin(name, detail string, nested bool) func() {
	if t == nil {
		return func() {}
	}
	phase := &indexPhase{name: name, detail: detail, nested: nested}
	t.phases = append(t.phases, phase)
	start := time.Now()
	stopped := false
	return func() {
		if !stopped {
			phase.duration = time.Since(start)
			stopped = true
		}
	}
}

func (t *indexTimings) print(total time.Duration, workspace string, result BatchResult, dryRun bool, err error) {
	status := "completed"
	if err != nil {
		status = "failed (timings include partial work)"
	} else if dryRun {
		status = "dry run"
	}
	fmt.Printf("\nIndex summary — %s\nWorkspace: %s\n", status, workspace)
	published := 0
	if err == nil && !dryRun {
		published = result.Parsed
	}
	fmt.Printf("Repositories: %d discovered, %d published, %d unchanged/skipped\n", t.discovered, published, result.Skipped)
	for _, warning := range t.warnings {
		fmt.Printf("Warning: %s\n", warning)
	}
	if t.copied > 0 {
		outcome := "published"
		if err != nil {
			outcome = "not published"
		}
		fmt.Printf("Additional affected callers: %d repository snapshots copied (%s)\n", t.copied, outcome)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "Phase\tElapsed\tWork")
	var accounted time.Duration
	var publication *indexPhase
	var publicationParts time.Duration
	for _, phase := range t.phases {
		name := phase.name
		if phase.nested {
			name = "  " + name
			publicationParts += phase.duration
		} else {
			accounted += phase.duration
		}
		if phase.name == "Publication" {
			publication = phase
		}
		fmt.Fprintf(w, "%s\t%.2fs\t%s\n", name, phase.duration.Seconds(), phase.detail)
	}
	if publication != nil {
		fmt.Fprintf(w, "  Transaction/selection work\t%.2fs\tLock, select snapshots, and commit or roll back\n", max(0, publication.duration-publicationParts).Seconds())
	}
	fmt.Fprintf(w, "Other/cleanup\t%.2fs\tCoordinator overhead and connection cleanup\n", max(0, total-accounted).Seconds())
	fmt.Fprintf(w, "TOTAL\t%s\tFull elapsed time, including discovery and cleanup\n", total.Round(10*time.Millisecond))
	_ = w.Flush()
	if publication != nil {
		fmt.Println("Indented publication steps are included in Publication; do not add them again.")
	} else if err == nil && !dryRun {
		fmt.Println("No new snapshots; input validation and publication were not needed.")
	}
	if t.workers > 0 {
		fmt.Printf("Summed repository worker times (up to %d concurrent; overlap, not additive to elapsed time):\n", t.workers)
		fmt.Printf("  Parsing: %.2fs — read source and persist declarations/calls\n", t.worker.parse.Seconds())
		fmt.Printf("  Enrichment: %.2fs — run requested HTTP, queue, and language/framework helpers\n", t.worker.enrichment.Seconds())
		fmt.Printf("  Candidate copying: %.2fs — reuse prior snapshots for incremental/enrichment-only work\n", t.worker.copy.Seconds())
	}
}
