package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sergiumoraru/tirion/internal/indexer"
	"github.com/sergiumoraru/tirion/internal/runtimeconfig"
)

func RunParse(dbURL, repoPath string) error {
	return RunParseForWorkspace(dbURL, repoPath, "")
}

func RunParseForWorkspace(dbURL, repoPath, workspaceSlug string) error {
	parseBinary, err := indexer.ResolveParseBinary(indexer.DefaultParseBinary())
	if err != nil {
		return fmt.Errorf("resolve parse binary: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	args := []string{}
	if workspaceSlug != "" {
		args = append(args, "-workspace", workspaceSlug)
	}
	args = append(args, repoPath)
	cmd := exec.CommandContext(ctx, parseBinary, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(runtimeconfig.ChildEnvironment(), "PARSE_SKIP_TESTS=1", "CODEBASE_SKIP_SCHEMA_INIT=1", "DATABASE_URL="+dbURL)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("parse repo: %w", err)
	}
	return nil
}

func RunParseAndResolve(dbURL, repoPath string) error {
	path, err := filepath.Abs(repoPath)
	if err != nil {
		return err
	}
	return indexer.RunSingleRepository(dbURL, "", filepath.Base(path), path, true)
}
