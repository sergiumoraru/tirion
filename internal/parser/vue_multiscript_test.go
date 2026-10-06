package parser

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestVueMultiScriptLineOffsets(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve test file path")
	}
	testPath := filepath.Join(filepath.Dir(file), "..", "..", "testdata", "TestVueMultiScript.vue")
	content, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatalf("failed to read test data: %v", err)
	}

	parser := NewJavaScriptParser()
	result := parser.ParseFile(testPath, content)

	var fooLine, barLine int
	for _, fn := range result.Functions {
		switch fn.Name {
		case "foo":
			fooLine = fn.StartLine
		case "bar":
			barLine = fn.StartLine
		}
	}

	if fooLine != 6 {
		t.Fatalf("expected foo to start on line 6, got %d", fooLine)
	}
	if barLine != 12 {
		t.Fatalf("expected bar to start on line 12, got %d", barLine)
	}
}
