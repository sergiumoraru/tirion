package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCORSAllowsWorkspacePreflight(t *testing.T) {
	called := false
	handler := CORS(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	request := httptest.NewRequest(http.MethodOptions, "/api/search", nil)
	request.Header.Set("Origin", "http://localhost:3000")
	request.Header.Set("Access-Control-Request-Method", http.MethodGet)
	request.Header.Set("Access-Control-Request-Headers", "x-tirion-workspace")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || called {
		t.Fatalf("preflight status=%d, downstream called=%v", response.Code, called)
	}
	allowed := strings.Split(strings.ToLower(response.Header().Get("Access-Control-Allow-Headers")), ",")
	for _, header := range allowed {
		if strings.TrimSpace(header) == "x-tirion-workspace" {
			return
		}
	}
	t.Fatal("workspace header missing from allowed CORS headers")
}
