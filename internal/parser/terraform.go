package parser

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/hcl"
)

type TerraformParser struct {
	parser *sitter.Parser
}

func NewTerraformParser() *TerraformParser {
	parser := sitter.NewParser()
	parser.SetLanguage(hcl.GetLanguage())
	return &TerraformParser{parser: parser}
}

func (p *TerraformParser) ParseFile(path string, content []byte) (result ParsedFile) {
	result = ParsedFile{
		Path:          path,
		Language:      "terraform",
		Functions:     []ParsedFunction{},
		Classes:       []ParsedClass{},
		Imports:       []ParsedImport{},
		Endpoints:     []ParsedEndpoint{},
		FunctionCalls: make(map[string][]ParsedFunctionCall),
		HttpCalls:     make(map[string][]ParsedHttpCall),
	}
	defer recoverParsePanic(&result)

	tree, diagnostics := parseWithTimeout(p.parser, content)
	result.ParseDiagnostics = diagnostics
	if tree == nil {
		return result
	}
	defer tree.Close()

	rootNode := tree.RootNode()
	if rootNode == nil {
		result.ParseDiagnostics = ParseDiagnostics{
			FailureKind: ParseFailureSyntaxUnsupported,
			Message:     "missing root node",
		}
		return result
	}
	if rootNode.HasError() {
		result.ParseDiagnostics = syntaxErrorDiagnostics()
	}
	p.extractNodes(rootNode, content, &result)

	return result
}

func (p *TerraformParser) extractNodes(node *sitter.Node, content []byte, result *ParsedFile) {
	nodeType := node.Type()

	switch nodeType {
	case "block":
		p.extractBlock(node, content, result)
	}

	// Recurse into children
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		p.extractNodes(child, content, result)
	}
}

func (p *TerraformParser) extractBlock(node *sitter.Node, content []byte, result *ParsedFile) {
	startLine := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1

	var blockType string
	var blockLabels []string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "identifier":
			if blockType == "" {
				blockType = child.Content(content)
			}
		case "string_lit":
			label := strings.Trim(child.Content(content), "\"")
			blockLabels = append(blockLabels, label)
		}
	}

	if blockType == "" {
		return
	}

	// Build a name from block type and labels
	name := blockType
	if len(blockLabels) > 0 {
		name = blockType + "." + strings.Join(blockLabels, ".")
	}

	sourceCode := string(content[node.StartByte():node.EndByte()])
	if len(sourceCode) > 2000 {
		sourceCode = sourceCode[:2000] + "..."
	}

	// Map Terraform blocks to our model:
	// - resource, data, module -> Classes (they define infrastructure objects)
	// - variable, output, locals -> Functions (they define values/interfaces)
	// - provider, terraform -> Imports (they define dependencies)

	switch blockType {
	case "resource", "data", "module":
		// Treat as "class" - a defined infrastructure component
		result.Classes = append(result.Classes, ParsedClass{
			Name:       name,
			StartLine:  startLine,
			EndLine:    endLine,
			IsExported: true,
		})

		// Also extract as function for searchability
		result.Functions = append(result.Functions, ParsedFunction{
			Name:       name,
			StartLine:  startLine,
			EndLine:    endLine,
			IsExported: true,
			SourceCode: sourceCode,
		})

		// Extract references to other resources
		p.extractReferences(node, content, result, name)

	case "variable", "output":
		// Variables and outputs are like function interfaces
		result.Functions = append(result.Functions, ParsedFunction{
			Name:       name,
			StartLine:  startLine,
			EndLine:    endLine,
			IsExported: blockType == "output",
			SourceCode: sourceCode,
		})

	case "locals":
		// Local values
		result.Functions = append(result.Functions, ParsedFunction{
			Name:       name,
			StartLine:  startLine,
			EndLine:    endLine,
			IsExported: false,
			SourceCode: sourceCode,
		})

	case "provider":
		// Provider is like an import
		if len(blockLabels) > 0 {
			result.Imports = append(result.Imports, ParsedImport{
				Path:  blockLabels[0],
				Names: []string{"provider"},
			})
		}

	case "terraform":
		// Terraform config block - extract required_providers
		p.extractRequiredProviders(node, content, result)
	}
}

