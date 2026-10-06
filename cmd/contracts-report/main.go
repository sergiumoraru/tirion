package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sergiumoraru/tirion/internal/runtimeconfig"
)

type contractsResponse struct {
	Services []contractSummary `json:"services"`
	Service  *contractDetail   `json:"service"`
}

type contractSummary struct {
	Repo               string `json:"repo"`
	EndpointCount      int    `json:"endpointCount"`
	HttpCallCount      int    `json:"httpCallCount"`
	GraphQLOperations  int    `json:"graphqlOperations,omitempty"`
	GraphQLUsages      int    `json:"graphqlUsages,omitempty"`
	GraphQLResolvers   int    `json:"graphqlResolvers,omitempty"`
	GraphQLPermissions int    `json:"graphqlPermissions,omitempty"`
	GraphQLEntrypoints int    `json:"graphqlEntrypoints,omitempty"`
	DataAccessCount    int    `json:"dataAccessCount,omitempty"`
	AzureTimerTriggers int    `json:"azureTimerTriggers,omitempty"`
	QueuesProduced     int    `json:"queuesProduced"`
	QueuesConsumed     int    `json:"queuesConsumed"`
}

type contractEndpoint struct {
	Method  string   `json:"method"`
	Path    string   `json:"path"`
	Handler string   `json:"handler"`
	File    string   `json:"file"`
	Line    int      `json:"line"`
	Owners  []string `json:"owners,omitempty"`
}

type contractHttpMatch struct {
	Repo    string `json:"repo"`
	Handler string `json:"handler"`
	Path    string `json:"path"`
	File    string `json:"file"`
	Line    int    `json:"line"`
}

type contractHttpCall struct {
	Method     string              `json:"method"`
	Path       string              `json:"path"`
	Count      int                 `json:"count"`
	ClientType string              `json:"clientType,omitempty"`
	External   bool                `json:"external,omitempty"`
	Matches    []contractHttpMatch `json:"matches,omitempty"`
}

type contractQueue struct {
	Name           string   `json:"name"`
	Count          int      `json:"count"`
	Counterparties []string `json:"counterparties,omitempty"`
}

type contractRepoCount struct {
	Repo  string `json:"repo"`
	Count int    `json:"count"`
}

type contractGraphQLOperation struct {
	Name string `json:"name"`
	Type string `json:"type"`
	File string `json:"file"`
	Line int    `json:"line"`
}

type contractGraphQLUsage struct {
	ImportedAs string `json:"importedAs"`
	ImportPath string `json:"importPath"`
	Caller     string `json:"caller"`
	File       string `json:"file"`
	Line       int    `json:"line"`
}

type contractGraphQLResolver struct {
	OperationName string `json:"operationName"`
	OperationType string `json:"operationType"`
	Resolver      string `json:"resolver"`
	File          string `json:"file"`
	Line          int    `json:"line"`
}

type contractGraphQLPermission struct {
	OperationName  string `json:"operationName"`
	OperationType  string `json:"operationType"`
	RuleExpression string `json:"ruleExpression"`
	File           string `json:"file"`
	Line           int    `json:"line"`
}

type contractGraphQLEntrypoint struct {
	HandlerName      string `json:"handlerName,omitempty"`
	RegistrationKind string `json:"registrationKind"`
	ControllersPath  string `json:"controllersPath,omitempty"`
	File             string `json:"file"`
	Line             int    `json:"line"`
}

type contractDataAccess struct {
	Entity string `json:"entity"`
	Access string `json:"access"`
	Caller string `json:"caller"`
	File   string `json:"file"`
	Line   int    `json:"line"`
}

type contractSchedule struct {
	RuleName           string `json:"ruleName"`
	ScheduleExpression string `json:"scheduleExpression"`
	TargetQueue        string `json:"targetQueue"`
	State              string `json:"state"`
	Source             string `json:"source,omitempty"`
	FunctionName       string `json:"functionName,omitempty"`
	File               string `json:"file,omitempty"`
	Line               int    `json:"line,omitempty"`
}

