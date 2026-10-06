package owners

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type CodeOwners struct {
	Entries []Entry
}

// Entry is one CODEOWNERS rule. Owners is empty for a rule that deliberately
// leaves matching files unowned (a pattern with no owners).
type Entry struct {
	Pattern string
	Owners  []string
	regex   *regexp.Regexp
}

// LoadCodeOwners reads the repository's CODEOWNERS file using GitHub's lookup
// order: .github/, the repository root, then docs/. The first file found wins.
func LoadCodeOwners(repoPath string) (*CodeOwners, error) {
	if repoPath == "" {
		return nil, nil
	}
	candidates := []string{
		filepath.Join(repoPath, ".github", "CODEOWNERS"),
		filepath.Join(repoPath, "CODEOWNERS"),
		filepath.Join(repoPath, "docs", "CODEOWNERS"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return parseCodeOwners(candidate)
		}
	}
	return nil, nil
}

func parseCodeOwners(path string) (*CodeOwners, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return ParseCodeOwners(strings.Join(lines, "\n")), nil
}

// ParseCodeOwners parses CODEOWNERS text. Invalid lines are skipped. Later
// entries take precedence (see Match).
func ParseCodeOwners(content string) *CodeOwners {
	var entries []Entry
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimPrefix(raw, "\xef\xbb\xbf"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Negation is not part of CODEOWNERS; section headers ([Section] or
		// ^[Section], a GitLab extension) are not path rules.
		if strings.HasPrefix(line, "!") || strings.HasPrefix(line, "[") || strings.HasPrefix(line, "^[") {
			continue
		}
		fields := splitUnescaped(line)
		if len(fields) == 0 {
			continue
		}
		pattern := fields[0]
		var owners []string
		for _, field := range fields[1:] {
			if strings.HasPrefix(field, "#") {
				break // trailing comment
			}
			owners = append(owners, field)
		}
		re, err := compilePattern(pattern)
		if err != nil {
			continue
		}
		entries = append(entries, Entry{Pattern: pattern, Owners: owners, regex: re})
	}
	return &CodeOwners{Entries: entries}
}

// splitUnescaped splits on whitespace that is not escaped with a backslash. The
// backslash is kept so pattern translation can see it.
func splitUnescaped(line string) []string {
	var fields []string
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			fields = append(fields, current.String())
			current.Reset()
		}
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && i+1 < len(line):
			current.WriteByte(c)
			i++
			current.WriteByte(line[i])
		case c == ' ' || c == '\t':
			flush()
		default:
			current.WriteByte(c)
		}
	}
	flush()
	return fields
}

// Match returns the owners of path: the owners of the last rule that matches.
// A matching rule without owners clears ownership.
func (c *CodeOwners) Match(path string) []string {
	owners, _ := c.MatchRule(path)
	return owners
}

// MatchRule is Match that also reports whether any rule matched, so a rule with
// no owners (an explicit "unowned" line) is distinguishable from no rule at all.
func (c *CodeOwners) MatchRule(path string) ([]string, bool) {
	if c == nil {
		return nil, false
	}
	normalized := strings.TrimPrefix(filepath.ToSlash(path), "./")
	normalized = strings.TrimPrefix(normalized, "/")
	var owners []string
	matched := false
	for _, entry := range c.Entries {
		if entry.regex != nil && entry.regex.MatchString(normalized) {
			owners, matched = entry.Owners, true
		}
	}
	return owners, matched
}

// compilePattern translates a CODEOWNERS pattern (gitignore-style) to a regexp
// over slash-separated repository-relative paths:
//   - a pattern containing a slash other than a trailing one is anchored to the
//     repository root; otherwise it may match at any depth
//   - a match on a directory covers everything beneath it
//   - a trailing slash restricts the match to directories (so it needs content)
//   - "*" and "?" never cross "/"; "**" as a whole segment spans directories
//   - a backslash escapes the next character
func compilePattern(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, fmt.Errorf("empty pattern")
	}
	dirOnly := strings.HasSuffix(pattern, "/") && !strings.HasSuffix(pattern, `\/`)
	body := pattern
	if dirOnly {
		body = strings.TrimSuffix(body, "/")
	}
	anchored := strings.Contains(body, "/")
	body = strings.TrimPrefix(body, "/")
	if body == "" {
		return nil, fmt.Errorf("pattern %q matches nothing", pattern)
	}
	segments := strings.Split(body, "/")
	for _, segment := range segments {
		if segment == "" {
			return nil, fmt.Errorf("pattern %q has an empty path segment", pattern)
		}
	}

	var re strings.Builder
	if anchored {
		re.WriteString("^")
	} else {
		re.WriteString("^(?:.*/)?")
	}
	last := len(segments) - 1
	terminalStars := false
	for i, segment := range segments {
		switch {
		case segment == "**" && last == 0:
			re.WriteString(".+")
			terminalStars = true
		case segment == "**" && i == last:
			re.WriteString(".+")
			terminalStars = true
		case segment == "**":
			// Leading or inner "**/": zero or more directories.
			re.WriteString("(?:.*/)?")
		default:
			re.WriteString(translateSegment(segment))
			if i < last {
				re.WriteString("/")
			}
		}
	}
	switch {
	case terminalStars:
		re.WriteString("$")
	case dirOnly:
		re.WriteString("/.+$")
	case hasWildcard(segments[last]):
		// GitHub CODEOWNERS (unlike gitignore): "docs/*" owns docs/a.md but not
		// docs/sub/a.md; a wildcard final segment matches only that level.
		re.WriteString("$")
	default:
		re.WriteString("(?:/.+)?$")
	}
	return regexp.Compile(re.String())
}

// hasWildcard reports an unescaped "*" or "?" in a pattern segment.
func hasWildcard(segment string) bool {
	for i := 0; i < len(segment); i++ {
		switch segment[i] {
		case '\\':
			i++
		case '*', '?':
			return true
		}
	}
	return false
}

func translateSegment(segment string) string {
	var out strings.Builder
	runes := []rune(segment)
	for i := 0; i < len(runes); i++ {
		switch r := runes[i]; r {
		case '\\':
			if i+1 < len(runes) {
				i++
				out.WriteString(regexp.QuoteMeta(string(runes[i])))
			} else {
				out.WriteString(regexp.QuoteMeta(`\`))
			}
		case '*':
			for i+1 < len(runes) && runes[i+1] == '*' {
				i++
			}
			out.WriteString(`[^/]*`)
		case '?':
			out.WriteString(`[^/]`)
		default:
			out.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return out.String()
}