func (p *TerraformParser) extractReferences(node *sitter.Node, content []byte, result *ParsedFile, currentBlock string) {
	// Look for references like aws_instance.foo, var.bar, data.aws_ami.ubuntu
	nodeType := node.Type()

	if nodeType == "expression" || nodeType == "get_attr" {
		text := node.Content(content)
		// Check if it looks like a resource reference
		if strings.Contains(text, ".") && !strings.HasPrefix(text, "\"") {
			parts := strings.Split(text, ".")
			if len(parts) >= 2 {
				// Could be: resource_type.name, var.name, data.type.name, local.name, module.name
				refType := parts[0]
				if refType == "var" || refType == "local" || refType == "data" || refType == "module" {
					calleeName := text
					if result.FunctionCalls[currentBlock] == nil {
						result.FunctionCalls[currentBlock] = []ParsedFunctionCall{}
					}
					// Check for duplicates
					isDupe := false
					for _, call := range result.FunctionCalls[currentBlock] {
						if call.CalleeName == calleeName {
							isDupe = true
							break
						}
					}
					if !isDupe {
						result.FunctionCalls[currentBlock] = append(result.FunctionCalls[currentBlock], ParsedFunctionCall{
							CalleeName: calleeName,
							LineNumber: int(node.StartPoint().Row) + 1,
						})
					}
				} else if isResourceType(refType) {
					// Direct resource reference like aws_instance.web
					calleeName := "resource." + text
					if result.FunctionCalls[currentBlock] == nil {
						result.FunctionCalls[currentBlock] = []ParsedFunctionCall{}
					}
					isDupe := false
					for _, call := range result.FunctionCalls[currentBlock] {
						if call.CalleeName == calleeName {
							isDupe = true
							break
						}
					}
					if !isDupe {
						result.FunctionCalls[currentBlock] = append(result.FunctionCalls[currentBlock], ParsedFunctionCall{
							CalleeName: calleeName,
							LineNumber: int(node.StartPoint().Row) + 1,
						})
					}
				}
			}
		}
	}

	// Recurse
	for i := 0; i < int(node.ChildCount()); i++ {
		p.extractReferences(node.Child(i), content, result, currentBlock)
	}
}

func (p *TerraformParser) extractRequiredProviders(node *sitter.Node, content []byte, result *ParsedFile) {
	// Look for required_providers block inside terraform block
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "body" {
			for j := 0; j < int(child.ChildCount()); j++ {
				bodyChild := child.Child(j)
				if bodyChild.Type() == "block" {
					// Check if this is required_providers
					for k := 0; k < int(bodyChild.ChildCount()); k++ {
						blockChild := bodyChild.Child(k)
						if blockChild.Type() == "identifier" && blockChild.Content(content) == "required_providers" {
							// Extract provider names
							p.extractProviderNames(bodyChild, content, result)
						}
					}
				}
			}
		}
	}
}

func (p *TerraformParser) extractProviderNames(node *sitter.Node, content []byte, result *ParsedFile) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "body" {
			for j := 0; j < int(child.ChildCount()); j++ {
				bodyChild := child.Child(j)
				if bodyChild.Type() == "attribute" {
					// First identifier is the provider name
					for k := 0; k < int(bodyChild.ChildCount()); k++ {
						attrChild := bodyChild.Child(k)
						if attrChild.Type() == "identifier" {
							providerName := attrChild.Content(content)
							result.Imports = append(result.Imports, ParsedImport{
								Path:  providerName,
								Names: []string{"required_provider"},
							})
							break
						}
					}
				}
			}
		}
	}
}

func isResourceType(s string) bool {
	// Common Terraform resource prefixes
	prefixes := []string{
		"aws_", "azurerm_", "google_", "kubernetes_", "helm_",
		"vault_", "consul_", "nomad_", "docker_", "null_",
		"random_", "local_", "template_", "tls_", "http_",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}
