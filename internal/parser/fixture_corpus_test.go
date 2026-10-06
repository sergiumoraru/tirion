package parser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"
)

type fixtureImportExpectation struct {
	Path        string   `json:"path"`
	Names       []string `json:"names"`
	IsDefault   bool     `json:"is_default"`
	IsNamespace bool     `json:"is_namespace"`
}

type fixtureEndpointExpectation struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Handler string `json:"handler"`
}

type fixtureTypeAliasExpectation struct {
	Name       string   `json:"name"`
	TypeParams []string `json:"type_params"`
}

type fixtureTypeParameterExpectation struct {
	Name      string   `json:"name"`
	BoundType string   `json:"bound_type"`
	Bounds    []string `json:"bounds"`
}

type tsFixtureExpectation struct {
	Language    string                        `json:"language"`
	Functions   []string                      `json:"functions"`
	Imports     []fixtureImportExpectation    `json:"imports"`
	Endpoints   []fixtureEndpointExpectation  `json:"endpoints"`
	TypeAliases []fixtureTypeAliasExpectation `json:"type_aliases"`
	Interfaces  []string                      `json:"interfaces"`
}

type fixtureSqsProducerExpectation struct {
	Function  string `json:"function"`
	QueueName string `json:"queue_name"`
}

type fixtureSqsConsumerExpectation struct {
	ClassName     string `json:"class_name"`
	QueueName     string `json:"queue_name"`
	HandlerMethod string `json:"handler_method"`
}

type fixtureHttpCallExpectation struct {
	Function   string `json:"function"`
	Method     string `json:"method"`
	URLPattern string `json:"url_pattern"`
	ClientType string `json:"client_type"`
}

type tsSemanticFixtureExpectation struct {
	Language     string                          `json:"language"`
	Functions    []string                        `json:"functions"`
	SqsProducers []fixtureSqsProducerExpectation `json:"sqs_producers"`
	HttpCalls    []fixtureHttpCallExpectation    `json:"http_calls"`
}

type javaSqsFixtureExpectation struct {
	Language     string                          `json:"language"`
	SqsProducers []fixtureSqsProducerExpectation `json:"sqs_producers"`
	SqsConsumers []fixtureSqsConsumerExpectation `json:"sqs_consumers"`
}

type javaClassFixtureExpectation struct {
	Name       string                            `json:"name"`
	IsAbstract bool                              `json:"is_abstract"`
	Extends    string                            `json:"extends"`
	Implements []string                          `json:"implements"`
	Modifiers  []string                          `json:"modifiers"`
	TypeParams []fixtureTypeParameterExpectation `json:"type_params"`
}

type javaFixtureExpectation struct {
	Language         string                                       `json:"language"`
	Class            javaClassFixtureExpectation                  `json:"class"`
	Endpoints        []fixtureEndpointExpectation                 `json:"endpoints"`
	Signatures       map[string][]string                          `json:"signatures"`
	MethodTypeParams map[string][]fixtureTypeParameterExpectation `json:"method_type_params"`
	MinLambdas       int                                          `json:"min_lambdas"`
}

type rpgDataAccessExpectation struct {
	Function string `json:"function"`
	Entity   string `json:"entity"`
	Access   string `json:"access"`
}

type rpgFixtureExpectation struct {
	Language     string                     `json:"language"`
	Functions    []string                   `json:"functions"`
	Imports      []fixtureImportExpectation `json:"imports"`
	Calls        map[string][]string        `json:"calls"`
	DataAccesses []rpgDataAccessExpectation `json:"data_accesses"`
}

type ddsFieldExpectation struct {
	Class     string `json:"class"`
	Name      string `json:"name"`
	FieldType string `json:"field_type"`
}

type ddsFixtureExpectation struct {
	Language        string                `json:"language"`
	RequiredClasses []string              `json:"required_classes"`
	RequiredFields  []ddsFieldExpectation `json:"required_fields"`
}

func fixturePath(t *testing.T, fileName string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve test path")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", "parser-fixtures", fileName)
}

