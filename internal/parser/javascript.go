package parser

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sergiumoraru/tirion/internal/config"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

var (
	graphQLOperationNamePattern         = regexp.MustCompile(`^[A-Z][A-Za-z0-9_]*$`)
	graphQLResolverOperationNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	anonymousGraphQLOperationPattern    = regexp.MustCompile(`(?is)\b(query|mutation|subscription)\s*(?:\([^)]*\))?\s*\{`)
	graphQLPermissionRuleAssignPattern  = regexp.MustCompile(`\b(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*(?:allow|deny|or|and|not|chain|race|rule|inputRule|shield)\b`)
	contentstackChainCallPattern        = regexp.MustCompile(`(?i)\b(contenttype|content_type|contenttypes|entry|entries|asset|assets|environment|environments|globalfield|global_field|globalfields|webhook|webhooks|extension|extensions)\s*\(\s*([^)]*?)\s*\)`)
	vueContractKeyPattern               = regexp.MustCompile(`^['"]?([A-Za-z_$][A-Za-z0-9_$-]*)['"]?\??\s*:\s*(.+)$`)
	vueEmitParamPattern                 = regexp.MustCompile(`\be\s*:\s*['"]([^'"]+)['"]`)
	vueRuntimeTypePattern               = regexp.MustCompile(`\btype\s*:\s*([A-Za-z_$][A-Za-z0-9_$]*)`)
)

type JavaScriptParser struct {
	routerIdx *routerIndex
	jsParser  *sitter.Parser
	tsParser  *sitter.Parser
	tsxParser *sitter.Parser
	jsLang    *sitter.Language
	tsLang    *sitter.Language
	tsxLang   *sitter.Language
	config    *config.PatternsConfig

	// templatePrefixConstants holds per-file string constants (class fields /
	// module consts assigned a path-like literal, e.g. controllerUrl =
	// '/api/resources/') so a leading `${this.controllerUrl}` in a URL template
	// is resolved to its value instead of being dropped as an opaque prefix.
	// Reset on every ParseFile; the tree-sitter parsers make a parser instance
	// inherently single-file at a time, so this is not shared concurrently.
	templatePrefixConstants map[string]string
}

func NewJavaScriptParser() *JavaScriptParser {
	jsLang := javascript.GetLanguage()
	tsLang := typescript.GetLanguage()
	tsxLang := tsx.GetLanguage()

	jsParser := sitter.NewParser()
	if jsLang != nil {
		jsParser.SetLanguage(jsLang)
	}

	tsParser := sitter.NewParser()
	if tsLang != nil {
		tsParser.SetLanguage(tsLang)
	}

	tsxParser := sitter.NewParser()
	if tsxLang != nil {
		tsxParser.SetLanguage(tsxLang)
	}

	return &JavaScriptParser{
		jsParser:  jsParser,
		tsParser:  tsParser,
		tsxParser: tsxParser,
		jsLang:    jsLang,
		tsLang:    tsLang,
		tsxLang:   tsxLang,
		config:    config.GetEffectivePatterns(),
	}
}

// SetConfig sets custom patterns configuration
func (p *JavaScriptParser) SetConfig(cfg *config.PatternsConfig) {
	p.config = cfg
}

var supportedExtensions = map[string]bool{
	".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".mjs": true, ".cjs": true, ".vue": true,
}

// jsFileExtension is the lower-cased extension, so FOO.JS, App.Vue and X.TSX
// are handled like their lower-case spellings.
func jsFileExtension(filePath string) string {
	return strings.ToLower(filepath.Ext(filePath))
}

func (p *JavaScriptParser) CanParse(filePath string) bool {
	return supportedExtensions[jsFileExtension(filePath)]
}

// getParser picks the grammar for a file. vueLang is the script language of a
// .vue file as reported by extractVueScript.
func (p *JavaScriptParser) getParser(filePath, vueLang string) *sitter.Parser {
	switch jsFileExtension(filePath) {
	case ".tsx":
		return p.tsxParser
	case ".vue":
		if vueLang == "tsx" {
			return p.tsxParser
		}
		return p.tsParser
	case ".ts":
		return p.tsParser
	default:
		return p.jsParser
	}
}

func (p *JavaScriptParser) getLanguage(filePath string) string {
	switch jsFileExtension(filePath) {
	case ".tsx":
		return "tsx"
	case ".ts":
		return "typescript"
	case ".jsx":
		return "jsx"
	case ".vue":
		return "vue"
	default:
		return "javascript"
	}
}

func (p *JavaScriptParser) ParseFile(filePath string, content []byte) (result ParsedFile) {
	result = ParsedFile{
		Path:          filePath,
		Language:      p.getLanguage(filePath),
		Functions:     []ParsedFunction{},
		Classes:       []ParsedClass{},
		Imports:       []ParsedImport{},
		FunctionCalls: make(map[string][]ParsedFunctionCall),
		Endpoints:     []ParsedEndpoint{},
		HttpCalls:     make(map[string][]ParsedHttpCall),
		DataAccesses:  make(map[string][]ParsedDataAccess),
		SqsProducers:  make(map[string][]ParsedSqsProducer),
		// TypeScript/JS specific
		Interfaces:                  []ParsedInterface{},
		TypeAliases:                 []ParsedTypeAlias{},
		HookCalls:                   []ParsedHookCall{},
		GraphQLOperations:           []ParsedGraphQLOperation{},
		GraphQLOperationUsages:      []ParsedGraphQLOperationUsage{},
		GraphQLBackendEntrypoints:   []ParsedGraphQLBackendEntrypoint{},
		GraphQLOperationResolvers:   []ParsedGraphQLOperationResolver{},
		GraphQLOperationPermissions: []ParsedGraphQLOperationPermission{},
	}
	defer recoverParsePanic(&result)
	if err := p.config.Err(); err != nil {
		result.ParseDiagnostics = ParseDiagnostics{FailureKind: ParseFailureInternal, Message: err.Error()}
		return result
	}

	// Extract <script> content from Vue files. Everything else is blanked but
	// newlines are kept, so function start lines map straight onto the .vue file.
	scriptContent, vueLang := content, ""
	isVue := jsFileExtension(filePath) == ".vue"
	if isVue {
		scriptContent, vueLang = extractVueScript(content)
		if len(scriptContent) == 0 {
			return result // No script section found
		}
	}

	parser := p.getParser(filePath, vueLang)
	tree, diagnostics := parseWithTimeout(parser, scriptContent)
	result.ParseDiagnostics = diagnostics
	if tree == nil {
		return result
	}
	defer tree.Close()

	root := tree.RootNode()
	if root == nil {
		result.ParseDiagnostics = ParseDiagnostics{
			FailureKind: ParseFailureSyntaxUnsupported,
			Message:     "missing root node",
		}
		return result
	}
	if root.HasError() {
		result.ParseDiagnostics = syntaxErrorDiagnostics()
	}

	p.collectRouterBindings(root, scriptContent)
	defer func() { p.routerIdx = nil }()
	lines := strings.Split(string(scriptContent), "\n")
	skipHttpExtraction := looksMinifiedOrVendor(filePath, scriptContent, lines)
	var httpClientAliases map[string][]httpClientAlias
	var messagePublisherTargets map[string]map[string]string
	var messagePublisherHelpers map[string][]int
	var sideEffectTargets map[string]map[string]string
	p.templatePrefixConstants = nil
	if !skipHttpExtraction {
		httpClientAliases = p.extractHttpClientAliases(root, scriptContent)
		p.templatePrefixConstants = p.collectTemplatePrefixConstants(root, scriptContent)
		messagePublisherTargets = p.extractMessagePublisherTargets(root, scriptContent)
		messagePublisherHelpers = p.extractMessagePublisherHelpers(root, scriptContent)
		sideEffectTargets = p.extractSideEffectTargets(root, scriptContent)
	}
	p.walkNode(root, &result, scriptContent, lines, httpClientAliases, messagePublisherTargets, messagePublisherHelpers, sideEffectTargets)
	result.Functions = append(result.Functions, p.extractExportedObjectFunctions(root, scriptContent, lines)...)
	for alias, calls := range p.extractExportedObjectAliasCalls(root, scriptContent) {
		result.FunctionCalls[alias] = append(result.FunctionCalls[alias], calls...)
	}
	if routeEndpoints := p.extractAngularRouteEndpoints(scriptContent); len(routeEndpoints) > 0 {
		result.Endpoints = append(result.Endpoints, routeEndpoints...)
		result.FunctionCalls["_module_"] = append(result.FunctionCalls["_module_"], p.extractFrontendRouteComponentCalls(scriptContent)...)
	}
	result.GraphQLOperations = p.extractEmbeddedGraphQLOperations(root, scriptContent)
	result.GraphQLOperationUsages = p.extractGraphQLOperationUsages(root, scriptContent, result.Imports)
	if isVue {
		result.GraphQLOperationUsages = mergeGraphQLOperationUsages(result.GraphQLOperationUsages, p.extractVueTemplateGraphQLOperationUsages(content, result.Imports))
	}
	result.GraphQLBackendEntrypoints = p.extractGraphQLBackendEntrypoints(root, scriptContent)
	result.GraphQLOperationResolvers = p.extractGraphQLOperationResolvers(root, scriptContent)
	result.GraphQLOperationPermissions = p.extractGraphQLOperationPermissions(root, scriptContent)
	p.aliasGenericEntryFunctions(&result, filePath)
	p.addModuleFunctionIfNeeded(&result, lines)

	return result
}

func (p *JavaScriptParser) addModuleFunctionIfNeeded(result *ParsedFile, lines []string) {
	needsModuleForEndpoint := false
	for _, ep := range result.Endpoints {
		if strings.TrimSpace(ep.HandlerName) == "_module_" {
			needsModuleForEndpoint = true
			break
		}
	}

	if len(result.FunctionCalls["_module_"]) == 0 &&
		len(result.HttpCalls["_module_"]) == 0 &&
		len(result.DataAccesses["_module_"]) == 0 &&
		len(result.SqsProducers["_module_"]) == 0 &&
		!parsedSqsConsumersReferenceHandler(result.SqsConsumers, "_module_") &&
		len(result.GraphQLOperationUsages) == 0 &&
		len(result.GraphQLBackendEntrypoints) == 0 &&
		!needsModuleForEndpoint {
		return
	}

	for _, fn := range result.Functions {
		if fn.Name == "_module_" {
			return
		}
	}

	endLine := len(lines)
	if endLine == 0 {
		endLine = 1
	}
	result.Functions = append(result.Functions, ParsedFunction{
		Name:       "_module_",
		StartLine:  1,
		EndLine:    endLine,
		IsExported: false,
		IsAsync:    false,
	})
}

func (p *JavaScriptParser) aliasGenericEntryFunctions(result *ParsedFile, filePath string) {
	if result == nil {
		return
	}
	if !p.isIndexLikeJavaScriptFile(filePath) {
		return
	}

	baseName := strings.TrimSpace(p.deriveFileScopedFunctionName(filePath))
	if baseName == "" {
		return
	}

	genericNames := map[string]bool{
		"httpTrigger": true,
		"handler":     true,
		"default":     true,
	}

	renameMap := make(map[string]string)
	seenNames := make(map[string]bool, len(result.Functions))
	for _, fn := range result.Functions {
		seenNames[fn.Name] = true
	}

	for i := range result.Functions {
		name := strings.TrimSpace(result.Functions[i].Name)
		if !genericNames[name] || seenNames[baseName] {
			continue
		}
		result.Functions[i].Name = baseName
		renameMap[name] = baseName
		seenNames[baseName] = true
	}

	if len(renameMap) == 0 {
		return
	}

	remapCalls := func(in map[string][]ParsedFunctionCall) map[string][]ParsedFunctionCall {
		out := make(map[string][]ParsedFunctionCall, len(in))
		for name, calls := range in {
			mappedName := name
			if alias, ok := renameMap[name]; ok {
				mappedName = alias
			}
			for idx := range calls {
				if alias, ok := renameMap[calls[idx].CalleeName]; ok {
					calls[idx].CalleeName = alias
				}
			}
			out[mappedName] = append(out[mappedName], calls...)
		}
		return out
	}

	remapHTTP := func(in map[string][]ParsedHttpCall) map[string][]ParsedHttpCall {
		out := make(map[string][]ParsedHttpCall, len(in))
		for name, calls := range in {
			mappedName := name
			if alias, ok := renameMap[name]; ok {
				mappedName = alias
			}
			out[mappedName] = append(out[mappedName], calls...)
		}
		return out
	}

	remapQueues := func(in map[string][]ParsedSqsProducer) map[string][]ParsedSqsProducer {
		out := make(map[string][]ParsedSqsProducer, len(in))
		for name, calls := range in {
			mappedName := name
			if alias, ok := renameMap[name]; ok {
				mappedName = alias
			}
			out[mappedName] = append(out[mappedName], calls...)
		}
		return out
	}

	for i := range result.GraphQLOperationUsages {
		if alias, ok := renameMap[result.GraphQLOperationUsages[i].FunctionName]; ok {
			result.GraphQLOperationUsages[i].FunctionName = alias
		}
	}
	for i := range result.GraphQLBackendEntrypoints {
		if alias, ok := renameMap[result.GraphQLBackendEntrypoints[i].HandlerName]; ok {
			result.GraphQLBackendEntrypoints[i].HandlerName = alias
		}
	}
	for i := range result.Endpoints {
		if alias, ok := renameMap[result.Endpoints[i].HandlerName]; ok {
			result.Endpoints[i].HandlerName = alias
		}
	}
	for i := range result.SqsConsumers {
		if alias, ok := renameMap[result.SqsConsumers[i].HandlerMethod]; ok {
			result.SqsConsumers[i].HandlerMethod = alias
		}
		if alias, ok := renameMap[result.SqsConsumers[i].ClassName]; ok {
			result.SqsConsumers[i].ClassName = alias
		}
	}

	result.FunctionCalls = remapCalls(result.FunctionCalls)
	result.HttpCalls = remapHTTP(result.HttpCalls)
	result.SqsProducers = remapQueues(result.SqsProducers)
	dataAccesses := make(map[string][]ParsedDataAccess, len(result.DataAccesses))
	for name, accesses := range result.DataAccesses {
		if alias, ok := renameMap[name]; ok {
			name = alias
		}
		dataAccesses[name] = append(dataAccesses[name], accesses...)
	}
	result.DataAccesses = dataAccesses
}

func (p *JavaScriptParser) isIndexLikeJavaScriptFile(filePath string) bool {
	name := strings.TrimSpace(strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)))
	return strings.EqualFold(name, "index")
}

func (p *JavaScriptParser) deriveFileScopedFunctionName(filePath string) string {
	base := strings.TrimSpace(filepath.Base(filePath))
	dir := strings.TrimSpace(filepath.Base(filepath.Dir(filePath)))
	name := strings.TrimSuffix(base, filepath.Ext(base))
	if strings.EqualFold(name, "index") && dir != "" && dir != "." && dir != string(filepath.Separator) {
		name = dir
	}
	if name == "" || name == "." {
		return ""
	}
	return name
}

func (p *JavaScriptParser) walkNode(node *sitter.Node, result *ParsedFile, content []byte, lines []string, httpClientAliases map[string][]httpClientAlias, messagePublisherTargets map[string]map[string]string, messagePublisherHelpers map[string][]int, sideEffectTargets map[string]map[string]string) {
	nodeType := node.Type()

	// Extract imports
	if nodeType == "import_statement" {
		if imp := p.extractImport(node, content); imp != nil {
			result.Imports = append(result.Imports, *imp)
		}
	}

	// Extract TypeScript interfaces
	if nodeType == "interface_declaration" {
		if iface := p.extractInterface(node, content, lines); iface != nil {
			result.Interfaces = append(result.Interfaces, *iface)
		}
	}

	// Extract TypeScript type aliases
	if nodeType == "type_alias_declaration" {
		if typeAlias := p.extractTypeAlias(node, content); typeAlias != nil {
			result.TypeAliases = append(result.TypeAliases, *typeAlias)
		}
	}

	// Extract TypeScript enums
	if nodeType == "enum_declaration" {
		if enum := p.extractEnum(node, content, lines); enum != nil {
			result.Classes = append(result.Classes, *enum)
		}
	}

	// Extract function declarations/expressions
	if node.IsNamed() && (nodeType == "function_declaration" || nodeType == "function" || nodeType == "function_expression") {
		if fn := p.extractFunction(node, content, lines); fn != nil {
			result.Functions = append(result.Functions, *fn)
		}
	}

	// The function node owns an arrow's declaration, including its signature.
	if nodeType == "arrow_function" {
		if fn := p.extractAnonymousCallback(node, content, lines); fn != nil {
			result.Functions = append(result.Functions, *fn)
		}
	}
	if nodeType == "lexical_declaration" || nodeType == "variable_declaration" {
		funcs := p.extractVariableObjectFunctions(node, content, lines)
		for _, fn := range funcs {
			result.Functions = append(result.Functions, fn)
		}
	}

	// Extract class declarations (including decorated classes)
	if node.IsNamed() && (nodeType == "class_declaration" || nodeType == "class") {
		if cls := p.extractClass(node, content, lines); cls != nil {
			result.Classes = append(result.Classes, *cls)
			for _, method := range cls.Methods {
				methodFn := method
				methodFn.Name = cls.Name + "." + method.Name
				result.Functions = append(result.Functions, methodFn)
			}
		}
	}

	// Extract endpoints and function calls
	if nodeType == "call_expression" {
		endpoint := p.extractEndpoint(node, content)
		if endpoint != nil {
			result.Endpoints = append(result.Endpoints, *endpoint)
		}
		optionEndpoints := p.extractRouteObjectEndpoints(node, content)
		result.Endpoints = append(result.Endpoints, optionEndpoints...)
		// Extract HTTP client calls (axios, fetch, etc.). A route registration
		// is the server side of the boundary and is never also a client call.
		if endpoint == nil && len(optionEndpoints) == 0 {
			if httpCall := p.extractHttpCall(node, content, httpClientAliases); httpCall != nil {
				// Find the containing function to associate the call
				funcName := p.findContainingFunction(node, content)
				if funcName == "" {
					funcName = "_module_"
				}
				result.HttpCalls[funcName] = append(result.HttpCalls[funcName], *httpCall)
			}
		}
		// Extract React hook calls (useState, useEffect, etc.)
		if hookCall := p.extractHookCall(node, content); hookCall != nil {
			result.HookCalls = append(result.HookCalls, *hookCall)
		}
		if contracts := p.extractVueComponentContracts(node, content); len(contracts) > 0 {
			result.VueComponentContracts = append(result.VueComponentContracts, contracts...)
		}
		if store := p.extractPiniaStore(node, content); store != nil {
			result.PiniaStores = append(result.PiniaStores, *store)
		}
		if producer := p.extractMessageProducer(node, content, messagePublisherTargets); producer != nil {
			funcName := p.findContainingFunction(node, content)
			if funcName == "" {
				funcName = "_module_"
			}
			result.SqsProducers[funcName] = append(result.SqsProducers[funcName], *producer)
		} else if producer := p.extractMessageProducerViaHelper(node, content, messagePublisherTargets, messagePublisherHelpers); producer != nil {
			funcName := p.findContainingFunction(node, content)
			if funcName == "" {
				funcName = "_module_"
			}
			result.SqsProducers[funcName] = append(result.SqsProducers[funcName], *producer)
		}
		if consumer := p.extractMessageConsumer(node, content, messagePublisherTargets); consumer != nil {
			result.SqsConsumers = append(result.SqsConsumers, *consumer)
		}
		if access := p.extractSideEffectDataAccess(node, content, sideEffectTargets); access != nil {
			funcName := p.findContainingFunction(node, content)
			if funcName == "" {
				funcName = "_module_"
			}
			result.DataAccesses[funcName] = append(result.DataAccesses[funcName], *access)
		}
		// Extract general function calls with receiver information
		methodName, receiver := p.extractCallMethod(node, content)
		callbackEdges := p.extractCallbackEdges(node, content, methodName, receiver)
		isJqFluent := p.isJQueryFluentCallWithMethod(node, content, methodName, receiver)
		if call := p.extractSingleFunctionCall(node, content, methodName, receiver); call != nil {
			if len(callbackEdges) == 0 && !isJqFluent {
				isJqRelated := strings.Contains(strings.ToLower(receiver), "jquery") || strings.Contains(receiver, "$") ||
					methodName == "$" || methodName == "jQuery"
				if !isJqRelated || !p.isJQueryCallbackChainCall(node, content) {
					funcName := p.findContainingFunction(node, content)
					if funcName == "" {
						funcName = "_module_"
					}
					if call.MethodName != "" {
						p.normalizeCallName(call, funcName, node, content)
					}
					result.FunctionCalls[funcName] = append(result.FunctionCalls[funcName], *call)
				}
			}
		}

		for _, callback := range callbackEdges {
			callbackEdge, callbackNode := callback.call, callback.node
			funcName := p.findContainingFunction(node, content)
			if funcName == "" {
				funcName = "_module_"
			}
			result.FunctionCalls[funcName] = append(result.FunctionCalls[funcName], callbackEdge)
			if callbackNode != nil {
				if handlerCall := p.extractCallbackReferenceCall(callbackNode, content, funcName, node); handlerCall != nil {
					result.FunctionCalls[callbackEdge.CalleeName] = append(result.FunctionCalls[callbackEdge.CalleeName], *handlerCall)
					p.addSyntheticCallbackFunction(result, callbackEdge.CalleeName, callbackNode, lines)
				}
			}
		}
	}
	if nodeType == "assignment_expression" {
		if fn := p.extractModuleExportFunction(node, content, lines); fn != nil {
			result.Functions = append(result.Functions, *fn)
		}
		if navigationCall := p.extractBrowserNavigationAssignment(node, content); navigationCall != nil {
			funcName := p.findContainingFunction(node, content)
			if funcName == "" {
				funcName = "_module_"
			}
			result.HttpCalls[funcName] = append(result.HttpCalls[funcName], *navigationCall)
		}
	}
	if nodeType == "call_expression" || nodeType == "new_expression" {
		p.connectInlineFunctions(node, result, content, lines)
	}

	// Recurse first
	for i := 0; i < int(node.ChildCount()); i++ {
		p.walkNode(node.Child(i), result, content, lines, httpClientAliases, messagePublisherTargets, messagePublisherHelpers, sideEffectTargets)
	}

	// Mark exports and attach decorators after recursion (so children are processed first)
	if nodeType == "export_statement" {
		// Attach decorators to exported classes: @Decorator export class Foo {}
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child.Type() == "class_declaration" || child.Type() == "class" {
				// Decorators come before the class_declaration in export_statement
				var decorators []ParsedAnnotation
				for j := 0; j < i; j++ {
					if node.Child(j).Type() == "decorator" {
						if dec := p.extractDecorator(node.Child(j), content); dec != nil {
							decorators = append(decorators, *dec)
						}
					}
				}
				if len(decorators) > 0 {
					// Find the class and attach decorators
					className := ""
					for k := 0; k < int(child.ChildCount()); k++ {
						c := child.Child(k)
						if c.Type() == "identifier" || c.Type() == "type_identifier" {
							className = c.Content(content)
							break
						}
					}
					for k := range result.Classes {
						if result.Classes[k].Name == className {
							result.Classes[k].Annotations = append(result.Classes[k].Annotations, decorators...)
							break
						}
					}
				}
			}
		}
		p.markExports(node, result, content)
	}
}

var (
	angularRoutePathSingleRe = regexp.MustCompile(`(?m)\bpath\s*:\s*'([^']*)'`)
	angularRoutePathDoubleRe = regexp.MustCompile(`(?m)\bpath\s*:\s*"([^"]*)"`)
	angularTrailingParamRe   = regexp.MustCompile(`^(.+)/:[^/]+$`)
	frontendRouteComponentRe = regexp.MustCompile(`(?s)\bcomponent\s*:\s*([A-Za-z_$][A-Za-z0-9_$]*)`)
	frontendRouteLazyRe      = regexp.MustCompile(`(?s)\b(?:component|loadChildren)\s*:\s*(?:\([^)]*\)|[A-Za-z_$][A-Za-z0-9_$]*)?\s*=>\s*import\s*\(\s*['"]([^'"]+)['"]\s*\)`)
)

func (p *JavaScriptParser) extractAngularRouteEndpoints(content []byte) []ParsedEndpoint {
	source := string(content)
	lowerSource := strings.ToLower(source)
	if !strings.Contains(lowerSource, "routermodule.forroot(") &&
		!strings.Contains(lowerSource, "routermodule.forchild(") &&
		!strings.Contains(lowerSource, "providerouter(") &&
		!strings.Contains(lowerSource, "createrouter(") &&
		!strings.Contains(lowerSource, "vue-router") &&
		!strings.Contains(source, "RouteRecordRaw") {
		return nil
	}

	matches := make([][]int, 0)
	matches = append(matches, angularRoutePathSingleRe.FindAllStringSubmatchIndex(source, -1)...)
	matches = append(matches, angularRoutePathDoubleRe.FindAllStringSubmatchIndex(source, -1)...)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(matches)*2)
	out := make([]ParsedEndpoint, 0, len(matches)*2)
	add := func(path string, line int, handlerName string) {
		if path == "" {
			return
		}
		handlerName = strings.TrimSpace(handlerName)
		if handlerName == "" {
			handlerName = "_module_"
		}
		key := "REQUEST|" + strings.ToLower(path)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, ParsedEndpoint{
			Path:        path,
			Method:      "REQUEST",
			HandlerName: handlerName,
			LineNumber:  line,
		})
	}

	for _, match := range matches {
		if len(match) < 4 {
			continue
		}
		rawPath := strings.TrimSpace(source[match[2]:match[3]])
		if rawPath == "**" {
			continue
		}
		path := normalizeAngularRoutePath(rawPath)
		if path == "" {
			continue
		}
		line := 1 + strings.Count(source[:match[0]], "\n")
		handlerName := frontendRouteHandlerNearPath(source, match[0])
		add(path, line, handlerName)
		if base := angularRouteBasePath(path); base != "" && base != path {
			add(base, line, handlerName)
		}
	}

	return out
}

func frontendRouteHandlerNearPath(source string, pathIndex int) string {
	objectText := enclosingObjectLiteralText(source, pathIndex)
	if objectText == "" {
		return ""
	}
	if match := frontendRouteLazyRe.FindStringSubmatch(objectText); len(match) == 2 {
		return routeHandlerNameFromImportPath(match[1])
	}
	if match := frontendRouteComponentRe.FindStringSubmatch(objectText); len(match) == 2 {
		return strings.TrimSpace(match[1])
	}
	return ""
}

func (p *JavaScriptParser) extractFrontendRouteComponentCalls(content []byte) []ParsedFunctionCall {
	source := string(content)
	matches := make([][]int, 0)
	matches = append(matches, angularRoutePathSingleRe.FindAllStringSubmatchIndex(source, -1)...)
	matches = append(matches, angularRoutePathDoubleRe.FindAllStringSubmatchIndex(source, -1)...)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]bool)
	var calls []ParsedFunctionCall
	for _, match := range matches {
		if len(match) < 4 {
			continue
		}
		rawPath := strings.TrimSpace(source[match[2]:match[3]])
		if rawPath == "" || rawPath == "**" {
			continue
		}
		handlerName := frontendRouteHandlerNearPath(source, match[0])
		if handlerName == "" || handlerName == "_module_" {
			continue
		}
		line := 1 + strings.Count(source[:match[0]], "\n")
		key := handlerName + "|" + strconv.Itoa(line)
		if seen[key] {
			continue
		}
		seen[key] = true
		calls = append(calls, ParsedFunctionCall{
			CalleeName: handlerName,
			MethodName: handlerName,
			LineNumber: line,
		})
	}
	return calls
}

func enclosingObjectLiteralText(source string, index int) string {
	if index < 0 || index >= len(source) {
		return ""
	}
	start := strings.LastIndex(source[:index], "{")
	if start < 0 {
		return ""
	}
	depth := 0
	inString := byte(0)
	escaped := false
	for i := start; i < len(source); i++ {
		ch := source[i]
		if inString != 0 {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == inString {
				inString = 0
			}
			continue
		}
		switch ch {
		case '\'', '"', '`':
			inString = ch
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return source[start : i+1]
			}
		}
	}
	return ""
}

func routeHandlerNameFromImportPath(importPath string) string {
	importPath = strings.TrimSpace(strings.ReplaceAll(importPath, "\\", "/"))
	if importPath == "" {
		return ""
	}
	base := filepath.Base(importPath)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	return strings.TrimSpace(base)
}

func normalizeAngularRoutePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "/"
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	trimmed = strings.ReplaceAll(trimmed, "\\", "/")
	for strings.Contains(trimmed, "//") {
		trimmed = strings.ReplaceAll(trimmed, "//", "/")
	}
	if trimmed != "/" {
		trimmed = strings.TrimSuffix(trimmed, "/")
		if trimmed == "" {
			trimmed = "/"
		}
	}
	return trimmed
}

func angularRouteBasePath(path string) string {
	match := angularTrailingParamRe.FindStringSubmatch(path)
	if len(match) != 2 {
		return ""
	}
	base := strings.TrimSpace(match[1])
	if base == "" {
		return ""
	}
	return base
}

func (p *JavaScriptParser) extractBrowserNavigationAssignment(node *sitter.Node, content []byte) *ParsedHttpCall {
	if node == nil || node.Type() != "assignment_expression" {
		return nil
	}

	left := node.ChildByFieldName("left")
	right := node.ChildByFieldName("right")
	if left == nil && node.ChildCount() > 0 {
		left = node.Child(0)
	}
	if right == nil && node.ChildCount() > 0 {
		right = node.Child(int(node.ChildCount()) - 1)
	}
	if left == nil || right == nil {
		return nil
	}
	if !p.isBrowserLocationTarget(left, content) {
		return nil
	}

	url := p.extractUrlFromValueNode(right, content)
	if url == "" {
		return nil
	}
	if !strings.HasPrefix(url, "/") && !strings.HasPrefix(strings.ToLower(url), "http") {
		return nil
	}

	return &ParsedHttpCall{
		HttpMethod: "REQUEST",
		UrlPattern: url,
		LineNumber: int(node.StartPoint().Row) + 1,
		ClientType: "navigation",
	}
}

