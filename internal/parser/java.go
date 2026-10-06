package parser

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sergiumoraru/tirion/internal/config"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/java"
)

type JavaParser struct {
	parser       *sitter.Parser
	lang         *sitter.Language
	config       *config.PatternsConfig
	sqsQueueVars map[sqsQueueBindingKey]string
	// interfaceRoles caches the HTTP role of each interface declaration in the
	// file being parsed, keyed by the declaration's start byte.
	interfaceRoles map[uint32]javaInterfaceHTTPRole
}

func NewJavaParser() *JavaParser {
	lang := java.GetLanguage()
	parser := sitter.NewParser()
	if lang != nil {
		parser.SetLanguage(lang)
	}
	return &JavaParser{
		parser: parser,
		lang:   lang,
		config: config.GetEffectivePatterns(),
	}
}

// SetConfig sets custom patterns configuration
func (p *JavaParser) SetConfig(cfg *config.PatternsConfig) {
	p.config = cfg
}

func (p *JavaParser) CanParse(filePath string) bool {
	ext := strings.ToLower(filepath.Ext(filePath))
	return ext == ".java"
}

func (p *JavaParser) ParseFile(filePath string, content []byte) (result ParsedFile) {
	result = ParsedFile{
		Path:                 filePath,
		Language:             "java",
		Functions:            []ParsedFunction{},
		Classes:              []ParsedClass{},
		Imports:              []ParsedImport{},
		Endpoints:            []ParsedEndpoint{},
		FunctionCalls:        make(map[string][]ParsedFunctionCall),
		LocalVarTypes:        make(map[string]map[string]string),
		HttpCalls:            make(map[string][]ParsedHttpCall),
		DataAccesses:         make(map[string][]ParsedDataAccess),
		SqlStatements:        make(map[string][]ParsedSqlStatementCall),
		SqsProducers:         make(map[string][]ParsedSqsProducer),
		SqsConsumers:         []ParsedSqsConsumer{},
		Interfaces:           []ParsedInterface{},
		HttpInterfaceMethods: []ParsedHttpInterfaceMethod{},
		BeanDefinitions:      []ParsedBeanDefinition{},
		EventListeners:       []ParsedEventListener{},
		ScheduledMethods:     []ParsedScheduledMethod{},
		JpaEntities:          []ParsedJpaEntity{},
		JpaRelationships:     []ParsedJpaRelationship{},
		SyntheticMethods:     []ParsedSyntheticMethod{},
		Lambdas:              []ParsedLambda{},
	}
	defer recoverParsePanic(&result)
	if err := p.config.Err(); err != nil {
		result.ParseDiagnostics = ParseDiagnostics{FailureKind: ParseFailureInternal, Message: err.Error()}
		return result
	}

	if p.lang == nil {
		return result
	}

	tree, diagnostics := parseWithTimeout(p.parser, content)
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
	p.sqsQueueVars = p.extractSqsQueueVarMap(root, content)
	defer func() { p.sqsQueueVars = nil }()
	p.interfaceRoles = map[uint32]javaInterfaceHTTPRole{}
	defer func() { p.interfaceRoles = nil }()
	p.extractNodes(root, content, &result, "", "", nil, nil)
	result.SqsConsumers = append(result.SqsConsumers, p.extractSqsConsumersFromQueueURLAssignments(root, content, result.Classes)...)
	result.SqsConsumers = p.filterSqsConsumersToConfiguredTypes(result.SqsConsumers, result.Classes)

	// Extract Spring framework patterns from parsed classes and functions
	result.BeanDefinitions = p.extractBeanDefinitions(result.Classes, result.Functions)
	result.EventListeners = p.extractEventListeners(result.Functions)
	result.ScheduledMethods = p.extractScheduledMethods(result.Functions)

	// Extract JPA entities and relationships from parsed classes
	result.JpaEntities, result.JpaRelationships = p.extractJpaPatterns(result.Classes)

	// Extract Lombok synthetic methods from parsed classes
	result.SyntheticMethods = append(result.SyntheticMethods, p.extractLombokMethods(result.Classes)...)

	return result
}

func (p *JavaParser) extractNodes(node *sitter.Node, content []byte, result *ParsedFile, currentClass, currentMethod string, currentClassPaths, currentClassMethods []string) {
	nodeType := node.Type()

	switch nodeType {
	case "package_declaration":
		for i := 0; i < int(node.NamedChildCount()); i++ {
			child := node.NamedChild(i)
			if child.Type() == "identifier" || child.Type() == "scoped_identifier" {
				result.JavaPackage = strings.Join(strings.Fields(child.Content(content)), "")
			}
		}
	case "import_declaration":
		p.extractImport(node, content, result)

	case "interface_declaration":
		p.extractInterface(node, content, result)

	case "class_declaration", "enum_declaration":
		p.extractClass(node, content, result, nodeType == "enum_declaration")

	case "record_declaration":
		p.extractRecord(node, content, result)

	case "method_declaration":
		p.extractMethod(node, content, result, currentClass)
		p.extractEndpoint(node, content, result, currentClass, currentClassPaths, currentClassMethods)
		if methodName := p.getMethodName(node, content, currentClass); methodName != "" {
			for _, httpCall := range p.extractFluentHttpCalls(node, content) {
				result.HttpCalls[methodName] = append(result.HttpCalls[methodName], httpCall)
			}
			for _, httpCall := range p.extractHttpURLConnectionCalls(node, content) {
				result.HttpCalls[methodName] = append(result.HttpCalls[methodName], httpCall)
			}
		}

	case "method_invocation":
		// Track method calls for the current method context
		if currentMethod != "" {
			p.extractMethodCall(node, content, result, currentMethod)
		}
		// Extract HTTP client calls (RestTemplate, WebClient, HttpClient)
		if httpCall := p.extractHttpCall(node, content); httpCall != nil {
			funcName := currentMethod
			if funcName == "" {
				funcName = "_module_"
			}
			result.HttpCalls[funcName] = append(result.HttpCalls[funcName], *httpCall)
		}
		// Extract SQS sendMessage calls
		if sqsProducer := p.extractSqsProducer(node, content); sqsProducer != nil {
			funcName := currentMethod
			if funcName == "" {
				funcName = "_module_"
			}
			result.SqsProducers[funcName] = append(result.SqsProducers[funcName], *sqsProducer)
		}
		// Extract DB reads/writes via SqlMapClient/SqlSession statement IDs and Criteria API.
		// Compute invocation method name once to avoid repeating node scans.
		invokedMethod := methodInvocationName(node, content)
		if stmt := p.extractSqlStatementCall(invokedMethod, node, content); stmt != nil {
			funcName := currentMethod
			if funcName == "" {
				funcName = "_module_"
			}
			result.SqlStatements[funcName] = append(result.SqlStatements[funcName], *stmt)
		}
		if access := p.extractCriteriaDataAccess(invokedMethod, node, content); access != nil {
			funcName := currentMethod
			if funcName == "" {
				funcName = "_module_"
			}
			result.DataAccesses[funcName] = append(result.DataAccesses[funcName], *access)
		}

	case "constructor_declaration":
		// Extract consumers from the configured queue annotation.
		sqsConsumers := p.extractSqsConsumers(node, content, currentClass)
		result.SqsConsumers = append(result.SqsConsumers, sqsConsumers...)

	case "local_variable_declaration":
		if currentMethod != "" {
			typeName, varNames := p.extractLocalVariableDeclaration(node, content)
			if typeName != "" && len(varNames) > 0 {
				if result.LocalVarTypes == nil {
					result.LocalVarTypes = make(map[string]map[string]string)
				}
				if result.LocalVarTypes[currentMethod] == nil {
					result.LocalVarTypes[currentMethod] = make(map[string]string)
				}
				for _, name := range varNames {
					if name != "" {
						result.LocalVarTypes[currentMethod][name] = typeName
					}
				}
			}
		}

	case "enhanced_for_statement":
		if currentMethod != "" {
			typeName, varName := p.extractEnhancedForVariable(node, content)
			if typeName != "" && varName != "" {
				if result.LocalVarTypes == nil {
					result.LocalVarTypes = make(map[string]map[string]string)
				}
				if result.LocalVarTypes[currentMethod] == nil {
					result.LocalVarTypes[currentMethod] = make(map[string]string)
				}
				result.LocalVarTypes[currentMethod][varName] = typeName
			}
		}

	case "lambda_expression":
		// Track lambda expressions and calls within them
		if lambda := p.extractLambda(node, content, currentClass, currentMethod); lambda != nil {
			result.Lambdas = append(result.Lambdas, *lambda)
		}

	case "method_reference":
		// Track method references as calls (Class::method)
		if currentMethod != "" {
			p.extractMethodReference(node, content, result, currentMethod)
		}
	}

	// Track current class and method context for recursion
	className := currentClass
	methodName := currentMethod
	classPaths := currentClassPaths
	classMethods := currentClassMethods

	if nodeType == "class_declaration" || nodeType == "record_declaration" || nodeType == "enum_declaration" || nodeType == "interface_declaration" {
		className = javaDeclarationName(node, content)
		classPaths, classMethods = p.extractClassRequestMapping(node, content)
	}

	if nodeType == "method_declaration" {
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child.Type() == "identifier" {
				// Full method name: ClassName.methodName
				if className != "" {
					methodName = className + "." + child.Content(content)
				} else {
					methodName = child.Content(content)
				}
				break
			}
		}
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		p.extractNodes(node.Child(i), content, result, className, methodName, classPaths, classMethods)
	}
}

func (p *JavaParser) extractImport(node *sitter.Node, content []byte, result *ParsedFile) {
	// import com.example.Foo;
	importText := node.Content(content)
	importText = strings.TrimPrefix(importText, "import ")
	importText = strings.TrimPrefix(importText, "static ")
	importText = strings.TrimSuffix(importText, ";")
	importText = strings.TrimSpace(importText)

	if importText != "" {
		result.Imports = append(result.Imports, ParsedImport{
			Path:      importText,
			Names:     []string{},
			IsDefault: false,
		})
	}
}

// javaDeclarationName keeps member types distinct without changing top-level
// names. Local types also include their enclosing block's source position: two
// disjoint blocks (even on one line) may legally declare the same local name.
func javaDeclarationName(node *sitter.Node, content []byte) string {
	var scopes []string
	for current := node; current != nil; current = current.Parent() {
		switch current.Type() {
		case "class_declaration", "enum_declaration", "record_declaration", "interface_declaration", "annotation_type_declaration":
			if name := current.ChildByFieldName("name"); name != nil {
				scopes = append(scopes, name.Content(content))
			}
		case "block":
			scopes = append(scopes, fmt.Sprintf("$local@%d", current.StartByte()))
		case "class_body":
			if parent := current.Parent(); parent != nil && parent.Type() == "object_creation_expression" {
				scopes = append(scopes, fmt.Sprintf("$anonymous@%d", parent.StartByte()))
			}
		}
	}
	for i, j := 0, len(scopes)-1; i < j; i, j = i+1, j-1 {
		scopes[i], scopes[j] = scopes[j], scopes[i]
	}
	return strings.Join(scopes, ".")
}

