package runtimeconfig

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var legacyPathWarnings sync.Map

// UserPath uses Tirion's configuration/state directory. Existing installations
// may keep reading an old path until explicitly migrated; new installs use .tirion.
// An explicit TIRION_HOME disables legacy fallback.
func UserPath(name string) (string, error) {
	if !filepath.IsLocal(name) {
		return "", fmt.Errorf("invalid Tirion configuration path")
	}
	if configured := strings.TrimSpace(os.Getenv("TIRION_HOME")); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", fmt.Errorf("TIRION_HOME must be absolute")
		}
		return filepath.Join(configured, name), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	current := filepath.Join(home, ".tirion", name)
	if _, err := os.Lstat(current); err == nil {
		return current, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	legacy := filepath.Join(home, ".codebase-intel", name)
	if _, err := os.Lstat(legacy); err == nil {
		if _, warned := legacyPathWarnings.LoadOrStore(name, true); !warned {
			log.Printf("Using legacy Tirion path %s; migrate it to %s (see SETUP.md#upgrading-from-earlier-builds)", legacy, current)
		}
		return legacy, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return current, nil
}