func (p *JavaScriptParser) isBrowserLocationTarget(node *sitter.Node, content []byte) bool {
	if node == nil {
		return false
	}

	switch node.Type() {
	case "identifier":
		return strings.EqualFold(node.Content(content), "location")
	case "member_expression", "optional_member_expression":
		receiver, method := p.extractReceiverAndMethod(node, content)
		receiver = strings.ToLower(strings.ReplaceAll(receiver, "?.", "."))
		method = strings.ToLower(strings.TrimSpace(method))
		if method == "href" {
			return receiver == "location" || receiver == "window.location" || strings.HasSuffix(receiver, ".location")
		}
		if method == "location" {
			return receiver == "window" || receiver == "document"
		}
	}

	return false
}

func (p *JavaScriptParser) extractModuleExportFunction(node *sitter.Node, content []byte, lines []string) *ParsedFunction {
	left, right := assignmentSides(node)
	if left == nil || right == nil || !isGraphQLResolverValue(right) {
		return nil
	}
	name := moduleExportsProperty(left, content)
	if name == "" || name == "queries" || name == "mutations" || name == "subscriptions" {
		return nil
	}
	if !looksLikeGraphQLResolverOperationName(name) {
		return nil
	}
	startLine := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1
	return &ParsedFunction{
		Name:       name,
		StartLine:  startLine,
		EndLine:    endLine,
		IsExported: true,
		IsAsync:    strings.Contains(right.Content(content), "async"),
		SourceCode: getSourceCode(lines, startLine, endLine),
	}
}

func (p *JavaScriptParser) extractImport(node *sitter.Node, content []byte) *ParsedImport {
	var source string
	var names []string
	isDefault := false
	isNamespace := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		if childType == "string" {
			source = strings.Trim(child.Content(content), "\"'`")
		}

		if childType == "identifier" {
			names = append(names, child.Content(content))
			isDefault = true
		}

		if childType == "import_clause" {
			for j := 0; j < int(child.ChildCount()); j++ {
				c := child.Child(j)
				cType := c.Type()

				if cType == "identifier" {
					names = append(names, c.Content(content))
					isDefault = true
				}

				if cType == "named_imports" {
					for k := 0; k < int(c.ChildCount()); k++ {
						spec := c.Child(k)
						if spec.Type() == "import_specifier" {
							alias := ""
							for l := 0; l < int(spec.ChildCount()); l++ {
								part := spec.Child(l)
								if part.Type() == "identifier" {
									// For `import { a as b }`, keep alias `b` (last identifier).
									alias = part.Content(content)
								}
							}
							if alias != "" {
								names = append(names, alias)
							}
						}
					}
				}

				if cType == "namespace_import" {
					for k := 0; k < int(c.ChildCount()); k++ {
						if c.Child(k).Type() == "identifier" {
							names = append(names, c.Child(k).Content(content))
							isNamespace = true
							break
						}
					}
				}
			}
		}
	}

	if source == "" {
		return nil
	}

	return &ParsedImport{
		Path:        source,
		Names:       names,
		IsDefault:   isDefault,
		IsNamespace: isNamespace,
	}
}

func (p *JavaScriptParser) extractGraphQLOperationUsages(root *sitter.Node, content []byte, imports []ParsedImport) []ParsedGraphQLOperationUsage {
	graphqlAliases := make(map[string]string)
	registryHelperAliases := make(map[string]bool)
	for _, imp := range imports {
		if !isGraphQLDocumentPath(imp.Path) {
			if looksLikeGraphQLRegistryPath(imp.Path) {
				for _, name := range imp.Names {
					if strings.TrimSpace(name) == "" {
						continue
					}
					registryHelperAliases[name] = true
				}
			}
			continue
		}
		for _, name := range imp.Names {
			if strings.TrimSpace(name) == "" {
				continue
			}
			graphqlAliases[name] = imp.Path
		}
	}
	if root == nil {
		return nil
	}

	seen := make(map[string]bool)
	var usages []ParsedGraphQLOperationUsage

	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil {
			return
		}
		if node.Type() == "call_expression" {
			if usage := p.extractGraphQLOperationRegistryUsage(node, content, registryHelperAliases); usage != nil {
				key := usage.ImportPath + "|" + usage.ImportedAs + "|" + usage.FunctionName + "|" + strconv.Itoa(usage.LineNumber)
				if !seen[key] {
					seen[key] = true
					usages = append(usages, *usage)
				}
			}
			for _, usage := range p.extractGraphQLClientRequestUsages(node, content) {
				key := usage.ImportPath + "|" + usage.ImportedAs + "|" + usage.FunctionName + "|" + strconv.Itoa(usage.LineNumber)
				if !seen[key] {
					seen[key] = true
					usages = append(usages, usage)
				}
			}
			for _, usage := range p.extractGraphQLHTTPBodyUsages(node, content) {
				key := usage.ImportPath + "|" + usage.ImportedAs + "|" + usage.FunctionName + "|" + strconv.Itoa(usage.LineNumber)
				if !seen[key] {
					seen[key] = true
					usages = append(usages, usage)
				}
			}
		}
		if node.Type() == "identifier" {
			identifier := strings.TrimSpace(node.Content(content))
			importPath, ok := graphqlAliases[identifier]
			if ok && !isImportIdentifier(node) {
				funcName := p.findContainingFunction(node, content)
				if funcName == "" {
					funcName = "_module_"
				}
				line := int(node.StartPoint().Row) + 1
				key := importPath + "|" + identifier + "|" + funcName + "|" + strconv.Itoa(line)
				if !seen[key] {
					seen[key] = true
					usages = append(usages, ParsedGraphQLOperationUsage{
						ImportPath:   importPath,
						ImportedAs:   identifier,
						FunctionName: funcName,
						LineNumber:   line,
					})
				}
			}
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			walk(node.Child(i))
		}
	}

	walk(root)
	return usages
}

func (p *JavaScriptParser) extractGraphQLHTTPBodyUsages(node *sitter.Node, content []byte) []ParsedGraphQLOperationUsage {
	if node == nil || node.Type() != "call_expression" || !p.looksLikeHTTPCallForGraphQLBody(node, content) {
		return nil
	}

	argsNode := node.ChildByFieldName("arguments")
	if argsNode == nil {
		for i := 0; i < int(node.ChildCount()); i++ {
			if node.Child(i).Type() == "arguments" {
				argsNode = node.Child(i)
				break
			}
		}
	}
	if argsNode == nil {
		return nil
	}

	var queryNodes []*sitter.Node
	for i := 0; i < int(argsNode.ChildCount()); i++ {
		arg := argsNode.Child(i)
		if arg == nil {
			continue
		}
		switch arg.Type() {
		case "object":
			queryNodes = append(queryNodes, p.graphQLQueryValueNodesFromObject(node, arg, content)...)
		case "identifier", "member_expression":
			resolved := p.resolveNodeValueBeforeNode(node, arg, content)
			if resolved != nil && resolved.Type() == "object" {
				queryNodes = append(queryNodes, p.graphQLQueryValueNodesFromObject(node, resolved, content)...)
			}
		}
	}
	if len(queryNodes) == 0 {
		return nil
	}

	funcName := p.findContainingFunction(node, content)
	if funcName == "" {
		funcName = "_module_"
	}
	seen := make(map[string]bool)
	var usages []ParsedGraphQLOperationUsage
	for _, queryNode := range queryNodes {
		for _, op := range extractGraphQLRequestOperationsFromValueNode(queryNode, content) {
			if op.Name == "" {
				continue
			}
			key := op.OperationType + "|" + op.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			usages = append(usages, ParsedGraphQLOperationUsage{
				ImportPath:   "__graphql_operation__:" + op.Name,
				ImportedAs:   op.Name,
				FunctionName: funcName,
				LineNumber:   int(node.StartPoint().Row) + 1,
			})
		}
	}
	return usages
}

func (p *JavaScriptParser) looksLikeHTTPCallForGraphQLBody(node *sitter.Node, content []byte) bool {
	methodName, receiver := p.extractCallMethod(node, content)
	method := strings.ToLower(strings.TrimSpace(methodName))
	receiver = strings.ToLower(strings.TrimSpace(receiver))
	if method == "axios" || receiver == "axios" || strings.Contains(receiver, ".axios") {
		return true
	}
	if method == "fetch" {
		return true
	}
	if _, ok := genericHttpMethod(method); ok && (looksLikeHttpReceiver(receiver) || receiver == "axios") {
		return true
	}
	return false
}

func (p *JavaScriptParser) graphQLQueryValueNodesFromObject(refNode, objectNode *sitter.Node, content []byte) []*sitter.Node {
	if objectNode == nil || objectNode.Type() != "object" {
		return nil
	}
	var out []*sitter.Node
	for i := 0; i < int(objectNode.ChildCount()); i++ {
		pair := objectNode.Child(i)
		if pair == nil || pair.Type() != "pair" {
			continue
		}
		key := strings.ToLower(graphQLObjectPropertyName(pair.ChildByFieldName("key"), content))
		value := pair.ChildByFieldName("value")
		if value == nil {
			continue
		}
		switch key {
		case "query", "graphql":
			switch value.Type() {
			case "string", "template_string":
				out = append(out, value)
			case "identifier", "member_expression":
				if resolved := p.resolveNodeValueBeforeNode(refNode, value, content); resolved != nil {
					out = append(out, resolved)
				}
			}
		case "data", "body", "payload":
			if value.Type() == "object" {
				out = append(out, p.graphQLQueryValueNodesFromObject(refNode, value, content)...)
			}
		}
	}
	return out
}

func (p *JavaScriptParser) extractVueTemplateGraphQLOperationUsages(content []byte, imports []ParsedImport) []ParsedGraphQLOperationUsage {
	graphqlAliases := make(map[string]string)
	for _, imp := range imports {
		if !isGraphQLDocumentPath(imp.Path) {
			continue
		}
		for _, name := range imp.Names {
			name = strings.TrimSpace(name)
			if name != "" {
				graphqlAliases[name] = imp.Path
			}
		}
	}
	if len(graphqlAliases) == 0 {
		return nil
	}

	template := vueTemplateContent(content)
	if template == "" {
		return nil
	}

	var usages []ParsedGraphQLOperationUsage
	seen := make(map[string]bool)
	for alias, importPath := range graphqlAliases {
		for _, line := range vueIdentifierLines(template, alias) {
			key := importPath + "|" + alias + "|" + strconv.Itoa(line)
			if seen[key] {
				continue
			}
			seen[key] = true
			usages = append(usages, ParsedGraphQLOperationUsage{
				ImportPath:   importPath,
				ImportedAs:   alias,
				FunctionName: "_template_",
				LineNumber:   line,
			})
		}
	}
	return usages
}

func vueTemplateContent(content []byte) string {
	contentStr := string(content)
	templateStart := strings.Index(contentStr, "<template")
	if templateStart < 0 {
		return ""
	}
	tagEndRel := strings.Index(contentStr[templateStart:], ">")
	if tagEndRel < 0 {
		return ""
	}
	bodyStart := templateStart + tagEndRel + 1
	bodyEndRel := strings.Index(contentStr[bodyStart:], "</template>")
	if bodyEndRel < 0 {
		return ""
	}
	prefixLines := strings.Count(contentStr[:bodyStart], "\n")
	body := contentStr[bodyStart : bodyStart+bodyEndRel]
	if prefixLines == 0 {
		return body
	}
	return strings.Repeat("\n", prefixLines) + body
}

func vueIdentifierLines(template, identifier string) []int {
	identifier = regexp.QuoteMeta(strings.TrimSpace(identifier))
	if identifier == "" {
		return nil
	}
	re := regexp.MustCompile(`(?:[:@#A-Za-z0-9_\-]+\s*=\s*"[^"]*\b` + identifier + `\b[^"]*"|\{\{[^}]*\b` + identifier + `\b[^}]*\}\})`)
	matches := re.FindAllStringIndex(template, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[int]bool)
	var lines []int
	for _, match := range matches {
		line := 1 + strings.Count(template[:match[0]], "\n")
		if !seen[line] {
			seen[line] = true
			lines = append(lines, line)
		}
	}
	return lines
}

func mergeGraphQLOperationUsages(base, extra []ParsedGraphQLOperationUsage) []ParsedGraphQLOperationUsage {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]bool, len(base)+len(extra))
	var merged []ParsedGraphQLOperationUsage
	add := func(usage ParsedGraphQLOperationUsage) {
		key := usage.ImportPath + "|" + usage.ImportedAs + "|" + usage.FunctionName + "|" + strconv.Itoa(usage.LineNumber)
		if seen[key] {
			return
		}
		seen[key] = true
		merged = append(merged, usage)
	}
	for _, usage := range base {
		add(usage)
	}
	for _, usage := range extra {
		add(usage)
	}
	return merged
}

func (p *JavaScriptParser) extractEmbeddedGraphQLOperations(root *sitter.Node, content []byte) []ParsedGraphQLOperation {
	if root == nil {
		return nil
	}

	seen := make(map[string]bool)
	var operations []ParsedGraphQLOperation

	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil {
			return
		}
		if node.Type() == "pair" {
			keyNode := node.ChildByFieldName("key")
			valueNode := node.ChildByFieldName("value")
			if keyNode != nil && valueNode != nil {
				key := strings.Trim(keyNode.Content(content), "\"'` ")
				if strings.EqualFold(key, "graphql") {
					for _, op := range extractGraphQLOperationsFromValueNode(valueNode, content) {
						key := op.Name + "|" + op.OperationType + "|" + strconv.Itoa(op.LineNumber)
						if seen[key] {
							continue
						}
						seen[key] = true
						operations = append(operations, op)
					}
				}
			}
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			walk(node.Child(i))
		}
	}

	walk(root)
	return operations
}

func extractGraphQLOperationsFromValueNode(node *sitter.Node, content []byte) []ParsedGraphQLOperation {
	if node == nil {
		return nil
	}

	var raw string
	switch node.Type() {
	case "string":
		raw = strings.Trim(node.Content(content), "\"'`")
	case "template_string":
		raw = strings.Trim(node.Content(content), "`")
	default:
		return nil
	}
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	var operations []ParsedGraphQLOperation
	for _, match := range graphqlOperationPattern.FindAllStringSubmatchIndex(raw, -1) {
		if len(match) < 6 {
			continue
		}
		opType := strings.ToLower(raw[match[2]:match[3]])
		name := raw[match[4]:match[5]]
		lineOffset := strings.Count(raw[:match[0]], "\n")
		line := int(node.StartPoint().Row) + 1 + lineOffset
		operations = append(operations, ParsedGraphQLOperation{
			Name:          name,
			OperationType: opType,
			LineNumber:    line,
		})
	}
	return operations
}

func extractGraphQLRequestOperationsFromValueNode(node *sitter.Node, content []byte) []ParsedGraphQLOperation {
	raw, ok := graphQLRawStringFromValueNode(node, content)
	if !ok {
		return nil
	}

	seen := make(map[string]bool)
	var operations []ParsedGraphQLOperation
	add := func(opType, name string, line int) {
		opType = strings.ToLower(strings.TrimSpace(opType))
		name = strings.TrimSpace(name)
		if opType == "" || name == "" {
			return
		}
		key := opType + ":" + name
		if seen[key] {
			return
		}
		seen[key] = true
		operations = append(operations, ParsedGraphQLOperation{
			Name:          name,
			OperationType: opType,
			LineNumber:    line,
		})
	}

	for _, op := range extractGraphQLOperationsFromValueNode(node, content) {
		add(op.OperationType, op.Name, op.LineNumber)
	}
	for _, match := range anonymousGraphQLOperationPattern.FindAllStringSubmatchIndex(raw, -1) {
		if len(match) < 4 {
			continue
		}
		opType := raw[match[2]:match[3]]
		selection := firstGraphQLSelectionName(raw[match[1]:])
		if selection == "" {
			continue
		}
		line := int(node.StartPoint().Row) + 1 + strings.Count(raw[:match[0]], "\n")
		add(opType, selection, line)
	}
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "{") {
		if selection := firstGraphQLSelectionName(trimmed); selection != "" {
			line := int(node.StartPoint().Row) + 1 + strings.Count(raw[:strings.Index(raw, "{")], "\n")
			add("query", selection, line)
		}
	}
	return operations
}

func graphQLRawStringFromValueNode(node *sitter.Node, content []byte) (string, bool) {
	if node == nil {
		return "", false
	}
	switch node.Type() {
	case "string":
		return strings.Trim(node.Content(content), "\"'`"), true
	case "template_string":
		return strings.Trim(node.Content(content), "`"), true
	default:
		return "", false
	}
}

func firstGraphQLSelectionName(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "{") {
		raw = raw[1:]
	}
	for {
		raw = strings.TrimLeft(raw, " \t\r\n,")
		if strings.HasPrefix(raw, "#") {
			if idx := strings.Index(raw, "\n"); idx >= 0 {
				raw = raw[idx+1:]
				continue
			}
			return ""
		}
		if strings.HasPrefix(raw, "...") {
			return ""
		}
		break
	}

	name, rest := leadingGraphQLName(raw)
	if name == "" {
		return ""
	}
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, ":") {
		aliasTarget, _ := leadingGraphQLName(strings.TrimSpace(strings.TrimPrefix(rest, ":")))
		if aliasTarget != "" {
			return aliasTarget
		}
	}
	return name
}

func leadingGraphQLName(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	for i, r := range raw {
		valid := r == '_' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
		if !valid {
			if i == 0 {
				return "", raw
			}
			return raw[:i], raw[i:]
		}
	}
	return raw, ""
}

func (p *JavaScriptParser) extractMessagePublisherTargets(root *sitter.Node, content []byte) map[string]map[string]string {
	if root == nil {
		return map[string]map[string]string{}
	}
	scoped := make(map[string]map[string]string)
	setScopedTarget := func(scope, receiver, queueName string) {
		scope = strings.TrimSpace(scope)
		receiver = strings.TrimSpace(receiver)
		queueName = strings.TrimSpace(queueName)
		if scope == "" || receiver == "" || queueName == "" {
			return
		}
		if scoped[scope] == nil {
			scoped[scope] = make(map[string]string)
		}
		scoped[scope][receiver] = queueName
	}
	scopeNameForNode := func(node *sitter.Node) string {
		if name := strings.TrimSpace(p.findContainingCallableScope(node, content)); name != "" {
			return name
		}
		return "_module_"
	}

	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil {
			return
		}
		if node.Type() == "variable_declarator" {
			name, value := extractDeclaratorNameAndValue(node, content)
			value = unwrapAwaitExpression(value)
			if name != "" && value != nil && value.Type() == "call_expression" {
				methodName, receiver := p.extractCallMethod(value, content)
				switch strings.TrimSpace(methodName) {
				case "load":
					if queueName := p.extractLoadedQueueTarget(value, content, receiver); queueName != "" {
						setScopedTarget(scopeNameForNode(node), name, queueName)
					}
				case "getQueueClient", "createSender", "createReceiver":
					if queueName := p.extractMessageTargetArgument(value, content); queueName != "" {
						setScopedTarget(scopeNameForNode(node), name, queueName)
					}
				case "fromConnectionString":
					if queueName := p.extractQueueClientFactoryTarget(value, content); queueName != "" {
						setScopedTarget(scopeNameForNode(node), name, queueName)
					}
				}
			}
		}
		if node.Type() == "call_expression" {
			methodName, receiver := p.extractCallMethod(node, content)
			switch strings.TrimSpace(methodName) {
			case "load":
				if queueName := p.extractLoadedQueueTarget(node, content, receiver); queueName != "" {
					setScopedTarget(scopeNameForNode(node), receiver, queueName)
				}
			case "getQueueClient", "createSender", "createReceiver":
				if queueName := p.extractMessageTargetArgument(node, content); queueName != "" {
					setScopedTarget(scopeNameForNode(node), receiver, queueName)
				}
			}
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			walk(node.Child(i))
		}
	}

	walk(root)
	return scoped
}

func (p *JavaScriptParser) extractMessageProducer(node *sitter.Node, content []byte, messagePublisherTargets map[string]map[string]string) *ParsedSqsProducer {
	if node == nil || node.Type() != "call_expression" || len(messagePublisherTargets) == 0 {
		return nil
	}

	methodName, receiver := p.extractCallMethod(node, content)
	if !p.isMessagePublishingMethod(methodName) {
		return nil
	}

	queueName := p.messagePublisherQueueForReceiver(node, content, messagePublisherTargets, receiver)
	if queueName == "" {
		return nil
	}

	return &ParsedSqsProducer{
		QueueName:  queueName,
		LineNumber: int(node.StartPoint().Row) + 1,
	}
}

func (p *JavaScriptParser) extractMessageProducerViaHelper(node *sitter.Node, content []byte, messagePublisherTargets map[string]map[string]string, messagePublisherHelpers map[string][]int) *ParsedSqsProducer {
	if node == nil || node.Type() != "call_expression" || len(messagePublisherTargets) == 0 || len(messagePublisherHelpers) == 0 {
		return nil
	}

	methodName, receiver := p.extractCallMethod(node, content)
	helperName := strings.TrimSpace(methodName)
	if receiver != "" && receiver != helperName {
		return nil
	}
	paramIndexes := messagePublisherHelpers[helperName]
	if len(paramIndexes) == 0 {
		return nil
	}

	for _, paramIndex := range paramIndexes {
		arg := p.extractArgumentNode(node, paramIndex)
		if arg == nil {
			continue
		}
		argName := strings.TrimSpace(arg.Content(content))
		if argName == "" {
			continue
		}
		queueName := p.messagePublisherQueueForReceiver(node, content, messagePublisherTargets, argName)
		if queueName == "" {
			continue
		}
		return &ParsedSqsProducer{
			QueueName:  queueName,
			LineNumber: int(node.StartPoint().Row) + 1,
		}
	}

	return nil
}

func parsedSqsConsumersReferenceHandler(consumers []ParsedSqsConsumer, handler string) bool {
	handler = strings.TrimSpace(handler)
	if handler == "" {
		return false
	}
	for _, consumer := range consumers {
		if strings.TrimSpace(consumer.HandlerMethod) == handler || strings.TrimSpace(consumer.ClassName) == handler {
			return true
		}
	}
	return false
}

func (p *JavaScriptParser) extractMessageConsumer(node *sitter.Node, content []byte, messagePublisherTargets map[string]map[string]string) *ParsedSqsConsumer {
	if node == nil || node.Type() != "call_expression" || len(messagePublisherTargets) == 0 {
		return nil
	}

	methodName, receiver := p.extractCallMethod(node, content)
	if !isMessageConsumingMethod(methodName) {
		return nil
	}

	queueName := p.messagePublisherQueueForReceiver(node, content, messagePublisherTargets, receiver)
	if queueName == "" {
		return nil
	}

	handler := p.messageConsumerHandlerMethod(node, content)
	if handler == "" {
		handler = p.findContainingFunction(node, content)
	}
	if handler == "" {
		handler = "_module_"
	}

	return &ParsedSqsConsumer{
		QueueName:     queueName,
		HandlerMethod: handler,
		ClassName:     handler,
	}
}

func (p *JavaScriptParser) messagePublisherQueueForReceiver(node *sitter.Node, content []byte, messagePublisherTargets map[string]map[string]string, receiver string) string {
	receiver = strings.TrimSpace(receiver)
	if receiver == "" {
		return ""
	}
	scopeName := "_module_"
	if name := strings.TrimSpace(p.findContainingCallableScope(node, content)); name != "" {
		scopeName = name
	}
	if scopedTargets, ok := messagePublisherTargets[scopeName]; ok {
		if queueName := strings.TrimSpace(scopedTargets[receiver]); queueName != "" {
			return queueName
		}
	}
	if scopedTargets, ok := messagePublisherTargets["_module_"]; ok {
		return strings.TrimSpace(scopedTargets[receiver])
	}
	return ""
}

func isMessageConsumingMethod(methodName string) bool {
	methodName = strings.TrimSpace(methodName)
	switch methodName {
	case "receiveMessages", "receiveMessage", "getMessages", "subscribe":
		return true
	default:
		return false
	}
}

func (p *JavaScriptParser) messageConsumerHandlerMethod(node *sitter.Node, content []byte) string {
	argsNode := node.ChildByFieldName("arguments")
	if argsNode == nil {
		for i := 0; i < int(node.ChildCount()); i++ {
			if node.Child(i).Type() == "arguments" {
				argsNode = node.Child(i)
				break
			}
		}
	}
	if argsNode == nil {
		return ""
	}
	for i := 0; i < int(argsNode.ChildCount()); i++ {
		arg := argsNode.Child(i)
		if arg == nil || arg.Type() != "object" {
			continue
		}
		for j := 0; j < int(arg.ChildCount()); j++ {
			pair := arg.Child(j)
			if pair == nil || pair.Type() != "pair" {
				continue
			}
			keyNode := pair.ChildByFieldName("key")
			if keyNode == nil {
				continue
			}
			key := strings.Trim(strings.TrimSpace(keyNode.Content(content)), "\"'`")
			if key != "processMessage" {
				continue
			}
			value := pair.ChildByFieldName("value")
			if value == nil {
				continue
			}
			switch value.Type() {
			case "identifier", "property_identifier":
				return strings.TrimSpace(value.Content(content))
			case "arrow_function", "function", "function_expression":
				if containing := strings.TrimSpace(p.findContainingFunction(node, content)); containing != "" {
					return containing
				}
				return "_module_"
			}
		}
	}
	return ""
}

func (p *JavaScriptParser) extractMessagePublisherHelpers(root *sitter.Node, content []byte) map[string][]int {
	helpers := make(map[string][]int)
	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil {
			return
		}
		name, params, body := p.messagePublisherHelperDeclaration(node, content)
		if name != "" && len(params) > 0 && body != nil {
			paramIndexes := p.messagePublisherParamIndexes(body, content, params)
			if len(paramIndexes) > 0 {
				helpers[name] = paramIndexes
			}
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			walk(node.Child(i))
		}
	}
	walk(root)
	return helpers
}

func (p *JavaScriptParser) messagePublisherHelperDeclaration(node *sitter.Node, content []byte) (string, []string, *sitter.Node) {
	if node == nil {
		return "", nil, nil
	}
	switch node.Type() {
	case "function_declaration", "function", "function_expression":
		name := ""
		var params []string
		var body *sitter.Node
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			switch child.Type() {
			case "identifier":
				if name == "" {
					name = child.Content(content)
				}
			case "formal_parameters":
				params, _ = p.extractParameterNamesAndTypes(child, content)
			case "statement_block":
				body = child
			}
		}
		return strings.TrimSpace(name), params, body
	case "variable_declarator":
		name := ""
		var value *sitter.Node
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			switch child.Type() {
			case "identifier":
				if name == "" {
					name = child.Content(content)
				}
			case "arrow_function", "function", "function_expression":
				value = child
			}
		}
		if name == "" || value == nil {
			return "", nil, nil
		}
		params, body := p.messagePublisherCallableParamsAndBody(value, content)
		return strings.TrimSpace(name), params, body
	default:
		return "", nil, nil
	}
}

func (p *JavaScriptParser) messagePublisherCallableParamsAndBody(node *sitter.Node, content []byte) ([]string, *sitter.Node) {
	var params []string
	var body *sitter.Node
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "formal_parameters":
			params, _ = p.extractParameterNamesAndTypes(child, content)
		case "identifier":
			if len(params) == 0 {
				params = append(params, child.Content(content))
			}
		case "statement_block", "expression_statement", "call_expression":
			if body == nil {
				body = child
			}
		}
	}
	return params, body
}

func (p *JavaScriptParser) messagePublisherParamIndexes(body *sitter.Node, content []byte, params []string) []int {
	paramIndex := make(map[string]int, len(params))
	for i, param := range params {
		param = strings.TrimSpace(param)
		if param != "" {
			paramIndex[param] = i
		}
	}
	seen := make(map[int]bool)
	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil {
			return
		}
		if node.Type() == "call_expression" {
			methodName, receiver := callMethodFromNode(node, content)
			if p.isMessagePublishingMethod(methodName) {
				if idx, ok := paramIndex[strings.TrimSpace(receiver)]; ok {
					seen[idx] = true
				}
			}
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			walk(node.Child(i))
		}
	}
	walk(body)
	var indexes []int
	for idx := range seen {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)
	return indexes
}

func callMethodFromNode(callNode *sitter.Node, content []byte) (string, string) {
	if callNode == nil || callNode.ChildCount() == 0 {
		return "", ""
	}
	funcNode := callNode.Child(0)
	switch funcNode.Type() {
	case "member_expression", "optional_member_expression":
		receiver, methodName := receiverAndMethodFromMemberNode(funcNode, content)
		return methodName, receiver
	case "identifier":
		name := funcNode.Content(content)
		return name, name
	default:
		return funcNode.Content(content), ""
	}
}

func receiverAndMethodFromMemberNode(node *sitter.Node, content []byte) (string, string) {
	if node == nil {
		return "", ""
	}
	var receiverParts []string
	var methodName string
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "property_identifier", "private_property_identifier":
			methodName = child.Content(content)
		case ".", "?.":
			continue
		default:
			if child.IsNamed() {
				receiverParts = append(receiverParts, child.Content(content))
			}
		}
	}
	if len(receiverParts) == 0 {
		return "", methodName
	}
	return strings.TrimSpace(strings.Join(receiverParts, ".")), strings.TrimSpace(methodName)
}

func (p *JavaScriptParser) isMessagePublishingMethod(methodName string) bool {
	switch strings.TrimSpace(methodName) {
	case "scheduleMessages", "sendMessage", "sendMessages":
		return true
	default:
		return p.config != nil && matchesConfiguredQueueName(methodName, p.config.JavaScriptQueues.SendMethods)
	}
}

func matchesConfiguredQueueName(name string, configured []string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, candidate := range configured {
		if name == strings.TrimSpace(candidate) {
			return true
		}
	}
	return false
}

