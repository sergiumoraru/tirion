// Package diffparse reads unified diffs produced by `git diff` or plain
// `diff -u`. It is deliberately strict about hunk sizes (the declared counts
// must be honoured exactly) and tolerant about the things real tools do:
// whitespace-stripped blank context lines, "\ No newline" markers, multi-file
// plain diffs without `diff --git` headers, and tab-separated timestamps.
//
// Line numbers in a Hunk are pre-image (old file) coordinates for '-' and ' '
// lines. Callers that map a diff onto an index built from the pre-image must
// use those, never the post-image numbers.
package diffparse

import (
	"bufio"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Error reports a diff that cannot be interpreted. Callers translate it to a
// client error: the request body is at fault, not the server.
type Error struct {
	Msg  string
	File string
	Line int // 1-based line in the diff text, 0 when unknown
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Msg)
	if e.File != "" {
		b.WriteString(" in ")
		b.WriteString(e.File)
	}
	if e.Line > 0 {
		fmt.Fprintf(&b, " (diff line %d)", e.Line)
	}
	return b.String()
}

// IsError reports whether err is a diffparse.Error.
func IsError(err error) bool {
	var target *Error
	return errors.As(err, &target)
}

// Line is one hunk body line. Op is ' ', '-' or '+'.
type Line struct {
	Op   byte
	Text string
}

// Hunk is one @@ section. Counts are the declared (and verified) sizes.
type Hunk struct {
	OldStart, OldCount int
	NewStart, NewCount int
	Lines              []Line
}

// File is one file's section of a diff.
type File struct {
	OldPath string // "" when the file did not exist before (new file)
	NewPath string // "" when the file was deleted
	New     bool
	Deleted bool
	Binary  bool
	Hunks   []Hunk
}

// Path is the path in the pre-image tree: the old path, or the new path for a
// newly added file.
func (f File) Path() string {
	if f.OldPath != "" {
		return f.OldPath
	}
	return f.NewPath
}

