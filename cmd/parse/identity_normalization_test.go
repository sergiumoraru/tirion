package main

import (
	"testing"

	"github.com/sergiumoraru/tirion/internal/parser"
)

func TestNormalizeFilePathIdentity(t *testing.T) {
	t.Parallel()

	got := normalizeFilePathIdentity("./src\\main//java/com/acme/Foo.java")
	want := "src/main/java/com/acme/Foo.java"
	if got != want {
		t.Fatalf("normalizeFilePathIdentity: got %q want %q", got, want)
	}
}

func TestNormalizeFilePathCanonical_SnapshotPrefix(t *testing.T) {
	t.Parallel()

	got := normalizeFilePathCanonical(".codebase-snapshots/run-123/src/main/java/com/acme/Foo.java")
	want := "src/main/java/com/acme/foo.java"
	if got != want {
		t.Fatalf("normalizeFilePathCanonical: got %q want %q", got, want)
	}
}

func TestNormalizeEndpointIdentity(t *testing.T) {
	t.Parallel()

	pathGot := normalizeEndpointPathIdentity("Pages//Save/")
	pathWant := "/pages/save"
	if pathGot != pathWant {
		t.Fatalf("normalizeEndpointPathIdentity: got %q want %q", pathGot, pathWant)
	}

	methodGot := normalizeEndpointMethodIdentity(" post ")
	methodWant := "POST"
	if methodGot != methodWant {
		t.Fatalf("normalizeEndpointMethodIdentity: got %q want %q", methodGot, methodWant)
	}
}

func TestNormalizeFunctionSignatureIdentity(t *testing.T) {
	t.Parallel()

	got := normalizeFunctionSignatureIdentity(
		" PagesController.save ",
		[]string{"String pageId", "id: number", "...flags?: string[]"},
		" void ",
	)
	want := "PagesController.save(String,number,string[])->void"
	if got != want {
		t.Fatalf("normalizeFunctionSignatureIdentity: got %q want %q", got, want)
	}
}

func TestDedupeFunctionInsertInputs_CollapsesDuplicateIdentity(t *testing.T) {
	t.Parallel()

	funcs := []parser.ParsedFunction{
		{
			Name:       " PagesController.save ",
			StartLine:  21,
			EndLine:    33,
			Params:     []string{"String pageId"},
			ReturnType: "void",
			SourceCode: "short",
		},
		{
			Name:       "PagesController.save",
			StartLine:  21,
			EndLine:    33,
			IsAsync:    true,
			SourceCode: "longer function source for same identity",
		},
		{
			Name:      "PagesController.update",
			StartLine: 40,
			EndLine:   60,
		},
	}

	records, ordAlias := dedupeFunctionInsertInputs(funcs)
	if len(records) != 2 {
		t.Fatalf("expected 2 deduped records, got %d", len(records))
	}
	if len(ordAlias) != len(funcs) {
		t.Fatalf("expected ord alias len %d, got %d", len(funcs), len(ordAlias))
	}
	if ordAlias[0] != 0 || ordAlias[1] != 0 || ordAlias[2] != 2 {
		t.Fatalf("unexpected ord aliases: %#v", ordAlias)
	}

	first := records[0]
	if first.Ord != 0 {
		t.Fatalf("expected first deduped ord=0, got %d", first.Ord)
	}
	if first.NameCanonical != "PagesController.save" {
		t.Fatalf("unexpected name canonical: %q", first.NameCanonical)
	}
	if first.SignatureCanonical != "PagesController.save(String)->void" {
		t.Fatalf("unexpected signature canonical: %q", first.SignatureCanonical)
	}
	if !first.Function.IsAsync {
		t.Fatalf("expected merged duplicate to preserve async=true")
	}
	if got := len(first.Function.Params); got != 1 || first.Function.Params[0] != "String pageId" {
		t.Fatalf("expected merged params from richer duplicate, got %#v", first.Function.Params)
	}
	if first.Function.SourceCode != "longer function source for same identity" {
		t.Fatalf("expected longest source code to win, got %q", first.Function.SourceCode)
	}
}
