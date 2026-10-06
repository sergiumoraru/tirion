package diffparse

import (
	"reflect"
	"strings"
	"testing"
)

func lines(parts ...string) string { return strings.Join(parts, "\n") + "\n" }

func TestParseFileLayouts(t *testing.T) {
	tests := []struct {
		name string
		diff string
		want []File // Hunks compared by counts only (see summarize)
	}{
		{
			name: "git diff single file",
			diff: lines(
				"diff --git a/src/a.go b/src/a.go",
				"index 1111111..2222222 100644",
				"--- a/src/a.go",
				"+++ b/src/a.go",
				"@@ -1,3 +1,3 @@ func main()",
				" one",
				"-two",
				"+TWO",
				" three",
			),
			want: []File{{OldPath: "src/a.go", NewPath: "src/a.go", Hunks: []Hunk{{OldStart: 1, OldCount: 3, NewStart: 1, NewCount: 3}}}},
		},
		{
			name: "plain diff -u with two files and timestamps",
			diff: lines(
				"--- a/x.go\t2024-01-01 00:00:00.000000000 +0000",
				"+++ b/x.go\t2024-01-02 00:00:00.000000000 +0000",
				"@@ -1,2 +1,2 @@",
				"-a",
				"+b",
				" c",
				"--- a/y.go\t2024-01-01 00:00:00.000000000 +0000",
				"+++ b/y.go\t2024-01-02 00:00:00.000000000 +0000",
				"@@ -5 +5 @@",
				"-old",
				"+new",
			),
			want: []File{
				{OldPath: "x.go", NewPath: "x.go", Hunks: []Hunk{{OldStart: 1, OldCount: 2, NewStart: 1, NewCount: 2}}},
				{OldPath: "y.go", NewPath: "y.go", Hunks: []Hunk{{OldStart: 5, OldCount: 1, NewStart: 5, NewCount: 1}}},
			},
		},
		{
			name: "new file",
			diff: lines(
				"diff --git a/n.go b/n.go",
				"new file mode 100644",
				"--- /dev/null",
				"+++ b/n.go",
				"@@ -0,0 +1,2 @@",
				"+package n",
				"+func N() {}",
			),
			want: []File{{NewPath: "n.go", New: true, Hunks: []Hunk{{OldStart: 0, OldCount: 0, NewStart: 1, NewCount: 2}}}},
		},
		{
			name: "plain diff new file",
			diff: lines("--- /dev/null", "+++ b/n.go", "@@ -0,0 +1 @@", "+x"),
			want: []File{{NewPath: "n.go", New: true, Hunks: []Hunk{{OldStart: 0, OldCount: 0, NewStart: 1, NewCount: 1}}}},
		},
		{
			name: "deleted file",
			diff: lines(
				"diff --git a/d.go b/d.go",
				"deleted file mode 100644",
				"--- a/d.go",
				"+++ /dev/null",
				"@@ -1,2 +0,0 @@",
				"-a",
				"-b",
			),
			want: []File{{OldPath: "d.go", Deleted: true, Hunks: []Hunk{{OldStart: 1, OldCount: 2, NewStart: 0, NewCount: 0}}}},
		},
		{
			name: "rename with edit keeps the old path",
			diff: lines(
				"diff --git a/old.go b/new.go",
				"similarity index 90%",
				"rename from old.go",
				"rename to new.go",
				"--- a/old.go",
				"+++ b/new.go",
				"@@ -1 +1 @@",
				"-x",
				"+y",
			),
			want: []File{{OldPath: "old.go", NewPath: "new.go", Hunks: []Hunk{{OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1}}}},
		},
		{
			name: "mode change only has no hunks",
			diff: lines("diff --git a/run.sh b/run.sh", "old mode 100644", "new mode 100755"),
			want: []File{{OldPath: "run.sh", NewPath: "run.sh"}},
		},
		{
			name: "git binary file",
			diff: lines("diff --git a/i.png b/i.png", "index 1..2 100644", "Binary files a/i.png and b/i.png differ"),
			want: []File{{OldPath: "i.png", NewPath: "i.png", Binary: true}},
		},
		{
			name: "plain diff binary file",
			diff: lines("Binary files a/i.png and b/i.png differ"),
			want: []File{{OldPath: "i.png", NewPath: "i.png", Binary: true}},
		},
		{
			name: "git diff of two files",
			diff: lines(
				"diff --git a/a.go b/a.go", "--- a/a.go", "+++ b/a.go", "@@ -1 +1 @@", "-a", "+b",
				"diff --git a/b.go b/b.go", "--- a/b.go", "+++ b/b.go", "@@ -2 +2 @@", "-c", "+d",
			),
			want: []File{
				{OldPath: "a.go", NewPath: "a.go", Hunks: []Hunk{{OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1}}},
				{OldPath: "b.go", NewPath: "b.go", Hunks: []Hunk{{OldStart: 2, OldCount: 1, NewStart: 2, NewCount: 1}}},
			},
		},
		{
			name: "no newline marker is not content",
			diff: lines("--- a/a.txt", "+++ b/a.txt", "@@ -1 +1 @@", "-a", "\\ No newline at end of file", "+b", "\\ No newline at end of file"),
			want: []File{{OldPath: "a.txt", NewPath: "a.txt", Hunks: []Hunk{{OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1}}}},
		},
		{
			name: "quoted and spaced paths",
			diff: lines("--- \"a/dir/my file.go\"", "+++ \"b/dir/my file.go\"", "@@ -1 +1 @@", "-a", "+b"),
			want: []File{{OldPath: "dir/my file.go", NewPath: "dir/my file.go", Hunks: []Hunk{{OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1}}}},
		},
		{
			name: "format-patch signature after the last hunk is ignored",
			diff: lines("--- a/a.go", "+++ b/a.go", "@@ -1 +1 @@", "-a", "+b", "-- ", "2.40.0"),
			want: []File{{OldPath: "a.go", NewPath: "a.go", Hunks: []Hunk{{OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1}}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.diff)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d files, want %d: %+v", len(got), len(tt.want), got)
			}
			for i := range got {
				g, w := got[i], tt.want[i]
				if g.OldPath != w.OldPath || g.NewPath != w.NewPath || g.New != w.New || g.Deleted != w.Deleted || g.Binary != w.Binary {
					t.Errorf("file %d: got %+v, want %+v", i, summarize(g), summarize(w))
				}
				if len(g.Hunks) != len(w.Hunks) {
					t.Fatalf("file %d: got %d hunks, want %d", i, len(g.Hunks), len(w.Hunks))
				}
				for j := range g.Hunks {
					gh, wh := g.Hunks[j], w.Hunks[j]
					if gh.OldStart != wh.OldStart || gh.OldCount != wh.OldCount || gh.NewStart != wh.NewStart || gh.NewCount != wh.NewCount {
						t.Errorf("file %d hunk %d: got %+v, want %+v", i, j, summarize(g).Hunks[j], wh)
					}
				}
			}
		})
	}
}

