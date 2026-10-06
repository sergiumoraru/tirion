package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		status := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		summary := captureRequestSummary(r)
		next.ServeHTTP(status, r)

		if summary != "" {
			log.Printf("%s %q %d %s %s", r.Method, r.URL.Path, status.status, summary, time.Since(start))
			return
		}
		log.Printf("%s %q %d %s", r.Method, r.URL.Path, status.status, time.Since(start))
	})
}

func captureRequestSummary(r *http.Request) string {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/search":
		return summarizeSearchQuery(r.URL.Query())
	case r.Method == http.MethodPost && r.URL.Path == "/api/search/integrations":
		return summarizeJSONBody(r, summarizeSearchIntegrationsBody)
	case r.Method == http.MethodPost && r.URL.Path == "/api/trace":
		return summarizeJSONBody(r, summarizeTraceBody)
	case r.Method == http.MethodPost && r.URL.Path == "/api/trace/expand":
		return summarizeJSONBody(r, summarizeTraceExpandBody)
	case r.Method == http.MethodPost && r.URL.Path == "/api/flow":
		return summarizeJSONBody(r, summarizeFlowBody)
	case r.Method == http.MethodPost && r.URL.Path == "/api/impact":
		return summarizeJSONBody(r, summarizeImpactBody)
	case r.Method == http.MethodPost && r.URL.Path == "/api/verify":
		return summarizeJSONBody(r, summarizeImpactBody)
	default:
		return ""
	}
}

func summarizeJSONBody(r *http.Request, format func(map[string]any) string) string {
	if r.Body == nil {
		return ""
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(nil))
		return ""
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if len(bytes.TrimSpace(body)) == 0 {
		return ""
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Sprintf("body_bytes=%d", len(body))
	}
	return format(payload)
}

func summarizeSearchQuery(values url.Values) string {
	parts := []string{
		fmt.Sprintf("q=%s", quoteLogValue(values.Get("q"))),
	}
	if mode := strings.TrimSpace(values.Get("mode")); mode != "" {
		parts = append(parts, "mode="+mode)
	}
	if repo := strings.TrimSpace(values.Get("repo")); repo != "" {
		parts = append(parts, "repo="+quoteLogValue(repo))
	}
	if limit := strings.TrimSpace(values.Get("limit")); limit != "" {
		parts = append(parts, "limit="+limit)
	}
	if sort := strings.TrimSpace(values.Get("sort")); sort != "" {
		parts = append(parts, "sort="+sort)
	}
	for _, key := range []string{"functionsOffset", "classesOffset", "typeSymbolsOffset", "endpointsOffset", "dataEntitiesOffset", "externalSymbolsOffset", "schedulesOffset", "graphqlOperationsOffset", "azureTriggersOffset", "queueHitsOffset"} {
		if raw := strings.TrimSpace(values.Get(key)); raw != "" && raw != "0" {
			parts = append(parts, key+"="+raw)
		}
	}
	return strings.Join(parts, " ")
}

func summarizeSearchIntegrationsBody(payload map[string]any) string {
	functionCount := len(anySlice(payload["functionCallerIds"]))
	classCount := len(anySlice(payload["classIds"]))
	return fmt.Sprintf("functionCallerIds=%d classIds=%d", functionCount, classCount)
}

func summarizeTraceBody(payload map[string]any) string {
	return strings.Join(compactParts(
		valuePart("function", payload["function"]),
		valuePart("match", payload["match"]),
		intPart("depth", payload["depth"]),
		intPart("maxNodes", payload["maxNodes"]),
		boolPart("noTests", payload["noTests"]),
		countPart("includeRepos", payload["includeRepos"]),
		countPart("excludeRepos", payload["excludeRepos"]),
	), " ")
}

func summarizeTraceExpandBody(payload map[string]any) string {
	return strings.Join(compactParts(
		valuePart("callerId", payload["callerId"]),
		valuePart("direction", payload["direction"]),
		intPart("depth", payload["depth"]),
		intPart("maxDepth", payload["maxDepth"]),
		intPart("maxNodes", payload["maxNodes"]),
		valuePart("allowedRepo", payload["allowedRepo"]),
	), " ")
}

func summarizeFlowBody(payload map[string]any) string {
	return strings.Join(compactParts(
		valuePart("start", payload["start"]),
		intPart("depth", payload["depth"]),
		intPart("maxHops", payload["maxHops"]),
		boolPart("noTests", payload["noTests"]),
		boolPart("includeRelatedEntities", payload["includeRelatedEntities"]),
	), " ")
}

func summarizeImpactBody(payload map[string]any) string {
	return strings.Join(compactParts(
		countPart("functions", payload["functions"]),
		countPart("callerIds", payload["callerIds"]),
		countPart("functionIds", payload["functionIds"]),
		countPart("files", payload["files"]),
		countPart("ranges", payload["ranges"]),
		intPart("depth", payload["depth"]),
		intPart("maxNodes", payload["maxNodes"]),
		boolPart("noTests", payload["noTests"]),
		boolPart("includeTrace", payload["includeTrace"]),
	), " ")
}

func valuePart(label string, value any) string {
	text := strings.TrimSpace(anyString(value))
	if text == "" {
		return ""
	}
	return label + "=" + quoteLogValue(text)
}

func intPart(label string, value any) string {
	if n, ok := anyInt(value); ok && n != 0 {
		return label + "=" + strconv.Itoa(n)
	}
	return ""
}

func boolPart(label string, value any) string {
	if b, ok := value.(bool); ok && b {
		return label + "=true"
	}
	return ""
}

func countPart(label string, value any) string {
	if count := len(anySlice(value)); count > 0 {
		return label + "=" + strconv.Itoa(count)
	}
	return ""
}

func compactParts(parts ...string) []string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func anyString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		return ""
	}
}

func anyInt(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	default:
		return 0, false
	}
}

func anySlice(value any) []any {
	switch v := value.(type) {
	case []any:
		return v
	default:
		return nil
	}
}

func quoteLogValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return `""`
	}
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	if len(value) > 120 {
		value = value[:117] + "..."
	}
	return strconv.Quote(value)
}
