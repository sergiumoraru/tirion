package sourceindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sergiumoraru/tirion/internal/runtimeconfig"
)

// Resolve full-file constants against the indexed bytes, not an arbitrary checkout.
func Read(ctx context.Context, repoPath, filePath, fileHash, commit string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if repoPath == "" || !filepath.IsLocal(filePath) || fileHash == "" {
		return nil, fmt.Errorf("indexed source identity is incomplete")
	}
	matches := func(data []byte) bool {
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:]) == fileHash
	}
	if root, err := os.OpenRoot(repoPath); err == nil {
		file, openErr := root.Open(filePath)
		if openErr == nil {
			if info, statErr := file.Stat(); statErr == nil && info.Mode().IsRegular() {
				data, readErr := io.ReadAll(file)
				file.Close()
				root.Close()
				if readErr == nil && matches(data) {
					return data, nil
				}
			} else {
				file.Close()
				root.Close()
			}
		} else {
			root.Close()
		}
	}
	if len(commit) != 40 && len(commit) != 64 {
		return nil, fmt.Errorf("indexed source revision is unavailable")
	}
	if _, err := hex.DecodeString(commit); err != nil {
		return nil, fmt.Errorf("invalid indexed source revision")
	}
	revisionPath := commit + ":" + strings.TrimPrefix(filepath.ToSlash(filepath.Clean(filePath)), "./")
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "show", "--no-ext-diff", "--no-textconv", revisionPath, "--")
	cmd.Env = runtimeconfig.GitEnvironment()
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read indexed source revision: %w", err)
	}
	if !matches(data) {
		return nil, fmt.Errorf("source content differs from the indexed file hash")
	}
	return data, nil
}