func simpleJavaDeclarationName(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i+1:]
	}
	return name
}

func (p *JavaParser) extractClass(node *sitter.Node, content []byte, result *ParsedFile, isEnum bool) {
	name := javaDeclarationName(node, content)
	var extendsClass string
	var implements []string
	var modifiers []string
	var typeParams []ParsedTypeParameter
	var annotations []ParsedAnnotation
	var fields []ParsedField
	var constructors []ParsedConstructor
	var enumConstants []ParsedEnumConstant
	isExported := false
	isAbstract := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "identifier":
			if name == "" {
				name = child.Content(content)
			}
		case "modifiers":
			modifiers, annotations = p.extractModifiersAndAnnotations(child, content)
			isExported = containsModifier(modifiers, "public")
			isAbstract = containsModifier(modifiers, "abstract")
		case "type_parameters":
			typeParams = p.extractTypeParameters(child, content)
		case "superclass":
			// extends Foo
			for j := 0; j < int(child.ChildCount()); j++ {
				childType := child.Child(j).Type()
				if childType == "type_identifier" || childType == "scoped_type_identifier" || childType == "generic_type" {
					extendsClass = child.Child(j).Content(content)
				}
			}
		case "super_interfaces":
			// implements Bar, Baz
			implements = p.extractInterfaceList(child, content)
		case "class_body":
			// Extract fields and constructors from the body
			fields, constructors = p.extractFieldsAndConstructors(child, content, name)
		case "enum_body":
			// Extract fields, constructors, and enum constants from enum body
			fields, constructors = p.extractFieldsAndConstructors(child, content, name)
			if isEnum {
				enumConstants = p.extractEnumConstants(child, content)
			}
		}
	}

	if name != "" {
		result.Classes = append(result.Classes, ParsedClass{
			Name:           name,
			ExtendsClass:   extendsClass,
			Implements:     implements,
			StartLine:      int(node.StartPoint().Row) + 1,
			EndLine:        int(node.EndPoint().Row) + 1,
			IsExported:     isExported,
			IsEnum:         isEnum,
			IsAbstract:     isAbstract,
			Modifiers:      modifiers,
			TypeParameters: typeParams,
			Fields:         fields,
			Constructors:   constructors,
			Annotations:    annotations,
			EnumConstants:  enumConstants,
		})
	}
}

// extractRecord extracts a Java record declaration (Java 14+)
// Records are immutable data classes with auto-generated accessors, constructor, equals, hashCode, toString
func (p *JavaParser) extractRecord(node *sitter.Node, content []byte, result *ParsedFile) {
	name := javaDeclarationName(node, content)
	var modifiers []string
	var annotations []ParsedAnnotation
	var typeParams []ParsedTypeParameter
	var implements []string
	var components []ParsedField // Record components (the parameters)
	isExported := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "identifier":
			if name == "" {
				name = child.Content(content)
			}
		case "modifiers":
			modifiers, annotations = p.extractModifiersAndAnnotations(child, content)
			isExported = containsModifier(modifiers, "public")
		case "type_parameters":
			typeParams = p.extractTypeParameters(child, content)
		case "formal_parameters":
			// Record components are in formal_parameters
			components = p.extractRecordComponents(child, content)
		case "super_interfaces":
			// Records can implement interfaces
			implements = p.extractInterfaceList(child, content)
		}
	}

	if name != "" {
		result.Classes = append(result.Classes, ParsedClass{
			Name:           name,
			Implements:     implements,
			StartLine:      int(node.StartPoint().Row) + 1,
			EndLine:        int(node.EndPoint().Row) + 1,
			IsExported:     isExported,
			IsRecord:       true,
			Modifiers:      modifiers,
			TypeParameters: typeParams,
			Fields:         components,
			Annotations:    annotations,
		})

		// Generate synthetic methods for record: accessors, constructor, equals, hashCode, toString
		syntheticMethods := p.generateRecordSyntheticMethods(name, components, int(node.StartPoint().Row)+1)
		result.SyntheticMethods = append(result.SyntheticMethods, syntheticMethods...)
	}
}

// extractRecordComponents extracts record components from the formal_parameters node
func (p *JavaParser) extractRecordComponents(node *sitter.Node, content []byte) []ParsedField {
	var components []ParsedField

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "formal_parameter" || child.Type() == "record_pattern_component" {
			var fieldType string
			var fieldName string
			var fieldAnnotations []ParsedAnnotation

			for j := 0; j < int(child.ChildCount()); j++ {
				param := child.Child(j)
				switch param.Type() {
				case "modifiers":
					_, fieldAnnotations = p.extractModifiersAndAnnotations(param, content)
				case "type_identifier", "generic_type", "array_type", "integral_type", "boolean_type", "floating_point_type":
					fieldType = param.Content(content)
				case "identifier":
					fieldName = param.Content(content)
				}
			}

			if fieldName != "" && fieldType != "" {
				components = append(components, ParsedField{
					Name:        fieldName,
					FieldType:   fieldType,
					Modifiers:   []string{"private", "final"}, // Record fields are implicitly private final
					Annotations: fieldAnnotations,
				})
			}
		}
	}

	return components
}

// generateRecordSyntheticMethods generates synthetic methods for a record
func (p *JavaParser) generateRecordSyntheticMethods(className string, components []ParsedField, lineNumber int) []ParsedSyntheticMethod {
	var methods []ParsedSyntheticMethod

	// Accessor methods - records use component name directly (no "get" prefix)
	for _, comp := range components {
		methods = append(methods, ParsedSyntheticMethod{
			Name:       comp.Name,
			ClassName:  className,
			ReturnType: comp.FieldType,
			Params:     []string{},
			Source:     "record",
			FieldName:  comp.Name,
			LineNumber: lineNumber,
		})
	}

	// Canonical constructor
	var paramTypes []string
	for _, comp := range components {
		paramTypes = append(paramTypes, comp.FieldType)
	}
	methods = append(methods, ParsedSyntheticMethod{
		Name:       simpleJavaDeclarationName(className),
		ClassName:  className,
		Params:     paramTypes,
		Source:     "record",
		LineNumber: lineNumber,
	})

	// equals(Object)
	methods = append(methods, ParsedSyntheticMethod{
		Name:       "equals",
		ClassName:  className,
		ReturnType: "boolean",
		Params:     []string{"Object"},
		Source:     "record",
		LineNumber: lineNumber,
	})

	// hashCode()
	methods = append(methods, ParsedSyntheticMethod{
		Name:       "hashCode",
		ClassName:  className,
		ReturnType: "int",
		Params:     []string{},
		Source:     "record",
		LineNumber: lineNumber,
	})

	// toString()
	methods = append(methods, ParsedSyntheticMethod{
		Name:       "toString",
		ClassName:  className,
		ReturnType: "String",
		Params:     []string{},
		Source:     "record",
		LineNumber: lineNumber,
	})

	return methods
}

// extractInterface extracts a Java interface declaration
func (p *JavaParser) extractInterface(node *sitter.Node, content []byte, result *ParsedFile) {
	name := javaDeclarationName(node, content)
	var extendsInterfaces []string
	var modifiers []string
	var typeParams []ParsedTypeParameter
	var annotations []ParsedAnnotation
	var methods []ParsedFunction
	var fields []ParsedField
	isExported := false
	isFunctional := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "identifier":
			if name == "" {
				name = child.Content(content)
			}
		case "modifiers":
			modifiers, annotations = p.extractModifiersAndAnnotations(child, content)
			isExported = containsModifier(modifiers, "public")
			// Check for @FunctionalInterface
			for _, ann := range annotations {
				if ann.Name == "FunctionalInterface" {
					isFunctional = true
					break
				}
			}
		case "type_parameters":
			typeParams = p.extractTypeParameters(child, content)
		case "extends_interfaces":
			// Interface extends multiple interfaces
			extendsInterfaces = p.extractInterfaceList(child, content)
		case "interface_body":
			// Extract interface methods and constants
			methods, fields = p.extractInterfaceBody(child, content, name)
		}
	}

	if name != "" {
		result.Interfaces = append(result.Interfaces, ParsedInterface{
			Name:              name,
			StartLine:         int(node.StartPoint().Row) + 1,
			EndLine:           int(node.EndPoint().Row) + 1,
			ExtendsInterfaces: extendsInterfaces,
			Methods:           methods,
			IsExported:        isExported,
			IsFunctional:      isFunctional,
			TypeParameters:    typeParams,
			Fields:            fields,
			Annotations:       annotations,
		})

		// Outbound HTTP clients (Retrofit, Feign, Spring HTTP interfaces) declare
		// calls; their endpoint-looking annotations are not routes.
		role := classifyJavaInterfaceHTTP(annotations, methods, result.Imports)
		httpMethods := p.extractHttpInterfaceMethods(name, methods, role)
		result.HttpInterfaceMethods = append(result.HttpInterfaceMethods, httpMethods...)
	}
}

// extractModifiersAndAnnotations extracts modifiers and annotations from a modifiers node
func (p *JavaParser) extractModifiersAndAnnotations(node *sitter.Node, content []byte) ([]string, []ParsedAnnotation) {
	var modifiers []string
	var annotations []ParsedAnnotation

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "annotation", "marker_annotation":
			ann := p.extractAnnotation(child, content)
			if ann != nil {
				annotations = append(annotations, *ann)
			}
		default:
			// Regular modifier keywords
			modText := child.Content(content)
			if isJavaModifier(modText) {
				modifiers = append(modifiers, modText)
			}
		}
	}

	return modifiers, annotations
}

// extractAnnotation extracts a single annotation with its values
func (p *JavaParser) extractAnnotation(node *sitter.Node, content []byte) *ParsedAnnotation {
	var name string
	values := make(map[string]interface{})
	lineNumber := int(node.StartPoint().Row) + 1

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "identifier":
			name = child.Content(content)
		case "annotation_argument_list":
			values = p.extractAnnotationValues(child, content)
		}
	}

	// For marker annotations like @Override, the identifier is direct
	if name == "" {
		// Try to get identifier directly from content
		text := node.Content(content)
		if strings.HasPrefix(text, "@") {
			// Extract annotation name after @
			name = strings.TrimPrefix(text, "@")
			// Remove parentheses if any
			if idx := strings.Index(name, "("); idx != -1 {
				name = name[:idx]
			}
		}
	}

	if name == "" {
		return nil
	}
	if idx := strings.LastIndex(name, "."); idx != -1 {
		name = name[idx+1:]
	}

	return &ParsedAnnotation{
		Name:       name,
		Values:     values,
		LineNumber: lineNumber,
	}
}