func (p *JavaScriptParser) extractMessageTargetArgument(node *sitter.Node, content []byte) string {
	argsNode := node.ChildByFieldName("arguments")
	if argsNode == nil {
		for i := 0; i < int(node.ChildCount()); i++ {
			if node.Child(i).Type() == "arguments" {
				argsNode = node.Child(i)
				break
			}
		}
	}
	if argsNode == nil {
		return ""
	}

	for i := 0; i < int(argsNode.ChildCount()); i++ {
		arg := argsNode.Child(i)
		switch arg.Type() {
		case "string":
			value := strings.TrimSpace(strings.Trim(arg.Content(content), "\"'`"))
			if value != "" {
				return value
			}
		case "template_string":
			value := strings.TrimSpace(strings.Trim(arg.Content(content), "`"))
			if value != "" {
				return value
			}
		case "identifier", "property_identifier":
			value := strings.TrimSpace(arg.Content(content))
			if value != "" {
				return value
			}
		case "member_expression", "optional_member_expression":
			if prop := extractMemberPropertyName(arg, content); strings.TrimSpace(prop) != "" {
				return strings.TrimSpace(prop)
			}
		}
	}

	return ""
}

func (p *JavaScriptParser) extractQueueClientFactoryTarget(node *sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}
	methodName, receiver := p.extractCallMethod(node, content)
	if strings.TrimSpace(methodName) != "fromConnectionString" || !strings.Contains(strings.ToLower(receiver), "queueclient") {
		return ""
	}
	if queue := p.extractMessageTargetArgumentAt(node, content, 1); queue != "" {
		return queue
	}
	return ""
}

func (p *JavaScriptParser) directQueueClientEntity(node *sitter.Node, content []byte) string {
	if node == nil || node.Type() != "new_expression" {
		return ""
	}
	ctor := strings.ToLower(strings.TrimSpace(p.extractConstructorNameFromNewExpression(node, content)))
	if !strings.Contains(ctor, "queueclient") {
		return ""
	}
	// The Azure QueueClient constructor commonly receives a queue URL rather
	// than a plain queue name, so avoid inventing a precise resource here.
	return "queue:unknown"
}

func (p *JavaScriptParser) extractMessageTargetArgumentAt(node *sitter.Node, content []byte, index int) string {
	arg := p.extractArgumentNode(node, index)
	if arg == nil {
		return ""
	}
	switch arg.Type() {
	case "string":
		return strings.TrimSpace(strings.Trim(arg.Content(content), "\"'`"))
	case "template_string":
		return strings.TrimSpace(strings.Trim(arg.Content(content), "`"))
	case "identifier", "property_identifier":
		return strings.TrimSpace(arg.Content(content))
	case "member_expression", "optional_member_expression":
		return strings.TrimSpace(extractMemberPropertyName(arg, content))
	default:
		return ""
	}
}

func (p *JavaScriptParser) extractSideEffectTargets(root *sitter.Node, content []byte) map[string]map[string]string {
	if root == nil {
		return map[string]map[string]string{}
	}
	scoped := make(map[string]map[string]string)
	setScopedTarget := func(scope, receiver, entity string) {
		scope = strings.TrimSpace(scope)
		receiver = strings.TrimSpace(receiver)
		entity = strings.TrimSpace(entity)
		if scope == "" || receiver == "" || entity == "" {
			return
		}
		if scoped[scope] == nil {
			scoped[scope] = make(map[string]string)
		}
		scoped[scope][receiver] = entity
	}
	targetForReceiver := func(scope, receiver string) string {
		receiver = strings.TrimSpace(receiver)
		if receiver == "" {
			return ""
		}
		if scopedTargets, ok := scoped[scope]; ok {
			if entity := strings.TrimSpace(scopedTargets[receiver]); entity != "" {
				return entity
			}
		}
		if scopedTargets, ok := scoped["_module_"]; ok {
			if entity := strings.TrimSpace(scopedTargets[receiver]); entity != "" {
				return entity
			}
		}
		return ""
	}
	scopeNameForNode := func(node *sitter.Node) string {
		if name := strings.TrimSpace(p.findContainingCallableScope(node, content)); name != "" {
			return name
		}
		return "_module_"
	}

	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil {
			return
		}
		if node.Type() == "variable_declarator" {
			name, value := extractDeclaratorNameAndValue(node, content)
			if name != "" && value != nil {
				value = unwrapAwaitExpression(value)
				if value.Type() == "new_expression" {
					if entity := p.directQueueClientEntity(value, content); entity != "" {
						setScopedTarget(scopeNameForNode(node), name, entity)
						for i := 0; i < int(node.ChildCount()); i++ {
							walk(node.Child(i))
						}
						return
					}
				}
				if value.Type() != "call_expression" {
					for i := 0; i < int(node.ChildCount()); i++ {
						walk(node.Child(i))
					}
					return
				}
				methodName, receiver := p.extractCallMethod(value, content)
				scope := scopeNameForNode(node)
				switch methodName {
				case "getContainerClient":
					container := p.extractFirstStringArgument(value, content)
					if container == "" {
						container = "unknown"
					}
					setScopedTarget(scope, name, "blob:"+container)
				case "load":
					if queue := p.extractLoadedQueueTarget(value, content, receiver); queue != "" {
						setScopedTarget(scope, name, "queue:"+queue)
					}
				case "getBlobClient", "getBlockBlobClient", "getAppendBlobClient", "getPageBlobClient":
					if entity := targetForReceiver(scope, receiver); entity != "" {
						setScopedTarget(scope, name, entity)
					}
				case "getQueueClient", "createSender", "createReceiver":
					queue := p.extractFirstStringArgument(value, content)
					if queue == "" {
						queue = p.extractMessageTargetArgument(value, content)
					}
					if queue == "" {
						queue = "unknown"
					}
					setScopedTarget(scope, name, "queue:"+queue)
				case "fromConnectionString":
					if queue := p.extractQueueClientFactoryTarget(value, content); queue != "" {
						setScopedTarget(scope, name, "queue:"+queue)
					} else if strings.Contains(strings.ToLower(receiver), "queueclient") {
						setScopedTarget(scope, name, "queue:unknown")
					}
				default:
					if entity := contentstackTargetForCall(methodName, receiver, targetForReceiver(scope, receiver), p.extractFirstStringArgument(value, content)); entity != "" {
						setScopedTarget(scope, name, entity)
					} else if _, entity := contentstackDataAccess(methodName, receiver, targetForReceiver(scope, receiver)); entity != "" {
						setScopedTarget(scope, name, entity)
					}
				}
			}
		}
		if node.Type() == "call_expression" {
			methodName, receiver := p.extractCallMethod(node, content)
			if strings.TrimSpace(methodName) == "load" {
				if queue := p.extractLoadedQueueTarget(node, content, receiver); queue != "" {
					setScopedTarget(scopeNameForNode(node), receiver, "queue:"+queue)
				}
			}
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			walk(node.Child(i))
		}
	}

	walk(root)
	return scoped
}

func (p *JavaScriptParser) extractLoadedQueueTarget(node *sitter.Node, content []byte, receiver string) string {
	if p.config == nil || !matchesConfiguredQueueName(receiver, p.config.JavaScriptQueues.LoadReceivers) {
		return ""
	}
	return strings.TrimSpace(p.extractMessageTargetArgumentAt(node, content, 0))
}

func (p *JavaScriptParser) extractSideEffectDataAccess(node *sitter.Node, content []byte, sideEffectTargets map[string]map[string]string) *ParsedDataAccess {
	if node == nil || node.Type() != "call_expression" {
		return nil
	}
	methodName, receiver := p.extractCallMethod(node, content)
	method := strings.TrimSpace(methodName)
	if method == "" {
		return nil
	}
	if access, entity := prismaDataAccess(method, receiver); access != "" {
		return &ParsedDataAccess{
			EntityName: entity,
			Access:     access,
			LineNumber: int(node.StartPoint().Row) + 1,
			Source:     "javascript_prisma",
		}
	}
	scope := "_module_"
	if name := strings.TrimSpace(p.findContainingCallableScope(node, content)); name != "" {
		scope = name
	}
	entity := sideEffectEntityForReceiver(scope, receiver, sideEffectTargets)
	if access := blobAccessKind(method); access != "" {
		if entity == "" && strings.Contains(strings.ToLower(receiver), "blob") {
			entity = "blob"
		}
		if entity != "" {
			return &ParsedDataAccess{
				EntityName: entity,
				Access:     access,
				LineNumber: int(node.StartPoint().Row) + 1,
				Source:     "javascript_side_effect",
			}
		}
	}
	if access := p.queueAccessKind(method); access != "" {
		if entity == "" && strings.Contains(strings.ToLower(receiver), "queue") {
			entity = "queue"
		}
		if entity != "" {
			return &ParsedDataAccess{
				EntityName: entity,
				Access:     access,
				LineNumber: int(node.StartPoint().Row) + 1,
				Source:     "javascript_side_effect",
			}
		}
	}
	if access, docEntity := documentSideEffect(method, receiver); access != "" {
		return &ParsedDataAccess{
			EntityName: docEntity,
			Access:     access,
			LineNumber: int(node.StartPoint().Row) + 1,
			Source:     "javascript_side_effect",
		}
	}
	if access, cmsEntity := contentstackDataAccess(method, receiver, sideEffectEntityForReceiver(scope, receiver, sideEffectTargets)); access != "" {
		return &ParsedDataAccess{
			EntityName: cmsEntity,
			Access:     access,
			LineNumber: int(node.StartPoint().Row) + 1,
			Source:     "javascript_contentstack",
		}
	}
	return nil
}

func sideEffectEntityForReceiver(scope, receiver string, sideEffectTargets map[string]map[string]string) string {
	if len(sideEffectTargets) == 0 {
		return ""
	}
	receiver = strings.TrimSpace(receiver)
	if receiver == "" {
		return ""
	}
	if scopedTargets, ok := sideEffectTargets[scope]; ok {
		if entity := strings.TrimSpace(scopedTargets[receiver]); entity != "" {
			return entity
		}
	}
	if scopedTargets, ok := sideEffectTargets["_module_"]; ok {
		if entity := strings.TrimSpace(scopedTargets[receiver]); entity != "" {
			return entity
		}
	}
	return ""
}

func blobAccessKind(method string) string {
	switch strings.TrimSpace(method) {
	case "upload", "uploadData", "uploadFile", "uploadStream", "uploadBlob", "stageBlock", "commitBlockList":
		return "write"
	case "delete", "deleteIfExists", "deleteBlob":
		return "delete"
	case "download", "downloadToBuffer", "downloadToFile", "listBlobsFlat", "listBlobsByHierarchy":
		return "read"
	default:
		return ""
	}
}

func prismaDataAccess(method, receiver string) (string, string) {
	access := prismaAccessKind(method)
	if access == "" {
		return "", ""
	}
	model := prismaReceiverModel(receiver)
	if model == "" {
		return "", ""
	}
	return access, "prisma:" + model
}

func prismaAccessKind(method string) string {
	switch strings.TrimSpace(method) {
	case "findUnique", "findUniqueOrThrow", "findFirst", "findFirstOrThrow", "findMany", "aggregate", "count", "groupBy":
		return "read"
	case "create", "createMany", "createManyAndReturn", "upsert", "update", "updateMany", "updateManyAndReturn":
		return "write"
	case "delete", "deleteMany":
		return "delete"
	default:
		return ""
	}
}

func prismaReceiverModel(receiver string) string {
	receiver = strings.TrimSpace(receiver)
	if receiver == "" {
		return ""
	}
	parts := strings.Split(receiver, ".")
	for i, part := range parts {
		if strings.EqualFold(strings.TrimSpace(part), "prisma") && i+1 < len(parts) {
			return strings.TrimSpace(parts[i+1])
		}
	}
	return ""
}

func (p *JavaScriptParser) queueAccessKind(method string) string {
	method = strings.TrimSpace(method)
	switch method {
	case "sendMessage", "sendMessages", "scheduleMessages":
		return "write"
	case "receiveMessages", "peekMessages":
		return "read"
	case "deleteMessage", "clearMessages":
		return "delete"
	case "updateMessage":
		return "write"
	}
	if p.config != nil {
		patterns := p.config.JavaScriptQueues
		switch {
		case matchesConfiguredQueueName(method, patterns.SendMethods):
			return "write"
		case matchesConfiguredQueueName(method, patterns.ReadMethods):
			return "read"
		case matchesConfiguredQueueName(method, patterns.DeleteMethods):
			return "delete"
		}
	}
	return ""
}

func documentSideEffect(method, receiver string) (string, string) {
	lowerReceiver := strings.ToLower(strings.TrimSpace(receiver))
	switch strings.TrimSpace(method) {
	case "toBuffer", "toBlob", "toStream":
		if strings.Contains(lowerReceiver, "packer") {
			return "generate", "document:docx"
		}
	case "launch":
		if lowerReceiver == "puppeteer" {
			return "generate", "document:browser_render"
		}
	case "pdf":
		return "generate", "document:pdf"
	}
	return "", ""
}

func contentstackTargetForCall(method, receiver, inheritedEntity, firstArg string) string {
	method = strings.ToLower(strings.TrimSpace(method))
	if !contentstackChainMethod(method) {
		return ""
	}
	if strings.TrimSpace(inheritedEntity) == "" {
		inheritedEntity = contentstackEntityFromReceiverChain(receiver)
	}
	if strings.TrimSpace(inheritedEntity) == "" && !looksLikeContentstackReceiver(receiver) {
		return ""
	}
	entity := "cms:contentstack"
	kind := ""
	switch method {
	case "contenttype", "content_type", "contenttypes":
		kind = "content_type"
	case "entry", "entries":
		kind = "entry"
	case "asset", "assets":
		kind = "asset"
	case "environment", "environments":
		kind = "environment"
	case "globalfield", "global_field", "globalfields":
		kind = "global_field"
	case "webhook", "webhooks":
		kind = "webhook"
	case "extension", "extensions":
		kind = "extension"
	}
	if kind == "" {
		return entity
	}
	arg := normalizeCMSResourceName(firstArg)
	if arg == "" {
		return entity + ":" + kind
	}
	return entity + ":" + kind + ":" + arg
}

func contentstackDataAccess(method, receiver, entity string) (string, string) {
	if strings.TrimSpace(entity) == "" {
		entity = contentstackEntityFromReceiverChain(receiver)
	}
	if strings.TrimSpace(entity) == "" && !looksLikeContentstackReceiver(receiver) {
		return "", ""
	}
	access := contentstackAccessKind(method)
	if access == "" {
		return "", ""
	}
	if strings.TrimSpace(entity) == "" {
		entity = "cms:contentstack"
	}
	return access, entity
}

func contentstackEntityFromReceiverChain(receiver string) string {
	receiver = strings.TrimSpace(receiver)
	if receiver == "" {
		return ""
	}
	lower := strings.ToLower(receiver)
	if !strings.Contains(lower, "stack") &&
		!strings.Contains(lower, "contenttype") &&
		!strings.Contains(lower, "content_type") &&
		!strings.Contains(lower, "entry") &&
		!strings.Contains(lower, "asset") {
		return ""
	}
	entity := ""
	for _, match := range contentstackChainCallPattern.FindAllStringSubmatch(receiver, -1) {
		if len(match) < 3 {
			continue
		}
		method := strings.ToLower(strings.TrimSpace(match[1]))
		arg := normalizeCMSResourceName(match[2])
		kind := ""
		switch method {
		case "contenttype", "contenttypes":
			kind = "content_type"
		case "content_type":
			kind = "content_type"
		case "entry", "entries":
			kind = "entry"
		case "asset", "assets":
			kind = "asset"
		case "environment", "environments":
			kind = "environment"
		case "globalfield", "globalfields":
			kind = "global_field"
		case "global_field":
			kind = "global_field"
		case "webhook", "webhooks":
			kind = "webhook"
		case "extension", "extensions":
			kind = "extension"
		default:
			continue
		}
		if arg == "" {
			entity = "cms:contentstack:" + kind
		} else {
			entity = "cms:contentstack:" + kind + ":" + arg
		}
	}
	if entity != "" {
		return entity
	}
	if looksLikeContentstackReceiver(receiver) {
		return "cms:contentstack"
	}
	return ""
}

func contentstackChainMethod(method string) bool {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "stack", "contenttype", "content_type", "contenttypes", "entry", "entries", "asset", "assets",
		"environment", "environments", "globalfield", "global_field", "globalfields", "webhook", "webhooks",
		"extension", "extensions":
		return true
	default:
		return false
	}
}

func contentstackAccessKind(method string) string {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "fetch", "find", "findone", "findall", "query", "includeowner", "includecontenttype", "tojson":
		return "read"
	case "create", "update", "publish", "unpublish", "import", "upload", "replace", "setworkflowstage":
		return "write"
	case "delete", "deleteall", "remove":
		return "delete"
	default:
		return ""
	}
}

func looksLikeContentstackReceiver(receiver string) bool {
	lower := strings.ToLower(strings.TrimSpace(receiver))
	return strings.Contains(lower, "contentstack") ||
		strings.Contains(lower, "managementclient") ||
		strings.Contains(lower, ".stack(") ||
		strings.HasSuffix(lower, ".stack")
}

func normalizeCMSResourceName(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "\"'`")
	value = strings.Trim(value, "{}")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

func (p *JavaScriptParser) extractGraphQLOperationRegistryUsage(node *sitter.Node, content []byte, registryHelperAliases map[string]bool) *ParsedGraphQLOperationUsage {
	if node == nil || node.Type() != "call_expression" {
		return nil
	}

	methodName, receiver := p.extractCallMethod(node, content)
	callName := strings.TrimSpace(receiver)
	if callName == "" {
		callName = strings.TrimSpace(methodName)
	}
	if !registryHelperAliases[callName] {
		return nil
	}

	argsNode := node.ChildByFieldName("arguments")
	if argsNode == nil {
		for i := 0; i < int(node.ChildCount()); i++ {
			if node.Child(i).Type() == "arguments" {
				argsNode = node.Child(i)
				break
			}
		}
	}
	if argsNode == nil {
		return nil
	}

	var operationName string
	for i := 0; i < int(argsNode.ChildCount()); i++ {
		arg := argsNode.Child(i)
		if arg.Type() != "string" {
			continue
		}
		value := strings.TrimSpace(strings.Trim(arg.Content(content), "\"'`"))
		if looksLikeGraphQLOperationName(value) {
			operationName = value
		}
	}
	if operationName == "" {
		return nil
	}

	funcName := p.findContainingFunction(node, content)
	if funcName == "" {
		funcName = "_module_"
	}

	return &ParsedGraphQLOperationUsage{
		ImportPath:   "__graphql_operation__:" + operationName,
		ImportedAs:   operationName,
		FunctionName: funcName,
		LineNumber:   int(node.StartPoint().Row) + 1,
	}
}

func (p *JavaScriptParser) extractGraphQLClientRequestUsages(node *sitter.Node, content []byte) []ParsedGraphQLOperationUsage {
	if node == nil || node.Type() != "call_expression" {
		return nil
	}

	methodName, receiver := p.extractCallMethod(node, content)
	if strings.TrimSpace(methodName) != "request" || !looksLikeGraphQLClientReceiver(receiver) {
		return nil
	}

	argsNode := node.ChildByFieldName("arguments")
	if argsNode == nil {
		for i := 0; i < int(node.ChildCount()); i++ {
			if node.Child(i).Type() == "arguments" {
				argsNode = node.Child(i)
				break
			}
		}
	}
	if argsNode == nil {
		return nil
	}

	var queryNode *sitter.Node
	for i := 0; i < int(argsNode.ChildCount()); i++ {
		arg := argsNode.Child(i)
		switch arg.Type() {
		case "string", "template_string", "identifier", "member_expression":
			queryNode = arg
		}
		if queryNode != nil {
			break
		}
	}
	if queryNode == nil {
		return nil
	}

	if queryNode.Type() == "identifier" || queryNode.Type() == "member_expression" {
		resolved := p.resolveNodeValueBeforeNode(node, queryNode, content)
		if resolved == nil {
			return nil
		}
		queryNode = resolved
	}

	ops := extractGraphQLRequestOperationsFromValueNode(queryNode, content)
	if len(ops) == 0 {
		return nil
	}

	funcName := p.findContainingFunction(node, content)
	if funcName == "" {
		funcName = "_module_"
	}
	usages := make([]ParsedGraphQLOperationUsage, 0, len(ops))
	for _, op := range ops {
		if op.Name == "" {
			continue
		}
		usages = append(usages, ParsedGraphQLOperationUsage{
			ImportPath:   "__graphql_operation__:" + op.Name,
			ImportedAs:   op.Name,
			FunctionName: funcName,
			LineNumber:   int(node.StartPoint().Row) + 1,
		})
	}
	return usages
}

func looksLikeGraphQLClientReceiver(receiver string) bool {
	receiver = strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(strings.TrimSpace(receiver)))
	return strings.Contains(receiver, "graphql") || strings.Contains(receiver, "gql")
}

func (p *JavaScriptParser) resolveNodeValueBeforeNode(refNode, valueNode *sitter.Node, content []byte) *sitter.Node {
	if valueNode == nil {
		return nil
	}
	switch valueNode.Type() {
	case "identifier":
		name := valueNode.Content(content)
		if resolved := p.findVariableValueBeforeNode(refNode, name, content); resolved != nil {
			return resolved
		}
		return p.findModuleVariableValueBeforeNode(refNode, name, content)
	case "member_expression":
		if prop := extractMemberPropertyName(valueNode, content); prop != "" {
			if resolved := p.findVariableValueBeforeNode(refNode, prop, content); resolved != nil {
				return resolved
			}
			return p.findModuleVariableValueBeforeNode(refNode, prop, content)
		}
	}
	return nil
}

func (p *JavaScriptParser) extractGraphQLBackendEntrypoints(root *sitter.Node, content []byte) []ParsedGraphQLBackendEntrypoint {
	if root == nil || p.config == nil || len(p.config.GraphQLRegistrations) == 0 {
		return nil
	}

	seen := make(map[string]bool)
	var entrypoints []ParsedGraphQLBackendEntrypoint

	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil {
			return
		}
		if node.Type() == "call_expression" {
			methodName, receiver := p.extractCallMethod(node, content)
			for _, registration := range p.config.GraphQLRegistrations {
				if strings.TrimSpace(registration.Receiver) == "" || strings.TrimSpace(registration.Method) == "" ||
					receiver != registration.Receiver || methodName != registration.Method {
					continue
				}
				line := int(node.StartPoint().Row) + 1
				controllersPath := extractGraphQLControllersPath(node, content, registration.ControllersPathKey)
				if controllersPath == "" {
					continue
				}
				handlerName := p.findContainingCallableScope(node, content)
				if handlerName == "" {
					handlerName = "_module_"
				}
				key := receiver + "." + methodName + "|" + controllersPath + "|" + strconv.Itoa(line)
				if !seen[key] {
					seen[key] = true
					entrypoints = append(entrypoints, ParsedGraphQLBackendEntrypoint{
						HandlerName:      handlerName,
						RegistrationKind: receiver + "." + methodName,
						ControllersPath:  controllersPath,
						LineNumber:       line,
					})
				}
			}
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			walk(node.Child(i))
		}
	}

	walk(root)
	return entrypoints
}

func (p *JavaScriptParser) extractGraphQLOperationResolvers(root *sitter.Node, content []byte) []ParsedGraphQLOperationResolver {
	if root == nil {
		return nil
	}
	permissionRuleIdentifiers := graphQLPermissionRuleIdentifiers(content)

	type exportAssignment struct {
		name string
		line int
	}
	operationTypes := make(map[string]string)
	exportedResolvers := make(map[string]exportAssignment)

	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil {
			return
		}
		if node.Type() == "assignment_expression" {
			left, right := assignmentSides(node)
			if left != nil && right != nil {
				prop := moduleExportsProperty(left, content)
				switch prop {
				case "queries", "mutations", "subscriptions":
					opType := graphQLOperationTypeForExportList(prop)
					for _, name := range stringArrayValues(right, content) {
						if looksLikeGraphQLResolverOperationName(name) {
							operationTypes[name] = opType
						}
					}
				case "":
				default:
					if isGraphQLResolverValue(right) {
						exportedResolvers[prop] = exportAssignment{name: prop, line: int(node.StartPoint().Row) + 1}
					}
				}
			}
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			walk(node.Child(i))
		}
	}

	walk(root)

	resolverByKey := make(map[string]ParsedGraphQLOperationResolver)
	addResolver := func(resolver ParsedGraphQLOperationResolver) {
		if resolver.OperationName == "" || resolver.OperationType == "" || resolver.ResolverName == "" {
			return
		}
		key := resolver.OperationType + "|" + resolver.OperationName + "|" + resolver.ResolverName + "|" + strconv.Itoa(resolver.LineNumber)
		resolverByKey[key] = resolver
	}

	if len(operationTypes) > 0 && len(exportedResolvers) > 0 {
		names := make([]string, 0, len(operationTypes))
		for name := range operationTypes {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			exported, ok := exportedResolvers[name]
			if !ok {
				continue
			}
			addResolver(ParsedGraphQLOperationResolver{
				OperationName: name,
				OperationType: operationTypes[name],
				ResolverName:  exported.name,
				LineNumber:    exported.line,
			})
		}
	}
	for _, resolver := range p.extractGraphQLOperationResolversFromObjectMaps(root, content, permissionRuleIdentifiers) {
		addResolver(resolver)
	}
	if len(resolverByKey) == 0 {
		return nil
	}
	resolvers := make([]ParsedGraphQLOperationResolver, 0, len(resolverByKey))
	for _, resolver := range resolverByKey {
		resolvers = append(resolvers, resolver)
	}
	sort.Slice(resolvers, func(i, j int) bool {
		if resolvers[i].OperationType != resolvers[j].OperationType {
			return resolvers[i].OperationType < resolvers[j].OperationType
		}
		if resolvers[i].OperationName != resolvers[j].OperationName {
			return resolvers[i].OperationName < resolvers[j].OperationName
		}
		return resolvers[i].LineNumber < resolvers[j].LineNumber
	})
	return resolvers
}

func (p *JavaScriptParser) extractGraphQLOperationResolversFromObjectMaps(root *sitter.Node, content []byte, permissionRuleIdentifiers map[string]bool) []ParsedGraphQLOperationResolver {
	if root == nil {
		return nil
	}
	var resolvers []ParsedGraphQLOperationResolver
	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil {
			return
		}
		if node.Type() == "object" {
			resolvers = append(resolvers, graphQLOperationResolversFromObject(node, content, permissionRuleIdentifiers)...)
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			walk(node.Child(i))
		}
	}
	walk(root)
	return resolvers
}

func (p *JavaScriptParser) extractGraphQLOperationPermissions(root *sitter.Node, content []byte) []ParsedGraphQLOperationPermission {
	if root == nil {
		return nil
	}
	permissionRuleIdentifiers := graphQLPermissionRuleIdentifiers(content)
	var permissions []ParsedGraphQLOperationPermission
	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil {
			return
		}
		if node.Type() == "object" {
			permissions = append(permissions, graphQLOperationPermissionsFromObject(node, content, permissionRuleIdentifiers)...)
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			walk(node.Child(i))
		}
	}
	walk(root)
	if len(permissions) == 0 {
		return nil
	}
	sort.Slice(permissions, func(i, j int) bool {
		if permissions[i].OperationType != permissions[j].OperationType {
			return permissions[i].OperationType < permissions[j].OperationType
		}
		if permissions[i].OperationName != permissions[j].OperationName {
			return permissions[i].OperationName < permissions[j].OperationName
		}
		return permissions[i].LineNumber < permissions[j].LineNumber
	})
	return permissions
}

func graphQLOperationTypeForExportList(prop string) string {
	switch prop {
	case "queries":
		return "query"
	case "mutations":
		return "mutation"
	case "subscriptions":
		return "subscription"
	default:
		return strings.TrimSuffix(prop, "s")
	}
}

func graphQLOperationResolversFromObject(objectNode *sitter.Node, content []byte, permissionRuleIdentifiers map[string]bool) []ParsedGraphQLOperationResolver {
	var resolvers []ParsedGraphQLOperationResolver
	for i := 0; i < int(objectNode.ChildCount()); i++ {
		child := objectNode.Child(i)
		if child == nil || child.Type() != "pair" {
			continue
		}
		key := graphQLObjectPropertyName(child.ChildByFieldName("key"), content)
		opType := graphQLOperationTypeForResolverMapKey(key)
		if opType == "" {
			continue
		}
		value := child.ChildByFieldName("value")
		if value == nil || value.Type() != "object" {
			continue
		}
		resolvers = append(resolvers, graphQLOperationResolversFromTypedMap(value, opType, content, permissionRuleIdentifiers)...)
	}
	return resolvers
}

func graphQLOperationResolversFromTypedMap(objectNode *sitter.Node, opType string, content []byte, permissionRuleIdentifiers map[string]bool) []ParsedGraphQLOperationResolver {
	if graphQLOperationTypedMapLooksPermissionMap(objectNode, content, permissionRuleIdentifiers) {
		return nil
	}
	var resolvers []ParsedGraphQLOperationResolver
	for i := 0; i < int(objectNode.ChildCount()); i++ {
		child := objectNode.Child(i)
		if child == nil {
			continue
		}
		switch child.Type() {
		case "pair", "property":
			name := graphQLObjectPropertyName(child.ChildByFieldName("key"), content)
			if !looksLikeGraphQLResolverOperationName(name) {
				continue
			}
			value := child.ChildByFieldName("value")
			if !isGraphQLResolverMapValue(value, content, permissionRuleIdentifiers) {
				continue
			}
			resolvers = append(resolvers, ParsedGraphQLOperationResolver{
				OperationName: name,
				OperationType: opType,
				ResolverName:  graphQLResolverMapValueName(name, value, content),
				LineNumber:    int(child.StartPoint().Row) + 1,
			})
		case "method_definition":
			name := graphQLMethodDefinitionName(child, content)
			if !looksLikeGraphQLResolverOperationName(name) {
				continue
			}
			resolvers = append(resolvers, ParsedGraphQLOperationResolver{
				OperationName: name,
				OperationType: opType,
				ResolverName:  name,
				LineNumber:    int(child.StartPoint().Row) + 1,
			})
		}
	}
	return resolvers
}