func summarize(f File) File {
	out := f
	out.Hunks = nil
	for _, h := range f.Hunks {
		out.Hunks = append(out.Hunks, Hunk{OldStart: h.OldStart, OldCount: h.OldCount, NewStart: h.NewStart, NewCount: h.NewCount})
	}
	return out
}

func TestParseToleratesWhitespaceStrippedBlankContext(t *testing.T) {
	// Editors and mail clients commonly strip the single space of an empty context line.
	diff := "--- a/a.go\n+++ b/a.go\n@@ -1,4 +1,4 @@\n one\n\n-three\n+THREE\n four\n"
	files, err := Parse(diff)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := files[0].Hunks[0].Lines
	want := []Line{{' ', "one"}, {' ', ""}, {'-', "three"}, {'+', "THREE"}, {' ', "four"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
	edits := files[0].Edits()
	if len(edits) != 1 || edits[0].Start != 3 || edits[0].End != 3 || edits[0].Insertion {
		t.Fatalf("edits = %+v, want one removal at old line 3", edits)
	}
}

func TestParseContentLookingLikeHeadersStaysContent(t *testing.T) {
	// "+++ evil" is an added line "++ evil"; "--- gone" a removed line "-- gone".
	diff := lines(
		"diff --git a/a.go b/a.go",
		"--- a/a.go",
		"+++ b/a.go",
		"@@ -1,2 +1,2 @@",
		"--- gone",
		"+++ evil",
		" keep",
		"diff --git a/b.go b/b.go",
		"--- a/b.go",
		"+++ b/b.go",
		"@@ -9 +9 @@",
		"-x",
		"+y",
	)
	files, err := Parse(diff)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(files) != 2 || files[0].Path() != "a.go" || files[1].Path() != "b.go" {
		t.Fatalf("files = %+v, want a.go then b.go", files)
	}
	first := files[0].Hunks[0].Lines
	if first[0] != (Line{'-', "-- gone"}) || first[1] != (Line{'+', "++ evil"}) {
		t.Fatalf("header-looking content was misread: %#v", first)
	}
}

func TestParseRejectsMalformedDiffs(t *testing.T) {
	tests := []struct {
		name    string
		diff    string
		wantMsg string
	}{
		{"garbage text", "hello world\nthis is prose\n", "no file headers"},
		{"hunk before any file header", "@@ -1 +1 @@\n-a\n+b\n", "before any file header"},
		{"invalid hunk header", lines("--- a/a", "+++ b/a", "@@ -x +y @@"), "invalid diff hunk header"},
		{"body shorter than declared", lines("--- a/a", "+++ b/a", "@@ -1,5 +1,5 @@", " only"), "incomplete diff hunk"},
		{"truncated before next file", lines("--- a/a", "+++ b/a", "@@ -1,3 +1,3 @@", " a", "diff --git a/b b/b"), "incomplete diff hunk"},
		{"too many removed lines", lines("--- a/a", "+++ b/a", "@@ -1 +1 @@", "-a", "-b", "+c"), "exceeds declared size"},
		{"too many added lines", lines("--- a/a", "+++ b/a", "@@ -1 +1 @@", "-a", "+b", "+c"), "exceeds declared size"},
		{"stray line after a complete hunk", lines("--- a/a", "+++ b/a", "@@ -1 +1 @@", "-a", "+b", " extra"), "exceeds declared size"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.diff)
			if err == nil {
				t.Fatal("Parse succeeded, want error")
			}
			if !IsError(err) {
				t.Fatalf("error %T is not a *diffparse.Error", err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("error %q does not contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestParseLargeDiffExceedingDefaultScannerBuffer(t *testing.T) {
	long := strings.Repeat("x", 200_000)
	files, err := Parse(lines("--- a/a", "+++ b/a", "@@ -1 +1 @@", "-"+long, "+"+long+"y"))
	if err != nil || len(files) != 1 || len(files[0].Hunks[0].Lines[0].Text) != len(long) {
		t.Fatalf("long line not preserved: %v", err)
	}
}

func TestEditsArePreimageCoordinates(t *testing.T) {
	tests := []struct {
		name string
		diff string
		want []Edit
	}{
		{
			name: "replacement is a removal of the old lines; context is not an edit",
			diff: lines("--- a/f", "+++ b/f", "@@ -10,5 +10,5 @@", " c1", " c2", "-r1", "-r2", "+n1", "+n2", " c3"),
			want: []Edit{{Path: "f", Start: 12, End: 13}},
		},
		{
			name: "pure insertion falls between two old lines",
			diff: lines("--- a/f", "+++ b/f", "@@ -10,2 +10,3 @@", " c1", "+new", " c2"),
			want: []Edit{{Path: "f", Start: 10, End: 11, Insertion: true}},
		},
		{
			name: "insertion into an empty old range",
			diff: lines("--- a/f", "+++ b/f", "@@ -7,0 +8,2 @@", "+a", "+b"),
			want: []Edit{{Path: "f", Start: 7, End: 8, Insertion: true}},
		},
		{
			name: "pure deletion",
			diff: lines("--- a/f", "+++ b/f", "@@ -3,4 +3,2 @@", " k", "-d1", "-d2", " k"),
			want: []Edit{{Path: "f", Start: 4, End: 5}},
		},
		{
			name: "two separate runs in one hunk",
			diff: lines("--- a/f", "+++ b/f", "@@ -1,4 +1,4 @@", "-a", "+A", " b", " c", "-d", "+D"),
			want: []Edit{{Path: "f", Start: 1, End: 1}, {Path: "f", Start: 4, End: 4}},
		},
		{
			name: "addition then removal in one block",
			diff: lines("--- a/f", "+++ b/f", "@@ -1,2 +1,2 @@", "+first", "-second", " rest"),
			want: []Edit{{Path: "f", Start: 0, End: 1, Insertion: true}, {Path: "f", Start: 1, End: 1}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, err := Parse(tt.diff)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := files[0].Edits(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Edits = %+v, want %+v", got, tt.want)
			}
		})
	}
}