// extractAnnotationValues extracts values from annotation_argument_list
func (p *JavaParser) extractAnnotationValues(node *sitter.Node, content []byte) map[string]interface{} {
	values := make(map[string]interface{})

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "element_value_pair":
			// Named value: key = value
			var key string
			var val interface{}
			for j := 0; j < int(child.ChildCount()); j++ {
				subChild := child.Child(j)
				if subChild.Type() == "identifier" {
					key = subChild.Content(content)
				} else if key != "" {
					val = p.extractAnnotationValue(subChild, content)
				}
			}
			if key != "" {
				values[key] = val
			}
		case "element_value":
			// Single value annotation like @Path("/api")
			values["value"] = p.extractAnnotationValue(child, content)
		case "string_literal", "number_literal", "true", "false":
			// Single value annotation like @Path("/api")
			values["value"] = p.extractAnnotationValue(child, content)
		case "field_access", "identifier":
			// Enum or constant reference like @Path(HttpMethod.GET)
			values["value"] = child.Content(content)
		case "array_initializer":
			values["value"] = p.extractAnnotationValue(child, content)
		case "element_value_array_initializer":
			// Tree-sitter uses this node type for annotation arguments like @Select({ "a", "b" }).
			values["value"] = p.extractAnnotationValue(child, content)
		}
	}

	return values
}

// extractAnnotationValue extracts a single annotation value
func (p *JavaParser) extractAnnotationValue(node *sitter.Node, content []byte) interface{} {
	nodeType := node.Type()
	text := node.Content(content)

	switch nodeType {
	case "string_literal":
		return strings.Trim(text, "\"")
	case "number_literal":
		return text
	case "true":
		return true
	case "false":
		return false
	case "field_access":
		return text
	case "array_initializer":
		fallthrough
	case "element_value_array_initializer":
		// Array of values
		var arr []interface{}
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child.Type() != "," && child.Type() != "{" && child.Type() != "}" {
				arr = append(arr, p.extractAnnotationValue(child, content))
			}
		}
		return arr
	default:
		return text
	}
}

// extractTypeParameters extracts generic type parameters
func (p *JavaParser) extractTypeParameters(node *sitter.Node, content []byte) []ParsedTypeParameter {
	var params []ParsedTypeParameter
	idx := 0

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "type_parameter" {
			param := p.extractSingleTypeParameter(child, content, idx)
			params = append(params, param)
			idx++
		}
	}

	return params
}

// extractSingleTypeParameter extracts a single type parameter (T extends Foo & Bar)
func (p *JavaParser) extractSingleTypeParameter(node *sitter.Node, content []byte, index int) ParsedTypeParameter {
	var name string
	var bounds []string
	var boundType string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "identifier", "type_identifier":
			// Type parameter name can be identifier or type_identifier depending on tree-sitter version
			if name == "" {
				name = child.Content(content)
			}
		case "type_bound":
			boundType = "extends"
			for j := 0; j < int(child.ChildCount()); j++ {
				boundChild := child.Child(j)
				childType := boundChild.Type()
				if childType == "type_identifier" || childType == "generic_type" {
					bounds = append(bounds, boundChild.Content(content))
				}
			}
		}
	}

	// If still no name, try getting direct content (for simple type params like <T>)
	if name == "" && node.ChildCount() > 0 {
		firstChild := node.Child(0)
		if firstChild.Type() == "identifier" || firstChild.Type() == "type_identifier" {
			name = firstChild.Content(content)
		}
	}

	return ParsedTypeParameter{
		Name:      name,
		Index:     index,
		Bounds:    bounds,
		BoundType: boundType,
	}
}

// extractInterfaceList extracts a list of interfaces from super_interfaces or extends_interfaces
func (p *JavaParser) extractInterfaceList(node *sitter.Node, content []byte) []string {
	var interfaces []string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()
		if childType == "type_identifier" || childType == "generic_type" {
			interfaces = append(interfaces, child.Content(content))
		} else if childType == "type_list" {
			// Recurse into type_list
			for j := 0; j < int(child.ChildCount()); j++ {
				subChild := child.Child(j)
				subType := subChild.Type()
				if subType == "type_identifier" || subType == "generic_type" {
					interfaces = append(interfaces, subChild.Content(content))
				}
			}
		}
	}

	return interfaces
}

// extractFieldsAndConstructors extracts fields and constructors from a class body
func (p *JavaParser) extractFieldsAndConstructors(node *sitter.Node, content []byte, className string) ([]ParsedField, []ParsedConstructor) {
	var fields []ParsedField
	var constructors []ParsedConstructor

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "field_declaration":
			field := p.extractField(child, content)
			if field != nil {
				fields = append(fields, *field)
			}
		case "constructor_declaration":
			constructor := p.extractConstructor(child, content, className)
			if constructor != nil {
				constructors = append(constructors, *constructor)
			}
		}
	}

	return fields, constructors
}

// extractField extracts a field declaration
func (p *JavaParser) extractField(node *sitter.Node, content []byte) *ParsedField {
	var fieldType string
	var typeParams []string
	var name string
	var modifiers []string
	var annotations []ParsedAnnotation
	startLine := int(node.StartPoint().Row) + 1

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "modifiers":
			modifiers, annotations = p.extractModifiersAndAnnotations(child, content)
		case "type_identifier", "integral_type", "boolean_type", "floating_point_type", "void_type":
			fieldType = child.Content(content)
		case "generic_type":
			fieldType, typeParams = p.extractGenericType(child, content)
		case "array_type":
			fieldType = child.Content(content)
		case "variable_declarator":
			// Get the variable name
			for j := 0; j < int(child.ChildCount()); j++ {
				if child.Child(j).Type() == "identifier" {
					name = child.Child(j).Content(content)
					break
				}
			}
		}
	}

	if name == "" || fieldType == "" {
		return nil
	}

	// Check for dependency injection annotations
	isInjected := false
	injectionType := ""
	for _, ann := range annotations {
		switch ann.Name {
		case "Autowired":
			isInjected = true
			injectionType = "autowired"
		case "Inject":
			isInjected = true
			injectionType = "inject"
		case "Value":
			isInjected = true
			injectionType = "value"
		case "Resource":
			isInjected = true
			injectionType = "resource"
		}
	}

	return &ParsedField{
		Name:           name,
		FieldType:      fieldType,
		TypeParameters: typeParams,
		Modifiers:      modifiers,
		StartLine:      startLine,
		IsInjected:     isInjected,
		InjectionType:  injectionType,
		Annotations:    annotations,
	}
}

// extractGenericType extracts a generic type like Map<String, Integer>
func (p *JavaParser) extractGenericType(node *sitter.Node, content []byte) (string, []string) {
	var baseType string
	var typeArgs []string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "type_identifier":
			baseType = child.Content(content)
		case "type_arguments":
			for j := 0; j < int(child.ChildCount()); j++ {
				argChild := child.Child(j)
				argType := argChild.Type()
				if argType == "type_identifier" || argType == "generic_type" {
					typeArgs = append(typeArgs, argChild.Content(content))
				}
			}
		}
	}

	return baseType, typeArgs
}

// extractConstructor extracts a constructor declaration
func (p *JavaParser) extractConstructor(node *sitter.Node, content []byte, className string) *ParsedConstructor {
	var params []ParsedConstructorParam
	var annotations []ParsedAnnotation
	startLine := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "modifiers":
			_, annotations = p.extractModifiersAndAnnotations(child, content)
		case "formal_parameters":
			params = p.extractConstructorParams(child, content)
		}
	}

	return &ParsedConstructor{
		ClassName:   className,
		Parameters:  params,
		StartLine:   startLine,
		EndLine:     endLine,
		Annotations: annotations,
	}
}

// extractConstructorParams extracts constructor parameters
func (p *JavaParser) extractConstructorParams(node *sitter.Node, content []byte) []ParsedConstructorParam {
	var params []ParsedConstructorParam
	index := 0

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "formal_parameter" {
			param := p.extractConstructorParam(child, content, index)
			if param != nil {
				params = append(params, *param)
				index++
			}
		}
	}

	return params
}

// extractConstructorParam extracts a single constructor parameter
func (p *JavaParser) extractConstructorParam(node *sitter.Node, content []byte, index int) *ParsedConstructorParam {
	var name string
	var paramType string
	var annotation string
	var annotationValue string
	isInjected := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "modifiers":
			// Check for parameter annotations
			for j := 0; j < int(child.ChildCount()); j++ {
				annChild := child.Child(j)
				if annChild.Type() == "annotation" || annChild.Type() == "marker_annotation" {
					ann := p.extractAnnotation(annChild, content)
					if ann != nil {
						annotation = ann.Name
						if val, ok := ann.Values["value"]; ok {
							annotationValue = fmt.Sprintf("%v", val)
						}
						// Check for DI annotations
						switch ann.Name {
						case "Autowired", "Inject", "Value":
							isInjected = true
						}
					}
				}
			}
		case "type_identifier", "generic_type", "integral_type", "boolean_type", "floating_point_type":
			paramType = child.Content(content)
		case "identifier":
			name = child.Content(content)
		}
	}

	if name == "" {
		return nil
	}

	return &ParsedConstructorParam{
		Name:            name,
		ParamType:       paramType,
		Index:           index,
		IsInjected:      isInjected,
		Annotation:      annotation,
		AnnotationValue: annotationValue,
	}
}

// extractInterfaceBody extracts methods and constants from an interface body
func (p *JavaParser) extractInterfaceBody(node *sitter.Node, content []byte, interfaceName string) ([]ParsedFunction, []ParsedField) {
	var methods []ParsedFunction
	var fields []ParsedField

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "method_declaration":
			method := p.extractInterfaceMethod(child, content, interfaceName)
			if method != nil {
				methods = append(methods, *method)
			}
		case "constant_declaration":
			field := p.extractField(child, content)
			if field != nil {
				fields = append(fields, *field)
			}
		}
	}

	return methods, fields
}

// extractInterfaceMethod extracts a method from an interface
func (p *JavaParser) extractInterfaceMethod(node *sitter.Node, content []byte, interfaceName string) *ParsedFunction {
	var name string
	var returnType string
	var params []string
	var paramTypes []string
	var modifiers []string
	var annotations []ParsedAnnotation
	isExported := true // Interface methods are always public

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "modifiers":
			modifiers, annotations = p.extractModifiersAndAnnotations(child, content)
		case "type_identifier", "void_type", "integral_type", "boolean_type", "floating_point_type", "generic_type":
			if returnType == "" {
				returnType = child.Content(content)
			}
		case "identifier":
			name = child.Content(content)
		case "formal_parameters":
			for j := 0; j < int(child.ChildCount()); j++ {
				param := child.Child(j)
				if param.Type() == "formal_parameter" {
					params = append(params, param.Content(content))
					if typeNode := param.ChildByFieldName("type"); typeNode != nil {
						paramTypes = append(paramTypes, typeNode.Content(content))
					}
				}
			}
		}
	}

	if name == "" {
		return nil
	}

	fullName := name
	if interfaceName != "" {
		fullName = interfaceName + "." + name
	}

	signature := &ParsedMethodSignature{
		Signature:      name + "(" + strings.Join(paramTypes, ",") + ")",
		ParameterTypes: paramTypes,
		ReturnType:     returnType,
		IsOverride:     false,
	}

	return &ParsedFunction{
		Name:        fullName,
		Params:      params,
		ReturnType:  returnType,
		StartLine:   int(node.StartPoint().Row) + 1,
		EndLine:     int(node.EndPoint().Row) + 1,
		IsExported:  isExported,
		Modifiers:   modifiers,
		Annotations: annotations,
		Signature:   signature,
	}
}

