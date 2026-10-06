package trace

import "testing"

func TestPickDeterministicFunctionID_Empty(t *testing.T) {
	id, ok := pickDeterministicFunctionID(nil)
	if ok {
		t.Fatalf("expected no selection, got id=%d", id)
	}
}

func TestPickDeterministicFunctionID_StableByStartLine(t *testing.T) {
	candidates := []functionIDCandidate{
		{id: 30, startLine: 50, endLine: 70},
		{id: 11, startLine: 10, endLine: 40},
		{id: 21, startLine: 30, endLine: 31},
	}

	id, ok := pickDeterministicFunctionID(candidates)
	if !ok {
		t.Fatalf("expected a selected id")
	}
	if id != 11 {
		t.Fatalf("expected id 11, got %d", id)
	}
}

func TestPickDeterministicFunctionID_StableByEndLineThenID(t *testing.T) {
	candidates := []functionIDCandidate{
		{id: 44, startLine: 10, endLine: 25},
		{id: 12, startLine: 10, endLine: 20},
		{id: 7, startLine: 10, endLine: 20},
	}

	id, ok := pickDeterministicFunctionID(candidates)
	if !ok {
		t.Fatalf("expected a selected id")
	}
	if id != 7 {
		t.Fatalf("expected id 7, got %d", id)
	}
}

func TestNormalizeCallerIDs(t *testing.T) {
	ids := []string{"repo:b:Fn.b", "", "repo:a:Fn.a", "repo:b:Fn.b", "  repo:c:Fn.c  "}
	got := normalizeCallerIDs(ids)
	want := []string{"repo:a:Fn.a", "repo:b:Fn.b", "repo:c:Fn.c"}
	if len(got) != len(want) {
		t.Fatalf("expected %d ids, got %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func TestNormalizeInterfaceImpls(t *testing.T) {
	impls := []InterfaceImpl{
		{ClassID: 2, Repo: "r2", File: "b/B.java", ClassName: "B"},
		{ClassID: 1, Repo: "r1", File: "a/A.java", ClassName: "A"},
		{ClassID: 2, Repo: "r2", File: "b/B.java", ClassName: "B"},
	}
	got := normalizeInterfaceImpls(impls)
	if len(got) != 2 {
		t.Fatalf("expected 2 impls, got %d (%v)", len(got), got)
	}
	if got[0].ClassName != "A" || got[1].ClassName != "B" {
		t.Fatalf("unexpected order: %v", got)
	}
}

func TestPickUniqueCallerID(t *testing.T) {
	if got := pickUniqueCallerID(nil); got != "" {
		t.Fatalf("expected empty for nil input, got %q", got)
	}
	if got := pickUniqueCallerID([]string{"repo:a:A.f", "repo:b:B.f"}); got != "" {
		t.Fatalf("expected empty for non-unique input, got %q", got)
	}
	if got := pickUniqueCallerID([]string{"repo:a:A.f", "repo:a:A.f", "  repo:a:A.f "}); got != "repo:a:A.f" {
		t.Fatalf("expected unique caller id, got %q", got)
	}
}

func TestNormalizeTypeName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "com.acme.PaymentGateway", want: "PaymentGateway"},
		{input: "PaymentGateway<String>", want: "PaymentGateway"},
		{input: "PaymentGateway[]", want: "PaymentGateway"},
		{input: "PaymentGateway...", want: "PaymentGateway"},
		{input: "  ", want: ""},
	}
	for _, tc := range tests {
		if got := normalizeTypeName(tc.input); got != tc.want {
			t.Fatalf("normalizeTypeName(%q): expected %q, got %q", tc.input, tc.want, got)
		}
	}
}