type contractDetail struct {
	Repo                string                      `json:"repo"`
	Endpoints           []contractEndpoint          `json:"endpoints"`
	HttpCalls           []contractHttpCall          `json:"httpCalls"`
	HttpTargets         []contractRepoCount         `json:"httpTargets,omitempty"`
	HttpCallers         []contractRepoCount         `json:"httpCallers,omitempty"`
	GraphQLOperations   []contractGraphQLOperation  `json:"graphqlOperations,omitempty"`
	GraphQLUsages       []contractGraphQLUsage      `json:"graphqlUsages,omitempty"`
	GraphQLTargets      []contractRepoCount         `json:"graphqlTargets,omitempty"`
	GraphQLCallers      []contractRepoCount         `json:"graphqlCallers,omitempty"`
	GraphQLResolvers    []contractGraphQLResolver   `json:"graphqlResolvers,omitempty"`
	GraphQLPermissions  []contractGraphQLPermission `json:"graphqlPermissions,omitempty"`
	GraphQLEntrypoints  []contractGraphQLEntrypoint `json:"graphqlEntrypoints,omitempty"`
	DataAccesses        []contractDataAccess        `json:"dataAccesses,omitempty"`
	QueuesProduced      []contractQueue             `json:"queuesProduced"`
	QueuesConsumed      []contractQueue             `json:"queuesConsumed"`
	EventBridgeTriggers []contractSchedule          `json:"eventBridgeTriggers,omitempty"`
	AzureTimerTriggers  []contractSchedule          `json:"azureTimerTriggers,omitempty"`
}