// Helper functions

func containsModifier(modifiers []string, mod string) bool {
	for _, m := range modifiers {
		if m == mod {
			return true
		}
	}
	return false
}

func isJavaModifier(text string) bool {
	modifiers := map[string]bool{
		"public": true, "private": true, "protected": true,
		"static": true, "final": true, "abstract": true,
		"synchronized": true, "volatile": true, "transient": true,
		"native": true, "strictfp": true, "default": true,
	}
	return modifiers[text]
}

func (p *JavaParser) extractMethod(node *sitter.Node, content []byte, result *ParsedFile, currentClass string) {
	var name string
	var returnType string
	var params []string
	var paramTypes []string
	var modifiers []string
	var annotations []ParsedAnnotation
	var typeParams []ParsedTypeParameter
	var throwsTypes []string
	isExported := false
	isAsync := false
	isOverride := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "modifiers":
			modifiers, annotations = p.extractModifiersAndAnnotations(child, content)
			isExported = containsModifier(modifiers, "public")
			// Check for @Async and @Override annotations
			for _, ann := range annotations {
				if ann.Name == "Async" {
					isAsync = true
				}
				if ann.Name == "Override" {
					isOverride = true
				}
			}
		case "type_parameters":
			typeParams = p.extractTypeParameters(child, content)
		case "type_identifier", "void_type", "integral_type", "boolean_type", "floating_point_type", "generic_type":
			if returnType == "" {
				returnType = child.Content(content)
			}
		case "identifier":
			name = child.Content(content)
		case "formal_parameters":
			for j := 0; j < int(child.ChildCount()); j++ {
				param := child.Child(j)
				if param.Type() == "formal_parameter" {
					params = append(params, param.Content(content))
					if typeNode := param.ChildByFieldName("type"); typeNode != nil {
						paramTypes = append(paramTypes, typeNode.Content(content))
					}
				}
			}
		case "throws":
			// Extract exception types from throws clause
			for j := 0; j < int(child.ChildCount()); j++ {
				exType := child.Child(j)
				if exType.Type() == "type_identifier" || exType.Type() == "generic_type" {
					throwsTypes = append(throwsTypes, exType.Content(content))
				}
			}
		}
	}

	if name != "" {
		fullName := name
		if currentClass != "" {
			fullName = currentClass + "." + name
		}

		// Get source code
		sourceCode := node.Content(content)
		if len(sourceCode) > 2000 {
			sourceCode = sourceCode[:2000] + "..."
		}

		signature := &ParsedMethodSignature{
			Signature:      name + "(" + strings.Join(paramTypes, ",") + ")",
			ParameterTypes: paramTypes,
			ReturnType:     returnType,
			IsOverride:     isOverride,
		}

		result.Functions = append(result.Functions, ParsedFunction{
			Name:           fullName,
			Params:         params,
			ReturnType:     returnType,
			StartLine:      int(node.StartPoint().Row) + 1,
			EndLine:        int(node.EndPoint().Row) + 1,
			IsExported:     isExported,
			IsAsync:        isAsync,
			SourceCode:     sourceCode,
			Modifiers:      modifiers,
			Annotations:    annotations,
			Signature:      signature,
			TypeParameters: typeParams,
			ThrowsTypes:    throwsTypes,
		})
	}
}

func (p *JavaParser) extractMethodCall(node *sitter.Node, content []byte, result *ParsedFile, currentMethod string) {
	// method_invocation: object.method(args) or method(args)
	// Structure: method_invocation can have:
	//   - identifier (simple method call: method())
	//   - object.identifier (qualified: obj.method())
	//   - field_access.identifier (chained: obj.field.method())
	var calleeName string
	var receiver string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		childType := child.Type()

		switch childType {
		case "identifier":
			// This could be the method name or the receiver
			// In "obj.method()", "obj" comes before "." and "method" after
			// In "method()", "method" is the only identifier
			text := child.Content(content)
			if calleeName == "" {
				// First identifier - could be method name or receiver
				// Check if next sibling is "." to determine
				if i+1 < int(node.ChildCount()) && node.Child(i+1).Type() == "." {
					receiver = text
				} else {
					calleeName = text
				}
			} else {
				// Already have a name, this is the method name
				calleeName = text
			}
		case ".":
			// Dot separator - the previous identifier was the receiver
			// Next identifier will be the method name
			continue
		case "field_access":
			// field_access for chained calls (e.g., obj.field or this.field)
			// Extract the receiver name
			receiver = p.extractFieldAccessReceiver(child, content)
		case "method_invocation":
			// Chained method call (e.g., obj.method1().method2())
			// The result of inner call is the receiver
			receiver = p.extractChainedMethodName(child, content)
		case "this":
			receiver = "this"
		}
	}

	// If we captured receiver but not method name, look for the method name after the dot
	if receiver != "" && calleeName == "" {
		// Find the identifier that follows the receiver/dot
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child.Type() == "identifier" {
				text := child.Content(content)
				if text != receiver {
					calleeName = text
					break
				}
			}
		}
	}

	if calleeName != "" && currentMethod != "" {
		// Format: receiver.method or just method
		fullName := calleeName
		if receiver != "" && receiver != "this" && receiver != "super" {
			fullName = receiver + "." + calleeName
		} else if receiver == "this" || receiver == "super" {
			if className := classNameFromMethodName(currentMethod); className != "" {
				fullName = className + "." + calleeName
			}
		}

		if result.FunctionCalls[currentMethod] == nil {
			result.FunctionCalls[currentMethod] = []ParsedFunctionCall{}
		}
		result.FunctionCalls[currentMethod] = append(result.FunctionCalls[currentMethod], ParsedFunctionCall{
			CalleeName: fullName,
			Receiver:   receiver,
			MethodName: calleeName,
			LineNumber: int(node.StartPoint().Row) + 1,
			IsAsync:    false,
		})
	}
}

func (p *JavaParser) extractLocalVariableDeclaration(node *sitter.Node, content []byte) (string, []string) {
	var typeName string
	var names []string

	collectNames := func(n *sitter.Node) {}
	collectNames = func(n *sitter.Node) {
		if n.Type() == "variable_declarator" || n.Type() == "variable_declarator_id" {
			if name := p.extractVariableDeclaratorName(n, content); name != "" {
				names = append(names, name)
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			collectNames(n.Child(i))
		}
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "type_identifier", "scoped_type_identifier", "integral_type", "boolean_type", "floating_point_type", "void_type", "array_type":
			if typeName == "" {
				typeName = child.Content(content)
			}
		case "generic_type":
			if typeName == "" {
				base, _ := p.extractGenericType(child, content)
				typeName = base
			}
		case "variable_declarator", "variable_declarator_list", "variable_declarator_id":
			collectNames(child)
		}
	}

	if typeName == "" {
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child.Type() == "variable_declarator" || child.Type() == "variable_declarator_list" {
				collectNames(child)
			}
		}
	}

	return typeName, names
}

func (p *JavaParser) extractEnhancedForVariable(node *sitter.Node, content []byte) (string, string) {
	var typeName string
	var varName string
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "type_identifier", "scoped_type_identifier", "integral_type", "boolean_type", "floating_point_type", "void_type", "array_type":
			if typeName == "" {
				typeName = child.Content(content)
			}
		case "generic_type":
			if typeName == "" {
				base, _ := p.extractGenericType(child, content)
				typeName = base
			}
		case "variable_declarator_id", "identifier":
			if varName == "" {
				varName = p.extractVariableDeclaratorName(child, content)
			}
		}
	}
	return typeName, varName
}

func (p *JavaParser) extractVariableDeclaratorName(node *sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}
	if node.Type() == "identifier" {
		return node.Content(content)
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "identifier":
			return child.Content(content)
		case "variable_declarator_id", "variable_declarator", "field_access":
			if name := p.extractVariableDeclaratorName(child, content); name != "" {
				return name
			}
		}
	}
	return ""
}

// extractFieldAccessReceiver extracts the receiver from a field_access node
func (p *JavaParser) extractFieldAccessReceiver(node *sitter.Node, content []byte) string {
	// field_access: obj.field or this.field
	// Children are: object, ".", identifier
	var parts []string
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "identifier":
			parts = append(parts, child.Content(content))
		case "this":
			parts = append(parts, "this")
		case "field_access":
			// Nested field access - recurse
			inner := p.extractFieldAccessReceiver(child, content)
			if inner != "" {
				parts = append(parts, inner)
			}
		}
	}
	// Return just the first identifier (the immediate receiver)
	if len(parts) > 0 {
		if (parts[0] == "this" || parts[0] == "super") && len(parts) > 1 {
			return parts[1]
		}
		return parts[0]
	}
	return ""
}

func classNameFromMethodName(name string) string {
	if idx := strings.LastIndex(name, "."); idx > 0 {
		return name[:idx]
	}
	return ""
}

// extractChainedMethodName extracts a simple name from a chained method call
func (p *JavaParser) extractChainedMethodName(node *sitter.Node, content []byte) string {
	// For chained calls, we just return a simplified receiver name
	// e.g., for obj.method1().method2(), return "obj"
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "identifier":
			return child.Content(content)
		case "field_access":
			return p.extractFieldAccessReceiver(child, content)
		case "this":
			return "this"
		}
	}
	return ""
}

