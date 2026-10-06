package middleware

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const MaxRequestBytes int64 = 8 << 20

// Default concurrency slots. Graph reads and administrative operations (index,
// fetch, checkout, parse) are admitted separately so a full index cannot starve
// reads. Override with TIRION_MAX_CONCURRENT_READS / TIRION_MAX_CONCURRENT_ADMIN.
const (
	DefaultMaxConcurrentReads = 16
	DefaultMaxConcurrentAdmin = 2
	maxConfigurableReads      = 1024
	maxConfigurableAdmin      = 64
)

const (
	defaultReadBudget  = 60 * time.Second
	defaultAdminBudget = 2 * time.Hour
)

// Limits configures ResourceLimits. Zero values select the defaults.
type Limits struct {
	Reads int
	Admin int

	// Unexported so tests can shorten the request budgets.
	readBudget  time.Duration
	adminBudget time.Duration
}

// LimitsFromEnv reads TIRION_MAX_CONCURRENT_READS (1-1024, default 16) and
// TIRION_MAX_CONCURRENT_ADMIN (1-64, default 2). Unset or blank selects the
// default; anything else that is not an in-range integer is an error.
func LimitsFromEnv() (Limits, error) {
	reads, err := envSlots("TIRION_MAX_CONCURRENT_READS", DefaultMaxConcurrentReads, maxConfigurableReads)
	if err != nil {
		return Limits{}, err
	}
	admin, err := envSlots("TIRION_MAX_CONCURRENT_ADMIN", DefaultMaxConcurrentAdmin, maxConfigurableAdmin)
	if err != nil {
		return Limits{}, err
	}
	return Limits{Reads: reads, Admin: admin}, nil
}

func envSlots(name string, fallback, max int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > max {
		return 0, fmt.Errorf("%s must be an integer between 1 and %d", name, max)
	}
	return value, nil
}

// WriteError writes the API's JSON error shape,
// {"error":{"code":...,"message":...}}, for failures raised before a request
// reaches a handler.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": message}})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// Authenticate protects every API route, including health; possession of the
// service token grants access to graph data and repository administration.
func Authenticate(next http.Handler, token string) http.Handler {
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		supplied := r.Header.Get("X-Tirion-Token")
		if authorization := r.Header.Get("Authorization"); supplied == "" && len(authorization) > 7 && strings.EqualFold(authorization[:7], "Bearer ") {
			supplied = strings.TrimSpace(authorization[7:])
		}
		actual := sha256.Sum256([]byte(supplied))
		if supplied == "" || subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="tirion"`)
			WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "A valid Tirion API token is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// TrustedHosts is outermost, before CORS and authentication. Never derive the
// allowlist from an incoming Host or X-Forwarded-* header.
func TrustedHosts(next http.Handler, hosts []string) (http.Handler, error) {
	allowed := map[string]bool{}
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" {
			continue
		}
		if host == "::1" {
			host = "[::1]"
		}
		u, err := url.Parse("http://" + host)
		if err != nil || u.Host != host || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(host, "* /\\") || strings.HasSuffix(host, ":") || u.Hostname() == "" {
			return nil, fmt.Errorf("TIRION_ALLOWED_HOSTS must contain exact hostnames or IP addresses, optionally with ports")
		}
		allowed[host] = true
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("TIRION_ALLOWED_HOSTS cannot be empty")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		hostname := host
		if name, _, err := net.SplitHostPort(host); err == nil {
			hostname = name
			if strings.Contains(name, ":") {
				hostname = "[" + name + "]"
			}
		}
		if !allowed[host] && !allowed[hostname] {
			WriteError(w, http.StatusForbidden, "UNTRUSTED_HOST", "Request Host is not in TIRION_ALLOWED_HOSTS")
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	}), nil
}

// ResourceLimits bounds request memory, admission, and lifetime. Administrative
// operations get a separate budget; a full index cannot starve graph reads.
func ResourceLimits(next http.Handler, limits Limits) http.Handler {
	if limits.Reads < 1 {
		limits.Reads = DefaultMaxConcurrentReads
	}
	if limits.Admin < 1 {
		limits.Admin = DefaultMaxConcurrentAdmin
	}
	if limits.readBudget <= 0 {
		limits.readBudget = defaultReadBudget
	}
	if limits.adminBudget <= 0 {
		limits.adminBudget = defaultAdminBudget
	}
	reads := make(chan struct{}, limits.Reads)
	writes := make(chan struct{}, limits.Admin)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		budget := limits.readBudget
		slots := reads
		administrative := r.Method == http.MethodPost && (strings.HasPrefix(r.URL.Path, "/api/workspaces") || strings.HasPrefix(r.URL.Path, "/api/admin/"))
		if administrative {
			budget = limits.adminBudget
			slots = writes
		}
		// No read deadline here: http.Server.ReadTimeout already bounds the body,
		// and re-arming one after net/http starts its background read cancels the
		// request context of bodyless requests when it expires.
		controller := http.NewResponseController(w)
		_ = controller.SetWriteDeadline(time.Now().Add(budget + 5*time.Second))
		if r.ContentLength > MaxRequestBytes {
			WriteError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Request body exceeds 8 MiB")
			return
		}
		select {
		case slots <- struct{}{}:
		default:
			w.Header().Set("Retry-After", "1")
			WriteError(w, http.StatusServiceUnavailable, "BUSY", "Server is busy; retry later")
			return
		}
		// Release inside the timed handler, so work that ignores cancellation cannot
		// accumulate an unbounded number of background goroutines.
		limited := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() { <-slots }()
			r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
			body, err := io.ReadAll(r.Body)
			if err != nil {
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					WriteError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Request body exceeds 8 MiB")
					return
				}
				WriteError(w, http.StatusBadRequest, "INVALID_BODY", "Request body could not be read")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r)
		})
		http.TimeoutHandler(limited, budget, `{"error":{"code":"TIMEOUT","message":"Request time limit exceeded"}}`).ServeHTTP(timeoutBodyWriter{w}, r)
	})
}

// timeoutBodyWriter labels the JSON body http.TimeoutHandler writes on expiry.
// TimeoutHandler sets no Content-Type of its own; a handler that completes in
// time always sets its own headers, which are copied over before this runs.
type timeoutBodyWriter struct{ http.ResponseWriter }

func (w timeoutBodyWriter) WriteHeader(status int) {
	if status == http.StatusServiceUnavailable && w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w timeoutBodyWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
