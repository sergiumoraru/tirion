package parser

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestJavaScriptThisAndSuperCalls(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve test file path")
	}
	testPath := filepath.Join(filepath.Dir(file), "..", "..", "testdata", "TestJsCalls.ts")
	content, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatalf("failed to read test data: %v", err)
	}

	parser := NewJavaScriptParser()
	result := parser.ParseFile(testPath, content)

	barCalls, ok := result.FunctionCalls["Foo.bar"]
	if !ok {
		t.Fatalf("expected calls for Foo.bar, got none")
	}
	if len(barCalls) != 3 {
		t.Fatalf("expected 3 calls in Foo.bar, got %d", len(barCalls))
	}
	barCallees := map[string]bool{}
	for _, call := range barCalls {
		barCallees[call.CalleeName] = true
	}
	for _, name := range []string{"Foo.baz", "service.qux", "Foo.quux"} {
		if !barCallees[name] {
			t.Fatalf("expected call %q in Foo.bar", name)
		}
	}

	standaloneCalls, ok := result.FunctionCalls["standalone"]
	if !ok {
		t.Fatalf("expected calls for standalone, got none")
	}
	if len(standaloneCalls) != 1 || standaloneCalls[0].CalleeName != "Foo.baz" {
		t.Fatalf("expected standalone to call foo.baz, got %#v", standaloneCalls)
	}
}

func TestJavaScriptJQueryCallbackCalls(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve test file path")
	}
	testPath := filepath.Join(filepath.Dir(file), "..", "..", "testdata", "TestJsJqueryCallbacks.ts")
	content, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatalf("failed to read test data: %v", err)
	}

	parser := NewJavaScriptParser()
	result := parser.ParseFile(testPath, content)

	initCalls, ok := result.FunctionCalls["initLogin"]
	if !ok {
		t.Fatalf("expected calls for initLogin, got none")
	}
	if len(initCalls) != 1 || initCalls[0].CalleeName != "jquery.click__forgot_password_L2" {
		t.Fatalf("expected initLogin to call jquery.click__forgot_password_L2, got %#v", initCalls)
	}

	callbackCalls, ok := result.FunctionCalls["jquery.click__forgot_password_L2"]
	if !ok {
		t.Fatalf("expected calls for jquery.click__forgot_password_L2, got none")
	}
	if len(callbackCalls) != 1 || callbackCalls[0].CalleeName != "submitLogin" {
		t.Fatalf("expected jquery.click__forgot_password_L2 to call submitLogin, got %#v", callbackCalls)
	}
}

func TestJavaScriptCallbackHandlers(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve test file path")
	}
	testPath := filepath.Join(filepath.Dir(file), "..", "..", "testdata", "TestJsCallbacks.ts")
	content, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatalf("failed to read test data: %v", err)
	}

	parser := NewJavaScriptParser()
	result := parser.ParseFile(testPath, content)

	initCalls, ok := result.FunctionCalls["init"]
	if !ok {
		t.Fatalf("expected calls for init, got none")
	}
	for _, name := range []string{"dom.click_L2", "timer.setTimeout_L5", "promise.then_L8", "promise.catch_L10"} {
		if !hasCall(initCalls, name) {
			t.Fatalf("expected init to call %q, got %#v", name, initCalls)
		}
	}

	assertCallbackCall(t, result, "dom.click_L2", "onClick")
	assertCallbackCall(t, result, "timer.setTimeout_L5", "onTimeout")
	assertCallbackCall(t, result, "promise.then_L8", "onThen")
	assertCallbackCall(t, result, "promise.catch_L10", "onCatch")
}

func TestJavaScriptCallbackReferenceCalls(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve test file path")
	}
	testPath := filepath.Join(filepath.Dir(file), "..", "..", "testdata", "TestJsCallbackReferences.ts")
	content, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatalf("failed to read test data: %v", err)
	}

	parser := NewJavaScriptParser()
	result := parser.ParseFile(testPath, content)

	initCalls, ok := result.FunctionCalls["init"]
	if !ok {
		t.Fatalf("expected calls for init, got none")
	}
	for _, name := range []string{
		"jquery.click__submit_L2",
		"jquery.on_click_.item_L3",
		"dom.click_L4",
	} {
		if !hasCall(initCalls, name) {
			t.Fatalf("expected init to call %q, got %#v", name, initCalls)
		}
	}

	assertCallbackCall(t, result, "jquery.click__submit_L2", "handleSubmit")
	assertCallbackCall(t, result, "jquery.on_click_.item_L3", "handleItem")
	assertCallbackCall(t, result, "dom.click_L4", "onClick")
}