func (p *JavaParser) extractEndpoint(node *sitter.Node, content []byte, result *ParsedFile, currentClass string, classPaths, classMethods []string) {
	// Look for Spring annotations: @GetMapping, @PostMapping, @RequestMapping
	// Or JAX-RS: @GET, @POST, @Path
	if role, inInterface := p.enclosingInterfaceRole(node, content, result); inInterface && (role.outboundClient || role.dataMapper) {
		return
	}
	handlerName := extractHandlerName(node, content, currentClass)
	lineNum := int(node.StartPoint().Row) + 1
	annotations := findFunctionAnnotations(result.Functions, handlerName, lineNum)
	if len(annotations) == 0 {
		annotations = p.collectNodeAnnotations(node, content)
	}
	if len(annotations) == 0 {
		return
	}

	if stripesEndpoints := p.extractStripesEndpoints(annotations, handlerName, classPaths, lineNum); len(stripesEndpoints) > 0 {
		result.Endpoints = append(result.Endpoints, stripesEndpoints...)
		return
	}

	var methods []string
	var paths []string
	hasRequestMapping := false

	for _, ann := range annotations {
		switch ann.Name {
		case "GetMapping":
			methods = append(methods, "GET")
			paths = append(paths, annotationPathValues(ann)...)
		case "PostMapping":
			methods = append(methods, "POST")
			paths = append(paths, annotationPathValues(ann)...)
		case "PutMapping":
			methods = append(methods, "PUT")
			paths = append(paths, annotationPathValues(ann)...)
		case "DeleteMapping":
			methods = append(methods, "DELETE")
			paths = append(paths, annotationPathValues(ann)...)
		case "PatchMapping":
			methods = append(methods, "PATCH")
			paths = append(paths, annotationPathValues(ann)...)
		case "RequestMapping":
			hasRequestMapping = true
			if reqMethods := annotationMethodValues(ann); len(reqMethods) > 0 {
				methods = append(methods, reqMethods...)
			}
			paths = append(paths, annotationPathValues(ann)...)
		case "Path":
			paths = append(paths, annotationPathValues(ann)...)
		case "GET":
			methods = append(methods, "GET")
		case "POST":
			methods = append(methods, "POST")
		case "PUT":
			methods = append(methods, "PUT")
		case "DELETE":
			methods = append(methods, "DELETE")
		case "PATCH":
			methods = append(methods, "PATCH")
		}
	}

	if len(methods) == 0 {
		if len(classMethods) > 0 {
			methods = append(methods, classMethods...)
		} else if hasRequestMapping {
			methods = append(methods, "REQUEST")
		}
	}

	if len(methods) == 0 {
		return
	}
	if len(paths) == 0 {
		paths = []string{""}
	}
	if len(classPaths) == 0 {
		classPaths = []string{""}
	}

	uniqueMethods := uniqueStrings(methods)
	uniquePaths := uniqueStringsAllowEmpty(paths)
	uniqueClassPaths := uniqueStringsAllowEmpty(classPaths)

	for _, method := range uniqueMethods {
		for _, basePath := range uniqueClassPaths {
			for _, path := range uniquePaths {
				fullPath := joinPaths(basePath, path)
				if fullPath == "" {
					fullPath = "/"
				}
				result.Endpoints = append(result.Endpoints, ParsedEndpoint{
					Path:        fullPath,
					Method:      method,
					HandlerName: handlerName,
					LineNumber:  lineNum,
				})
			}
		}
	}

}

func (p *JavaParser) extractStripesEndpoints(annotations []ParsedAnnotation, handlerName string, classPaths []string, lineNum int) []ParsedEndpoint {
	var eventNames []string
	hasDefault := false

	for _, ann := range annotations {
		switch ann.Name {
		case "HandlesEvent":
			eventNames = append(eventNames, annotationPathValues(ann)...)
		case "DefaultHandler":
			hasDefault = true
		}
	}

	if len(eventNames) == 0 && !hasDefault {
		return nil
	}
	if len(classPaths) == 0 {
		return nil
	}

	if len(eventNames) == 0 && hasDefault {
		eventNames = []string{""}
	}

	uniqueClassPaths := uniqueStringsAllowEmpty(classPaths)
	uniqueEvents := uniqueStringsAllowEmpty(eventNames)

	var endpoints []ParsedEndpoint
	for _, basePath := range uniqueClassPaths {
		for _, event := range uniqueEvents {
			fullPath := normalizeStripesBinding(basePath, event)
			if fullPath == "" {
				continue
			}
			endpoints = append(endpoints, ParsedEndpoint{
				Path:        fullPath,
				Method:      "REQUEST",
				HandlerName: handlerName,
				LineNumber:  lineNum,
			})
		}
	}

	if len(endpoints) == 0 {
		return nil
	}
	return endpoints
}

func (p *JavaParser) collectNodeAnnotations(node *sitter.Node, content []byte) []ParsedAnnotation {
	var annotations []ParsedAnnotation
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "modifiers":
			_, anns := p.extractModifiersAndAnnotations(child, content)
			annotations = append(annotations, anns...)
		case "annotation", "marker_annotation":
			if ann := p.extractAnnotation(child, content); ann != nil {
				annotations = append(annotations, *ann)
			}
		}
	}
	return annotations
}

func extractHandlerName(node *sitter.Node, content []byte, currentClass string) string {
	var handlerName string
	for i := 0; i < int(node.ChildCount()); i++ {
		if node.Child(i).Type() == "identifier" {
			handlerName = node.Child(i).Content(content)
			break
		}
	}
	if handlerName != "" && currentClass != "" {
		return currentClass + "." + handlerName
	}
	return handlerName
}

func annotationPathValues(ann ParsedAnnotation) []string {
	if ann.Values == nil {
		return nil
	}
	if v, ok := ann.Values["path"]; ok {
		return normalizePathValues(v)
	}
	if v, ok := ann.Values["value"]; ok {
		return normalizePathValues(v)
	}
	return nil
}

func annotationMethodValues(ann ParsedAnnotation) []string {
	if ann.Values == nil {
		return nil
	}
	if v, ok := ann.Values["method"]; ok {
		methods := normalizePathValues(v)
		for i := range methods {
			methods[i] = normalizeRequestMethod(methods[i])
		}
		return methods
	}
	return nil
}

func normalizePathValues(v interface{}) []string {
	switch val := v.(type) {
	case string:
		return splitAnnotationValueString(val)
	case []string:
		var out []string
		for _, item := range val {
			out = append(out, splitAnnotationValueString(item)...)
		}
		return out
	case []interface{}:
		var out []string
		for _, item := range val {
			if s, ok := item.(string); ok {
				out = append(out, splitAnnotationValueString(s)...)
			}
		}
		return out
	}
	return nil
}

func normalizeRequestMethod(method string) string {
	method = strings.TrimSpace(method)
	method = strings.TrimPrefix(method, "{")
	method = strings.TrimSuffix(method, "}")
	if strings.HasPrefix(method, "RequestMethod.") {
		method = strings.TrimPrefix(method, "RequestMethod.")
	}
	return strings.ToUpper(method)
}

func splitAnnotationValueString(value string) []string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "\"")
	if strings.HasPrefix(value, "{") && strings.HasSuffix(value, "}") {
		value = strings.TrimPrefix(value, "{")
		value = strings.TrimSuffix(value, "}")
	}
	if strings.Contains(value, ",") {
		parts := strings.Split(value, ",")
		var out []string
		for _, part := range parts {
			part = strings.TrimSpace(part)
			part = strings.Trim(part, "\"")
			if part == "" {
				continue
			}
			out = append(out, part)
		}
		return out
	}
	if value == "" {
		return nil
	}
	return []string{value}
}

func findFunctionAnnotations(funcs []ParsedFunction, name string, line int) []ParsedAnnotation {
	if name == "" {
		return nil
	}
	for _, fn := range funcs {
		if fn.Name == name && line >= fn.StartLine && line <= fn.EndLine {
			return fn.Annotations
		}
	}
	for _, fn := range funcs {
		if fn.Name == name {
			return fn.Annotations
		}
	}
	return nil
}

func uniqueStrings(items []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func uniqueStringsAllowEmpty(items []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, item := range items {
		item = strings.TrimSpace(item)
		if seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func (p *JavaParser) extractClassRequestMapping(node *sitter.Node, content []byte) ([]string, []string) {
	var annotations []ParsedAnnotation
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "modifiers":
			_, anns := p.extractModifiersAndAnnotations(child, content)
			annotations = append(annotations, anns...)
		case "annotation", "marker_annotation":
			if ann := p.extractAnnotation(child, content); ann != nil {
				annotations = append(annotations, *ann)
			}
		}
	}

	var paths []string
	var methods []string

	for _, ann := range annotations {
		switch ann.Name {
		case "RequestMapping":
			paths = append(paths, annotationPathValues(ann)...)
			if reqMethods := annotationMethodValues(ann); len(reqMethods) > 0 {
				methods = append(methods, reqMethods...)
			}
		case "UrlBinding":
			paths = append(paths, annotationPathValues(ann)...)
		case "Path":
			paths = append(paths, annotationPathValues(ann)...)
		case "GetMapping":
			methods = append(methods, "GET")
			paths = append(paths, annotationPathValues(ann)...)
		case "PostMapping":
			methods = append(methods, "POST")
			paths = append(paths, annotationPathValues(ann)...)
		case "PutMapping":
			methods = append(methods, "PUT")
			paths = append(paths, annotationPathValues(ann)...)
		case "DeleteMapping":
			methods = append(methods, "DELETE")
			paths = append(paths, annotationPathValues(ann)...)
		case "PatchMapping":
			methods = append(methods, "PATCH")
			paths = append(paths, annotationPathValues(ann)...)
		case "GET":
			methods = append(methods, "GET")
		case "POST":
			methods = append(methods, "POST")
		case "PUT":
			methods = append(methods, "PUT")
		case "DELETE":
			methods = append(methods, "DELETE")
		case "PATCH":
			methods = append(methods, "PATCH")
		}
	}

	return uniqueStringsAllowEmpty(paths), uniqueStrings(methods)
}

var stripesBindingParamRe = regexp.MustCompile(`\{\$?([A-Za-z0-9_]+)\}`)
var httpUrlConnUrlRe = regexp.MustCompile(`new URL\("([^"]+)"\)`)
var httpUrlConnMethodRe = regexp.MustCompile(`setRequestMethod\("([A-Z]+)"\)`)
var webClientUriRe = regexp.MustCompile(`\.uri\s*\(\s*["']([^"']+)["']`)
var webClientVerbRe = regexp.MustCompile(`\.(get|post|put|delete|patch|head|options)\s*\(\s*\)`)
var webClientMethodVerbRe = regexp.MustCompile(`\.method\s*\(\s*HttpMethod\.(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS)\s*\)`)
var webTargetPathRe = regexp.MustCompile(`\.(?:target|path)\s*\(\s*["']([^"']+)["']\s*\)`)
var webTargetRequestVerbRe = regexp.MustCompile(`\.request\s*\([^)]*\)\s*\.(get|post|put|delete|patch|head|options)\s*\(`)
var webTargetRequestMethodVerbRe = regexp.MustCompile(`\.request\s*\([^)]*\)\s*\.method\s*\(\s*["']([A-Za-z]+)["']`)
var stripesBindingSlashRe = regexp.MustCompile(`/+`)

func normalizeStripesBinding(binding string, event string) string {
	binding = strings.TrimSpace(binding)
	if binding == "" {
		return ""
	}
	if !strings.HasPrefix(binding, "/") {
		binding = "/" + binding
	}

	binding = stripesBindingParamRe.ReplaceAllStringFunc(binding, func(match string) string {
		sub := stripesBindingParamRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		name := sub[1]
		if name == "event" {
			if event == "" {
				return ""
			}
			return event
		}
		return ":" + name
	})
	binding = stripesBindingSlashRe.ReplaceAllString(binding, "/")
	if len(binding) > 1 {
		binding = strings.TrimRight(binding, "/")
	}
	return binding
}

func normalizePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func joinPaths(base, sub string) string {
	base = normalizePath(base)
	sub = normalizePath(sub)
	if base == "" {
		return sub
	}
	if sub == "" {
		return base
	}
	return strings.TrimRight(base, "/") + sub
}

func extractAnnotationValue(modText, annotation string) string {
	// Extract value from @Annotation("value") or @Annotation(value = "value")
	idx := strings.Index(modText, "@"+annotation)
	if idx == -1 {
		return ""
	}

	rest := modText[idx+len(annotation)+1:]
	start := strings.Index(rest, "(")
	if start == -1 {
		return ""
	}
	end := strings.Index(rest, ")")
	if end == -1 || end <= start {
		return ""
	}

	value := rest[start+1 : end]
	value = strings.Trim(value, "\"' ")

	// Handle value = "..." format
	if strings.Contains(value, "=") {
		parts := strings.SplitN(value, "=", 2)
		if len(parts) == 2 {
			value = strings.Trim(parts[1], "\"' ")
		}
	}

	return value
}

// extractHttpCall extracts HTTP client calls like RestTemplate, WebClient, HttpClient
func (p *JavaParser) extractHttpCall(node *sitter.Node, content []byte) *ParsedHttpCall {
	// Get the full invocation text to analyze
	invocationText := node.Content(content)
	lineNumber := int(node.StartPoint().Row) + 1

	var clientType, method, url string

	// Check against configured Java patterns
	for _, pattern := range p.config.GetJavaPatterns() {
		// Check if any of the pattern's Contains strings match the invocation
		matched := false
		for _, containsStr := range pattern.Contains {
			if strings.Contains(invocationText, containsStr) {
				matched = true
				break
			}
		}

		if matched {
			clientType = pattern.Name

			// Find the HTTP method by checking which method pattern matches
			for methodPattern, verb := range pattern.Methods {
				if strings.Contains(invocationText, methodPattern) {
					method = verb
					break
				}
			}

			// Special handling for RestTemplate.exchange() with HttpMethod.XXX
			if clientType == "RestTemplate" && method == "REQUEST" {
				if strings.Contains(invocationText, "HttpMethod.GET") {
					method = "GET"
				} else if strings.Contains(invocationText, "HttpMethod.POST") {
					method = "POST"
				} else if strings.Contains(invocationText, "HttpMethod.PUT") {
					method = "PUT"
				} else if strings.Contains(invocationText, "HttpMethod.DELETE") {
					method = "DELETE"
				} else if strings.Contains(invocationText, "HttpMethod.PATCH") {
					method = "PATCH"
				}
			}

			break
		}
	}

	if clientType == "" || method == "" {
		return nil
	}

	// Extract URL from arguments
	url = extractUrlFromJavaCall(node, content)
	if url == "" {
		// Try to get URL from the full invocation text
		url = extractUrlFromText(invocationText)
	}

	// For WebTarget, URL is often set on a different line, so allow empty URLs
	if clientType == "WebTarget" && url == "" {
		url = "(external API)"
	}

	// Skip if no URL found or URL doesn't look like an API path
	if url == "" || (!strings.HasPrefix(url, "/") && !strings.HasPrefix(url, "http") && !strings.HasPrefix(url, "(")) {
		return nil
	}

	return &ParsedHttpCall{
		HttpMethod: method,
		UrlPattern: url,
		LineNumber: lineNumber,
		ClientType: clientType,
	}
}

// extractUrlFromJavaCall tries to extract URL from method arguments
func extractUrlFromJavaCall(node *sitter.Node, content []byte) string {
	// Look for argument_list child
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "argument_list" {
			// First string argument is usually the URL
			for j := 0; j < int(child.ChildCount()); j++ {
				arg := child.Child(j)
				if arg.Type() == "string_literal" {
					url := arg.Content(content)
					url = strings.Trim(url, "\"")
					return url
				}
			}
		}
	}
	return ""
}

