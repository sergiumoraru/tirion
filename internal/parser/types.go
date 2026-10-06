package parser

// ParsedAnnotation represents an annotation on any entity (class, method, field, parameter)
type ParsedAnnotation struct {
	Name       string
	Values     map[string]interface{} // {"value": "/api/users", "method": "GET"}
	LineNumber int
}

// ParsedTypeParameter represents a generic type parameter (T, K extends Comparable, etc.)
type ParsedTypeParameter struct {
	Name      string   // "T", "K", "V"
	Index     int      // Position in type parameter list
	Bounds    []string // ["Comparable", "Serializable"]
	BoundType string   // "extends" or "super"
}

// ParsedField represents a field in a class or interface
type ParsedField struct {
	Name           string
	FieldType      string   // The declared type
	TypeParameters []string // For generics: ["String", "Integer"] for Map<String, Integer>
	Modifiers      []string // ["private", "final", "static"]
	StartLine      int
	IsInjected     bool               // @Autowired, @Inject, @Value
	InjectionType  string             // "autowired", "inject", "value", "resource"
	Annotations    []ParsedAnnotation // All annotations on this field
}

// ParsedConstructorParam represents a parameter in a constructor
type ParsedConstructorParam struct {
	Name            string
	ParamType       string
	Index           int
	IsInjected      bool   // If the constructor is for DI
	Annotation      string // Source annotation name, including configured custom annotations.
	AnnotationValue string // Value from annotation
}

// ParsedConstructor represents a constructor declaration
type ParsedConstructor struct {
	ClassName   string
	Parameters  []ParsedConstructorParam
	StartLine   int
	EndLine     int
	Annotations []ParsedAnnotation
}

// ParsedMethodSignature holds method signature info for overload resolution
type ParsedMethodSignature struct {
	Signature       string   // "processOrder(Order,User)"
	ParameterTypes  []string // ["Order", "User"]
	ReturnType      string
	IsOverride      bool
	OverridesMethod string // Name of overridden method if known
}

type ParsedFunction struct {
	Name           string
	StartLine      int
	EndLine        int
	Params         []string
	ParamTypes     []string // Parameter types: ["string", "number", "User"]
	ReturnType     string
	IsExported     bool
	IsAsync        bool
	SourceCode     string
	Modifiers      []string               // ["public", "static", "synchronized"]
	Annotations    []ParsedAnnotation     // Annotations on this method
	Signature      *ParsedMethodSignature // Method signature for overload resolution
	TypeParameters []ParsedTypeParameter  // Generic type parameters on method
	ThrowsTypes    []string               // Exception types from throws clause
}

type ParsedClass struct {
	Name           string
	StartLine      int
	EndLine        int
	ExtendsClass   string
	Implements     []string
	IsExported     bool
	Methods        []ParsedFunction
	IsInterface    bool                  // Kept for backward compat, prefer ParsedInterface
	IsEnum         bool                  // Distinguish enum declarations
	IsAbstract     bool                  // Abstract class
	IsRecord       bool                  // Java 14+ record
	Modifiers      []string              // All modifiers: ["public", "abstract", "final"]
	TypeParameters []ParsedTypeParameter // Generic type parameters: class Foo<T extends Bar>
	Fields         []ParsedField         // Class fields
	Constructors   []ParsedConstructor   // Class constructors
	Annotations    []ParsedAnnotation    // Annotations on the class
	InnerClasses   []ParsedClass         // Nested/inner classes
	EnumConstants  []ParsedEnumConstant  // Enum values (only if IsEnum)
}

// ParsedInterface represents a Java interface declaration (separate from class)
type ParsedInterface struct {
	Name              string
	StartLine         int
	EndLine           int
	ExtendsInterfaces []string              // Interfaces can extend multiple interfaces
	Methods           []ParsedFunction      // Interface methods (default/abstract)
	IsExported        bool                  // Public visibility
	IsFunctional      bool                  // @FunctionalInterface
	TypeParameters    []ParsedTypeParameter // Generic type parameters
	Fields            []ParsedField         // Interface fields (constants)
	Annotations       []ParsedAnnotation    // Annotations on interface
}

type ParsedImport struct {
	Path        string
	Names       []string
	IsDefault   bool
	IsNamespace bool
}

type ParsedFunctionCall struct {
	CalleeName string // Full call name (for backward compat): "this.service.get"
	Receiver   string // The receiver object: "this.service", "axios"
	MethodName string // The method being called: "get", "fetchUsers"
	LineNumber int
	IsAsync    bool
	// True for speculative arguments without supported API invocation semantics.
	// Supported callback positions and direct calls leave this false.
	IsCallbackArgument bool
}

