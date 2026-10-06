package main

import (
	"testing"

	"github.com/sergiumoraru/tirion/internal/audit"
)

func TestFinalizeAuditMetrics_ComputesReasonRollups(t *testing.T) {
	t.Parallel()

	m := &audit.Metrics{
		HTTPPathUnmatched:      12,
		HTTPMethodMismatched:   5,
		QueueProducerTotal:     24,
		QueueProducerCount:     20,
		QueueExactMatched:      8,
		QueueNormalizedMatched: 6,
		DataWriteEntityCount:   10,
		DataSharedEntityCount:  10,
		DataCrossRepoCount:     4,
	}

	audit.Finalize(m)

	if m.UnresolvedHTTPCalls != 17 {
		t.Fatalf("expected unresolved HTTP calls 17, got %d", m.UnresolvedHTTPCalls)
	}
	if m.QueueMatchedCount != 14 {
		t.Fatalf("expected queue matched count 14, got %d", m.QueueMatchedCount)
	}
	if m.QueueMatchRate != 70.0 {
		t.Fatalf("expected queue match rate 70.0, got %.2f", m.QueueMatchRate)
	}
	if m.DataCoverageRate != 40.0 {
		t.Fatalf("expected data coverage rate 40.0, got %.2f", m.DataCoverageRate)
	}
}

func TestFinalizeAuditMetrics_ZeroDenominators(t *testing.T) {
	t.Parallel()

	m := &audit.Metrics{
		HTTPPathUnmatched:     0,
		HTTPMethodMismatched:  0,
		DataWriteEntityCount:  0,
		DataSharedEntityCount: 0,
	}

	audit.Finalize(m)

	if m.UnresolvedHTTPCalls != 0 {
		t.Fatalf("expected unresolved HTTP calls 0, got %d", m.UnresolvedHTTPCalls)
	}
	if m.QueueMatchRate != 100.0 {
		t.Fatalf("expected queue match rate 100.0 for zero producers, got %.2f", m.QueueMatchRate)
	}
	if m.DataCoverageRate != 100.0 {
		t.Fatalf("expected data coverage rate 100.0 for zero write entities, got %.2f", m.DataCoverageRate)
	}
}

func TestFinalizeAuditMetrics_NoSharedEntities(t *testing.T) {
	t.Parallel()

	m := &audit.Metrics{
		DataWriteEntityCount:  7,
		DataSharedEntityCount: 0,
		DataCrossRepoCount:    0,
	}

	audit.Finalize(m)

	if m.DataCoverageRate != 0.0 {
		t.Fatalf("expected data coverage rate 0.0 with write entities but no shared entities, got %.2f", m.DataCoverageRate)
	}
}
