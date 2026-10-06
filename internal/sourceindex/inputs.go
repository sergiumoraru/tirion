package sourceindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/sergiumoraru/tirion/internal/sourcepath"
)

// ReadCurrent confines native parser inputs to regular files in the repository.
func ReadCurrent(rootPath, path string) ([]byte, error) {
	if !filepath.IsLocal(path) {
		return nil, fmt.Errorf("source path is outside repository: %s", path)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("source is not a regular file: %s", path)
	}
	return io.ReadAll(f)
}

// CaptureInputs records metadata that affects native source selection and entity/
// queue mappings. Hashes bind enrichment to the parse run even in non-Git repos.
func CaptureInputs(root string) (map[string]string, error) {
	inputs := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && sourcepath.SkipDir(filepath.Dir(path), entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		name := strings.ToLower(entry.Name())
		switch filepath.Ext(name) {
		case ".properties", ".csproj", ".edmx", ".tt":
		default:
			if name != "package.json" && name != "lerna.json" && name != "pnpm-workspace.yaml" {
				return nil
			}
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		excluded, err := ExcludedLink(root, rel)
		if err != nil {
			return err
		}
		if excluded {
			return nil
		}
		data, err := ReadCurrent(root, rel)
		if err != nil {
			return fmt.Errorf("read metadata %s: %w", rel, err)
		}
		sum := sha256.Sum256(data)
		inputs[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	return inputs, err
}

func CheckInputs(root string, original map[string]string) error {
	current, err := CaptureInputs(root)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, original) {
		return fmt.Errorf("repository metadata changed during parsing; retry with stable source inputs")
	}
	return nil
}

func ReadInput(ctx context.Context, root, path, revision string, inputs map[string]string) ([]byte, error) {
	hash, ok := inputs[filepath.ToSlash(path)]
	if !ok {
		return nil, fmt.Errorf("metadata input is absent from indexed manifest: %s", path)
	}
	return Read(ctx, root, path, hash, revision)
}

// CheckIncludes verifies explicitly referenced copybooks, including sibling paths.
// Empty hashes record a missing path probed by include resolution.
func CheckIncludes(inputs map[string]string) error {
	for path, expected := range inputs {
		data, err := ReadCurrent(filepath.Dir(path), filepath.Base(path))
		if expected == "" && os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read include %s: %w", path, err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != expected {
			return fmt.Errorf("include changed during indexing: %s", path)
		}
	}
	return nil
}

// ExcludedLink identifies links that cannot represent repository-owned source.
// Ordinary read failures still fail publication; an intentionally excluded link
// is treated consistently by parsing, metadata capture and incremental manifests.
func ExcludedLink(rootPath, path string) (bool, error) {
	if !filepath.IsLocal(path) {
		return true, nil
	}
	full := filepath.Join(rootPath, path)
	info, err := os.Lstat(full)
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return false, nil
	}
	target, err := filepath.EvalSymlinks(full)
	if err != nil {
		return true, nil
	}
	root, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		return false, err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || !filepath.IsLocal(relative) {
		return true, nil
	}
	// ReadCurrent resolves links through os.Root, which refuses absolute link
	// targets even when they point back inside the repository. Decide here, from
	// the same resolver, so an unreadable link is excluded by every caller
	// (parse walk, metadata capture, ignored inputs) instead of aborting a read.
	scoped, err := os.OpenRoot(rootPath)
	if err != nil {
		return false, err
	}
	defer scoped.Close()
	targetInfo, err := scoped.Stat(path)
	if err != nil {
		return true, nil
	}
	return !targetInfo.Mode().IsRegular(), nil
}
