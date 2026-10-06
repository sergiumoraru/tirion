package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sergiumoraru/tirion/internal/diffparse"
	"github.com/sergiumoraru/tirion/internal/mcpintel"
	"github.com/sergiumoraru/tirion/internal/runtimeconfig"
)

// rpcRequest keeps the id as raw JSON so an absent id (a notification) can be
// told apart from an explicit null, and so numeric ids echo back exactly.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// A nil ID marshals as null, which is what JSON-RPC requires when the request
// id could not be determined (parse errors, invalid requests).
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type changedRange struct {
	Repo      string `json:"repo,omitempty"`
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
}

func main() {
	if err := runtimeconfig.LoadEnvironment(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	writer := bufio.NewWriter(os.Stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if out := processLine([]byte(line)); out != nil {
			writeRaw(writer, out)
		}
	}
	if err := scanner.Err(); err != nil {
		writeRPC(writer, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "MCP input exceeds 8 MiB or could not be read"}})
		writer.Flush()
		fmt.Fprintf(os.Stderr, "mcp-guard input error: %v\n", err)
		os.Exit(1)
	}
}

// processLine answers one line of input. It returns nil when nothing may be
// written: notifications (requests without an id) never get a response, and a
// batch made only of notifications gets none either.
func processLine(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(trimmed, &batch); err != nil {
			return marshalRPC(*fail(nil, -32700, "parse error: "+err.Error()))
		}
		if len(batch) == 0 {
			return marshalRPC(*fail(nil, -32600, "invalid request: empty batch"))
		}
		var out []rpcResponse
		for _, item := range batch {
			if resp := processMessage(item); resp != nil {
				out = append(out, *resp)
			}
		}
		if len(out) == 0 {
			return nil
		}
		data, _ := json.Marshal(out)
		return data
	}
	resp := processMessage(trimmed)
	if resp == nil {
		return nil
	}
	return marshalRPC(*resp)
}

func processMessage(raw []byte) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		if !json.Valid(raw) {
			return fail(nil, -32700, "parse error: "+err.Error())
		}
		return fail(nil, -32600, "invalid request")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fail(nil, -32600, "invalid request")
	}
	if req.Method == "" {
		if req.ID == nil || len(req.Result) > 0 || len(req.Error) > 0 {
			// A response to something we never sent, or a malformed notification.
			return nil
		}
		return fail(req.ID, -32600, "invalid request: missing method")
	}
	if req.ID == nil {
		// Notification (notifications/initialized, notifications/cancelled, ...):
		// the sender expects no reply, not even an error.
		return nil
	}
	return handleRPC(req)
}

func handleRPC(req rpcRequest) *rpcResponse {
	switch req.Method {
	case "initialize":
		return ok(req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo": map[string]any{
				"name":    "mcp-guard",
				"version": "core-impact",
			},
			"capabilities": map[string]any{"tools": map[string]any{}},
		})
	case "ping":
		return ok(req.ID, map[string]any{})
	case "tools/list":
		return ok(req.ID, map[string]any{"tools": tools()})
	case "tools/call":
		var params toolCallParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return fail(req.ID, -32602, err.Error())
			}
		}
		return ok(req.ID, callTool(params.Name, params.Arguments))
	default:
		return fail(req.ID, -32601, "method not found")
	}
}

func tools() []map[string]any {
	return []map[string]any{
		{
			"name":        "get_changed_ranges",
			"description": "Parse a unified diff, or the current git diff when omitted, into repo/path/line ranges suitable for codebase_impact.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"diff": map[string]any{"type": "string"},
					"repo": map[string]any{"type": "string"},
				},
			},
		},
		{
			"name":        "impact_from_diff",
			"description": "Send a unified diff, or the current git diff when omitted, to Tirion /api/impact and return the impact report.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"diff":        map[string]any{"type": "string"},
					"repo":        map[string]any{"type": "string"},
					"workspaceId": map[string]any{"type": "string"},
					"depth":       map[string]any{"type": "integer"},
					"maxNodes":    map[string]any{"type": "integer"},
					"noTests":     map[string]any{"type": "boolean"},
				},
			},
		},
		{
			"name":        "verify_diff",
			"description": "Validate completed agent work. Provide task, root-cause evidence, requirement claims, and a unified diff (or use the current git diff).",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"repo", "task", "rootCause", "requirementsComplete", "rootCauseEvidence", "claims"},
				"properties": map[string]any{
					"diff":                 map[string]any{"type": "string"},
					"repo":                 map[string]any{"type": "string"},
					"task":                 map[string]any{"type": "string"},
					"baseSha":              map[string]any{"type": "string"},
					"rootCause":            map[string]any{"type": "string"},
					"requirementsComplete": map[string]any{"type": "boolean"},
					"rootCauseEvidence":    map[string]any{"type": "array"},
					"claims":               map[string]any{"type": "array"},
					"workspaceId":          map[string]any{"type": "string"},
					"depth":                map[string]any{"type": "integer"},
					"maxNodes":             map[string]any{"type": "integer"},
					"noTests":              map[string]any{"type": "boolean"},
				},
			},
		},
	}
}

