package parser

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

type AzureFunctionsParser struct{}

type azureFunctionConfig struct {
	Bindings   []azureBinding `json:"bindings"`
	ScriptFile string         `json:"scriptFile"`
}

type azureBinding struct {
	Type          string   `json:"type"`
	Direction     string   `json:"direction"`
	Name          string   `json:"name"`
	Route         string   `json:"route"`
	Methods       []string `json:"methods"`
	Schedule      string   `json:"schedule"`
	QueueName     string   `json:"queueName"`
	TopicName     string   `json:"topicName"`
	Subscription  string   `json:"subscriptionName"`
	Connection    string   `json:"connection"`
	AuthLevel     string   `json:"authLevel"`
	DatabaseName  string   `json:"databaseName"`
	Collection    string   `json:"collectionName"`
	ContainerName string   `json:"containerName"`
}

func NewAzureFunctionsParser() *AzureFunctionsParser {
	return &AzureFunctionsParser{}
}

func (p *AzureFunctionsParser) CanParse(filePath string) bool {
	return strings.EqualFold(filepath.Base(filePath), "function.json")
}

// ParseFile recovers parser panics into a per-file failure.
func (p *AzureFunctionsParser) ParseFile(filePath string, content []byte) ParsedFile {
	return parseGuarded(filePath, "azure-functions", func() ParsedFile { return p.parseFile(filePath, content) })
}

func (p *AzureFunctionsParser) parseFile(filePath string, content []byte) ParsedFile {
	result := ParsedFile{
		Path:          filePath,
		Language:      "azure-functions",
		Functions:     []ParsedFunction{},
		Classes:       []ParsedClass{},
		Imports:       []ParsedImport{},
		Endpoints:     []ParsedEndpoint{},
		FunctionCalls: make(map[string][]ParsedFunctionCall),
		HttpCalls:     make(map[string][]ParsedHttpCall),
		AzureTriggers: []ParsedAzureTrigger{},
	}

	var cfg azureFunctionConfig
	if err := json.Unmarshal(content, &cfg); err != nil {
		result.ParseDiagnostics = ParseDiagnostics{
			FailureKind: ParseFailureSyntaxUnsupported,
			Message:     err.Error(),
		}
		return result
	}

	functionName := filepath.Base(filepath.Dir(filePath))
	contentStr := string(content)

	for _, binding := range cfg.Bindings {
		if binding.Type == "" {
			continue
		}

		line := 1
		if idx := strings.Index(contentStr, `"`+binding.Type+`"`); idx >= 0 {
			line += strings.Count(contentStr[:idx], "\n")
		}

		if !isAzureTriggerBinding(binding.Type, binding.Direction) {
			continue
		}

		trigger := ParsedAzureTrigger{
			FunctionName: functionName,
			TriggerType:  binding.Type,
			Direction:    binding.Direction,
			BindingName:  binding.Name,
			Route:        binding.Route,
			AuthLevel:    binding.AuthLevel,
			Connection:   binding.Connection,
			Schedule:     binding.Schedule,
			ResourceName: azureBindingResourceName(binding),
			ScriptFile:   cfg.ScriptFile,
			LineNumber:   line,
		}
		if len(binding.Methods) > 0 {
			trigger.Methods = make([]string, 0, len(binding.Methods))
			for _, method := range binding.Methods {
				trigger.Methods = append(trigger.Methods, strings.ToUpper(strings.TrimSpace(method)))
			}
		}
		if binding.Type != "httpTrigger" || !strings.EqualFold(binding.Direction, "in") {
			result.AzureTriggers = append(result.AzureTriggers, trigger)
			continue
		}

		path := normalizeAzureHTTPRoute(functionName, binding.Route, "api")
		trigger.Route = path
		result.AzureTriggers = append(result.AzureTriggers, trigger)
		methods := trigger.Methods
		if len(methods) == 0 {
			methods = []string{"REQUEST"}
		}
		for _, method := range methods {
			result.Endpoints = append(result.Endpoints, ParsedEndpoint{
				Path:        path,
				Method:      method,
				HandlerName: functionName,
				LineNumber:  line,
			})
		}
	}

	return result
}

func isAzureTriggerBinding(bindingType, direction string) bool {
	if !strings.EqualFold(strings.TrimSpace(direction), "in") {
		return false
	}
	switch strings.TrimSpace(bindingType) {
	case "httpTrigger", "timerTrigger", "queueTrigger", "serviceBusTrigger", "blobTrigger", "eventHubTrigger", "cosmosDBTrigger":
		return true
	default:
		return strings.HasSuffix(strings.TrimSpace(bindingType), "Trigger")
	}
}

func normalizeAzureHTTPRoute(functionName, route, routePrefix string) string {
	prefix := strings.Trim(strings.TrimSpace(routePrefix), "/")
	if strings.TrimSpace(route) != "" {
		normalizedRoute := strings.Trim(strings.TrimSpace(route), "/")
		if prefix == "" {
			return ensureLeadingSlash(normalizedRoute)
		}
		return ensureLeadingSlash(prefix + "/" + normalizedRoute)
	}
	if prefix == "" {
		return "/" + functionName
	}
	return "/" + prefix + "/" + functionName
}

func ensureLeadingSlash(path string) string {
	if path == "" {
		return path
	}
	if strings.HasPrefix(path, "/") {
		return path
	}
	return "/" + path
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func azureBindingResourceName(binding azureBinding) string {
	if strings.EqualFold(strings.TrimSpace(binding.Type), "serviceBusTrigger") {
		topic := strings.TrimSpace(binding.TopicName)
		subscription := strings.TrimSpace(binding.Subscription)
		if topic != "" && subscription != "" {
			return topic + "/subscriptions/" + subscription
		}
	}
	return firstNonEmpty(binding.QueueName, binding.TopicName, binding.Subscription, binding.DatabaseName, binding.Collection, binding.ContainerName)
}
