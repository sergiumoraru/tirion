package handlers

import (
	"context"
	"github.com/sergiumoraru/tirion/internal/sourceindex"
)

func readIntegrationSource(ctx context.Context, repoPath, filePath, fileHash, commit string) ([]byte, error) {
	return sourceindex.Read(ctx, repoPath, filePath, fileHash, commit)
}
