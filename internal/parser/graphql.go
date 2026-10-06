package parser

import (
	"path/filepath"
	"regexp"
	"strings"
)

var graphqlImportPattern = regexp.MustCompile(`(?m)^#import\s+"([^"]+)"`)
var graphqlOperationPattern = regexp.MustCompile(`(?m)\b(query|mutation|subscription)\s+([A-Za-z_][A-Za-z0-9_]*)\b`)

type GraphQLParser struct{}

func NewGraphQLParser() *GraphQLParser {
	return &GraphQLParser{}
}

func (p *GraphQLParser) CanParse(filePath string) bool {
	ext := strings.ToLower(filepath.Ext(filePath))
	return ext == ".gql" || ext == ".graphql"
}

// ParseFile recovers parser panics into a per-file failure.
func (p *GraphQLParser) ParseFile(filePath string, content []byte) ParsedFile {
	return parseGuarded(filePath, "graphql", func() ParsedFile { return p.parseFile(filePath, content) })
}

func (p *GraphQLParser) parseFile(filePath string, content []byte) ParsedFile {
	result := ParsedFile{
		Path:              filePath,
		Language:          "graphql",
		Functions:         []ParsedFunction{},
		Classes:           []ParsedClass{},
		Imports:           []ParsedImport{},
		Endpoints:         []ParsedEndpoint{},
		FunctionCalls:     make(map[string][]ParsedFunctionCall),
		HttpCalls:         make(map[string][]ParsedHttpCall),
		GraphQLOperations: []ParsedGraphQLOperation{},
	}

	contentStr := string(content)

	for _, match := range graphqlImportPattern.FindAllStringSubmatchIndex(contentStr, -1) {
		if len(match) < 4 {
			continue
		}
		path := contentStr[match[2]:match[3]]
		result.Imports = append(result.Imports, ParsedImport{Path: path})
	}

	for _, match := range graphqlOperationPattern.FindAllStringSubmatchIndex(contentStr, -1) {
		if len(match) < 6 {
			continue
		}
		opType := strings.ToLower(contentStr[match[2]:match[3]])
		name := contentStr[match[4]:match[5]]
		line := 1 + strings.Count(contentStr[:match[0]], "\n")
		result.GraphQLOperations = append(result.GraphQLOperations, ParsedGraphQLOperation{
			Name:          name,
			OperationType: opType,
			LineNumber:    line,
		})
	}

	return result
}