func graphQLOperationPermissionsFromObject(objectNode *sitter.Node, content []byte, permissionRuleIdentifiers map[string]bool) []ParsedGraphQLOperationPermission {
	var permissions []ParsedGraphQLOperationPermission
	for i := 0; i < int(objectNode.ChildCount()); i++ {
		child := objectNode.Child(i)
		if child == nil || child.Type() != "pair" {
			continue
		}
		key := graphQLObjectPropertyName(child.ChildByFieldName("key"), content)
		opType := graphQLOperationTypeForResolverMapKey(key)
		if opType == "" {
			continue
		}
		value := child.ChildByFieldName("value")
		if value == nil || value.Type() != "object" {
			continue
		}
		permissions = append(permissions, graphQLOperationPermissionsFromTypedMap(value, opType, content, permissionRuleIdentifiers)...)
	}
	return permissions
}

func graphQLOperationPermissionsFromTypedMap(objectNode *sitter.Node, opType string, content []byte, permissionRuleIdentifiers map[string]bool) []ParsedGraphQLOperationPermission {
	if !graphQLOperationTypedMapLooksPermissionMap(objectNode, content, permissionRuleIdentifiers) {
		return nil
	}
	var permissions []ParsedGraphQLOperationPermission
	for i := 0; i < int(objectNode.ChildCount()); i++ {
		child := objectNode.Child(i)
		if child == nil || child.Type() != "pair" {
			continue
		}
		name := graphQLObjectPropertyName(child.ChildByFieldName("key"), content)
		if !looksLikeGraphQLResolverOperationName(name) {
			continue
		}
		value := child.ChildByFieldName("value")
		if !isGraphQLPermissionRuleCandidate(value, content, permissionRuleIdentifiers) {
			continue
		}
		permissions = append(permissions, ParsedGraphQLOperationPermission{
			OperationName:  name,
			OperationType:  opType,
			RuleExpression: compactGraphQLPermissionRule(value.Content(content)),
			LineNumber:     int(child.StartPoint().Row) + 1,
		})
	}
	return permissions
}

func graphQLOperationTypedMapLooksPermissionMap(objectNode *sitter.Node, content []byte, permissionRuleIdentifiers map[string]bool) bool {
	if objectNode == nil {
		return false
	}
	for i := 0; i < int(objectNode.ChildCount()); i++ {
		child := objectNode.Child(i)
		if child == nil || child.Type() != "pair" {
			continue
		}
		value := child.ChildByFieldName("value")
		if isGraphQLPermissionRuleCandidate(value, content, permissionRuleIdentifiers) {
			return true
		}
	}
	return false
}

func graphQLOperationTypeForResolverMapKey(key string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "query", "queries":
		return "query"
	case "mutation", "mutations":
		return "mutation"
	case "subscription", "subscriptions":
		return "subscription"
	default:
		return ""
	}
}

func graphQLObjectPropertyName(node *sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}
	switch node.Type() {
	case "property_identifier", "identifier", "shorthand_property_identifier":
		return strings.TrimSpace(node.Content(content))
	case "string", "template_string":
		return strings.Trim(strings.TrimSpace(node.Content(content)), "\"'`")
	default:
		return strings.Trim(strings.TrimSpace(node.Content(content)), "\"'`")
	}
}

func isGraphQLPermissionMapValue(node *sitter.Node, content []byte) bool {
	if node == nil {
		return false
	}
	switch node.Type() {
	case "identifier", "property_identifier":
		return isGraphQLShieldRuleValue(strings.TrimSpace(node.Content(content)))
	case "call_expression":
		return isGraphQLShieldRuleCall(node, content)
	default:
		return false
	}
}

func isGraphQLPermissionRuleCandidate(node *sitter.Node, content []byte, permissionRuleIdentifiers map[string]bool) bool {
	if node == nil {
		return false
	}
	if isGraphQLPermissionMapValue(node, content) {
		return true
	}
	switch node.Type() {
	case "identifier", "property_identifier":
		return permissionRuleIdentifiers[strings.TrimSpace(node.Content(content))]
	case "member_expression", "optional_member_expression", "call_expression":
		return !callExpressionContainsFunction(node)
	default:
		return false
	}
}

func compactGraphQLPermissionRule(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func isGraphQLResolverMapValue(node *sitter.Node, content []byte, permissionRuleIdentifiers map[string]bool) bool {
	if node == nil {
		return false
	}
	switch node.Type() {
	case "function", "function_declaration", "arrow_function", "generator_function", "method_definition":
		return true
	case "identifier", "property_identifier":
		value := strings.TrimSpace(node.Content(content))
		return !isGraphQLShieldRuleValue(value) && !permissionRuleIdentifiers[value]
	case "member_expression", "optional_member_expression":
		return true
	case "call_expression":
		if isGraphQLShieldRuleCall(node, content) {
			return false
		}
		return callExpressionContainsFunction(node)
	default:
		return false
	}
}

func graphQLResolverMapValueName(fallback string, node *sitter.Node, content []byte) string {
	if node == nil {
		return fallback
	}
	switch node.Type() {
	case "identifier", "property_identifier", "member_expression", "optional_member_expression":
		if value := strings.TrimSpace(node.Content(content)); value != "" {
			return value
		}
	}
	return fallback
}

func isGraphQLShieldRuleValue(value string) bool {
	switch strings.TrimSpace(value) {
	case "allow", "deny", "or", "and", "not", "chain", "race", "rule", "inputRule", "shield":
		return true
	default:
		return false
	}
}

func graphQLPermissionRuleIdentifiers(content []byte) map[string]bool {
	matches := graphQLPermissionRuleAssignPattern.FindAllSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make(map[string]bool, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		name := strings.TrimSpace(string(match[1]))
		if name != "" {
			out[name] = true
		}
	}
	return out
}

func isGraphQLShieldRuleCall(node *sitter.Node, content []byte) bool {
	// This extraction reads an existing AST and needs no native parser handles.
	var syntax JavaScriptParser
	method, receiver := syntax.extractCallMethod(node, content)
	if isGraphQLShieldRuleValue(method) || isGraphQLShieldRuleValue(receiver) {
		return true
	}
	return false
}

func callExpressionContainsFunction(node *sitter.Node) bool {
	if node == nil {
		return false
	}
	switch node.Type() {
	case "function", "function_declaration", "arrow_function", "generator_function":
		return true
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		if callExpressionContainsFunction(node.Child(i)) {
			return true
		}
	}
	return false
}

func graphQLMethodDefinitionName(node *sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}
	if nameNode := node.ChildByFieldName("name"); nameNode != nil {
		return graphQLObjectPropertyName(nameNode, content)
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		switch child.Type() {
		case "property_identifier", "identifier", "string":
			return graphQLObjectPropertyName(child, content)
		}
	}
	return ""
}

func assignmentSides(node *sitter.Node) (*sitter.Node, *sitter.Node) {
	if node == nil || node.Type() != "assignment_expression" {
		return nil, nil
	}
	left := node.ChildByFieldName("left")
	right := node.ChildByFieldName("right")
	if left != nil || right != nil {
		return left, right
	}
	var nodes []*sitter.Node
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil || child.IsNamed() == false {
			continue
		}
		nodes = append(nodes, child)
	}
	if len(nodes) >= 2 {
		return nodes[0], nodes[1]
	}
	return nil, nil
}

func moduleExportsProperty(node *sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}
	text := strings.TrimSpace(node.Content(content))
	if strings.HasPrefix(text, "module.exports.") {
		return strings.TrimSpace(strings.TrimPrefix(text, "module.exports."))
	}
	for _, prefix := range []string{"module.exports[", "exports["} {
		if strings.HasPrefix(text, prefix) && strings.HasSuffix(text, "]") {
			prop := strings.TrimSuffix(strings.TrimPrefix(text, prefix), "]")
			return strings.Trim(prop, "\"'` ")
		}
	}
	if strings.HasPrefix(text, "exports.") {
		return strings.TrimSpace(strings.TrimPrefix(text, "exports."))
	}
	return ""
}

func stringArrayValues(node *sitter.Node, content []byte) []string {
	if node == nil {
		return nil
	}
	var values []string
	var walk func(*sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.Type() == "string" || n.Type() == "template_string" {
			value := strings.Trim(strings.TrimSpace(n.Content(content)), "\"'`")
			if value != "" {
				values = append(values, value)
			}
			return
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(node)
	return values
}

func isGraphQLResolverValue(node *sitter.Node) bool {
	if node == nil {
		return false
	}
	switch node.Type() {
	case "function", "function_declaration", "arrow_function", "generator_function":
		return true
	case "call_expression":
		// Allows wrappers such as auth(async (...) => ...). The resolver
		// name still comes from the module.exports.<operation> assignment.
		return callExpressionContainsFunction(node)
	default:
		return false
	}
}

func looksLikeGraphQLResolverOperationName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	return graphQLResolverOperationNamePattern.MatchString(value)
}

func isGraphQLDocumentPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(path)))
	return ext == ".gql" || ext == ".graphql"
}

func isImportIdentifier(node *sitter.Node) bool {
	for current := node.Parent(); current != nil; current = current.Parent() {
		switch current.Type() {
		case "import_statement", "import_clause", "import_specifier", "namespace_import", "named_imports":
			return true
		}
	}
	return false
}

func extractGraphQLControllersPath(callNode *sitter.Node, content []byte, pathKey string) string {
	if callNode == nil {
		return ""
	}
	args := callNode.ChildByFieldName("arguments")
	if args == nil {
		for i := 0; i < int(callNode.ChildCount()); i++ {
			child := callNode.Child(i)
			if child.Type() == "arguments" {
				args = child
				break
			}
		}
	}
	if args == nil {
		return ""
	}

	for i := 0; i < int(args.ChildCount()); i++ {
		arg := args.Child(i)
		if arg.Type() != "object" {
			if pathKey == "" && arg.Type() != "comment" {
				if text := strings.TrimSpace(arg.Content(content)); text != "" && !isArgumentPunctuation(text) {
					return text
				}
			}
			continue
		}
		for j := 0; j < int(arg.ChildCount()); j++ {
			pair := arg.Child(j)
			if pair.Type() != "pair" {
				continue
			}
			keyNode := pair.ChildByFieldName("key")
			valueNode := pair.ChildByFieldName("value")
			if keyNode == nil || valueNode == nil {
				continue
			}
			key := strings.Trim(keyNode.Content(content), "\"'` ")
			if pathKey == "" || key != pathKey {
				continue
			}
			return strings.TrimSpace(valueNode.Content(content))
		}
	}

	return ""
}

func isArgumentPunctuation(text string) bool {
	switch text {
	case "(", ")", ",":
		return true
	default:
		return false
	}
}

func (p *JavaScriptParser) extractFunction(node *sitter.Node, content []byte, lines []string) *ParsedFunction {
	var name string
	var params []string
	var paramTypes []string
	var returnType string
	var typeParams []ParsedTypeParameter
	isAsync := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "identifier":
			if name == "" {
				name = child.Content(content)
			}
		case "async":
			isAsync = true
		case "formal_parameters":
			params, paramTypes = p.extractParameterNamesAndTypes(child, content)
		case "type_annotation":
			// Return type: function foo(): Type
			returnType = p.extractTypeAnnotation(child, content)
		case "type_parameters":
			// Generic function: function foo<T>()
			typeParams = p.extractTypeParameters(child, content)
		}
	}

	if name == "" {
		name = p.syntheticCallbackName(node, content)
		if name == "" {
			name = p.functionExpressionBindingName(node, content)
		}
		if name == "" {
			name = anonymousFunctionName(node)
		}
	}

	startLine := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1
	sourceCode := node.Content(content)

	return &ParsedFunction{
		Name:           name,
		StartLine:      startLine,
		EndLine:        endLine,
		Params:         params,
		ParamTypes:     paramTypes,
		ReturnType:     returnType,
		TypeParameters: typeParams,
		IsExported:     false,
		IsAsync:        isAsync,
		SourceCode:     sourceCode,
	}
}

func (p *JavaScriptParser) extractVariableObjectFunctions(node *sitter.Node, content []byte, lines []string) []ParsedFunction {
	var funcs []ParsedFunction
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() != "variable_declarator" {
			continue
		}
		name, value := extractDeclaratorNameAndValue(child, content)
		if name != "" && value != nil && value.Type() == "object" {
			funcs = append(funcs, p.extractObjectLiteralFunctions(name, value, content, lines)...)
		}
	}
	return funcs
}

func (p *JavaScriptParser) extractObjectLiteralFunctions(objectName string, objectNode *sitter.Node, content []byte, lines []string) []ParsedFunction {
	var funcs []ParsedFunction

	for i := 0; i < int(objectNode.ChildCount()); i++ {
		child := objectNode.Child(i)
		switch child.Type() {
		case "pair", "property":
			propName := ""
			var fnNode *sitter.Node
			var nestedObject *sitter.Node
			for j := 0; j < int(child.ChildCount()); j++ {
				c := child.Child(j)
				switch c.Type() {
				case "property_identifier", "identifier":
					if propName == "" {
						propName = c.Content(content)
					}
				case "string":
					if propName == "" {
						propName = strings.Trim(c.Content(content), "\"'")
					}
				case "arrow_function", "function", "method_definition":
					fnNode = c
				case "object":
					nestedObject = c
				}
			}
			if propName != "" && fnNode != nil {
				funcs = append(funcs, p.buildObjectLiteralFunction(objectName, propName, fnNode, content, lines))
			}
			if propName != "" && nestedObject != nil {
				nestedName := propName
				if objectName != "" {
					nestedName = objectName + "." + propName
				}
				funcs = append(funcs, p.extractObjectLiteralFunctions(nestedName, nestedObject, content, lines)...)
			}
		case "method_definition":
			propName := ""
			for j := 0; j < int(child.ChildCount()); j++ {
				c := child.Child(j)
				if c.Type() == "property_identifier" {
					propName = c.Content(content)
					break
				}
			}
			if propName != "" {
				funcs = append(funcs, p.buildObjectLiteralFunction(objectName, propName, child, content, lines))
			}
		}
	}

	return funcs
}

func (p *JavaScriptParser) extractExportedObjectFunctions(root *sitter.Node, content []byte, lines []string) []ParsedFunction {
	if root == nil {
		return nil
	}

	var funcs []ParsedFunction
	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil {
			return
		}
		switch node.Type() {
		case "export_statement", "export_default_declaration":
			for i := 0; i < int(node.ChildCount()); i++ {
				child := node.Child(i)
				if child == nil || child.Type() != "object" {
					continue
				}
				objectName := p.extractObjectLiteralDeclaredName(child, content)
				funcs = append(funcs, p.extractObjectLiteralFunctions(objectName, child, content, lines)...)
			}
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			walk(node.Child(i))
		}
	}
	walk(root)
	return funcs
}

func (p *JavaScriptParser) extractExportedObjectAliasCalls(root *sitter.Node, content []byte) map[string][]ParsedFunctionCall {
	if root == nil {
		return nil
	}
	out := make(map[string][]ParsedFunctionCall)
	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil {
			return
		}
		switch node.Type() {
		case "export_statement", "export_default_declaration":
			for i := 0; i < int(node.ChildCount()); i++ {
				child := node.Child(i)
				if child == nil || child.Type() != "object" {
					continue
				}
				for alias, target := range exportedObjectIdentifierAliases(child, content) {
					out[alias] = append(out[alias], ParsedFunctionCall{
						CalleeName: target,
						LineNumber: int(child.StartPoint().Row) + 1,
					})
				}
			}
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			walk(node.Child(i))
		}
	}
	walk(root)
	if len(out) == 0 {
		return nil
	}
	return out
}

func exportedObjectIdentifierAliases(objectNode *sitter.Node, content []byte) map[string]string {
	if objectNode == nil {
		return nil
	}
	out := make(map[string]string)
	for i := 0; i < int(objectNode.ChildCount()); i++ {
		child := objectNode.Child(i)
		if child == nil || (child.Type() != "pair" && child.Type() != "property") {
			continue
		}
		var identifiers []string
		for j := 0; j < int(child.ChildCount()); j++ {
			part := child.Child(j)
			switch part.Type() {
			case "property_identifier", "identifier":
				identifiers = append(identifiers, strings.TrimSpace(part.Content(content)))
			}
		}
		if len(identifiers) < 2 {
			continue
		}
		alias := identifiers[0]
		target := identifiers[len(identifiers)-1]
		if alias == "" || target == "" || alias == target {
			continue
		}
		out[alias] = target
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (p *JavaScriptParser) extractObjectLiteralDeclaredName(objectNode *sitter.Node, content []byte) string {
	if objectNode == nil {
		return ""
	}
	for i := 0; i < int(objectNode.ChildCount()); i++ {
		child := objectNode.Child(i)
		if child == nil || (child.Type() != "pair" && child.Type() != "property") {
			continue
		}
		var keyName string
		var value string
		for j := 0; j < int(child.ChildCount()); j++ {
			part := child.Child(j)
			switch part.Type() {
			case "property_identifier", "identifier":
				if keyName == "" {
					keyName = part.Content(content)
				}
			case "string":
				trimmed := strings.Trim(part.Content(content), "\"'`")
				if keyName == "" {
					keyName = trimmed
				} else if value == "" {
					value = trimmed
				}
			}
		}
		if strings.EqualFold(strings.TrimSpace(keyName), "name") && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (p *JavaScriptParser) buildObjectLiteralFunction(objectName, propName string, fnNode *sitter.Node, content []byte, lines []string) ParsedFunction {
	var params []string
	var paramTypes []string
	var returnType string
	var typeParams []ParsedTypeParameter
	isAsync := false

	for i := 0; i < int(fnNode.ChildCount()); i++ {
		child := fnNode.Child(i)
		switch child.Type() {
		case "async":
			isAsync = true
		case "formal_parameters":
			params, paramTypes = p.extractParameterNamesAndTypes(child, content)
		case "identifier":
			body := fnNode.ChildByFieldName("body")
			if fnNode.Type() == "arrow_function" && len(params) == 0 && body != nil && child.EndByte() <= body.StartByte() {
				params = append(params, child.Content(content))
				paramTypes = append(paramTypes, "")
			}
		case "type_annotation":
			returnType = p.extractTypeAnnotation(child, content)
		case "type_parameters":
			typeParams = p.extractTypeParameters(child, content)
		}
	}

	startLine := int(fnNode.StartPoint().Row) + 1
	endLine := int(fnNode.EndPoint().Row) + 1
	sourceCode := getSourceCode(lines, startLine, endLine)

	name := propName
	if objectName != "" {
		name = objectName + "." + propName
	}
	return ParsedFunction{
		Name:           name,
		StartLine:      startLine,
		EndLine:        endLine,
		Params:         params,
		ParamTypes:     paramTypes,
		ReturnType:     returnType,
		TypeParameters: typeParams,
		IsExported:     false,
		IsAsync:        isAsync,
		SourceCode:     sourceCode,
	}
}

func (p *JavaScriptParser) extractClass(node *sitter.Node, content []byte, lines []string) *ParsedClass {
	var name string
	var extendsClass string
	var implements []string
	var methods []ParsedFunction
	var fields []ParsedField
	var typeParams []ParsedTypeParameter
	var annotations []ParsedAnnotation

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		if (childType == "identifier" || childType == "type_identifier") && name == "" {
			name = child.Content(content)
		}

		// Extract decorators on the class itself
		if childType == "decorator" {
			if dec := p.extractDecorator(child, content); dec != nil {
				annotations = append(annotations, *dec)
			}
		}

		// Extract generic type parameters
		if childType == "type_parameters" {
			typeParams = p.extractTypeParameters(child, content)
		}

		if childType == "class_heritage" {
			for j := 0; j < int(child.ChildCount()); j++ {
				c := child.Child(j)
				switch c.Type() {
				case "extends_clause":
					for k := 0; k < int(c.ChildCount()); k++ {
						if c.Child(k).Type() == "identifier" || c.Child(k).Type() == "type_identifier" {
							extendsClass = c.Child(k).Content(content)
							break
						}
					}
				case "implements_clause":
					// Extract implemented interfaces
					for k := 0; k < int(c.ChildCount()); k++ {
						cc := c.Child(k)
						if cc.Type() == "type_identifier" || cc.Type() == "generic_type" {
							implements = append(implements, cc.Content(content))
						}
					}
				}
			}
		}

		if childType == "class_body" {
			// Track pending decorators for the next method/field
			var pendingDecorators []ParsedAnnotation

			for j := 0; j < int(child.ChildCount()); j++ {
				member := child.Child(j)
				memberType := member.Type()

				switch memberType {
				case "decorator":
					// Collect decorators for the next method/field
					if dec := p.extractDecorator(member, content); dec != nil {
						pendingDecorators = append(pendingDecorators, *dec)
					}
				case "method_definition":
					method := p.extractMethod(member, content, lines)
					if method != nil {
						// Attach any pending decorators
						if len(pendingDecorators) > 0 {
							method.Annotations = pendingDecorators
							pendingDecorators = nil
						}
						methods = append(methods, *method)
					}
				case "public_field_definition", "field_definition":
					// Class field with optional type annotation
					decorators := append([]ParsedAnnotation(nil), pendingDecorators...)
					field := p.extractClassField(member, content)
					if field != nil {
						// Attach any pending decorators
						if len(decorators) > 0 {
							field.Annotations = decorators
						}
						fields = append(fields, *field)
					}
					if fieldMethod := p.extractClassFieldMethod(member, content, lines); fieldMethod != nil {
						if len(fieldMethod.Annotations) == 0 && field != nil && len(field.Annotations) > 0 {
							fieldMethod.Annotations = append([]ParsedAnnotation(nil), field.Annotations...)
						}
						if len(decorators) > 0 {
							fieldMethod.Annotations = append([]ParsedAnnotation(nil), decorators...)
						}
						methods = append(methods, *fieldMethod)
					}
					pendingDecorators = nil
				default:
					// Clear pending decorators if we hit something unexpected
					pendingDecorators = nil
				}
			}
		}
	}

	if name == "" {
		name = p.findClassNameFromParent(node, content)
	}
	if name == "" {
		return nil
	}

	startLine := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1

	return &ParsedClass{
		Name:           name,
		StartLine:      startLine,
		EndLine:        endLine,
		ExtendsClass:   extendsClass,
		Implements:     implements,
		IsExported:     false,
		Methods:        methods,
		Fields:         fields,
		TypeParameters: typeParams,
		Annotations:    annotations,
	}
}

// extractClassField extracts a field from a class body
func (p *JavaScriptParser) extractClassField(node *sitter.Node, content []byte) *ParsedField {
	var name string
	var fieldType string
	var modifiers []string
	var annotations []ParsedAnnotation

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "property_identifier":
			name = child.Content(content)
		case "private_property_identifier":
			// #privateField - ES private field
			name = child.Content(content)
			modifiers = append(modifiers, "private")
		case "type_annotation":
			fieldType = p.extractTypeAnnotation(child, content)
		case "accessibility_modifier":
			// public, private, protected
			modifiers = append(modifiers, child.Content(content))
		case "readonly":
			modifiers = append(modifiers, "readonly")
		case "static":
			modifiers = append(modifiers, "static")
		case "override":
			modifiers = append(modifiers, "override")
		case "decorator":
			// Decorator on this field: @Inject() private service
			if dec := p.extractDecorator(child, content); dec != nil {
				annotations = append(annotations, *dec)
			}
		}
	}

	if name == "" {
		return nil
	}

	return &ParsedField{
		Name:        name,
		FieldType:   fieldType,
		Modifiers:   modifiers,
		StartLine:   int(node.StartPoint().Row) + 1,
		Annotations: annotations,
	}
}

func (p *JavaScriptParser) extractClassFieldMethod(node *sitter.Node, content []byte, lines []string) *ParsedFunction {
	if node == nil {
		return nil
	}

	var name string
	var fnNode *sitter.Node
	var returnType string
	isAsync := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "property_identifier", "private_property_identifier":
			if name == "" {
				name = child.Content(content)
			}
		case "arrow_function", "function", "function_expression":
			fnNode = child
		case "type_annotation":
			if returnType == "" {
				returnType = p.extractTypeAnnotation(child, content)
			}
		case "async":
			isAsync = true
		}
	}

	if name == "" || fnNode == nil {
		return nil
	}

	var params []string
	var paramTypes []string
	for i := 0; i < int(fnNode.ChildCount()); i++ {
		child := fnNode.Child(i)
		switch child.Type() {
		case "formal_parameters":
			params, paramTypes = p.extractParameterNamesAndTypes(child, content)
		case "type_annotation":
			returnType = p.extractTypeAnnotation(child, content)
		case "async":
			isAsync = true
		}
	}

	startLine := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1
	sourceCode := getSourceCode(lines, startLine, endLine)

	return &ParsedFunction{
		Name:       name,
		StartLine:  startLine,
		EndLine:    endLine,
		Params:     params,
		ParamTypes: paramTypes,
		ReturnType: returnType,
		IsExported: false,
		IsAsync:    isAsync,
		SourceCode: sourceCode,
	}
}

// extractDecorator extracts a TypeScript/JavaScript decorator
// Handles: @Component, @Component(), @Component({ selector: 'app' })
func (p *JavaScriptParser) extractDecorator(node *sitter.Node, content []byte) *ParsedAnnotation {
	var name string
	values := make(map[string]interface{})

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "identifier":
			// @Decorator
			name = child.Content(content)
		case "call_expression":
			// @Decorator() or @Decorator({...})
			for j := 0; j < int(child.ChildCount()); j++ {
				c := child.Child(j)
				cType := c.Type()

				switch cType {
				case "identifier":
					name = c.Content(content)
				case "arguments":
					// Extract arguments
					p.extractDecoratorArguments(c, content, values)
				}
			}
		}
	}

	if name == "" {
		return nil
	}

	return &ParsedAnnotation{
		Name:       name,
		Values:     values,
		LineNumber: int(node.StartPoint().Row) + 1,
	}
}

// extractDecoratorArguments extracts decorator argument values
func (p *JavaScriptParser) extractDecoratorArguments(node *sitter.Node, content []byte, values map[string]interface{}) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "string", "template_string":
			// @Route('/path')
			values["value"] = child.Content(content)
		case "number":
			values["value"] = child.Content(content)
		case "object":
			// @Component({ selector: 'app-root', template: '...' })
			p.extractObjectProperties(child, content, values)
		case "identifier":
			// @Inject(ServiceClass)
			values["value"] = child.Content(content)
		case "member_expression":
			// @Inject(Service.Type)
			values["value"] = child.Content(content)
		}
	}
}

// extractObjectProperties extracts key-value pairs from an object literal
func (p *JavaScriptParser) extractObjectProperties(node *sitter.Node, content []byte, values map[string]interface{}) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "pair" || child.Type() == "property" {
			var key, value string
			for j := 0; j < int(child.ChildCount()); j++ {
				c := child.Child(j)
				cType := c.Type()

				switch cType {
				case "property_identifier", "string":
					if key == "" {
						key = c.Content(content)
					}
				default:
					if key != "" && cType != ":" {
						value = c.Content(content)
					}
				}
			}
			if key != "" {
				values[key] = value
			}
		}
	}
}

// extractInterface extracts a TypeScript interface declaration
func (p *JavaScriptParser) extractInterface(node *sitter.Node, content []byte, lines []string) *ParsedInterface {
	var name string
	var extendsInterfaces []string
	var methods []ParsedFunction
	var fields []ParsedField
	var typeParams []ParsedTypeParameter
	isExported := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "type_identifier":
			if name == "" {
				name = child.Content(content)
			}
		case "extends_type_clause":
			// interface Foo extends Bar, Baz
			extendsInterfaces = p.extractExtendsClause(child, content)
		case "type_parameters":
			// interface Foo<T, K extends string>
			typeParams = p.extractTypeParameters(child, content)
		case "interface_body", "object_type":
			// Extract method and property signatures
			methods, fields = p.extractInterfaceBody(child, content, lines)
		}
	}

	if name == "" {
		return nil
	}

	startLine := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1

	return &ParsedInterface{
		Name:              name,
		StartLine:         startLine,
		EndLine:           endLine,
		ExtendsInterfaces: extendsInterfaces,
		Methods:           methods,
		Fields:            fields,
		IsExported:        isExported,
		TypeParameters:    typeParams,
	}
}

// extractExtendsClause extracts interface names from extends clause
func (p *JavaScriptParser) extractExtendsClause(node *sitter.Node, content []byte) []string {
	var interfaces []string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		if childType == "type_identifier" || childType == "generic_type" {
			interfaces = append(interfaces, child.Content(content))
		}
	}

	return interfaces
}

// extractTypeParameters extracts generic type parameters like <T, K extends Foo>
func (p *JavaScriptParser) extractTypeParameters(node *sitter.Node, content []byte) []ParsedTypeParameter {
	var params []ParsedTypeParameter
	index := 0

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "type_parameter" {
			param := p.extractTypeParameter(child, content, index)
			if param != nil {
				params = append(params, *param)
				index++
			}
		}
	}

	return params
}

// extractTypeParameter extracts a single type parameter
func (p *JavaScriptParser) extractTypeParameter(node *sitter.Node, content []byte, index int) *ParsedTypeParameter {
	var name string
	var bounds []string
	var boundType string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "type_identifier":
			if name == "" {
				name = child.Content(content)
			} else {
				// This is a bound
				bounds = append(bounds, child.Content(content))
			}
		case "constraint":
			// T extends Foo
			boundType = "extends"
			for j := 0; j < int(child.ChildCount()); j++ {
				c := child.Child(j)
				if c.Type() == "type_identifier" || c.Type() == "generic_type" {
					bounds = append(bounds, c.Content(content))
				}
			}
		case "default_type":
			// T = DefaultType (ignore for now, just note it exists)
		}
	}

	if name == "" {
		return nil
	}

	return &ParsedTypeParameter{
		Name:      name,
		Index:     index,
		Bounds:    bounds,
		BoundType: boundType,
	}
}

