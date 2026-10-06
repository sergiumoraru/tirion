package main

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

func TestDatabaseFlagHidesFallbackFromHelp(t *testing.T) {
	const fallback = "postgres://fixture:private-password@localhost/database"
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	value := databaseFlag(fs, fallback)
	var help bytes.Buffer
	fs.SetOutput(&help)
	fs.PrintDefaults()
	if strings.Contains(help.String(), "private-password") || strings.Contains(help.String(), fallback) {
		t.Fatal("help exposed the connection default")
	}
	if *value != fallback {
		t.Fatal("suppressing help changed the runtime default")
	}
	if err := fs.Parse([]string{"-db", "postgres:///override"}); err != nil {
		t.Fatal(err)
	}
	if *value != "postgres:///override" {
		t.Fatalf("explicit database override not retained: %q", *value)
	}
}
