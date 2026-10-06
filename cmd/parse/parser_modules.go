package main

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sergiumoraru/tirion/internal/config"
	"github.com/sergiumoraru/tirion/internal/graph"
	"github.com/sergiumoraru/tirion/internal/parser"
)

type fileParserModule interface {
	ID() string
	CanParse(path string) bool
	Parse(relPath, absPath string, content []byte) parser.ParsedFile
}

type filePrepareHook interface {
	PrepareFile(relPath string)
}

type fileAfterParseHook interface {
	AfterParse(relPath string, result *parser.ParsedFile)
}

type fileSkipHook interface {
	SkipFile(fileName, relPath string) (parseSkipCategory, bool)
}

type fileContentSkipHook interface {
	SkipContent(relPath, absPath string, content []byte) (parseSkipCategory, bool)
}

type filePersistHook interface {
	PersistExtras(ctx *modulePersistContext, result parser.ParsedFile) error
}

type repoParseHook interface {
	ID() string
	ResolveRepo(ctx *repoFinalizeContext) error
}

type repoFinalizeHook interface {
	ID() string
	FinalizeRepo(ctx *repoFinalizeContext) error
}

type modulePersistContext struct {
	repoID        int64
	snapshotID    *int64
	repoName      string
	relPath       string
	fileID        int64
	functionMetas map[string][]functionMeta
	batch         *pgx.Batch
	storage       *graph.Storage
}

type parserRegistry struct {
	modules       []fileParserModule
	repoHooks     []repoParseHook
	finalizeHooks []repoFinalizeHook
	java          *javaParserModule
	includes      *ibmiIncludeResolver
}

type repoFinalizeContext struct {
	repoID            int64
	snapshotID        *int64
	repoName          string
	workspaceSlug     string
	activeSnapshotIDs []int64
	storage           *graph.Storage
}

