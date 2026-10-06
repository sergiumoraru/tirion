package parser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	sitter "github.com/smacker/go-tree-sitter"
)

// defaultParseTimeout bounds one tree-sitter parse. Pathological or generated
// sources can otherwise stall a whole repository run. CODE_INTEL_PARSE_TIMEOUT_MS
// overrides it; 0 disables the limit.
const defaultParseTimeout = 30 * time.Second

var (
	parseTimeoutOnce   sync.Once
	cachedParseTimeout time.Duration
)

func parseTimeout() time.Duration {
	parseTimeoutOnce.Do(func() {
		timeout, warning := parseTimeoutFromEnv(os.Getenv("CODE_INTEL_PARSE_TIMEOUT_MS"))
		if warning != "" {
			fmt.Fprintln(os.Stderr, warning)
		}
		cachedParseTimeout = timeout
	})
	return cachedParseTimeout
}

// parseTimeoutFromEnv interprets CODE_INTEL_PARSE_TIMEOUT_MS: empty keeps the
// default, a positive integer sets milliseconds, 0 disables the limit, and
// anything else keeps the default and reports why.
func parseTimeoutFromEnv(raw string) (time.Duration, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultParseTimeout, ""
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms < 0 {
		return defaultParseTimeout, fmt.Sprintf("Warning: ignoring invalid CODE_INTEL_PARSE_TIMEOUT_MS=%q; using %s", raw, defaultParseTimeout)
	}
	return time.Duration(ms) * time.Millisecond, ""
}

func parseWithTimeout(p *sitter.Parser, content []byte) (*sitter.Tree, ParseDiagnostics) {
	return parseWithTimeoutAfter(p, content, parseTimeout())
}

func parseWithTimeoutAfter(p *sitter.Parser, content []byte, timeout time.Duration) (*sitter.Tree, ParseDiagnostics) {
	if p == nil {
		return nil, ParseDiagnostics{
			FailureKind: ParseFailureSyntaxUnsupported,
			Message:     "parser unavailable",
		}
	}

	// Use tree-sitter's own timeout, not a cancellable context: sitter.ParseCtx's
	// watcher goroutine can observe the deferred cancel after a successful parse
	// and set the parser's shared cancellation flag, which then fails every later
	// parse on the reused parser. 0 disables the limit.
	p.SetOperationLimit(int(timeout / time.Microsecond))
	// An aborted parse leaves resumable state; every file must start from scratch.
	p.Reset()
	tree, err := p.ParseCtx(context.Background(), nil, content)
	if err != nil || tree == nil {
		p.Reset()
	}
	if err != nil {
		return nil, ParseDiagnostics{
			FailureKind: classifyParseError(err),
			Message:     err.Error(),
		}
	}
	if tree == nil {
		return nil, ParseDiagnostics{
			FailureKind: ParseFailureSyntaxUnsupported,
			Message:     "empty parse tree",
		}
	}
	return tree, ParseDiagnostics{}
}

// recoverParsePanic turns a parser panic into a per-file failure. Facts
// extracted before the panic are discarded: an interrupted extraction pass
// leaves a function without its calls or a class without its endpoints, which
// is worse than an explicit empty result with a recorded failure. It must be
// deferred directly, with result being the function's named return value; an
// unnamed return would hand the caller a zero ParsedFile with no diagnostics.
func recoverParsePanic(result *ParsedFile) {
	if r := recover(); r != nil {
		message := fmt.Sprintf("panic: %v", r)
		if site := panicSite(debug.Stack()); site != "" {
			message += " (at " + site + ")"
		}
		*result = ParsedFile{
			Path:        result.Path,
			Language:    result.Language,
			JavaPackage: result.JavaPackage,
			ParseDiagnostics: ParseDiagnostics{
				FailureKind: ParseFailureInternal,
				Message:     message,
				Recovered:   true,
			},
		}
	}
}

// parseGuarded runs parse and converts a panic into a failed ParsedFile that
// still names the file and language, for parsers without their own recovery.
func parseGuarded(path, language string, parse func() ParsedFile) (result ParsedFile) {
	result.Path, result.Language = path, language
	defer recoverParsePanic(&result)
	return parse()
}

// panicSite returns the innermost parser-package source location in a stack,
// which is where a recovered panic originated.
func panicSite(stack []byte) string {
	const marker = "/internal/parser/"
	for _, line := range strings.Split(string(stack), "\n") {
		line = strings.TrimSpace(line)
		i := strings.Index(line, marker)
		if i < 0 || strings.Contains(line, "parse_status.go") {
			continue
		}
		line = line[i+len("/internal/"):]
		if j := strings.Index(line, " +0x"); j > 0 {
			line = line[:j]
		}
		return line
	}
	return ""
}

func classifyParseError(err error) ParseFailureKind {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, sitter.ErrOperationLimit):
		return ParseFailureTimeout
	case errors.Is(err, sitter.ErrNoLanguage):
		return ParseFailureSyntaxUnsupported
	default:
		return ParseFailureInternal
	}
}

func syntaxErrorDiagnostics() ParseDiagnostics {
	return ParseDiagnostics{
		FailureKind: ParseFailureSyntaxUnsupported,
		Message:     "tree-sitter syntax errors in parse tree",
		PartialTree: true,
	}
}