var hunkRegex = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// Parse reads every file section of diff. It returns an *Error when the text
// is not a usable unified diff: no file sections at all, a hunk before any file
// header, an invalid hunk header, or a hunk whose body disagrees with its
// declared line counts.
func Parse(diff string) ([]File, error) {
	var files []File
	var cur *File
	headerOpen := false // between a file's first header line and its first hunk
	sawMinus := false   // a "--- " header was consumed for cur
	var hunk *Hunk
	var oldLeft, newLeft int
	lineNo := 0

	curName := func() string {
		if cur == nil {
			return ""
		}
		return cur.Path()
	}
	finishHunk := func() error {
		if hunk == nil {
			return nil
		}
		if oldLeft != 0 || newLeft != 0 {
			return &Error{Msg: "incomplete diff hunk", File: curName(), Line: lineNo}
		}
		cur.Hunks = append(cur.Hunks, *hunk)
		hunk = nil
		return nil
	}
	startFile := func() {
		files = append(files, File{})
		cur = &files[len(files)-1]
		headerOpen, sawMinus = true, false
	}
	// files may be reallocated by append; re-anchor cur after every append.
	reanchor := func() {
		if len(files) > 0 {
			cur = &files[len(files)-1]
		}
	}

	scanner := bufio.NewScanner(strings.NewReader(diff))
	scanner.Buffer(make([]byte, 4096), len(diff)+1)
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()

		if hunk != nil && (oldLeft > 0 || newLeft > 0) {
			// Inside a hunk body: only body lines are legal.
			switch {
			case line == "" || line[0] == ' ':
				text := ""
				if line != "" {
					text = line[1:]
				}
				if oldLeft == 0 || newLeft == 0 {
					return nil, &Error{Msg: "diff hunk exceeds declared size", File: curName(), Line: lineNo}
				}
				oldLeft--
				newLeft--
				hunk.Lines = append(hunk.Lines, Line{Op: ' ', Text: text})
			case line[0] == '-':
				if oldLeft == 0 {
					return nil, &Error{Msg: "diff hunk exceeds declared size", File: curName(), Line: lineNo}
				}
				oldLeft--
				hunk.Lines = append(hunk.Lines, Line{Op: '-', Text: line[1:]})
			case line[0] == '+':
				if newLeft == 0 {
					return nil, &Error{Msg: "diff hunk exceeds declared size", File: curName(), Line: lineNo}
				}
				newLeft--
				hunk.Lines = append(hunk.Lines, Line{Op: '+', Text: line[1:]})
			case line[0] == '\\':
				// "\ No newline at end of file"
			default:
				return nil, &Error{Msg: "incomplete diff hunk", File: curName(), Line: lineNo}
			}
			continue
		}
		if err := finishHunk(); err != nil {
			return nil, err
		}

		switch {
		case strings.HasPrefix(line, "diff --git "):
			startFile()
			path := GitHeaderPath(line)
			cur.OldPath, cur.NewPath = path, path
		case strings.HasPrefix(line, "--- "):
			// A "--- " outside a hunk body starts a plain-diff file unless a
			// git header is still open and has not yet seen its own "---".
			if cur == nil || !headerOpen || sawMinus {
				startFile()
				reanchor()
			}
			sawMinus = true
			old := headerPath(line[4:])
			if old == "/dev/null" {
				cur.New, cur.OldPath = true, ""
			} else {
				cur.OldPath = cleanPath(old, "a/")
			}
		case strings.HasPrefix(line, "+++ ") && cur != nil && headerOpen:
			next := headerPath(line[4:])
			if next == "/dev/null" {
				cur.Deleted, cur.NewPath = true, ""
			} else {
				cur.NewPath = cleanPath(next, "b/")
			}
		case strings.HasPrefix(line, "new file mode") && cur != nil && headerOpen:
			cur.New, cur.OldPath = true, ""
		case strings.HasPrefix(line, "deleted file mode") && cur != nil && headerOpen:
			cur.Deleted = true
		case strings.HasPrefix(line, "rename from ") && cur != nil && headerOpen:
			cur.OldPath = cleanPath(strings.TrimPrefix(line, "rename from "), "")
		case strings.HasPrefix(line, "rename to ") && cur != nil && headerOpen:
			cur.NewPath = cleanPath(strings.TrimPrefix(line, "rename to "), "")
		case strings.HasPrefix(line, "Binary files ") && strings.HasSuffix(line, " differ"):
			if cur == nil || !headerOpen {
				startFile()
				reanchor()
				a, b := binaryPaths(line)
				cur.OldPath, cur.NewPath = a, b
			}
			cur.Binary = true
		case strings.HasPrefix(line, "GIT binary patch") && cur != nil:
			cur.Binary = true
		case strings.HasPrefix(line, "@@ "):
			if cur == nil || (!headerOpen && len(cur.Hunks) == 0) {
				return nil, &Error{Msg: "diff hunk appears before any file header", Line: lineNo}
			}
			match := hunkRegex.FindStringSubmatch(line)
			if match == nil {
				return nil, &Error{Msg: "invalid diff hunk header " + strconv.Quote(line), File: curName(), Line: lineNo}
			}
			h := Hunk{OldCount: 1, NewCount: 1}
			var err error
			if h.OldStart, err = strconv.Atoi(match[1]); err != nil {
				return nil, &Error{Msg: "invalid diff hunk header " + strconv.Quote(line), File: curName(), Line: lineNo}
			}
			if match[2] != "" {
				if h.OldCount, err = strconv.Atoi(match[2]); err != nil {
					return nil, &Error{Msg: "invalid diff hunk header " + strconv.Quote(line), File: curName(), Line: lineNo}
				}
			}
			if h.NewStart, err = strconv.Atoi(match[3]); err != nil {
				return nil, &Error{Msg: "invalid diff hunk header " + strconv.Quote(line), File: curName(), Line: lineNo}
			}
			if match[4] != "" {
				if h.NewCount, err = strconv.Atoi(match[4]); err != nil {
					return nil, &Error{Msg: "invalid diff hunk header " + strconv.Quote(line), File: curName(), Line: lineNo}
				}
			}
			headerOpen = false
			hunk = &h
			oldLeft, newLeft = h.OldCount, h.NewCount
			if oldLeft == 0 && newLeft == 0 {
				// An empty hunk is complete as soon as it is declared.
				if err := finishHunk(); err != nil {
					return nil, err
				}
			}
		case cur != nil && !headerOpen && len(cur.Hunks) > 0 && line != "" && line != "-- " &&
			(line[0] == '+' || line[0] == '-' || line[0] == ' '):
			// Body-looking text after a hunk that already received every line it
			// declared: the hunk header undercounts.
			return nil, &Error{Msg: "diff hunk exceeds declared size", File: curName(), Line: lineNo}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, &Error{Msg: "cannot read diff: " + err.Error()}
	}
	if err := finishHunk(); err != nil {
		return nil, err
	}
	if len(files) == 0 && strings.TrimSpace(diff) != "" {
		return nil, &Error{Msg: "no file headers or hunks found; expected a unified diff"}
	}
	return files, nil
}

