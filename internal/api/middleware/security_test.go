package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "0123456789abcdef0123456789abcdef"

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeAPIError(t *testing.T, response *httptest.ResponseRecorder) apiError {
	t.Helper()
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json (body %q)", got, response.Body.String())
	}
	var body apiError
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %v: %q", err, response.Body.String())
	}
	if body.Error.Code == "" || body.Error.Message == "" {
		t.Fatalf("error body missing code or message: %q", response.Body.String())
	}
	return body
}

func okHandler(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if called != nil {
			*called = true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
}

func TestAuthenticate(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"missing", nil, http.StatusUnauthorized},
		{"wrong X-Tirion-Token", map[string]string{"X-Tirion-Token": testToken + "x"}, http.StatusUnauthorized},
		{"prefix of token", map[string]string{"X-Tirion-Token": testToken[:31]}, http.StatusUnauthorized},
		{"right X-Tirion-Token", map[string]string{"X-Tirion-Token": testToken}, http.StatusOK},
		{"right Bearer", map[string]string{"Authorization": "Bearer " + testToken}, http.StatusOK},
		{"scheme is case-insensitive", map[string]string{"Authorization": "bearer " + testToken}, http.StatusOK},
		{"wrong Bearer", map[string]string{"Authorization": "Bearer nope"}, http.StatusUnauthorized},
		{"empty Bearer", map[string]string{"Authorization": "Bearer "}, http.StatusUnauthorized},
		{"Basic is not a token", map[string]string{"Authorization": "Basic " + testToken}, http.StatusUnauthorized},
		{"raw token without scheme", map[string]string{"Authorization": testToken}, http.StatusUnauthorized},
		{"X-Tirion-Token wins over Authorization", map[string]string{"X-Tirion-Token": "wrong", "Authorization": "Bearer " + testToken}, http.StatusUnauthorized},
		{"proxy Authorization alongside right token", map[string]string{"X-Tirion-Token": testToken, "Authorization": "Basic Zm9vOmJhcg=="}, http.StatusOK},
		{"query string token is ignored", nil, http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			handler := Authenticate(okHandler(&called), testToken)
			target := "/api/health"
			if strings.Contains(test.name, "query") {
				target += "?token=" + testToken
			}
			request := httptest.NewRequest(http.MethodGet, target, nil)
			for key, value := range test.headers {
				request.Header.Set(key, value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
			if called != (test.want == http.StatusOK) {
				t.Fatalf("downstream called = %v for status %d", called, response.Code)
			}
			if test.want == http.StatusUnauthorized {
				if body := decodeAPIError(t, response); body.Error.Code != "UNAUTHORIZED" {
					t.Fatalf("code = %q, want UNAUTHORIZED", body.Error.Code)
				}
				if response.Header().Get("WWW-Authenticate") == "" {
					t.Fatal("missing WWW-Authenticate challenge")
				}
			}
		})
	}
}

func TestAuthenticateProtectsEveryRouteAndMethod(t *testing.T) {
	handler := Authenticate(okHandler(nil), testToken)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		for _, path := range []string{"/api/health", "/api/admin/repos", "/api/workspaces", "/", "/nonexistent"} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
			if response.Code != http.StatusUnauthorized {
				t.Errorf("%s %s without a token = %d, want 401", method, path, response.Code)
			}
		}
	}
}

