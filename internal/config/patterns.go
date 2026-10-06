package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sergiumoraru/tirion/internal/runtimeconfig"
	"gopkg.in/yaml.v3"
)

// PatternsConfig holds custom HTTP client patterns and settings
type PatternsConfig struct {
	loadError            error
	Disable              []string                `yaml:"disable"`                // names of built-in patterns to disable
	HttpClients          []HttpClientPattern     `yaml:"http_clients"`           // custom HTTP client patterns
	ContextPathVariables []string                `yaml:"context_path_variables"` // additional context path variables
	JavaSQS              JavaSQSFramework        `yaml:"java_sqs"`
	GraphQLRegistrations []GraphQLRegistration   `yaml:"graphql_registrations"`
	JavaScriptQueues     JavaScriptQueuePatterns `yaml:"javascript_queues"`
}

// JavaScriptQueuePatterns describes deployment-specific queue wrappers.
// Load receivers are exact source expressions; no resource-name prefix is inferred.
type JavaScriptQueuePatterns struct {
	LoadReceivers []string `yaml:"load_receivers"`
	SendMethods   []string `yaml:"send_methods"`
	ReadMethods   []string `yaml:"read_methods"`
	DeleteMethods []string `yaml:"delete_methods"`
}

// GraphQLRegistration identifies an application's resolver-registration wrapper.
// These are explicit deployment conventions, not GraphQL language constructs.
type GraphQLRegistration struct {
	Receiver           string `yaml:"receiver"`
	Method             string `yaml:"method"`
	ControllersPathKey string `yaml:"controllers_path_key"`
}

// JavaSQSFramework describes a deployment's custom queue injection framework.
// These conventions are not part of the AWS SDK and have no built-in defaults.
type JavaSQSFramework struct {
	QueueAnnotation    string   `yaml:"queue_annotation"`
	QueueEnum          string   `yaml:"queue_enum"`
	ConsumerTypes      []string `yaml:"consumer_types"`
	HandlerMethod      string   `yaml:"handler_method"`
	ConstantSuffix     string   `yaml:"constant_suffix"`
	QueueURLFields     []string `yaml:"queue_url_fields"`
	SendMethods        []string `yaml:"send_methods"`
	PropertyAnnotation string   `yaml:"property_annotation"`
}

func (c JavaSQSFramework) MatchesConsumerType(types []string) bool {
	if strings.TrimSpace(c.HandlerMethod) == "" {
		return false
	}
	for _, typ := range types {
		typ = strings.TrimSpace(strings.SplitN(typ, "<", 2)[0])
		for _, configured := range c.ConsumerTypes {
			configured = strings.TrimSpace(configured)
			if configured != "" && (typ == configured || strings.HasSuffix(typ, "."+configured)) {
				return true
			}
		}
	}
	return false
}

// HttpClientPattern defines how to detect an HTTP client
type HttpClientPattern struct {
	Name     string            `yaml:"name"`     // identifier for this pattern (e.g., "axios", "RestTemplate")
	Language string            `yaml:"language"` // "javascript" or "java"
	Objects  []string          `yaml:"objects"`  // for JS: variable/object names to match (e.g., ["axios", "$http"])
	Contains []string          `yaml:"contains"` // for Java: strings to match in invocation text
	Methods  map[string]string `yaml:"methods"`  // method name -> HTTP verb (e.g., "get" -> "GET")
}

// LoadPatterns loads custom patterns from ~/.tirion/patterns.yaml
// Returns nil if file doesn't exist (not an error)
func LoadPatterns() (*PatternsConfig, error) {
	configPath, err := runtimeconfig.UserPath("patterns.yaml")
	if err != nil {
		return nil, fmt.Errorf("locate patterns configuration: %w", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no config file, use defaults
		}
		return nil, fmt.Errorf("read patterns configuration: %w", err)
	}

	var config PatternsConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil && err != io.EOF {
		return nil, fmt.Errorf("invalid patterns configuration %s: check YAML syntax and supported keys", configPath)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("invalid patterns configuration %s: expected one YAML document", configPath)
	}
	for i, client := range config.HttpClients {
		if client.Language != "java" && client.Language != "javascript" {
			return nil, fmt.Errorf("invalid patterns configuration %s: http_clients[%d].language must be java or javascript", configPath, i)
		}
		for _, verb := range client.Methods {
			switch verb {
			case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "CONNECT", "TRACE", "REQUEST", "ANY":
			default:
				return nil, fmt.Errorf("invalid patterns configuration %s: http_clients[%d].methods contains an unsupported HTTP verb", configPath, i)
			}
		}
	}

	return &config, nil
}

// GetEffectivePatterns returns the merged configuration (defaults + custom, minus disabled)
func GetEffectivePatterns() *PatternsConfig {
	custom, err := LoadPatterns()
	result := MergePatterns(custom)
	result.loadError = err
	return result
}

// Err distinguishes absent optional configuration from invalid supplied input.
// Callers must check it before extracting or replacing facts.
func (c *PatternsConfig) Err() error { return c.loadError }

// MergePatterns combines custom patterns with defaults and applies disable list
func MergePatterns(custom *PatternsConfig) *PatternsConfig {
	result := &PatternsConfig{
		HttpClients:          make([]HttpClientPattern, 0),
		ContextPathVariables: make([]string, len(DefaultContextPathVariables)),
	}

	// Start with default context path variables
	copy(result.ContextPathVariables, DefaultContextPathVariables)

	// Build disable set
	disabled := make(map[string]bool)
	if custom != nil {
		for _, name := range custom.Disable {
			disabled[name] = true
		}
	}

	// Add default patterns (unless disabled)
	for _, pattern := range DefaultHttpClients {
		if !disabled[pattern.Name] {
			result.HttpClients = append(result.HttpClients, pattern)
		}
	}

	// Add custom patterns
	if custom != nil {
		result.HttpClients = append(result.HttpClients, custom.HttpClients...)
		result.JavaSQS = custom.JavaSQS
		result.JavaScriptQueues = custom.JavaScriptQueues
		result.GraphQLRegistrations = append([]GraphQLRegistration(nil), custom.GraphQLRegistrations...)

		// Add custom context path variables
		result.ContextPathVariables = append(result.ContextPathVariables, custom.ContextPathVariables...)
	}

	return result
}

// IsContextPathVariable checks if a variable name is a known context path prefix
func (c *PatternsConfig) IsContextPathVariable(varName string) bool {
	for _, cpv := range c.ContextPathVariables {
		if varName == cpv {
			return true
		}
	}
	return false
}

// GetJavaScriptPatterns returns only JavaScript patterns
func (c *PatternsConfig) GetJavaScriptPatterns() []HttpClientPattern {
	var patterns []HttpClientPattern
	for _, p := range c.HttpClients {
		if p.Language == "javascript" {
			patterns = append(patterns, p)
		}
	}
	return patterns
}

// GetJavaPatterns returns only Java patterns
func (c *PatternsConfig) GetJavaPatterns() []HttpClientPattern {
	var patterns []HttpClientPattern
	for _, p := range c.HttpClients {
		if p.Language == "java" {
			patterns = append(patterns, p)
		}
	}
	return patterns
}