func callTool(name string, args map[string]any) toolResult {
	if args == nil {
		args = map[string]any{}
	}
	switch name {
	case "get_changed_ranges":
		return getChangedRanges(args)
	case "impact_from_diff":
		return impactFromDiff(args)
	case "verify_diff":
		return verifyDiff(args)
	default:
		return errorResult(fmt.Sprintf("unknown tool: %s", name))
	}
}

func getChangedRanges(args map[string]any) toolResult {
	diff, err := diffFromArgs(args)
	if err != nil {
		return errorResult(err.Error())
	}
	repo := argString(args, "repo")
	parsed, err := parseRanges(diff, repo)
	if err != nil {
		return errorResult("diff cannot be parsed: " + err.Error())
	}
	ranges := mergeRanges(parsed)
	body := map[string]any{"ranges": ranges, "changedFiles": countFiles(ranges)}
	text := fmt.Sprintf("Parsed %d changed ranges across %d files.", len(ranges), countFiles(ranges))
	return jsonResult(text, body)
}

func impactFromDiff(args map[string]any) toolResult {
	return impactFromDiffWithVerify(args, false)
}

func verifyDiff(args map[string]any) toolResult {
	return impactFromDiffWithVerify(args, true)
}

func impactFromDiffWithVerify(args map[string]any, verify bool) toolResult {
	diff, err := diffFromArgs(args)
	if err != nil {
		return errorResult(err.Error())
	}
	req := map[string]any{
		"diff":     diff,
		"noTests":  argBool(args, "noTests", true),
		"depth":    argInt(args, "depth", 4),
		"maxNodes": argInt(args, "maxNodes", 2000),
	}
	if verify {
		req["verify"] = true
		if task := argString(args, "task"); task != "" || args["rootCauseEvidence"] != nil || args["claims"] != nil {
			req["agentWork"] = map[string]any{
				"task":                 task,
				"baseSha":              argString(args, "baseSha"),
				"rootCause":            argString(args, "rootCause"),
				"requirementsComplete": argBool(args, "requirementsComplete", false),
				"rootCauseEvidence":    args["rootCauseEvidence"],
				"claims":               args["claims"],
			}
		}
	}
	if repo := argString(args, "repo"); repo != "" {
		req["repo"] = repo
	}
	if workspaceID := argString(args, "workspaceId"); workspaceID != "" {
		req["workspaceId"] = workspaceID
	}
	var resp map[string]any
	endpoint := "/api/impact"
	if verify {
		endpoint = "/api/verify"
	}
	if err := postJSON(endpoint, req, &resp); err != nil {
		return errorResult(err.Error())
	}
	if verify {
		verifyBlock, _ := resp["verify"].(map[string]any)
		verdict := asString(verifyBlock["verdict"])
		breaking := anySlice(verifyBlock["breakingChanges"])
		reasons := anySlice(verifyBlock["reasons"])
		text := fmt.Sprintf("Agent work verify: %s, breaking=%d.", verdict, len(breaking))
		if len(reasons) > 0 {
			text += " " + asString(reasons[0])
		}
		return jsonResult(text, compactVerifyResponse(resp))
	}
	summary, _ := resp["summary"].(map[string]any)
	stats, _ := resp["stats"].(map[string]any)
	text := fmt.Sprintf("Impact: roots=%d entrypoints=%d repos=%d.",
		len(anySlice(resp["roots"])),
		len(anySlice(summary["entrypoints"])),
		len(anySlice(summary["repos"])))
	if stats != nil {
		text += fmt.Sprintf(" nodes=%v duration=%v.", stats["totalNodes"], stats["impactTime"])
	}
	return jsonResult(text, resp)
}

func compactVerifyResponse(resp map[string]any) map[string]any {
	out := map[string]any{
		"workspace":   resp["workspace"],
		"roots":       resp["roots"],
		"rootDetails": resp["rootDetails"],
		"stats":       resp["stats"],
		"verify":      resp["verify"],
		"warnings":    resp["warnings"],
	}
	return out
}