func TestTrustedHosts(t *testing.T) {
	handler, err := TrustedHosts(okHandler(nil), []string{"localhost", "127.0.0.1", "[::1]", " Tirion.Example.com:8443 ", "::1", ""})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		host string
		want int
	}{
		{"localhost", 200},
		{"localhost:8080", 200},
		{"LOCALHOST:8080", 200},
		{"127.0.0.1", 200},
		{"127.0.0.1:65535", 200},
		{"[::1]", 200},
		{"[::1]:8080", 200},
		{"tirion.example.com:8443", 200},
		{"TIRION.EXAMPLE.COM:8443", 200},
		// A port-qualified entry allows only that port.
		{"tirion.example.com", 403},
		{"tirion.example.com:9999", 403},
		{"localhost.evil.com", 403},
		{"evil.com", 403},
		{"evil.com:80", 403},
		{"localhost@evil.com", 403},
		{"127.0.0.2", 403},
		{"[::2]:8080", 403},
		{"", 403},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		request.Host = test.host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Errorf("Host %q = %d, want %d", test.host, response.Code, test.want)
			continue
		}
		if test.want == 403 {
			if decodeAPIError(t, response).Error.Code != "UNTRUSTED_HOST" {
				t.Errorf("Host %q: wrong error code", test.host)
			}
		} else if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("Host %q: missing hardening headers", test.host)
		}
	}
}

func TestTrustedHostsIgnoresForwardedHeaders(t *testing.T) {
	handler, err := TrustedHosts(okHandler(nil), []string{"localhost"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	request.Host = "evil.example"
	request.Header.Set("X-Forwarded-Host", "localhost")
	request.Header.Set("Forwarded", "host=localhost")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("forwarded headers must not satisfy the allowlist, got %d", response.Code)
	}
}

func TestTrustedHostsRejectsInvalidConfiguration(t *testing.T) {
	for _, hosts := range [][]string{
		nil, {}, {""}, {" ", ""},
		{"*"}, {"*.example.com"}, {"http://example.com"}, {"example.com/path"}, {"user@example.com"}, {"example.com\\x"}, {"exa mple.com"}, {"example.com:"},
	} {
		if _, err := TrustedHosts(okHandler(nil), hosts); err == nil {
			t.Errorf("TrustedHosts(%q) accepted invalid configuration", hosts)
		}
	}
}

func TestResourceLimitsBodyLimit(t *testing.T) {
	var received int
	handler := ResourceLimits(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = len(body)
		w.WriteHeader(http.StatusNoContent)
	}), Limits{})

	t.Run("declared length over limit is refused without reading", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/api/impact", io.LimitReader(zeroReader{}, MaxRequestBytes+1))
		request.ContentLength = MaxRequestBytes + 1
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusRequestEntityTooLarge || decodeAPIError(t, response).Error.Code != "PAYLOAD_TOO_LARGE" {
			t.Fatalf("status = %d body %q", response.Code, response.Body.String())
		}
	})
	t.Run("undeclared length over limit is cut off", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/api/impact", io.LimitReader(zeroReader{}, MaxRequestBytes+1))
		request.ContentLength = -1
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusRequestEntityTooLarge || decodeAPIError(t, response).Error.Code != "PAYLOAD_TOO_LARGE" {
			t.Fatalf("status = %d body %q", response.Code, response.Body.String())
		}
	})
	t.Run("exactly at the limit is accepted", func(t *testing.T) {
		received = 0
		request := httptest.NewRequest(http.MethodPost, "/api/impact", io.LimitReader(zeroReader{}, MaxRequestBytes))
		request.ContentLength = -1
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent || int64(received) != MaxRequestBytes {
			t.Fatalf("status = %d, handler received %d bytes", response.Code, received)
		}
	})
	t.Run("body read failure is a 400 JSON error", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/api/impact", io.MultiReader(strings.NewReader("{"), failingReader{}))
		request.ContentLength = -1
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || decodeAPIError(t, response).Error.Code != "INVALID_BODY" {
			t.Fatalf("status = %d body %q", response.Code, response.Body.String())
		}
	})
	t.Run("handler sees the buffered body", func(t *testing.T) {
		var got string
		echo := ResourceLimits(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			got = string(body)
		}), Limits{})
		request := httptest.NewRequest(http.MethodPost, "/api/impact", bytes.NewBufferString(`{"a":1}`))
		echo.ServeHTTP(httptest.NewRecorder(), request)
		if got != `{"a":1}` {
			t.Fatalf("handler body = %q", got)
		}
	})
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