func nullableInt64Ptr(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func newParserRegistry(repoRoot string) *parserRegistry {
	javaModule := newJavaParserModule(repoRoot)
	ibmiResolver := buildIBMiIncludeResolver(repoRoot)
	ibmiState := &ibmiModuleState{}
	myBatisModule := newMyBatisXMLModule(javaModule)
	javaModule.myBatis = myBatisModule
	goParser := parser.NewGoParser()
	tfParser := parser.NewTerraformParser()
	binderParser := parser.NewIBMiBinderParser()
	ddsParser := parser.NewDDSParser()
	clParser := parser.NewCLParser()
	rpgParser := parser.NewRPGParser()
	csharpModule := newCSharpEntityModule(repoRoot)
	jsParser := parser.NewJavaScriptParser()
	jsEnrichmentParser := jsParser.ForEnrichment()
	graphqlParser := parser.NewGraphQLParser()
	sqlParser := parser.NewSQLParser()
	powerShellParser := parser.NewPowerShellParser()
	azureFunctionsParser := parser.NewAzureFunctionsParser()
	azureHostParser := parser.NewAzureHostParser()
	apimParser := parser.NewAPIMParser()
	resourceConfigParser := parser.NewResourceConfigParser()

	return &parserRegistry{
		includes: ibmiResolver,
		modules: []fileParserModule{
			newAzureHostModule(azureHostParser),
			newAzureFunctionsModule(azureFunctionsParser),
			newAPIMModule(apimParser),
			newGraphQLDocumentsModule(graphqlParser),
			newSimpleParserModule("sql", sqlParser.CanParse, func(relPath, absPath string, content []byte) parser.ParsedFile {
				return sqlParser.ParseFile(relPath, content)
			}),
			newSimpleParserModule("powershell", powerShellParser.CanParse, func(relPath, absPath string, content []byte) parser.ParsedFile {
				return powerShellParser.ParseFile(relPath, content)
			}),
			newResourceConfigModule(resourceConfigParser),
			newSimpleParserModule("go", func(path string) bool {
				return strings.HasSuffix(path, ".go")
			}, func(relPath, absPath string, content []byte) parser.ParsedFile {
				result := goParser.ParseFile(absPath, content)
				result.Language = "go"
				return result
			}),
			newSimpleParserModule("terraform", func(path string) bool {
				return strings.HasSuffix(path, ".tf")
			}, func(relPath, absPath string, content []byte) parser.ParsedFile {
				result := tfParser.ParseFile(absPath, content)
				result.Language = "terraform"
				return result
			}),
			javaModule,
			myBatisModule,
			newIBMiParserModule("ibmi_binder", binderParser.CanParse, func(relPath, absPath string, content []byte) parser.ParsedFile {
				return binderParser.ParseFile(absPath, content)
			}, ibmiResolver, ibmiState, false),
			newIBMiParserModule("dds", ddsParser.CanParse, func(relPath, absPath string, content []byte) parser.ParsedFile {
				return ddsParser.ParseFile(absPath, content)
			}, ibmiResolver, ibmiState, false),
			newIBMiParserModule("cl", clParser.CanParse, func(relPath, absPath string, content []byte) parser.ParsedFile {
				return clParser.ParseFile(absPath, content)
			}, ibmiResolver, ibmiState, false),
			newIBMiParserModule("rpg", rpgParser.CanParse, func(relPath, absPath string, content []byte) parser.ParsedFile {
				return rpgParser.ParseFile(absPath, content)
			}, ibmiResolver, ibmiState, true),
			csharpModule,
			newJavaScriptParserModule(jsParser, func(relPath, absPath string, content []byte) parser.ParsedFile {
				return jsEnrichmentParser.ParseFile(absPath, content)
			}),
		},
		repoHooks: []repoParseHook{
			ibmiRepoHook{state: ibmiState},
		},
		finalizeHooks: []repoFinalizeHook{
			csharpModule,
			myBatisModule,
			&graphQLResolutionModule{},
			&graphQLControllerResolutionModule{},
			&azureRouteConfigModule{},
			&javaAPIInterfaceModule{},
		},
		java: javaModule,
	}
}

func (r *parserRegistry) Match(path string) fileParserModule {
	for _, module := range r.modules {
		if module.CanParse(path) {
			return module
		}
	}
	return nil
}

func (r *parserRegistry) PrepareFile(module fileParserModule, relPath string) {
	if hook, ok := module.(filePrepareHook); ok {
		hook.PrepareFile(relPath)
	}
}

func (r *parserRegistry) AfterParse(module fileParserModule, relPath string, result *parser.ParsedFile) {
	if hook, ok := module.(fileAfterParseHook); ok {
		hook.AfterParse(relPath, result)
	}
}

func (r *parserRegistry) SkipFile(module fileParserModule, fileName, relPath string) (parseSkipCategory, bool) {
	if hook, ok := module.(fileSkipHook); ok {
		return hook.SkipFile(fileName, relPath)
	}
	return "", false
}

func (r *parserRegistry) SkipContent(module fileParserModule, relPath, absPath string, content []byte) (parseSkipCategory, bool) {
	if hook, ok := module.(fileContentSkipHook); ok {
		return hook.SkipContent(relPath, absPath, content)
	}
	return "", false
}

func (r *parserRegistry) PersistExtras(module fileParserModule, ctx *modulePersistContext, result parser.ParsedFile) error {
	if hook, ok := module.(filePersistHook); ok {
		return hook.PersistExtras(ctx, result)
	}
	return nil
}

func (r *parserRegistry) ResolveRepo(ctx *repoFinalizeContext) error {
	for _, hook := range r.repoHooks {
		if err := hook.ResolveRepo(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (r *parserRegistry) FinalizeRepo(ctx *repoFinalizeContext) error {
	for _, hook := range r.finalizeHooks {
		if err := hook.FinalizeRepo(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (r *parserRegistry) JavaPathIndex() map[string]string {
	if r == nil || r.java == nil {
		return nil
	}
	return r.java.pathIndex
}

type simpleParserModule struct {
	id       string
	canParse func(path string) bool
	parse    func(relPath, absPath string, content []byte) parser.ParsedFile
}

func newSimpleParserModule(id string, canParse func(path string) bool, parse func(relPath, absPath string, content []byte) parser.ParsedFile) *simpleParserModule {
	return &simpleParserModule{id: id, canParse: canParse, parse: parse}
}

func (m *simpleParserModule) ID() string {
	return m.id
}

func (m *simpleParserModule) CanParse(path string) bool {
	return m.canParse(path)
}

func (m *simpleParserModule) Parse(relPath, absPath string, content []byte) parser.ParsedFile {
	return m.parse(relPath, absPath, content)
}

type javaScriptParserModule struct {
	*simpleParserModule
}

func newJavaScriptParserModule(jsParser *parser.JavaScriptParser, parse func(relPath, absPath string, content []byte) parser.ParsedFile) *javaScriptParserModule {
	return &javaScriptParserModule{
		simpleParserModule: newSimpleParserModule("javascript", jsParser.CanParse, parse),
	}
}

func (m *javaScriptParserModule) SkipFile(fileName, relPath string) (parseSkipCategory, bool) {
	lower := strings.ToLower(fileName)
	if strings.HasSuffix(lower, ".min.js") || strings.HasSuffix(lower, ".bundle.js") || isVendoredLibraryFile(lower) {
		return parseSkipExcludedFilePattern, true
	}
	return "", false
}

// vendoredLibraryFileRe matches third-party libraries copied into a source tree
// outside any vendor directory (static/jquery-3.6.0.js, lib/lodash.js). The whole
// lowercase basename must be a known library name plus an optional version and
// build qualifiers; a substring (memberService.ts, remember.js, lodashWrapper.ts)
// never matches. Only well-known libraries are listed, so first-party files with
// a generic name (chart.js, utils.js) stay indexed.
var vendoredLibraryFileRe = regexp.MustCompile(
	`^(?:jquery(?:[.-](?:ui|migrate|validate|cookie|mobile))?|lodash(?:[.-](?:core|custom|fp))?|underscore|` +
		`angular(?:-(?:route|resource|animate|sanitize|cookies|touch|aria|messages|loader|mocks))?|` +
		`moment(?:-with-locales|-timezone(?:-with-data)?)?|backbone|d3|bootstrap(?:[.-]bundle)?|popper|` +
		`handlebars|knockout|ember|mootools|prototype|zepto|modernizr|requirejs|highcharts|three|rxjs|socket\.io)` +
		`(?:[.-]v?\d[\w.-]*)?(?:\.(?:min|slim|bundle|umd|esm|full|debug|production|development))*\.m?js$`)

// reactDistFileRe matches React's distribution builds; a bare react.js is
// ambiguous, so only the dev/production build names count.
var reactDistFileRe = regexp.MustCompile(`^react(?:-dom)?(?:-server)?(?:\.(?:production|development))(?:\.min)?\.js$`)

func isVendoredLibraryFile(lowerFileName string) bool {
	return vendoredLibraryFileRe.MatchString(lowerFileName) || reactDistFileRe.MatchString(lowerFileName)
}

// Large JavaScript files that carry a build or third-party signature are copied
// libraries or bundler output (swagger-ui, oidc-client, dist/esm/index.js,
// angular-ui-router-v1.0.0.js). Their facts are not first-party relationships,
// and they dominate parse, enrichment, and resolution time.
const generatedJavaScriptMinBytes = 100 << 10

var (
	jsSourceMapCommentRe = regexp.MustCompile(`(?m)^\s*//[#@] sourceMappingURL=`)
	jsBundlerRuntimeRe   = regexp.MustCompile(`__webpack_require__|webpackJsonp|webpackChunk|System\.register\(|\bdefine\.amd\b`)
	jsLicenseBannerRe    = regexp.MustCompile(`(?i)@license|\(c\)|copyright`)
	jsVersionRe          = regexp.MustCompile(`@version|\bv?\d+\.\d+\.\d+\b`)
)

func looksGeneratedOrVendoredJavaScript(relPath string, content []byte) bool {
	switch strings.ToLower(filepath.Ext(relPath)) {
	case ".js", ".mjs", ".cjs":
	default:
		return false
	}
	if len(content) < generatedJavaScriptMinBytes {
		return false
	}
	head := content
	if len(head) > 3000 {
		head = head[:3000]
	}
	return jsSourceMapCommentRe.Match(content) || jsBundlerRuntimeRe.Match(content) ||
		(jsLicenseBannerRe.Match(head) && jsVersionRe.Match(head))
}

func (m *javaScriptParserModule) SkipContent(relPath, absPath string, content []byte) (parseSkipCategory, bool) {
	if looksGeneratedOrVendoredJavaScript(relPath, content) {
		return parseSkipGeneratedJavaScript, true
	}
	return "", false
}

func (m *javaScriptParserModule) PersistExtras(ctx *modulePersistContext, result parser.ParsedFile) error {
	queueInsertGraphQLOperationBatch(ctx.batch, ctx.repoID, ctx.fileID, result.GraphQLOperations)
	queueInsertGraphQLOperationUsageBatch(ctx.batch, ctx.repoID, ctx.fileID, result.GraphQLOperationUsages)
	queueInsertGraphQLBackendEntrypointBatch(ctx.batch, ctx.repoID, ctx.fileID, result.GraphQLBackendEntrypoints)
	queueInsertGraphQLOperationResolverBatch(ctx.batch, ctx.repoID, ctx.fileID, result.GraphQLOperationResolvers)
	queueInsertGraphQLOperationPermissionBatch(ctx.batch, ctx.repoID, ctx.fileID, result.GraphQLOperationPermissions)
	return nil
}

type graphQLDocumentsModule struct {
	*simpleParserModule
}

func newGraphQLDocumentsModule(graphqlParser *parser.GraphQLParser) *graphQLDocumentsModule {
	return &graphQLDocumentsModule{
		simpleParserModule: newSimpleParserModule("graphql_documents", graphqlParser.CanParse, func(relPath, absPath string, content []byte) parser.ParsedFile {
			return graphqlParser.ParseFile(relPath, content)
		}),
	}
}

func (m *graphQLDocumentsModule) PersistExtras(ctx *modulePersistContext, result parser.ParsedFile) error {
	queueInsertGraphQLOperationBatch(ctx.batch, ctx.repoID, ctx.fileID, result.GraphQLOperations)
	return nil
}

type resourceConfigModule struct {
	*simpleParserModule
}

func newResourceConfigModule(configParser *parser.ResourceConfigParser) *resourceConfigModule {
	return &resourceConfigModule{
		simpleParserModule: newSimpleParserModule("resource_config", configParser.CanParse, func(relPath, absPath string, content []byte) parser.ParsedFile {
			return configParser.ParseFile(relPath, content)
		}),
	}
}

func (m *resourceConfigModule) PersistExtras(ctx *modulePersistContext, result parser.ParsedFile) error {
	queueInsertResourceAliasBatch(ctx.batch, ctx.repoID, ctx.snapshotID, ctx.fileID, result.ResourceAliases)
	return nil
}

type azureFunctionsModule struct {
	*simpleParserModule
}

func newAzureFunctionsModule(azureParser *parser.AzureFunctionsParser) *azureFunctionsModule {
	return &azureFunctionsModule{
		simpleParserModule: newSimpleParserModule("azure_functions", azureParser.CanParse, func(relPath, absPath string, content []byte) parser.ParsedFile {
			return azureParser.ParseFile(relPath, content)
		}),
	}
}

func (m *azureFunctionsModule) PersistExtras(ctx *modulePersistContext, result parser.ParsedFile) error {
	queueInsertAzureTriggerBatch(ctx.batch, ctx.repoID, ctx.fileID, result.AzureTriggers)
	return nil
}

type azureHostModule struct {
	*simpleParserModule
}

func newAzureHostModule(hostParser *parser.AzureHostParser) *azureHostModule {
	return &azureHostModule{
		simpleParserModule: newSimpleParserModule("azure_host", hostParser.CanParse, func(relPath, absPath string, content []byte) parser.ParsedFile {
			return hostParser.ParseFile(relPath, content)
		}),
	}
}

func (m *azureHostModule) PersistExtras(ctx *modulePersistContext, result parser.ParsedFile) error {
	queueInsertAzureHostConfigBatch(ctx.batch, ctx.repoID, ctx.fileID, result.AzureHostConfigs)
	return nil
}

type apimModule struct {
	*simpleParserModule
}

func newAPIMModule(apimParser *parser.APIMParser) *apimModule {
	return &apimModule{
		simpleParserModule: newSimpleParserModule("apim", apimParser.CanParse, func(relPath, absPath string, content []byte) parser.ParsedFile {
			return apimParser.ParseFile(relPath, content)
		}),
	}
}

func (m *apimModule) SkipContent(relPath, absPath string, content []byte) (parseSkipCategory, bool) {
	if !strings.HasSuffix(strings.ToLower(relPath), ".json") {
		return parseSkipUnsupportedExtension, true
	}
	body := string(content)
	if !strings.Contains(body, "Microsoft.ApiManagement/service/apis") &&
		!strings.Contains(body, "Microsoft.Logic/workflows") &&
		!strings.Contains(body, "Microsoft.Logic/workflows/triggers") {
		return parseSkipNoExtractedContent, true
	}
	return "", false
}

func (m *apimModule) PersistExtras(ctx *modulePersistContext, result parser.ParsedFile) error {
	queueInsertGatewayRouteBatch(ctx.batch, ctx.repoID, ctx.fileID, result.GatewayRoutes)
	queueInsertAzureTriggerBatch(ctx.batch, ctx.repoID, ctx.fileID, result.AzureTriggers)
	return nil
}

type javaParserModule struct {
	parser            *parser.JavaParser
	enrichmentParser  *parser.EnrichmentParser
	repoRoot          string
	propertyMap       map[string]string
	propertyMapLoaded bool
	propertyMapError  error
	pathIndex         map[string]string
	pathCounts        map[string]int
	myBatis           *myBatisXMLModule
	queueFramework    config.JavaSQSFramework
}

func newJavaParserModule(repoRoot string) *javaParserModule {
	javaParser := parser.NewJavaParser()
	return &javaParserModule{
		parser:           javaParser,
		enrichmentParser: javaParser.ForEnrichment(),
		repoRoot:         repoRoot,
		pathIndex:        make(map[string]string),
		pathCounts:       make(map[string]int),
		queueFramework:   config.GetEffectivePatterns().JavaSQS,
	}
}

func (m *javaParserModule) ID() string {
	return "java"
}

func (m *javaParserModule) CanParse(path string) bool {
	return m.parser.CanParse(path)
}

func (m *javaParserModule) PrepareFile(relPath string) {
	registerJavaPathIndex(m.pathIndex, m.pathCounts, relPath)
}

func (m *javaParserModule) Parse(relPath, absPath string, content []byte) parser.ParsedFile {
	return m.enrichmentParser.ParseFile(absPath, content)
}

func (m *javaParserModule) AfterParse(relPath string, result *parser.ParsedFile) {
	if result == nil {
		return
	}
	if m.myBatis != nil {
		m.myBatis.RecordSQLStatements(relPath, result.SqlStatements)
	}
	if parser.HasPropertyQueueConsumer(*result, m.queueFramework) {
		if !m.propertyMapLoaded {
			m.propertyMap, m.propertyMapError = parser.LoadJavaQueueProperties(m.repoRoot)
			m.propertyMapLoaded = true
		}
		if m.propertyMapError != nil {
			result.ParseDiagnostics = parser.ParseDiagnostics{FailureKind: parser.ParseFailureInternal, Message: m.propertyMapError.Error()}
			return
		}
		if derived := parser.DerivePropertyQueueConsumers(*result, m.propertyMap, m.queueFramework); len(derived) > 0 {
			result.SqsConsumers = append(result.SqsConsumers, derived...)
		}
	}
}

type ibmiParserModule struct {
	*simpleParserModule
	includeResolver     *ibmiIncludeResolver
	state               *ibmiModuleState
	applyCopyAliasHints bool
}

func newIBMiParserModule(id string, canParse func(path string) bool, parse func(relPath, absPath string, content []byte) parser.ParsedFile, resolver *ibmiIncludeResolver, state *ibmiModuleState, applyCopyAliasHints bool) *ibmiParserModule {
	return &ibmiParserModule{
		simpleParserModule:  newSimpleParserModule(id, canParse, parse),
		includeResolver:     resolver,
		state:               state,
		applyCopyAliasHints: applyCopyAliasHints,
	}
}

func (m *ibmiParserModule) PrepareFile(relPath string) {
	if m.state != nil {
		m.state.markUsed()
	}
}

func (m *ibmiParserModule) AfterParse(relPath string, result *parser.ParsedFile) {
	if !m.applyCopyAliasHints || result == nil {
		return
	}
	applyRPGCopyAliasExpansion(relPath, result, m.includeResolver)
}

func (m *ibmiParserModule) PersistExtras(ctx *modulePersistContext, result parser.ParsedFile) error {
	if ctx == nil || ctx.batch == nil {
		return nil
	}
	if len(result.IBMiExports) == 0 && len(result.IBMiBindings) == 0 {
		return nil
	}
	if m.state != nil {
		m.state.markUsed()
		if err := m.state.ensureSchema(ctx.storage); err != nil {
			return err
		}
	}
	for _, export := range result.IBMiExports {
		var functionID *int64
		if export.FunctionName != "" {
			if metas, ok := ctx.functionMetas[export.FunctionName]; ok {
				if id, ok := selectFunctionIDByLine(metas, export.LineNumber); ok && id != 0 {
					functionID = &id
				}
			}
		}
		queueInsertIBMiExport(ctx.batch, ctx.repoID, ctx.snapshotID, ctx.fileID, functionID, export.ObjectName, export.ObjectType, export.ExportName, export.SourceType, export.LineNumber)
	}
	for _, binding := range result.IBMiBindings {
		queueInsertIBMiBinding(ctx.batch, ctx.repoID, ctx.snapshotID, ctx.fileID, binding.OwnerObject, binding.BindingName, binding.BindingType, binding.TargetObject, binding.TargetObjectType, binding.LineNumber)
	}
	return nil
}

type ibmiRepoHook struct {
	state *ibmiModuleState
}

func (ibmiRepoHook) ID() string {
	return "ibmi"
}

func (h ibmiRepoHook) ResolveRepo(ctx *repoFinalizeContext) error {
	if h.state == nil {
		return nil
	}
	return h.state.resolveRepo(ctx)
}
