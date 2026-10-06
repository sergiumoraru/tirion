package middleware

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// CORS allows same-origin requests and the documented local development UI.
func CORS(next http.Handler) http.Handler {
	handler, _ := NewCORS(next, []string{"http://localhost:3000", "http://127.0.0.1:3000"})
	return handler
}

// NewCORS requires explicit origins. It does not authenticate API clients.
func NewCORS(next http.Handler, origins []string) (http.Handler, error) {
	allowed := make(map[string]bool)
	for _, origin := range origins {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.Contains(u.Host, "*") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("TIRION_ALLOWED_ORIGINS must contain exact HTTP(S) origins without paths or wildcards")
		}
		allowed[origin] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		origin := r.Header.Get("Origin")
		if origin != "" {
			u, err := url.Parse(origin)
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			sameOrigin := err == nil && u.Scheme == scheme && u.Host == r.Host && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
			if !sameOrigin && !allowed[origin] {
				WriteError(w, http.StatusForbidden, "ORIGIN_NOT_ALLOWED", "Browser origin is not in TIRION_ALLOWED_ORIGINS")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Tirion-Token, X-Tirion-Workspace, X-Tirion-Actor")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	}), nil
}
