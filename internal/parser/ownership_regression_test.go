package parser

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sergiumoraru/tirion/internal/config"
)

func TestJavaScriptCallbackEvidenceAndOwnership(t *testing.T) {
	source := []byte("function send() {}\nfunction ignore(cb) {}\n" +
		"function timer() { setTimeout(send, 0); }\n" +
		"function ignored() { ignore(() => send()); }\n" +
		"function immediate() { (() => send())(); }\n" +
		"function array() { rows.forEach(x => send(x)); }\n" +
		"function promise() { Promise.resolve().then(() => send()); }\n" +
		"function event() { element.addEventListener('click', send); }\n" +
		"function extra() { setTimeout(send, 0, () => ignore()); }\n" +
		"function recover() {}\nfunction both() { Promise.resolve().then(\n  send,\n  /* rejection */ recover\n); }\n")
	result := NewJavaScriptParser().ParseFile("callbacks.js", source)
	if result.ParseDiagnostics.Failed() {
		t.Fatal(result.ParseDiagnostics)
	}
	reachable := func(start, target string, includeSpeculative bool) bool {
		queue := []string{start}
		seen := map[string]bool{}
		for len(queue) > 0 {
			name := queue[0]
			queue = queue[1:]
			if name == target {
				return true
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			for _, call := range result.FunctionCalls[name] {
				if includeSpeculative || !call.IsCallbackArgument {
					queue = append(queue, call.CalleeName)
				}
			}
		}
		return false
	}
	for _, name := range []string{"timer", "immediate", "promise", "event", "extra"} {
		if !reachable(name, "send", false) {
			t.Errorf("supported path %s -> send missing: %#v", name, result.FunctionCalls)
		}
	}
	for _, name := range []string{"ignored", "array"} {
		if !reachable(name, "send", true) {
			t.Errorf("discovery path %s -> send missing", name)
		}
		if reachable(name, "send", false) {
			t.Errorf("speculative path %s became invocation proof", name)
		}
	}
	if reachable("extra", "ignore", false) {
		t.Fatal("extra timer argument became a handler")
	}
	if !reachable("extra", "ignore", true) {
		t.Fatal("extra timer argument lost discovery connection")
	}
	for _, handler := range []string{"send", "recover"} {
		if !reachable("both", handler, false) {
			t.Errorf("Promise.then lost supported handler %s", handler)
		}
	}
	counts := map[string]int{}
	for _, fn := range result.Functions {
		counts[fn.Name]++
	}
	for name, count := range counts {
		if count != 1 {
			t.Errorf("duplicate %s: %d", name, count)
		}
	}
}

func TestJavaScriptMultilineArrowHasOneCompleteIdentity(t *testing.T) {
	source := []byte("const transform =\n  async <T>(value: T): Promise<T> => { return send(value); };\nconst empty = () => value;\n")
	result := NewJavaScriptParser().ParseFile("arrows.ts", source)
	var declarations []ParsedFunction
	for _, fn := range result.Functions {
		if fn.Name == "transform" {
			declarations = append(declarations, fn)
		}
		if fn.Name == "empty" && len(fn.Params) != 0 {
			t.Errorf("expression result treated as parameter: %#v", fn)
		}
	}
	if len(declarations) != 1 {
		t.Fatalf("want one transform, got %#v", result.Functions)
	}
	fn := declarations[0]
	if fn.StartLine != 1 || !fn.IsAsync || fn.ReturnType != "Promise<T>" ||
		!reflect.DeepEqual(fn.Params, []string{"value"}) || !reflect.DeepEqual(fn.ParamTypes, []string{"T"}) ||
		len(fn.TypeParameters) != 1 || fn.TypeParameters[0].Name != "T" {
		t.Fatalf("incomplete declaration: %#v", fn)
	}
	if !hasCallee(result.FunctionCalls["transform"], "send") {
		t.Fatal("body call separated from declaration")
	}
}

func TestCSharpMultilineBodiesDoNotLeakOwnership(t *testing.T) {
	source := "public class Sample\n{\n" +
		"    public string Quote => @\"\"\"\";\n" +
		"    public void Next() { Work(); }\n" +
		"    public string Convert(\n        string value) {\n        return Map(value);\n    }\n" +
		"    public string Expression(string value) =>\n        Map(value);\n" +
		"    public string Property =>\n        Read();\n" +
		"    public void Last() { Finish(); }\n}\n"
	result := NewCSharpParser().ParseFile("Sample.cs", []byte(source))
	want := map[string]string{"Next": "Work", "Convert": "Map", "Expression": "Map", "Property.get": "Read", "Last": "Finish"}
	for method, callee := range want {
		if !hasFunction(result.Functions, "Sample."+method) || !hasCallee(result.FunctionCalls["Sample."+method], callee) {
			t.Errorf("%s -> %s missing: functions=%#v calls=%#v", method, callee, result.Functions, result.FunctionCalls)
		}
	}
	if len(result.FunctionCalls["Sample.Quote.get"]) != 0 {
		t.Errorf("Quote absorbed following methods: %#v", result.FunctionCalls)
	}
	for _, fn := range result.Functions {
		if fn.Name == "Sample.Quote.get" && fn.EndLine != 3 {
			t.Errorf("Quote extends to %d", fn.EndLine)
		}
	}
}

func TestJavaScriptTypedArrowRecoveryRetainsBinding(t *testing.T) {
	source := []byte("const dimensions = (\n  value: number,\n  total: number,\n  archived = false\n): ViewStyleProp => { return send(value); };\n")
	result := NewJavaScriptParser().ParseFile("dimensions.js", source)
	if !hasFunction(result.Functions, "dimensions") || !hasCallee(result.FunctionCalls["dimensions"], "send") {
		t.Fatalf("binding lost through parser recovery: %#v %#v", result.Functions, result.FunctionCalls)
	}
	for _, fn := range result.Functions {
		if fn.Name == "dimensions" && fn.StartLine != 1 {
			t.Errorf("binding span starts at %d", fn.StartLine)
		}
	}
}

func TestMergeRepeatedInterfacePayload(t *testing.T) {
	result := NewJavaScriptParser().ParseFile("merged.ts", []byte("interface X<T> extends A { a(): void; left: string }\ninterface X<T> extends B { b(): void; right: number }"))
	merged := MergeInterfaceDeclarations(result.Interfaces)
	if len(merged) != 1 {
		t.Fatalf("want one interface: %#v", merged)
	}
	got := merged[0]
	if !reflect.DeepEqual(got.ExtendsInterfaces, []string{"A", "B"}) || len(got.Methods) != 2 || len(got.Fields) != 2 || len(got.TypeParameters) != 1 {
		t.Fatalf("lost merged metadata: %#v", got)
	}
}

func TestCSharpGenericLocalTupleAndAccessorOwnership(t *testing.T) {
	source := `public class Sample
{
    public void Run()
    {
        string Local(string value)
        {
            return Convert(value);
        }
        Local("value");
    }
    private static T Fetch<T>(string key) where T : IValue
    {
        return Client.Read(key);
    }
    private (int count, string label) Pair(string key)
    {
        return (Count(key), Label(key));
    }
    public string Value
    {
        get
        {
            return Read();
        }
        private set { Write(value); }
    }
    public string Inline { get { return Read(); } set => Write(value); }
    public void Last() { Finish(); }
}`
	result := NewCSharpParser().ParseFile("Sample.cs", []byte(source))
	for _, pair := range [][2]string{{"Run.Local", "Convert"}, {"Fetch", "Client.Read"}, {"Pair", "Count"}, {"Pair", "Label"}, {"Value.get", "Read"}, {"Value.set", "Write"}, {"Inline.get", "Read"}, {"Inline.set", "Write"}, {"Last", "Finish"}} {
		if !hasFunction(result.Functions, "Sample."+pair[0]) || !hasCallee(result.FunctionCalls["Sample."+pair[0]], pair[1]) {
			t.Errorf("missing %s -> %s: %#v", pair[0], pair[1], result.FunctionCalls)
		}
	}
	if hasFunction(result.Functions, "Sample.Local") {
		t.Fatal("local function was hoisted to a class member")
	}
	if hasCallee(result.FunctionCalls["Sample.Run"], "Convert") {
		t.Fatal("local function body leaked into enclosing method")
	}
	if !hasCallee(result.FunctionCalls["Sample.Run"], "Local") {
		t.Fatal("enclosing method lost its local function invocation")
	}
}

func TestJavaPropertyQueueResolutionShared(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "queues.properties"), []byte("queue.orders=orders-events\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.MergePatterns(nil)
	cfg.JavaSQS = config.JavaSQSFramework{ConsumerTypes: []string{"MessageHandler"}, HandlerMethod: "handle", PropertyAnnotation: "Named"}
	p := NewJavaParser()
	p.SetConfig(cfg)
	result := p.ParseFile("Consumer.java", []byte("class Consumer implements MessageHandler { Consumer(@Named(\"queue.orders\") String queue) {} public void handle(String body) {} }"))
	if !HasPropertyQueueConsumer(result, cfg.JavaSQS) {
		t.Fatalf("property consumer not recognized: %#v", result.Classes)
	}
	properties, err := LoadJavaQueueProperties(root)
	if err != nil {
		t.Fatal(err)
	}
	consumers := DerivePropertyQueueConsumers(result, properties, cfg.JavaSQS)
	if len(consumers) != 1 || consumers[0].QueueName != "orders-events" || !strings.HasSuffix(consumers[0].HandlerMethod, "handle") {
		t.Fatalf("wrong consumers: %#v; classes=%#v functions=%#v properties=%#v", consumers, result.Classes, result.Functions, properties)
	}
}

func TestCSharpInterpolationAndCommentBoundaries(t *testing.T) {
	source := `public class Sample
{
    /*
    public void Removed() {
        Lost();
    }
    */
    public IEnumerable<(string key, string value)> Pairs(string value)
    {
        string text = $"{{escaped}} '{value.Replace("'", "''")}'";
        string nested = $@"value {Format($"inner {Read("}")}")} doubled ""quote""";
        return Fetch();
    }
    public void Next() { Work(); }
}`
	result := NewCSharpParser().ParseFile("Sample.cs", []byte(source))
	if hasFunction(result.Functions, "Sample.Removed") {
		t.Fatal("comment became a declaration")
	}
	for _, pair := range [][2]string{{"Pairs", "Fetch"}, {"Pairs", "value.Replace"}, {"Pairs", "Format"}, {"Pairs", "Read"}, {"Next", "Work"}} {
		if !hasFunction(result.Functions, "Sample."+pair[0]) || !hasCallee(result.FunctionCalls["Sample."+pair[0]], pair[1]) {
			t.Fatalf("lost %v: %#v %#v", pair, result.Functions, result.FunctionCalls)
		}
	}
	for _, fn := range result.Functions {
		if fn.Name == "Sample.Pairs" && fn.EndLine != 13 {
			t.Errorf("interpolated string extended method to %d", fn.EndLine)
		}
	}
}

func TestCSharpInterpolationCallOwnership(t *testing.T) {
	source := `public class Sample
{
    public string Name { get { return $"literal Fake() {Load()}"; } }
    public string Text()
    {
        return $@"literal Fake()
{Read()} {Format($"nested {Load()}")}
{{Ignored()}} {Count():Fake()}";
    }
    public string Raw() => $$"""
        {Ignored()} {{Read()}} {{Count():Fake()}}
        """;
    public string Escaped => @"""";
    public void Next() { Work(); }
    public string Load() => "";
    public string Read() => "";
    public string Format(string value) => value;
    public int Count() => 1;
    public void Work() { }
}`
	result := NewCSharpParser().ParseFile("Sample.cs", []byte(source))
	want := map[string]map[string]int{
		"Sample.Name.get": {"Load": 3},
		"Sample.Text":     {"Read": 7, "Format": 7, "Load": 7, "Count": 8},
		"Sample.Raw":      {"Read": 11, "Count": 11},
		"Sample.Next":     {"Work": 14},
	}
	for owner, targets := range want {
		if !hasFunction(result.Functions, owner) {
			t.Errorf("missing owner %s", owner)
		}
		calls := result.FunctionCalls[owner]
		if len(calls) != len(targets) {
			t.Errorf("%s: expected %d calls, got %#v", owner, len(targets), calls)
		}
		for target, line := range targets {
			found := false
			for _, call := range calls {
				if call.CalleeName == target && call.LineNumber == line {
					found = true
				}
			}
			if !found {
				t.Errorf("missing %s -> %s at %d: %#v", owner, target, line, calls)
			}
		}
	}
	for owner, calls := range result.FunctionCalls {
		for _, call := range calls {
			if _, ok := want[owner][call.CalleeName]; !ok {
				t.Errorf("unexpected call %s -> %#v", owner, call)
			}
		}
	}
	ends := map[string]int{"Sample.Name.get": 3, "Sample.Text": 9, "Sample.Raw": 12, "Sample.Escaped.get": 13, "Sample.Next": 14}
	for _, fn := range result.Functions {
		if end, ok := ends[fn.Name]; ok && fn.EndLine != end {
			t.Errorf("%s absorbed following source: end=%d, want %d", fn.Name, fn.EndLine, end)
		}
	}
}