// extractInterfaceBody extracts methods and fields from interface body
func (p *JavaScriptParser) extractInterfaceBody(node *sitter.Node, content []byte, lines []string) ([]ParsedFunction, []ParsedField) {
	var methods []ParsedFunction
	var fields []ParsedField

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "method_signature", "function_signature":
			if method := p.extractMethodSignature(child, content, lines); method != nil {
				methods = append(methods, *method)
			}
		case "property_signature":
			if field := p.extractPropertySignature(child, content); field != nil {
				fields = append(fields, *field)
			}
		case "call_signature":
			// Function type: (arg: Type) => ReturnType
			// Treat as a callable method
			if method := p.extractCallSignature(child, content, lines); method != nil {
				methods = append(methods, *method)
			}
		case "index_signature":
			// [key: string]: ValueType - index signatures
			// Store as a special field
			if field := p.extractIndexSignature(child, content); field != nil {
				fields = append(fields, *field)
			}
		}
	}

	return methods, fields
}

// extractMethodSignature extracts a method signature from interface
func (p *JavaScriptParser) extractMethodSignature(node *sitter.Node, content []byte, lines []string) *ParsedFunction {
	var name string
	var params []string
	var returnType string
	isAsync := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "property_identifier":
			name = child.Content(content)
		case "formal_parameters", "call_signature":
			params = p.extractParameterNames(child, content)
		case "type_annotation":
			// Return type
			returnType = p.extractTypeAnnotation(child, content)
		case "async":
			isAsync = true
		}
	}

	if name == "" {
		return nil
	}

	startLine := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1

	return &ParsedFunction{
		Name:       name,
		StartLine:  startLine,
		EndLine:    endLine,
		Params:     params,
		ReturnType: returnType,
		IsAsync:    isAsync,
	}
}

// extractPropertySignature extracts a property from interface
func (p *JavaScriptParser) extractPropertySignature(node *sitter.Node, content []byte) *ParsedField {
	var name string
	var fieldType string
	var modifiers []string
	isOptional := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "property_identifier":
			name = child.Content(content)
		case "type_annotation":
			fieldType = p.extractTypeAnnotation(child, content)
		case "?":
			isOptional = true
		case "readonly":
			modifiers = append(modifiers, "readonly")
		}
	}

	if name == "" {
		return nil
	}

	if isOptional {
		modifiers = append(modifiers, "optional")
	}

	return &ParsedField{
		Name:      name,
		FieldType: fieldType,
		Modifiers: modifiers,
		StartLine: int(node.StartPoint().Row) + 1,
	}
}

// extractCallSignature extracts callable signature from interface
func (p *JavaScriptParser) extractCallSignature(node *sitter.Node, content []byte, lines []string) *ParsedFunction {
	var params []string
	var returnType string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "formal_parameters":
			params = p.extractParameterNames(child, content)
		case "type_annotation":
			returnType = p.extractTypeAnnotation(child, content)
		}
	}

	startLine := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1

	return &ParsedFunction{
		Name:       "(call)",
		StartLine:  startLine,
		EndLine:    endLine,
		Params:     params,
		ReturnType: returnType,
	}
}

// extractIndexSignature extracts index signature like [key: string]: Type
func (p *JavaScriptParser) extractIndexSignature(node *sitter.Node, content []byte) *ParsedField {
	var keyType string
	var valueType string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "index_type_query" || child.Type() == "type_annotation" {
			if keyType == "" {
				keyType = child.Content(content)
			} else {
				valueType = child.Content(content)
			}
		}
	}

	return &ParsedField{
		Name:      "[index]",
		FieldType: valueType,
		Modifiers: []string{"index"},
		StartLine: int(node.StartPoint().Row) + 1,
	}
}

// extractParameterNames extracts parameter names from formal_parameters
func (p *JavaScriptParser) extractParameterNames(node *sitter.Node, content []byte) []string {
	var params []string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "identifier":
			params = append(params, child.Content(content))
		case "required_parameter", "optional_parameter":
			// Get the identifier inside
			for j := 0; j < int(child.ChildCount()); j++ {
				c := child.Child(j)
				if c.Type() == "identifier" {
					params = append(params, c.Content(content))
					break
				}
			}
		case "rest_parameter":
			// ...args
			for j := 0; j < int(child.ChildCount()); j++ {
				c := child.Child(j)
				if c.Type() == "identifier" {
					params = append(params, "..."+c.Content(content))
					break
				}
			}
		}
	}

	return params
}

// extractParameterNamesAndTypes extracts parameter names and their types from formal_parameters
func (p *JavaScriptParser) extractParameterNamesAndTypes(node *sitter.Node, content []byte) ([]string, []string) {
	var names []string
	var types []string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "identifier":
			// Untyped parameter
			names = append(names, child.Content(content))
			types = append(types, "")
		case "required_parameter", "optional_parameter":
			// Typed parameter: name: Type or name?: Type
			var paramName, paramType string
			for j := 0; j < int(child.ChildCount()); j++ {
				c := child.Child(j)
				cType := c.Type()

				switch cType {
				case "identifier":
					if paramName == "" {
						paramName = c.Content(content)
					}
				case "type_annotation":
					paramType = p.extractTypeAnnotation(c, content)
				}
			}
			if paramName != "" {
				names = append(names, paramName)
				types = append(types, paramType)
			}
		case "rest_parameter":
			// ...args: Type[]
			var paramName, paramType string
			for j := 0; j < int(child.ChildCount()); j++ {
				c := child.Child(j)
				cType := c.Type()

				switch cType {
				case "identifier":
					paramName = "..." + c.Content(content)
				case "type_annotation":
					paramType = p.extractTypeAnnotation(c, content)
				}
			}
			if paramName != "" {
				names = append(names, paramName)
				types = append(types, paramType)
			}
		}
	}

	return names, types
}

// extractTypeAnnotation extracts type from type annotation (: Type)
func (p *JavaScriptParser) extractTypeAnnotation(node *sitter.Node, content []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		// Skip the colon
		if childType == ":" {
			continue
		}

		// Return the type content
		if childType != "" {
			return child.Content(content)
		}
	}

	return ""
}

// extractTypeAlias extracts a TypeScript type alias declaration
// Handles: type ID = string; type Callback<T> = (data: T) => void;
func (p *JavaScriptParser) extractTypeAlias(node *sitter.Node, content []byte) *ParsedTypeAlias {
	var name string
	var definition string
	var typeParams []string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "type_identifier":
			if name == "" {
				name = child.Content(content)
			}
		case "type_parameters":
			// Extract generic parameters like <T, K extends string>
			typeParams = p.extractTypeParamNames(child, content)
		default:
			// The type definition - could be union_type, intersection_type, function_type, etc.
			if name != "" && definition == "" && childType != "=" && childType != "type" {
				definition = child.Content(content)
			}
		}
	}

	if name == "" {
		return nil
	}

	return &ParsedTypeAlias{
		Name:       name,
		Definition: definition,
		TypeParams: typeParams,
		StartLine:  int(node.StartPoint().Row) + 1,
		EndLine:    int(node.EndPoint().Row) + 1,
		IsExported: false, // Set by markExports
	}
}

// extractTypeParamNames extracts just the type parameter names
func (p *JavaScriptParser) extractTypeParamNames(node *sitter.Node, content []byte) []string {
	var params []string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "type_parameter" {
			// Get the name from the type parameter
			for j := 0; j < int(child.ChildCount()); j++ {
				c := child.Child(j)
				if c.Type() == "type_identifier" {
					params = append(params, c.Content(content))
					break
				}
			}
		}
	}

	return params
}

// extractEnum extracts a TypeScript enum declaration
// Handles: enum Status { Active, Inactive } and const enum Direction { Up = 0 }
func (p *JavaScriptParser) extractEnum(node *sitter.Node, content []byte, lines []string) *ParsedClass {
	var name string
	var enumConstants []ParsedEnumConstant

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "identifier":
			if name == "" {
				name = child.Content(content)
			}
		case "enum_body":
			enumConstants = p.extractEnumBody(child, content)
		}
	}

	if name == "" {
		return nil
	}

	startLine := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1

	return &ParsedClass{
		Name:          name,
		StartLine:     startLine,
		EndLine:       endLine,
		IsEnum:        true,
		IsExported:    false, // Set by markExports
		EnumConstants: enumConstants,
		Methods:       []ParsedFunction{},
		Implements:    []string{},
	}
}

// extractEnumBody extracts enum constants from enum body
func (p *JavaScriptParser) extractEnumBody(node *sitter.Node, content []byte) []ParsedEnumConstant {
	var constants []ParsedEnumConstant
	ordinal := 0

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "enum_assignment" || child.Type() == "property_identifier" {
			constant := p.extractEnumConstant(child, content, ordinal)
			if constant != nil {
				constants = append(constants, *constant)
				ordinal++
			}
		}
	}

	return constants
}

// extractEnumConstant extracts a single enum constant
func (p *JavaScriptParser) extractEnumConstant(node *sitter.Node, content []byte, ordinal int) *ParsedEnumConstant {
	var name string
	var args []string

	nodeType := node.Type()

	if nodeType == "property_identifier" {
		// Simple enum member: Active
		name = node.Content(content)
	} else if nodeType == "enum_assignment" {
		// Enum member with value: GET = 'GET'
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child.Type() == "property_identifier" && name == "" {
				name = child.Content(content)
			} else if child.Type() != "=" && name != "" {
				// The value
				args = append(args, child.Content(content))
			}
		}
	}

	if name == "" {
		return nil
	}

	return &ParsedEnumConstant{
		Name:      name,
		Ordinal:   ordinal,
		Arguments: args,
	}
}

func (p *JavaScriptParser) extractMethod(node *sitter.Node, content []byte, lines []string) *ParsedFunction {
	var name string
	var params []string
	var paramTypes []string
	var returnType string
	isAsync := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "property_identifier":
			if name == "" {
				name = child.Content(content)
			}
		case "async":
			isAsync = true
		case "formal_parameters":
			params, paramTypes = p.extractParameterNamesAndTypes(child, content)
		case "type_annotation":
			// Return type: method(): Type
			returnType = p.extractTypeAnnotation(child, content)
		}
	}

	if name == "" {
		return nil
	}

	startLine := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1
	sourceCode := getSourceCode(lines, startLine, endLine)

	return &ParsedFunction{
		Name:       name,
		StartLine:  startLine,
		EndLine:    endLine,
		Params:     params,
		ParamTypes: paramTypes,
		ReturnType: returnType,
		IsExported: false,
		IsAsync:    isAsync,
		SourceCode: sourceCode,
	}
}

func (p *JavaScriptParser) extractFunctionCalls(node *sitter.Node, content []byte) []ParsedFunctionCall {
	var calls []ParsedFunctionCall

	var findCalls func(*sitter.Node)
	findCalls = func(n *sitter.Node) {
		if n.Type() == "call_expression" {
			if n.ChildCount() > 0 {
				funcNode := n.Child(0)
				var calleeName, receiver, methodName string

				if funcNode.Type() == "identifier" {
					// Simple function call: foo()
					calleeName = funcNode.Content(content)
					methodName = calleeName
				} else if funcNode.Type() == "member_expression" {
					// Method call: obj.method() or this.service.method()
					receiver, methodName = p.extractReceiverAndMethod(funcNode, content)
					calleeName = methodName
				}

				if calleeName != "" {
					calls = append(calls, ParsedFunctionCall{
						CalleeName: calleeName,
						Receiver:   receiver,
						MethodName: methodName,
						LineNumber: int(n.StartPoint().Row) + 1,
						IsAsync:    false,
					})
				}
			}
		}

		for i := 0; i < int(n.ChildCount()); i++ {
			findCalls(n.Child(i))
		}
	}

	findCalls(node)
	return calls
}

// extractSingleFunctionCall extracts a single function call from a call_expression node.
// methodName/receiver are precomputed to avoid repeated extraction.
func (p *JavaScriptParser) extractSingleFunctionCall(node *sitter.Node, content []byte, methodName, receiver string) *ParsedFunctionCall {
	if node.ChildCount() == 0 {
		return nil
	}

	funcNode := node.Child(0)
	var calleeName string

	if funcNode.Type() == "identifier" {
		// Simple function call: foo()
		calleeName = funcNode.Content(content)
		receiver = ""
		if methodName == "" {
			methodName = calleeName
		}
	} else if funcNode.Type() == "member_expression" {
		// Method call: obj.method() or this.service.method()
		if methodName == "" || receiver == "" {
			receiver, methodName = p.extractReceiverAndMethod(funcNode, content)
		}
		calleeName = methodName
	} else {
		return nil
	}

	if calleeName == "" {
		return nil
	}

	return &ParsedFunctionCall{
		CalleeName: calleeName,
		Receiver:   receiver,
		MethodName: methodName,
		LineNumber: int(node.StartPoint().Row) + 1,
		IsAsync:    false,
	}
}

func (p *JavaScriptParser) normalizeCallName(call *ParsedFunctionCall, funcName string, node *sitter.Node, content []byte) {
	if call == nil || call.MethodName == "" {
		return
	}

	isSuper := call.Receiver == "super" || (call.Receiver == "" && strings.HasPrefix(call.CalleeName, "super."))
	isThis := call.Receiver == "this"

	if isSuper || isThis {
		className := p.findEnclosingClassName(node, content)
		if idx := strings.LastIndex(funcName, "."); className == "" && idx > 0 {
			className = funcName[:idx]
		}
		if className != "" {
			call.CalleeName = className + "." + call.MethodName
			call.Receiver = ""
			return
		}
	}

	if call.Receiver != "" {
		receiver := call.Receiver
		if strings.HasPrefix(receiver, "this.") {
			receiver = strings.TrimPrefix(receiver, "this.")
		} else if strings.HasPrefix(receiver, "super.") {
			receiver = strings.TrimPrefix(receiver, "super.")
		}
		if receiverLooksLikeCallExpression(receiver) {
			call.Receiver = receiver
			call.CalleeName = call.MethodName
			return
		}
		if receiverIsKnownJSRuntimeValue(receiver) {
			call.Receiver = receiver
			call.CalleeName = call.MethodName
			return
		}
		if !strings.Contains(receiver, ".") {
			if className := p.findLocalInstanceClassName(node, receiver, content); className != "" {
				call.CalleeName = className + "." + call.MethodName
				call.Receiver = ""
				return
			}
		}
		call.Receiver = receiver
		call.CalleeName = receiver + "." + call.MethodName
	}
}

func receiverLooksLikeCallExpression(receiver string) bool {
	return strings.Contains(receiver, "(") || strings.Contains(receiver, ")")
}

func receiverIsKnownJSRuntimeValue(receiver string) bool {
	switch strings.TrimSpace(receiver) {
	case "__dirname", "__filename", "console", "JSON", "Object", "Array", "String", "Number", "Boolean", "Math",
		"Date", "Promise", "process", "Buffer", "global", "window", "document":
		return true
	default:
		return false
	}
}

func (p *JavaScriptParser) findLocalInstanceClassName(node *sitter.Node, receiver string, content []byte) string {
	receiver = strings.TrimSpace(receiver)
	if node == nil || receiver == "" {
		return ""
	}
	scope := p.findEnclosingCallableNode(node)
	if scope == nil {
		return ""
	}

	current := node
	for current != nil && current != scope {
		parent := current.Parent()
		if parent == nil {
			break
		}
		if className := p.findMatchingInstanceInPriorSiblings(parent, current, receiver, content); className != "" {
			return className
		}
		current = parent
	}

	if scope != nil {
		if className := p.findMatchingInstanceInPriorSiblings(scope, current, receiver, content); className != "" {
			return className
		}
	}
	return ""
}

func (p *JavaScriptParser) findMatchingInstanceInPriorSiblings(parent, current *sitter.Node, receiver string, content []byte) string {
	if parent == nil {
		return ""
	}
	currentIndex := -1
	for i := 0; i < int(parent.ChildCount()); i++ {
		if parent.Child(i) == current {
			currentIndex = i
			break
		}
	}
	if currentIndex < 0 {
		return ""
	}
	for i := currentIndex - 1; i >= 0; i-- {
		if className := p.findMatchingInstanceInDeclarationNode(parent.Child(i), receiver, content); className != "" {
			return className
		}
	}
	return ""
}

func (p *JavaScriptParser) findMatchingInstanceInDeclarationNode(node *sitter.Node, receiver string, content []byte) string {
	if node == nil {
		return ""
	}
	switch node.Type() {
	case "lexical_declaration", "variable_declaration", "variable_declarator", "expression_statement":
	default:
		return ""
	}

	var className string
	var walk func(*sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil || className != "" {
			return
		}
		if p.isCallableScopeBoundary(n) && n != node {
			return
		}
		if n.Type() == "variable_declarator" {
			var ident string
			var value *sitter.Node
			for i := 0; i < int(n.ChildCount()); i++ {
				child := n.Child(i)
				switch child.Type() {
				case "identifier":
					if ident == "" {
						ident = child.Content(content)
					}
				case "new_expression":
					value = child
				}
			}
			if ident == receiver && value != nil {
				className = p.extractConstructorNameFromNewExpression(value, content)
				return
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
			if className != "" {
				return
			}
		}
	}

	walk(node)
	return className
}

func (p *JavaScriptParser) isCallableScopeBoundary(node *sitter.Node) bool {
	if node == nil {
		return false
	}
	switch node.Type() {
	case "function_declaration", "function", "function_expression", "arrow_function", "method_definition":
		return true
	case "public_field_definition", "field_definition":
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			switch child.Type() {
			case "arrow_function", "function", "function_expression":
				return true
			}
		}
	}
	return false
}

func (p *JavaScriptParser) findEnclosingCallableNode(node *sitter.Node) *sitter.Node {
	current := node
	for current != nil {
		switch current.Type() {
		case "function_declaration", "function", "function_expression", "arrow_function", "method_definition":
			return current
		case "public_field_definition", "field_definition":
			for i := 0; i < int(current.ChildCount()); i++ {
				child := current.Child(i)
				switch child.Type() {
				case "arrow_function", "function", "function_expression":
					return current
				}
			}
		}
		current = current.Parent()
	}
	return nil
}

func (p *JavaScriptParser) extractConstructorNameFromNewExpression(node *sitter.Node, content []byte) string {
	if node == nil || node.Type() != "new_expression" {
		return ""
	}
	ctor := node.ChildByFieldName("constructor")
	if ctor == nil && node.ChildCount() > 0 {
		ctor = node.Child(0)
	}
	if ctor == nil {
		return ""
	}
	switch ctor.Type() {
	case "identifier", "type_identifier":
		return ctor.Content(content)
	case "member_expression", "optional_member_expression":
		if prop := extractMemberPropertyName(ctor, content); prop != "" {
			return prop
		}
	}
	return ""
}

// extractReceiverAndMethod extracts receiver and method from member_expression
// For "this.service.get", returns receiver="this.service", method="get"
func (p *JavaScriptParser) extractReceiverAndMethod(node *sitter.Node, content []byte) (string, string) {
	var receiver, method string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "property_identifier":
			// The last property_identifier is the method name
			method = child.Content(content)
		case "identifier":
			// Single identifier as object: obj.method
			if receiver == "" {
				receiver = child.Content(content)
			}
		case "member_expression":
			// Nested: this.service.method - the inner member_expression is the receiver
			receiver = child.Content(content)
		case "call_expression":
			// Chained call as receiver: $(...).click
			receiver = child.Content(content)
		case "this":
			if receiver == "" {
				receiver = "this"
			}
		case "super":
			if receiver == "" {
				receiver = "super"
			}
		}
	}

	return receiver, method
}

// extractSubscriptReceiverAndMethod extracts receiver and method from subscript_expression.
// For "client['get']", returns receiver="client", method="get".
// For "client[method]", returns receiver="client", method="method", dynamic=true.
func (p *JavaScriptParser) extractSubscriptReceiverAndMethod(node *sitter.Node, content []byte) (string, string, bool) {
	var receiver, method string
	dynamic := false

	objectNode := node.ChildByFieldName("object")
	indexNode := node.ChildByFieldName("index")

	if objectNode == nil || indexNode == nil {
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			switch child.Type() {
			case "identifier", "member_expression", "this", "super":
				if receiver == "" {
					receiver = child.Content(content)
				}
			case "string":
				method = strings.Trim(child.Content(content), "\"'`")
			}
		}
		return receiver, method, method == ""
	}

	receiver = strings.TrimSpace(objectNode.Content(content))
	switch indexNode.Type() {
	case "string":
		method = strings.Trim(indexNode.Content(content), "\"'`")
	case "template_string":
		method = strings.Trim(indexNode.Content(content), "`")
		dynamic = true
	case "identifier":
		method = indexNode.Content(content)
		dynamic = true
	case "member_expression":
		if prop := extractMemberPropertyName(indexNode, content); prop != "" {
			method = prop
		}
		dynamic = true
	default:
		dynamic = true
	}

	return receiver, method, dynamic
}

func (p *JavaScriptParser) extractEndpoint(node *sitter.Node, content []byte) *ParsedEndpoint {
	if node.ChildCount() == 0 || p.routerIdx == nil {
		return nil
	}

	funcNode := node.Child(0)
	if funcNode.Type() != "member_expression" {
		return nil
	}

	// The method name is the cheapest test, so it runs before any receiver lookup.
	property := funcNode.ChildByFieldName("property")
	object := funcNode.ChildByFieldName("object")
	if property == nil || object == nil {
		return nil
	}
	method := routerRegistrationMethods[strings.ToLower(property.Content(content))]
	if method == "" {
		return nil
	}

	argsNode := node.ChildByFieldName("arguments")
	if argsNode == nil {
		return nil
	}
	var args []*sitter.Node
	for i := 0; i < int(argsNode.NamedChildCount()); i++ {
		if arg := argsNode.NamedChild(i); arg.Type() != "comment" {
			args = append(args, arg)
		}
	}

	var path string
	handlerArgs := args
	switch kind := p.callReceiverKind(node); kind {
	case rkRouter:
		if len(args) > 0 {
			path = routerPathText(args[0], content)
			handlerArgs = args[1:]
		}
		if len(handlerArgs) == 0 && !strings.HasPrefix(path, "/") {
			return nil // app.get('env') reads a setting
		}
	case rkRoute:
		path = routeChainPath(object, content)
		if len(args) == 0 {
			return nil
		}
	case "":
		// Unknown provenance: accept only a call shaped like a registration.
		if !p.routeCallLooksRegistered(node, object, args, p.routerIdx.frames[node.ID()], content) {
			return nil
		}
		path = routerPathText(args[0], content)
		handlerArgs = args[1:]
	default:
		return nil
	}
	if path == "" {
		return nil
	}

	// The final handler is the endpoint; preceding ones are middleware, and
	// four-parameter error middleware only when nothing else is left.
	var handlerName, errorHandlerName string
	for _, arg := range handlerArgs {
		name := p.routeHandlerName(arg, content, 0)
		switch {
		case name == "":
		case routerIsErrorMiddleware(arg):
			errorHandlerName = name
		default:
			handlerName = name
		}
	}
	if handlerName == "" {
		handlerName = errorHandlerName
	}

	return &ParsedEndpoint{
		Path:        path,
		Method:      method,
		HandlerName: handlerName,
		LineNumber:  int(node.StartPoint().Row) + 1,
	}
}

// Built-in composition APIs across the JS frontend frameworks we index.
var builtInHookOrigins = map[string]string{
	"useState":             "react",
	"useEffect":            "react",
	"useContext":           "react",
	"useReducer":           "react",
	"useCallback":          "react",
	"useMemo":              "react",
	"useRef":               "react",
	"useImperativeHandle":  "react",
	"useLayoutEffect":      "react",
	"useDebugValue":        "react",
	"useDeferredValue":     "react",
	"useTransition":        "react",
	"useId":                "react",
	"useSyncExternalStore": "react",
	"useInsertionEffect":   "react",
	"ref":                  "vue_reactivity",
	"shallowRef":           "vue_reactivity",
	"reactive":             "vue_reactivity",
	"shallowReactive":      "vue_reactivity",
	"computed":             "vue_reactivity",
	"watch":                "vue_reactivity",
	"watchEffect":          "vue_reactivity",
	"watchPostEffect":      "vue_reactivity",
	"watchSyncEffect":      "vue_reactivity",
	"provide":              "vue_reactivity",
	"inject":               "vue_reactivity",
	"onMounted":            "vue_lifecycle",
	"onBeforeMount":        "vue_lifecycle",
	"onUpdated":            "vue_lifecycle",
	"onBeforeUpdate":       "vue_lifecycle",
	"onUnmounted":          "vue_lifecycle",
	"onBeforeUnmount":      "vue_lifecycle",
	"onActivated":          "vue_lifecycle",
	"onDeactivated":        "vue_lifecycle",
	"onErrorCaptured":      "vue_lifecycle",
	"onRenderTracked":      "vue_lifecycle",
	"onRenderTriggered":    "vue_lifecycle",
	"defineProps":          "vue_macro",
	"defineEmits":          "vue_macro",
	"defineExpose":         "vue_macro",
	"defineModel":          "vue_macro",
	"defineOptions":        "vue_macro",
	"defineSlots":          "vue_macro",
	"defineStore":          "pinia",
	"useRouter":            "vue_router",
	"useRoute":             "vue_router",
	"useVuelidate":         "vuelidate",
}

func hookOrigin(name string) string {
	name = strings.TrimSpace(name)
	if origin, ok := builtInHookOrigins[name]; ok {
		return origin
	}
	if strings.HasPrefix(name, "use") && strings.HasSuffix(name, "Store") {
		return "pinia"
	}
	return "custom"
}

// extractHookCall extracts React hook calls
func (p *JavaScriptParser) extractHookCall(node *sitter.Node, content []byte) *ParsedHookCall {
	if node.ChildCount() == 0 {
		return nil
	}

	funcNode := node.Child(0)
	var hookName string

	// Get the function name
	if funcNode.Type() == "identifier" {
		hookName = funcNode.Content(content)
	}

	if !isCompositionHookName(hookName) {
		return nil
	}

	origin := hookOrigin(hookName)
	isCustomHook := origin == "custom"

	// Get arguments
	var args *sitter.Node
	for i := 0; i < int(node.ChildCount()); i++ {
		if node.Child(i).Type() == "arguments" {
			args = node.Child(i)
			break
		}
	}

	var initialValue string
	var dependencies []string

	if args != nil {
		// Extract initial value (first arg) and dependencies (second arg for useEffect, etc.)
		argIndex := 0
		for i := 0; i < int(args.ChildCount()); i++ {
			arg := args.Child(i)
			if arg.Type() == "," || arg.Type() == "(" || arg.Type() == ")" {
				continue
			}

			if argIndex == 0 {
				// First argument
				if hookCapturesInitialValue(hookName) {
					initialValue = arg.Content(content)
				}
			} else if argIndex == 1 {
				// Second argument (dependency array for useEffect, useMemo, useCallback)
				if arg.Type() == "array" {
					dependencies = p.extractArrayElements(arg, content)
				}
			}
			argIndex++
		}
	}

	// Get containing function
	funcName := p.findContainingFunction(node, content)
	if funcName == "" {
		funcName = "_module_"
	}

	return &ParsedHookCall{
		HookName:     hookName,
		Dependencies: dependencies,
		InitialValue: initialValue,
		FunctionName: funcName,
		LineNumber:   int(node.StartPoint().Row) + 1,
		IsCustomHook: isCustomHook,
		Origin:       origin,
	}
}

func (p *JavaScriptParser) extractVueComponentContracts(node *sitter.Node, content []byte) []ParsedVueComponentContract {
	if node == nil || node.Type() != "call_expression" {
		return nil
	}
	macroName := extractCallCalleeName(node, content)
	kind := vueContractKind(macroName)
	if kind == "" {
		return nil
	}

	text := strings.TrimSpace(node.Content(content))
	definition, generic := extractVueMacroDefinition(macroName, text)
	line := int(node.StartPoint().Row) + 1
	switch kind {
	case "props":
		return parseVueContractFields(kind, definition, generic, line)
	case "emits":
		return parseVueEmitsContract(definition, generic, line)
	case "expose":
		return parseVueExposeContract(definition, line)
	case "model":
		return parseVueModelContract(definition, generic, line)
	default:
		return nil
	}
}

func vueContractKind(name string) string {
	switch strings.TrimSpace(name) {
	case "defineProps":
		return "props"
	case "defineEmits":
		return "emits"
	case "defineExpose":
		return "expose"
	case "defineModel":
		return "model"
	default:
		return ""
	}
}

func extractVueMacroDefinition(macroName, text string) (string, bool) {
	if macroName == "" {
		return "", false
	}
	idx := strings.Index(text, macroName)
	if idx < 0 {
		return "", false
	}
	rest := strings.TrimSpace(text[idx+len(macroName):])
	if strings.HasPrefix(rest, "<") {
		if typeText, end := extractBalancedDelimited(rest, '<', '>'); end > 0 {
			return strings.TrimSpace(typeText), true
		}
	}
	open := strings.Index(rest, "(")
	if open < 0 {
		return "", false
	}
	if args, end := extractBalancedDelimited(rest[open:], '(', ')'); end > 0 {
		return strings.TrimSpace(args), false
	}
	return "", false
}

func parseVueContractFields(kind, definition string, generic bool, line int) []ParsedVueComponentContract {
	definition = strings.TrimSpace(definition)
	if definition == "" {
		return nil
	}
	if generic {
		fields := parseVueTypeLiteralFields(kind, definition, line)
		if len(fields) > 0 {
			return fields
		}
		return []ParsedVueComponentContract{{
			Kind:       kind,
			FieldName:  "$" + kind,
			FieldType:  definition,
			IsRequired: true,
			LineNumber: line,
			Definition: definition,
		}}
	}
	if strings.HasPrefix(definition, "[") {
		return parseVueArrayContract(kind, definition, line)
	}
	if strings.HasPrefix(definition, "{") {
		return parseVueObjectContract(kind, definition, line)
	}
	return nil
}

func parseVueTypeLiteralFields(kind, definition string, line int) []ParsedVueComponentContract {
	body := strings.TrimSpace(definition)
	if strings.HasPrefix(body, "{") && strings.HasSuffix(body, "}") {
		body = strings.TrimSpace(body[1 : len(body)-1])
	}
	var out []ParsedVueComponentContract
	for _, entry := range splitTopLevelVueEntries(body, ";,\n") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		match := vueContractKeyPattern.FindStringSubmatch(entry)
		if len(match) != 3 {
			continue
		}
		name := strings.TrimSpace(match[1])
		required := !strings.Contains(entry[:strings.Index(entry, ":")], "?")
		out = append(out, ParsedVueComponentContract{
			Kind:       kind,
			FieldName:  name,
			FieldType:  strings.TrimSpace(match[2]),
			IsRequired: required,
			LineNumber: line,
			Definition: definition,
		})
	}
	return out
}