func methodInvocationName(node *sitter.Node, content []byte) string {
	name := ""
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "identifier":
			name = child.Content(content)
		case "argument_list":
			return name
		}
	}
	return name
}

func firstStringLiteralArg(node *sitter.Node, content []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() != "argument_list" {
			continue
		}
		for j := 0; j < int(child.ChildCount()); j++ {
			arg := child.Child(j)
			if arg.Type() != "string_literal" {
				continue
			}
			value := strings.TrimSpace(arg.Content(content))
			value = strings.Trim(value, "\"")
			return value
		}
		return ""
	}
	return ""
}

func firstClassLiteralArg(node *sitter.Node, content []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() != "argument_list" {
			continue
		}
		for j := 0; j < int(child.ChildCount()); j++ {
			arg := child.Child(j)
			if arg.Type() != "class_literal" {
				continue
			}
			return strings.TrimSpace(arg.Content(content))
		}
		return ""
	}
	return ""
}

func entityFromClassLiteral(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimSuffix(text, ".class")
	text = strings.TrimSpace(text)
	if idx := strings.LastIndex(text, "."); idx >= 0 {
		text = text[idx+1:]
	}
	return strings.TrimSpace(text)
}

func sqlStatementAccess(method string) string {
	switch method {
	case "queryForList", "queryForObject", "queryForMap", "queryForRowSet",
		"selectOne", "selectList", "selectMap", "selectCursor":
		return "read"
	case "insert", "update", "delete":
		return "write"
	default:
		return ""
	}
}

func (p *JavaParser) extractSqlStatementCall(method string, node *sitter.Node, content []byte) *ParsedSqlStatementCall {
	access := sqlStatementAccess(method)
	if access == "" {
		return nil
	}

	stmt := firstStringLiteralArg(node, content)
	if stmt == "" {
		return nil
	}

	// Most statement IDs are namespaced (com.foo.Mapper.method). Avoid obvious non-statement strings.
	if strings.HasPrefix(stmt, "/") || strings.HasPrefix(stmt, "http") {
		return nil
	}

	return &ParsedSqlStatementCall{
		StatementID: stmt,
		Access:      access,
		LineNumber:  int(node.StartPoint().Row) + 1,
	}
}

func (p *JavaParser) extractCriteriaDataAccess(method string, node *sitter.Node, content []byte) *ParsedDataAccess {
	access := ""
	switch method {
	case "from":
		access = "read"
	case "createCriteriaUpdate", "createCriteriaDelete":
		access = "write"
	default:
		return nil
	}

	classLit := firstClassLiteralArg(node, content)
	if classLit == "" {
		return nil
	}
	entity := entityFromClassLiteral(classLit)
	if entity == "" {
		return nil
	}

	return &ParsedDataAccess{
		EntityName: entity,
		Access:     access,
		LineNumber: int(node.StartPoint().Row) + 1,
		Source:     "criteria",
	}
}

// extractUrlFromText extracts URL from invocation text using string parsing
func extractUrlFromText(text string) string {
	// Look for quoted strings that look like URLs
	// Pattern: "/api/..." or "http://..."
	start := -1
	for i := 0; i < len(text); i++ {
		if text[i] == '"' {
			if start == -1 {
				start = i + 1
			} else {
				url := text[start:i]
				// Normalize path variables: {id} -> :id
				url = strings.ReplaceAll(url, "{", ":")
				url = strings.ReplaceAll(url, "}", "")
				if strings.HasPrefix(url, "/") || strings.HasPrefix(url, "http") {
					return url
				}
				start = -1
			}
		}
	}
	return ""
}

type indexedString struct {
	value string
	index int
}

type indexedMethod struct {
	method string
	index  int
}

func (p *JavaParser) getMethodName(node *sitter.Node, content []byte, currentClass string) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "identifier" {
			name := child.Content(content)
			if currentClass != "" {
				return currentClass + "." + name
			}
			return name
		}
	}
	return ""
}

func (p *JavaParser) extractHttpURLConnectionCalls(methodNode *sitter.Node, content []byte) []ParsedHttpCall {
	if !methodNodeLikelyHasHttpUrlConn(methodNode, content) {
		return nil
	}
	methodText := methodNode.Content(content)
	if !strings.Contains(methodText, "setRequestMethod(") {
		return nil
	}
	urlMatches := httpUrlConnUrlRe.FindAllStringSubmatchIndex(methodText, -1)
	methodMatches := httpUrlConnMethodRe.FindAllStringSubmatchIndex(methodText, -1)
	if len(urlMatches) == 0 || len(methodMatches) == 0 {
		return nil
	}

	urls := make([]indexedString, 0, len(urlMatches))
	for _, m := range urlMatches {
		if len(m) >= 4 {
			urls = append(urls, indexedString{
				value: methodText[m[2]:m[3]],
				index: m[0],
			})
		}
	}

	methods := make([]indexedString, 0, len(methodMatches))
	for _, m := range methodMatches {
		if len(m) >= 4 {
			methods = append(methods, indexedString{
				value: methodText[m[2]:m[3]],
				index: m[0],
			})
		}
	}

	if len(urls) == 0 || len(methods) == 0 {
		return nil
	}

	calls := make([]ParsedHttpCall, 0, len(methods))
	baseLine := int(methodNode.StartPoint().Row)
	for _, method := range methods {
		url := nearestUrlForOffset(urls, method.index)
		if url == "" {
			continue
		}
		url = strings.ReplaceAll(url, "{", ":")
		url = strings.ReplaceAll(url, "}", "")
		if url == "" || (!strings.HasPrefix(url, "/") && !strings.HasPrefix(url, "http")) {
			continue
		}
		calls = append(calls, ParsedHttpCall{
			HttpMethod: method.value,
			UrlPattern: url,
			LineNumber: lineNumberForOffset(baseLine, methodText, method.index),
			ClientType: "HttpURLConnection",
		})
	}

	return calls
}

func (p *JavaParser) extractFluentHttpCalls(methodNode *sitter.Node, content []byte) []ParsedHttpCall {
	if methodNode == nil {
		return nil
	}
	methodText := methodNode.Content(content)
	if methodText == "" {
		return nil
	}
	baseLine := int(methodNode.StartPoint().Row)

	out := make([]ParsedHttpCall, 0)
	out = append(out, p.extractWebClientFluentCalls(methodText, baseLine)...)
	out = append(out, p.extractWebTargetFluentCalls(methodText, baseLine)...)
	return out
}

