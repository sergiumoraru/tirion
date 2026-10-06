package parser

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
)

type GoParser struct {
	parser *sitter.Parser
}

func NewGoParser() *GoParser {
	parser := sitter.NewParser()
	parser.SetLanguage(golang.GetLanguage())
	return &GoParser{parser: parser}
}

func (p *GoParser) ParseFile(path string, content []byte) (result ParsedFile) {
	result = ParsedFile{
		Path:          path,
		Language:      "go",
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
	p.extractNodes(rootNode, content, &result, "", "")

	return result
}

func (p *GoParser) extractNodes(node *sitter.Node, content []byte, result *ParsedFile, currentPackage string, currentFunc string) {
	nodeType := node.Type()

	switch nodeType {
	case "package_clause":
		// Extract package name
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child.Type() == "package_identifier" {
				currentPackage = child.Content(content)
			}
		}

	case "import_declaration":
		p.extractImports(node, content, result)

	case "function_declaration":
		fn := p.extractFunction(node, content, currentPackage)
		if fn != nil {
			result.Functions = append(result.Functions, *fn)
			// Extract calls within this function
			funcName := fn.Name
			p.extractCalls(node, content, result, funcName)
		}

	case "method_declaration":
		fn := p.extractMethod(node, content, currentPackage)
		if fn != nil {
			result.Functions = append(result.Functions, *fn)
			// Extract calls within this method
			funcName := fn.Name
			p.extractCalls(node, content, result, funcName)
		}

	case "type_declaration":
		p.extractTypes(node, content, result)

	case "call_expression":
		if currentFunc != "" {
			p.extractCallExpr(node, content, result, currentFunc)
		}
	}

	// Recurse into children
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		p.extractNodes(child, content, result, currentPackage, currentFunc)
	}
}

func (p *GoParser) extractFunction(node *sitter.Node, content []byte, pkg string) *ParsedFunction {
	var name string
	var params []string
	var returnType string
	startLine := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "identifier":
			name = child.Content(content)
		case "parameter_list":
			params = p.extractParams(child, content)
		case "type_identifier", "pointer_type", "slice_type", "map_type", "interface_type":
			returnType = child.Content(content)
		case "result":
			returnType = child.Content(content)
		}
	}

	if name == "" {
		return nil
	}

	// Check if exported (starts with uppercase)
	isExported := len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z'

	sourceCode := string(content[node.StartByte():node.EndByte()])
	if len(sourceCode) > 2000 {
		sourceCode = sourceCode[:2000] + "..."
	}

	return &ParsedFunction{
		Name:       name,
		StartLine:  startLine,
		EndLine:    endLine,
		Params:     params,
		ReturnType: returnType,
		IsExported: isExported,
		IsAsync:    false,
		SourceCode: sourceCode,
	}
}

func (p *GoParser) extractMethod(node *sitter.Node, content []byte, pkg string) *ParsedFunction {
	var name string
	var receiverType string
	var params []string
	var returnType string
	startLine := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "parameter_list":
			// First parameter_list is receiver, second is params
			if receiverType == "" {
				receiverType = p.extractReceiverType(child, content)
			} else {
				params = p.extractParams(child, content)
			}
		case "field_identifier":
			name = child.Content(content)
		case "result":
			returnType = child.Content(content)
		}
	}

	if name == "" {
		return nil
	}

	// Format as ReceiverType.MethodName
	fullName := name
	if receiverType != "" {
		fullName = receiverType + "." + name
	}

	isExported := len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z'

	sourceCode := string(content[node.StartByte():node.EndByte()])
	if len(sourceCode) > 2000 {
		sourceCode = sourceCode[:2000] + "..."
	}

	return &ParsedFunction{
		Name:       fullName,
		StartLine:  startLine,
		EndLine:    endLine,
		Params:     params,
		ReturnType: returnType,
		IsExported: isExported,
		IsAsync:    false,
		SourceCode: sourceCode,
	}
}

func (p *GoParser) extractReceiverType(node *sitter.Node, content []byte) string {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		param := node.NamedChild(i)
		if param.Type() != "parameter_declaration" {
			continue
		}
		typ := param.ChildByFieldName("type")
		if typ == nil {
			continue
		}
		return goReceiverBaseName(typ.Content(content))
	}
	return ""
}

// goReceiverBaseName reduces a receiver type expression to its base type name:
// "*Plain", "G[T, U]", "(G[T, U])" and "(*Plain)" yield "Plain" or "G". Go
// allows redundant parentheses around a receiver type.
func goReceiverBaseName(text string) string {
	text = strings.TrimSpace(text)
	for {
		switch {
		case strings.HasPrefix(text, "*"):
			text = strings.TrimSpace(text[1:])
		case strings.HasPrefix(text, "(") && strings.HasSuffix(text, ")"):
			text = strings.TrimSpace(text[1 : len(text)-1])
		default:
			if bracket := strings.IndexByte(text, '['); bracket >= 0 {
				text = text[:bracket]
			}
			return strings.TrimSpace(text)
		}
	}
}