type ParsedEndpoint struct {
	Path        string
	Method      string
	HandlerName string
	LineNumber  int
}

type ParsedHttpCall struct {
	HttpMethod string // GET, POST, PUT, DELETE, etc.
	UrlPattern string // /api/pages/:id or /api/users
	LineNumber int
	ClientType string // axios, fetch, HttpClient, RestTemplate
}

type ParsedDataAccess struct {
	EntityName string // e.g., "Pages", "Invoice"
	Access     string // "read" | "write"
	LineNumber int
	Source     string // "criteria", "sqlmap", "sql_literal", etc.
}

type ParsedIBMiExport struct {
	FunctionName string // Parsed function name when directly known
	ExportName   string // External symbol or program name
	ObjectName   string // Program/service program object identity
	ObjectType   string // "program", "procedure", "service_program"
	SourceType   string // "program_entry", "procedure_export", "binder_export"
	LineNumber   int
}

type ParsedIBMiBinding struct {
	OwnerObject      string // Program/service program that owns or references this binding
	BindingName      string // Binding directory or referenced service object
	BindingType      string // "bnddir_ref", "bnddir_entry", "binder_source"
	TargetObject     string // Service program target when known
	TargetObjectType string // "srvpgm", "pgm", etc.
	LineNumber       int
}

type ParsedSqlStatementCall struct {
	StatementID string // e.g., "com.example.dao.PageSqlMap.getPageById"
	Access      string // "read" | "write"
	LineNumber  int
}

type ParsedSqsProducer struct {
	QueueName  string // Queue identity or configuration symbol
	LineNumber int
}

type ParsedSqsConsumer struct {
	QueueName     string // Queue identity or configuration symbol
	HandlerMethod string // Source-backed handler selected by the configured framework
	ClassName     string // class containing the constructor
}

type ParsedResourceAlias struct {
	Alias      string // Configuration key, such as TASK_QUEUE
	Value      string // Configured resource, such as task-events
	Kind       string // e.g., config
	LineNumber int
}

// ParsedHttpInterfaceMethod represents a Retrofit/Feign annotated interface method
type ParsedHttpInterfaceMethod struct {
	InterfaceName string // Name of the interface
	MethodName    string // Name of the method
	HttpMethod    string // GET, POST, PUT, DELETE
	UrlPattern    string // /users/{id}
	LineNumber    int
}

// ParsedBeanDefinition represents a @Bean method in a @Configuration class
type ParsedBeanDefinition struct {
	ConfigClassName string   // Name of the @Configuration class
	MethodName      string   // Name of the @Bean method
	BeanName        string   // Bean name (from @Bean("name") or method name)
	BeanType        string   // Return type of the method
	Qualifiers      []string // Additional qualifiers
	IsPrimary       bool     // @Primary annotation present
	LineNumber      int
}

// ParsedEventListener represents an @EventListener method
type ParsedEventListener struct {
	ClassName  string   // Class containing the listener
	MethodName string   // Method name
	EventTypes []string // Event class names from parameter or annotation
	Condition  string   // SpEL condition from @EventListener(condition="...")
	IsAsync    bool     // @Async annotation present
	LineNumber int
}

// ParsedScheduledMethod represents a @Scheduled method
type ParsedScheduledMethod struct {
	ClassName    string // Class containing the method
	MethodName   string // Method name
	Cron         string // Cron expression
	FixedRate    int64  // Fixed rate in ms
	FixedDelay   int64  // Fixed delay in ms
	InitialDelay int64  // Initial delay in ms
	LineNumber   int
}

// ParsedJpaEntity represents a JPA @Entity annotated class
type ParsedJpaEntity struct {
	ClassName string
	TableName string // From @Table(name=...)
	Schema    string // From @Table(schema=...)
	Catalog   string // From @Table(catalog=...)
}

// ParsedJpaRelationship represents entity relationships (@OneToMany, @ManyToOne, etc.)
type ParsedJpaRelationship struct {
	SourceEntity   string // Class name containing the relationship field
	TargetEntity   string // Target entity class name (from field type)
	RelationType   string // "OneToMany", "ManyToOne", "OneToOne", "ManyToMany"
	SourceField    string // Field name in source entity
	MappedBy       string // For bidirectional relationships
	JoinColumnName string // From @JoinColumn(name=...)
	FetchType      string // "LAZY" or "EAGER"
	CascadeTypes   []string
	LineNumber     int
}

// ParsedSyntheticMethod represents a method generated by Lombok or records
type ParsedSyntheticMethod struct {
	Name       string   // Method name (e.g., "getName", "setName")
	ClassName  string   // Class containing this synthetic method
	ReturnType string   // Return type
	Params     []string // Parameter types
	Source     string   // "lombok", "record", "enum"
	Annotation string   // "@Getter", "@Data", etc.
	FieldName  string   // For getter/setter: the field it's derived from
	LineNumber int      // Line of the annotation that generates this
}

