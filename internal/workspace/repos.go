package workspace

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sergiumoraru/tirion/internal/runtimeconfig"
)

type RepoStatus struct {
	CurrentBranch string
	HeadSHA       string
	Dirty         bool
	AheadCount    int
	BehindCount   int
}

func InspectRepo(path string) (RepoStatus, error) {
	repoPath, err := filepath.Abs(path)
	if err != nil {
		return RepoStatus{}, err
	}

	statusOut, err := gitOutput(repoPath, "status", "--porcelain=2", "--branch")
	if err != nil {
		return RepoStatus{}, err
	}

	var out RepoStatus
	lines := strings.Split(statusOut, "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "# ") {
			out.Dirty = true
			continue
		}
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			out.CurrentBranch = strings.TrimPrefix(line, "# branch.head ")
			if out.CurrentBranch == "(detached)" {
				out.CurrentBranch = "detached"
			}
		case strings.HasPrefix(line, "# branch.oid "):
			out.HeadSHA = strings.TrimPrefix(line, "# branch.oid ")
		case strings.HasPrefix(line, "# branch.ab "):
			var ahead, behind int
			if _, err := fmt.Sscanf(strings.TrimPrefix(line, "# branch.ab "), "+%d -%d", &ahead, &behind); err == nil {
				out.AheadCount = ahead
				out.BehindCount = behind
			}
		}
	}

	return out, nil
}

func Fetch(path string) error {
	_, err := gitOutput(path, "fetch", "--all", "--prune")
	return err
}

func RemoteDefaultBranch(path string) (string, error) {
	out, err := gitOutput(path, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err == nil {
		ref := strings.TrimSpace(out)
		ref = strings.TrimPrefix(ref, "origin/")
		if ref != "" {
			return ref, nil
		}
	}
	for _, candidate := range []string{"master", "main"} {
		if _, err := resolveRefCommit(path, "origin/"+candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("remote default branch not found")
}

func FetchRef(path, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fmt.Errorf("ref is required")
	}
	_, err := gitOutput(path, "fetch", "--prune", "origin", "+refs/heads/"+ref+":refs/remotes/origin/"+ref)
	return err
}

func ResolveRefCommit(path, ref string) (string, error) {
	repoPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return resolveRefCommit(repoPath, ref)
}

func Checkout(path, branch string) error {
	trimmed := strings.TrimSpace(branch)
	if trimmed == "" || strings.HasPrefix(trimmed, "-") {
		return fmt.Errorf("branch must be a name, not a Git option")
	}
	if _, err := gitOutput(path, "checkout", trimmed); err == nil {
		return nil
	} else if isBranchAlreadyUsedByWorktree(err) {
		commit, resolveErr := resolveRefCommit(path, trimmed)
		if resolveErr != nil {
			return err
		}
		_, detachErr := gitOutput(path, "checkout", "--detach", commit)
		return detachErr
	}
	_, err := gitOutput(path, "checkout", "-B", trimmed, "--track", "origin/"+trimmed)
	return err
}

func CheckoutRefDetached(path, ref string) error {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return fmt.Errorf("ref is required")
	}
	commit, err := resolveRefCommit(path, trimmed)
	if err != nil {
		return err
	}
	_, err = gitOutput(path, "checkout", "--detach", commit)
	return err
}

func isBranchAlreadyUsedByWorktree(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "already used by worktree") || strings.Contains(msg, "is already checked out")
}

func EnsureWorktree(sourceRepoPath, targetPath, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fmt.Errorf("ref is required")
	}
	targetAbs, err := filepath.Abs(targetPath)
	if err != nil {
		return err
	}
	if _, err := gitOutput(sourceRepoPath, "fetch", "--all", "--prune"); err != nil {
		return err
	}
	sourceAbs, err := filepath.Abs(sourceRepoPath)
	if err != nil {
		return err
	}
	commit, err := resolveRefCommit(sourceAbs, ref)
	if err != nil {
		return err
	}
	if _, err := gitOutput(targetAbs, "rev-parse", "--git-dir"); err == nil {
		status, err := InspectRepo(targetAbs)
		if err != nil {
			return err
		}
		if status.Dirty {
			return fmt.Errorf("target worktree has local changes: %s", targetAbs)
		}
		if _, err := gitOutput(targetAbs, "fetch", "--all", "--prune"); err != nil {
			return err
		}
		_, err = gitOutput(targetAbs, "checkout", "--detach", commit)
		return err
	}
	if err := ensureParentDir(targetAbs); err != nil {
		return err
	}
	return addDetachedWorktree(sourceAbs, targetAbs, commit)
}