func loadJSONFixture[T any](t *testing.T, fileName string) T {
	t.Helper()
	raw, err := os.ReadFile(fixturePath(t, fileName))
	if err != nil {
		t.Fatalf("failed to read fixture %s: %v", fileName, err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("failed to decode fixture %s: %v", fileName, err)
	}
	return out
}

func normalizeNames(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func normalizeEndpoints(eps []fixtureEndpointExpectation) []string {
	normalized := make([]string, 0, len(eps))
	for _, ep := range eps {
		normalized = append(normalized, ep.Method+" "+ep.Path+" "+ep.Handler)
	}
	sort.Strings(normalized)
	return normalized
}

func normalizeParsedEndpoints(eps []ParsedEndpoint) []string {
	normalized := make([]string, 0, len(eps))
	for _, ep := range eps {
		normalized = append(normalized, ep.Method+" "+ep.Path+" "+ep.HandlerName)
	}
	sort.Strings(normalized)
	return normalized
}

func normalizeTypeParamBounds(params []fixtureTypeParameterExpectation) []fixtureTypeParameterExpectation {
	out := append([]fixtureTypeParameterExpectation(nil), params...)
	for i := range out {
		out[i].Bounds = normalizeNames(out[i].Bounds)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func normalizeParsedTypeParams(params []ParsedTypeParameter) []fixtureTypeParameterExpectation {
	out := make([]fixtureTypeParameterExpectation, 0, len(params))
	for _, tp := range params {
		out = append(out, fixtureTypeParameterExpectation{
			Name:      tp.Name,
			BoundType: tp.BoundType,
			Bounds:    normalizeNames(tp.Bounds),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func normalizeRPGDataAccesses(accesses []rpgDataAccessExpectation) []string {
	out := make([]string, 0, len(accesses))
	for _, access := range accesses {
		out = append(out, access.Function+"|"+access.Entity+"|"+access.Access)
	}
	sort.Strings(out)
	return out
}

func normalizeParsedRPGDataAccesses(data map[string][]ParsedDataAccess) []string {
	out := make([]string, 0)
	for fnName, accesses := range data {
		for _, access := range accesses {
			out = append(out, fnName+"|"+access.EntityName+"|"+access.Access)
		}
	}
	sort.Strings(out)
	return out
}

func normalizeFixtureSqsProducers(producers []fixtureSqsProducerExpectation) []string {
	out := make([]string, 0, len(producers))
	for _, producer := range producers {
		out = append(out, producer.Function+"|"+producer.QueueName)
	}
	sort.Strings(out)
	return out
}

func normalizeParsedSqsProducers(data map[string][]ParsedSqsProducer) []string {
	out := make([]string, 0)
	for fnName, producers := range data {
		for _, producer := range producers {
			out = append(out, fnName+"|"+producer.QueueName)
		}
	}
	sort.Strings(out)
	return out
}

func normalizeFixtureSqsConsumers(consumers []fixtureSqsConsumerExpectation) []string {
	out := make([]string, 0, len(consumers))
	for _, consumer := range consumers {
		out = append(out, consumer.ClassName+"|"+consumer.QueueName+"|"+consumer.HandlerMethod)
	}
	sort.Strings(out)
	return out
}

func normalizeParsedSqsConsumers(consumers []ParsedSqsConsumer) []string {
	out := make([]string, 0, len(consumers))
	for _, consumer := range consumers {
		out = append(out, consumer.ClassName+"|"+consumer.QueueName+"|"+consumer.HandlerMethod)
	}
	sort.Strings(out)
	return out
}

func normalizeFixtureHttpCalls(calls []fixtureHttpCallExpectation) []string {
	out := make([]string, 0, len(calls))
	for _, call := range calls {
		out = append(out, call.Function+"|"+call.Method+"|"+call.URLPattern+"|"+call.ClientType)
	}
	sort.Strings(out)
	return out
}

func normalizeParsedHttpCalls(calls map[string][]ParsedHttpCall) []string {
	out := make([]string, 0)
	for fnName, fnCalls := range calls {
		for _, call := range fnCalls {
			out = append(out, fnName+"|"+call.HttpMethod+"|"+call.UrlPattern+"|"+call.ClientType)
		}
	}
	sort.Strings(out)
	return out
}

func uniqueNormalized(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func hasCallee(calls []ParsedFunctionCall, callee string) bool {
	for _, call := range calls {
		if call.CalleeName == callee {
			return true
		}
	}
	return false
}

func hasClassNamed(classes []ParsedClass, name string) bool {
	for _, cls := range classes {
		if cls.Name == name {
			return true
		}
	}
	return false
}

func findClassByName(classes []ParsedClass, name string) *ParsedClass {
	for i := range classes {
		if classes[i].Name == name {
			return &classes[i]
		}
	}
	return nil
}

// TestParserFixtureCorpus_TypeScript covers:
// C-009 fixture corpus + C-010 endpoints + C-013 import alias/namespace behavior.
func TestParserFixtureCorpus_TypeScript(t *testing.T) {
	expected := loadJSONFixture[tsFixtureExpectation](t, "core/resource_routes.expected.json")
	content, err := os.ReadFile(fixturePath(t, "core/resource_routes.ts"))
	if err != nil {
		t.Fatalf("failed to read fixture source: %v", err)
	}

	p := NewJavaScriptParser()
	result := p.ParseFile("resource_routes.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if result.Language != expected.Language {
		t.Fatalf("expected language %q, got %q", expected.Language, result.Language)
	}

	functionNames := make([]string, 0, len(result.Functions))
	for _, fn := range result.Functions {
		functionNames = append(functionNames, fn.Name)
	}
	if !reflect.DeepEqual(normalizeNames(functionNames), normalizeNames(expected.Functions)) {
		t.Fatalf("unexpected functions: got=%v expected=%v", normalizeNames(functionNames), normalizeNames(expected.Functions))
	}

	gotImports := make(map[string]fixtureImportExpectation, len(result.Imports))
	for _, imp := range result.Imports {
		gotImports[imp.Path] = fixtureImportExpectation{
			Path:        imp.Path,
			Names:       imp.Names,
			IsDefault:   imp.IsDefault,
			IsNamespace: imp.IsNamespace,
		}
	}
	for _, want := range expected.Imports {
		got, ok := gotImports[want.Path]
		if !ok {
			t.Fatalf("missing import for path %q", want.Path)
		}
		if got.IsDefault != want.IsDefault || got.IsNamespace != want.IsNamespace {
			t.Fatalf("unexpected import flags for %q: got=%+v want=%+v", want.Path, got, want)
		}
		if !reflect.DeepEqual(normalizeNames(got.Names), normalizeNames(want.Names)) {
			t.Fatalf("unexpected import names for %q: got=%v want=%v", want.Path, normalizeNames(got.Names), normalizeNames(want.Names))
		}
	}

	if !reflect.DeepEqual(normalizeParsedEndpoints(result.Endpoints), normalizeEndpoints(expected.Endpoints)) {
		t.Fatalf("unexpected endpoints: got=%v expected=%v", normalizeParsedEndpoints(result.Endpoints), normalizeEndpoints(expected.Endpoints))
	}

	gotTypeAliases := make(map[string][]string, len(result.TypeAliases))
	for _, ta := range result.TypeAliases {
		gotTypeAliases[ta.Name] = append([]string(nil), ta.TypeParams...)
	}
	for _, want := range expected.TypeAliases {
		got, ok := gotTypeAliases[want.Name]
		if !ok {
			t.Fatalf("missing type alias %q", want.Name)
		}
		if !reflect.DeepEqual(normalizeNames(got), normalizeNames(want.TypeParams)) {
			t.Fatalf("unexpected type alias params for %q: got=%v want=%v", want.Name, normalizeNames(got), normalizeNames(want.TypeParams))
		}
	}

	gotInterfaces := make([]string, 0, len(result.Interfaces))
	for _, iface := range result.Interfaces {
		gotInterfaces = append(gotInterfaces, iface.Name)
	}
	if !reflect.DeepEqual(normalizeNames(gotInterfaces), normalizeNames(expected.Interfaces)) {
		t.Fatalf("unexpected interfaces: got=%v expected=%v", normalizeNames(gotInterfaces), normalizeNames(expected.Interfaces))
	}

	// Nested/anonymous callback extraction checks.
	normalizedCalls, ok := result.FunctionCalls["_anonymous_L13_C41"]
	if !ok || !hasCallee(normalizedCalls, "ResourceUtils.normalize") {
		t.Fatalf("expected map callback -> ResourceUtils.normalize, got=%v", normalizedCalls)
	}
	inlineRouteCalls, ok := result.FunctionCalls["_anonymous_L18_C31"]
	if !ok || !hasCallee(inlineRouteCalls, "res.json") {
		t.Fatalf("expected anonymous inline callback call in routeWithInlineHandler, got=%v", inlineRouteCalls)
	}
	if !hasCallee(result.FunctionCalls["saveResource"], "_anonymous_L13_C41") ||
		!hasCallee(result.FunctionCalls["routeWithInlineHandler"], "_anonymous_L18_C31") {
		t.Fatalf("callback owners disconnected: %#v", result.FunctionCalls)
	}
}

func TestParserFixtureCorpus_TypeScriptSemanticEdges(t *testing.T) {
	expected := loadJSONFixture[tsSemanticFixtureExpectation](t, "core/async_export.expected.json")
	content, err := os.ReadFile(fixturePath(t, "core/async_export.ts"))
	if err != nil {
		t.Fatalf("failed to read fixture source: %v", err)
	}

	p := configuredHTTPFixtureParser()
	result := p.ParseFile("async_export.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if result.Language != expected.Language {
		t.Fatalf("expected language %q, got %q", expected.Language, result.Language)
	}

	functionNames := make([]string, 0, len(result.Functions))
	for _, fn := range result.Functions {
		functionNames = append(functionNames, fn.Name)
	}
	if !reflect.DeepEqual(normalizeNames(functionNames), normalizeNames(expected.Functions)) {
		t.Fatalf("unexpected functions: got=%v expected=%v", normalizeNames(functionNames), normalizeNames(expected.Functions))
	}

	gotProducers := normalizeParsedSqsProducers(result.SqsProducers)
	wantProducers := normalizeFixtureSqsProducers(expected.SqsProducers)
	if !reflect.DeepEqual(gotProducers, wantProducers) {
		t.Fatalf("unexpected sqs producers: got=%v expected=%v", gotProducers, wantProducers)
	}

	gotHttpCalls := normalizeParsedHttpCalls(result.HttpCalls)
	wantHttpCalls := normalizeFixtureHttpCalls(expected.HttpCalls)
	if !reflect.DeepEqual(gotHttpCalls, wantHttpCalls) {
		t.Fatalf("unexpected http calls: got=%v expected=%v", gotHttpCalls, wantHttpCalls)
	}
}

// TestParserFixtureCorpus_Java covers:
// C-009 fixture corpus + C-010 annotation endpoints + C-011 lambdas + C-012 modifiers/inheritance + C-014 signatures/generics.
func TestParserFixtureCorpus_Java(t *testing.T) {
	expected := loadJSONFixture[javaFixtureExpectation](t, "core/resource_controller.expected.json")
	content, err := os.ReadFile(fixturePath(t, "core/resource_controller.java"))
	if err != nil {
		t.Fatalf("failed to read fixture source: %v", err)
	}

	p := NewJavaParser()
	result := p.ParseFile("resource_controller.java", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if result.Language != expected.Language {
		t.Fatalf("expected language %q, got %q", expected.Language, result.Language)
	}

	var targetClass *ParsedClass
	for i := range result.Classes {
		if result.Classes[i].Name == expected.Class.Name {
			targetClass = &result.Classes[i]
			break
		}
	}
	if targetClass == nil {
		t.Fatalf("missing class %q", expected.Class.Name)
	}
	if targetClass.IsAbstract != expected.Class.IsAbstract {
		t.Fatalf("unexpected class isAbstract: got=%v want=%v", targetClass.IsAbstract, expected.Class.IsAbstract)
	}
	if targetClass.ExtendsClass != expected.Class.Extends {
		t.Fatalf("unexpected class extends: got=%q want=%q", targetClass.ExtendsClass, expected.Class.Extends)
	}
	if !reflect.DeepEqual(normalizeNames(targetClass.Implements), normalizeNames(expected.Class.Implements)) {
		t.Fatalf("unexpected class implements: got=%v want=%v", normalizeNames(targetClass.Implements), normalizeNames(expected.Class.Implements))
	}
	if !reflect.DeepEqual(normalizeNames(targetClass.Modifiers), normalizeNames(expected.Class.Modifiers)) {
		t.Fatalf("unexpected class modifiers: got=%v want=%v", normalizeNames(targetClass.Modifiers), normalizeNames(expected.Class.Modifiers))
	}
	if !reflect.DeepEqual(normalizeParsedTypeParams(targetClass.TypeParameters), normalizeTypeParamBounds(expected.Class.TypeParams)) {
		t.Fatalf("unexpected class type params: got=%v want=%v", normalizeParsedTypeParams(targetClass.TypeParameters), normalizeTypeParamBounds(expected.Class.TypeParams))
	}

	if !reflect.DeepEqual(normalizeParsedEndpoints(result.Endpoints), normalizeEndpoints(expected.Endpoints)) {
		t.Fatalf("unexpected endpoints: got=%v expected=%v", normalizeParsedEndpoints(result.Endpoints), normalizeEndpoints(expected.Endpoints))
	}

	gotSignatures := make(map[string][]string)
	gotMethodTypeParams := make(map[string][]fixtureTypeParameterExpectation)
	for _, fn := range result.Functions {
		if fn.Signature != nil {
			gotSignatures[fn.Name] = append(gotSignatures[fn.Name], fn.Signature.Signature)
		}
		if len(fn.TypeParameters) > 0 {
			gotMethodTypeParams[fn.Name] = normalizeParsedTypeParams(fn.TypeParameters)
		}
	}
	for fnName := range gotSignatures {
		sort.Strings(gotSignatures[fnName])
	}
	for fnName := range expected.Signatures {
		sort.Strings(expected.Signatures[fnName])
	}
	for fnName, want := range expected.Signatures {
		got, ok := gotSignatures[fnName]
		if !ok {
			t.Fatalf("missing signatures for %s", fnName)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("unexpected signatures for %s: got=%v want=%v", fnName, got, want)
		}
	}

	normalizedExpectedTypeParams := make(map[string][]fixtureTypeParameterExpectation, len(expected.MethodTypeParams))
	for fnName, params := range expected.MethodTypeParams {
		normalizedExpectedTypeParams[fnName] = normalizeTypeParamBounds(params)
	}
	for fnName, want := range normalizedExpectedTypeParams {
		got, ok := gotMethodTypeParams[fnName]
		if !ok {
			t.Fatalf("missing method type params for %s", fnName)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("unexpected method type params for %s: got=%v want=%v", fnName, got, want)
		}
	}

	if len(result.Lambdas) < expected.MinLambdas {
		t.Fatalf("expected at least %d lambdas, got %d", expected.MinLambdas, len(result.Lambdas))
	}
}

func TestParserFixtureCorpus_JavaSqs(t *testing.T) {
	expected := loadJSONFixture[javaSqsFixtureExpectation](t, "core/sqs_queues.expected.json")
	content, err := os.ReadFile(fixturePath(t, "core/sqs_queues.java"))
	if err != nil {
		t.Fatalf("failed to read fixture source: %v", err)
	}

	p := javaQueueFixtureParser()
	result := p.ParseFile("sqs_queues.java", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if result.Language != expected.Language {
		t.Fatalf("expected language %q, got %q", expected.Language, result.Language)
	}

	gotProducers := normalizeParsedSqsProducers(result.SqsProducers)
	wantProducers := normalizeFixtureSqsProducers(expected.SqsProducers)
	if !reflect.DeepEqual(gotProducers, wantProducers) {
		t.Fatalf("unexpected sqs producers: got=%v expected=%v", gotProducers, wantProducers)
	}

	gotConsumers := normalizeParsedSqsConsumers(result.SqsConsumers)
	wantConsumers := normalizeFixtureSqsConsumers(expected.SqsConsumers)
	if !reflect.DeepEqual(gotConsumers, wantConsumers) {
		t.Fatalf("unexpected sqs consumers: got=%v expected=%v", gotConsumers, wantConsumers)
	}
}

// TestParserFixtureCorpus_RPG covers:
// C-009 fixture corpus for RPG free-form/fixed-form parsing, imports, external aliases, and opcode accesses.
func TestParserFixtureCorpus_RPG(t *testing.T) {
	fixtures := []struct {
		name         string
		sourceFile   string
		expectedFile string
		parsePath    string
	}{
		{
			name:         "free_form_extpgm",
			sourceFile:   "synthetic-ibmi/rpg_lock_demo.rpgle",
			expectedFile: "synthetic-ibmi/rpg_lock_demo.expected.json",
		},
		{
			name:         "fixed_form_extpgm",
			sourceFile:   "synthetic-ibmi/rpg_config_loader.rpgle",
			expectedFile: "synthetic-ibmi/rpg_config_loader.expected.json",
		},
		{
			name:         "invoice_sql_cursor",
			sourceFile:   "synthetic-ibmi/invoice_scan.sqlrpgle",
			expectedFile: "synthetic-ibmi/invoice_scan.expected.json",
			parsePath:    "INVOICE_SCAN.SQLRPGLE",
		},
		{
			name:         "public_configr4",
			sourceFile:   "public-ibmi/public_configr4.rpgle",
			expectedFile: "public-ibmi/public_configr4.expected.json",
			parsePath:    "CONFIGR4.RPGLE",
		},
		{
			name:         "public_qshonisrv",
			sourceFile:   "public-ibmi/public_qshonisrv.rpgle",
			expectedFile: "public-ibmi/public_qshonisrv.expected.json",
			parsePath:    "QSHONISRV.RPGLE",
		},
	}

	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			expected := loadJSONFixture[rpgFixtureExpectation](t, fixture.expectedFile)
			content, err := os.ReadFile(fixturePath(t, fixture.sourceFile))
			if err != nil {
				t.Fatalf("failed to read fixture source: %v", err)
			}

			parsePath := fixture.sourceFile
			if fixture.parsePath != "" {
				parsePath = fixture.parsePath
			}

			p := NewRPGParser()
			result := p.ParseFile(parsePath, content)
			if result.ParseDiagnostics.Failed() {
				t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
			}

			if result.Language != expected.Language {
				t.Fatalf("expected language %q, got %q", expected.Language, result.Language)
			}

			functionNames := make([]string, 0, len(result.Functions))
			for _, fn := range result.Functions {
				functionNames = append(functionNames, fn.Name)
			}
			if !reflect.DeepEqual(normalizeNames(functionNames), normalizeNames(expected.Functions)) {
				t.Fatalf("unexpected functions: got=%v expected=%v", normalizeNames(functionNames), normalizeNames(expected.Functions))
			}

			gotImports := make(map[string]fixtureImportExpectation, len(result.Imports))
			for _, imp := range result.Imports {
				gotImports[imp.Path] = fixtureImportExpectation{
					Path:        imp.Path,
					Names:       imp.Names,
					IsDefault:   imp.IsDefault,
					IsNamespace: imp.IsNamespace,
				}
			}
			if len(gotImports) != len(expected.Imports) {
				t.Fatalf("unexpected import count: got=%d expected=%d", len(gotImports), len(expected.Imports))
			}
			for _, want := range expected.Imports {
				got, ok := gotImports[want.Path]
				if !ok {
					t.Fatalf("missing import for path %q", want.Path)
				}
				if got.IsDefault != want.IsDefault || got.IsNamespace != want.IsNamespace {
					t.Fatalf("unexpected import flags for %q: got=%+v want=%+v", want.Path, got, want)
				}
				if !reflect.DeepEqual(normalizeNames(got.Names), normalizeNames(want.Names)) {
					t.Fatalf("unexpected import names for %q: got=%v want=%v", want.Path, normalizeNames(got.Names), normalizeNames(want.Names))
				}
			}

			for fnName, want := range expected.Calls {
				got := make([]string, 0, len(result.FunctionCalls[fnName]))
				for _, call := range result.FunctionCalls[fnName] {
					got = append(got, call.CalleeName)
				}
				if !reflect.DeepEqual(uniqueNormalized(got), uniqueNormalized(want)) {
					t.Fatalf("unexpected calls for %s: got=%v want=%v", fnName, uniqueNormalized(got), uniqueNormalized(want))
				}
			}

			if !reflect.DeepEqual(normalizeParsedRPGDataAccesses(result.DataAccesses), normalizeRPGDataAccesses(expected.DataAccesses)) {
				t.Fatalf("unexpected data accesses: got=%v want=%v", normalizeParsedRPGDataAccesses(result.DataAccesses), normalizeRPGDataAccesses(expected.DataAccesses))
			}
		})
	}
}

func TestParserFixtureCorpus_CL(t *testing.T) {
	fixtures := []struct {
		name         string
		sourceFile   string
		expectedFile string
		parsePath    string
	}{
		{
			name:         "invoice_batch",
			sourceFile:   "synthetic-ibmi/invoice_batch.clle",
			expectedFile: "synthetic-ibmi/invoice_batch.expected.json",
			parsePath:    "INVOICE_BATCH.CLLE",
		},
		{
			name:         "public_qshcallc",
			sourceFile:   "public-ibmi/public_qshcallc.clle",
			expectedFile: "public-ibmi/public_qshcallc.expected.json",
			parsePath:    "QSHCALLC.CLLE",
		},
		{
			name:         "public_package",
			sourceFile:   "public-ibmi/public_package.clle",
			expectedFile: "public-ibmi/public_package.expected.json",
			parsePath:    "PACKAGE.CLLE",
		},
	}

	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			expected := loadJSONFixture[rpgFixtureExpectation](t, fixture.expectedFile)
			content, err := os.ReadFile(fixturePath(t, fixture.sourceFile))
			if err != nil {
				t.Fatalf("failed to read fixture source: %v", err)
			}

			p := NewCLParser()
			result := p.ParseFile(fixture.parsePath, content)
			if result.ParseDiagnostics.Failed() {
				t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
			}

			if result.Language != expected.Language {
				t.Fatalf("expected language %q, got %q", expected.Language, result.Language)
			}

			functionNames := make([]string, 0, len(result.Functions))
			for _, fn := range result.Functions {
				functionNames = append(functionNames, fn.Name)
			}
			if !reflect.DeepEqual(normalizeNames(functionNames), normalizeNames(expected.Functions)) {
				t.Fatalf("unexpected functions: got=%v expected=%v", normalizeNames(functionNames), normalizeNames(expected.Functions))
			}

			if len(result.Imports) != len(expected.Imports) {
				t.Fatalf("unexpected import count: got=%d expected=%d", len(result.Imports), len(expected.Imports))
			}
			for fnName, want := range expected.Calls {
				got := make([]string, 0, len(result.FunctionCalls[fnName]))
				for _, call := range result.FunctionCalls[fnName] {
					got = append(got, call.CalleeName)
				}
				if !reflect.DeepEqual(uniqueNormalized(got), uniqueNormalized(want)) {
					t.Fatalf("unexpected calls for %s: got=%v want=%v", fnName, uniqueNormalized(got), uniqueNormalized(want))
				}
			}

			if !reflect.DeepEqual(normalizeParsedRPGDataAccesses(result.DataAccesses), normalizeRPGDataAccesses(expected.DataAccesses)) {
				t.Fatalf("unexpected data accesses: got=%v want=%v", normalizeParsedRPGDataAccesses(result.DataAccesses), normalizeRPGDataAccesses(expected.DataAccesses))
			}
		})
	}
}

func TestParserFixtureCorpus_DDS(t *testing.T) {
	fixtures := []struct {
		name         string
		sourceFile   string
		expectedFile string
		parsePath    string
	}{
		{
			name:         "resource_list",
			sourceFile:   "synthetic-ibmi/resource_list.dspf",
			expectedFile: "synthetic-ibmi/resource_list.expected.json",
			parsePath:    "RESOURCES.dspf",
		},
		{
			name:         "public_configs",
			sourceFile:   "public-ibmi/public_configs.dspf",
			expectedFile: "public-ibmi/public_configs.expected.json",
			parsePath:    "CONFIGS.dspf",
		},
	}

	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			expected := loadJSONFixture[ddsFixtureExpectation](t, fixture.expectedFile)
			content, err := os.ReadFile(fixturePath(t, fixture.sourceFile))
			if err != nil {
				t.Fatalf("failed to read fixture source: %v", err)
			}

			p := NewDDSParser()
			result := p.ParseFile(fixture.parsePath, content)
			if result.ParseDiagnostics.Failed() {
				t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
			}

			if result.Language != expected.Language {
				t.Fatalf("expected language %q, got %q", expected.Language, result.Language)
			}

			for _, className := range expected.RequiredClasses {
				if !hasClassNamed(result.Classes, className) {
					t.Fatalf("missing required class %q in %#v", className, result.Classes)
				}
			}

			for _, want := range expected.RequiredFields {
				cls := findClassByName(result.Classes, want.Class)
				if cls == nil {
					t.Fatalf("missing class %q for field check", want.Class)
				}
				found := false
				for _, field := range cls.Fields {
					if field.Name == want.Name && field.FieldType == want.FieldType {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("missing field %s:%s (%s) in %#v", want.Class, want.Name, want.FieldType, cls.Fields)
				}
			}
		})
	}
}
