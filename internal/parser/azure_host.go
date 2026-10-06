package parser

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

type AzureHostParser struct{}

type azureHostConfigFile struct {
	Extensions struct {
		HTTP struct {
			RoutePrefix *string `json:"routePrefix"`
		} `json:"http"`
	} `json:"extensions"`
}

func NewAzureHostParser() *AzureHostParser {
	return &AzureHostParser{}
}

func (p *AzureHostParser) CanParse(filePath string) bool {
	return strings.EqualFold(filepath.Base(filePath), "host.json")
}

// ParseFile recovers parser panics into a per-file failure.
func (p *AzureHostParser) ParseFile(filePath string, content []byte) ParsedFile {
	return parseGuarded(filePath, "azure-functions-host", func() ParsedFile { return p.parseFile(filePath, content) })
}

func (p *AzureHostParser) parseFile(filePath string, content []byte) ParsedFile {
	result := ParsedFile{
		Path:             filePath,
		Language:         "azure-functions-host",
		Functions:        []ParsedFunction{},
		Classes:          []ParsedClass{},
		Imports:          []ParsedImport{},
		Endpoints:        []ParsedEndpoint{},
		FunctionCalls:    make(map[string][]ParsedFunctionCall),
		HttpCalls:        make(map[string][]ParsedHttpCall),
		AzureHostConfigs: []ParsedAzureHostConfig{},
	}

	var cfg azureHostConfigFile
	if err := json.Unmarshal(content, &cfg); err != nil {
		result.ParseDiagnostics = ParseDiagnostics{
			FailureKind: ParseFailureSyntaxUnsupported,
			Message:     err.Error(),
		}
		return result
	}

	routePrefix := "api"
	if cfg.Extensions.HTTP.RoutePrefix != nil {
		routePrefix = strings.TrimSpace(*cfg.Extensions.HTTP.RoutePrefix)
	}

	line := 1
	contentStr := string(content)
	if idx := strings.Index(contentStr, "routePrefix"); idx >= 0 {
		line += strings.Count(contentStr[:idx], "\n")
	}

	result.AzureHostConfigs = append(result.AzureHostConfigs, ParsedAzureHostConfig{
		RoutePrefix: routePrefix,
		LineNumber:  line,
	})
	return result
}