func (p *JavaParser) extractWebClientFluentCalls(methodText string, baseLine int) []ParsedHttpCall {
	if !p.javaPatternEnabled("WebClient") {
		return nil
	}
	if !strings.Contains(methodText, ".uri(") {
		return nil
	}

	verbs := make([]indexedMethod, 0)
	for _, m := range webClientVerbRe.FindAllStringSubmatchIndex(methodText, -1) {
		if len(m) < 4 {
			continue
		}
		verbs = append(verbs, indexedMethod{
			method: strings.ToUpper(methodText[m[2]:m[3]]),
			index:  m[0],
		})
	}
	for _, m := range webClientMethodVerbRe.FindAllStringSubmatchIndex(methodText, -1) {
		if len(m) < 4 {
			continue
		}
		verbs = append(verbs, indexedMethod{
			method: strings.ToUpper(methodText[m[2]:m[3]]),
			index:  m[0],
		})
	}
	if len(verbs) == 0 {
		return nil
	}

	uriMatches := webClientUriRe.FindAllStringSubmatchIndex(methodText, -1)
	if len(uriMatches) == 0 {
		return nil
	}

	calls := make([]ParsedHttpCall, 0, len(uriMatches))
	for _, m := range uriMatches {
		if len(m) < 4 {
			continue
		}
		verb := nearestMethodBeforeOffset(verbs, m[0])
		if verb == "" {
			continue
		}
		url := normalizeJavaHttpURL(methodText[m[2]:m[3]])
		if url == "" {
			continue
		}
		calls = append(calls, ParsedHttpCall{
			HttpMethod: verb,
			UrlPattern: url,
			LineNumber: lineNumberForOffset(baseLine, methodText, m[0]),
			ClientType: "WebClient",
		})
	}

	return calls
}

func (p *JavaParser) extractWebTargetFluentCalls(methodText string, baseLine int) []ParsedHttpCall {
	if !p.javaPatternEnabled("WebTarget") {
		return nil
	}
	if !strings.Contains(methodText, ".request(") {
		return nil
	}

	paths := make([]indexedString, 0)
	for _, m := range webTargetPathRe.FindAllStringSubmatchIndex(methodText, -1) {
		if len(m) < 4 {
			continue
		}
		paths = append(paths, indexedString{
			value: normalizeJavaHttpURL(methodText[m[2]:m[3]]),
			index: m[0],
		})
	}
	if len(paths) == 0 {
		return nil
	}

	calls := make([]ParsedHttpCall, 0)
	appendCall := func(method, url string, idx int) {
		if method == "" || url == "" {
			return
		}
		calls = append(calls, ParsedHttpCall{
			HttpMethod: strings.ToUpper(method),
			UrlPattern: url,
			LineNumber: lineNumberForOffset(baseLine, methodText, idx),
			ClientType: "WebTarget",
		})
	}

	for _, m := range webTargetRequestVerbRe.FindAllStringSubmatchIndex(methodText, -1) {
		if len(m) < 4 {
			continue
		}
		method := methodText[m[2]:m[3]]
		path := nearestUrlForOffset(paths, m[0])
		appendCall(method, path, m[0])
	}
	for _, m := range webTargetRequestMethodVerbRe.FindAllStringSubmatchIndex(methodText, -1) {
		if len(m) < 4 {
			continue
		}
		method := methodText[m[2]:m[3]]
		path := nearestUrlForOffset(paths, m[0])
		appendCall(method, path, m[0])
	}

	return calls
}

func nearestMethodBeforeOffset(methods []indexedMethod, offset int) string {
	best := ""
	bestIdx := -1
	for _, method := range methods {
		if method.index <= offset && method.index >= bestIdx {
			best = method.method
			bestIdx = method.index
		}
	}
	return best
}

func normalizeJavaHttpURL(url string) string {
	url = strings.TrimSpace(url)
	url = strings.ReplaceAll(url, "{", ":")
	url = strings.ReplaceAll(url, "}", "")
	if url == "" {
		return ""
	}
	if strings.HasPrefix(url, "/") || strings.HasPrefix(url, "http") {
		return url
	}
	return ""
}

func (p *JavaParser) javaPatternEnabled(name string) bool {
	for _, pattern := range p.config.GetJavaPatterns() {
		if pattern.Name == name {
			return true
		}
	}
	return false
}

func nearestUrlForOffset(urls []indexedString, offset int) string {
	if len(urls) == 0 {
		return ""
	}
	best := urls[0].value
	bestIdx := urls[0].index
	for _, u := range urls {
		if u.index <= offset && u.index >= bestIdx {
			best = u.value
			bestIdx = u.index
		}
	}
	return best
}

func lineNumberForOffset(baseLine int, text string, offset int) int {
	if offset > len(text) {
		offset = len(text)
	}
	line := baseLine + 1
	for i := 0; i < offset; i++ {
		if text[i] == '\n' {
			line++
		}
	}
	return line
}

func methodNodeLikelyHasHttpUrlConn(node *sitter.Node, content []byte) bool {
	targets := map[string]struct{}{
		"setRequestMethod":  {},
		"openConnection":    {},
		"HttpURLConnection": {},
		"URL":               {},
	}
	stack := []*sitter.Node{node}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch n.Type() {
		case "identifier", "type_identifier":
			text := n.Content(content)
			if _, ok := targets[text]; ok {
				return true
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			stack = append(stack, n.Child(i))
		}
	}
	return false
}

// extractBeanDefinitions extracts @Bean methods from @Configuration classes
func (p *JavaParser) extractBeanDefinitions(classes []ParsedClass, functions []ParsedFunction) []ParsedBeanDefinition {
	var beans []ParsedBeanDefinition

	// Build a set of @Configuration class names
	configClasses := make(map[string]bool)
	for _, class := range classes {
		for _, ann := range class.Annotations {
			if ann.Name == "Configuration" {
				configClasses[class.Name] = true
				break
			}
		}
	}

	// Look for @Bean methods in config classes
	for _, fn := range functions {
		// Check if function belongs to a @Configuration class
		// Function names are stored as "ClassName.methodName"
		className := classNameFromMethodName(fn.Name)
		if className == "" {
			continue
		}
		methodName := strings.TrimPrefix(fn.Name, className+".")

		if !configClasses[className] {
			continue
		}

		// Check for @Bean annotation
		for _, ann := range fn.Annotations {
			if ann.Name == "Bean" {
				bean := ParsedBeanDefinition{
					ConfigClassName: className,
					MethodName:      methodName,
					BeanType:        fn.ReturnType,
					LineNumber:      fn.StartLine,
				}

				// Get bean name from @Bean("name") or use method name
				if val, ok := ann.Values["value"]; ok {
					bean.BeanName = fmt.Sprintf("%v", val)
				} else if val, ok := ann.Values["name"]; ok {
					bean.BeanName = fmt.Sprintf("%v", val)
				} else {
					bean.BeanName = methodName
				}

				// Check for @Primary
				for _, fnAnn := range fn.Annotations {
					if fnAnn.Name == "Primary" {
						bean.IsPrimary = true
						break
					}
				}

				// Check for @Qualifier
				for _, fnAnn := range fn.Annotations {
					if fnAnn.Name == "Qualifier" {
						if val, ok := fnAnn.Values["value"]; ok {
							bean.Qualifiers = append(bean.Qualifiers, fmt.Sprintf("%v", val))
						}
					}
				}

				beans = append(beans, bean)
				break
			}
		}
	}

	return beans
}

// extractEventListeners extracts @EventListener methods from functions
func (p *JavaParser) extractEventListeners(functions []ParsedFunction) []ParsedEventListener {
	var listeners []ParsedEventListener

	for _, fn := range functions {
		// Extract class name from function name
		className := classNameFromMethodName(fn.Name)
		if className == "" {
			continue
		}
		methodName := strings.TrimPrefix(fn.Name, className+".")

		for _, ann := range fn.Annotations {
			if ann.Name == "EventListener" || ann.Name == "TransactionalEventListener" {
				listener := ParsedEventListener{
					ClassName:  className,
					MethodName: methodName,
					LineNumber: fn.StartLine,
				}

				// Get event types from annotation value or method parameters
				if val, ok := ann.Values["value"]; ok {
					switch v := val.(type) {
					case string:
						listener.EventTypes = []string{v}
					case []interface{}:
						for _, ev := range v {
							listener.EventTypes = append(listener.EventTypes, fmt.Sprintf("%v", ev))
						}
					}
				} else if val, ok := ann.Values["classes"]; ok {
					switch v := val.(type) {
					case string:
						listener.EventTypes = []string{v}
					case []interface{}:
						for _, ev := range v {
							listener.EventTypes = append(listener.EventTypes, fmt.Sprintf("%v", ev))
						}
					}
				}

				// Get condition if present
				if val, ok := ann.Values["condition"]; ok {
					listener.Condition = fmt.Sprintf("%v", val)
				}

				// Check for @Async
				for _, fnAnn := range fn.Annotations {
					if fnAnn.Name == "Async" {
						listener.IsAsync = true
						break
					}
				}

				// If no event types from annotation, try to get from parameter types
				if len(listener.EventTypes) == 0 && fn.Signature != nil {
					for _, paramType := range fn.Signature.ParameterTypes {
						if strings.Contains(paramType, "Event") {
							listener.EventTypes = append(listener.EventTypes, paramType)
						}
					}
				}

				listeners = append(listeners, listener)
				break
			}
		}
	}

	return listeners
}

// extractScheduledMethods extracts @Scheduled methods from functions
func (p *JavaParser) extractScheduledMethods(functions []ParsedFunction) []ParsedScheduledMethod {
	var scheduled []ParsedScheduledMethod

	for _, fn := range functions {
		// Extract class name from function name
		className := classNameFromMethodName(fn.Name)
		if className == "" {
			continue
		}
		methodName := strings.TrimPrefix(fn.Name, className+".")

		for _, ann := range fn.Annotations {
			if ann.Name == "Scheduled" {
				sched := ParsedScheduledMethod{
					ClassName:  className,
					MethodName: methodName,
					LineNumber: fn.StartLine,
				}

				// Extract scheduling parameters
				if val, ok := ann.Values["cron"]; ok {
					sched.Cron = fmt.Sprintf("%v", val)
				}
				if val, ok := ann.Values["fixedRate"]; ok {
					sched.FixedRate = parseToInt64(val)
				}
				if val, ok := ann.Values["fixedRateString"]; ok {
					sched.FixedRate = parseToInt64(val)
				}
				if val, ok := ann.Values["fixedDelay"]; ok {
					sched.FixedDelay = parseToInt64(val)
				}
				if val, ok := ann.Values["fixedDelayString"]; ok {
					sched.FixedDelay = parseToInt64(val)
				}
				if val, ok := ann.Values["initialDelay"]; ok {
					sched.InitialDelay = parseToInt64(val)
				}
				if val, ok := ann.Values["initialDelayString"]; ok {
					sched.InitialDelay = parseToInt64(val)
				}

				scheduled = append(scheduled, sched)
				break
			}
		}
	}

	return scheduled
}

// parseToInt64 converts various types to int64
func parseToInt64(val interface{}) int64 {
	switch v := val.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		// Try to parse as number, ignore ${...} placeholders
		if strings.HasPrefix(v, "${") {
			return 0
		}
		var result int64
		fmt.Sscanf(v, "%d", &result)
		return result
	}
	return 0
}

// ==========================================
// Feature-Complete Java Parser Functions
// ==========================================