func (p *GoParser) extractParams(node *sitter.Node, content []byte) []string {
	var params []string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "parameter_declaration" {
			var name, paramType string
			for j := 0; j < int(child.ChildCount()); j++ {
				paramChild := child.Child(j)
				switch paramChild.Type() {
				case "identifier":
					name = paramChild.Content(content)
				case "type_identifier", "pointer_type", "slice_type", "map_type", "interface_type", "qualified_type":
					paramType = paramChild.Content(content)
				}
			}
			if name != "" && paramType != "" {
				params = append(params, name+" "+paramType)
			} else if paramType != "" {
				params = append(params, paramType)
			} else if name != "" {
				params = append(params, name)
			}
		}
	}

	return params
}

func (p *GoParser) extractImports(node *sitter.Node, content []byte, result *ParsedFile) {
	// Handle both single imports and import blocks
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "import_spec" {
			p.extractImportSpec(child, content, result)
		} else if child.Type() == "import_spec_list" {
			for j := 0; j < int(child.ChildCount()); j++ {
				specChild := child.Child(j)
				if specChild.Type() == "import_spec" {
					p.extractImportSpec(specChild, content, result)
				}
			}
		}
	}
}

func (p *GoParser) extractImportSpec(node *sitter.Node, content []byte, result *ParsedFile) {
	var alias, path string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "package_identifier", "blank_identifier", "dot":
			alias = child.Content(content)
		case "interpreted_string_literal":
			path = strings.Trim(child.Content(content), "\"")
		}
	}

	if path != "" {
		names := []string{}
		if alias != "" {
			names = append(names, alias)
		}
		result.Imports = append(result.Imports, ParsedImport{
			Path:  path,
			Names: names,
		})
	}
}

func (p *GoParser) extractTypes(node *sitter.Node, content []byte, result *ParsedFile) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "type_spec" {
			p.extractTypeSpec(child, content, result)
		}
	}
}

func (p *GoParser) extractTypeSpec(node *sitter.Node, content []byte, result *ParsedFile) {
	var name string
	var isStruct bool
	startLine := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "type_identifier":
			name = child.Content(content)
		case "struct_type":
			isStruct = true
		case "interface_type":
			isStruct = true // Treat interfaces as classes too
		}
	}

	if name != "" && isStruct {
		isExported := len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z'
		result.Classes = append(result.Classes, ParsedClass{
			Name:       name,
			StartLine:  startLine,
			EndLine:    endLine,
			IsExported: isExported,
		})
	}
}

func (p *GoParser) extractCalls(node *sitter.Node, content []byte, result *ParsedFile, currentFunc string) {
	if node.Type() == "call_expression" {
		p.extractCallExpr(node, content, result, currentFunc)
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		p.extractCalls(node.Child(i), content, result, currentFunc)
	}
}

func (p *GoParser) extractCallExpr(node *sitter.Node, content []byte, result *ParsedFile, currentFunc string) {
	if currentFunc == "" {
		return
	}

	var calleeName string
	lineNumber := int(node.StartPoint().Row) + 1

	// Get the function being called
	if node.ChildCount() > 0 {
		funcNode := node.Child(0)
		switch funcNode.Type() {
		case "identifier":
			calleeName = funcNode.Content(content)
		case "selector_expression":
			// e.g., pkg.Function or obj.Method
			calleeName = funcNode.Content(content)
		case "call_expression":
			// Chained call - skip
			return
		}
	}

	if calleeName != "" && currentFunc != "" {
		// Skip common built-ins
		if isGoBuiltin(calleeName) {
			return
		}

		if result.FunctionCalls[currentFunc] == nil {
			result.FunctionCalls[currentFunc] = []ParsedFunctionCall{}
		}
		result.FunctionCalls[currentFunc] = append(result.FunctionCalls[currentFunc], ParsedFunctionCall{
			CalleeName: calleeName,
			LineNumber: lineNumber,
			IsAsync:    false,
		})
	}
}

func isGoBuiltin(name string) bool {
	builtins := map[string]bool{
		"make": true, "new": true, "len": true, "cap": true,
		"append": true, "copy": true, "delete": true,
		"print": true, "println": true, "panic": true, "recover": true,
		"close": true, "complex": true, "real": true, "imag": true,
	}
	return builtins[name]
}