// ParsedLambda represents a lambda expression
type ParsedLambda struct {
	ContainingClass  string
	ContainingMethod string
	Parameters       []string
	StartLine        int
	EndLine          int
}

// ParsedMethodReference represents a method reference (Class::method)
type ParsedMethodReference struct {
	ClassName     string // Class or instance type
	MethodName    string // Referenced method (or "<init>" for constructor)
	ReferenceType string // "static", "bound", "unbound", "constructor"
	LineNumber    int
}

// ParsedEnumConstant represents an enum constant value
type ParsedEnumConstant struct {
	Name      string   // Constant name (e.g., "MONDAY")
	Ordinal   int      // Position in enum (0-based)
	Arguments []string // Constructor arguments if any
}

// ParsedTypeAlias represents a TypeScript type alias declaration
type ParsedTypeAlias struct {
	Name       string   // The type alias name
	Definition string   // The type definition (e.g., "string | number")
	TypeParams []string // Generic parameters (e.g., ["T", "K"])
	StartLine  int
	EndLine    int
	IsExported bool
}

// ParsedHookCall represents a React hook call
type ParsedHookCall struct {
	HookName     string   // useState, useEffect, useCustomHook
	Dependencies []string // Dependency array for useEffect/useCallback/useMemo
	InitialValue string   // For useState
	FunctionName string   // Containing function name
	LineNumber   int
	IsCustomHook bool   // Starts with "use" but not built-in
	Origin       string // react, vue_reactivity, vue_lifecycle, vue_macro, vue_router, pinia, vuelidate, custom
}

// ParsedVueComponentContract represents a public contract declared by a Vue
// <script setup> macro such as defineProps, defineEmits, defineExpose, or defineModel.
type ParsedVueComponentContract struct {
	Kind       string // props, emits, expose, model
	FieldName  string // prop/event/exposed member/model name
	FieldType  string // declared type when available
	IsRequired bool
	LineNumber int
	Definition string // raw macro argument/type snippet for context
}

// ParsedPiniaStore represents a Pinia defineStore declaration.
type ParsedPiniaStore struct {
	StoreID     string
	LineNumber  int
	StateFields []string
	Getters     []string
	Actions     []string
}

// ParsedGraphQLOperation represents a named GraphQL query/mutation/subscription document.
type ParsedGraphQLOperation struct {
	Name          string
	OperationType string // query, mutation, subscription
	LineNumber    int
}

// ParsedGraphQLOperationUsage represents a JS/TS/Vue usage site of an imported GraphQL document.
type ParsedGraphQLOperationUsage struct {
	ImportPath   string
	ImportedAs   string
	FunctionName string
	LineNumber   int
}

// ParsedGraphQLBackendEntrypoint represents a backend GraphQL registration/entrypoint convention.
type ParsedGraphQLBackendEntrypoint struct {
	HandlerName      string
	RegistrationKind string // launcher.register, apollo_server, etc.
	ControllersPath  string
	LineNumber       int
}

// ParsedGraphQLOperationResolver represents a backend resolver export for an operation name.
type ParsedGraphQLOperationResolver struct {
	OperationName string
	OperationType string // query, mutation, subscription
	ResolverName  string
	LineNumber    int
}

// ParsedGraphQLOperationPermission represents an authorization rule attached to a GraphQL operation.
type ParsedGraphQLOperationPermission struct {
	OperationName  string
	OperationType  string // query, mutation, subscription
	RuleExpression string
	LineNumber     int
}

// ParsedAzureTrigger represents an Azure Functions binding trigger from function.json.
type ParsedAzureTrigger struct {
	FunctionName string
	TriggerType  string   // httpTrigger, timerTrigger, queueTrigger, serviceBusTrigger, etc.
	Direction    string   // in / out
	BindingName  string   // req, queueItem, cleanupTimer, etc.
	Route        string   // HTTP route when present
	Methods      []string // HTTP methods when present
	AuthLevel    string   // anonymous / function / admin for HTTP triggers
	Connection   string   // configured binding connection when present
	Schedule     string   // timer schedule when present
	ResourceName string   // queue/topic/container/etc. when present
	ScriptFile   string
	LineNumber   int
}

// ParsedAzureHostConfig represents route-related host.json settings for Azure Functions.
type ParsedAzureHostConfig struct {
	RoutePrefix string
	LineNumber  int
}