func TestJavaScriptResolvesLocalInstanceMethodCallsToClassMethods(t *testing.T) {
	content := []byte(`
class DefinitionsPublisher {
  processActive = async () => {}
  publish = async () => {}
}

export async function serviceBusTrigger() {
  const definitionsPublisher = new DefinitionsPublisher();
  await definitionsPublisher.processActive();
  await definitionsPublisher.publish();
}
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("WatchPublishDocumentCycles/index.ts", content)

	calls, ok := result.FunctionCalls["serviceBusTrigger"]
	if !ok {
		t.Fatalf("expected calls for serviceBusTrigger, got none")
	}
	for _, name := range []string{
		"DefinitionsPublisher.processActive",
		"DefinitionsPublisher.publish",
	} {
		if !hasCall(calls, name) {
			t.Fatalf("expected serviceBusTrigger to call %q, got %#v", name, calls)
		}
	}
}

func TestJavaScriptChainedCallsUseTerminalMethodNames(t *testing.T) {
	content := []byte(`
export function makePath() {
  return __dirname.split("\\").slice(0, -1).join("/")
}
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("path-utils.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	calls, ok := result.FunctionCalls["makePath"]
	if !ok {
		t.Fatalf("expected calls for makePath, got none")
	}
	for _, bad := range []string{`__dirname.split("\\").slice`, `__dirname.split("\\").slice(0, -1).join`} {
		if hasCall(calls, bad) {
			t.Fatalf("did not expect chained source expression callee %q, got %#v", bad, calls)
		}
	}
	for _, name := range []string{"split", "slice", "join"} {
		if !hasCall(calls, name) {
			t.Fatalf("expected terminal method call %q, got %#v", name, calls)
		}
	}
}

func TestJavaScriptCompositionHookOrigins(t *testing.T) {
	content := []byte(`
export function setupWidget() {
  const count = ref(0)
  const route = useRoute()
  const store = useWidgetStore()
  const custom = useProjectData()
  const [value] = useState(0)
  onMounted(() => {})
  defineProps({ id: String })
  defineEmits(['save'])
  return { count, route, store, custom, value }
}
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("Widget.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	origins := map[string]string{}
	custom := map[string]bool{}
	for _, hook := range result.HookCalls {
		origins[hook.HookName] = hook.Origin
		custom[hook.HookName] = hook.IsCustomHook
	}

	want := map[string]string{
		"ref":            "vue_reactivity",
		"useRoute":       "vue_router",
		"useWidgetStore": "pinia",
		"useProjectData": "custom",
		"useState":       "react",
		"onMounted":      "vue_lifecycle",
		"defineProps":    "vue_macro",
		"defineEmits":    "vue_macro",
	}
	for hook, origin := range want {
		if origins[hook] != origin {
			t.Fatalf("expected %s origin %q, got origins=%#v hooks=%#v", hook, origin, origins, result.HookCalls)
		}
	}
	if !custom["useProjectData"] {
		t.Fatalf("expected useProjectData to remain custom, got %#v", result.HookCalls)
	}
	if custom["ref"] || custom["useRoute"] || custom["useWidgetStore"] || custom["useState"] || custom["defineProps"] {
		t.Fatalf("expected framework hooks to be non-custom, got %#v", result.HookCalls)
	}
}

func TestJavaScriptVueComponentContracts(t *testing.T) {
	content := []byte(`
<template><button /></template>
<script setup lang="ts">
const props = defineProps<{
  productId: string
  disabled?: boolean
  owner: UserOwner
}>()
const emit = defineEmits<{
  (e: 'save', payload: Product): void
  (e: 'cancel'): void
}>()
defineExpose({ focus, reset })
const model = defineModel<string>()
const runtimeProps = defineProps({
  jurisdictionId: { type: String, required: true },
  pageSize: Number,
})
const runtimeEmit = defineEmits(['loaded', 'failed'])
</script>
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("Contract.vue", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	for _, want := range []struct {
		kind     string
		name     string
		typeName string
		required bool
	}{
		{kind: "props", name: "productId", typeName: "string", required: true},
		{kind: "props", name: "disabled", typeName: "boolean", required: false},
		{kind: "props", name: "owner", typeName: "UserOwner", required: true},
		{kind: "emits", name: "save", required: true},
		{kind: "emits", name: "cancel", required: true},
		{kind: "expose", name: "focus", required: false},
		{kind: "expose", name: "reset", required: false},
		{kind: "model", name: "modelValue", typeName: "string", required: false},
		{kind: "props", name: "jurisdictionId", typeName: "String", required: true},
		{kind: "props", name: "pageSize", typeName: "Number", required: false},
		{kind: "emits", name: "loaded", required: true},
		{kind: "emits", name: "failed", required: true},
	} {
		got, ok := findVueContract(result.VueComponentContracts, want.kind, want.name)
		if !ok {
			t.Fatalf("expected Vue contract %s.%s, got %#v", want.kind, want.name, result.VueComponentContracts)
		}
		if got.FieldType != want.typeName {
			t.Fatalf("expected %s.%s type %q, got %#v", want.kind, want.name, want.typeName, got)
		}
		if got.IsRequired != want.required {
			t.Fatalf("expected %s.%s required=%v, got %#v", want.kind, want.name, want.required, got)
		}
	}
}

func TestJavaScriptPiniaStoreDefinitions(t *testing.T) {
	content := []byte(`
import { defineStore } from 'pinia'
import { ref, computed } from 'vue'

export const useProductStore = defineStore('product', {
  state: () => ({
    selectedProductId: '',
    pageSize: 25,
  }),
  getters: {
    selectedProduct: (state) => state.selectedProductId,
    hasSelection() { return Boolean(this.selectedProductId) },
  },
  actions: {
    selectProduct(id: string) { this.selectedProductId = id },
    clearSelection() { this.selectedProductId = '' },
  },
})

export const useSessionStore = defineStore('session', () => {
  const token = ref('')
  const isLoggedIn = computed(() => Boolean(token.value))
  function setToken(value: string) {
    token.value = value
  }
  return { token, isLoggedIn, setToken }
})
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("stores/product.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	product, ok := findPiniaStore(result.PiniaStores, "product")
	if !ok {
		t.Fatalf("expected product Pinia store, got %#v", result.PiniaStores)
	}
	if !sameStringSet(product.StateFields, []string{"pageSize", "selectedProductId"}) {
		t.Fatalf("unexpected product state fields: %#v", product)
	}
	if !sameStringSet(product.Getters, []string{"hasSelection", "selectedProduct"}) {
		t.Fatalf("unexpected product getters: %#v", product)
	}
	if !sameStringSet(product.Actions, []string{"clearSelection", "selectProduct"}) {
		t.Fatalf("unexpected product actions: %#v", product)
	}

	session, ok := findPiniaStore(result.PiniaStores, "session")
	if !ok {
		t.Fatalf("expected session Pinia store, got %#v", result.PiniaStores)
	}
	if !sameStringSet(session.StateFields, []string{"isLoggedIn", "token"}) {
		t.Fatalf("unexpected session state fields: %#v", session)
	}
	if !sameStringSet(session.Actions, []string{"setToken"}) {
		t.Fatalf("unexpected session actions: %#v", session)
	}
}

func findPiniaStore(stores []ParsedPiniaStore, storeID string) (ParsedPiniaStore, bool) {
	for _, store := range stores {
		if store.StoreID == storeID {
			return store, true
		}
	}
	return ParsedPiniaStore{}, false
}

func sameStringSet(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]int{}
	for _, item := range got {
		seen[item]++
	}
	for _, item := range want {
		if seen[item] == 0 {
			return false
		}
		seen[item]--
	}
	return true
}

func findVueContract(contracts []ParsedVueComponentContract, kind, name string) (ParsedVueComponentContract, bool) {
	for _, contract := range contracts {
		if contract.Kind == kind && contract.FieldName == name {
			return contract, true
		}
	}
	return ParsedVueComponentContract{}, false
}

func hasCall(calls []ParsedFunctionCall, name string) bool {
	for _, call := range calls {
		if call.CalleeName == name {
			return true
		}
	}
	return false
}

func assertCallbackCall(t *testing.T, result ParsedFile, caller string, callee string) {
	t.Helper()
	calls, ok := result.FunctionCalls[caller]
	if !ok {
		t.Fatalf("expected calls for %s, got none", caller)
	}
	if !hasCall(calls, callee) {
		t.Fatalf("expected %s to call %s, got %#v", caller, callee, calls)
	}
}