func parseVueArrayContract(kind, definition string, line int) []ParsedVueComponentContract {
	body := strings.TrimSpace(definition)
	if strings.HasPrefix(body, "[") && strings.HasSuffix(body, "]") {
		body = strings.TrimSpace(body[1 : len(body)-1])
	}
	var out []ParsedVueComponentContract
	for _, entry := range splitTopLevelVueEntries(body, ",") {
		name := strings.TrimSpace(stripStringQuotes(entry))
		if name == "" {
			continue
		}
		out = append(out, ParsedVueComponentContract{
			Kind:       kind,
			FieldName:  name,
			IsRequired: true,
			LineNumber: line,
			Definition: definition,
		})
	}
	return out
}

func parseVueObjectContract(kind, definition string, line int) []ParsedVueComponentContract {
	body := strings.TrimSpace(definition)
	if strings.HasPrefix(body, "{") && strings.HasSuffix(body, "}") {
		body = strings.TrimSpace(body[1 : len(body)-1])
	}
	var out []ParsedVueComponentContract
	for _, entry := range splitTopLevelVueEntries(body, ",;\n") {
		name, value, ok := splitVueObjectEntry(entry)
		if !ok || name == "" {
			continue
		}
		fieldType := ""
		if match := vueRuntimeTypePattern.FindStringSubmatch(value); len(match) == 2 {
			fieldType = match[1]
		} else {
			fieldType = leadingIdentifier(value)
		}
		out = append(out, ParsedVueComponentContract{
			Kind:       kind,
			FieldName:  name,
			FieldType:  fieldType,
			IsRequired: strings.Contains(value, "required: true"),
			LineNumber: line,
			Definition: definition,
		})
	}
	return out
}

func parseVueEmitsContract(definition string, generic bool, line int) []ParsedVueComponentContract {
	definition = strings.TrimSpace(definition)
	if definition == "" {
		return nil
	}
	if generic {
		out := parseVueGenericEmitEvents(definition, line)
		if len(out) > 0 {
			return dedupeVueContracts(out)
		}
		return []ParsedVueComponentContract{{
			Kind:       "emits",
			FieldName:  "$emits",
			FieldType:  definition,
			IsRequired: true,
			LineNumber: line,
			Definition: definition,
		}}
	}
	if strings.HasPrefix(definition, "[") {
		return parseVueArrayContract("emits", definition, line)
	}
	if strings.HasPrefix(definition, "{") {
		return parseVueObjectContract("emits", definition, line)
	}
	return nil
}

func parseVueGenericEmitEvents(definition string, line int) []ParsedVueComponentContract {
	body := strings.TrimSpace(definition)
	if strings.HasPrefix(body, "{") && strings.HasSuffix(body, "}") {
		body = strings.TrimSpace(body[1 : len(body)-1])
	}
	var out []ParsedVueComponentContract
	for _, entry := range splitTopLevelVueEntries(body, ";\n") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if match := vueEmitParamPattern.FindStringSubmatch(entry); len(match) == 2 && match[1] != "" {
			out = append(out, ParsedVueComponentContract{
				Kind:       "emits",
				FieldName:  match[1],
				IsRequired: true,
				LineNumber: line,
				Definition: definition,
			})
			continue
		}
		if match := vueContractKeyPattern.FindStringSubmatch(entry); len(match) == 3 {
			out = append(out, ParsedVueComponentContract{
				Kind:       "emits",
				FieldName:  strings.TrimSpace(match[1]),
				FieldType:  strings.TrimSpace(match[2]),
				IsRequired: true,
				LineNumber: line,
				Definition: definition,
			})
		}
	}
	return out
}

func parseVueExposeContract(definition string, line int) []ParsedVueComponentContract {
	definition = strings.TrimSpace(definition)
	if definition == "" {
		return nil
	}
	if strings.HasPrefix(definition, "{") {
		return parseVueObjectContract("expose", definition, line)
	}
	if strings.HasPrefix(definition, "[") {
		return parseVueArrayContract("expose", definition, line)
	}
	return nil
}

func parseVueModelContract(definition string, generic bool, line int) []ParsedVueComponentContract {
	fieldName := "modelValue"
	fieldType := ""
	if generic {
		fieldType = strings.TrimSpace(definition)
	} else {
		parts := splitTopLevelVueEntries(definition, ",")
		if len(parts) > 0 {
			first := strings.TrimSpace(parts[0])
			if strings.HasPrefix(first, "'") || strings.HasPrefix(first, "\"") {
				fieldName = stripStringQuotes(first)
			}
		}
		for _, part := range parts {
			if match := vueRuntimeTypePattern.FindStringSubmatch(part); len(match) == 2 {
				fieldType = match[1]
				break
			}
		}
	}
	if fieldName == "" {
		fieldName = "modelValue"
	}
	return []ParsedVueComponentContract{{
		Kind:       "model",
		FieldName:  fieldName,
		FieldType:  fieldType,
		IsRequired: false,
		LineNumber: line,
		Definition: definition,
	}}
}

func dedupeVueContracts(in []ParsedVueComponentContract) []ParsedVueComponentContract {
	seen := map[string]bool{}
	out := make([]ParsedVueComponentContract, 0, len(in))
	for _, item := range in {
		key := item.Kind + "\x00" + item.FieldName
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

func splitVueObjectEntry(entry string) (string, string, bool) {
	idx := topLevelVueIndex(entry, ':')
	if idx < 0 {
		name := strings.TrimSpace(stripStringQuotes(entry))
		if name == "" {
			return "", "", false
		}
		return name, "", true
	}
	name := strings.TrimSpace(stripStringQuotes(entry[:idx]))
	value := strings.TrimSpace(entry[idx+1:])
	return name, value, name != ""
}

func extractBalancedDelimited(text string, open, close rune) (string, int) {
	depth := 0
	inString := rune(0)
	escaped := false
	start := -1
	for i, r := range text {
		if inString != 0 {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == inString {
				inString = 0
			}
			continue
		}
		if r == '\'' || r == '"' || r == '`' {
			inString = r
			continue
		}
		if r == open {
			if depth == 0 {
				start = i + len(string(r))
			}
			depth++
			continue
		}
		if r == close {
			depth--
			if depth == 0 && start >= 0 {
				return text[start:i], i + len(string(r))
			}
		}
	}
	return "", -1
}

func splitTopLevelVueEntries(text, separators string) []string {
	var out []string
	start := 0
	depthParen, depthBrace, depthBracket, depthAngle := 0, 0, 0, 0
	inString := rune(0)
	escaped := false
	for i, r := range text {
		if inString != 0 {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == inString {
				inString = 0
			}
			continue
		}
		switch r {
		case '\'', '"', '`':
			inString = r
		case '(':
			depthParen++
		case ')':
			if depthParen > 0 {
				depthParen--
			}
		case '{':
			depthBrace++
		case '}':
			if depthBrace > 0 {
				depthBrace--
			}
		case '[':
			depthBracket++
		case ']':
			if depthBracket > 0 {
				depthBracket--
			}
		case '<':
			depthAngle++
		case '>':
			if depthAngle > 0 {
				depthAngle--
			}
		default:
			if strings.ContainsRune(separators, r) && depthParen == 0 && depthBrace == 0 && depthBracket == 0 && depthAngle == 0 {
				out = append(out, text[start:i])
				start = i + len(string(r))
			}
		}
	}
	out = append(out, text[start:])
	return out
}

func topLevelVueIndex(text string, target rune) int {
	depthParen, depthBrace, depthBracket, depthAngle := 0, 0, 0, 0
	inString := rune(0)
	escaped := false
	for i, r := range text {
		if inString != 0 {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == inString {
				inString = 0
			}
			continue
		}
		switch r {
		case '\'', '"', '`':
			inString = r
		case '(':
			depthParen++
		case ')':
			if depthParen > 0 {
				depthParen--
			}
		case '{':
			depthBrace++
		case '}':
			if depthBrace > 0 {
				depthBrace--
			}
		case '[':
			depthBracket++
		case ']':
			if depthBracket > 0 {
				depthBracket--
			}
		case '<':
			depthAngle++
		case '>':
			if depthAngle > 0 {
				depthAngle--
			}
		default:
			if r == target && depthParen == 0 && depthBrace == 0 && depthBracket == 0 && depthAngle == 0 {
				return i
			}
		}
	}
	return -1
}

func stripStringQuotes(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		first := value[0]
		last := value[len(value)-1]
		if (first == '\'' && last == '\'') || (first == '"' && last == '"') || (first == '`' && last == '`') {
			return strings.TrimSpace(value[1 : len(value)-1])
		}
	}
	return value
}

func leadingIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	end := 0
	for end < len(value) {
		ch := value[end]
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '$' {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return ""
	}
	return value[:end]
}

func (p *JavaScriptParser) extractPiniaStore(node *sitter.Node, content []byte) *ParsedPiniaStore {
	if node == nil || node.Type() != "call_expression" || extractCallCalleeName(node, content) != "defineStore" {
		return nil
	}
	args := callArgumentNodes(node)
	if len(args) == 0 {
		return nil
	}
	storeID := piniaStoreID(args[0], content)
	if storeID == "" {
		return nil
	}
	store := &ParsedPiniaStore{
		StoreID:    storeID,
		LineNumber: int(node.StartPoint().Row) + 1,
	}
	if len(args) < 2 {
		return store
	}
	second := args[1]
	switch second.Type() {
	case "object":
		store.StateFields, store.Getters, store.Actions = p.extractPiniaOptionsStore(second, content)
	case "arrow_function", "function", "function_expression":
		store.StateFields, store.Actions = p.extractPiniaSetupStore(second, content)
	}
	store.StateFields = uniqueNonEmptyJSStrings(store.StateFields)
	store.Getters = uniqueNonEmptyJSStrings(store.Getters)
	store.Actions = uniqueNonEmptyJSStrings(store.Actions)
	return store
}

func callArgumentNodes(call *sitter.Node) []*sitter.Node {
	if call == nil {
		return nil
	}
	var argsNode *sitter.Node
	for i := 0; i < int(call.ChildCount()); i++ {
		if call.Child(i).Type() == "arguments" {
			argsNode = call.Child(i)
			break
		}
	}
	if argsNode == nil {
		return nil
	}
	var args []*sitter.Node
	for i := 0; i < int(argsNode.ChildCount()); i++ {
		arg := argsNode.Child(i)
		if arg == nil || !arg.IsNamed() {
			continue
		}
		args = append(args, arg)
	}
	return args
}

func piniaStoreID(node *sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}
	switch node.Type() {
	case "string", "template_string":
		return strings.TrimSpace(strings.Trim(node.Content(content), "\"'`"))
	case "identifier", "property_identifier":
		return strings.TrimSpace(node.Content(content))
	default:
		return ""
	}
}

func (p *JavaScriptParser) extractPiniaOptionsStore(objectNode *sitter.Node, content []byte) (stateFields, getters, actions []string) {
	for i := 0; i < int(objectNode.ChildCount()); i++ {
		pair := objectNode.Child(i)
		if pair == nil || pair.Type() != "pair" {
			continue
		}
		keyNode := pair.ChildByFieldName("key")
		valueNode := pair.ChildByFieldName("value")
		if keyNode == nil || valueNode == nil {
			continue
		}
		switch strings.TrimSpace(stripStringQuotes(keyNode.Content(content))) {
		case "state":
			stateFields = append(stateFields, p.extractPiniaStateFields(valueNode, content)...)
		case "getters":
			if valueNode.Type() == "object" {
				getters = append(getters, objectPropertyNames(valueNode, content)...)
			}
		case "actions":
			if valueNode.Type() == "object" {
				actions = append(actions, objectPropertyNames(valueNode, content)...)
			}
		}
	}
	return stateFields, getters, actions
}

func (p *JavaScriptParser) extractPiniaStateFields(node *sitter.Node, content []byte) []string {
	node = unwrapAwaitExpression(node)
	if node == nil {
		return nil
	}
	if node.Type() == "object" {
		return objectPropertyNames(node, content)
	}
	if node.Type() == "parenthesized_expression" {
		for i := 0; i < int(node.ChildCount()); i++ {
			if child := node.Child(i); child != nil && child.Type() == "object" {
				return objectPropertyNames(child, content)
			}
		}
	}
	if node.Type() == "arrow_function" || node.Type() == "function" || node.Type() == "function_expression" {
		if returned := firstReturnedObject(node); returned != nil {
			return objectPropertyNames(returned, content)
		}
	}
	return nil
}

func (p *JavaScriptParser) extractPiniaSetupStore(node *sitter.Node, content []byte) (stateFields, actions []string) {
	returned := firstReturnedObject(node)
	if returned == nil {
		return nil, nil
	}
	for _, name := range objectPropertyNames(returned, content) {
		if p.setupStoreNameLooksAction(node, content, name) {
			actions = append(actions, name)
		} else {
			stateFields = append(stateFields, name)
		}
	}
	return stateFields, actions
}

func (p *JavaScriptParser) setupStoreNameLooksAction(scope *sitter.Node, content []byte, name string) bool {
	if scope == nil || strings.TrimSpace(name) == "" {
		return false
	}
	var found bool
	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil || found {
			return
		}
		switch node.Type() {
		case "function_declaration":
			for i := 0; i < int(node.ChildCount()); i++ {
				if child := node.Child(i); child != nil && child.Type() == "identifier" && child.Content(content) == name {
					found = true
					return
				}
			}
		case "variable_declarator":
			var declName string
			var value *sitter.Node
			for i := 0; i < int(node.ChildCount()); i++ {
				child := node.Child(i)
				if child.Type() == "identifier" && declName == "" {
					declName = child.Content(content)
				} else if child.Type() == "arrow_function" || child.Type() == "function" || child.Type() == "function_expression" {
					value = child
				}
			}
			if declName == name && value != nil {
				found = true
				return
			}
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			walk(node.Child(i))
		}
	}
	walk(scope)
	return found
}

func firstReturnedObject(node *sitter.Node) *sitter.Node {
	if node == nil {
		return nil
	}
	if node.Type() == "object" {
		return node
	}
	var found *sitter.Node
	var walk func(*sitter.Node)
	walk = func(current *sitter.Node) {
		if current == nil || found != nil {
			return
		}
		if current.Type() == "return_statement" {
			for i := 0; i < int(current.ChildCount()); i++ {
				child := current.Child(i)
				if child == nil {
					continue
				}
				if child.Type() == "object" {
					found = child
					return
				}
				if child.Type() == "parenthesized_expression" {
					for j := 0; j < int(child.ChildCount()); j++ {
						if grandchild := child.Child(j); grandchild != nil && grandchild.Type() == "object" {
							found = grandchild
							return
						}
					}
				}
			}
		}
		if current.Type() == "arrow_function" {
			for i := 0; i < int(current.ChildCount()); i++ {
				child := current.Child(i)
				if child != nil && child.Type() == "object" {
					found = child
					return
				}
				if child != nil && child.Type() == "parenthesized_expression" {
					for j := 0; j < int(child.ChildCount()); j++ {
						if grandchild := child.Child(j); grandchild != nil && grandchild.Type() == "object" {
							found = grandchild
							return
						}
					}
				}
			}
		}
		for i := 0; i < int(current.ChildCount()); i++ {
			walk(current.Child(i))
		}
	}
	walk(node)
	return found
}

func objectPropertyNames(objectNode *sitter.Node, content []byte) []string {
	if objectNode == nil || objectNode.Type() != "object" {
		return nil
	}
	var names []string
	for i := 0; i < int(objectNode.ChildCount()); i++ {
		child := objectNode.Child(i)
		if child == nil {
			continue
		}
		switch child.Type() {
		case "pair":
			key := child.ChildByFieldName("key")
			if key == nil {
				continue
			}
			name := strings.TrimSpace(stripStringQuotes(key.Content(content)))
			if name != "" {
				names = append(names, name)
			}
		case "method_definition":
			for j := 0; j < int(child.ChildCount()); j++ {
				key := child.Child(j)
				if key == nil {
					continue
				}
				if key.Type() == "property_identifier" || key.Type() == "identifier" || key.Type() == "string" {
					name := strings.TrimSpace(stripStringQuotes(key.Content(content)))
					if name != "" {
						names = append(names, name)
						break
					}
				}
			}
		case "shorthand_property_identifier":
			name := strings.TrimSpace(child.Content(content))
			if name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

func uniqueNonEmptyJSStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func isCompositionHookName(name string) bool {
	name = strings.TrimSpace(name)
	_, builtIn := builtInHookOrigins[name]
	return name != "" && (strings.HasPrefix(name, "use") || builtIn)
}

func hookCapturesInitialValue(name string) bool {
	switch strings.TrimSpace(name) {
	case "useState", "ref", "shallowRef", "reactive", "shallowReactive", "computed", "defineStore":
		return true
	default:
		return false
	}
}

// extractArrayElements extracts element identifiers from an array
func (p *JavaScriptParser) extractArrayElements(node *sitter.Node, content []byte) []string {
	var elements []string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		if childType == "identifier" || childType == "member_expression" {
			elements = append(elements, child.Content(content))
		}
	}

	return elements
}

func (p *JavaScriptParser) markExports(node *sitter.Node, result *ParsedFile, content []byte) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		if childType == "function_declaration" {
			for j := 0; j < int(child.ChildCount()); j++ {
				if child.Child(j).Type() == "identifier" {
					name := child.Child(j).Content(content)
					for k := range result.Functions {
						if result.Functions[k].Name == name {
							result.Functions[k].IsExported = true
						}
					}
					break
				}
			}
		}

		if childType == "class_declaration" {
			for j := 0; j < int(child.ChildCount()); j++ {
				childNodeType := child.Child(j).Type()
				if childNodeType == "identifier" || childNodeType == "type_identifier" {
					name := child.Child(j).Content(content)
					for k := range result.Classes {
						if result.Classes[k].Name == name {
							result.Classes[k].IsExported = true
						}
					}
					break
				}
			}
		}

		if childType == "lexical_declaration" || childType == "variable_declaration" {
			for j := 0; j < int(child.ChildCount()); j++ {
				decl := child.Child(j)
				if decl.Type() == "variable_declarator" {
					for k := 0; k < int(decl.ChildCount()); k++ {
						if decl.Child(k).Type() == "identifier" {
							name := decl.Child(k).Content(content)
							for l := range result.Functions {
								if result.Functions[l].Name == name {
									result.Functions[l].IsExported = true
								}
							}
							break
						}
					}
				}
			}
		}

		// Mark exported interfaces
		if childType == "interface_declaration" {
			for j := 0; j < int(child.ChildCount()); j++ {
				if child.Child(j).Type() == "type_identifier" {
					name := child.Child(j).Content(content)
					for k := range result.Interfaces {
						if result.Interfaces[k].Name == name {
							result.Interfaces[k].IsExported = true
						}
					}
					break
				}
			}
		}

		// Mark exported type aliases
		if childType == "type_alias_declaration" {
			for j := 0; j < int(child.ChildCount()); j++ {
				if child.Child(j).Type() == "type_identifier" {
					name := child.Child(j).Content(content)
					for k := range result.TypeAliases {
						if result.TypeAliases[k].Name == name {
							result.TypeAliases[k].IsExported = true
						}
					}
					break
				}
			}
		}

		// Mark exported enums (stored as classes with IsEnum=true)
		if childType == "enum_declaration" {
			for j := 0; j < int(child.ChildCount()); j++ {
				if child.Child(j).Type() == "identifier" {
					name := child.Child(j).Content(content)
					for k := range result.Classes {
						if result.Classes[k].Name == name && result.Classes[k].IsEnum {
							result.Classes[k].IsExported = true
						}
					}
					break
				}
			}
		}
	}
}

func getSourceCode(lines []string, startLine, endLine int) string {
	if startLine < 1 || endLine > len(lines) {
		return ""
	}
	return strings.Join(lines[startLine-1:endLine], "\n")
}

type httpClientAlias struct {
	PatternName string
	StartByte   uint32
	StartRow    uint32
}

func (p *JavaScriptParser) extractHttpClientAliases(root *sitter.Node, content []byte) map[string][]httpClientAlias {
	aliases := make(map[string][]httpClientAlias)
	factoryNames := make(map[string]bool)

	var collectFactories func(node *sitter.Node)
	collectFactories = func(node *sitter.Node) {
		if node == nil {
			return
		}
		switch node.Type() {
		case "function_declaration":
			if name := extractFunctionName(node, content); name != "" {
				if p.containsAxiosCreateCall(node, content) {
					factoryNames[name] = true
				}
			}
		case "variable_declarator":
			name, value := extractDeclaratorNameAndValue(node, content)
			if name != "" && value != nil {
				switch value.Type() {
				case "arrow_function", "function", "function_expression":
					if p.containsAxiosCreateCall(value, content) {
						factoryNames[name] = true
					}
				}
			}
		case "assignment_expression":
			name, value := extractAssignmentNameAndValue(node, content)
			if name != "" && value != nil {
				switch value.Type() {
				case "arrow_function", "function", "function_expression":
					if p.containsAxiosCreateCall(value, content) {
						factoryNames[name] = true
					}
				}
			}
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			collectFactories(node.Child(i))
		}
	}

	var collectAliases func(node *sitter.Node)
	collectAliases = func(node *sitter.Node) {
		if node == nil {
			return
		}
		switch node.Type() {
		case "variable_declarator":
			name, value := extractDeclaratorNameAndValue(node, content)
			if name != "" {
				if alias := p.inferAxiosAlias(value, factoryNames, content); alias != "" {
					aliases[name] = append(aliases[name], httpClientAlias{
						PatternName: alias,
						StartByte:   node.StartByte(),
						StartRow:    node.StartPoint().Row,
					})
				}
			}
		case "assignment_expression":
			name, value := extractAssignmentNameAndValue(node, content)
			if name != "" {
				if alias := p.inferAxiosAlias(value, factoryNames, content); alias != "" {
					aliases[name] = append(aliases[name], httpClientAlias{
						PatternName: alias,
						StartByte:   node.StartByte(),
						StartRow:    node.StartPoint().Row,
					})
				}
			}
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			collectAliases(node.Child(i))
		}
	}

	collectFactories(root)
	collectAliases(root)
	return aliases
}

func httpClientAliasAt(aliases map[string][]httpClientAlias, name string, callNode *sitter.Node) string {
	if aliases == nil || callNode == nil || name == "" {
		return ""
	}
	var selected *httpClientAlias
	callByte := callNode.StartByte()
	callRow := callNode.StartPoint().Row
	for i := range aliases[name] {
		alias := aliases[name][i]
		if alias.StartByte > callByte {
			continue
		}
		if alias.StartRow > callRow {
			continue
		}
		if selected == nil || alias.StartByte >= selected.StartByte {
			selected = &aliases[name][i]
		}
	}
	if selected == nil {
		return ""
	}
	return selected.PatternName
}

func extractFunctionName(node *sitter.Node, content []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "identifier" {
			return child.Content(content)
		}
	}
	return ""
}

func extractDeclaratorNameAndValue(node *sitter.Node, content []byte) (string, *sitter.Node) {
	var name string
	var value *sitter.Node
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "identifier":
			if name == "" {
				name = child.Content(content)
			}
		case "=":
			continue
		default:
			if value == nil && child.Type() != "type_annotation" {
				value = child
			}
		}
	}
	return name, value
}

func extractAssignmentNameAndValue(node *sitter.Node, content []byte) (string, *sitter.Node) {
	if node.ChildCount() < 3 {
		return "", nil
	}
	left := node.Child(0)
	right := node.Child(2)

	switch left.Type() {
	case "identifier", "member_expression", "optional_member_expression":
		return left.Content(content), right
	}

	return "", nil
}

func (p *JavaScriptParser) inferAxiosAlias(value *sitter.Node, factoryNames map[string]bool, content []byte) string {
	if value == nil || value.Type() != "call_expression" {
		return ""
	}
	if p.isAxiosCreateCall(value, content) {
		return "axios"
	}
	if callee := extractCallCalleeName(value, content); callee != "" && factoryNames[callee] {
		return "axios"
	}
	return ""
}

func extractCallCalleeName(node *sitter.Node, content []byte) string {
	if node == nil || node.Type() != "call_expression" || node.ChildCount() == 0 {
		return ""
	}
	funcNode := node.Child(0)
	switch funcNode.Type() {
	case "identifier":
		return funcNode.Content(content)
	case "member_expression", "optional_member_expression":
		return extractMemberPropertyName(funcNode, content)
	}
	return ""
}

func (p *JavaScriptParser) containsAxiosCreateCall(node *sitter.Node, content []byte) bool {
	if node == nil {
		return false
	}
	if node.Type() == "call_expression" && p.isAxiosCreateCall(node, content) {
		return true
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		if p.containsAxiosCreateCall(node.Child(i), content) {
			return true
		}
	}
	return false
}

func (p *JavaScriptParser) isAxiosCreateCall(node *sitter.Node, content []byte) bool {
	if node == nil || node.Type() != "call_expression" || node.ChildCount() == 0 {
		return false
	}
	funcNode := node.Child(0)
	switch funcNode.Type() {
	case "member_expression", "optional_member_expression":
		receiver, method := p.extractReceiverAndMethod(funcNode, content)
		receiver = strings.ReplaceAll(receiver, "?.", ".")
		return (receiver == "axios" || receiver == "Axios") && method == "create"
	}
	return false
}

// extractHttpCall extracts HTTP client calls like axios.get(), fetch(), etc.
func (p *JavaScriptParser) extractHttpCall(node *sitter.Node, content []byte, httpClientAliases map[string][]httpClientAlias) *ParsedHttpCall {
	if httpClientAliases == nil || node.ChildCount() == 0 {
		return nil
	}

	// Methods called on a known router are route registrations or settings
	// reads, never outbound requests.
	if p.callOnRouter(node) {
		return nil
	}

	funcNode := node.Child(0)
	var clientType, method, url string
	var receiver, methodName string
	var methodDynamic bool
	lineNumber := int(node.StartPoint().Row) + 1

	// Unwrap optional chaining to find the callable member expression.
	if funcNode.Type() == "optional_chain" {
		for i := 0; i < int(funcNode.ChildCount()); i++ {
			child := funcNode.Child(i)
			switch child.Type() {
			case "member_expression", "optional_member_expression", "subscript_expression", "optional_subscript_expression":
				funcNode = child
			case "call_expression":
				if innerCall := p.extractHttpCall(child, content, httpClientAliases); innerCall != nil {
					return innerCall
				}
			}
		}
	}

	// Check for object.method() patterns (axios.get, $http.post, etc.)
	if funcNode.Type() == "member_expression" || funcNode.Type() == "optional_member_expression" {
		receiver, methodName = p.extractReceiverAndMethod(funcNode, content)
		receiver = strings.ReplaceAll(receiver, "?.", ".")
	} else if funcNode.Type() == "subscript_expression" || funcNode.Type() == "optional_subscript_expression" {
		receiver, methodName, methodDynamic = p.extractSubscriptReceiverAndMethod(funcNode, content)
		receiver = strings.ReplaceAll(receiver, "?.", ".")
	}

	if receiver != "" || methodName != "" {
		candidates := make(map[string]bool)
		if receiver != "" {
			candidates[receiver] = true
			trimmed := strings.TrimPrefix(receiver, "this.")
			trimmed = strings.TrimPrefix(trimmed, "super.")
			if trimmed != "" {
				candidates[trimmed] = true
				if idx := strings.LastIndex(trimmed, "."); idx >= 0 && idx < len(trimmed)-1 {
					candidates[trimmed[idx+1:]] = true
				}
				if idx := strings.Index(trimmed, "."); idx > 0 {
					candidates[trimmed[:idx]] = true
				}
			}
		}
		if len(candidates) == 0 {
			candidates[funcNode.Content(content)] = true
		}

		if clientType == "" && len(httpClientAliases) > 0 {
			for alias := range candidates {
				patternName := httpClientAliasAt(httpClientAliases, alias, node)
				if patternName == "" {
					continue
				}
				for _, pattern := range p.config.GetJavaScriptPatterns() {
					if pattern.Name != patternName {
						continue
					}
					if verb, ok := pattern.Methods[strings.ToLower(methodName)]; ok {
						clientType = pattern.Name
						method = verb
						break
					}
					if methodDynamic && clientType == "" {
						clientType = pattern.Name
						method = "ANY"
						break
					}
				}
				if clientType != "" {
					break
				}
			}
		}

		// Check against configured patterns
		if clientType == "" {
			for _, pattern := range p.config.GetJavaScriptPatterns() {
				for _, obj := range pattern.Objects {
					if !candidates[obj] {
						continue
					}
					if verb, ok := pattern.Methods[strings.ToLower(methodName)]; ok {
						clientType = pattern.Name
						method = verb
						break
					}
					if methodDynamic && clientType == "" {
						clientType = pattern.Name
						method = "ANY"
						break
					}
				}
				if clientType != "" {
					break
				}
			}
		}
	}

	// Check for fetch() pattern - function call without object
	if funcNode.Type() == "identifier" {
		funcName := funcNode.Content(content)
		methodName = funcName
		for _, pattern := range p.config.GetJavaScriptPatterns() {
			for _, obj := range pattern.Objects {
				if funcName == obj {
					clientType = pattern.Name
					if verb, ok := pattern.Methods[""]; ok {
						method = verb
					} else {
						method = "ANY"
					}
					break
				}
			}
			if clientType != "" {
				break
			}
		}
	}

	// Check for await fetch pattern (fetch is wrapped)
	if funcNode.Type() == "await_expression" {
		for i := 0; i < int(funcNode.ChildCount()); i++ {
			child := funcNode.Child(i)
			if child.Type() == "call_expression" {
				if innerCall := p.extractHttpCall(child, content, httpClientAliases); innerCall != nil {
					return innerCall
				}
			}
		}
	}

	if clientType == "" {
		// We'll try to infer a generic HTTP client from args/method name below.
	}

	// Extract URL and method from arguments
	var argsNode *sitter.Node
	for i := 0; i < int(node.ChildCount()); i++ {
		if node.Child(i).Type() == "arguments" {
			argsNode = node.Child(i)
			break
		}
	}

	configHTTPShaped := false
	if argsNode != nil {
		argumentIndex := 0
		for i := 0; i < int(argsNode.ChildCount()); i++ {
			child := argsNode.Child(i)
			if !child.IsNamed() || child.Type() == "comment" {
				continue
			}
			isURLArgument := argumentIndex == 0
			argumentIndex++
			// String literal URL
			if isURLArgument && child.Type() == "string" && url == "" {
				url = strings.Trim(child.Content(content), "\"'`")
			}
			// Template literal URL
			if isURLArgument && child.Type() == "template_string" && url == "" {
				url = p.extractUrlFromTemplateString(child.Content(content))
			}
			// Binary expression (concatenation): API_CONTEXT_PATH + '/projects/' + id
			if isURLArgument && child.Type() == "binary_expression" && url == "" {
				url = p.extractUrlFromBinaryExpr(child, content)
			}
			if isURLArgument && (child.Type() == "identifier" || child.Type() == "member_expression") {
				cfgURL, cfgMethod := p.extractResolvedHTTPConfigFromReference(node, child, content)
				if cfgURL != "" && url == "" {
					url = cfgURL
				}
				if cfgMethod != "" {
					method = cfgMethod
				}
			}
			// Config object: { url, method }
			if child.Type() == "object" {
				cfgURL, cfgMethod := p.extractHttpConfigFromObject(child, content)
				if cfgURL != "" && url == "" {
					url = cfgURL
				}
				if cfgMethod != "" {
					method = cfgMethod
				}
				// A config object carrying both a concrete HTTP verb and a
				// URL/endpoint is an HTTP request regardless of the (often custom
				// or cross-file imported) wrapper function it is passed to,
				// e.g. callHttpApi({ method: 'POST', endpoint: '/api/...' }).
				if cfgURL != "" && isHTTPVerb(cfgMethod) {
					configHTTPShaped = true
				}
			}
		}
	}

	if clientType == "" && configHTTPShaped {
		clientType = "http-config"
	}

	if clientType == "" && methodDynamic && looksLikeHttpReceiver(receiver) {
		clientType = "unknown"
		method = "ANY"
	}
	if clientType == "" {
		if verb, ok := httpVerbFromMethodName(methodName); ok {
			if url != "" && (looksLikeHttpReceiver(receiver) || methodName == "request") {
				clientType = "unknown"
				if method == "" {
					method = verb
				}
			}
		}
	}

	if url != "" {
		url = normalizeHttpURL(url)
	}
	// Skip if no URL found or URL is just a variable
	if clientType == "" || url == "" || (!strings.HasPrefix(url, "/") && !strings.HasPrefix(url, "http")) {
		return nil
	}

	return &ParsedHttpCall{
		HttpMethod: method,
		UrlPattern: url,
		LineNumber: lineNumber,
		ClientType: clientType,
	}
}

