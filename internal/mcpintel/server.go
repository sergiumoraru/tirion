package mcpintel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/sergiumoraru/tirion/internal/runtimeconfig"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultProtocolVersion = "2024-11-05"
	serverName             = "tirion-intel"
	serverVersion          = "0.1.0"
)

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *mcpError   `json:"error,omitempty"`
}

type mcpError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type toolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type toolResult struct {
	Content           []toolContent `json:"content"`
	StructuredContent interface{}   `json:"structuredContent,omitempty"`
	IsError           bool          `json:"isError,omitempty"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type mcpServer struct {
	// apiToken caches the token once resolveToken has found it. The token is
	// resolved per call rather than at startup so the server starts before
	// `tirion serve` has ever created the token file.
	apiToken     string
	resolveToken func() (string, error)
	tokenMu      sync.Mutex
	localRoots   []*os.Root
	apiBaseURL   string
	workspace    string
	authHeader   string
	username     string
	password     string
	actor        string
	workdir      string
	client       *http.Client
}

func RunFromEnv(in io.Reader, out io.Writer) error {
	if err := runtimeconfig.LoadEnvironment(); err != nil {
		return err
	}

	// Optional process log for MCP transport diagnostics.
	if logPath := strings.TrimSpace(getEnvFirst("TIRION_MCP_LOG_FILE", "CODEBASE_INTEL_MCP_LOG_FILE")); logPath != "" {
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			previousOutput, previousFlags := log.Writer(), log.Flags()
			defer func() {
				log.SetOutput(previousOutput)
				log.SetFlags(previousFlags)
				f.Close()
			}()
			log.SetOutput(f)
			log.SetFlags(log.LstdFlags | log.Lmicroseconds)
			log.Printf("mcp-intel: log started pid=%d", os.Getpid())
		}
	}

	apiBase := strings.TrimSpace(getEnvFirst("TIRION_API_URL", "CODEBASE_INTEL_API_URL"))
	if apiBase == "" {
		apiBase = "http://localhost:8080"
	}

	timeout := 60 * time.Second
	if raw := strings.TrimSpace(getEnvFirst("TIRION_MCP_TIMEOUT_SECONDS", "CODEBASE_INTEL_MCP_TIMEOUT_SECONDS")); raw != "" {
		if secs, err := strconv.Atoi(raw); err == nil && secs > 0 && secs <= 600 {
			timeout = time.Duration(secs) * time.Second
		}
	}

	roots, err := openLocalRoots()
	if err != nil {
		return err
	}
	defer func() {
		for _, root := range roots {
			root.Close()
		}
	}()
	client := &http.Client{Timeout: timeout, CheckRedirect: runtimeconfig.NoRedirect}
	server := &mcpServer{
		apiBaseURL: apiBase,
		resolveToken: func() (string, error) {
			return runtimeconfig.ClientToken(apiBase)
		},
		localRoots: roots,
		workspace:  strings.TrimSpace(getEnvFirst("TIRION_WORKSPACE", "CODEBASE_INTEL_WORKSPACE")),
		authHeader: strings.TrimSpace(getEnvFirst("TIRION_API_AUTH_HEADER", "CODEBASE_INTEL_API_AUTH_HEADER")),
		username:   strings.TrimSpace(getEnvFirst("TIRION_API_USERNAME", "CODEBASE_INTEL_API_USERNAME")),
		password:   getEnvFirst("TIRION_API_PASSWORD", "CODEBASE_INTEL_API_PASSWORD"),
		actor:      resolveActor(),
		workdir:    strings.TrimSpace(getEnvFirst("TIRION_WORKDIR", "CODEBASE_INTEL_WORKDIR")),
		client:     client,
	}

	return server.serve(in, out)
}

func RunCLI() {
	if err := RunFromEnv(os.Stdin, os.Stdout); err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "mcp-intel error: %v\n", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// JSON-RPC serve loop
// ---------------------------------------------------------------------------

func (s *mcpServer) serve(in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)
	writer := bufio.NewWriter(out)
	defer writer.Flush()

	for {
		body, err := readMessage(reader)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if len(bytes.TrimSpace(body)) == 0 {
			continue
		}

		var req mcpRequest
		if err := json.Unmarshal(body, &req); err != nil {
			resp := mcpResponse{
				JSONRPC: "2.0",
				Error: &mcpError{
					Code:    -32700,
					Message: "parse error",
				},
			}
			if err := writeMessage(writer, resp); err != nil {
				return err
			}
			continue
		}

		resp, hasResponse := s.handle(req)
		if !hasResponse {
			continue
		}
		if err := writeMessage(writer, resp); err != nil {
			return err
		}
	}
}

func (s *mcpServer) handle(req mcpRequest) (mcpResponse, bool) {
	id := parseID(req.ID)
	if len(req.ID) == 0 {
		return mcpResponse{}, false
	}
	if id == nil {
		return mcpResponse{JSONRPC: "2.0", Error: &mcpError{
			Code: -32600, Message: "invalid request id: expected a string or number",
		}}, true
	}
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return mcpResponse{
			JSONRPC: "2.0",
			ID:      id,
			Error: &mcpError{
				Code:    -32600,
				Message: "invalid request: jsonrpc must be 2.0",
			},
		}, true
	}

	switch req.Method {
	case "notifications/initialized":
		return mcpResponse{}, false
	case "initialize":
		protocol := defaultProtocolVersion
		if len(req.Params) > 0 {
			var params map[string]any
			if err := json.Unmarshal(req.Params, &params); err == nil {
				if p := asString(params["protocolVersion"]); p != "" {
					protocol = p
				}
				if info, ok := params["clientInfo"].(map[string]any); ok && info != nil {
					name := asString(info["name"])
					version := asString(info["version"])
					log.Printf("mcp-intel: initialize clientInfo name=%q version=%q", name, version)
				} else {
					log.Printf("mcp-intel: initialize without clientInfo")
				}
			}
		}
		result := map[string]any{
			"protocolVersion": protocol,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    serverName,
				"version": serverVersion,
			},
			"instructions": s.instructions(),
		}
		return mcpResponse{JSONRPC: "2.0", ID: id, Result: result}, true
	case "shutdown":
		return mcpResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{}}, true
	case "exit":
		return mcpResponse{}, false
	case "ping":
		return mcpResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{"ok": true}}, true
	case "resources/list":
		return mcpResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{"resources": []any{}}}, true
	case "resources/templates/list":
		return mcpResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{"resourceTemplates": []any{}}}, true
	case "prompts/list":
		return mcpResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{"prompts": []any{}}}, true
	case "tools/list":
		return mcpResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{"tools": s.tools()}}, true
	case "tools/call":
		var params toolCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return mcpResponse{
				JSONRPC: "2.0",
				ID:      id,
				Error: &mcpError{
					Code:    -32602,
					Message: "invalid params for tools/call",
				},
			}, true
		}
		result := s.callTool(params.Name, params.Arguments)
		return mcpResponse{JSONRPC: "2.0", ID: id, Result: result}, true
	default:
		return mcpResponse{
			JSONRPC: "2.0",
			ID:      id,
			Error: &mcpError{
				Code:    -32601,
				Message: "method not found",
			},
		}, true
	}
}

func (s *mcpServer) instructions() string {
	lines := []string{
		"Use Tirion as source-backed code intelligence: search code, read indexed source, trace execution/data paths, inspect contracts, and assess impact.",
		"Use the selected workspace's indexed source and graph evidence to investigate changes, then edit your checkout using your own development tools.",
		"Before submitting changes, call codebase_verify with the repository diff and inspect its findings. Its verdict covers indexed estate obligations, not overall application correctness.",
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// Tool definitions
// ---------------------------------------------------------------------------

func agentWorkEvidenceSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"file"},
		"properties": map[string]any{
			"kind":   map[string]any{"type": "string", "description": "Evidence role, for example implementation, runtime_boundary, preservation, or test."},
			"repo":   map[string]any{"type": "string"},
			"file":   map[string]any{"type": "string"},
			"symbol": map[string]any{"type": "string"},
			"line":   map[string]any{"type": "integer"},
		},
	}
}

func (s *mcpServer) tools() []toolSchema {
	tools := []toolSchema{
		{
			Name:        "codebase_search",
			Description: "Search indexed functions, classes, type/interface symbols, endpoints, GraphQL operations/resolvers, Azure triggers, schedules, queue producers/consumers, and data entities by name. Use this to find the exact operation, trigger, queue, function, or repo to inspect next.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"noNoise": map[string]any{"type": "boolean", "description": "Hide generated/test boilerplate (default true); set false to include every indexed match."},
					"query": map[string]any{
						"type":        "string",
						"description": "Search query — a function name, class name, endpoint path, or keyword.",
					},
					"repo": map[string]any{
						"type":        "string",
						"description": "Optional: filter results to a specific repository.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Max results to return (default 10, max 50).",
						"default":     10,
					},
					"mode": map[string]any{
						"type":        "string",
						"enum":        []string{"keyword", "trigram"},
						"description": "Search mode (default: keyword).",
						"default":     "keyword",
					},
					"workspaceId": map[string]any{
						"type":        "string",
						"description": "Optional Tirion workspace slug. Defaults to TIRION_WORKSPACE for this MCP server.",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "codebase_trace",
			Description: "Trace the upstream callers and downstream callees of a function, GraphQL operation/resolver, Azure Function trigger, endpoint, queue/Service Bus resource, or schedule. Shows the full call chain across services including HTTP and queue boundaries. Use exact names from codebase_search results.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"function": map[string]any{
						"type":        "string",
						"description": "Fully-qualified function name (e.g. 'CatalogController.listItems').",
					},
					"depth": map[string]any{
						"type":        "integer",
						"description": "Max traversal depth (default 4).",
						"default":     4,
					},
					"noTests": map[string]any{
						"type":        "boolean",
						"description": "Exclude test files from results (default true).",
						"default":     true,
					},
					"resolve": map[string]any{
						"type":        "boolean",
						"description": "Resolve cross-service HTTP/queue edges (default false). Setting to true gives richer results but is slower.",
						"default":     false,
					},
					"maxNodes": map[string]any{
						"type":        "integer",
						"description": "Max nodes in the trace graph (default 2000).",
						"default":     2000,
					},
					"workspaceId": map[string]any{
						"type":        "string",
						"description": "Optional Tirion workspace slug. Defaults to TIRION_WORKSPACE for this MCP server.",
					},
				},
				"required": []string{"function"},
			},
		},
		{
			Name:        "code_search",
			Description: "Alias for codebase_search. Search indexed functions, classes, type/interface symbols, endpoints, GraphQL operations/resolvers, Azure triggers, schedules, queue producers/consumers, and data entities by keyword.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"noNoise":     map[string]any{"type": "boolean", "description": "Hide generated/test boilerplate (default true); set false to include every indexed match."},
					"query":       map[string]any{"type": "string"},
					"repo":        map[string]any{"type": "string"},
					"limit":       map[string]any{"type": "integer", "default": 10},
					"mode":        map[string]any{"type": "string", "enum": []string{"keyword", "trigram"}, "default": "keyword"},
					"workspaceId": map[string]any{"type": "string"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "code_trace",
			Description: "Alias for codebase_trace. Trace callers/callees for a known function, GraphQL operation/resolver, endpoint, Azure trigger, queue/Service Bus resource, or schedule.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"function":    map[string]any{"type": "string"},
					"depth":       map[string]any{"type": "integer", "default": 4},
					"noTests":     map[string]any{"type": "boolean", "default": true},
					"resolve":     map[string]any{"type": "boolean", "default": false},
					"maxNodes":    map[string]any{"type": "integer", "default": 2000},
					"workspaceId": map[string]any{"type": "string"},
				},
				"required": []string{"function"},
			},
		},
		{
			Name:        "code_read",
			Description: "Read a bounded local source file or file range for source-backed investigation. This is read-only and clamps output size.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":      map[string]any{"type": "string", "description": "Path inside TIRION_REPOS_ROOT or TIRION_WORKDIR; relative paths require TIRION_WORKDIR."},
					"repoPath":  map[string]any{"type": "string", "description": "Optional repository root when using file."},
					"file":      map[string]any{"type": "string", "description": "Optional repo-relative file path."},
					"startLine": map[string]any{"type": "integer", "description": "Optional 1-based start line."},
					"endLine":   map[string]any{"type": "integer", "description": "Optional 1-based end line."},
					"maxBytes":  map[string]any{"type": "integer", "default": 200000},
				},
			},
		},
		{
			Name:        "git_diff",
			Description: "Read a bounded git diff from a local repository. This is read-only; use staged=true for cached diff or baseRef for a compare base.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repoPath":  map[string]any{"type": "string"},
					"staged":    map[string]any{"type": "boolean", "default": false},
					"baseRef":   map[string]any{"type": "string"},
					"files":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"maxBytes":  map[string]any{"type": "integer", "default": 500000},
					"statOnly":  map[string]any{"type": "boolean", "default": false},
					"workspace": map[string]any{"type": "string", "description": "Alias for repoPath."},
				},
				"required": []string{"repoPath"},
			},
		},
		{
			Name:        "codebase_flow",
			Description: "Get the end-to-end flow starting from a function. Best for the main path once you know the right root.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"start": map[string]any{
						"type":        "string",
						"description": "Starting function name (e.g. 'CatalogController.listItems').",
					},
					"depth": map[string]any{
						"type":        "integer",
						"description": "Max traversal depth (default 3, use 5 for richer results).",
						"default":     3,
					},
					"maxHops": map[string]any{
						"type":        "integer",
						"description": "Max number of hops to return (default 200).",
						"default":     200,
					},
					"noTests": map[string]any{
						"type":        "boolean",
						"description": "Exclude test files (default true).",
						"default":     true,
					},
					"workspaceId": map[string]any{
						"type":        "string",
						"description": "Optional Tirion workspace slug. Defaults to TIRION_WORKSPACE for this MCP server.",
					},
				},
				"required": []string{"start"},
			},
		},
		{
			Name:        "codebase_graphql_flow",
			Description: "Resolve a GraphQL operation across indexed frontend usages, backend resolver exports, backend entrypoints, and controller files. Use this when a story names a .gql/.graphql operation, Apollo query/mutation, or GraphQL resolver.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"operation": map[string]any{
						"type":        "string",
						"description": "GraphQL operation or resolver name, for example AllResources.",
					},
					"frontendRepo": map[string]any{
						"type":        "string",
						"description": "Optional frontend repository filter.",
					},
					"backendRepo": map[string]any{
						"type":        "string",
						"description": "Optional backend GraphQL repository filter.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Max rows per section (default 25, max 100).",
						"default":     25,
					},
					"workspaceId": map[string]any{
						"type":        "string",
						"description": "Optional Tirion workspace slug. Defaults to TIRION_WORKSPACE for this MCP server.",
					},
				},
				"required": []string{"operation"},
			},
		},
		{
			Name:        "codebase_impact",
			Description: "Analyze the blast radius of changing specific functions, file ranges, whole files, or a unified diff. Shows affected entrypoints, HTTP calls, queues, EventBridge/Azure timer schedules, and repositories touched. Use this before making changes to understand risk.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"functions": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Function names to analyze (e.g. ['UserService.createUser']).",
					},
					"ranges": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"repo":      map[string]any{"type": "string"},
								"path":      map[string]any{"type": "string"},
								"startLine": map[string]any{"type": "integer"},
								"endLine":   map[string]any{"type": "integer"},
							},
							"required": []string{"path", "startLine", "endLine"},
						},
						"description": "File ranges to analyze (alternative to function names).",
					},
					"files": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"repo": map[string]any{"type": "string"},
								"path": map[string]any{"type": "string"},
							},
							"required": []string{"path"},
						},
						"description": "Whole files to analyze (alternative to functions/ranges).",
					},
					"diff": map[string]any{
						"type":        "string",
						"description": "Unified diff to analyze. Tirion maps changed hunks to indexed functions and traces impact.",
					},
					"repo": map[string]any{
						"type":        "string",
						"description": "Optional repository name for the diff, file, or range paths. Inferred from the diff paths when omitted; required only if the paths are ambiguous across indexed repositories.",
					},
					"depth": map[string]any{
						"type":        "integer",
						"description": "Max traversal depth (default 4).",
						"default":     4,
					},
					"maxNodes": map[string]any{
						"type":        "integer",
						"description": "Max nodes in the impact graph (default 2000).",
						"default":     2000,
					},
					"noTests": map[string]any{
						"type":        "boolean",
						"description": "Exclude test files (default true).",
						"default":     true,
					},
					"workspaceId": map[string]any{
						"type":        "string",
						"description": "Optional Tirion workspace slug. Defaults to TIRION_WORKSPACE for this MCP server.",
					},
				},
			},
		},
		{
			Name:        "codebase_verify",
			Description: "Validate a repository change against the active estate graph. With an anchor, Tirion derives fix-connection and equivalent-predecessor obligations from the graph; without one, it validates deterministic contract deltas and regression exposure only.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"repo"},
				"properties": map[string]any{
					"diff": map[string]any{
						"type":        "string",
						"description": "Unified diff to verify. If omitted, the local git diff is used when available.",
					},
					"repo": map[string]any{
						"type":        "string",
						"description": "Repository name for diff paths when the diff itself does not identify a repo.",
					},
					"task": map[string]any{
						"type":        "string",
						"description": "The original task or story the agent implemented.",
					},
					"baseSha": map[string]any{
						"type":        "string",
						"description": "Git SHA the diff is based on. Tirion compares it to the active indexed workspace SHA.",
					},
					"anchor": map[string]any{
						"type":        "object",
						"description": "Observed failure boundary for fix-validation mode. It must resolve outside the changed symbol set.",
						"required":    []string{"kind", "ref"},
						"properties": map[string]any{
							"kind": map[string]any{"type": "string", "enum": []string{"symbol", "endpoint", "queue", "entity"}},
							"ref":  map[string]any{"type": "string"},
							"repo": map[string]any{"type": "string"},
						},
					},
					"waivers": map[string]any{
						"type":        "array",
						"description": "Explicit waivers for derived obligations. Waivers are recorded and cap a clean verdict at info; they never satisfy an obligation.",
						"items": map[string]any{
							"type":     "object",
							"required": []string{"subject", "justification"},
							"properties": map[string]any{
								"class":         map[string]any{"type": "string"},
								"subject":       map[string]any{"type": "string"},
								"justification": map[string]any{"type": "string"},
							},
						},
					},
					"requireAnchor": map[string]any{
						"type":        "boolean",
						"description": "Require fix-validation mode; an absent anchor yields not_verified instead of contracts-only verification.",
					},
					"rootCause": map[string]any{
						"type":        "string",
						"description": "The concrete mechanism diagnosed by the agent, stated before implementation.",
					},
					"requirementsComplete": map[string]any{
						"type":        "boolean",
						"description": "True only when claims contains every behavior/constraint from the task.",
					},
					"rootCauseEvidence": map[string]any{
						"type":        "array",
						"description": "Source locations proving the diagnosed cause. These may be unchanged by the diff.",
						"items":       agentWorkEvidenceSchema(),
					},
					"claims": map[string]any{
						"type":        "array",
						"description": "One entry per task requirement. covered claims must cite changed implementation code; preserved claims must cite unchanged paths.",
						"items": map[string]any{
							"type":     "object",
							"required": []string{"id", "requirement", "status"},
							"properties": map[string]any{
								"id":          map[string]any{"type": "string"},
								"requirement": map[string]any{"type": "string"},
								"status":      map[string]any{"type": "string", "enum": []string{"covered", "preserved", "not_applicable"}},
								"rationale":   map[string]any{"type": "string"},
								"evidence":    map[string]any{"type": "array", "items": agentWorkEvidenceSchema()},
							},
						},
					},
					"workspaceId": map[string]any{
						"type":        "string",
						"description": "Optional Tirion workspace slug. Defaults to TIRION_WORKSPACE for this MCP server.",
					},
					"depth": map[string]any{
						"type":        "integer",
						"description": "Max traversal depth (default 4).",
						"default":     4,
					},
					"maxNodes": map[string]any{
						"type":        "integer",
						"description": "Max nodes in the impact graph (default 2000).",
						"default":     2000,
					},
					"noTests": map[string]any{
						"type":        "boolean",
						"description": "Exclude test files (default true).",
						"default":     true,
					},
				},
			},
		},
		{
			Name:        "codebase_contracts",
			Description: "Get a repository's service boundaries: endpoints, outgoing HTTP, GraphQL operations/usages/resolvers/permission rules, data/side-effect accesses, queues, EventBridge/Azure timer schedules, and counterparty repos.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo": map[string]any{
						"type":        "string",
						"description": "Repository name (e.g. 'orders-api').",
					},
					"workspaceId": map[string]any{
						"type":        "string",
						"description": "Optional Tirion workspace slug. Defaults to TIRION_WORKSPACE for this MCP server.",
					},
				},
				"required": []string{"repo"},
			},
		},
	}
	return tools
}

// ---------------------------------------------------------------------------
// Tool dispatch
// ---------------------------------------------------------------------------

func (s *mcpServer) callTool(name string, args map[string]any) toolResult {
	if args == nil {
		args = map[string]any{}
	}

	switch name {
	case "codebase_search", "code_search":
		return s.toolSearch(args)
	case "codebase_trace", "code_trace":
		return s.toolTrace(args)
	case "code_read":
		return s.toolCodeRead(args)
	case "git_diff":
		return s.toolGitDiff(args)
	case "codebase_flow":
		return s.toolFlow(args)
	case "codebase_graphql_flow":
		return s.toolGraphQLFlow(args)
	case "codebase_impact":
		return s.toolImpact(args)
	case "codebase_verify":
		return s.toolVerify(args)
	case "codebase_contracts":
		return s.toolContracts(args)
	default:
		return errorToolResult(fmt.Sprintf("unknown tool: %s", name), nil)
	}
}

func joinAnyStrings(items []any) string {
	var values []string
	for _, item := range items {
		if value := strings.TrimSpace(asString(item)); value != "" {
			values = append(values, value)
		}
	}
	return strings.Join(values, ",")
}

// ---------------------------------------------------------------------------
// codebase_search
// ---------------------------------------------------------------------------

func (s *mcpServer) toolCodeRead(args map[string]any) toolResult {
	path := strings.TrimSpace(argString(args, "path", ""))
	repoPath := strings.TrimSpace(argString(args, "repoPath", ""))
	file := strings.TrimSpace(argString(args, "file", ""))
	if path == "" && repoPath != "" && file != "" {
		if !filepath.IsLocal(file) {
			return errorToolResult("file must be a local repository-relative path", nil)
		}
		path = filepath.Join(repoPath, file)
	}
	if path == "" {
		return errorToolResult("path is required, or repoPath + file", nil)
	}
	root, relative, err := s.localPath(path)
	if err != nil {
		return errorToolResult(err.Error(), nil)
	}
	info, err := root.Stat(relative)
	if err != nil || !info.Mode().IsRegular() {
		return errorToolResult("source must be an accessible regular file within an allowed root", nil)
	}
	const maxSourceBytes = 16 << 20
	if info.Size() > maxSourceBytes {
		return errorToolResult("code_read supports source files up to 16 MiB", nil)
	}
	source, err := root.OpenFile(relative, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	path = filepath.Join(root.Name(), relative)

	maxBytes := argInt(args, "maxBytes", 200000)
	if maxBytes <= 0 || maxBytes > 1000000 {
		maxBytes = 200000
	}
	if err != nil {
		return errorToolResult(fmt.Sprintf("code read failed: %v", err), nil)
	}
	defer source.Close()
	info, err = source.Stat()
	if err != nil {
		return errorToolResult(fmt.Sprintf("stat source failed: %v", err), nil)
	}
	if !info.Mode().IsRegular() {
		return errorToolResult("source must be a regular file", nil)
	}
	startLine := argInt(args, "startLine", 1)
	endLine := argInt(args, "endLine", 0)
	if startLine < 1 {
		startLine = 1
	}
	if endLine > 0 && startLine > endLine {
		return errorToolResult("startLine must be <= endLine", nil)
	}

	// Stream past earlier lines; the byte limit applies to the selected range.
	reader := bufio.NewReader(io.LimitReader(source, maxSourceBytes+1))
	scannedBytes := 0
	var content strings.Builder
	totalLines := 1
	truncated := false
	for {
		fragment, readErr := reader.ReadSlice('\n')
		scannedBytes += len(fragment)
		if scannedBytes > maxSourceBytes {
			return errorToolResult("source exceeded the 16 MiB read limit", nil)
		}
		newline := len(fragment) > 0 && fragment[len(fragment)-1] == '\n'
		if totalLines >= startLine && (endLine <= 0 || totalLines <= endLine) {
			selected := fragment
			if newline && totalLines == endLine {
				selected = selected[:len(selected)-1]
			}
			remaining := maxBytes - content.Len()
			if len(selected) > remaining {
				selected = selected[:remaining]
				truncated = true
			}
			content.Write(selected)
		}
		if newline {
			totalLines++
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil && readErr != bufio.ErrBufferFull {
			return errorToolResult(fmt.Sprintf("code read failed: %v", readErr), nil)
		}
	}
	if endLine <= 0 || endLine > totalLines {
		endLine = totalLines
	}
	if startLine > endLine {
		return errorToolResult("startLine must be <= endLine", map[string]any{"startLine": startLine, "endLine": endLine})
	}
	selected := content.String()
	text := fmt.Sprintf("%s:%d-%d\n%s", path, startLine, endLine, selected)
	if truncated {
		text += "\n\n[truncated by maxBytes]"
	}
	return successToolResult(text, map[string]any{
		"path":       path,
		"startLine":  startLine,
		"endLine":    endLine,
		"totalLines": totalLines,
		"truncated":  truncated,
		"content":    selected,
	})
}

func (s *mcpServer) toolGitDiff(args map[string]any) toolResult {
	repoPath := strings.TrimSpace(argString(args, "repoPath", ""))
	if repoPath == "" {
		repoPath = strings.TrimSpace(argString(args, "workspace", ""))
	}
	if repoPath == "" {
		return errorToolResult("repoPath is required", nil)
	}
	var err error
	repoPath, err = s.gitRepository(repoPath)
	if err != nil {
		return errorToolResult(err.Error(), nil)
	}
	maxBytes := argInt(args, "maxBytes", 500000)
	if maxBytes <= 0 || maxBytes > 2000000 {
		maxBytes = 500000
	}

	gitArgs := []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--submodule=short"}
	if argBool(args, "statOnly", false) {
		gitArgs = append(gitArgs, "--stat")
	}
	if argBool(args, "staged", false) {
		gitArgs = append(gitArgs, "--cached")
	}
	if baseRef := strings.TrimSpace(argString(args, "baseRef", "")); baseRef != "" {
		if strings.HasPrefix(baseRef, "-") {
			return errorToolResult("baseRef must be a revision, not a Git option", nil)
		}
		gitArgs = append(gitArgs, baseRef)
	}
	gitArgs = append(gitArgs, "--")
	if files := argStringSlice(args["files"]); len(files) > 0 {
		for _, file := range files {
			if !filepath.IsLocal(file) || strings.HasPrefix(file, ":") {
				return errorToolResult("files must be literal repository-relative paths", nil)
			}
		}
		gitArgs = append(gitArgs, files...)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := localGitCommand(ctx, repoPath, gitArgs...)
	output := limitedOutput{limit: maxBytes}
	diagnostics := limitedOutput{limit: maxBytes}
	cmd.Stdout = &output
	cmd.Stderr = &diagnostics
	err = cmd.Run()
	out := output.buffer.Bytes()
	truncated := output.truncated
	if err != nil {
		return errorToolResult(fmt.Sprintf("git diff failed: %v\n%s", err, strings.TrimSpace(diagnostics.buffer.String())), map[string]any{
			"repoPath": repoPath,
			"args":     gitArgs,
		})
	}
	text := string(out)
	if strings.TrimSpace(text) == "" {
		text = "No diff."
	}
	if truncated {
		text += "\n\n[truncated by maxBytes]"
	}
	if diagnostics.buffer.Len() > 0 {
		text += "\n\nGit diagnostics:\n" + diagnostics.buffer.String()
	}
	return successToolResult(text, map[string]any{
		"repoPath":             repoPath,
		"args":                 gitArgs,
		"truncated":            truncated,
		"diff":                 string(out),
		"diagnostics":          diagnostics.buffer.String(),
		"diagnosticsTruncated": diagnostics.truncated,
	})
}

// Drain subprocess output while retaining only the caller's requested prefix.
type limitedOutput struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := w.limit - w.buffer.Len()
	if remaining < len(p) {
		p = p[:remaining]
		w.truncated = true
	}
	w.buffer.Write(p)
	return n, nil
}

func (s *mcpServer) localGitDiff() (string, error) {
	if strings.TrimSpace(s.workdir) == "" {
		return "", fmt.Errorf("automatic local diff requires TIRION_WORKDIR; alternatively provide diff explicitly")
	}
	repoPath, err := s.gitRepository(s.workdir)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := localGitCommand(ctx, repoPath, "diff", "--no-color", "--no-ext-diff", "--no-textconv", "--submodule=short", "--unified=0", "--")
	output := limitedOutput{limit: 2 << 20}
	diagnostics := limitedOutput{limit: 8192}
	cmd.Stdout = &output
	cmd.Stderr = &diagnostics
	if err = cmd.Run(); err != nil {
		return "", fmt.Errorf("git diff failed: %w: %s", err, diagnostics.buffer.String())
	}
	if output.truncated {
		return "", fmt.Errorf("local diff exceeds 2 MiB; provide a scoped diff explicitly")
	}
	return output.buffer.String(), nil
}

func (s *mcpServer) toolSearch(args map[string]any) toolResult {
	query := strings.TrimSpace(argString(args, "query", ""))
	if query == "" {
		return errorToolResult("query is required", nil)
	}
	workspaceID := s.workspaceForArgs(args)

	params := map[string]string{
		"q":       query,
		"sort":    "richness",
		"noNoise": strconv.FormatBool(argBool(args, "noNoise", true)),
	}
	if repo := strings.TrimSpace(argString(args, "repo", "")); repo != "" {
		params["repo"] = repo
	}
	limit := argInt(args, "limit", 10)
	if limit <= 0 {
		limit = 10
	}
	params["limit"] = strconv.Itoa(minInt(limit, 50))
	if mode := strings.TrimSpace(argString(args, "mode", "")); mode != "" {
		params["mode"] = mode
	}
	addWorkspaceParam(params, workspaceID)

	var resp map[string]any
	if err := s.getJSON("/search", params, &resp); err != nil {
		return errorToolResult(fmt.Sprintf("search failed: %v", err), nil)
	}

	// Build text summary
	var sb strings.Builder
	results, _ := resp["results"].(map[string]any)
	stats, _ := resp["stats"].(map[string]any)
	buckets, _ := stats["buckets"].(map[string]any)

	funcs := anySlice(results["functions"])
	classes := anySlice(results["classes"])
	typeSymbols := anySlice(results["typeSymbols"])
	endpoints := anySlice(results["endpoints"])
	schedules := anySlice(results["schedules"])
	dataEntities := anySlice(results["dataEntities"])
	graphqlOps := anySlice(results["graphqlOperations"])
	azureTriggers := anySlice(results["azureTriggers"])
	queueHits := anySlice(results["queueHits"])
	fmt.Fprintf(&sb, "Search \"%s\": %d functions, %d classes, %d typeSymbols, %d endpoints, %d schedules",
		query, len(funcs), len(classes), len(typeSymbols), len(endpoints), len(schedules))
	if len(dataEntities) > 0 {
		fmt.Fprintf(&sb, ", %d dataEntities", len(dataEntities))
	}
	if len(graphqlOps) > 0 {
		fmt.Fprintf(&sb, ", %d graphqlOps", len(graphqlOps))
	}
	if len(azureTriggers) > 0 {
		fmt.Fprintf(&sb, ", %d azureTriggers", len(azureTriggers))
	}
	if len(queueHits) > 0 {
		fmt.Fprintf(&sb, ", %d queueHits", len(queueHits))
	}
	sb.WriteString("\n")
	appendWorkspaceLine(&sb, resp)
	appendWorkspaceHints(&sb, resp)

	if len(funcs) > 0 {
		sb.WriteString("\nTop functions:\n")
		limit := len(funcs)
		if limit > 8 {
			limit = 8
		}
		for i, f := range funcs[:limit] {
			m, _ := f.(map[string]any)
			name := asString(m["name"])
			repo := asString(m["repo"])
			file := asString(m["file"])
			callerID := asString(m["callerId"])
			startLine := asNumber(m["startLine"])
			endLine := asNumber(m["endLine"])
			richness := asNumber(m["richness"])
			tagsStr := ""
			if t := anySlice(m["tags"]); len(t) > 0 {
				tagsStr = " tags=" + strings.Join(toStringSlice(t), ",")
			}
			lineRange := ""
			if startLine > 0 {
				lineRange = fmt.Sprintf(" L%d-%d", startLine, endLine)
			}
			idStr := ""
			if callerID != "" {
				idStr = fmt.Sprintf(" id=%s", callerID)
			}
			fmt.Fprintf(&sb, "  %d. %s  [repo=%s file=%s%s%s richness=%d%s]\n", i+1, name, repo, file, lineRange, idStr, richness, tagsStr)
		}
		if len(funcs) > limit {
			fmt.Fprintf(&sb, "  ... and %d more functions\n", len(funcs)-limit)
		}
	}

	if len(classes) > 0 {
		sb.WriteString("\nTop classes:\n")
		limit := len(classes)
		if limit > 5 {
			limit = 5
		}
		for i, c := range classes[:limit] {
			m, _ := c.(map[string]any)
			name := asString(m["name"])
			repo := asString(m["repo"])
			file := asString(m["file"])
			fmt.Fprintf(&sb, "  %d. %s  [repo=%s file=%s]\n", i+1, name, repo, file)
		}
	}

	if len(typeSymbols) > 0 {
		sb.WriteString("\nType/interface symbols:\n")
		limit := len(typeSymbols)
		if limit > 5 {
			limit = 5
		}
		for i, t := range typeSymbols[:limit] {
			m, _ := t.(map[string]any)
			name := asString(m["name"])
			kind := asString(m["kind"])
			repo := asString(m["repo"])
			file := asString(m["file"])
			line := asNumber(m["line"])
			locStr := ""
			if file != "" {
				locStr = fmt.Sprintf(" %s:%d", file, line)
			}
			fmt.Fprintf(&sb, "  %d. %s  [kind=%s repo=%s%s]\n", i+1, name, kind, repo, locStr)
		}
	}

	if len(endpoints) > 0 {
		sb.WriteString("\nTop endpoints:\n")
		limit := len(endpoints)
		if limit > 5 {
			limit = 5
		}
		for i, e := range endpoints[:limit] {
			m, _ := e.(map[string]any)
			method := asString(m["method"])
			path := asString(m["path"])
			handler := asString(m["handler"])
			repo := asString(m["repo"])
			file := asString(m["file"])
			line := asNumber(m["line"])
			locStr := ""
			if file != "" {
				locStr = fmt.Sprintf(" %s:%d", file, line)
			}
			fmt.Fprintf(&sb, "  %d. %s %s → %s  [repo=%s%s]\n", i+1, method, path, handler, repo, locStr)
		}
	}

	if len(dataEntities) > 0 {
		sb.WriteString("\nData entities:\n")
		limit := len(dataEntities)
		if limit > 5 {
			limit = 5
		}
		for i, d := range dataEntities[:limit] {
			m, _ := d.(map[string]any)
			name := asString(m["name"])
			repo := asString(m["repo"])
			file := asString(m["file"])
			access := asString(m["access"])
			fmt.Fprintf(&sb, "  %d. %s  [access=%s repo=%s file=%s]\n", i+1, name, access, repo, file)
		}
	}

	if len(queueHits) > 0 {
		sb.WriteString("\nQueue hits:\n")
		limit := len(queueHits)
		if limit > 5 {
			limit = 5
		}
		for i, q := range queueHits[:limit] {
			m, _ := q.(map[string]any)
			name := asString(m["name"])
			queue := asString(m["queue"])
			role := asString(m["role"])
			repo := asString(m["repo"])
			file := asString(m["file"])
			line := asNumber(m["startLine"])
			locStr := ""
			if file != "" {
				locStr = fmt.Sprintf(" %s:%d", file, line)
			}
			fmt.Fprintf(&sb, "  %d. %s  [queue=%s role=%s repo=%s%s]\n", i+1, name, queue, role, repo, locStr)
		}
	}

	if len(schedules) > 0 {
		sb.WriteString("\nSchedules:\n")
		limit := len(schedules)
		if limit > 5 {
			limit = 5
		}
		for i, s := range schedules[:limit] {
			m, _ := s.(map[string]any)
			ruleName := asString(m["ruleName"])
			expr := asString(m["scheduleExpression"])
			targetType := asString(m["targetType"])
			targetName := asString(m["targetName"])
			state := asString(m["state"])
			fmt.Fprintf(&sb, "  %d. %s  %s → %s:%s  [%s]\n", i+1, ruleName, expr, strings.ToUpper(targetType), targetName, state)
		}
	}

	if len(graphqlOps) > 0 {
		sb.WriteString("\nGraphQL operations:\n")
		limit := len(graphqlOps)
		if limit > 5 {
			limit = 5
		}
		for i, g := range graphqlOps[:limit] {
			m, _ := g.(map[string]any)
			name := asString(m["name"])
			opType := asString(m["operationType"])
			repo := asString(m["repo"])
			fmt.Fprintf(&sb, "  %d. %s  [type=%s repo=%s]\n", i+1, name, opType, repo)
		}
	}

	if len(azureTriggers) > 0 {
		sb.WriteString("\nAzure triggers:\n")
		limit := len(azureTriggers)
		if limit > 5 {
			limit = 5
		}
		for i, a := range azureTriggers[:limit] {
			m, _ := a.(map[string]any)
			funcName := asString(m["functionName"])
			triggerType := asString(m["triggerType"])
			repo := asString(m["repo"])
			fmt.Fprintf(&sb, "  %d. %s  [trigger=%s repo=%s]\n", i+1, funcName, triggerType, repo)
		}
	}

	// Truncation summary from stats
	if buckets != nil {
		var truncLines []string
		for _, bk := range []struct{ key, label string }{
			{"functions", "functions"}, {"classes", "classes"}, {"endpoints", "endpoints"},
			{"typeSymbols", "typeSymbols"}, {"dataEntities", "dataEntities"}, {"schedules", "schedules"},
			{"graphqlOperations", "graphqlOps"}, {"azureTriggers", "azureTriggers"}, {"queueHits", "queueHits"},
		} {
			bm, _ := buckets[bk.key].(map[string]any)
			if bm == nil {
				continue
			}
			returned := asNumber(bm["returned"])
			total := asNumber(bm["total"])
			if total > returned {
				truncLines = append(truncLines, fmt.Sprintf("  %s: showing %d of %d", bk.label, returned, total))
			}
		}
		if len(truncLines) > 0 {
			sb.WriteString("\nTruncated results:\n")
			for _, tl := range truncLines {
				sb.WriteString(tl)
				sb.WriteString("\n")
			}
		}
	}

	return successToolResult(sb.String(), compactSearchStructuredPayload(resp))
}

// ---------------------------------------------------------------------------
// codebase_trace
// ---------------------------------------------------------------------------

func (s *mcpServer) toolTrace(args map[string]any) toolResult {
	function := strings.TrimSpace(argString(args, "function", ""))
	if function == "" {
		return errorToolResult("function is required", nil)
	}

	req := map[string]any{
		"function":      function,
		"depth":         argInt(args, "depth", 4),
		"noTests":       argBool(args, "noTests", true),
		"maxNodes":      argInt(args, "maxNodes", 2000),
		"includeSource": false,
	}
	addWorkspaceBody(req, s.workspaceForArgs(args))
	if resolve := args["resolve"]; resolve != nil {
		req["resolve"] = argBool(args, "resolve", false)
	}

	var resp map[string]any
	if err := s.postJSON("/trace", req, &resp); err != nil {
		return errorToolResult(fmt.Sprintf("trace failed: %v", err), nil)
	}

	// Handle graceful not-found
	if notFound, _ := resp["notFound"].(bool); notFound {
		var sb strings.Builder
		fmt.Fprintf(&sb, "Function %q not found.\n", function)
		appendWorkspaceLine(&sb, resp)
		appendWorkspaceHints(&sb, resp)
		if suggestions := anySlice(resp["suggestions"]); len(suggestions) > 0 {
			sb.WriteString("Did you mean:\n")
			for i, s := range suggestions {
				fmt.Fprintf(&sb, "  %d. %s\n", i+1, asString(s))
			}
		}
		return successToolResult(sb.String(), resp)
	}

	// Build text summary
	var sb strings.Builder
	matches := anySlice(resp["matches"])
	downstream := anySlice(resp["downstream"])
	upstream := anySlice(resp["upstream"])
	stats, _ := resp["stats"].(map[string]any)

	downCount := asNumber(stats["downstreamNodes"])
	upCount := asNumber(stats["upstreamNodes"])

	fmt.Fprintf(&sb, "Trace \"%s\": %d matches, %d downstream nodes, %d upstream nodes\n",
		function, len(matches), downCount, upCount)
	appendWorkspaceLine(&sb, resp)
	appendWorkspaceHints(&sb, resp)

	if len(matches) > 0 {
		sb.WriteString("\nMatched functions:\n")
		for i, m := range matches {
			match, _ := m.(map[string]any)
			name := asString(match["name"])
			repo := asString(match["repo"])
			file := asString(match["file"])
			qid := asString(match["qualified_id"])
			idStr := ""
			if qid != "" {
				idStr = fmt.Sprintf(" id=%s", qid)
			}
			fmt.Fprintf(&sb, "  %d. %s  [repo=%s file=%s%s]\n", i+1, name, repo, file, idStr)
		}
	}

	if len(downstream) > 0 {
		sb.WriteString("\nDownstream call tree:\n")
		for _, node := range downstream {
			formatTreeNode(&sb, node, 1)
		}
	}

	if len(upstream) > 0 {
		sb.WriteString("\nUpstream callers:\n")
		for _, node := range upstream {
			formatTreeNode(&sb, node, 1)
		}
	}

	// Completeness
	if completeness, _ := resp["completeness"].(map[string]any); completeness != nil {
		truncated := asBool(completeness["truncated"])
		ds, _ := completeness["downstream"].(map[string]any)
		us, _ := completeness["upstream"].(map[string]any)
		fmt.Fprintf(&sb, "\nCompleteness: truncated=%v", truncated)
		if ds != nil {
			fmt.Fprintf(&sb, ", downstream=%d/%d", asNumber(ds["returnedNodes"]), asNumber(ds["availableNodes"]))
		}
		if us != nil {
			fmt.Fprintf(&sb, ", upstream=%d/%d", asNumber(us["returnedNodes"]), asNumber(us["availableNodes"]))
		}
		if maxN := asNumber(completeness["appliedMaxNodes"]); maxN > 0 {
			fmt.Fprintf(&sb, ", maxNodes=%d", maxN)
		}
		sb.WriteString("\n")
	}

	// Quality
	if quality, _ := resp["quality"].(map[string]any); quality != nil {
		ds, _ := quality["downstream"].(map[string]any)
		if ds != nil {
			crossService := asNumber(ds["crossServiceCount"])
			confCounts, _ := ds["confidenceCounts"].(map[string]any)
			high := asNumber(confCounts["high"])
			medium := asNumber(confCounts["medium"])
			low := asNumber(confCounts["low"])
			fmt.Fprintf(&sb, "Quality: %d cross-service edges, confidence: high=%d medium=%d low=%d\n",
				crossService, high, medium, low)
		}
	}

	// Warnings
	if warnings := anySlice(resp["warnings"]); len(warnings) > 0 {
		sb.WriteString("Warnings:\n")
		for _, w := range warnings {
			fmt.Fprintf(&sb, "  - %s\n", asString(w))
		}
	}

	return successToolResult(sb.String(), resp)
}

func formatTreeNode(sb *strings.Builder, node any, indent int) {
	m, ok := node.(map[string]any)
	if !ok {
		return
	}
	prefix := strings.Repeat("  ", indent)
	name := asString(m["name"])
	repo := asString(m["repo"])
	edgeType := asString(m["edge_type"])
	confidence := asString(m["confidence"])
	callerID := asString(m["caller_id"])
	httpMethod := asString(m["http_method"])
	httpTarget := asString(m["http_target"])
	queueTarget := asString(m["queue_target"])
	injected := asBool(m["injected"])
	isSqs := asBool(m["is_sqs"])

	// Build display name based on edge type
	displayName := name
	if (edgeType == "http" || edgeType == "resolve") && httpMethod != "" && httpTarget != "" {
		displayName = fmt.Sprintf("[%s %s]", httpMethod, httpTarget)
	} else if (isSqs || edgeType == "sqs") && queueTarget != "" {
		displayName = fmt.Sprintf("[queue: %s]", queueTarget)
	} else if edgeType == "eventbridge" {
		displayName = fmt.Sprintf("[EventBridge: %s]", name)
	}

	// Build edge annotation
	edgeAnnotation := ""
	if edgeType != "" {
		confStr := ""
		if confidence != "" && confidence != "high" {
			confStr = fmt.Sprintf(", %s-confidence", confidence)
		}
		edgeAnnotation = fmt.Sprintf(" (%s%s)", edgeType, confStr)
	}

	// Build optional markers
	extra := ""
	if injected {
		extra += " (injected)"
	}
	if indent <= 2 && callerID != "" {
		extra += fmt.Sprintf(" id=%s", callerID)
	}

	if repo != "" {
		fmt.Fprintf(sb, "%s→ %s [%s]%s%s\n", prefix, displayName, repo, edgeAnnotation, extra)
	} else {
		fmt.Fprintf(sb, "%s→ %s%s%s\n", prefix, displayName, edgeAnnotation, extra)
	}

	children := anySlice(m["children"])
	if indent < 6 {
		for _, child := range children {
			formatTreeNode(sb, child, indent+1)
		}
	} else if len(children) > 0 {
		fmt.Fprintf(sb, "%s  ... %d more children\n", prefix, len(children))
	}
}

// ---------------------------------------------------------------------------
// codebase_flow
// ---------------------------------------------------------------------------

func (s *mcpServer) toolFlow(args map[string]any) toolResult {
	start := strings.TrimSpace(argString(args, "start", ""))
	if start == "" {
		return errorToolResult("start is required", nil)
	}

	req := map[string]any{
		"start":   start,
		"depth":   argInt(args, "depth", 3),
		"maxHops": argInt(args, "maxHops", 200),
		"noTests": argBool(args, "noTests", true),
	}
	addWorkspaceBody(req, s.workspaceForArgs(args))

	var resp map[string]any
	if err := s.postJSON("/flow", req, &resp); err != nil {
		return errorToolResult(fmt.Sprintf("flow failed: %v", err), nil)
	}

	// Build text summary
	var sb strings.Builder
	roots := anySlice(resp["roots"])
	hops := anySlice(resp["hops"])
	narratives := anySlice(resp["narratives"])
	callers := anySlice(resp["callers"])
	stats, _ := resp["stats"].(map[string]any)

	fmt.Fprintf(&sb, "Flow \"%s\": %d roots, %d hops, %d narratives\n",
		start, len(roots), len(hops), len(narratives))
	appendWorkspaceLine(&sb, resp)
	appendWorkspaceHints(&sb, resp)

	if len(roots) > 0 {
		sb.WriteString("\nEntry points:\n")
		limit := len(roots)
		if limit > 8 {
			limit = 8
		}
		for i, r := range roots[:limit] {
			root, _ := r.(map[string]any)
			handler := asString(root["handler"])
			repo := asString(root["repo"])
			file := asString(root["file"])
			line := asNumber(root["line"])
			method := asString(root["method"])
			path := asString(root["path"])
			locStr := ""
			if file != "" {
				locStr = fmt.Sprintf(" %s:%d", file, line)
			}
			if method != "" && path != "" {
				fmt.Fprintf(&sb, "  %d. %s %s → %s [%s%s]\n", i+1, method, path, handler, repo, locStr)
			} else {
				fmt.Fprintf(&sb, "  %d. %s [%s%s]\n", i+1, handler, repo, locStr)
			}
		}
	}

	if len(hops) > 0 {
		sb.WriteString("\nMain hops:\n")
		limit := len(hops)
		if limit > 20 {
			limit = 20
		}
		for i := 0; i < limit; i++ {
			hop, _ := hops[i].(map[string]any)
			from, _ := hop["from"].(map[string]any)
			to, _ := hop["to"].(map[string]any)
			via, _ := hop["via"].(map[string]any)

			fromName := asString(from["handler"])
			fromRepo := asString(from["repo"])
			toName := asString(to["handler"])
			toRepo := asString(to["repo"])

			viaStr := buildViaString(via)

			fmt.Fprintf(&sb, "  %d. [%s] %s —(%s)→ [%s] %s\n",
				i+1, fromRepo, fromName, viaStr, toRepo, toName)
		}
		if len(hops) > limit {
			fmt.Fprintf(&sb, "  ... and %d more hops\n", len(hops)-limit)
		}
	}

	if candidates := anySlice(resp["candidates"]); len(candidates) > 0 {
		sb.WriteString("\nEndpoint discovery candidates (not established data-flow hops):\n")
		for _, item := range candidates {
			candidate, _ := item.(map[string]any)
			endpoint, _ := candidate["endpoint"].(map[string]any)
			fmt.Fprintf(&sb, "  - %s: %s %s [%s %s:%d] %s\n",
				asString(candidate["entity"]), asString(endpoint["method"]), asString(endpoint["path"]),
				asString(endpoint["repo"]), asString(endpoint["file"]), asNumber(endpoint["line"]), asString(candidate["reason"]))
		}
	}

	// Narratives
	if len(narratives) > 0 {
		sb.WriteString("\nNarratives:\n")
		narLimit := len(narratives)
		if narLimit > 3 {
			narLimit = 3
		}
		for ni, n := range narratives[:narLimit] {
			nar, _ := n.(map[string]any)
			narRoot, _ := nar["root"].(map[string]any)
			rootHandler := asString(narRoot["handler"])
			rootRepo := asString(narRoot["repo"])
			fmt.Fprintf(&sb, "  [%d] %s [%s]:\n", ni+1, rootHandler, rootRepo)
			steps := anySlice(nar["steps"])
			stepLimit := len(steps)
			if stepLimit > 8 {
				stepLimit = 8
			}
			for si, s := range steps[:stepLimit] {
				step, _ := s.(map[string]any)
				kind := asString(step["kind"])
				stepFrom, _ := step["from"].(map[string]any)
				stepTo, _ := step["to"].(map[string]any)
				stepVia, _ := step["via"].(map[string]any)
				conf := asString(step["confidence"])

				fromName := asString(stepFrom["handler"])
				fromRepo := asString(stepFrom["repo"])
				toName := asString(stepTo["handler"])
				toRepo := asString(stepTo["repo"])

				viaStr := ""
				if stepVia != nil {
					viaStr = buildViaString(stepVia)
				}

				confStr := ""
				if conf != "" && conf != "exact" {
					confStr = fmt.Sprintf(" (%s)", conf)
				}

				if viaStr != "" {
					fmt.Fprintf(&sb, "    %d. [%s] [%s] %s —(%s)→ [%s] %s%s\n",
						si+1, kind, fromRepo, fromName, viaStr, toRepo, toName, confStr)
				} else {
					fmt.Fprintf(&sb, "    %d. [%s] [%s] %s → [%s] %s%s\n",
						si+1, kind, fromRepo, fromName, toRepo, toName, confStr)
				}
			}
			if len(steps) > stepLimit {
				fmt.Fprintf(&sb, "    ... and %d more steps\n", len(steps)-stepLimit)
			}
		}
		if len(narratives) > narLimit {
			fmt.Fprintf(&sb, "  ... and %d more narratives\n", len(narratives)-narLimit)
		}
	}

	// Callers
	if len(callers) > 0 {
		sb.WriteString("\nHTTP callers into these endpoints:\n")
		callerLimit := len(callers)
		if callerLimit > 5 {
			callerLimit = 5
		}
		for i, c := range callers[:callerLimit] {
			cm, _ := c.(map[string]any)
			method := asString(cm["method"])
			path := asString(cm["path"])
			handler := asString(cm["handler"])
			repo := asString(cm["repo"])
			if method != "" && path != "" {
				fmt.Fprintf(&sb, "  %d. %s %s → %s [%s]\n", i+1, method, path, handler, repo)
			} else {
				fmt.Fprintf(&sb, "  %d. %s [%s]\n", i+1, handler, repo)
			}
		}
	}

	// Completeness
	if completeness, _ := resp["completeness"].(map[string]any); completeness != nil {
		truncated := asBool(completeness["truncated"])
		returned := asNumber(completeness["returnedHops"])
		available := asNumber(completeness["availableHops"])
		if truncated || available > returned {
			fmt.Fprintf(&sb, "\nCompleteness: showing %d of %d hops, truncated=%v\n", returned, available, truncated)
		}
	}

	if stats != nil {
		fmt.Fprintf(&sb, "Stats: hops=%v roots=%v duration=%v\n",
			stats["hops"], stats["roots"], stats["duration"])
	}

	// Warnings
	if warnings := anySlice(resp["warnings"]); len(warnings) > 0 {
		sb.WriteString("Warnings:\n")
		for _, w := range warnings {
			fmt.Fprintf(&sb, "  - %s\n", asString(w))
		}
	}

	return successToolResult(sb.String(), resp)
}

func (s *mcpServer) toolGraphQLFlow(args map[string]any) toolResult {
	operation := strings.TrimSpace(argString(args, "operation", ""))
	if operation == "" {
		return errorToolResult("operation is required", nil)
	}

	limit := argInt(args, "limit", 25)
	if limit < 1 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}

	params := map[string]string{
		"operation": operation,
		"limit":     strconv.Itoa(limit),
	}
	if frontendRepo := strings.TrimSpace(argString(args, "frontendRepo", "")); frontendRepo != "" {
		params["frontendRepo"] = frontendRepo
	}
	if backendRepo := strings.TrimSpace(argString(args, "backendRepo", "")); backendRepo != "" {
		params["backendRepo"] = backendRepo
	}
	addWorkspaceParam(params, s.workspaceForArgs(args))

	var resp map[string]any
	if err := s.getJSON("/graphql/flow", params, &resp); err != nil {
		return errorToolResult(fmt.Sprintf("graphql flow failed: %v", err), nil)
	}

	var sb strings.Builder
	operations := anySlice(resp["operations"])
	usages := anySlice(resp["usages"])
	resolvers := anySlice(resp["resolvers"])
	entrypoints := anySlice(resp["entrypoints"])
	controllers := anySlice(resp["controllers"])

	fmt.Fprintf(&sb, "GraphQL flow %q: %d operations, %d frontend usages, %d resolvers, %d backend entrypoints, %d controller files\n",
		operation, len(operations), len(usages), len(resolvers), len(entrypoints), len(controllers))
	appendWorkspaceLine(&sb, resp)
	appendWorkspaceHints(&sb, resp)

	if len(operations) > 0 {
		sb.WriteString("\nOperations:\n")
		limit := minInt(len(operations), 8)
		for i := 0; i < limit; i++ {
			op, _ := operations[i].(map[string]any)
			name := asString(op["operation_name"])
			opType := asString(op["operation_type"])
			repo := asString(op["repo_name"])
			file := asString(op["file_path"])
			line := asNumber(op["line"])
			fmt.Fprintf(&sb, "  %d. %s %s [%s %s:%d]\n", i+1, opType, name, repo, file, line)
		}
		if len(operations) > limit {
			fmt.Fprintf(&sb, "  ... and %d more operations\n", len(operations)-limit)
		}
	}

	if len(usages) > 0 {
		sb.WriteString("\nFrontend usages:\n")
		limit := minInt(len(usages), 8)
		for i := 0; i < limit; i++ {
			usage, _ := usages[i].(map[string]any)
			importedAs := asString(usage["imported_as"])
			resolved := asString(usage["resolved_operation_name"])
			repo := asString(usage["repo_name"])
			file := asString(usage["file_path"])
			line := asNumber(usage["line"])
			caller := asString(usage["caller_function"])
			if resolved != "" && resolved != importedAs {
				importedAs = fmt.Sprintf("%s -> %s", importedAs, resolved)
			}
			if caller != "" {
				fmt.Fprintf(&sb, "  %d. %s used by %s [%s %s:%d]\n", i+1, importedAs, caller, repo, file, line)
			} else {
				fmt.Fprintf(&sb, "  %d. %s [%s %s:%d]\n", i+1, importedAs, repo, file, line)
			}
		}
		if len(usages) > limit {
			fmt.Fprintf(&sb, "  ... and %d more frontend usages\n", len(usages)-limit)
		}
	}

	if len(resolvers) > 0 {
		sb.WriteString("\nBackend resolvers:\n")
		limit := minInt(len(resolvers), 12)
		for i := 0; i < limit; i++ {
			resolver, _ := resolvers[i].(map[string]any)
			name := asString(resolver["operation_name"])
			opType := asString(resolver["operation_type"])
			resolverName := asString(resolver["resolver_name"])
			repo := asString(resolver["repo_name"])
			file := asString(resolver["file_path"])
			line := asNumber(resolver["line"])
			display := firstNonEmptyString(resolverName, name)
			fmt.Fprintf(&sb, "  %d. %s %s [%s %s:%d]\n", i+1, opType, display, repo, file, line)
		}
		if len(resolvers) > limit {
			fmt.Fprintf(&sb, "  ... and %d more backend resolvers\n", len(resolvers)-limit)
		}
	}

	if len(entrypoints) > 0 {
		sb.WriteString("\nBackend entrypoints:\n")
		limit := minInt(len(entrypoints), 8)
		for i := 0; i < limit; i++ {
			entry, _ := entrypoints[i].(map[string]any)
			handler := asString(entry["handler_name"])
			repo := asString(entry["repo_name"])
			file := asString(entry["file_path"])
			conf := asString(entry["match_confidence"])
			if conf != "" {
				fmt.Fprintf(&sb, "  %d. %s [%s %s confidence=%s]\n", i+1, handler, repo, file, conf)
			} else {
				fmt.Fprintf(&sb, "  %d. %s [%s %s]\n", i+1, handler, repo, file)
			}
		}
		if len(entrypoints) > limit {
			fmt.Fprintf(&sb, "  ... and %d more backend entrypoints\n", len(entrypoints)-limit)
		}
	}

	if len(controllers) > 0 {
		sb.WriteString("\nController files:\n")
		limit := minInt(len(controllers), 12)
		for i := 0; i < limit; i++ {
			controller, _ := controllers[i].(map[string]any)
			repo := asString(controller["repo_name"])
			file := asString(controller["controller_file_path"])
			conf := asString(controller["resolution_confidence"])
			if conf != "" {
				fmt.Fprintf(&sb, "  %d. %s [%s confidence=%s]\n", i+1, file, repo, conf)
			} else {
				fmt.Fprintf(&sb, "  %d. %s [%s]\n", i+1, file, repo)
			}
		}
		if len(controllers) > limit {
			fmt.Fprintf(&sb, "  ... and %d more controller files\n", len(controllers)-limit)
		}
	}

	if inferences := anySlice(resp["inferences"]); len(inferences) > 0 {
		sb.WriteString("\nInferences:\n")
		for _, inference := range inferences {
			fmt.Fprintf(&sb, "  - %s\n", asString(inference))
		}
	}

	return successToolResult(sb.String(), resp)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func buildViaString(via map[string]any) string {
	if via == nil {
		return ""
	}
	viaType := asString(via["type"])
	viaMethod := asString(via["method"])
	viaPath := asString(via["path"])
	viaQueue := asString(via["queue"])
	viaEntity := asString(via["entity"])

	switch viaType {
	case "http":
		if viaMethod != "" && viaPath != "" {
			return fmt.Sprintf("http %s %s", viaMethod, viaPath)
		}
		return "http"
	case "sqs":
		if viaQueue != "" {
			return fmt.Sprintf("sqs %s", viaQueue)
		}
		return "sqs"
	case "data":
		if viaEntity != "" {
			return fmt.Sprintf("data %s", viaEntity)
		}
		return "data"
	default:
		return viaType
	}
}

// ---------------------------------------------------------------------------
// codebase_impact
// ---------------------------------------------------------------------------

func (s *mcpServer) toolImpact(args map[string]any) toolResult {
	req := map[string]any{
		"depth":    argInt(args, "depth", 4),
		"maxNodes": argInt(args, "maxNodes", 2000),
		"noTests":  argBool(args, "noTests", true),
	}
	addWorkspaceBody(req, s.workspaceForArgs(args))

	// At least one of functions, ranges, or files must be provided
	hasFunctions := false
	if funcs := argStringSlice(args["functions"]); len(funcs) > 0 {
		req["functions"] = funcs
		hasFunctions = true
	}

	hasRanges := false
	if ranges, ok := args["ranges"]; ok && ranges != nil {
		req["ranges"] = ranges
		hasRanges = true
	}

	hasFiles := false
	if files, ok := args["files"]; ok && files != nil {
		req["files"] = files
		hasFiles = true
	}

	hasDiff := false
	if diff := strings.TrimSpace(argString(args, "diff", "")); diff != "" {
		req["diff"] = diff
		hasDiff = true
	}
	if repo := strings.TrimSpace(argString(args, "repo", "")); repo != "" {
		req["repo"] = repo
	}

	if !hasFunctions && !hasRanges && !hasFiles && !hasDiff {
		return errorToolResult("at least one of functions, ranges, files, or diff is required", nil)
	}

	var resp map[string]any
	if err := s.postJSON("/impact", req, &resp); err != nil {
		return errorToolResult(fmt.Sprintf("impact failed: %v", err), nil)
	}

	// Build text summary
	var sb strings.Builder
	roots := anySlice(resp["roots"])
	rootDetails := anySlice(resp["rootDetails"])
	summary, _ := resp["summary"].(map[string]any)
	stats, _ := resp["stats"].(map[string]any)

	entrypoints := anySlice(summary["entrypoints"])
	httpCalls := anySlice(summary["httpCalls"])
	queues := anySlice(summary["queues"])
	dataAccesses := anySlice(summary["dataAccesses"])
	ebSchedules := anySlice(summary["eventBridgeSchedules"])
	azureSchedules := anySlice(summary["azureTimerSchedules"])
	repos := anySlice(summary["repos"])

	fmt.Fprintf(&sb, "Impact analysis: %d roots, %d entrypoints, %d HTTP calls, %d queues, %d data/side effects, %d EventBridge schedules, %d Azure timers, %d repos\n",
		len(roots), len(entrypoints), len(httpCalls), len(queues), len(dataAccesses), len(ebSchedules), len(azureSchedules), len(repos))
	appendWorkspaceLine(&sb, resp)
	appendWorkspaceHints(&sb, resp)

	if stats != nil {
		fmt.Fprintf(&sb, "Nodes: downstream=%v upstream=%v total=%v edges=%v duration=%v\n",
			stats["downstreamNodes"], stats["upstreamNodes"], stats["totalNodes"],
			stats["totalEdges"], stats["impactTime"])
	}
	appendVerifySummary(&sb, resp)

	if len(rootDetails) > 0 {
		sb.WriteString("\nRoots:\n")
		limit := len(rootDetails)
		if limit > 8 {
			limit = 8
		}
		for i := 0; i < limit; i++ {
			root, _ := rootDetails[i].(map[string]any)
			repo := asString(root["repo"])
			file := asString(root["file"])
			name := asString(root["name"])
			callerID := asString(root["callerId"])
			if repo != "" || file != "" || name != "" {
				fmt.Fprintf(&sb, "  %d. %s [%s %s]\n", i+1, name, repo, file)
			} else {
				fmt.Fprintf(&sb, "  %d. %s\n", i+1, callerID)
			}
		}
		if len(rootDetails) > limit {
			fmt.Fprintf(&sb, "  ... and %d more roots\n", len(rootDetails)-limit)
		}
	}

	if len(entrypoints) > 0 {
		sb.WriteString("\nAffected entrypoints:\n")
		limit := len(entrypoints)
		if limit > 20 {
			limit = 20
		}
		for i := 0; i < limit; i++ {
			ep, _ := entrypoints[i].(map[string]any)
			method := asString(ep["method"])
			path := asString(ep["path"])
			handler := asString(ep["handler"])
			repo := asString(ep["repo"])
			file := asString(ep["file"])
			line := asNumber(ep["line"])
			score := asFloat(ep["score"])
			minDepth := asNumber(ep["minDepth"])
			fanout := asNumber(ep["fanout"])
			relation := impactRelationLabel(ep)
			locStr := ""
			if file != "" {
				locStr = fmt.Sprintf(" %s:%d", file, line)
			}
			fmt.Fprintf(&sb, "  %d. %s %s → %s [%s%s %s score=%.2f depth=%d fanout=%d]\n",
				i+1, method, path, handler, repo, locStr, relation, score, minDepth, fanout)
		}
		if len(entrypoints) > limit {
			fmt.Fprintf(&sb, "  ... and %d more entrypoints\n", len(entrypoints)-limit)
		}
	}

	if len(repos) > 0 {
		sb.WriteString("\nRepositories touched:\n")
		for i, r := range repos {
			repo, _ := r.(map[string]any)
			name := asString(repo["name"])
			minDepth := asNumber(repo["minDepth"])
			fanout := asNumber(repo["fanout"])
			score := asFloat(repo["score"])
			relation := impactRelationLabel(repo)
			fmt.Fprintf(&sb, "  %d. %s  [%s depth=%d fanout=%d score=%.2f]\n", i+1, name, relation, minDepth, fanout, score)
		}
	}

	if len(httpCalls) > 0 {
		sb.WriteString("\nOutgoing HTTP calls:\n")
		for i, c := range httpCalls {
			call, _ := c.(map[string]any)
			method := asString(call["method"])
			path := asString(call["path"])
			score := asFloat(call["score"])
			relation := impactRelationLabel(call)
			// Show resolved target repos from matches
			matchTargets := ""
			if matches := anySlice(call["matches"]); len(matches) > 0 {
				var targets []string
				mlimit := len(matches)
				if mlimit > 3 {
					mlimit = 3
				}
				for _, mm := range matches[:mlimit] {
					mt, _ := mm.(map[string]any)
					targets = append(targets, asString(mt["repo"])+":"+asString(mt["handler"]))
				}
				matchTargets = " → " + strings.Join(targets, ", ")
				if len(matches) > 3 {
					matchTargets += fmt.Sprintf(" (+%d)", len(matches)-3)
				}
			}
			fmt.Fprintf(&sb, "  %d. %s %s  [%s score=%.2f]%s\n", i+1, method, path, relation, score, matchTargets)
		}
	}

	if len(queues) > 0 {
		sb.WriteString("\nQueues / Service Bus:\n")
		for i, q := range queues {
			queue, _ := q.(map[string]any)
			name := asString(queue["name"])
			score := asFloat(queue["score"])
			relation := impactRelationLabel(queue)
			fmt.Fprintf(&sb, "  %d. %s  [%s score=%.2f]\n", i+1, name, relation, score)
		}
	}

	if len(dataAccesses) > 0 {
		sb.WriteString("\nData / side-effect resources:\n")
		limit := len(dataAccesses)
		if limit > 20 {
			limit = 20
		}
		for i := 0; i < limit; i++ {
			access, _ := dataAccesses[i].(map[string]any)
			entity := asString(access["entity"])
			verb := asString(access["access"])
			score := asFloat(access["score"])
			minDepth := asNumber(access["minDepth"])
			fanout := asNumber(access["fanout"])
			relation := impactRelationLabel(access)
			fmt.Fprintf(&sb, "  %d. %s %s  [%s depth=%d fanout=%d score=%.2f]\n", i+1, verb, entity, relation, minDepth, fanout, score)
			if callers := anySlice(access["callers"]); len(callers) > 0 {
				climit := len(callers)
				if climit > 3 {
					climit = 3
				}
				var names []string
				for _, caller := range callers[:climit] {
					names = append(names, asString(caller))
				}
				fmt.Fprintf(&sb, "     callers: %s\n", strings.Join(names, ", "))
			}
		}
		if len(dataAccesses) > limit {
			fmt.Fprintf(&sb, "  ... and %d more data/side-effect resources\n", len(dataAccesses)-limit)
		}
	}

	if len(ebSchedules) > 0 {
		sb.WriteString("\nEventBridge schedules:\n")
		for i, s := range ebSchedules {
			sched, _ := s.(map[string]any)
			name := asString(sched["name"])
			expr := asString(sched["schedule"])
			state := asString(sched["state"])
			score := asFloat(sched["score"])
			relation := impactRelationLabel(sched)
			if expr != "" {
				fmt.Fprintf(&sb, "  %d. %s  %s [%s %s score=%.2f]\n", i+1, name, expr, state, relation, score)
			} else {
				fmt.Fprintf(&sb, "  %d. %s  [%s score=%.2f]\n", i+1, name, relation, score)
			}
		}
	}

	if len(azureSchedules) > 0 {
		sb.WriteString("\nAzure timer schedules:\n")
		for i, s := range azureSchedules {
			sched, _ := s.(map[string]any)
			name := asString(sched["name"])
			expr := asString(sched["schedule"])
			state := asString(sched["state"])
			score := asFloat(sched["score"])
			relation := impactRelationLabel(sched)
			if expr != "" {
				fmt.Fprintf(&sb, "  %d. %s  %s [%s %s score=%.2f]\n", i+1, name, expr, state, relation, score)
			} else {
				fmt.Fprintf(&sb, "  %d. %s  [%s score=%.2f]\n", i+1, name, relation, score)
			}
		}
	}

	// Completeness
	if completeness, _ := resp["completeness"].(map[string]any); completeness != nil {
		truncated := asBool(completeness["truncated"])
		maxNodes := asNumber(completeness["appliedMaxNodes"])
		ds, _ := completeness["downstream"].(map[string]any)
		us, _ := completeness["upstream"].(map[string]any)
		if truncated {
			fmt.Fprintf(&sb, "\nCompleteness: TRUNCATED, maxNodes=%d", maxNodes)
			if ds != nil {
				fmt.Fprintf(&sb, ", downstream=%d/%d", asNumber(ds["returnedNodes"]), asNumber(ds["availableNodes"]))
			}
			if us != nil {
				fmt.Fprintf(&sb, ", upstream=%d/%d", asNumber(us["returnedNodes"]), asNumber(us["availableNodes"]))
			}
			sb.WriteString("\n")
		}
	}

	// Warnings
	if warnings := anySlice(resp["warnings"]); len(warnings) > 0 {
		sb.WriteString("Warnings:\n")
		for _, w := range warnings {
			fmt.Fprintf(&sb, "  - %s\n", asString(w))
		}
	}

	return successToolResult(sb.String(), resp)
}

func (s *mcpServer) toolVerify(args map[string]any) toolResult {
	req := map[string]any{
		"depth":    argInt(args, "depth", 4),
		"maxNodes": argInt(args, "maxNodes", 2000),
		"noTests":  argBool(args, "noTests", true),
		"verify":   true,
	}
	addWorkspaceBody(req, s.workspaceForArgs(args))
	repo := strings.TrimSpace(argString(args, "repo", ""))
	if repo == "" {
		return errorToolResult("repo is required", nil)
	}
	req["repo"] = repo
	diff := argString(args, "diff", "")
	if strings.TrimSpace(diff) == "" {
		var err error
		diff, err = s.localGitDiff()
		if err != nil {
			return errorToolResult(fmt.Sprintf("local git diff failed: %v", err), nil)
		}
		if strings.TrimSpace(diff) == "" {
			return errorToolResult("diff is required and local git diff is empty", nil)
		}
	}
	req["diff"] = diff
	if baseSHA := strings.TrimSpace(argString(args, "baseSha", "")); baseSHA != "" {
		req["baseSha"] = baseSHA
	}
	if anchor := args["anchor"]; anchor != nil {
		req["anchor"] = anchor
	}
	if waivers := args["waivers"]; waivers != nil {
		req["waivers"] = waivers
	}
	if argBool(args, "requireAnchor", false) {
		req["requireAnchor"] = true
	}
	if task := strings.TrimSpace(argString(args, "task", "")); task != "" || args["rootCauseEvidence"] != nil || args["claims"] != nil {
		req["agentWork"] = map[string]any{
			"task":                 task,
			"baseSha":              strings.TrimSpace(argString(args, "baseSha", "")),
			"rootCause":            strings.TrimSpace(argString(args, "rootCause", "")),
			"requirementsComplete": argBool(args, "requirementsComplete", false),
			"rootCauseEvidence":    args["rootCauseEvidence"],
			"claims":               args["claims"],
		}
	}
	var resp map[string]any
	if err := s.postJSON("/verify", req, &resp); err != nil {
		return errorToolResult(fmt.Sprintf("verify failed: %v", err), nil)
	}
	var sb strings.Builder
	appendVerifySummary(&sb, resp)
	return successToolResult(strings.TrimSpace(sb.String()), compactMCPVerifyResponse(resp))
}

func compactMCPVerifyResponse(resp map[string]any) map[string]any {
	return map[string]any{
		"workspace":   resp["workspace"],
		"roots":       resp["roots"],
		"rootDetails": resp["rootDetails"],
		"stats":       resp["stats"],
		"verify":      resp["verify"],
		"warnings":    resp["warnings"],
	}
}

func appendVerifySummary(sb *strings.Builder, resp map[string]any) {
	verify, _ := resp["verify"].(map[string]any)
	if verify == nil {
		return
	}
	verdict := asString(verify["verdict"])
	breaking := anySlice(verify["breakingChanges"])
	reasons := anySlice(verify["reasons"])
	exposed, _ := verify["exposedSurface"].(map[string]any)
	endpoints := anySlice(exposed["endpoints"])
	queues := anySlice(exposed["queues"])
	entities := anySlice(exposed["entities"])
	change, _ := verify["changeValidation"].(map[string]any)
	mode := asString(verify["mode"])
	fixStatus := ""
	obligationCount := 0
	if change != nil {
		if value := asString(change["mode"]); value != "" {
			mode = value
		}
		fixStatus = asString(change["fixValidation"])
		obligationCount = len(anySlice(change["obligations"]))
	}
	fmt.Fprintf(sb, "\nAgent change verify: %s (%s; fix=%s; obligations=%d; %d breaking, %d endpoints, %d queues, %d entities)\n",
		verdict, mode, fixStatus, obligationCount, len(breaking), len(endpoints), len(queues), len(entities))
	if change != nil {
		anchor, _ := change["anchor"].(map[string]any)
		connection, _ := change["connection"].(map[string]any)
		if anchor != nil {
			fmt.Fprintf(sb, "  anchor: %s %s:%s\n", asString(anchor["status"]), asString(anchor["kind"]), asString(anchor["ref"]))
		}
		if connection != nil {
			fmt.Fprintf(sb, "  boundary paths: changed=%v untouched=%v\n", connection["changedPathsToBoundary"], connection["untouchedPathsToBoundary"])
		}
	}
	if work, _ := verify["agentWork"].(map[string]any); work != nil {
		fmt.Fprintf(sb, "  proof: %s (%d claims, %d unaccounted production files)\n", asString(work["status"]), len(anySlice(work["claims"])), len(anySlice(work["unaccountedChanges"])))
	}
	for i, item := range breaking {
		if i >= 5 {
			fmt.Fprintf(sb, "  ... and %d more breaking changes\n", len(breaking)-i)
			break
		}
		change, _ := item.(map[string]any)
		fmt.Fprintf(sb, "  - %s %s removed %s [%s]\n", asString(change["severity"]), asString(change["endpoint"]), joinAnyStrings(anySlice(change["removedParams"])), asString(change["file"]))
	}
	for i, item := range reasons {
		if i >= 5 {
			fmt.Fprintf(sb, "  ... and %d more reasons\n", len(reasons)-i)
			break
		}
		fmt.Fprintf(sb, "  - %s\n", asString(item))
	}
}

func impactRelationLabel(item map[string]any) string {
	if item != nil && asBool(item["directlyAffected"]) {
		return "direct"
	}
	return "transitive"
}

// ---------------------------------------------------------------------------
// codebase_contracts
// ---------------------------------------------------------------------------

func (s *mcpServer) toolContracts(args map[string]any) toolResult {
	repo := strings.TrimSpace(argString(args, "repo", ""))
	if repo == "" {
		return errorToolResult("repo is required", nil)
	}

	var resp map[string]any
	params := map[string]string{}
	addWorkspaceParam(params, s.workspaceForArgs(args))
	if err := s.getJSON("/contracts/"+url.PathEscape(repo), params, &resp); err != nil {
		return errorToolResult(fmt.Sprintf("contracts failed: %v", err), nil)
	}

	// Build text summary
	var sb strings.Builder
	service, _ := resp["service"].(map[string]any)

	endpoints := anySlice(service["endpoints"])
	httpCalls := anySlice(service["httpCalls"])
	httpTargets := anySlice(service["httpTargets"])
	httpCallers := anySlice(service["httpCallers"])
	graphqlOperations := anySlice(service["graphqlOperations"])
	graphqlUsages := anySlice(service["graphqlUsages"])
	graphqlTargets := anySlice(service["graphqlTargets"])
	graphqlCallers := anySlice(service["graphqlCallers"])
	graphqlResolvers := anySlice(service["graphqlResolvers"])
	graphqlPermissions := anySlice(service["graphqlPermissions"])
	graphqlEntrypoints := anySlice(service["graphqlEntrypoints"])
	dataAccesses := anySlice(service["dataAccesses"])
	queuesProduced := anySlice(service["queuesProduced"])
	queuesConsumed := anySlice(service["queuesConsumed"])
	azureTimerTriggers := anySlice(service["azureTimerTriggers"])

	fmt.Fprintf(&sb, "Contracts for %s: %d endpoints, %d HTTP calls, %d GraphQL ops, %d GraphQL usages, %d GraphQL resolvers, %d GraphQL permissions, %d GraphQL entrypoints, %d data/side effects, %d queues produced, %d queues consumed, %d Azure timers\n",
		repo, len(endpoints), len(httpCalls), len(graphqlOperations), len(graphqlUsages), len(graphqlResolvers), len(graphqlPermissions), len(graphqlEntrypoints), len(dataAccesses), len(queuesProduced), len(queuesConsumed), len(azureTimerTriggers))
	appendWorkspaceLine(&sb, resp)
	appendWorkspaceHints(&sb, resp)

	if len(endpoints) > 0 {
		sb.WriteString("\nHTTP endpoints exposed:\n")
		epLimit := len(endpoints)
		if epLimit > 16 {
			epLimit = 16
		}
		for i := 0; i < epLimit; i++ {
			ep, _ := endpoints[i].(map[string]any)
			method := asString(ep["method"])
			path := asString(ep["path"])
			handler := asString(ep["handler"])
			file := asString(ep["file"])
			line := asNumber(ep["line"])
			locStr := ""
			if file != "" {
				locStr = fmt.Sprintf("  [%s:%d]", file, line)
			}
			fmt.Fprintf(&sb, "  %d. %s %s → %s%s\n", i+1, method, path, handler, locStr)
		}
		if len(endpoints) > epLimit {
			fmt.Fprintf(&sb, "  ... and %d more endpoints\n", len(endpoints)-epLimit)
		}
	}

	if len(httpCalls) > 0 {
		sb.WriteString("\nOutgoing HTTP calls:\n")
		hcLimit := len(httpCalls)
		if hcLimit > 12 {
			hcLimit = 12
		}
		for i := 0; i < hcLimit; i++ {
			call, _ := httpCalls[i].(map[string]any)
			method := asString(call["method"])
			path := asString(call["path"])
			count := asNumber(call["count"])
			external := asBool(call["external"])
			tag := ""
			if external {
				tag = " [external]"
			}
			// Show resolved target repos from matches
			matchStr := ""
			if matches := anySlice(call["matches"]); len(matches) > 0 {
				var targets []string
				mlimit := len(matches)
				if mlimit > 3 {
					mlimit = 3
				}
				for _, mm := range matches[:mlimit] {
					mt, _ := mm.(map[string]any)
					targets = append(targets, asString(mt["repo"])+":"+asString(mt["handler"]))
				}
				matchStr = " → " + strings.Join(targets, ", ")
				if len(matches) > 3 {
					matchStr += fmt.Sprintf(" (+%d)", len(matches)-3)
				}
			}
			fmt.Fprintf(&sb, "  %d. %s %s (count=%d)%s%s\n", i+1, method, path, count, tag, matchStr)
		}
		if len(httpCalls) > hcLimit {
			fmt.Fprintf(&sb, "  ... and %d more HTTP calls\n", len(httpCalls)-hcLimit)
		}
	}

	if len(httpTargets) > 0 {
		sb.WriteString("\nDownstream services (this repo calls):\n")
		limit := len(httpTargets)
		if limit > 8 {
			limit = 8
		}
		for i, t := range httpTargets[:limit] {
			target, _ := t.(map[string]any)
			name := asString(target["repo"])
			count := asNumber(target["count"])
			fmt.Fprintf(&sb, "  %d. %s (%d calls)\n", i+1, name, count)
		}
	}

	if len(httpCallers) > 0 {
		sb.WriteString("\nUpstream services (call this repo):\n")
		limit := len(httpCallers)
		if limit > 8 {
			limit = 8
		}
		for i, c := range httpCallers[:limit] {
			caller, _ := c.(map[string]any)
			name := asString(caller["repo"])
			count := asNumber(caller["count"])
			fmt.Fprintf(&sb, "  %d. %s (%d calls)\n", i+1, name, count)
		}
	}

	if len(graphqlOperations) > 0 {
		sb.WriteString("\nGraphQL operations defined:\n")
		limit := len(graphqlOperations)
		if limit > 10 {
			limit = 10
		}
		for i, op := range graphqlOperations[:limit] {
			item, _ := op.(map[string]any)
			name := asString(item["name"])
			opType := asString(item["type"])
			file := asString(item["file"])
			line := asNumber(item["line"])
			locStr := ""
			if file != "" {
				locStr = fmt.Sprintf("  [%s:%d]", file, line)
			}
			fmt.Fprintf(&sb, "  %d. %s %s%s\n", i+1, opType, name, locStr)
		}
		if len(graphqlOperations) > limit {
			fmt.Fprintf(&sb, "  ... and %d more GraphQL operations\n", len(graphqlOperations)-limit)
		}
	}

	if len(graphqlUsages) > 0 {
		sb.WriteString("\nGraphQL operations used:\n")
		limit := len(graphqlUsages)
		if limit > 10 {
			limit = 10
		}
		for i, usage := range graphqlUsages[:limit] {
			item, _ := usage.(map[string]any)
			importedAs := asString(item["importedAs"])
			caller := asString(item["caller"])
			file := asString(item["file"])
			line := asNumber(item["line"])
			locStr := ""
			if file != "" {
				locStr = fmt.Sprintf("  [%s:%d]", file, line)
			}
			fmt.Fprintf(&sb, "  %d. %s in %s%s\n", i+1, importedAs, caller, locStr)
		}
		if len(graphqlUsages) > limit {
			fmt.Fprintf(&sb, "  ... and %d more GraphQL usages\n", len(graphqlUsages)-limit)
		}
	}

	if len(graphqlResolvers) > 0 {
		sb.WriteString("\nGraphQL resolvers exposed:\n")
		limit := len(graphqlResolvers)
		if limit > 10 {
			limit = 10
		}
		for i, resolver := range graphqlResolvers[:limit] {
			item, _ := resolver.(map[string]any)
			opName := asString(item["operationName"])
			opType := asString(item["operationType"])
			name := asString(item["resolver"])
			file := asString(item["file"])
			line := asNumber(item["line"])
			locStr := ""
			if file != "" {
				locStr = fmt.Sprintf("  [%s:%d]", file, line)
			}
			fmt.Fprintf(&sb, "  %d. %s %s -> %s%s\n", i+1, opType, opName, name, locStr)
		}
		if len(graphqlResolvers) > limit {
			fmt.Fprintf(&sb, "  ... and %d more GraphQL resolvers\n", len(graphqlResolvers)-limit)
		}
	}

	if len(graphqlPermissions) > 0 {
		sb.WriteString("\nGraphQL permission rules:\n")
		limit := len(graphqlPermissions)
		if limit > 10 {
			limit = 10
		}
		for i, permission := range graphqlPermissions[:limit] {
			item, _ := permission.(map[string]any)
			opName := asString(item["operationName"])
			opType := asString(item["operationType"])
			rule := asString(item["ruleExpression"])
			file := asString(item["file"])
			line := asNumber(item["line"])
			locStr := ""
			if file != "" {
				locStr = fmt.Sprintf("  [%s:%d]", file, line)
			}
			fmt.Fprintf(&sb, "  %d. %s %s guarded by %s%s\n", i+1, opType, opName, rule, locStr)
		}
		if len(graphqlPermissions) > limit {
			fmt.Fprintf(&sb, "  ... and %d more GraphQL permission rules\n", len(graphqlPermissions)-limit)
		}
	}

	if len(graphqlTargets) > 0 {
		sb.WriteString("\nDownstream GraphQL services (this repo uses):\n")
		limit := len(graphqlTargets)
		if limit > 8 {
			limit = 8
		}
		for i, target := range graphqlTargets[:limit] {
			item, _ := target.(map[string]any)
			fmt.Fprintf(&sb, "  %d. %s (%d operations)\n", i+1, asString(item["repo"]), asNumber(item["count"]))
		}
	}

	if len(graphqlCallers) > 0 {
		sb.WriteString("\nUpstream GraphQL callers (use this repo):\n")
		limit := len(graphqlCallers)
		if limit > 8 {
			limit = 8
		}
		for i, caller := range graphqlCallers[:limit] {
			item, _ := caller.(map[string]any)
			fmt.Fprintf(&sb, "  %d. %s (%d operations)\n", i+1, asString(item["repo"]), asNumber(item["count"]))
		}
	}

	if len(graphqlEntrypoints) > 0 {
		sb.WriteString("\nGraphQL backend entrypoints:\n")
		limit := len(graphqlEntrypoints)
		if limit > 6 {
			limit = 6
		}
		for i, entry := range graphqlEntrypoints[:limit] {
			item, _ := entry.(map[string]any)
			kind := asString(item["registrationKind"])
			path := asString(item["controllersPath"])
			file := asString(item["file"])
			line := asNumber(item["line"])
			locStr := ""
			if file != "" {
				locStr = fmt.Sprintf("  [%s:%d]", file, line)
			}
			if path != "" {
				fmt.Fprintf(&sb, "  %d. %s controllers=%s%s\n", i+1, kind, path, locStr)
			} else {
				fmt.Fprintf(&sb, "  %d. %s%s\n", i+1, kind, locStr)
			}
		}
	}

	if len(dataAccesses) > 0 {
		sb.WriteString("\nData and side-effect accesses:\n")
		limit := len(dataAccesses)
		if limit > 12 {
			limit = 12
		}
		for i, access := range dataAccesses[:limit] {
			item, _ := access.(map[string]any)
			entity := asString(item["entity"])
			kind := asString(item["access"])
			caller := asString(item["caller"])
			file := asString(item["file"])
			line := asNumber(item["line"])
			locStr := ""
			if file != "" {
				locStr = fmt.Sprintf("  [%s:%d]", file, line)
			}
			fmt.Fprintf(&sb, "  %d. %s %s by %s%s\n", i+1, kind, entity, caller, locStr)
		}
		if len(dataAccesses) > limit {
			fmt.Fprintf(&sb, "  ... and %d more data/side-effect accesses\n", len(dataAccesses)-limit)
		}
	}

	if len(queuesProduced) > 0 {
		sb.WriteString("\nQueues / Service Bus produced:\n")
		limit := len(queuesProduced)
		if limit > 8 {
			limit = 8
		}
		for i, q := range queuesProduced[:limit] {
			queue, _ := q.(map[string]any)
			name := asString(queue["name"])
			count := asNumber(queue["count"])
			counterparties := anySlice(queue["counterparties"])
			fmt.Fprintf(&sb, "  %d. %s (count=%d) → consumed by %v\n", i+1, name, count, toStringSlice(counterparties))
		}
	}

	if len(queuesConsumed) > 0 {
		sb.WriteString("\nQueues / Service Bus consumed:\n")
		limit := len(queuesConsumed)
		if limit > 8 {
			limit = 8
		}
		for i, q := range queuesConsumed[:limit] {
			queue, _ := q.(map[string]any)
			name := asString(queue["name"])
			count := asNumber(queue["count"])
			counterparties := anySlice(queue["counterparties"])
			fmt.Fprintf(&sb, "  %d. %s (count=%d) ← produced by %v\n", i+1, name, count, toStringSlice(counterparties))
		}
	}

	ebTriggers := anySlice(service["eventBridgeTriggers"])
	if len(ebTriggers) > 0 {
		sb.WriteString("\nEventBridge triggers:\n")
		limit := len(ebTriggers)
		if limit > 6 {
			limit = 6
		}
		for i, t := range ebTriggers[:limit] {
			trigger, _ := t.(map[string]any)
			ruleName := asString(trigger["ruleName"])
			expr := asString(trigger["scheduleExpression"])
			targetQueue := asString(trigger["targetQueue"])
			state := asString(trigger["state"])
			fmt.Fprintf(&sb, "  %d. %s  %s -> queue:%s  [%s]\n", i+1, ruleName, expr, targetQueue, state)
		}
	}

	if len(azureTimerTriggers) > 0 {
		sb.WriteString("\nAzure timer triggers:\n")
		limit := len(azureTimerTriggers)
		if limit > 6 {
			limit = 6
		}
		for i, t := range azureTimerTriggers[:limit] {
			trigger, _ := t.(map[string]any)
			functionName := asString(trigger["functionName"])
			expr := asString(trigger["scheduleExpression"])
			file := asString(trigger["file"])
			line := asNumber(trigger["line"])
			locStr := ""
			if file != "" {
				locStr = fmt.Sprintf("  [%s:%d]", file, line)
			}
			fmt.Fprintf(&sb, "  %d. %s  %s%s\n", i+1, functionName, expr, locStr)
		}
	}

	return successToolResult(sb.String(), resp)
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func (s *mcpServer) getJSON(path string, params map[string]string, out any) error {
	endpoint := s.endpoint(path)
	if len(params) > 0 {
		vals := url.Values{}
		for k, v := range params {
			vals.Set(k, v)
		}
		endpoint += "?" + vals.Encode()
	}

	httpReq, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := s.do(httpReq)
	if err != nil {
		return err
	}
	return decodeAPIResponse(resp, out)
}

func (s *mcpServer) postJSON(path string, req any, out any) error {
	endpoint := s.endpoint(path)
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := s.do(httpReq)
	if err != nil {
		return err
	}
	return decodeAPIResponse(resp, out)
}

func decodeAPIResponse(resp *http.Response, out any) error {
	defer resp.Body.Close()

	const maxResponseBytes = 20 * 1024 * 1024
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}
	if len(payload) > maxResponseBytes {
		return fmt.Errorf("API response (%d) exceeds %d bytes; narrow the requested results", resp.StatusCode, maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}
	return nil
}

func (s *mcpServer) endpoint(path string) string {
	base := strings.TrimRight(s.apiBaseURL, "/")
	path = "/" + strings.TrimLeft(path, "/")
	if strings.HasSuffix(base, "/api") {
		return base + path
	}
	return base + "/api" + path
}

func (s *mcpServer) workspaceForArgs(args map[string]any) string {
	if args != nil {
		if workspaceID := strings.TrimSpace(argString(args, "workspaceId", "")); workspaceID != "" {
			return workspaceID
		}
	}
	return strings.TrimSpace(s.workspace)
}

func addWorkspaceParam(params map[string]string, workspaceID string) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return
	}
	params["workspaceId"] = workspaceID
}

func addWorkspaceBody(req map[string]any, workspaceID string) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return
	}
	req["workspaceId"] = workspaceID
}

func appendWorkspaceLine(sb *strings.Builder, resp map[string]any) {
	workspace, _ := resp["workspace"].(map[string]any)
	if workspace == nil {
		return
	}
	id := asString(workspace["id"])
	name := asString(workspace["name"])
	if id == "" {
		return
	}
	if name != "" && name != id {
		fmt.Fprintf(sb, "Workspace: %s (%s)\n", id, name)
		return
	}
	fmt.Fprintf(sb, "Workspace: %s\n", id)
}

func appendWorkspaceHints(sb *strings.Builder, resp map[string]any) {
	hints := anySlice(resp["workspaceHints"])
	if len(hints) == 0 {
		return
	}
	sb.WriteString("Workspace hints:\n")
	for _, rawHint := range hints {
		hint, _ := rawHint.(map[string]any)
		kind := asString(hint["kind"])
		found := anySlice(hint["foundIn"])
		for _, rawMatch := range found {
			match, _ := rawMatch.(map[string]any)
			workspaceID := asString(match["workspaceId"])
			repo := asString(match["repo"])
			branch := asString(match["branch"])
			symbol := asString(match["symbol"])
			file := asString(match["file"])
			if workspaceID == "" && repo == "" && symbol == "" {
				continue
			}
			fmt.Fprintf(sb, "  - %s: %s %s@%s %s", kind, workspaceID, repo, branch, symbol)
			if file != "" {
				fmt.Fprintf(sb, " [%s]", file)
			}
			sb.WriteString("\n")
		}
	}
}

// token returns the Tirion API token, resolving it on first use and caching it
// once found. A failure is reported to the caller as a tool error and retried
// on the next call, so starting `tirion serve` (or setting the token) after
// this process started takes effect without a restart.
func (s *mcpServer) token() (string, error) {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	if s.apiToken != "" || s.resolveToken == nil {
		return s.apiToken, nil
	}
	token, err := s.resolveToken()
	if err != nil {
		return "", fmt.Errorf("Tirion API token unavailable: %w. To fix: run `tirion serve` once on this machine so it creates the token file (default ~/.tirion/api-token, or under TIRION_HOME), "+
			"or set TIRION_API_TOKEN / TIRION_API_TOKEN_FILE in this MCP server's environment. "+
			"A non-loopback TIRION_API_URL must use https and an explicit token", err)
	}
	s.apiToken = token
	return token, nil
}

// forgetToken drops a cached token the API rejected, so a rotated token file
// is picked up by the next call.
func (s *mcpServer) forgetToken() {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	if s.resolveToken != nil {
		s.apiToken = ""
	}
}

func (s *mcpServer) applyAuth(req *http.Request) error {
	if req == nil {
		return nil
	}
	token, err := s.token()
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("X-Tirion-Token", token)
	}
	if s.workspace != "" {
		req.Header.Set("X-Tirion-Workspace", s.workspace)
	}
	if s.actor != "" {
		req.Header.Set("X-Tirion-Actor", s.actor)
	}
	if s.authHeader != "" {
		req.Header.Set("Authorization", s.authHeader)
		return nil
	}
	if s.username != "" {
		req.SetBasicAuth(s.username, s.password)
	}
	return nil
}

// do sends an authenticated API request. A 401 clears the cached token so a
// rotated token file is re-read on the next call.
func (s *mcpServer) do(req *http.Request) (*http.Response, error) {
	if err := s.applyAuth(req); err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		s.forgetToken()
	}
	return resp, nil
}

func getEnvFirst(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

// Actor attribution is opt-in; do not disclose workstation identity by default.
func resolveActor() string {
	return strings.TrimSpace(getEnvFirst("TIRION_USER", "TIRION_ACTOR", "CODEBASE_INTEL_USER"))
}

// ---------------------------------------------------------------------------
// JSON-RPC message I/O
// ---------------------------------------------------------------------------

func parseID(raw json.RawMessage) interface{} {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || !json.Valid(raw) {
		return nil
	}
	if raw[0] == '"' || raw[0] == '-' || (raw[0] >= '0' && raw[0] <= '9') {
		return raw
	}
	return nil
}

func readMessage(r *bufio.Reader) ([]byte, error) {
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}

		if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
			continue
		}

		if b == '{' {
			return readJSONObject(r, b)
		}

		line, err := readHeaderLine(r)
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimSpace(string(append([]byte{b}, []byte(line)...)))
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(strings.ToLower(trimmed), "content-length:") {
			n, err := parseContentLength(trimmed)
			if err != nil {
				return nil, err
			}
			for headerCount := 0; ; headerCount++ {
				if headerCount >= 32 {
					return nil, fmt.Errorf("too many MCP headers")
				}
				headerLine, err := readHeaderLine(r)
				if err != nil {
					return nil, err
				}
				if headerLine == "\r\n" || headerLine == "\n" {
					break
				}
			}
			if n < 0 || n > 8<<20 {
				return nil, fmt.Errorf("MCP message exceeds 8 MiB")
			}
			body := make([]byte, n)
			if _, err := io.ReadFull(r, body); err != nil {
				return nil, err
			}
			return body, nil
		}
	}
}

func readJSONObject(r *bufio.Reader, first byte) ([]byte, error) {
	buf := []byte{first}
	depth := 1
	inString := false
	escape := false

	for depth > 0 {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if len(buf) >= 8<<20 {
			return nil, fmt.Errorf("MCP message exceeds 8 MiB")
		}
		buf = append(buf, b)

		if inString {
			if escape {
				escape = false
				continue
			}
			if b == '\\' {
				escape = true
				continue
			}
			if b == '"' {
				inString = false
			}
			continue
		}

		switch b {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
		}
	}

	return buf, nil
}

func parseContentLength(line string) (int, error) {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid content-length header")
	}
	n, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid content-length value")
	}
	return n, nil
}

func writeMessage(w *bufio.Writer, resp mcpResponse) error {
	body, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if _, err := w.Write(body); err != nil {
		return err
	}
	return w.Flush()
}

// ---------------------------------------------------------------------------
// Result helpers
// ---------------------------------------------------------------------------

func successToolResult(text string, structured interface{}) toolResult {
	return toolResult{
		Content: []toolContent{
			{Type: "text", Text: text},
		},
		StructuredContent: structured,
	}
}

func compactStructuredPayload(resp map[string]any, dropKeys ...string) map[string]any {
	if resp == nil {
		return nil
	}
	out := make(map[string]any, len(resp))
	for k, v := range resp {
		out[k] = v
	}
	for _, key := range dropKeys {
		delete(out, key)
	}
	return out
}

func compactSearchStructuredPayload(resp map[string]any) map[string]any {
	return compactStructuredPayload(resp, "repoContext", "workspaceHints")
}

func errorToolResult(text string, structured interface{}) toolResult {
	return toolResult{
		Content: []toolContent{
			{Type: "text", Text: text},
		},
		StructuredContent: structured,
		IsError:           true,
	}
}

// ---------------------------------------------------------------------------
// Argument helpers
// ---------------------------------------------------------------------------

func argString(args map[string]any, key, fallback string) string {
	value, ok := args[key]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return fallback
		}
		return typed
	default:
		return fallback
	}
}

func argBool(args map[string]any, key string, fallback bool) bool {
	value, ok := args[key]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes":
			return true
		case "false", "0", "no":
			return false
		default:
			return fallback
		}
	default:
		return fallback
	}
}

func argInt(args map[string]any, key string, fallback int) int {
	value, ok := args[key]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed
		}
	}
	return fallback
}

func argStringSlice(value any) []string {
	if value == nil {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			str, ok := item.(string)
			if !ok {
				continue
			}
			str = strings.TrimSpace(str)
			if str != "" {
				out = append(out, str)
			}
		}
		return out
	case string:
		parts := strings.Split(typed, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
		return out
	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// Value helpers
// ---------------------------------------------------------------------------

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return ""
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func asNumber(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	default:
		return 0
	}
}

func asFloat(v any) float64 {
	if t, ok := v.(float64); ok {
		return t
	}
	return 0
}

func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	default:
		return false
	}
}

func anySlice(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case []map[string]any:
		out := make([]any, 0, len(t))
		for _, item := range t {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func toStringSlice(items []any) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Bounded transport headers
// ---------------------------------------------------------------------------

func readHeaderLine(r *bufio.Reader) (string, error) {
	var line []byte
	for {
		fragment, err := r.ReadSlice('\n')
		line = append(line, fragment...)
		if len(line) > 8192 {
			return "", fmt.Errorf("MCP header exceeds 8 KiB")
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return string(line), err
	}
}
