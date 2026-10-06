package parser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type APIMParser struct{}

type apimTemplate struct {
	Resources []apimResource `json:"resources"`
}

type apimResource struct {
	Type       string          `json:"type"`
	Name       string          `json:"name"`
	Properties json.RawMessage `json:"properties"`
}

type apimAPIProperties struct {
	Path string `json:"path"`
}

type apimOperationProperties struct {
	Method      string `json:"method"`
	URLTemplate string `json:"urlTemplate"`
}

type apimPolicyProperties struct {
	Value string `json:"value"`
}

type logicTriggerProperties struct {
	Recurrence json.RawMessage `json:"recurrence"`
	Type       string          `json:"type"`
	Inputs     json.RawMessage `json:"inputs"`
}

type logicWorkflowProperties struct {
	Definition struct {
		Triggers map[string]logicWorkflowDefinitionTrigger `json:"triggers"`
	} `json:"definition"`
}

type logicWorkflowDefinitionTrigger struct {
	Type       string          `json:"type"`
	Recurrence json.RawMessage `json:"recurrence"`
	Inputs     json.RawMessage `json:"inputs"`
}

type logicRecurrence struct {
	Frequency string          `json:"frequency"`
	Interval  any             `json:"interval"`
	Schedule  json.RawMessage `json:"schedule"`
}

type apimAPI struct {
	Name string
	Path string
	Line int
}

type apimOperation struct {
	APIName       string
	OperationName string
	Method        string
	URLTemplate   string
	Line          int
}

type apimPolicy struct {
	APIName       string
	OperationName string
	BackendMethod string
	BackendURL    string
	BackendPath   string
	BackendID     string
}

var (
	apimSingleQuotedRe      = regexp.MustCompile(`'([^']*)'`)
	apimSetBackendURLRe     = regexp.MustCompile(`(?is)<set-backend-service[^>]*\sbase-url="([^"]+)"`)
	apimSetBackendIDRe      = regexp.MustCompile(`(?is)<set-backend-service[^>]*\sbackend-id="([^"]+)"`)
	apimRewriteURIRe        = regexp.MustCompile(`(?is)<rewrite-uri[^>]*\stemplate="([^"]+)"`)
	apimSetMethodRe         = regexp.MustCompile(`(?is)<set-method[^>]*>\s*([^<]+?)\s*</set-method>`)
	apimContentSignatureSet = [][]byte{
		[]byte(`"Microsoft.ApiManagement/service/apis"`),
		[]byte(`"Microsoft.ApiManagement/service/apis/operations"`),
		[]byte(`"Microsoft.ApiManagement/service/apis/policies"`),
		[]byte(`"Microsoft.ApiManagement/service/apis/operations/policies"`),
		[]byte(`"Microsoft.Logic/workflows"`),
		[]byte(`"Microsoft.Logic/workflows/triggers"`),
	}
)

func NewAPIMParser() *APIMParser {
	return &APIMParser{}
}

func (p *APIMParser) CanParse(filePath string) bool {
	if !strings.EqualFold(filepath.Ext(filePath), ".json") {
		return false
	}
	normalized := strings.ToLower(filepath.ToSlash(strings.TrimSpace(filePath)))
	if normalized == "" {
		return false
	}
	base := strings.ToLower(filepath.Base(normalized))
	return strings.Contains(base, "apim") ||
		strings.Contains(base, "policy") ||
		strings.Contains(base, "api-management") ||
		strings.Contains(base, "logic") ||
		strings.Contains(base, "workflow") ||
		strings.Contains(base, "azuredeploy") ||
		strings.Contains(base, "arm-template") ||
		base == "arm.json" ||
		strings.Contains(normalized, "/apim/") ||
		strings.Contains(normalized, "/api-management/") ||
		strings.Contains(normalized, "/api_management/") ||
		strings.Contains(normalized, "/logic/") ||
		strings.Contains(normalized, "/logic-app/") ||
		strings.Contains(normalized, "/logic_apps/") ||
		strings.Contains(normalized, "/workflows/") ||
		strings.Contains(normalized, "/arm/")
}

// ParseFile recovers parser panics into a per-file failure.
func (p *APIMParser) ParseFile(filePath string, content []byte) ParsedFile {
	return parseGuarded(filePath, "apim", func() ParsedFile { return p.parseFile(filePath, content) })
}