// headerPath drops the tab-separated timestamp plain `diff -u` appends and the
// trailing tab git appends to paths containing spaces.
func headerPath(raw string) string {
	if idx := strings.IndexByte(raw, '\t'); idx >= 0 {
		raw = raw[:idx]
	}
	return strings.TrimSpace(raw)
}

func binaryPaths(line string) (string, string) {
	rest := strings.TrimSuffix(strings.TrimPrefix(line, "Binary files "), " differ")
	if left, right, ok := strings.Cut(rest, " and "); ok {
		a, b := cleanPath(left, "a/"), cleanPath(right, "b/")
		if left == "/dev/null" {
			a = ""
		}
		if right == "/dev/null" {
			b = ""
		}
		return a, b
	}
	return "", ""
}

// GitHeaderPath extracts the destination path of a `diff --git a/x b/x` line in
// linear time. Rename and copy headers are refined by later rename/---/+++ lines.
func GitHeaderPath(line string) string {
	line = strings.TrimSpace(line)
	rest := strings.TrimPrefix(line, "diff --git ")
	if rest == line {
		return ""
	}
	// Paths with special characters are C-quoted: "a/x" "b/y".
	if strings.HasSuffix(rest, `"`) {
		if i := strings.LastIndex(rest[:len(rest)-1], ` "`); i >= 0 {
			return cleanPath(rest[i+1:], "b/")
		}
	}
	if strings.HasPrefix(rest, "a/") {
		// An unchanged path splits "a/X b/X" exactly in the middle, even when X
		// itself contains " b/".
		if n := len(rest); n >= 5 && (n-5)%2 == 0 {
			x := (n - 5) / 2
			if rest[2+x:5+x] == " b/" && rest[2:2+x] == rest[5+x:] {
				return cleanPath(rest[3+x:], "b/")
			}
		}
		if idx := strings.LastIndex(rest, " b/"); idx >= 0 {
			return cleanPath(rest[idx+1:], "b/")
		}
	}
	parts := strings.Fields(rest)
	if len(parts) < 2 {
		return ""
	}
	return NormalizePath(parts[len(parts)-1])
}

// unquotePath decodes git's C-style quoting ("d/\303\251.go" for d/é.go).
func unquotePath(path string) string {
	path = strings.TrimSpace(path)
	if len(path) >= 2 && path[0] == '"' && path[len(path)-1] == '"' {
		if unquoted, err := strconv.Unquote(path); err == nil {
			return unquoted
		}
		return path[1 : len(path)-1]
	}
	return path
}

// cleanPath unquotes a header path and strips exactly the given side prefix
// ("a/" for the old side, "b/" for the new side, "" for rename lines).
func cleanPath(path, prefix string) string {
	path = unquotePath(path)
	if prefix != "" {
		path = strings.TrimPrefix(path, prefix)
	}
	return strings.ReplaceAll(path, "\\", "/")
}

// NormalizePath unquotes a path and strips one a/ or b/ prefix.
func NormalizePath(path string) string {
	path = unquotePath(path)
	if strings.HasPrefix(path, "a/") || strings.HasPrefix(path, "b/") {
		path = path[2:]
	}
	return strings.ReplaceAll(path, "\\", "/")
}

// Edit is a contiguous change in pre-image coordinates. A pure insertion has
// Insertion set and spans the two old lines it falls between.
type Edit struct {
	Path       string
	Start, End int
	Insertion  bool
}

// Edits returns the edits of one file in pre-image coordinates. Unchanged
// context never forms an edit. A replacement (removed lines followed by added
// lines) is a removal of the old lines.
func (f File) Edits() []Edit {
	var edits []Edit
	for _, hunk := range f.Hunks {
		oldLine := hunk.OldStart
		if hunk.OldCount == 0 {
			oldLine++
		}
		var removedStart, removedEnd int
		added := false
		flush := func() {
			if removedStart > 0 {
				edits = append(edits, Edit{Path: f.Path(), Start: removedStart, End: removedEnd})
			} else if added {
				edits = append(edits, Edit{Path: f.Path(), Start: oldLine - 1, End: oldLine, Insertion: true})
			}
			removedStart, removedEnd, added = 0, 0, false
		}
		for _, l := range hunk.Lines {
			switch l.Op {
			case '-':
				if added {
					flush()
				}
				if removedStart == 0 {
					removedStart = oldLine
				}
				removedEnd = oldLine
				oldLine++
			case '+':
				added = true
			case ' ':
				flush()
				oldLine++
			}
		}
		flush()
	}
	return edits
}