func (p *JavaScriptParser) extractResolvedHTTPConfigFromReference(refNode, valueNode *sitter.Node, content []byte) (string, string) {
	if valueNode == nil {
		return "", ""
	}

	var resolved *sitter.Node
	switch valueNode.Type() {
	case "identifier", "member_expression":
		resolved = p.resolveNodeValueBeforeNode(refNode, valueNode, content)
		if resolved == nil {
			return "", ""
		}
	default:
		return "", ""
	}
	if resolved == nil {
		return "", ""
	}
	if resolved.Type() == "object" {
		return p.extractHttpConfigFromObject(resolved, content)
	}
	return p.extractUrlFromValueNode(resolved, content), ""
}

func (p *JavaScriptParser) extractResolvedURLFromReference(refNode, valueNode *sitter.Node, content []byte) string {
	if valueNode == nil {
		return ""
	}

	switch valueNode.Type() {
	case "identifier", "member_expression":
		resolved := p.resolveNodeValueBeforeNode(refNode, valueNode, content)
		if resolved == nil {
			return ""
		}
		return p.extractUrlFromValueNode(resolved, content)
	}

	return ""
}

func referenceURLName(node *sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}
	if node.Type() == "member_expression" {
		return extractMemberPropertyName(node, content)
	}
	return strings.TrimSpace(node.Content(content))
}

// maxFindVariableWalkNodes caps total nodes visited by findVariableValueBeforeNode.
// Large minified/bundled JS files produce massive flat ASTs (thousands of siblings)
// that turn this walk into a multi-minute hang even at moderate depth. A node budget
// bails out early and returns whatever has been found so far.
const maxFindVariableWalkNodes = 1_000

func (p *JavaScriptParser) findVariableValueBeforeNode(refNode *sitter.Node, name string, content []byte) *sitter.Node {
	if refNode == nil || strings.TrimSpace(name) == "" {
		return nil
	}

	scope := p.findContainingCallableNode(refNode)
	if scope == nil {
		scope = p.findProgramNode(refNode)
	}
	if scope == nil {
		return nil
	}

	refRow := refNode.StartPoint().Row
	var best *sitter.Node
	var bestRow uint32
	visited := 0

	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil || visited >= maxFindVariableWalkNodes {
			return
		}
		visited++
		if node.Type() == "variable_declarator" {
			var ident *sitter.Node
			var value *sitter.Node
			ident = node.ChildByFieldName("name")
			value = node.ChildByFieldName("value")
			if ident == nil || value == nil {
				for i := 0; i < int(node.ChildCount()); i++ {
					child := node.Child(i)
					switch child.Type() {
					case "identifier":
						if ident == nil {
							ident = child
						}
					case "string", "template_string", "binary_expression", "member_expression", "object":
						if value == nil {
							value = child
						}
					}
				}
			}
			if ident != nil && value != nil && ident.Content(content) == name {
				row := node.StartPoint().Row
				if row <= refRow && (best == nil || row >= bestRow) {
					best = value
					bestRow = row
				}
			}
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			walk(node.Child(i))
		}
	}

	walk(scope)
	return best
}

func (p *JavaScriptParser) findModuleVariableValueBeforeNode(refNode *sitter.Node, name string, content []byte) *sitter.Node {
	if refNode == nil || strings.TrimSpace(name) == "" {
		return nil
	}
	program := p.findProgramNode(refNode)
	if program == nil {
		return nil
	}

	refRow := refNode.StartPoint().Row
	var best *sitter.Node
	var bestRow uint32
	visited := 0
	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil || visited >= maxFindVariableWalkNodes {
			return
		}
		visited++
		if node.Type() == "variable_declarator" {
			if p.findContainingCallableNode(node) != nil {
				return
			}
			ident := node.ChildByFieldName("name")
			value := node.ChildByFieldName("value")
			if ident == nil || value == nil {
				for i := 0; i < int(node.ChildCount()); i++ {
					child := node.Child(i)
					switch child.Type() {
					case "identifier":
						if ident == nil {
							ident = child
						}
					case "string", "template_string", "binary_expression", "member_expression", "object":
						if value == nil {
							value = child
						}
					}
				}
			}
			if ident != nil && value != nil && ident.Content(content) == name {
				row := node.StartPoint().Row
				if row <= refRow && (best == nil || row >= bestRow) {
					best = value
					bestRow = row
				}
			}
			return
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			walk(node.Child(i))
		}
	}
	walk(program)
	return best
}

func (p *JavaScriptParser) findContainingCallableNode(node *sitter.Node) *sitter.Node {
	for current := node; current != nil; current = current.Parent() {
		switch current.Type() {
		case "function_declaration", "function", "method_definition", "arrow_function", "generator_function", "generator_function_declaration":
			return current
		}
	}
	return nil
}

func (p *JavaScriptParser) findProgramNode(node *sitter.Node) *sitter.Node {
	for current := node; current != nil; current = current.Parent() {
		if current.Type() == "program" {
			return current
		}
	}
	return nil
}

// looksMinifiedOrVendor returns true when the file is almost certainly a
// bundled, minified, or third-party vendor file that should be excluded from
// expensive HTTP call extraction. Detected via:
//   - .min.js / .min.ts / .min.jsx / .min.tsx / .bundle.js suffix
//   - vendor path segments (wwwroot/lib, bower_components, vendor/js, vendor/lib,
//     __generated__)
//   - average line length > 300 chars (minifier signature)
//   - file > 500 KB with average line length > 150 chars (large semi-minified bundles)
//
// LooksMinifiedOrVendor is the exported version for use by extractor binaries.
func LooksMinifiedOrVendor(filePath string, content []byte) bool {
	lines := strings.Split(string(content), "\n")
	return looksMinifiedOrVendor(filePath, content, lines)
}

func looksMinifiedOrVendor(filePath string, content []byte, lines []string) bool {
	lower := strings.ToLower(filePath)

	// Explicit minified suffix
	if strings.HasSuffix(lower, ".min.js") || strings.HasSuffix(lower, ".min.ts") ||
		strings.HasSuffix(lower, ".min.jsx") || strings.HasSuffix(lower, ".min.tsx") ||
		strings.HasSuffix(lower, ".bundle.js") {
		return true
	}

	// Known vendor path segments
	vendorSegments := []string{
		"/wwwroot/lib/", "/bower_components/",
		"/vendor/js/", "/vendor/lib/", "/__generated__/",
	}
	for _, seg := range vendorSegments {
		if strings.Contains(lower, seg) {
			return true
		}
	}

	// Average line length heuristic
	lineCount := len(lines)
	if lineCount == 0 {
		return false
	}
	avgLineLen := len(content) / lineCount

	// Strong signal: very long lines regardless of file size
	if avgLineLen > 300 {
		return true
	}

	// Weak signal: large file with moderately long lines (semi-minified bundles)
	if len(content) > 500*1024 && avgLineLen > 150 {
		return true
	}

	return false
}

func looksLikeGraphQLRegistryPath(path string) bool {
	lower := strings.ToLower(strings.TrimSpace(path))
	return strings.Contains(lower, "graphql") && !isGraphQLDocumentPath(lower)
}

func looksLikeGraphQLOperationName(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	if !graphQLOperationNamePattern.MatchString(value) {
		return false
	}
	return true
}

func (p *JavaScriptParser) extractHttpConfigFromObject(node *sitter.Node, content []byte) (string, string) {
	var url string
	var method string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "shorthand_property_identifier" {
			key := strings.Trim(child.Content(content), "\"'`")
			switch strings.ToLower(key) {
			case "url", "uri", "path":
				// Resolve the binding, not the property name as a fabricated route.
				resolved := p.findVariableValueBeforeNode(node, key, content)
				if resolved == nil {
					resolved = p.findModuleVariableValueBeforeNode(node, key, content)
				}
				if resolved != nil && url == "" {
					url = p.extractUrlFromValueNode(resolved, content)
				}
			}
			continue
		}
		if child.Type() != "pair" {
			continue
		}

		var key string
		var value *sitter.Node
		for j := 0; j < int(child.ChildCount()); j++ {
			propChild := child.Child(j)
			if key == "" && (propChild.Type() == "property_identifier" || propChild.Type() == "string") {
				key = strings.Trim(propChild.Content(content), "\"'`")
				continue
			}
			if key != "" && value == nil && propChild.Type() != ":" {
				value = propChild
			}
		}

		switch strings.ToLower(key) {
		case "url", "uri", "path", "endpoint", "resource":
			if url == "" {
				url = p.extractUrlFromValueNode(value, content)
			}
		case "method", "type":
			if method == "" {
				method = extractMethodFromValueNode(value, content)
			}
		}
	}

	return url, method
}

// Custom wrapper methods are declared in HTTP client patterns, not inferred by suffix.
func httpVerbFromMethodName(name string) (string, bool) {
	return genericHttpMethod(strings.ToLower(strings.TrimSpace(name)))
}

// collectTemplatePrefixConstants gathers per-file path-literal constants (class
// fields or consts assigned a string beginning with '/') so URL templates that
// lead with `${this.<field>}` resolve to the full route.
func (p *JavaScriptParser) collectTemplatePrefixConstants(root *sitter.Node, content []byte) map[string]string {
	out := map[string]string{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "public_field_definition", "field_definition", "variable_declarator", "property_signature", "assignment_expression":
			name, value := classFieldNameAndStringValue(n, content)
			if name != "" && strings.HasPrefix(value, "/") {
				if _, exists := out[name]; !exists {
					out[name] = value
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	return out
}

// classFieldNameAndStringValue pulls a (name, string-literal value) pair out of a
// field/variable/assignment node, e.g. `controllerUrl = '/api/resources/'`.
func classFieldNameAndStringValue(n *sitter.Node, content []byte) (string, string) {
	var name, value string
	for i := 0; i < int(n.ChildCount()); i++ {
		child := n.Child(i)
		switch child.Type() {
		case "property_identifier", "identifier":
			if name == "" {
				name = child.Content(content)
			}
		case "string":
			if value == "" {
				value = strings.Trim(child.Content(content), "\"'`")
			}
		}
	}
	return name, value
}

// isHTTPVerb reports whether method is a concrete HTTP verb literal.
func isHTTPVerb(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS":
		return true
	}
	return false
}

func (p *JavaScriptParser) extractUrlFromValueNode(node *sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}

	switch node.Type() {
	case "string":
		return normalizeHttpURL(strings.Trim(node.Content(content), "\"'`"))
	case "template_string":
		return normalizeHttpURL(p.extractUrlFromTemplateString(node.Content(content)))
	case "binary_expression":
		return p.extractUrlFromBinaryExpr(node, content)
	case "identifier":
		varName := node.Content(content)
		if p.isTemplateURLPrefixVariable(varName) {
			return ""
		}
		return normalizeHttpURL(":" + varName)
	case "member_expression":
		if prop := extractMemberPropertyName(node, content); prop != "" {
			if p.isTemplateURLPrefixVariable(prop) {
				return ""
			}
			return normalizeHttpURL(":" + prop)
		}
	}

	return ""
}

func (p *JavaScriptParser) extractUrlFromTemplateString(raw string) string {
	raw = strings.Trim(strings.TrimSpace(raw), "`")
	if raw == "" {
		return ""
	}
	if p.templateStringIsGraphQLDocument(raw) {
		return ""
	}

	var out strings.Builder
	skippedLeadingPrefix := false
	for i := 0; i < len(raw); {
		start := strings.Index(raw[i:], "${")
		if start < 0 {
			out.WriteString(raw[i:])
			break
		}
		start += i
		out.WriteString(raw[i:start])
		end := strings.Index(raw[start+2:], "}")
		if end < 0 {
			out.WriteString(raw[start:])
			break
		}
		end += start + 2
		expr := strings.TrimSpace(raw[start+2 : end])
		param := templateURLParamName(expr)
		if param == "" {
			param = "value"
		}
		if out.Len() == 0 && (p.isTemplateURLPrefixVariable(expr) || p.isTemplateURLPrefixVariable(param)) {
			// If the leading prefix is a known path-literal constant (e.g.
			// controllerUrl = '/api/resources/'), substitute its value so the
			// full route is matchable instead of dropping it.
			if val := p.templatePrefixConstants[param]; val != "" {
				out.WriteString(val)
				i = end + 1
				continue
			}
			skippedLeadingPrefix = true
			i = end + 1
			continue
		}
		out.WriteString(":" + param)
		i = end + 1
	}

	value := out.String()
	if skippedLeadingPrefix && value != "" && !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return normalizeHttpURL(value)
}

func (p *JavaScriptParser) templateStringIsGraphQLDocument(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	if graphQLDocumentMatchAtStart(trimmed) {
		return true
	}
	idx := firstGraphQLDocumentIndex(trimmed)
	if idx < 0 {
		return false
	}
	prefix := strings.TrimSpace(trimmed[:idx])
	if prefix == "" {
		return true
	}
	if strings.HasPrefix(prefix, "${") && strings.HasSuffix(prefix, "}") {
		param := templateURLParamName(strings.TrimSuffix(strings.TrimPrefix(prefix, "${"), "}"))
		return p.isTemplateURLPrefixVariable(param)
	}
	firstLine := strings.TrimSpace(strings.Split(prefix, "\n")[0])
	return strings.HasPrefix(firstLine, "http://") ||
		strings.HasPrefix(firstLine, "https://") ||
		strings.HasPrefix(firstLine, "/") ||
		strings.HasPrefix(firstLine, "${")
}

func graphQLDocumentMatchAtStart(raw string) bool {
	for _, re := range []*regexp.Regexp{graphqlOperationPattern, anonymousGraphQLOperationPattern} {
		if loc := re.FindStringIndex(raw); loc != nil && loc[0] == 0 {
			return true
		}
	}
	return false
}

func firstGraphQLDocumentIndex(raw string) int {
	first := -1
	for _, re := range []*regexp.Regexp{graphqlOperationPattern, anonymousGraphQLOperationPattern} {
		if loc := re.FindStringIndex(raw); loc != nil && (first < 0 || loc[0] < first) {
			first = loc[0]
		}
	}
	return first
}

func (p *JavaScriptParser) isTemplateURLPrefixVariable(varName string) bool {
	if p.isURLPrefixVariable(varName) {
		return true
	}
	normalized := strings.NewReplacer("_", "", "-", "", ".", "", "$", "").Replace(strings.ToLower(varName))
	return strings.Contains(normalized, "url") ||
		strings.Contains(normalized, "uri") ||
		strings.Contains(normalized, "endpoint") ||
		strings.Contains(normalized, "origin") ||
		strings.Contains(normalized, "host")
}

func templateURLParamName(expr string) string {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return ""
	}
	expr = strings.TrimPrefix(expr, "this.")
	expr = strings.TrimPrefix(expr, "_self.")
	parts := strings.FieldsFunc(expr, func(r rune) bool {
		return !(r == '_' || r == '$' || r == '-' || r == '.' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')
	})
	if len(parts) == 0 {
		return ""
	}
	candidate := strings.Trim(parts[len(parts)-1], " .")
	if idx := strings.LastIndex(candidate, "."); idx >= 0 && idx < len(candidate)-1 {
		candidate = candidate[idx+1:]
	}
	return strings.Trim(candidate, " .")
}

func extractMethodFromValueNode(node *sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}

	switch node.Type() {
	case "string":
		return strings.ToUpper(strings.Trim(node.Content(content), "\"'`"))
	case "identifier", "property_identifier":
		return strings.ToUpper(node.Content(content))
	case "member_expression":
		if prop := extractMemberPropertyName(node, content); prop != "" {
			return strings.ToUpper(prop)
		}
	}

	return ""
}

func extractMemberPropertyName(node *sitter.Node, content []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "property_identifier" {
			return child.Content(content)
		}
	}
	return ""
}

func genericHttpMethod(name string) (string, bool) {
	switch strings.ToLower(name) {
	case "get":
		return "GET", true
	case "post":
		return "POST", true
	case "put":
		return "PUT", true
	case "delete", "del":
		return "DELETE", true
	case "patch":
		return "PATCH", true
	case "head":
		return "HEAD", true
	case "options":
		return "OPTIONS", true
	case "request":
		return "ANY", true
	}
	return "", false
}

func looksLikeHttpReceiver(receiver string) bool {
	if receiver == "" {
		return false
	}
	receiver = strings.TrimSpace(receiver)
	lowerReceiver := strings.ToLower(receiver)
	if lowerReceiver == "$" || lowerReceiver == "$http" || lowerReceiver == "axios" || lowerReceiver == "fetch" || lowerReceiver == "request" {
		return true
	}
	tokens := splitIdentifierTokens(receiver)
	if len(tokens) > 0 {
		// apiRouter and adminRoutes serve requests rather than send them.
		switch tokens[len(tokens)-1] {
		case "router", "routes", "route":
			return false
		}
	}
	for _, token := range tokens {
		switch token {
		case "http", "api", "request", "axios", "fetch":
			return true
		}
	}
	return false
}

func splitIdentifierTokens(value string) []string {
	var tokens []string
	var current strings.Builder
	var prev rune
	flush := func() {
		if current.Len() == 0 {
			return
		}
		tokens = append(tokens, strings.ToLower(current.String()))
		current.Reset()
	}
	runes := []rune(value)
	for i, r := range runes {
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}
		if isASCIIUpper(r) {
			if current.Len() > 0 && (isASCIILower(prev) || isASCIIDigit(prev) || isASCIIUpper(prev) && isASCIILower(next)) {
				flush()
			}
			current.WriteRune(r)
			prev = r
			continue
		}
		if isASCIILower(r) || isASCIIDigit(r) {
			current.WriteRune(r)
			prev = r
			continue
		}
		flush()
		prev = 0
	}
	flush()
	return tokens
}

func isASCIIUpper(r rune) bool {
	return r >= 'A' && r <= 'Z'
}

func isASCIILower(r rune) bool {
	return r >= 'a' && r <= 'z'
}

func isASCIIDigit(r rune) bool {
	return r >= '0' && r <= '9'
}

func normalizeHttpURL(url string) string {
	if url == "" {
		return ""
	}
	if idx := strings.Index(url, "#"); idx >= 0 {
		if idx == 0 {
			return ""
		}
		url = url[:idx]
	}
	if idx := strings.Index(url, "?"); idx >= 0 {
		url = url[:idx]
	}
	if strings.HasPrefix(url, "http") || strings.HasPrefix(url, "/") {
		return url
	}
	if strings.HasPrefix(url, ":") {
		return "/" + url
	}
	if strings.Contains(url, "/") {
		return "/" + strings.TrimLeft(url, "/")
	}
	return ""
}

// extractUrlFromBinaryExpr extracts URL from concatenation like: API_CONTEXT_PATH + '/projects/' + id
func (p *JavaScriptParser) extractUrlFromBinaryExpr(node *sitter.Node, content []byte) string {
	var parts []string
	p.collectStringParts(node, content, &parts)

	if len(parts) == 0 {
		return ""
	}

	// Join all string parts
	url := strings.Join(parts, "")

	if !strings.HasPrefix(url, "/") && !strings.HasPrefix(url, "http") && !strings.HasPrefix(url, ":") {
		if !strings.Contains(url, "/") {
			return ""
		}
		url = "/" + strings.TrimLeft(url, "/")
	}

	return normalizeHttpURL(url)
}

// collectStringParts recursively collects string literals from binary expressions
func (p *JavaScriptParser) collectStringParts(node *sitter.Node, content []byte, parts *[]string) {
	if node == nil {
		return
	}

	nodeType := node.Type()
	if nodeType == "member_expression" && strings.HasPrefix(node.Content(content), "this.") {
		name := extractMemberPropertyName(node, content)
		for parent := node.Parent(); parent != nil; parent = parent.Parent() {
			if parent.Type() != "class_body" {
				continue
			}
			for i := 0; i < int(parent.NamedChildCount()); i++ {
				field := parent.NamedChild(i)
				if field.Type() != "public_field_definition" && field.Type() != "field_definition" {
					continue
				}
				fieldName, value := classFieldNameAndStringValue(field, content)
				if fieldName == name && (strings.HasPrefix(value, "/") || strings.HasPrefix(value, "http")) {
					*parts = append(*parts, value)
					return
				}
			}
			break
		}
	}

	// String literal - extract and add
	if nodeType == "string" {
		s := strings.Trim(node.Content(content), "\"'`")
		*parts = append(*parts, s)
		return
	}

	// Identifier (variable) - replace with :param placeholder
	if nodeType == "identifier" {
		varName := node.Content(content)
		if p.isURLPrefixVariable(varName) {
			// Skip - these are URL prefixes
			return
		}
		*parts = append(*parts, ":"+varName)
		return
	}

	// Member expression like options.projectId - use the property name
	if nodeType == "member_expression" {
		expr := strings.TrimSpace(node.Content(content))
		if p.isTemplateURLPrefixVariable(expr) {
			return
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child.Type() == "property_identifier" {
				if p.isTemplateURLPrefixVariable(child.Content(content)) {
					return
				}
				*parts = append(*parts, ":"+child.Content(content))
				return
			}
		}
		return
	}

	// Binary expression - recurse into children
	if nodeType == "binary_expression" {
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			// Skip the + operator
			if child.Type() != "+" {
				p.collectStringParts(child, content, parts)
			}
		}
		return
	}

	// For other node types, recurse into children
	for i := 0; i < int(node.ChildCount()); i++ {
		p.collectStringParts(node.Child(i), content, parts)
	}
}

func (p *JavaScriptParser) isURLPrefixVariable(varName string) bool {
	if varName == "" {
		return false
	}

	lowerVar := strings.ToLower(varName)
	normalizedVar := strings.NewReplacer("_", "", "-", "", ".", "", "$", "").Replace(lowerVar)
	for _, cpv := range p.config.ContextPathVariables {
		cpvLower := strings.NewReplacer("_", "", "-", "", ".", "", "$", "").Replace(strings.ToLower(cpv))
		if varName == cpv || normalizedVar == cpvLower || strings.Contains(normalizedVar, cpvLower) {
			return true
		}
	}

	if strings.Contains(normalizedVar, "contextpath") || strings.Contains(normalizedVar, "contextroot") {
		return true
	}
	if normalizedVar == "baseurl" || normalizedVar == "baseuri" {
		return true
	}
	if normalizedVar == "apim" || strings.HasSuffix(normalizedVar, "server") {
		return true
	}
	if strings.HasPrefix(normalizedVar, "api") &&
		(strings.HasSuffix(normalizedVar, "url") || strings.HasSuffix(normalizedVar, "uri") || strings.HasSuffix(normalizedVar, "endpoint")) {
		return true
	}

	return false
}

// findContainingFunction walks up the AST to find the containing function name
func (p *JavaScriptParser) findContainingFunction(node *sitter.Node, content []byte) string {
	current := node.Parent()
	for current != nil {
		nodeType := current.Type()

		// Check for function declaration or expression
		if nodeType == "function_declaration" || nodeType == "function" || nodeType == "function_expression" {
			for i := 0; i < int(current.ChildCount()); i++ {
				child := current.Child(i)
				if child.Type() == "identifier" {
					return child.Content(content)
				}
			}
			if name := p.syntheticCallbackName(current, content); name != "" {
				return name
			}
			if name := p.functionExpressionBindingName(current, content); name != "" {
				return name
			}
			return anonymousFunctionName(current)
		}

		// Check for arrow functions
		if nodeType == "arrow_function" {
			if name := p.syntheticCallbackName(current, content); name != "" {
				return name
			}
			if name := p.functionExpressionBindingName(current, content); name != "" {
				return name
			}
			return anonymousFunctionName(current)
		}

		// Check for method definition
		if nodeType == "method_definition" {
			var methodName string
			for i := 0; i < int(current.ChildCount()); i++ {
				child := current.Child(i)
				if child.Type() == "property_identifier" {
					methodName = child.Content(content)
					break
				}
			}
			if methodName != "" {
				if current.Parent() != nil && current.Parent().Type() == "object" {
					if objectName := p.findEnclosingObjectName(current, content); objectName != "" {
						return objectName + "." + methodName
					}
				}
				if className := p.findEnclosingClassName(current, content); className != "" {
					return className + "." + methodName
				}
				return methodName
			}
		}

		// Check for object literal properties: const api = { foo: () => {} }
		if nodeType == "pair" || nodeType == "property" {
			value := current.ChildByFieldName("value")
			if value == nil || (!isFunctionValueNode(value) && !declaratorValueContainsFunctionForNode(value, node)) {
				current = current.Parent()
				continue
			}
			propName := ""
			for i := 0; i < int(current.ChildCount()); i++ {
				child := current.Child(i)
				switch child.Type() {
				case "property_identifier", "identifier":
					if propName == "" {
						propName = child.Content(content)
					}
				case "string":
					if propName == "" {
						propName = strings.Trim(child.Content(content), "\"'")
					}
				}
			}
			if propName != "" {
				if objectName := p.findEnclosingObjectName(current, content); objectName != "" {
					return objectName + "." + propName
				}
				return propName
			}
		}

		// Check for arrow function assigned to variable
		if nodeType == "variable_declarator" {
			name, value := extractDeclaratorNameAndValue(current, content)
			if name != "" && value != nil {
				if isFunctionValueNode(value) || declaratorValueContainsFunctionForNode(value, node) {
					return name
				}
			}
		}

		if nodeType == "assignment_expression" {
			left, right := assignmentSides(current)
			if left != nil && right != nil && nodeWithin(node, right) && (isFunctionValueNode(right) || declaratorValueContainsFunctionForNode(right, node)) {
				if name := moduleExportsProperty(left, content); name != "" {
					return name
				}
			}
		}

		// Check for class field arrow functions: class Foo { bar = () => {} }
		if nodeType == "public_field_definition" || nodeType == "field_definition" {
			value := current.ChildByFieldName("value")
			if value == nil || (!isFunctionValueNode(value) && !declaratorValueContainsFunctionForNode(value, node)) {
				current = current.Parent()
				continue
			}
			var fieldName string
			for i := 0; i < int(current.ChildCount()); i++ {
				child := current.Child(i)
				switch child.Type() {
				case "property_identifier", "private_property_identifier":
					fieldName = child.Content(content)
				}
			}
			if fieldName != "" {
				if className := p.findEnclosingClassName(current, content); className != "" {
					return className + "." + fieldName
				}
				return fieldName
			}
		}

		current = current.Parent()
	}
	return ""
}

func anonymousFunctionName(node *sitter.Node) string {
	point := node.StartPoint()
	return "_anonymous_L" + strconv.Itoa(int(point.Row)+1) + "_C" + strconv.Itoa(int(point.Column)+1)
}

