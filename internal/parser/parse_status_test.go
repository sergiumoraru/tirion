package parser

import (
	"context"
	"strings"
	"testing"
	"time"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
)

func TestClassifyParseError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected ParseFailureKind
	}{
		{
			name:     "deadline exceeded",
			err:      context.DeadlineExceeded,
			expected: ParseFailureTimeout,
		},
		{
			name:     "cancelled",
			err:      context.Canceled,
			expected: ParseFailureTimeout,
		},
		{
			name:     "operation limit",
			err:      sitter.ErrOperationLimit,
			expected: ParseFailureTimeout,
		},
		{
			name:     "no language",
			err:      sitter.ErrNoLanguage,
			expected: ParseFailureSyntaxUnsupported,
		},
		{
			name:     "generic",
			err:      context.DeadlineExceeded, // overwritten below in loop
			expected: ParseFailureInternal,
		},
	}

	tests[4].err = assertError("something else")

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyParseError(tc.err)
			if got != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, got)
			}
		})
	}
}

func TestJavaScriptParser_SyntaxErrorDiagnostics(t *testing.T) {
	p := NewJavaScriptParser()
	result := p.ParseFile("broken.ts", []byte("function broken( {"))
	if result.ParseDiagnostics.FailureKind != ParseFailureSyntaxUnsupported {
		t.Fatalf("expected %q, got %q", ParseFailureSyntaxUnsupported, result.ParseDiagnostics.FailureKind)
	}
}

func TestJavaParser_SyntaxErrorDiagnostics(t *testing.T) {
	p := NewJavaParser()
	result := p.ParseFile("Broken.java", []byte("class Broken { public void f( { }"))
	if result.ParseDiagnostics.FailureKind != ParseFailureSyntaxUnsupported {
		t.Fatalf("expected %q, got %q", ParseFailureSyntaxUnsupported, result.ParseDiagnostics.FailureKind)
	}
}

func TestRecoverParsePanic(t *testing.T) {
	result := ParsedFile{}
	func() {
		defer recoverParsePanic(&result)
		panic("boom")
	}()
	if result.ParseDiagnostics.FailureKind != ParseFailureInternal {
		t.Fatalf("expected %q, got %q", ParseFailureInternal, result.ParseDiagnostics.FailureKind)
	}
	if result.ParseDiagnostics.Message == "" {
		t.Fatalf("expected panic message")
	}
}

type assertError string

func (e assertError) Error() string {
	return string(e)
}

// A reused parser must stay usable: a cancellable-context timeout once set the
// shared cancellation flag after successful parses, failing most later files,
// and an aborted parse left resumable state that corrupted the next file.
func TestParseWithTimeoutKeepsReusedParserUsable(t *testing.T) {
	p := sitter.NewParser()
	p.SetLanguage(golang.GetLanguage())
	src := []byte("package x\n\nfunc f() { a := []int{1, 2, 3}; _ = a }\n")
	for i := 0; i < 2000; i++ {
		tree, diag := parseWithTimeoutAfter(p, src, 30*time.Second)
		if tree == nil {
			t.Fatalf("parse %d failed on a reused parser: %+v", i, diag)
		}
		tree.Close()
	}

	huge := []byte(strings.Repeat("func g() { b := []int{1, 2, 3}; _ = b }\n", 400000))
	if tree, diag := parseWithTimeoutAfter(p, append([]byte("package y\n"), huge...), time.Millisecond); tree != nil || diag.FailureKind != ParseFailureTimeout {
		t.Fatalf("expected a timeout, got tree=%v diag=%+v", tree != nil, diag)
	}
	tree, diag := parseWithTimeoutAfter(p, src, 30*time.Second)
	if tree == nil {
		t.Fatalf("parse after a timeout failed: %+v", diag)
	}
	defer tree.Close()
	if root := tree.RootNode(); int(root.EndByte()) != len(src) || root.HasError() {
		t.Fatalf("parse after a timeout resumed stale state: endByte=%d len=%d hasError=%v", root.EndByte(), len(src), root.HasError())
	}
}