func diffFromArgs(args map[string]any) (string, error) {
	// Never trim a diff: a trailing blank context line (" ") is part of its hunk.
	if diff, _ := args["diff"].(string); strings.TrimSpace(diff) != "" {
		return diff, nil
	}
	diff, err := mcpintel.LocalGitDiffFromEnv()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(diff) == "" {
		return "", fmt.Errorf("no diff provided and configured workdir diff is empty")
	}
	return diff, nil
}

// parseRanges returns the edited line ranges in pre-image coordinates (what the
// index holds). Only hunk bodies are interpreted, so added content that merely
// looks like a header ("+++ x") cannot change the current file.
func parseRanges(diff, repo string) ([]changedRange, error) {
	files, err := diffparse.Parse(diff)
	if err != nil {
		return nil, err
	}
	var ranges []changedRange
	for _, file := range files {
		if file.New {
			continue
		}
		for _, edit := range file.Edits() {
			start := max(edit.Start, 1)
			ranges = append(ranges, changedRange{
				Repo:      strings.TrimSpace(repo),
				Path:      edit.Path,
				StartLine: start,
				EndLine:   max(edit.End, start),
			})
		}
	}
	return ranges, nil
}

func mergeRanges(in []changedRange) []changedRange {
	grouped := map[string][]changedRange{}
	for _, r := range in {
		if r.Path == "" || r.StartLine <= 0 {
			continue
		}
		if r.EndLine < r.StartLine {
			r.EndLine = r.StartLine
		}
		grouped[r.Repo+"|"+r.Path] = append(grouped[r.Repo+"|"+r.Path], r)
	}
	var out []changedRange
	for _, ranges := range grouped {
		sort.Slice(ranges, func(i, j int) bool {
			return ranges[i].StartLine < ranges[j].StartLine
		})
		current := ranges[0]
		for _, next := range ranges[1:] {
			if next.StartLine <= current.EndLine+1 {
				if next.EndLine > current.EndLine {
					current.EndLine = next.EndLine
				}
				continue
			}
			out = append(out, current)
			current = next
		}
		out = append(out, current)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].StartLine < out[j].StartLine
	})
	return out
}

func countFiles(ranges []changedRange) int {
	seen := map[string]bool{}
	for _, r := range ranges {
		seen[r.Repo+"|"+r.Path] = true
	}
	return len(seen)
}

func postJSON(path string, body any, out any) error {
	base := strings.TrimRight(os.Getenv("TIRION_API_URL"), "/")
	if base == "" {
		base = strings.TrimRight(os.Getenv("CODEBASE_INTEL_API_URL"), "/")
	}
	if base == "" {
		base = "http://localhost:8080"
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	token, err := runtimeconfig.ClientToken(base)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: runtimeconfig.NoRedirect}
	req, err := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("X-Tirion-Token", token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if readErr != nil {
		return readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(respBody, out)
}

func ok(id json.RawMessage, result any) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func fail(id json.RawMessage, code int, message string) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}

func marshalRPC(resp rpcResponse) []byte {
	data, _ := json.Marshal(resp)
	return data
}

func writeRPC(writer *bufio.Writer, resp rpcResponse) {
	writeRaw(writer, marshalRPC(resp))
}

func writeRaw(writer *bufio.Writer, data []byte) {
	fmt.Fprintln(writer, string(data))
	writer.Flush()
}

func jsonResult(text string, data any) toolResult {
	raw, _ := json.MarshalIndent(data, "", "  ")
	return toolResult{Content: []toolContent{
		{Type: "text", Text: text},
		{Type: "text", Text: string(raw)},
	}}
}

func errorResult(message string) toolResult {
	return toolResult{IsError: true, Content: []toolContent{{Type: "text", Text: message}}}
}

func argString(args map[string]any, key string) string {
	value, ok := args[key]
	if !ok {
		return ""
	}
	if typed, ok := value.(string); ok {
		return strings.TrimSpace(typed)
	}
	return ""
}

func argInt(args map[string]any, key string, fallback int) int {
	value, ok := args[key]
	if !ok {
		return fallback
	}
	switch typed := value.(type) {
	case float64:
		if typed > 0 {
			return int(typed)
		}
	case int:
		if typed > 0 {
			return typed
		}
	}
	return fallback
}

func argBool(args map[string]any, key string, fallback bool) bool {
	value, ok := args[key]
	if !ok {
		return fallback
	}
	if typed, ok := value.(bool); ok {
		return typed
	}
	return fallback
}

func anySlice(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	default:
		return nil
	}
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		if value == nil {
			return ""
		}
		return fmt.Sprint(value)
	}
}