// Use the same source binding for an anonymous declaration and its body calls.
func (p *JavaScriptParser) functionExpressionBindingName(node *sitter.Node, content []byte) string {
	value := node
	parent := node.Parent()
	for parent != nil && (parent.Type() == "parenthesized_expression" || parent.Type() == "as_expression" || parent.Type() == "satisfies_expression") {
		value, parent = parent, parent.Parent()
	}
	if parent == nil {
		return ""
	}
	switch parent.Type() {
	case "public_field_definition", "field_definition":
		assigned := parent.ChildByFieldName("value")
		name := parent.ChildByFieldName("name")
		if name == nil {
			name = parent.ChildByFieldName("property")
		}
		if name != nil && assigned != nil && assigned.StartByte() == value.StartByte() && assigned.EndByte() == value.EndByte() {
			if className := p.findEnclosingClassName(parent, content); className != "" {
				return className + "." + name.Content(content)
			}
			return name.Content(content)
		}
	case "variable_declarator":
		nameNode, assigned := parent.ChildByFieldName("name"), parent.ChildByFieldName("value")
		name := ""
		if nameNode != nil && nameNode.Type() == "identifier" {
			name = nameNode.Content(content)
		}
		if assigned != nil && assigned.StartByte() == value.StartByte() && assigned.EndByte() == value.EndByte() {
			return name
		}
	case "pair", "property":
		assigned := parent.ChildByFieldName("value")
		if assigned == nil || assigned.StartByte() != value.StartByte() || assigned.EndByte() != value.EndByte() {
			return ""
		}
		name := extractPairKeyName(parent, content)
		if name != "" {
			if objectName := p.findEnclosingObjectName(parent, content); objectName != "" {
				return objectName + "." + name
			}
			return name
		}
	case "assignment_expression":
		left, right := assignmentSides(parent)
		if left == nil || right == nil || right.StartByte() != value.StartByte() || right.EndByte() != value.EndByte() {
			return ""
		}
		if name := moduleExportsProperty(left, content); name != "" {
			return name
		}
		if left.Type() == "identifier" || left.Type() == "member_expression" {
			return left.Content(content)
		}
	}
	return ""
}

func nodeWithin(node, parent *sitter.Node) bool {
	if node == nil || parent == nil {
		return false
	}
	return node.StartByte() >= parent.StartByte() && node.EndByte() <= parent.EndByte()
}

func isFunctionValueNode(node *sitter.Node) bool {
	if node == nil {
		return false
	}
	switch node.Type() {
	case "function", "function_declaration", "function_expression", "arrow_function", "generator_function":
		return true
	default:
		return false
	}
}

func declaratorValueContainsFunctionForNode(value, target *sitter.Node) bool {
	if value == nil || target == nil {
		return false
	}
	var found bool
	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil || found {
			return
		}
		if isFunctionValueNode(node) && nodeWithin(target, node) {
			found = true
			return
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			walk(node.Child(i))
		}
	}
	walk(value)
	return found
}

func unwrapAwaitExpression(node *sitter.Node) *sitter.Node {
	if node == nil || node.Type() != "await_expression" {
		return node
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil || !child.IsNamed() {
			continue
		}
		return child
	}
	return node
}

// findContainingCallableScope resolves the nearest enclosing callable boundary and
// avoids attributing inner expressions to local variable names inside that scope.
func (p *JavaScriptParser) findContainingCallableScope(node *sitter.Node, content []byte) string {
	return p.findContainingFunction(node, content)
}

func (p *JavaScriptParser) extractAnonymousCallback(node *sitter.Node, content []byte, lines []string) *ParsedFunction {
	if node == nil {
		return nil
	}
	switch node.Type() {
	case "function", "function_expression", "arrow_function":
	default:
		return nil
	}
	if node.Type() != "arrow_function" {
		for i := 0; i < int(node.ChildCount()); i++ {
			if node.Child(i).Type() == "identifier" {
				return nil
			}
		}
	}

	name := p.syntheticCallbackName(node, content)
	if name == "" {
		name = p.functionExpressionBindingName(node, content)
	}
	if name == "" {
		name = anonymousFunctionName(node)
	}

	fn := p.buildObjectLiteralFunction("", name, node, content, lines)
	fn.SourceCode = node.Content(content)
	value := node
	parent := node.Parent()
	for parent != nil && (parent.Type() == "parenthesized_expression" || parent.Type() == "as_expression" || parent.Type() == "satisfies_expression") {
		value, parent = parent, parent.Parent()
	}
	if parent != nil && (parent.Type() == "variable_declarator" || parent.Type() == "public_field_definition" || parent.Type() == "field_definition") {
		assigned := parent.ChildByFieldName("value")
		if assigned != nil && assigned.StartByte() == value.StartByte() && assigned.EndByte() == value.EndByte() {
			fn.StartLine = int(parent.StartPoint().Row) + 1
			fn.EndLine = int(parent.EndPoint().Row) + 1
			fn.SourceCode = parent.Content(content)
			if annotation := parent.ChildByFieldName("type"); annotation != nil && fn.ReturnType == "" {
				fn.ReturnType = p.extractTypeAnnotation(annotation, content)
			}
		}
	}
	return &fn
}

// Only callback positions recognized by the supported API models are invocation
// edges. Other callable arguments remain speculative discovery connections.
func (p *JavaScriptParser) connectInlineFunctions(node *sitter.Node, result *ParsedFile, content []byte, lines []string) {
	owner := p.findContainingFunction(node, content)
	if owner == "" {
		owner = "_module_"
	}
	connect := func(value *sitter.Node, callbackArgument bool) {
		for value != nil && (value.Type() == "parenthesized_expression" || value.Type() == "as_expression" || value.Type() == "satisfies_expression") {
			value = value.NamedChild(0)
		}
		if value == nil {
			return
		}
		var fn *ParsedFunction
		switch value.Type() {
		case "arrow_function":
			fn = p.extractAnonymousCallback(value, content, lines)
		case "function", "function_expression":
			fn = p.extractFunction(value, content, lines)
		default:
			return
		}
		if fn == nil {
			return
		}
		line := int(node.StartPoint().Row) + 1
		for _, call := range result.FunctionCalls[owner] {
			if call.CalleeName == fn.Name && call.LineNumber == line && call.IsCallbackArgument == callbackArgument {
				return
			}
		}
		result.FunctionCalls[owner] = append(result.FunctionCalls[owner], ParsedFunctionCall{CalleeName: fn.Name, LineNumber: line, IsCallbackArgument: callbackArgument})
	}
	connect(node.ChildByFieldName("function"), false)
	if args := node.ChildByFieldName("arguments"); args != nil {
		for i := 0; i < int(args.NamedChildCount()); i++ {
			argument := args.NamedChild(i)
			connect(argument, !p.isSupportedCallbackArgument(node, argument, content))
		}
	}
}

func (p *JavaScriptParser) isSupportedCallbackArgument(node, argument *sitter.Node, content []byte) bool {
	if node == nil || argument == nil {
		return false
	}
	matches := func(candidate *sitter.Node) bool {
		return candidate != nil && candidate.StartByte() == argument.StartByte() && candidate.EndByte() == argument.EndByte()
	}
	if node.Type() == "new_expression" {
		return p.isSupportedCallbackNewExpression(node, content) && matches(p.extractArgumentNode(node, 0))
	}
	method, receiver := p.extractCallMethod(node, content)
	if !p.isSupportedCallbackCallWithMethod(node, method, receiver, content) {
		return false
	}
	if isJQueryCall(receiver, method) || p.isJQueryCallExpression(node, content) {
		return matches(p.findCallbackArg(node, method))
	}
	switch {
	case isTimerCallbackMethod(method), method == "catch", method == "finally":
		return matches(p.extractArgumentNode(node, 0))
	case method == "then":
		return matches(p.extractArgumentNode(node, 0)) || matches(p.extractArgumentNode(node, 1))
	case isDomEventListenerMethod(method), isEventEmitterMethod(method):
		return matches(p.extractArgumentNode(node, 1))
	default:
		return matches(p.findCallbackArg(node, method))
	}
}

func (p *JavaScriptParser) syntheticCallbackName(fnNode *sitter.Node, content []byte) string {
	callNode, propName := p.findCallbackCallExpression(fnNode, content)
	if callNode != nil {
		if name := p.syntheticCallbackNameForCall(fnNode, callNode, propName, content); name != "" {
			return name
		}
	}

	if newNode := p.findCallbackNewExpression(fnNode); newNode != nil {
		if name := p.syntheticCallbackNameForNewExpression(fnNode, newNode, content); name != "" {
			return name
		}
	}

	return ""
}

func (p *JavaScriptParser) findCallbackCallExpression(fnNode *sitter.Node, content []byte) (*sitter.Node, string) {
	current := fnNode.Parent()
	for current != nil {
		switch current.Type() {
		case "function", "function_expression", "arrow_function", "function_declaration", "method_definition", "variable_declarator", "statement_block":
			return nil, ""
		case "call_expression":
			return current, ""
		case "arguments":
			parent := current.Parent()
			if parent != nil && parent.Type() == "call_expression" {
				return parent, ""
			}
		case "pair":
			propName := extractPairKeyName(current, content)
			if propName == "" {
				break
			}
			obj := current.Parent()
			for obj != nil && obj.Type() != "object" {
				obj = obj.Parent()
			}
			if obj == nil {
				break
			}
			args := obj.Parent()
			for args != nil && args.Type() != "arguments" {
				args = args.Parent()
			}
			if args != nil && args.Parent() != nil && args.Parent().Type() == "call_expression" {
				return args.Parent(), propName
			}
		}
		current = current.Parent()
	}
	return nil, ""
}

func (p *JavaScriptParser) syntheticCallbackNameForCall(fnNode *sitter.Node, callNode *sitter.Node, propName string, content []byte) string {
	if callNode == nil || fnNode == nil {
		return ""
	}

	methodName, receiver := p.extractCallMethod(callNode, content)
	if !p.isSupportedCallbackCallWithMethod(callNode, methodName, receiver, content) {
		return ""
	}
	if propName == "" && !p.isSupportedCallbackArgument(callNode, fnNode, content) {
		return ""
	}

	startLine := int(fnNode.StartPoint().Row) + 1

	if p.isJQueryCallExpression(callNode, content) || isJQueryCall(receiver, methodName) {
		label := methodName
		if methodName == "$" || methodName == "jQuery" {
			label = "ready"
		}
		if label == "" {
			label = "callback"
		}
		if propName != "" {
			label = label + "_" + propName
		}
		if event := p.extractFirstStringArgument(callNode, content); event != "" {
			if methodName == "on" || methodName == "bind" {
				label = label + "_" + sanitizeCallbackLabel(event)
			}
		}
		selectorLabel := ""
		switch methodName {
		case "on", "bind":
			selectorLabel = p.extractStringArgumentAt(callNode, content, 1)
		case "delegate":
			selectorLabel = p.extractStringArgumentAt(callNode, content, 0)
		}
		if selectorLabel == "" {
			selectorLabel = p.extractJQuerySelectorLabel(callNode, content)
		}
		if selectorLabel != "" {
			label = label + "_" + sanitizeCallbackLabel(selectorLabel)
		}
		label = sanitizeCallbackLabel(label)
		return "jquery." + label + "_L" + strconv.Itoa(startLine)
	}

	if isDomEventListenerMethod(methodName) {
		label := methodName
		if event := p.extractFirstStringArgument(callNode, content); event != "" {
			label = event
		}
		label = sanitizeCallbackLabel(label)
		return "dom." + label + "_L" + strconv.Itoa(startLine)
	}

	if isEventEmitterMethod(methodName) {
		label := methodName
		if event := p.extractFirstStringArgument(callNode, content); event != "" {
			label = methodName + "_" + sanitizeCallbackLabel(event)
		}
		label = sanitizeCallbackLabel(label)
		return "event." + label + "_L" + strconv.Itoa(startLine)
	}

	if isPromiseCallbackMethod(methodName) {
		label := sanitizeCallbackLabel(methodName)
		return "promise." + label + "_L" + strconv.Itoa(startLine)
	}

	if isTimerCallbackMethod(methodName) {
		label := sanitizeCallbackLabel(methodName)
		return "timer." + label + "_L" + strconv.Itoa(startLine)
	}

	return ""
}

func (p *JavaScriptParser) syntheticCallbackNameForNewExpression(fnNode *sitter.Node, newNode *sitter.Node, content []byte) string {
	if fnNode == nil || newNode == nil || newNode.Type() != "new_expression" {
		return ""
	}

	if !p.isSupportedCallbackArgument(newNode, fnNode, content) {
		return ""
	}

	ctor := newNode.ChildByFieldName("constructor")
	if ctor == nil && newNode.ChildCount() > 0 {
		ctor = newNode.Child(0)
	}
	if ctor == nil {
		return ""
	}

	ctorName := ""
	switch ctor.Type() {
	case "identifier":
		ctorName = ctor.Content(content)
	case "member_expression", "optional_member_expression":
		if prop := extractMemberPropertyName(ctor, content); prop != "" {
			ctorName = prop
		}
	}
	if ctorName != "Promise" {
		return ""
	}

	startLine := int(fnNode.StartPoint().Row) + 1
	return "promise.new_L" + strconv.Itoa(startLine)
}

func (p *JavaScriptParser) findCallbackNewExpression(fnNode *sitter.Node) *sitter.Node {
	current := fnNode.Parent()
	for current != nil {
		switch current.Type() {
		case "function", "function_expression", "arrow_function", "function_declaration", "method_definition", "variable_declarator", "statement_block", "call_expression":
			return nil
		case "new_expression":
			return current
		case "arguments":
			parent := current.Parent()
			if parent != nil && parent.Type() == "new_expression" {
				return parent
			}
		}
		current = current.Parent()
	}
	return nil
}

func (p *JavaScriptParser) extractCallMethod(callNode *sitter.Node, content []byte) (string, string) {
	if callNode == nil || callNode.ChildCount() == 0 {
		return "", ""
	}
	funcNode := callNode.Child(0)
	switch funcNode.Type() {
	case "member_expression", "optional_member_expression":
		receiver, methodName := p.extractReceiverAndMethod(funcNode, content)
		return methodName, receiver
	case "subscript_expression", "optional_subscript_expression":
		receiver, methodName, _ := p.extractSubscriptReceiverAndMethod(funcNode, content)
		return methodName, receiver
	case "identifier":
		name := funcNode.Content(content)
		return name, name
	default:
		return funcNode.Content(content), ""
	}
}

func (p *JavaScriptParser) extractFirstStringArgument(callNode *sitter.Node, content []byte) string {
	for i := 0; i < int(callNode.ChildCount()); i++ {
		child := callNode.Child(i)
		if child.Type() != "arguments" {
			continue
		}
		for j := 0; j < int(child.ChildCount()); j++ {
			arg := child.Child(j)
			if arg.Type() == "string" {
				return strings.Trim(arg.Content(content), "\"'`")
			}
			if arg.Type() == "template_string" {
				return strings.Trim(arg.Content(content), "`")
			}
		}
	}
	return ""
}

func (p *JavaScriptParser) extractArgumentNode(callNode *sitter.Node, index int) *sitter.Node {
	if callNode == nil || index < 0 {
		return nil
	}
	for i := 0; i < int(callNode.ChildCount()); i++ {
		child := callNode.Child(i)
		if child.Type() != "arguments" {
			continue
		}
		argIndex := 0
		for j := 0; j < int(child.ChildCount()); j++ {
			arg := child.Child(j)
			if !isArgumentNodeType(arg.Type()) {
				continue
			}
			if argIndex == index {
				return arg
			}
			argIndex++
		}
	}
	return nil
}

func (p *JavaScriptParser) extractStringArgumentAt(callNode *sitter.Node, content []byte, index int) string {
	arg := p.extractArgumentNode(callNode, index)
	if arg == nil {
		return ""
	}
	switch arg.Type() {
	case "string":
		return strings.Trim(arg.Content(content), "\"'`")
	case "template_string":
		return strings.Trim(arg.Content(content), "`")
	default:
		return ""
	}
}

func isArgumentNodeType(nodeType string) bool {
	switch nodeType {
	case "(", ")", ",", "comment":
		return false
	default:
		return true
	}
}

func extractPairKeyName(node *sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "property_identifier":
			return child.Content(content)
		case "string":
			return strings.Trim(child.Content(content), "\"'`")
		}
	}
	return ""
}

func isJQueryCall(receiver, methodName string) bool {
	receiverLower := strings.ToLower(receiver)
	if strings.Contains(receiverLower, "jquery") || strings.Contains(receiver, "$") {
		return true
	}
	if methodName == "$" || methodName == "jQuery" {
		return true
	}
	return false
}

func (p *JavaScriptParser) extractJQuerySelectorLabel(callNode *sitter.Node, content []byte) string {
	if callNode == nil {
		return ""
	}
	funcNode := callNode.ChildByFieldName("function")
	if funcNode == nil && callNode.ChildCount() > 0 {
		funcNode = callNode.Child(0)
	}
	if funcNode == nil {
		return ""
	}

	switch funcNode.Type() {
	case "member_expression", "optional_member_expression", "subscript_expression", "optional_subscript_expression":
		if obj := funcNode.ChildByFieldName("object"); obj != nil {
			return p.extractJQuerySelectorFromNode(obj, content)
		}
	}
	return ""
}

func (p *JavaScriptParser) extractJQuerySelectorFromNode(node *sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}

	switch node.Type() {
	case "call_expression":
		methodName, receiver := p.extractCallMethod(node, content)
		if methodName == "$" || methodName == "jQuery" || isJQueryCall(receiver, methodName) {
			if selector := p.extractStringArgumentAt(node, content, 0); selector != "" {
				return selector
			}
			if arg := p.extractArgumentNode(node, 0); arg != nil {
				switch arg.Type() {
				case "identifier", "member_expression", "this":
					return arg.Content(content)
				}
			}
		}
		funcNode := node.ChildByFieldName("function")
		if funcNode == nil && node.ChildCount() > 0 {
			funcNode = node.Child(0)
		}
		if funcNode != nil {
			switch funcNode.Type() {
			case "member_expression", "optional_member_expression", "subscript_expression", "optional_subscript_expression":
				if obj := funcNode.ChildByFieldName("object"); obj != nil {
					return p.extractJQuerySelectorFromNode(obj, content)
				}
			}
		}
	case "member_expression", "optional_member_expression":
		if obj := node.ChildByFieldName("object"); obj != nil {
			return p.extractJQuerySelectorFromNode(obj, content)
		}
	case "identifier", "this":
		return node.Content(content)
	}

	return ""
}

func (p *JavaScriptParser) isJQueryCallExpression(node *sitter.Node, content []byte) bool {
	if node == nil || node.Type() != "call_expression" {
		return false
	}

	methodName, receiver := p.extractCallMethod(node, content)
	if isJQueryCall(receiver, methodName) {
		return true
	}

	funcNode := node.ChildByFieldName("function")
	if funcNode == nil && node.ChildCount() > 0 {
		funcNode = node.Child(0)
	}
	if funcNode == nil {
		return false
	}

	switch funcNode.Type() {
	case "member_expression", "optional_member_expression", "subscript_expression", "optional_subscript_expression":
		if obj := funcNode.ChildByFieldName("object"); obj != nil && obj.Type() == "call_expression" {
			return p.isJQueryCallExpression(obj, content)
		}
	case "call_expression":
		return p.isJQueryCallExpression(funcNode, content)
	}

	return false
}

func (p *JavaScriptParser) isSupportedCallbackCall(node *sitter.Node, content []byte) bool {
	if node == nil || node.Type() != "call_expression" {
		return false
	}
	methodName, receiver := p.extractCallMethod(node, content)
	return p.isSupportedCallbackCallWithMethod(node, methodName, receiver, content)
}

func (p *JavaScriptParser) isSupportedCallbackCallWithMethod(node *sitter.Node, methodName, receiver string, content []byte) bool {
	if node == nil || node.Type() != "call_expression" {
		return false
	}
	if isJQueryCall(receiver, methodName) {
		return true
	}
	if methodName == "" && receiver == "" {
		if p.isJQueryCallExpression(node, content) {
			return true
		}
	}
	if methodName == "" {
		return false
	}
	if isDomEventListenerMethod(methodName) || isEventEmitterMethod(methodName) ||
		isPromiseCallbackMethod(methodName) || isTimerCallbackMethod(methodName) {
		return true
	}
	return false
}

func (p *JavaScriptParser) isSupportedCallbackNewExpression(node *sitter.Node, content []byte) bool {
	if node == nil || node.Type() != "new_expression" {
		return false
	}
	ctor := node.ChildByFieldName("constructor")
	if ctor == nil && node.ChildCount() > 0 {
		ctor = node.Child(0)
	}
	if ctor == nil {
		return false
	}

	switch ctor.Type() {
	case "identifier":
		return ctor.Content(content) == "Promise"
	case "member_expression", "optional_member_expression":
		return extractMemberPropertyName(ctor, content) == "Promise"
	default:
		return false
	}
}

func isDomEventListenerMethod(methodName string) bool {
	switch methodName {
	case "addEventListener":
		return true
	default:
		return false
	}
}

func isCallbackReferenceNode(node *sitter.Node) bool {
	if node == nil {
		return false
	}
	switch node.Type() {
	case "identifier", "member_expression", "optional_member_expression":
		return true
	default:
		return false
	}
}

func isEventEmitterMethod(methodName string) bool {
	switch methodName {
	case "on", "once", "addListener":
		return true
	default:
		return false
	}
}

func isPromiseCallbackMethod(methodName string) bool {
	switch methodName {
	case "then", "catch", "finally":
		return true
	default:
		return false
	}
}

func isTimerCallbackMethod(methodName string) bool {
	switch methodName {
	case "setTimeout", "setInterval", "requestAnimationFrame":
		return true
	default:
		return false
	}
}

func (p *JavaScriptParser) isJQueryFluentCall(node *sitter.Node, content []byte) bool {
	if node == nil || node.Type() != "call_expression" {
		return false
	}
	methodName, receiver := p.extractCallMethod(node, content)
	return p.isJQueryFluentCallWithMethod(node, content, methodName, receiver)
}

func (p *JavaScriptParser) isJQueryFluentCallWithMethod(node *sitter.Node, content []byte, methodName, receiver string) bool {
	if node == nil || node.Type() != "call_expression" {
		return false
	}
	if isJQueryCall(receiver, methodName) {
		return true
	}
	if receiver == "" {
		return p.isJQueryCallExpression(node, content)
	}
	return false
}

func sanitizeCallbackLabel(label string) string {
	if label == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(label))
	for _, r := range label {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

type parsedCallbackEdge struct {
	call ParsedFunctionCall
	node *sitter.Node
}

func (p *JavaScriptParser) extractCallbackEdges(node *sitter.Node, content []byte, methodName, receiver string) []parsedCallbackEdge {
	if node == nil || node.Type() != "call_expression" {
		return nil
	}

	if !p.isSupportedCallbackCallWithMethod(node, methodName, receiver, content) {
		return nil
	}

	nodes := []*sitter.Node{p.findCallbackArg(node, methodName)}
	if methodName == "then" {
		failure := p.extractArgumentNode(node, 1)
		if failure != nil && (nodes[0] == nil || failure.StartByte() != nodes[0].StartByte()) {
			nodes = append(nodes, failure)
		}
	}
	var edges []parsedCallbackEdge
	for _, callbackNode := range nodes {
		if callbackNode == nil || !p.isSupportedCallbackArgument(node, callbackNode, content) {
			continue
		}
		switch callbackNode.Type() {
		case "function", "function_expression", "arrow_function", "identifier", "member_expression", "optional_member_expression":
		default:
			continue
		}
		callbackName := p.syntheticCallbackName(callbackNode, content)
		if callbackNode.Type() == "function" || callbackNode.Type() == "function_expression" {
			if name := callbackNode.ChildByFieldName("name"); name != nil {
				callbackName = name.Content(content)
			}
		}
		if callbackName != "" {
			edges = append(edges, parsedCallbackEdge{
				call: ParsedFunctionCall{CalleeName: callbackName, LineNumber: int(node.StartPoint().Row) + 1},
				node: callbackNode,
			})
		}
	}
	return edges
}

func (p *JavaScriptParser) extractCallbackReferenceCall(callbackNode *sitter.Node, content []byte, funcName string, callNode *sitter.Node) *ParsedFunctionCall {
	if callbackNode == nil || !p.isSupportedCallbackArgument(callNode, callbackNode, content) {
		return nil
	}

	var call ParsedFunctionCall
	switch callbackNode.Type() {
	case "identifier":
		name := callbackNode.Content(content)
		if name == "" || name == "undefined" || name == "null" {
			return nil
		}
		call = ParsedFunctionCall{
			CalleeName: name,
			MethodName: name,
			LineNumber: int(callbackNode.StartPoint().Row) + 1,
			IsAsync:    false,
		}
	case "member_expression", "optional_member_expression":
		calleeName := callbackNode.Content(content)
		if calleeName == "" {
			return nil
		}
		receiver, methodName := p.extractReceiverAndMethod(callbackNode, content)
		call = ParsedFunctionCall{
			CalleeName: calleeName,
			Receiver:   receiver,
			MethodName: methodName,
			LineNumber: int(callbackNode.StartPoint().Row) + 1,
			IsAsync:    false,
		}
	default:
		return nil
	}

	p.normalizeCallName(&call, funcName, callNode, content)
	return &call
}

func (p *JavaScriptParser) addSyntheticCallbackFunction(result *ParsedFile, name string, node *sitter.Node, lines []string) {
	if result == nil || name == "" || node == nil {
		return
	}
	for _, fn := range result.Functions {
		if fn.Name == name {
			return
		}
	}
	startLine := int(node.StartPoint().Row) + 1
	endLine := startLine
	sourceCode := getSourceCode(lines, startLine, endLine)
	result.Functions = append(result.Functions, ParsedFunction{
		Name:       name,
		StartLine:  startLine,
		EndLine:    endLine,
		IsExported: false,
		IsAsync:    false,
		SourceCode: sourceCode,
	})
}

func (p *JavaScriptParser) findCallbackArg(node *sitter.Node, methodName string) *sitter.Node {
	if node == nil || node.Type() != "call_expression" {
		return nil
	}
	if isTimerCallbackMethod(methodName) || isPromiseCallbackMethod(methodName) {
		positions := []int{0}
		if methodName == "then" {
			positions = append(positions, 1)
		}
		for _, position := range positions {
			arg := p.extractArgumentNode(node, position)
			if arg == nil {
				continue
			}
			switch arg.Type() {
			case "function", "function_expression", "arrow_function", "identifier", "member_expression", "optional_member_expression":
				return arg
			}
		}
		return nil
	}
	var args []*sitter.Node
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() != "arguments" {
			continue
		}
		for j := 0; j < int(child.ChildCount()); j++ {
			arg := child.Child(j)
			if !isArgumentNodeType(arg.Type()) {
				continue
			}
			args = append(args, arg)
		}
	}
	for _, arg := range args {
		switch arg.Type() {
		case "function", "function_expression", "arrow_function":
			return arg
		}
	}
	if methodName == "$" || methodName == "jQuery" {
		return nil
	}
	if methodName == "addEventListener" && len(args) > 1 {
		if isCallbackReferenceNode(args[1]) {
			return args[1]
		}
	}
	if isEventEmitterMethod(methodName) && len(args) > 1 {
		if isCallbackReferenceNode(args[1]) {
			return args[1]
		}
	}
	for i := len(args) - 1; i >= 0; i-- {
		if isCallbackReferenceNode(args[i]) {
			return args[i]
		}
	}
	return nil
}

func (p *JavaScriptParser) isJQueryCallbackChainCall(node *sitter.Node, content []byte) bool {
	if node == nil || node.Type() != "call_expression" {
		return false
	}
	for current := node.Parent(); current != nil; current = current.Parent() {
		if current.Type() != "call_expression" {
			continue
		}
		if !p.isJQueryCallExpression(current, content) {
			continue
		}
		methodName, _ := p.extractCallMethod(current, content)
		if p.findCallbackArg(current, methodName) == nil {
			continue
		}
		funcNode := current.ChildByFieldName("function")
		if funcNode == nil && current.ChildCount() > 0 {
			funcNode = current.Child(0)
		}
		if funcNode != nil && isDescendantNode(funcNode, node) {
			return true
		}
	}
	return false
}

func isDescendantNode(root *sitter.Node, target *sitter.Node) bool {
	if root == nil || target == nil {
		return false
	}
	if root == target {
		return true
	}
	for i := 0; i < int(root.ChildCount()); i++ {
		if isDescendantNode(root.Child(i), target) {
			return true
		}
	}
	return false
}

func (p *JavaScriptParser) findEnclosingClassName(node *sitter.Node, content []byte) string {
	for current := node.Parent(); current != nil; current = current.Parent() {
		if current.Type() == "class_declaration" || current.Type() == "class" {
			for i := 0; i < int(current.ChildCount()); i++ {
				child := current.Child(i)
				if child.Type() == "identifier" || child.Type() == "type_identifier" {
					return child.Content(content)
				}
			}
			if name := p.findClassNameFromParent(current, content); name != "" {
				return name
			}
		}
	}
	return ""
}

func (p *JavaScriptParser) findClassNameFromParent(node *sitter.Node, content []byte) string {
	for current := node.Parent(); current != nil; current = current.Parent() {
		switch current.Type() {
		case "variable_declarator":
			for i := 0; i < int(current.ChildCount()); i++ {
				child := current.Child(i)
				if child.Type() == "identifier" {
					return child.Content(content)
				}
			}
		case "assignment_expression":
			if current.ChildCount() > 0 {
				left := current.Child(0)
				switch left.Type() {
				case "identifier", "member_expression":
					return left.Content(content)
				}
			}
		}
	}
	return ""
}

func (p *JavaScriptParser) findEnclosingObjectName(node *sitter.Node, content []byte) string {
	var parts []string
	for current := node.Parent(); current != nil; current = current.Parent() {
		switch current.Type() {
		case "pair", "property":
			if name := p.extractPropertyKeyName(current, content); name != "" {
				parts = append([]string{name}, parts...)
			}
		case "object":
			if name := p.extractObjectLiteralDeclaredName(current, content); name != "" {
				if len(parts) == 0 {
					return name
				}
				return name + "." + strings.Join(parts, ".")
			}
		case "variable_declarator":
			for i := 0; i < int(current.ChildCount()); i++ {
				child := current.Child(i)
				if child.Type() == "identifier" {
					name := child.Content(content)
					if len(parts) == 0 {
						return name
					}
					return name + "." + strings.Join(parts, ".")
				}
			}
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, ".")
	}
	return ""
}

func (p *JavaScriptParser) extractPropertyKeyName(node *sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "property_identifier", "identifier":
			return child.Content(content)
		case "string":
			return strings.Trim(child.Content(content), "\"'`")
		}
	}
	return ""
}