func (p *APIMParser) parseFile(filePath string, content []byte) ParsedFile {
	result := ParsedFile{
		Path:          filePath,
		Language:      "apim",
		Functions:     []ParsedFunction{},
		Classes:       []ParsedClass{},
		Imports:       []ParsedImport{},
		Endpoints:     []ParsedEndpoint{},
		FunctionCalls: make(map[string][]ParsedFunctionCall),
		HttpCalls:     make(map[string][]ParsedHttpCall),
		GatewayRoutes: []ParsedGatewayRoute{},
	}

	if !looksLikeAPIMTemplate(content) {
		result.Language = "json"
		return result
	}

	clean := stripJSONComments(content)
	var tpl apimTemplate
	if err := json.Unmarshal(clean, &tpl); err != nil {
		result.ParseDiagnostics = ParseDiagnostics{
			FailureKind: ParseFailureSyntaxUnsupported,
			Message:     err.Error(),
		}
		return result
	}

	apis := make(map[string]apimAPI)
	operations := make(map[string]apimOperation)
	apiPolicies := make(map[string]apimPolicy)
	operationPolicies := make(map[string]apimPolicy)
	contentStr := string(content)

	for _, resource := range tpl.Resources {
		switch resource.Type {
		case "Microsoft.ApiManagement/service/apis":
			var props apimAPIProperties
			if err := json.Unmarshal(resource.Properties, &props); err != nil {
				continue
			}
			segments := apimNameSegments(resource.Name)
			if len(segments) < 1 {
				continue
			}
			apiName := segments[len(segments)-1]
			apis[apiName] = apimAPI{
				Name: apiName,
				Path: normalizeGatewayPath(props.Path),
				Line: resourceLineNumber(contentStr, resource.Name),
			}
		case "Microsoft.ApiManagement/service/apis/operations":
			var props apimOperationProperties
			if err := json.Unmarshal(resource.Properties, &props); err != nil {
				continue
			}
			segments := apimNameSegments(resource.Name)
			if len(segments) < 2 {
				continue
			}
			apiName := segments[len(segments)-2]
			opName := segments[len(segments)-1]
			operations[apiName+"|"+opName] = apimOperation{
				APIName:       apiName,
				OperationName: opName,
				Method:        strings.ToUpper(strings.TrimSpace(props.Method)),
				URLTemplate:   normalizeGatewayPath(props.URLTemplate),
				Line:          resourceLineNumber(contentStr, resource.Name),
			}
		case "Microsoft.ApiManagement/service/apis/policies":
			var props apimPolicyProperties
			if err := json.Unmarshal(resource.Properties, &props); err != nil {
				continue
			}
			segments := apimNameSegments(resource.Name)
			if len(segments) < 2 {
				continue
			}
			apiName := segments[len(segments)-2]
			apiPolicies[apiName] = parseAPIMPolicy(apiName, "", props.Value)
		case "Microsoft.ApiManagement/service/apis/operations/policies":
			var props apimPolicyProperties
			if err := json.Unmarshal(resource.Properties, &props); err != nil {
				continue
			}
			segments := apimNameSegments(resource.Name)
			if len(segments) < 3 {
				continue
			}
			apiName := segments[len(segments)-3]
			opName := segments[len(segments)-2]
			operationPolicies[apiName+"|"+opName] = parseAPIMPolicy(apiName, opName, props.Value)
		case "Microsoft.Logic/workflows/triggers":
			trigger := parseLogicWorkflowTrigger(resource, contentStr)
			if trigger.FunctionName != "" {
				result.AzureTriggers = append(result.AzureTriggers, trigger)
			}
		case "Microsoft.Logic/workflows":
			result.AzureTriggers = append(result.AzureTriggers, parseLogicWorkflowDefinitionTriggers(resource, contentStr)...)
		}
	}

	for key, operation := range operations {
		api := apis[operation.APIName]
		policy := mergeAPIMPolicies(apiPolicies[operation.APIName], operationPolicies[key])
		publicPath := normalizeGatewayPath(api.Path + "/" + operation.URLTemplate)
		result.GatewayRoutes = append(result.GatewayRoutes, ParsedGatewayRoute{
			GatewayType:   "azure_apim",
			APIName:       operation.APIName,
			OperationName: operation.OperationName,
			PublicMethod:  firstNonEmpty(operation.Method, policy.BackendMethod, "REQUEST"),
			PublicPath:    publicPath,
			BackendMethod: firstNonEmpty(policy.BackendMethod, operation.Method),
			BackendURL:    strings.TrimSpace(policy.BackendURL),
			BackendPath:   normalizeGatewayPath(policy.BackendPath),
			BackendID:     strings.TrimSpace(policy.BackendID),
			LineNumber:    operation.Line,
		})
	}

	return result
}

func looksLikeAPIMTemplate(content []byte) bool {
	for _, sig := range apimContentSignatureSet {
		if bytes.Contains(content, sig) {
			return true
		}
	}
	return false
}

