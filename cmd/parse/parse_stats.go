package main

import (
	"fmt"
	"io"

	"github.com/sergiumoraru/tirion/internal/parser"
	"github.com/sergiumoraru/tirion/internal/sourceindex"
)

type parseFailureCategory string

const (
	parseFailureSyntaxUnsupported parseFailureCategory = "syntax_unsupported"
	parseFailureTimeout           parseFailureCategory = "timeout"
	parseFailureInternal          parseFailureCategory = "internal_error"
)

var parseFailureOrder = []parseFailureCategory{
	parseFailureSyntaxUnsupported,
	parseFailureTimeout,
	parseFailureInternal,
}

type parseSkipCategory string

const (
	parseSkipUnsupportedExtension parseSkipCategory = "unsupported_extension"
	parseSkipExcludedPath         parseSkipCategory = "excluded_path"
	parseSkipExcludedFilePattern  parseSkipCategory = "excluded_file_pattern"
	parseSkipXMLNotMapper         parseSkipCategory = "xml_not_mapper"
	parseSkipReadError            parseSkipCategory = "read_error"
	parseSkipIncrementalUnchanged parseSkipCategory = "incremental_unchanged"
	parseSkipNoExtractedContent   parseSkipCategory = "no_extracted_content"
	parseSkipGeneratedJavaScript  parseSkipCategory = "generated_or_vendored_js"
)

var parseSkipOrder = []parseSkipCategory{
	parseSkipUnsupportedExtension,
	parseSkipExcludedPath,
	parseSkipExcludedFilePattern,
	parseSkipXMLNotMapper,
	parseSkipReadError,
	parseSkipIncrementalUnchanged,
	parseSkipNoExtractedContent,
	parseSkipGeneratedJavaScript,
}

type parseCounters struct {
	filesScanned int
	filesParsed  int
	filesFailed  int
	filesSkipped int

	failedByCategory  map[parseFailureCategory]int
	skippedByCategory map[parseSkipCategory]int
	failures          []parseFailure
}

// maxListedFailures bounds the per-file failure list in the run summary; the
// category counts always cover every failure.
const maxListedFailures = 50

type parseFailure struct {
	path     string
	category parseFailureCategory
	message  string
}

func newParseCounters() parseCounters {
	return parseCounters{
		failedByCategory:  make(map[parseFailureCategory]int),
		skippedByCategory: make(map[parseSkipCategory]int),
	}
}

func (c *parseCounters) markScanned() {
	c.filesScanned++
}

func (c *parseCounters) markParsed() {
	c.filesParsed++
}

func (c *parseCounters) markFailed(category parseFailureCategory) {
	c.filesFailed++
	c.failedByCategory[category]++
}

// recordFailure remembers a failed file for the run summary. Syntax-level
// partial trees are expected noise and are only counted; timeouts and internal
// errors lose facts and are always listed.
func (c *parseCounters) recordFailure(path string, d parser.ParseDiagnostics) {
	if d.FailureKind == parser.ParseFailureSyntaxUnsupported {
		return
	}
	if len(c.failures) < maxListedFailures {
		c.failures = append(c.failures, parseFailure{path: path, category: classifyFailureCategory(d), message: d.Message})
	}
}

func (c *parseCounters) markSkipped(category parseSkipCategory) {
	c.filesSkipped++
	c.skippedByCategory[category]++
}

func classifyFailureCategory(d parser.ParseDiagnostics) parseFailureCategory {
	switch d.FailureKind {
	case parser.ParseFailureTimeout:
		return parseFailureTimeout
	case parser.ParseFailureInternal:
		return parseFailureInternal
	case parser.ParseFailureSyntaxUnsupported, parser.ParseFailureNone:
		return parseFailureSyntaxUnsupported
	default:
		return parseFailureInternal
	}
}

func writeParseCounters(w io.Writer, counters parseCounters) {
	fmt.Fprintf(w, "Files scanned: %d\n", counters.filesScanned)
	fmt.Fprintf(w, "Files parsed:  %d\n", counters.filesParsed)
	fmt.Fprintf(w, "Files failed:  %d\n", counters.filesFailed)
	fmt.Fprintf(w, "Files skipped: %d\n", counters.filesSkipped)
	fmt.Fprintln(w, "Failure categories:")
	for _, category := range parseFailureOrder {
		fmt.Fprintf(w, "  %s: %d\n", category, counters.failedByCategory[category])
	}
	fmt.Fprintln(w, "Skip categories:")
	for _, category := range parseSkipOrder {
		fmt.Fprintf(w, "  %s: %d\n", category, counters.skippedByCategory[category])
	}
	if len(counters.failures) > 0 {
		fmt.Fprintln(w, "Files that lost facts (timeout or internal error):")
		for _, failure := range counters.failures {
			fmt.Fprintf(w, "  [%s] %s: %s\n", failure.category, failure.path, failure.message)
		}
		if lost := counters.failedByCategory[parseFailureTimeout] + counters.failedByCategory[parseFailureInternal]; lost > len(counters.failures) {
			fmt.Fprintf(w, "  ... and %d more\n", lost-len(counters.failures))
		}
	}
}

// parseModuleFile runs a module's parser and converts a panic into a failed
// file so one pathological source cannot abort the repository.
func parseModuleFile(module fileParserModule, relPath, absPath string, content []byte) (result parser.ParsedFile) {
	defer func() {
		if r := recover(); r != nil {
			result = parser.ParsedFile{
				Path:     relPath,
				Language: module.ID(),
				ParseDiagnostics: parser.ParseDiagnostics{
					FailureKind: parser.ParseFailureInternal,
					Message:     fmt.Sprintf("panic: %v", r),
					Recovered:   true,
				},
			}
		}
	}()
	return module.Parse(relPath, absPath, content)
}

// logTextNormalization reports source repairs that are not already visible in
// the bytes: a decode or removal changes indexed text relative to the file.
func logTextNormalization(w io.Writer, relPath string, n sourceindex.TextNormalization) {
	if n.Encoding != "" {
		fmt.Fprintf(w, "Note: %s is %s; decoded to UTF-8 for indexing\n", relPath, n.Encoding)
	}
	if n.NULsRemoved > 0 {
		fmt.Fprintf(w, "Warning: %s contains %d NUL byte(s); removed from indexed text\n", relPath, n.NULsRemoved)
	}
	if n.InvalidUTF8 {
		fmt.Fprintf(w, "Warning: %s contains invalid UTF-8; undecodable bytes are represented as U+FFFD in indexed text\n", relPath)
	}
}