// extractJpaPatterns extracts JPA entities and relationships from classes
func (p *JavaParser) extractJpaPatterns(classes []ParsedClass) ([]ParsedJpaEntity, []ParsedJpaRelationship) {
	var entities []ParsedJpaEntity
	var relationships []ParsedJpaRelationship

	for _, class := range classes {
		// Check if class has @Entity annotation
		isEntity := false
		var tableName, schemaName, catalogName string

		for _, ann := range class.Annotations {
			switch ann.Name {
			case "Entity":
				isEntity = true
			case "Table":
				if name, ok := ann.Values["name"].(string); ok {
					tableName = name
				}
				if schema, ok := ann.Values["schema"].(string); ok {
					schemaName = schema
				}
				if catalog, ok := ann.Values["catalog"].(string); ok {
					catalogName = catalog
				}
			}
		}

		if isEntity {
			// Default table name to class name if not specified
			if tableName == "" {
				tableName = simpleJavaDeclarationName(class.Name)
			}

			entities = append(entities, ParsedJpaEntity{
				ClassName: class.Name,
				TableName: tableName,
				Schema:    schemaName,
				Catalog:   catalogName,
			})

			// Extract relationships from fields
			for _, field := range class.Fields {
				if rel := extractJpaRelationship(class.Name, field); rel != nil {
					rel.TargetEntity = javaTypeInScope(rel.TargetEntity, class.Name, classes)
					relationships = append(relationships, *rel)
				}
			}
		}
	}

	return entities, relationships
}

// Resolve an in-file JPA target in its lexical type scope before considering a
// top-level name. Keep external/unknown types textual for workspace resolution.
func javaTypeInScope(name, owner string, classes []ParsedClass) string {
	for scope := owner; ; {
		candidate := name
		if scope != "" {
			candidate = scope + "." + name
		}
		for _, cls := range classes {
			if cls.Name == candidate {
				return candidate
			}
		}
		if scope == "" {
			return name
		}
		scope = classNameFromMethodName(scope)
	}
}

// extractJpaRelationship checks a field for JPA relationship annotations
func extractJpaRelationship(className string, field ParsedField) *ParsedJpaRelationship {
	var relationType, mappedBy, joinColumn, fetchType string
	var cascadeTypes []string

	for _, ann := range field.Annotations {
		switch ann.Name {
		case "OneToMany", "ManyToOne", "OneToOne", "ManyToMany":
			relationType = ann.Name
			if mb, ok := ann.Values["mappedBy"].(string); ok {
				mappedBy = mb
			}
			if ft, ok := ann.Values["fetch"].(string); ok {
				fetchType = ft
			}
			if ct, ok := ann.Values["cascade"].([]interface{}); ok {
				for _, c := range ct {
					if cs, ok := c.(string); ok {
						cascadeTypes = append(cascadeTypes, cs)
					}
				}
			}
		case "JoinColumn":
			if name, ok := ann.Values["name"].(string); ok {
				joinColumn = name
			}
		}
	}

	if relationType == "" {
		return nil
	}

	// Extract target entity from field type
	targetEntity := field.FieldType
	// Handle generic types like List<User> -> User
	if len(field.TypeParameters) > 0 {
		targetEntity = field.TypeParameters[0]
	}

	return &ParsedJpaRelationship{
		SourceEntity:   className,
		TargetEntity:   targetEntity,
		RelationType:   relationType,
		SourceField:    field.Name,
		MappedBy:       mappedBy,
		JoinColumnName: joinColumn,
		FetchType:      fetchType,
		CascadeTypes:   cascadeTypes,
		LineNumber:     field.StartLine,
	}
}

// extractLombokMethods extracts synthetic methods generated by Lombok annotations
func (p *JavaParser) extractLombokMethods(classes []ParsedClass) []ParsedSyntheticMethod {
	var methods []ParsedSyntheticMethod

	for _, class := range classes {
		// Check for class-level Lombok annotations
		hasGetter := false
		hasSetter := false
		hasData := false
		hasNoArgsConstructor := false
		hasAllArgsConstructor := false
		classLine := class.StartLine

		for _, ann := range class.Annotations {
			switch ann.Name {
			case "Getter":
				hasGetter = true
			case "Setter":
				hasSetter = true
			case "Data":
				hasData = true
				hasGetter = true
				hasSetter = true
			case "NoArgsConstructor":
				hasNoArgsConstructor = true
			case "AllArgsConstructor":
				hasAllArgsConstructor = true
			}
		}

		// Generate synthetic methods for fields
		for _, field := range class.Fields {
			// Check for field-level annotations
			fieldHasGetter := hasGetter
			fieldHasSetter := hasSetter

			for _, ann := range field.Annotations {
				if ann.Name == "Getter" {
					fieldHasGetter = true
				}
				if ann.Name == "Setter" {
					fieldHasSetter = true
				}
			}

			// Skip static fields
			isStatic := false
			for _, mod := range field.Modifiers {
				if mod == "static" {
					isStatic = true
					break
				}
			}
			if isStatic {
				continue
			}

			// Generate getter
			if fieldHasGetter {
				getterName := "get" + capitalizeFirst(field.Name)
				// Boolean fields use "is" prefix
				if field.FieldType == "boolean" || field.FieldType == "Boolean" {
					getterName = "is" + capitalizeFirst(field.Name)
				}
				methods = append(methods, ParsedSyntheticMethod{
					Name:       getterName,
					ClassName:  class.Name,
					ReturnType: field.FieldType,
					Params:     []string{},
					Source:     "lombok",
					Annotation: "@Getter",
					FieldName:  field.Name,
					LineNumber: field.StartLine,
				})
			}

			// Generate setter
			if fieldHasSetter {
				setterName := "set" + capitalizeFirst(field.Name)
				methods = append(methods, ParsedSyntheticMethod{
					Name:       setterName,
					ClassName:  class.Name,
					ReturnType: "void",
					Params:     []string{field.FieldType},
					Source:     "lombok",
					Annotation: "@Setter",
					FieldName:  field.Name,
					LineNumber: field.StartLine,
				})
			}
		}

		// Generate @Data methods (toString, equals, hashCode)
		if hasData {
			methods = append(methods,
				ParsedSyntheticMethod{
					Name:       "toString",
					ClassName:  class.Name,
					ReturnType: "String",
					Params:     []string{},
					Source:     "lombok",
					Annotation: "@Data",
					LineNumber: classLine,
				},
				ParsedSyntheticMethod{
					Name:       "equals",
					ClassName:  class.Name,
					ReturnType: "boolean",
					Params:     []string{"Object"},
					Source:     "lombok",
					Annotation: "@Data",
					LineNumber: classLine,
				},
				ParsedSyntheticMethod{
					Name:       "hashCode",
					ClassName:  class.Name,
					ReturnType: "int",
					Params:     []string{},
					Source:     "lombok",
					Annotation: "@Data",
					LineNumber: classLine,
				},
			)
		}

		// Generate @NoArgsConstructor
		if hasNoArgsConstructor {
			methods = append(methods, ParsedSyntheticMethod{
				Name:       simpleJavaDeclarationName(class.Name),
				ClassName:  class.Name,
				ReturnType: "",
				Params:     []string{},
				Source:     "lombok",
				Annotation: "@NoArgsConstructor",
				LineNumber: classLine,
			})
		}

		// Generate @AllArgsConstructor
		if hasAllArgsConstructor {
			var params []string
			for _, field := range class.Fields {
				isStatic := false
				for _, mod := range field.Modifiers {
					if mod == "static" {
						isStatic = true
						break
					}
				}
				if !isStatic {
					params = append(params, field.FieldType)
				}
			}
			methods = append(methods, ParsedSyntheticMethod{
				Name:       simpleJavaDeclarationName(class.Name),
				ClassName:  class.Name,
				ReturnType: "",
				Params:     params,
				Source:     "lombok",
				Annotation: "@AllArgsConstructor",
				LineNumber: classLine,
			})
		}
	}

	return methods
}

// capitalizeFirst capitalizes the first letter of a string
func capitalizeFirst(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// extractLambda extracts a lambda expression
func (p *JavaParser) extractLambda(node *sitter.Node, content []byte, currentClass, currentMethod string) *ParsedLambda {
	lambda := &ParsedLambda{
		ContainingClass:  currentClass,
		ContainingMethod: currentMethod,
		StartLine:        int(node.StartPoint().Row) + 1,
		EndLine:          int(node.EndPoint().Row) + 1,
	}

	// Extract parameters
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "identifier":
			// Single parameter without parentheses: x -> x + 1
			lambda.Parameters = append(lambda.Parameters, child.Content(content))
		case "inferred_parameters", "formal_parameters":
			// Multiple parameters: (x, y) -> x + y
			for j := 0; j < int(child.ChildCount()); j++ {
				param := child.Child(j)
				if param.Type() == "identifier" || param.Type() == "formal_parameter" {
					// Get just the parameter name
					paramText := param.Content(content)
					// For formal_parameter, extract just the name
					parts := strings.Fields(paramText)
					if len(parts) > 0 {
						lambda.Parameters = append(lambda.Parameters, parts[len(parts)-1])
					}
				}
			}
		}
	}

	return lambda
}

// extractMethodReference extracts a method reference and adds it as a function call
func (p *JavaParser) extractMethodReference(node *sitter.Node, content []byte, result *ParsedFile, currentMethod string) {
	var typeName, methodName string
	isConstructor := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "type_identifier", "identifier":
			if typeName == "" {
				typeName = child.Content(content)
			} else if methodName == "" {
				methodName = child.Content(content)
			}
		case "new":
			isConstructor = true
			methodName = "<init>"
		}
	}

	if methodName == "" && !isConstructor {
		return
	}

	calleeName := methodName
	if typeName != "" {
		calleeName = typeName + "." + methodName
	}

	result.FunctionCalls[currentMethod] = append(result.FunctionCalls[currentMethod], ParsedFunctionCall{
		CalleeName: calleeName,
		Receiver:   typeName,
		MethodName: methodName,
		LineNumber: int(node.StartPoint().Row) + 1,
		IsAsync:    false,
	})
}

// extractEnumConstants extracts enum constant values from an enum body
func (p *JavaParser) extractEnumConstants(node *sitter.Node, content []byte) []ParsedEnumConstant {
	var constants []ParsedEnumConstant
	ordinal := 0

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "enum_constant" {
			constant := ParsedEnumConstant{
				Ordinal: ordinal,
			}

			// Extract constant name and arguments
			for j := 0; j < int(child.ChildCount()); j++ {
				part := child.Child(j)
				switch part.Type() {
				case "identifier":
					constant.Name = part.Content(content)
				case "argument_list":
					// Extract constructor arguments
					for k := 0; k < int(part.ChildCount()); k++ {
						arg := part.Child(k)
						if arg.Type() != "(" && arg.Type() != ")" && arg.Type() != "," {
							constant.Arguments = append(constant.Arguments, arg.Content(content))
						}
					}
				}
			}

			if constant.Name != "" {
				constants = append(constants, constant)
				ordinal++
			}
		}
	}

	return constants
}