// ParsedGatewayRoute represents a public gateway/API-management route and its backend mapping.
type ParsedGatewayRoute struct {
	GatewayType   string
	APIName       string
	OperationName string
	PublicMethod  string
	PublicPath    string
	BackendMethod string
	BackendURL    string
	BackendPath   string
	BackendID     string
	LineNumber    int
}

type ParseFailureKind string

const (
	ParseFailureNone              ParseFailureKind = ""
	ParseFailureSyntaxUnsupported ParseFailureKind = "syntax_unsupported"
	ParseFailureTimeout           ParseFailureKind = "timeout"
	ParseFailureInternal          ParseFailureKind = "internal_error"
)

type ParseDiagnostics struct {
	FailureKind ParseFailureKind
	Message     string
	PartialTree bool // A tree was produced, but contains unsupported or invalid syntax.
	// Recovered marks an internal failure caused by a panic while parsing this
	// file. It is specific to the file's content, so callers record it and move on;
	// internal failures without it (invalid parser configuration) are systemic.
	Recovered bool
}

func (d ParseDiagnostics) Failed() bool {
	return d.FailureKind != ParseFailureNone
}

// Usable reports whether the extracted facts may be consumed: a clean parse or a
// recovered partial tree, which cmd/parse stores as well. Timeouts, recovered
// panics and missing trees yield no trustworthy facts.
func (d ParseDiagnostics) Usable() bool {
	return !d.Failed() || d.PartialTree
}

// Systemic reports an internal failure that is not tied to one file's content
// (for example invalid parser configuration). It affects every file, so batch
// callers abort instead of skipping the file.
func (d ParseDiagnostics) Systemic() bool {
	return d.FailureKind == ParseFailureInternal && !d.Recovered
}

type ParsedFile struct {
	Path             string
	Language         string
	JavaPackage      string // Source-declared package; empty for the unnamed package.
	ParseDiagnostics ParseDiagnostics
	Functions        []ParsedFunction
	Classes          []ParsedClass
	Imports          []ParsedImport
	FunctionCalls    map[string][]ParsedFunctionCall // function name -> calls
	// Java local variable types by function: funcName -> varName -> type
	LocalVarTypes        map[string]map[string]string
	Endpoints            []ParsedEndpoint
	HttpCalls            map[string][]ParsedHttpCall         // function name -> HTTP calls made
	DataAccesses         map[string][]ParsedDataAccess       // function name -> entity read/write evidence
	IBMiExports          []ParsedIBMiExport                  // IBM i program/procedure/binder exports
	IBMiBindings         []ParsedIBMiBinding                 // IBM i binding-directory and binder references
	SqlStatements        map[string][]ParsedSqlStatementCall // function name -> statement ID calls (SqlMapClient/SqlSession)
	SqsProducers         map[string][]ParsedSqsProducer      // function name -> SQS sendMessage calls
	SqsConsumers         []ParsedSqsConsumer                 // class-level SQS consumers
	ResourceAliases      []ParsedResourceAlias               // config/env indirections to runtime resource names
	Interfaces           []ParsedInterface                   // Interface declarations (separate from classes)
	HttpInterfaceMethods []ParsedHttpInterfaceMethod         // Retrofit/Feign annotated methods
	// Spring framework declarations
	BeanDefinitions  []ParsedBeanDefinition  // @Bean methods in @Configuration classes
	EventListeners   []ParsedEventListener   // @EventListener methods
	ScheduledMethods []ParsedScheduledMethod // @Scheduled methods
	// Java persistence and generated declarations
	JpaEntities      []ParsedJpaEntity       // JPA @Entity classes
	JpaRelationships []ParsedJpaRelationship // JPA relationships (@OneToMany, etc.)
	SyntheticMethods []ParsedSyntheticMethod // Lombok-generated methods
	Lambdas          []ParsedLambda          // Lambda expressions
	// TypeScript-specific additions
	TypeAliases           []ParsedTypeAlias            // TypeScript type alias declarations
	HookCalls             []ParsedHookCall             // React/Vue hook calls and composition primitives
	VueComponentContracts []ParsedVueComponentContract // Vue macro-declared public component contracts
	PiniaStores           []ParsedPiniaStore           // Pinia defineStore declarations
	// GraphQL document additions
	GraphQLOperations           []ParsedGraphQLOperation
	GraphQLOperationUsages      []ParsedGraphQLOperationUsage
	GraphQLBackendEntrypoints   []ParsedGraphQLBackendEntrypoint
	GraphQLOperationResolvers   []ParsedGraphQLOperationResolver
	GraphQLOperationPermissions []ParsedGraphQLOperationPermission
	// Azure Functions additions
	AzureTriggers    []ParsedAzureTrigger
	AzureHostConfigs []ParsedAzureHostConfig
	// Gateway / API management additions
	GatewayRoutes []ParsedGatewayRoute
}
