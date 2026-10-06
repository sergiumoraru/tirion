package runtimeconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func envKeys(entries []string) map[string]string {
	out := map[string]string{}
	for _, entry := range entries {
		key, value, _ := strings.Cut(entry, "=")
		out[key] = value
	}
	return out
}

func TestFilterChildEnvironmentUnix(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin", "HOME=/home/u", "USER=u", "LOGNAME=u", "LANG=C",
		"HTTPS_PROXY=http://proxy:3128", "HTTP_PROXY=http://proxy:3128", "NO_PROXY=localhost", "ALL_PROXY=socks5://p:1080",
		"https_proxy=http://proxy:3128", "http_proxy=http://proxy:3128", "no_proxy=localhost", "all_proxy=socks5://p:1080",
		"SSL_CERT_FILE=/etc/ca.pem", "SSL_CERT_DIR=/etc/certs",
		"PGSSLROOTCERT=/etc/pg-ca.pem", "PGHOST=db", "DATABASE_URL=postgres://u:p@h/db",
		// Never forwarded.
		"TIRION_API_TOKEN=secret", "TIRION_API_TOKEN_FILE=/x", "GH_TOKEN=gh", "GITHUB_TOKEN=gh", "TIRION_GIT_TOKEN=gh",
		"GIT_SSH_COMMAND=evil", "GIT_CONFIG_GLOBAL=/evil", "GIT_ASKPASS=/evil", "GIT_SSL_NO_VERIFY=1",
		"LD_PRELOAD=/evil.so", "LD_LIBRARY_PATH=/evil", "DYLD_INSERT_LIBRARIES=/evil", "DYLD_LIBRARY_PATH=/evil",
		// Windows-only names are not special on Unix.
		"APPDATA=/a", "COMSPEC=/c", "USERNAME=u",
		"path=/lower", "malformed",
	}
	got := envKeys(filterChildEnvironment(environ, "linux"))

	for _, key := range []string{"PATH", "HOME", "USER", "LOGNAME", "LANG",
		"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "ALL_PROXY", "https_proxy", "http_proxy", "no_proxy", "all_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "PGSSLROOTCERT", "PGHOST"} {
		if _, ok := got[key]; !ok {
			t.Errorf("%s should be passed through", key)
		}
	}
	if got["HTTPS_PROXY"] != "http://proxy:3128" || got["SSL_CERT_FILE"] != "/etc/ca.pem" {
		t.Errorf("values must pass through unchanged: %v", got)
	}
	for _, key := range []string{"DATABASE_URL", "TIRION_API_TOKEN", "TIRION_API_TOKEN_FILE", "GH_TOKEN", "GITHUB_TOKEN", "TIRION_GIT_TOKEN",
		"GIT_SSH_COMMAND", "GIT_CONFIG_GLOBAL", "GIT_ASKPASS", "GIT_SSL_NO_VERIFY",
		"LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH",
		"APPDATA", "COMSPEC", "USERNAME", "path", "malformed"} {
		if _, ok := got[key]; ok {
			t.Errorf("%s must not be passed through on unix", key)
		}
	}
}

func TestGitEnvironmentKeepsProxyAndDropsOverrides(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	t.Setenv("SSL_CERT_FILE", "/etc/corp-ca.pem")
	t.Setenv("PGPASSWORD", "dbsecret")
	t.Setenv("GIT_SSH_COMMAND", "evil")
	t.Setenv("GIT_CONFIG_GLOBAL", "/evil")
	t.Setenv("LD_PRELOAD", "/evil.so")
	t.Setenv("TIRION_API_TOKEN", strings.Repeat("a", 32))
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	got := envKeys(GitEnvironment())
	if got["HTTPS_PROXY"] != "http://proxy.example:3128" || got["SSL_CERT_FILE"] != "/etc/corp-ca.pem" {
		t.Fatalf("proxy/TLS trust variables missing: %v", got)
	}
	for _, key := range []string{"PGPASSWORD", "GIT_SSH_COMMAND", "GIT_CONFIG_GLOBAL", "LD_PRELOAD", "TIRION_API_TOKEN"} {
		if _, ok := got[key]; ok {
			t.Errorf("%s must not reach git", key)
		}
	}
	prompts := 0
	for _, entry := range GitEnvironment() {
		if strings.HasPrefix(entry, "GIT_TERMINAL_PROMPT=") {
			prompts++
			if entry != "GIT_TERMINAL_PROMPT=0" {
				t.Errorf("unexpected %s", entry)
			}
		}
	}
	if prompts != 1 {
		t.Errorf("GIT_TERMINAL_PROMPT entries = %d, want exactly one", prompts)
	}
}

func unsetEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, key := range keys {
		old, had := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if had {
				os.Setenv(key, old)
			} else {
				os.Unsetenv(key)
			}
		})
	}
}

func writeToken(t *testing.T, path, token string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestClientTokenNeverDiscoversForRemoteOrigins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TIRION_HOME", home)
	unsetEnv(t, "TIRION_API_TOKEN", "TIRION_API_TOKEN_FILE")
	writeToken(t, filepath.Join(home, "api-token"), strings.Repeat("cd", 16))

	for _, rawURL := range []string{
		"https://tirion.example.com", "https://tirion.example.com:8443", "https://localhost.example.com:8080", "https://10.0.0.5:8080",
	} {
		if token, err := ClientToken(rawURL); err == nil || token != "" {
			t.Errorf("ClientToken(%q) = %q, %v; want explicit-credential error", rawURL, token, err)
		}
	}
	for _, rawURL := range []string{"http://tirion.example.com", "http://10.0.0.5:8080", "ftp://localhost:8080", "http://user:pw@localhost:8080", "http://localhost:8080#x", "localhost:8080", ""} {
		if token, err := ClientToken(rawURL); err == nil || token != "" {
			t.Errorf("ClientToken(%q) = %q, %v; want rejection", rawURL, token, err)
		}
	}
}

func TestClientTokenRejectsGroupReadableTokenFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	home := t.TempDir()
	t.Setenv("TIRION_HOME", home)
	unsetEnv(t, "TIRION_API_TOKEN", "TIRION_API_TOKEN_FILE")
	path := filepath.Join(home, "api-token")
	writeToken(t, path, strings.Repeat("ab", 16))
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ClientToken("http://localhost:8080"); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("expected private-file error, got %v", err)
	}
}

func TestAPITokenCreatesPrivateFileOnlyWhenAsked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TIRION_HOME", home)
	unsetEnv(t, "TIRION_API_TOKEN", "TIRION_API_TOKEN_FILE")
	path := filepath.Join(home, "state", "api-token")
	t.Setenv("TIRION_API_TOKEN_FILE", path)

	if _, err := APIToken(false); err == nil {
		t.Fatal("client lookup must not invent a token")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("APIToken(false) created %s", path)
	}
	created, err := APIToken(true)
	if err != nil || len(created) != 64 {
		t.Fatalf("APIToken(true) = %q, %v", created, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("token file mode = %v, %v", info, err)
		}
	}
	again, err := APIToken(true)
	if err != nil || again != created {
		t.Fatalf("token must be stable across starts: %q vs %q (%v)", again, created, err)
	}
}
