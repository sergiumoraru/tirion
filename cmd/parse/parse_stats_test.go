package main

import (
	"bytes"
	"testing"

	"github.com/sergiumoraru/tirion/internal/parser"
)

func TestClassifyFailureCategory(t *testing.T) {
	tests := []struct {
		name     string
		input    parser.ParseDiagnostics
		expected parseFailureCategory
	}{
		{
			name: "syntax unsupported",
			input: parser.ParseDiagnostics{
				FailureKind: parser.ParseFailureSyntaxUnsupported,
			},
			expected: parseFailureSyntaxUnsupported,
		},
		{
			name: "timeout",
			input: parser.ParseDiagnostics{
				FailureKind: parser.ParseFailureTimeout,
			},
			expected: parseFailureTimeout,
		},
		{
			name: "internal",
			input: parser.ParseDiagnostics{
				FailureKind: parser.ParseFailureInternal,
			},
			expected: parseFailureInternal,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyFailureCategory(tc.input)
			if got != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, got)
			}
		})
	}
}

func TestWriteParseCounters_DeterministicOrder(t *testing.T) {
	counters := newParseCounters()
	counters.markScanned()
	counters.markScanned()
	counters.markScanned()
	counters.markParsed()
	counters.markFailed(parseFailureTimeout)
	counters.markSkipped(parseSkipUnsupportedExtension)

	var out bytes.Buffer
	writeParseCounters(&out, counters)

	expected := "" +
		"Files scanned: 3\n" +
		"Files parsed:  1\n" +
		"Files failed:  1\n" +
		"Files skipped: 1\n" +
		"Failure categories:\n" +
		"  syntax_unsupported: 0\n" +
		"  timeout: 1\n" +
		"  internal_error: 0\n" +
		"Skip categories:\n" +
		"  unsupported_extension: 1\n" +
		"  excluded_path: 0\n" +
		"  excluded_file_pattern: 0\n" +
		"  xml_not_mapper: 0\n" +
		"  read_error: 0\n" +
		"  incremental_unchanged: 0\n" +
		"  no_extracted_content: 0\n" +
		"  generated_or_vendored_js: 0\n"

	if out.String() != expected {
		t.Fatalf("unexpected output:\n%s", out.String())
	}
}