func main() {

	server := flag.String("server", "http://localhost:8080", "API server base URL")
	repo := flag.String("repo", "", "Repo name to fetch contract details")
	workspace := flag.String("workspace", "", "Workspace ID to query")
	limit := flag.Int("limit", 200, "Limit for endpoints/calls")
	timeout := flag.Duration("timeout", 60*time.Second, "HTTP timeout")
	out := flag.String("out", "", "Output Markdown file (default stdout)")
	jsonOut := flag.String("json-out", "", "Optional raw JSON output file")
	flag.Parse()

	token, err := runtimeconfig.ClientToken(*server)
	if err != nil {
		fail(err)
	}
	req, err := http.NewRequest(http.MethodGet, contractsURL(*server, *repo, *limit, *workspace), nil)
	if err != nil {
		fail(err)
	}
	req.Header.Set("X-Tirion-Token", token)
	client := &http.Client{Timeout: *timeout, CheckRedirect: runtimeconfig.NoRedirect}
	resp, err := client.Do(req)
	if err != nil {
		fail(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fail(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fail(fmt.Errorf("contracts failed: %s", strings.TrimSpace(string(body))))
	}

	if *jsonOut != "" {
		if err := os.WriteFile(*jsonOut, body, 0o644); err != nil {
			fail(err)
		}
	}

	var response contractsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		fail(err)
	}

	report := buildMarkdownReport(response, *repo)
	if err := writeOutput(*out, report); err != nil {
		fail(err)
	}
}

func contractsURL(server, repo string, limit int, workspace string) string {
	base := strings.TrimRight(strings.TrimSpace(server), "/") + "/api/contracts"
	if strings.TrimSpace(repo) != "" {
		base += "/" + url.PathEscape(strings.TrimSpace(repo))
	}
	values := url.Values{}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	if strings.TrimSpace(workspace) != "" {
		values.Set("workspaceId", strings.TrimSpace(workspace))
	}
	if encoded := values.Encode(); encoded != "" {
		base += "?" + encoded
	}
	return base
}

func buildMarkdownReport(resp contractsResponse, repo string) string {
	var b strings.Builder
	if repo != "" && resp.Service != nil {
		writeServiceReport(&b, *resp.Service)
	} else {
		writeSummaryReport(&b, resp.Services)
	}
	return b.String()
}

func writeSummaryReport(b *strings.Builder, services []contractSummary) {
	b.WriteString("# Service Contracts\n\n")
	b.WriteString(fmt.Sprintf("Services: %d\n\n", len(services)))
	b.WriteString("## Services\n")
	if len(services) == 0 {
		b.WriteString("- None\n")
		return
	}
	for _, svc := range services {
		graphQLCount := svc.GraphQLOperations + svc.GraphQLUsages + svc.GraphQLResolvers + svc.GraphQLPermissions + svc.GraphQLEntrypoints
		b.WriteString(fmt.Sprintf("- %s — endpoints %d, http %d, graphql %d, data %d, queues %d/%d, azure timers %d\n",
			svc.Repo, svc.EndpointCount, svc.HttpCallCount, graphQLCount, svc.DataAccessCount, svc.QueuesProduced, svc.QueuesConsumed, svc.AzureTimerTriggers))
	}
}

func writeServiceReport(b *strings.Builder, svc contractDetail) {
	b.WriteString(fmt.Sprintf("# Service Contract: %s\n\n", svc.Repo))

	writeEndpoints(b, svc.Endpoints)
	writeHttpCalls(b, svc.HttpCalls)
	writeRepoCounts(b, "HTTP Targets", svc.HttpTargets)
	writeRepoCounts(b, "HTTP Callers", svc.HttpCallers)
	writeGraphQLOperations(b, svc.GraphQLOperations)
	writeGraphQLUsages(b, svc.GraphQLUsages)
	writeRepoCounts(b, "GraphQL Targets", svc.GraphQLTargets)
	writeRepoCounts(b, "GraphQL Callers", svc.GraphQLCallers)
	writeGraphQLResolvers(b, svc.GraphQLResolvers)
	writeGraphQLPermissions(b, svc.GraphQLPermissions)
	writeGraphQLEntrypoints(b, svc.GraphQLEntrypoints)
	writeDataAccesses(b, svc.DataAccesses)
	writeQueues(b, "Queues Produced", svc.QueuesProduced)
	writeQueues(b, "Queues Consumed", svc.QueuesConsumed)
	writeSchedules(b, "EventBridge Triggers", svc.EventBridgeTriggers)
	writeSchedules(b, "Azure Timer Triggers", svc.AzureTimerTriggers)
}

func writeEndpoints(b *strings.Builder, endpoints []contractEndpoint) {
	b.WriteString("## Endpoints\n")
	if len(endpoints) == 0 {
		b.WriteString("- None\n\n")
		return
	}
	for _, endpoint := range endpoints {
		owners := ""
		if len(endpoint.Owners) > 0 {
			owners = fmt.Sprintf(" | owners: %s", strings.Join(endpoint.Owners, ", "))
		}
		b.WriteString(fmt.Sprintf("- %s %s → %s (%s:%d)%s\n",
			endpoint.Method, endpoint.Path, endpoint.Handler, endpoint.File, endpoint.Line, owners))
	}
	b.WriteString("\n")
}

func writeHttpCalls(b *strings.Builder, calls []contractHttpCall) {
	b.WriteString("## Outbound HTTP\n")
	if len(calls) == 0 {
		b.WriteString("- None\n\n")
		return
	}
	for _, call := range calls {
		external := ""
		if call.External {
			external = " (external)"
		}
		client := ""
		if call.ClientType != "" {
			client = fmt.Sprintf(" | client %s", call.ClientType)
		}
		b.WriteString(fmt.Sprintf("- %s %s — %d call(s)%s%s\n",
			call.Method, call.Path, call.Count, external, client))
		if len(call.Matches) > 0 {
			b.WriteString("  - matches: ")
			for i, match := range call.Matches {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(fmt.Sprintf("%s.%s", match.Repo, match.Handler))
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
}

func writeRepoCounts(b *strings.Builder, title string, rows []contractRepoCount) {
	b.WriteString("## " + title + "\n")
	if len(rows) == 0 {
		b.WriteString("- None\n\n")
		return
	}
	for _, row := range rows {
		b.WriteString(fmt.Sprintf("- %s (%d)\n", row.Repo, row.Count))
	}
	b.WriteString("\n")
}

func writeGraphQLOperations(b *strings.Builder, operations []contractGraphQLOperation) {
	b.WriteString("## GraphQL Operations\n")
	if len(operations) == 0 {
		b.WriteString("- None\n\n")
		return
	}
	for _, operation := range operations {
		b.WriteString(fmt.Sprintf("- %s %s (%s:%d)\n",
			strings.ToUpper(operation.Type), operation.Name, operation.File, operation.Line))
	}
	b.WriteString("\n")
}

func writeGraphQLUsages(b *strings.Builder, usages []contractGraphQLUsage) {
	b.WriteString("## GraphQL Usages\n")
	if len(usages) == 0 {
		b.WriteString("- None\n\n")
		return
	}
	for _, usage := range usages {
		b.WriteString(fmt.Sprintf("- %s uses %s from %s (%s:%d)\n",
			firstNonEmpty(usage.Caller, "_module_"), usage.ImportedAs, usage.ImportPath, usage.File, usage.Line))
	}
	b.WriteString("\n")
}

func writeGraphQLResolvers(b *strings.Builder, resolvers []contractGraphQLResolver) {
	b.WriteString("## GraphQL Resolvers\n")
	if len(resolvers) == 0 {
		b.WriteString("- None\n\n")
		return
	}
	for _, resolver := range resolvers {
		b.WriteString(fmt.Sprintf("- %s %s → %s (%s:%d)\n",
			strings.ToUpper(resolver.OperationType), resolver.OperationName, resolver.Resolver, resolver.File, resolver.Line))
	}
	b.WriteString("\n")
}

func writeGraphQLPermissions(b *strings.Builder, permissions []contractGraphQLPermission) {
	b.WriteString("## GraphQL Permissions\n")
	if len(permissions) == 0 {
		b.WriteString("- None\n\n")
		return
	}
	for _, permission := range permissions {
		b.WriteString(fmt.Sprintf("- %s %s → %s (%s:%d)\n",
			strings.ToUpper(permission.OperationType), permission.OperationName, permission.RuleExpression, permission.File, permission.Line))
	}
	b.WriteString("\n")
}

func writeGraphQLEntrypoints(b *strings.Builder, entrypoints []contractGraphQLEntrypoint) {
	b.WriteString("## GraphQL Entrypoints\n")
	if len(entrypoints) == 0 {
		b.WriteString("- None\n\n")
		return
	}
	for _, entrypoint := range entrypoints {
		target := entrypoint.ControllersPath
		if target == "" {
			target = entrypoint.RegistrationKind
		}
		b.WriteString(fmt.Sprintf("- %s → %s (%s:%d)\n",
			firstNonEmpty(entrypoint.HandlerName, "_module_"), target, entrypoint.File, entrypoint.Line))
	}
	b.WriteString("\n")
}

func writeDataAccesses(b *strings.Builder, accesses []contractDataAccess) {
	b.WriteString("## Data / Side Effects\n")
	if len(accesses) == 0 {
		b.WriteString("- None\n\n")
		return
	}
	for _, access := range accesses {
		b.WriteString(fmt.Sprintf("- %s %s by %s (%s:%d)\n",
			access.Access, access.Entity, firstNonEmpty(access.Caller, "_module_"), access.File, access.Line))
	}
	b.WriteString("\n")
}

func writeQueues(b *strings.Builder, title string, queues []contractQueue) {
	b.WriteString("## " + title + "\n")
	if len(queues) == 0 {
		b.WriteString("- None\n\n")
		return
	}
	for _, queue := range queues {
		line := fmt.Sprintf("- %s (%d)", queue.Name, queue.Count)
		if len(queue.Counterparties) > 0 {
			line += " → " + strings.Join(queue.Counterparties, ", ")
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n")
}

func writeSchedules(b *strings.Builder, title string, schedules []contractSchedule) {
	b.WriteString("## " + title + "\n")
	if len(schedules) == 0 {
		b.WriteString("- None\n\n")
		return
	}
	for _, schedule := range schedules {
		name := firstNonEmpty(schedule.RuleName, schedule.FunctionName)
		target := schedule.TargetQueue
		if target == "" && schedule.FunctionName != "" {
			target = schedule.FunctionName
		}
		location := ""
		if schedule.File != "" {
			location = fmt.Sprintf(" (%s:%d)", schedule.File, schedule.Line)
		}
		state := ""
		if schedule.State != "" {
			state = " | " + schedule.State
		}
		b.WriteString(fmt.Sprintf("- %s — %s → %s%s%s\n",
			name, schedule.ScheduleExpression, target, state, location))
	}
	b.WriteString("\n")
}

func writeOutput(path string, content string) error {
	if path == "" {
		_, err := fmt.Fprint(os.Stdout, content)
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