func stripJSONComments(content []byte) []byte {
	out := make([]byte, 0, len(content))
	inString := false
	escaped := false
	for i := 0; i < len(content); i++ {
		ch := content[i]
		if inString {
			out = append(out, ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		if ch == '"' {
			inString = true
			out = append(out, ch)
			continue
		}
		if ch == '/' && i+1 < len(content) {
			switch content[i+1] {
			case '/':
				i += 2
				for i < len(content) && content[i] != '\n' {
					i++
				}
				if i < len(content) {
					out = append(out, content[i])
				}
				continue
			case '*':
				i += 2
				for i+1 < len(content) && !(content[i] == '*' && content[i+1] == '/') {
					i++
				}
				i++
				continue
			}
		}
		out = append(out, ch)
	}
	return out
}

func apimNameSegments(expr string) []string {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "[") {
		matches := apimSingleQuotedRe.FindAllStringSubmatch(trimmed, -1)
		for i := len(matches) - 1; i >= 0; i-- {
			literal := strings.TrimSpace(matches[i][1])
			if strings.Contains(literal, "/") {
				return splitGatewaySegments(literal)
			}
		}
	}
	return splitGatewaySegments(trimmed)
}

func splitGatewaySegments(path string) []string {
	parts := strings.Split(strings.Trim(strings.TrimSpace(path), "/"), "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func parseAPIMPolicy(apiName, operationName, value string) apimPolicy {
	return apimPolicy{
		APIName:       apiName,
		OperationName: operationName,
		BackendMethod: strings.ToUpper(strings.TrimSpace(firstCapture(apimSetMethodRe, value))),
		BackendURL:    strings.TrimSpace(firstCapture(apimSetBackendURLRe, value)),
		BackendPath:   strings.TrimSpace(firstCapture(apimRewriteURIRe, value)),
		BackendID:     strings.TrimSpace(firstCapture(apimSetBackendIDRe, value)),
	}
}

func mergeAPIMPolicies(apiPolicy, operationPolicy apimPolicy) apimPolicy {
	merged := apiPolicy
	if strings.TrimSpace(operationPolicy.BackendMethod) != "" {
		merged.BackendMethod = operationPolicy.BackendMethod
	}
	if strings.TrimSpace(operationPolicy.BackendURL) != "" {
		merged.BackendURL = operationPolicy.BackendURL
	}
	if strings.TrimSpace(operationPolicy.BackendPath) != "" {
		merged.BackendPath = operationPolicy.BackendPath
	}
	if strings.TrimSpace(operationPolicy.BackendID) != "" {
		merged.BackendID = operationPolicy.BackendID
	}
	return merged
}

func parseLogicWorkflowTrigger(resource apimResource, content string) ParsedAzureTrigger {
	segments := apimNameSegments(resource.Name)
	if len(segments) < 2 {
		return ParsedAzureTrigger{}
	}
	workflow := segments[len(segments)-2]
	triggerName := segments[len(segments)-1]
	var props logicTriggerProperties
	if err := json.Unmarshal(resource.Properties, &props); err != nil {
		return ParsedAzureTrigger{}
	}
	if queue := extractLogicServiceBusQueueName(props.Type, props.Inputs, resource.Properties); queue != "" {
		return ParsedAzureTrigger{
			FunctionName: workflow + "/" + triggerName,
			TriggerType:  "serviceBusTrigger",
			Direction:    "in",
			BindingName:  triggerName,
			ResourceName: queue,
			LineNumber:   resourceLineNumber(content, resource.Name),
		}
	}
	if len(bytes.TrimSpace(props.Recurrence)) == 0 {
		return ParsedAzureTrigger{}
	}
	schedule := formatLogicRecurrence(props.Recurrence)
	return ParsedAzureTrigger{
		FunctionName: workflow + "/" + triggerName,
		TriggerType:  "timerTrigger",
		Direction:    "in",
		BindingName:  triggerName,
		Schedule:     schedule,
		ResourceName: workflow,
		LineNumber:   resourceLineNumber(content, resource.Name),
	}
}

func parseLogicWorkflowDefinitionTriggers(resource apimResource, content string) []ParsedAzureTrigger {
	segments := apimNameSegments(resource.Name)
	if len(segments) < 1 {
		return nil
	}
	workflow := segments[len(segments)-1]
	var props logicWorkflowProperties
	if err := json.Unmarshal(resource.Properties, &props); err != nil {
		return nil
	}
	if len(props.Definition.Triggers) == 0 {
		return nil
	}
	names := make([]string, 0, len(props.Definition.Triggers))
	for name := range props.Definition.Triggers {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []ParsedAzureTrigger
	for _, triggerName := range names {
		trigger := props.Definition.Triggers[triggerName]
		if queue := extractLogicServiceBusQueueName(trigger.Type, trigger.Inputs, nil); queue != "" {
			line := resourceLineNumber(content, triggerName)
			if line <= 1 {
				line = resourceLineNumber(content, resource.Name)
			}
			out = append(out, ParsedAzureTrigger{
				FunctionName: workflow + "/" + triggerName,
				TriggerType:  "serviceBusTrigger",
				Direction:    "in",
				BindingName:  triggerName,
				ResourceName: queue,
				LineNumber:   line,
			})
			continue
		}
		if len(bytes.TrimSpace(trigger.Recurrence)) == 0 {
			continue
		}
		line := resourceLineNumber(content, triggerName)
		if line <= 1 {
			line = resourceLineNumber(content, resource.Name)
		}
		out = append(out, ParsedAzureTrigger{
			FunctionName: workflow + "/" + triggerName,
			TriggerType:  "timerTrigger",
			Direction:    "in",
			BindingName:  triggerName,
			Schedule:     formatLogicRecurrence(trigger.Recurrence),
			ResourceName: workflow,
			LineNumber:   line,
		})
	}
	return out
}

func extractLogicServiceBusQueueName(triggerType string, inputs json.RawMessage, fallback json.RawMessage) string {
	rawInputs := strings.TrimSpace(string(inputs))
	rawFallback := strings.TrimSpace(string(fallback))
	raw := strings.Join([]string{triggerType, rawInputs, rawFallback}, " ")
	lower := strings.ToLower(raw)
	if !strings.Contains(lower, "servicebus") && !strings.Contains(lower, "service-bus") && !strings.Contains(lower, "service_bus") {
		return ""
	}
	for _, candidate := range logicJSONValuesForKeys(inputs, "queueName", "queue", "topicName", "topic") {
		if normalized := normalizeLogicQueueCandidate(candidate); normalized != "" {
			return normalized
		}
	}
	for _, candidate := range logicJSONValuesForKeys(fallback, "queueName", "queue", "topicName", "topic") {
		if normalized := normalizeLogicQueueCandidate(candidate); normalized != "" {
			return normalized
		}
	}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?i)queues?\(['"]([^'"]+)['"]\)`),
		regexp.MustCompile(`(?i)topics?\(['"]([^'"]+)['"]\)`),
		regexp.MustCompile(`(?i)/(?:queues|topics)/([^/?'"]+)`),
	} {
		if match := re.FindStringSubmatch(raw); len(match) > 1 {
			if normalized := normalizeLogicQueueCandidate(match[1]); normalized != "" {
				return normalized
			}
		}
	}
	return ""
}

func logicJSONValuesForKeys(raw json.RawMessage, keys ...string) []string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	keySet := map[string]bool{}
	for _, key := range keys {
		keySet[strings.ToLower(key)] = true
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch typed := v.(type) {
		case map[string]any:
			for key, child := range typed {
				if keySet[strings.ToLower(key)] {
					if str, ok := child.(string); ok {
						out = append(out, str)
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return out
}

func normalizeLogicQueueCandidate(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "@") || strings.Contains(value, "$") {
		return ""
	}
	value = strings.Trim(value, "'\"` ")
	value = strings.TrimPrefix(value, "/")
	value = strings.TrimSuffix(value, "/")
	if value == "" || strings.Contains(value, " ") {
		return ""
	}
	return value
}

func formatLogicRecurrence(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var recurrence logicRecurrence
	if err := json.Unmarshal(raw, &recurrence); err != nil {
		return compactJSON(raw)
	}
	frequency := strings.TrimSpace(recurrence.Frequency)
	interval := strings.TrimSpace(fmtAny(recurrence.Interval))
	var parts []string
	if frequency != "" {
		parts = append(parts, "frequency="+frequency)
	}
	if interval != "" {
		parts = append(parts, "interval="+interval)
	}
	if len(bytes.TrimSpace(recurrence.Schedule)) > 0 {
		parts = append(parts, "schedule="+compactJSON(recurrence.Schedule))
	}
	if len(parts) == 0 {
		return compactJSON(raw)
	}
	return strings.Join(parts, " ")
}

func compactJSON(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

func fmtAny(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func firstCapture(re *regexp.Regexp, value string) string {
	match := re.FindStringSubmatch(value)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func resourceLineNumber(content, name string) int {
	line := 1
	if idx := strings.Index(content, name); idx >= 0 {
		line += strings.Count(content[:idx], "\n")
	}
	return line
}

func normalizeGatewayPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, "//", "/")
	parts := strings.Split(trimmed, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return "/"
	}
	return "/" + strings.Join(out, "/")
}
