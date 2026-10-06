package main

import (
	"testing"

	"github.com/sergiumoraru/tirion/internal/parser"
)

func TestSameLineAnonymousMethodMetadataUsesCompleteSpan(t *testing.T) {
	result := parser.NewJavaParser().ParseFile("Host.java", []byte(`class Host {
  @Deprecated void run() { Runnable task = new Runnable() { @Override public void run() {
    System.out.println("inner");
  }};
  }
}`))
	if result.ParseDiagnostics.Failed() || len(result.Functions) != 2 {
		t.Fatalf("unexpected parse: %+v / %+v", result.ParseDiagnostics, result.Functions)
	}
	metas := make(map[string][]functionMeta)
	for i, fn := range result.Functions {
		addFunctionMeta(metas, fn.Name, int64(i+1), fn.StartLine, fn.EndLine)
	}
	if result.Functions[0].StartLine != result.Functions[1].StartLine || result.Functions[0].EndLine == result.Functions[1].EndLine {
		t.Fatal("fixture must have equal starts and distinct ends")
	}
	for i, fn := range result.Functions {
		id, ok := selectFunctionIDBySpan(metas[fn.Name], fn.StartLine, fn.EndLine)
		if !ok || id != int64(i+1) {
			t.Fatalf("wrong owner for %+v: %d, %v", fn, id, ok)
		}
	}
	if _, ok := selectFunctionIDBySpan(metas["Host.run"], 2, 99); ok {
		t.Fatal("mismatched declaration span accepted")
	}
}
