// Package runtimeconfig contains process configuration shared by native clients
// and the server. Repository contents are never a source of implicit trust.
package runtimeconfig

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// LoadEnvironment reads only an explicitly selected, absolute configuration file.
// It deliberately does not search the current directory or its parents.
func LoadEnvironment() error {
	path := strings.TrimSpace(os.Getenv("TIRION_ENV_FILE"))
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("TIRION_ENV_FILE must be an absolute path")
	}
	return LoadEnvironmentFile(path)
}

func allowedEnvironmentKey(key string) bool {
	switch key {
	case "DATABASE_URL", "PORT", "GH_TOKEN", "GITHUB_TOKEN", "REPOS_ROOT", "PARSE_SKIP_TESTS", "CODE_INTEL_PARSE_TIMEOUT_MS":
		return true
	}
	// Only application-owned settings; never Git, executable search, dynamic
	// loader, shell, or HOME variables. This file is selected by the operator.
	return strings.HasPrefix(key, "TIRION_") && key != "TIRION_ENV_FILE" || strings.HasPrefix(key, "CODEBASE_")
}

func LoadEnvironmentFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return fmt.Errorf("environment file must be regular and at most 1 MiB")
	}
	values := map[string]string{}
	scanner := bufio.NewScanner(f)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !allowedEnvironmentKey(key) || strings.ContainsAny(key, " \t\x00") {
			return fmt.Errorf("unsupported environment key on line %d", lineNo)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		value = strings.ReplaceAll(value, `\n`, "\n")
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("invalid environment value on line %d", lineNo)
		}
		if _, exists := values[key]; !exists {
			values[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	for key, value := range values {
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func TokenFile() (string, error) {
	if path := strings.TrimSpace(os.Getenv("TIRION_API_TOKEN_FILE")); path != "" {
		if !filepath.IsAbs(path) {
			return "", fmt.Errorf("TIRION_API_TOKEN_FILE must be absolute")
		}
		return path, nil
	}
	return UserPath("api-token")
}

func validateToken(token string) (string, error) {
	token = strings.TrimSpace(token)
	if len(token) < 32 || len(token) > 512 {
		return "", fmt.Errorf("API token must contain 32–512 printable non-whitespace ASCII characters")
	}
	for _, c := range token {
		if c < 33 || c > 126 {
			return "", fmt.Errorf("API token must contain printable non-whitespace ASCII characters")
		}
	}
	return token, nil
}

// APIToken creates a persistent per-user token only for the server. Clients never
// create credentials, so a missing server setup fails instead of inventing a token.
func APIToken(create bool) (string, error) {
	if token, ok := os.LookupEnv("TIRION_API_TOKEN"); ok {
		return validateToken(token)
	}
	path, err := TokenFile()
	if err != nil {
		return "", err
	}
	if create {
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return "", err
		}
		if _, err = os.Lstat(path); os.IsNotExist(err) {
			data := make([]byte, 32)
			if _, err = rand.Read(data); err != nil {
				return "", err
			}
			f, openErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if openErr == nil {
				_, writeErr := io.WriteString(f, hex.EncodeToString(data)+"\n")
				closeErr := f.Close()
				if writeErr != nil {
					return "", writeErr
				}
				if closeErr != nil {
					return "", closeErr
				}
			} else if !os.IsExist(openErr) {
				return "", openErr
			}
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("read API token (start tirion serve or configure TIRION_API_TOKEN): %w", err)
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) || info.Size() > 1024 {
		return "", fmt.Errorf("API token file must be a regular private file (chmod 600): %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return validateToken(string(data))
}

// ClientToken never forwards the automatically discovered local credential to
// an arbitrary endpoint. The per-user token file is discovered only for a
// loopback API URL on any port, since the file belongs to the local `tirion
// serve`; every other origin must use HTTPS with an explicitly configured
// TIRION_API_TOKEN or TIRION_API_TOKEN_FILE.
func ClientToken(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("invalid API URL")
	}
	host := strings.ToLower(u.Hostname())
	local := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return "", fmt.Errorf("API URL must use HTTPS outside loopback")
	}
	if _, ok := os.LookupEnv("TIRION_API_TOKEN"); ok {
		return APIToken(false)
	}
	if os.Getenv("TIRION_API_TOKEN_FILE") != "" {
		return APIToken(false)
	}
	if local {
		return APIToken(false)
	}
	return "", fmt.Errorf("set TIRION_API_TOKEN or TIRION_API_TOKEN_FILE for this API origin")
}

func NoRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

// ChildEnvironment supplies only runtime, network-trust, and parser settings.
// Indexing helpers need the database URL, but never API credentials or GitHub
// tokens. Proxy and TLS trust variables are passed through so repository
// fetches and libpq keep working behind corporate proxies and private
// certificate authorities.
func ChildEnvironment() []string {
	return filterChildEnvironment(os.Environ(), runtime.GOOS)
}

var (
	childEnvironmentKeys        = map[string]bool{}
	windowsChildEnvironmentKeys = map[string]bool{}
)

func init() {
	for _, key := range []string{
		"PATH", "HOME", "USERPROFILE", "SYSTEMROOT", "SystemRoot", "WINDIR", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "TZ",
		"CODE_INTEL_PARSE_TIMEOUT_MS", "PARSE_SKIP_TESTS", "TIRION_ENRICHMENT_CACHE_DIR", "TIRION_HOME",
		"PGPASSFILE", "PGSERVICE", "PGSERVICEFILE", "PGSSLROOTCERT", "PGSSLCERT", "PGSSLKEY", "PGSSLMODE", "PGSSLCRL", "PGCONNECT_TIMEOUT",
		"PGHOST", "PGPORT", "PGUSER", "PGPASSWORD", "PGDATABASE", "PGTARGETSESSIONATTRS",
		// Outbound proxy and TLS trust configuration.
		"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "ALL_PROXY", "https_proxy", "http_proxy", "no_proxy", "all_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR",
	} {
		childEnvironmentKeys[key] = true
		windowsChildEnvironmentKeys[strings.ToUpper(key)] = true
	}
	// Windows tools need the profile locations and shell variables to start.
	for _, key := range []string{"APPDATA", "LOCALAPPDATA", "PROGRAMDATA", "PROGRAMFILES", "PATHEXT", "COMSPEC", "USERNAME", "USERDOMAIN"} {
		windowsChildEnvironmentKeys[key] = true
	}
}

// filterChildEnvironment keeps the allowlisted entries of environ. Windows
// environment names are case-insensitive ("Path"), so they match upper-cased
// there; elsewhere names match exactly. On Unix USER and LOGNAME are passed
// because ssh derives the account name from them.
func filterChildEnvironment(environ []string, goos string) []string {
	var out []string
	for _, entry := range environ {
		key, _, _ := strings.Cut(entry, "=")
		var keep bool
		if goos == "windows" {
			keep = windowsChildEnvironmentKeys[strings.ToUpper(key)]
		} else {
			keep = childEnvironmentKeys[key] || key == "USER" || key == "LOGNAME"
		}
		if keep {
			out = append(out, entry)
		}
	}
	return out
}

// GitEnvironment removes inherited Git overrides and application credentials.
// The service account's own Git/SSH configuration remains available for fetch.
func GitEnvironment() []string {
	var env []string
	for _, entry := range ChildEnvironment() {
		if !strings.HasPrefix(entry, "PG") {
			env = append(env, entry)
		}
	}
	for _, key := range []string{"SSH_AUTH_SOCK", "SSH_AGENT_PID", "XDG_CONFIG_HOME"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return append(env, "GIT_TERMINAL_PROMPT=0")
}