func EnsureWorktreeAtCommit(sourceRepoPath, targetPath, commit string) error {
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return fmt.Errorf("commit is required")
	}
	targetAbs, err := filepath.Abs(targetPath)
	if err != nil {
		return err
	}
	sourceAbs, err := filepath.Abs(sourceRepoPath)
	if err != nil {
		return err
	}
	if _, err := gitOutput(sourceAbs, "cat-file", "-e", commit+"^{commit}"); err != nil {
		return err
	}
	if _, err := gitOutput(targetAbs, "rev-parse", "--git-dir"); err == nil {
		status, err := InspectRepo(targetAbs)
		if err != nil {
			return err
		}
		if status.Dirty {
			return fmt.Errorf("target worktree has local changes: %s", targetAbs)
		}
		_, err = gitOutput(targetAbs, "checkout", "--detach", commit)
		return err
	}
	if err := ensureParentDir(targetAbs); err != nil {
		return err
	}
	return addDetachedWorktree(sourceAbs, targetAbs, commit)
}

func addDetachedWorktree(sourceAbs, targetAbs, commit string) error {
	if targetAbs == "" || targetAbs == string(filepath.Separator) || targetAbs == sourceAbs {
		return fmt.Errorf("invalid worktree target %q", targetAbs)
	}
	// Failed Git commands do not establish ownership of an existing directory.
	// Leave both existing content and partial worktrees intact for recovery.
	entries, err := os.ReadDir(targetAbs)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("worktree target is not empty: %s", targetAbs)
	}
	_, err = gitOutput(sourceAbs, "worktree", "add", "--detach", targetAbs, commit)
	return err
}

func resolveRefCommit(repoPath, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	for _, candidate := range []string{ref, "origin/" + ref} {
		out, err := gitOutput(repoPath, "rev-parse", "--verify", "--end-of-options", candidate+"^{commit}")
		if err == nil {
			commit := strings.TrimSpace(out)
			if commit != "" {
				return commit, nil
			}
		}
	}
	return "", fmt.Errorf("ref %q does not resolve to a commit", ref)
}

func githubToken() string {
	if token := strings.TrimSpace(os.Getenv("GH_TOKEN")); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
}

func redactToken(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), token, "[redacted]"))
}

func ensureParentDir(path string) error {
	parent := filepath.Dir(path)
	return ensureDir(parent)
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

func gitOutput(path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	repoPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	gitArgs := append([]string{"-c", "safe.directory=" + repoPath, "-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + os.DevNull, "-C", repoPath}, args...)
	token := ""
	if len(args) > 0 && args[0] == "fetch" {
		token = githubToken()
		if token != "" {
			// Scope auth to HTTPS GitHub without storing the token in argv or remotes.
			gitArgs = append([]string{
				"-c", "credential.https://github.com.helper=",
				"-c", "credential.https://github.com.helper=" + githubCredentialHelper,
			}, gitArgs...)
		}
	}
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	cmd.Env = runtimeconfig.GitEnvironment()
	cmd.WaitDelay = 2 * time.Second
	if token != "" {
		cmd.Env = append(cmd.Env, "TIRION_GIT_TOKEN="+token, "GIT_TERMINAL_PROMPT=0")
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", redactToken(fmt.Errorf("git %s: %s", strings.Join(args, " "), msg), token)
	}
	return stdout.String(), nil
}

const githubCredentialHelper = `!f() {
  test "$1" = get || exit 0
  protocol= host=
  while IFS="=" read -r key value; do
    case "$key" in
      protocol) protocol="$value" ;;
      host) host="$value" ;;
    esac
  done
  if test "$protocol" = https && test "$host" = github.com; then
    printf "%s\n" "username=x-access-token" "password=$TIRION_GIT_TOKEN"
  fi
}; f`
