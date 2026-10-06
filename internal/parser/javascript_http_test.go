package parser

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestJavaScriptHttpCallsFromConfig(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve test file path")
	}
	testPath := filepath.Join(filepath.Dir(file), "..", "..", "testdata", "TestJsHttpCalls.ts")
	content, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatalf("failed to read test data: %v", err)
	}

	parser := NewJavaScriptParser()
	result := parser.ParseFile(testPath, content)

	calls, ok := result.HttpCalls["makeCalls"]
	if !ok {
		t.Fatalf("expected HTTP calls for makeCalls, got none")
	}

	byURL := make(map[string]ParsedHttpCall)
	for _, call := range calls {
		byURL[call.UrlPattern] = call
	}

	expected := map[string]struct {
		method     string
		clientType string
	}{
		"/api/foo":         {method: "POST", clientType: "axios"},
		"/api/req":         {method: "PUT", clientType: "axios"},
		"/api/fetch":       {method: "PATCH", clientType: "fetch"},
		"/api/optional":    {method: "GET", clientType: "axios"},
		"/api/short":       {method: "ANY", clientType: "axios"},
		"/api/quoted":      {method: "POST", clientType: "axios"},
		"/api/dynamic":     {method: "ANY", clientType: "axios"},
		"/api/jquery":      {method: "DELETE", clientType: "jquery"},
		"/api/jquery-get":  {method: "GET", clientType: "jquery"},
		"/api/got-default": {method: "GET", clientType: "got"},
		"/api/got-post":    {method: "POST", clientType: "got"},
		"/api/ky-default":  {method: "DELETE", clientType: "ky"},
		"/api/ky-patch":    {method: "PATCH", clientType: "ky"},
		"/api/factory":     {method: "GET", clientType: "axios"},
		"/api/factory2":    {method: "POST", clientType: "axios"},
	}

	for url, want := range expected {
		call, exists := byURL[url]
		if !exists {
			t.Fatalf("expected HTTP call to %s, got none", url)
		}
		if call.HttpMethod != want.method {
			t.Fatalf("expected %s to use method %s, got %s", url, want.method, call.HttpMethod)
		}
		if call.ClientType != want.clientType {
			t.Fatalf("expected %s to use client %s, got %s", url, want.clientType, call.ClientType)
		}
	}
}

func TestJavaScriptAngularRouteEndpoints(t *testing.T) {
	content := []byte(`
import { RouterModule, Routes } from '@angular/router'

const routes: Routes = [
  { path: '', redirectTo: 'error', pathMatch: 'full' },
  { path: 'resources/:resourceId', loadChildren: () => import('./resource.module') },
  { path: 'cancel', loadChildren: () => import('./cancel.module') },
  { path: '**', redirectTo: 'error' }
]

@NgModule({
  imports: [RouterModule.forRoot(routes)],
  exports: [RouterModule]
})
export class AppRoutingModule {}
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("app-routing.module.ts", content)

	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	hasEndpoint := func(method, path, handler string) bool {
		for _, ep := range result.Endpoints {
			if ep.Method == method && ep.Path == path && ep.HandlerName == handler {
				return true
			}
		}
		return false
	}

	if !hasEndpoint("REQUEST", "/resources/:resourceId", "resource") {
		t.Fatalf("expected angular route endpoint for /resources/:resourceId, got %#v", result.Endpoints)
	}
	if !hasEndpoint("REQUEST", "/resources", "resource") {
		t.Fatalf("expected base angular route endpoint /resources, got %#v", result.Endpoints)
	}
	if !hasEndpoint("REQUEST", "/cancel", "cancel") {
		t.Fatalf("expected angular route endpoint for /cancel, got %#v", result.Endpoints)
	}

	hasModuleFunction := false
	for _, fn := range result.Functions {
		if fn.Name == "_module_" {
			hasModuleFunction = true
			break
		}
	}
	if !hasModuleFunction {
		t.Fatalf("expected synthetic _module_ function for angular route endpoints")
	}
}

func TestJavaScriptVueRouterRouteEndpointsUseComponentHandlers(t *testing.T) {
	content := []byte(`
import { createRouter } from 'vue-router'
import ResourceList from '@/views/resources/ResourceList.vue'

const routes: RouteRecordRaw[] = [
  { path: '/resources', name: 'resources', component: ResourceList },
  { path: '/publish/:id', component: () => import('./publish/PublishView.vue') },
]

export default createRouter({ routes })
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("src/router/index.ts", content)

	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	hasEndpoint := func(method, path, handler string) bool {
		for _, ep := range result.Endpoints {
			if ep.Method == method && ep.Path == path && ep.HandlerName == handler {
				return true
			}
		}
		return false
	}

	if !hasEndpoint("REQUEST", "/resources", "ResourceList") {
		t.Fatalf("expected Vue route /resources to target ResourceList, got %#v", result.Endpoints)
	}
	if !hasEndpoint("REQUEST", "/publish/:id", "PublishView") {
		t.Fatalf("expected Vue route /publish/:id to target lazy PublishView import, got %#v", result.Endpoints)
	}
	if !hasEndpoint("REQUEST", "/publish", "PublishView") {
		t.Fatalf("expected Vue route base /publish to target lazy PublishView import, got %#v", result.Endpoints)
	}
	if !hasCallee(result.FunctionCalls["_module_"], "ResourceList") {
		t.Fatalf("expected route module to call ResourceList, got %#v", result.FunctionCalls)
	}
	if !hasCallee(result.FunctionCalls["_module_"], "PublishView") {
		t.Fatalf("expected route module to call PublishView, got %#v", result.FunctionCalls)
	}
}

func TestJavaScriptAliasGenericEntryFunctionsRemapsEndpointHandlerName(t *testing.T) {
	content := []byte(`
const express = require('express')
const app = express()
export const handler = async (req, res) => {
  return res.status(200).json({ ok: true })
}
app.get('/reports', handler)
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("reports/index.js", content)

	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	foundEndpoint := false
	for _, ep := range result.Endpoints {
		if ep.Method == "GET" && ep.Path == "/reports" && ep.HandlerName == "reports" {
			foundEndpoint = true
			break
		}
	}
	if !foundEndpoint {
		t.Fatalf("expected aliased endpoint handler name 'reports', got %#v", result.Endpoints)
	}
}

func TestJavaScriptDecoratedClassFieldMethodPreservesAnnotations(t *testing.T) {
	content := []byte(`
class ReportController {
  @Get('/reports')
  handler = () => {
    return true
  }
}
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("report-controller.ts", content)

	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	var handler *ParsedFunction
	for i := range result.Functions {
		if result.Functions[i].Name == "ReportController.handler" {
			handler = &result.Functions[i]
			break
		}
	}
	if handler == nil {
		t.Fatalf("expected synthetic class field method, got %#v", result.Functions)
	}
	if len(handler.Annotations) == 0 || handler.Annotations[0].Name != "Get" {
		t.Fatalf("expected handler annotations to include @Get, got %#v", handler.Annotations)
	}
}

func TestJavaScriptLocalInstanceCallRewriteRequiresPriorVisibleDeclaration(t *testing.T) {
	content := []byte(`
function runLater() {
  helper.send()
  const helper = new Mailer()
}

function runNested() {
  if (enabled) {
    const helper = new Mailer()
  }
  helper.send()
}
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("mailer.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	assertNoMailerSend := func(funcName string) {
		calls := result.FunctionCalls[funcName]
		if len(calls) == 0 {
			t.Fatalf("expected calls for %s, got none", funcName)
		}
		for _, call := range calls {
			if call.CalleeName == "Mailer.send" {
				t.Fatalf("did not expect %s to resolve to Mailer.send, got %#v", funcName, calls)
			}
		}
	}

	assertNoMailerSend("runLater")
	assertNoMailerSend("runNested")
}

func TestJavaScriptBrowserNavigationAssignmentAsHttpCall(t *testing.T) {
	content := []byte("export function navigateResource(path) {\n" +
		"  window.location.href = `/proxy?path=${path}`\n" +
		"}\n")

	parser := NewJavaScriptParser()
	result := parser.ParseFile("navigation.js", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	calls := result.HttpCalls["navigateResource"]
	if len(calls) == 0 {
		t.Fatalf("expected navigation http call for navigateResource, got none")
	}

	found := false
	for _, call := range calls {
		if call.UrlPattern == "/proxy" && call.HttpMethod == "REQUEST" && call.ClientType == "navigation" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected REQUEST /proxy navigation call, got %#v", calls)
	}
}

func TestJavaScriptHttpCallsWithURLPrefixVariables(t *testing.T) {
	content := []byte(`
const apiUrl = '/api'
const CONTEXT_PATH = '/portal'

export function loadResources() {
  return axios.get(apiUrl + '/resources')
}

export function openResourceFrame(iframePath) {
  return httpClient.request({
    method: 'REQUEST',
    url: CONTEXT_PATH + '/proxy?path=' + iframePath,
  })
}
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("resourceClient.js", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	loadCalls := result.HttpCalls["loadResources"]
	if len(loadCalls) != 1 {
		t.Fatalf("expected one HTTP call for loadResources, got %#v", loadCalls)
	}
	if loadCalls[0].UrlPattern != "/resources" || loadCalls[0].HttpMethod != "GET" {
		t.Fatalf("expected GET /resources, got %#v", loadCalls[0])
	}

	iframeCalls := result.HttpCalls["openResourceFrame"]
	if len(iframeCalls) != 1 {
		t.Fatalf("expected one HTTP call for openResourceFrame, got %#v", iframeCalls)
	}
	if iframeCalls[0].UrlPattern != "/proxy" || iframeCalls[0].HttpMethod != "REQUEST" {
		t.Fatalf("expected REQUEST /proxy, got %#v", iframeCalls[0])
	}
}

func TestJavaScriptHttpCallsDoNotTreatGenericURLVariablesAsAPIPrefixes(t *testing.T) {
	content := []byte(`
const redirectUrl = '/resources'

export function openLanding() {
  return axios.get(redirectUrl + '/details')
}
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("urls.js", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	calls := result.HttpCalls["openLanding"]
	if len(calls) != 1 {
		t.Fatalf("expected one HTTP call, got %#v", calls)
	}
	if calls[0].UrlPattern != "/:redirectUrl/details" {
		t.Fatalf("expected generic variable placeholder path, got %#v", calls[0])
	}
}

func TestJavaScriptHttpCallsTrimTemplateBaseURLVariables(t *testing.T) {
	content := []byte(
		"const baseApiUrl = process.env.BASE_API_URL\n" +
			"const resourceServiceUrl = process.env.RESOURCE_SERVICE_URL\n\n" +
			"export async function applyChanges(changes) {\n" +
			"  await axios.post(`${baseApiUrl}resources/batch`, { changes })\n" +
			"}\n\n" +
			"export async function importResources(form) {\n" +
			"  await axios.post(`${resourceServiceUrl}/import`, form)\n" +
			"}\n\n" +
			"export async function loadPage(id) {\n" +
			"  await axios.get(`/api/pages/${id}/details`)\n" +
			"}\n")

	parser := NewJavaScriptParser()
	result := parser.ParseFile("template_urls.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	assertCall := func(functionName, method, url string) {
		t.Helper()
		for _, call := range result.HttpCalls[functionName] {
			if call.HttpMethod == method && call.UrlPattern == url {
				return
			}
		}
		t.Fatalf("expected %s to contain %s %s, got %#v", functionName, method, url, result.HttpCalls[functionName])
	}
	assertCall("applyChanges", "POST", "/resources/batch")
	assertCall("importResources", "POST", "/import")
	assertCall("loadPage", "GET", "/api/pages/:id/details")
}

func TestJavaScriptHttpCallsResolveVariableHeldConfigObject(t *testing.T) {
	content := []byte(`
export async function callConfiguredApi(token) {
  const options = {
    url: this.environment.apiServer,
    method: 'post',
    headers: { Authorization: token },
  }
  return axios(options)
}

export async function callResourceService(url, update) {
  return axios({ url, method: 'post', data: update })
}
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("ResourceClient.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	authCalls := result.HttpCalls["callConfiguredApi"]
	if len(authCalls) != 0 {
		t.Fatalf("expected base-only apiServer URL to be dropped, got %#v", authCalls)
	}

	permissionCalls := result.HttpCalls["callResourceService"]
	if len(permissionCalls) != 0 {
		t.Fatalf("expected unresolved url variable to be dropped, got %#v", permissionCalls)
	}
}

func TestJavaScriptHttpCallsDropUnresolvedBaseURLPlaceholders(t *testing.T) {
	content := []byte(`
export async function baseOnly(apiUrl) {
  return axios.post(apiUrl, { ok: true })
}

export async function unknownContextPath(API_GLOBAL) {
  return axios.get(API_GLOBAL + 'Global')
}

export async function relativeAfterBase(API_ROOT) {
  return axios.post(API_ROOT + 'resources/items')
}

export async function stackedApimBase(context) {
  return axios.post(` + "`" + `${context.env.apim}${context.env.resourceEndpoint}/resources` + "`" + `, {})
}
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("baseUrls.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if calls := result.HttpCalls["baseOnly"]; len(calls) != 0 {
		t.Fatalf("expected direct base URL variable to be dropped, got %#v", calls)
	}
	if calls := result.HttpCalls["unknownContextPath"]; len(calls) != 0 {
		t.Fatalf("expected unresolved context-path concatenation to be dropped, got %#v", calls)
	}

	relativeCalls := result.HttpCalls["relativeAfterBase"]
	if len(relativeCalls) != 1 {
		t.Fatalf("expected relative path after base prefix to be retained, got %#v", relativeCalls)
	}
	if relativeCalls[0].UrlPattern != "/resources/items" || relativeCalls[0].HttpMethod != "POST" {
		t.Fatalf("expected POST /resources/items, got %#v", relativeCalls[0])
	}

	calls := result.HttpCalls["stackedApimBase"]
	if len(calls) != 1 {
		t.Fatalf("expected one path-only call for stacked APIM base, got %#v", calls)
	}
	if calls[0].HttpMethod != "POST" || calls[0].UrlPattern != "/resources" || calls[0].ClientType != "axios" {
		t.Fatalf("expected POST /resources after dropping stacked base variables, got %#v", calls[0])
	}
}

func TestJavaScriptHttpCallsDropRequestPostGraphQLBodyTemplate(t *testing.T) {
	content := []byte(`
import request from 'request-promise'

export async function mapClient(url) {
  return request.post(` + "`" + `${url}
    mutation UpdateClient($id: ID!) {
      updateClient(id: $id) { id }
    }
  ` + "`" + `)
}
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("mapClient.js", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if calls := result.HttpCalls["mapClient"]; len(calls) != 0 {
		t.Fatalf("expected GraphQL body template to be dropped as an HTTP URL, got %#v", calls)
	}
}

func TestJavaScriptHttpCallsDoNotTreatGenericHttpObjectAsAxios(t *testing.T) {
	content := []byte(`
export function readFromLocalWrapper(http) {
  return http.get('/api/widgets')
}

export function readFromAxiosFactory() {
  const http = axios.create({})
  return http.get('/api/widgets')
}

export function readFromApiAxiosFactory() {
  const api = axios.create({})
  return api.post('/api/widgets')
}

export function readFromNamedApiClient(apiClient) {
  return apiClient.get('/api/widgets')
}

export function readFromUpperHTTPClient(HTTPClient) {
  return HTTPClient.post('/api/widgets')
}

export function readFromStoreClient(storeClient) {
  return storeClient.get('/api/widgets')
}
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("http-wrapper.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	calls := result.HttpCalls["readFromLocalWrapper"]
	if len(calls) != 1 {
		t.Fatalf("expected one generic HTTP call, got %#v", calls)
	}
	if calls[0].ClientType != "unknown" || calls[0].HttpMethod != "GET" || calls[0].UrlPattern != "/api/widgets" {
		t.Fatalf("expected unknown GET /api/widgets, got %#v", calls[0])
	}

	factoryCalls := result.HttpCalls["readFromAxiosFactory"]
	if len(factoryCalls) != 1 {
		t.Fatalf("expected one axios factory call, got %#v", factoryCalls)
	}
	if factoryCalls[0].ClientType != "axios" || factoryCalls[0].HttpMethod != "GET" || factoryCalls[0].UrlPattern != "/api/widgets" {
		t.Fatalf("expected axios GET /api/widgets, got %#v", factoryCalls[0])
	}

	apiFactoryCalls := result.HttpCalls["readFromApiAxiosFactory"]
	if len(apiFactoryCalls) != 1 {
		t.Fatalf("expected one api axios factory call, got %#v", apiFactoryCalls)
	}
	if apiFactoryCalls[0].ClientType != "axios" || apiFactoryCalls[0].HttpMethod != "POST" || apiFactoryCalls[0].UrlPattern != "/api/widgets" {
		t.Fatalf("expected axios POST /api/widgets from api factory, got %#v", apiFactoryCalls[0])
	}

	apiCalls := result.HttpCalls["readFromNamedApiClient"]
	if len(apiCalls) != 1 {
		t.Fatalf("expected one API client call, got %#v", apiCalls)
	}
	if apiCalls[0].ClientType != "unknown" || apiCalls[0].HttpMethod != "GET" || apiCalls[0].UrlPattern != "/api/widgets" {
		t.Fatalf("expected unknown GET /api/widgets from apiClient, got %#v", apiCalls[0])
	}

	upperHTTPCalls := result.HttpCalls["readFromUpperHTTPClient"]
	if len(upperHTTPCalls) != 1 {
		t.Fatalf("expected one uppercase HTTP client call, got %#v", upperHTTPCalls)
	}
	if upperHTTPCalls[0].ClientType != "unknown" || upperHTTPCalls[0].HttpMethod != "POST" || upperHTTPCalls[0].UrlPattern != "/api/widgets" {
		t.Fatalf("expected unknown POST /api/widgets from HTTPClient, got %#v", upperHTTPCalls[0])
	}

	if calls := result.HttpCalls["readFromStoreClient"]; len(calls) != 0 {
		t.Fatalf("did not expect storeClient to be treated as HTTP receiver, got %#v", calls)
	}
}

func TestJavaScriptServiceBusProducerExtractionFromLoadedQueue(t *testing.T) {
	content := []byte(`
import { ServiceBus } from './queue-client'
import * as config from '../core/config.json'

const serviceBus = new ServiceBus('account', 'key', 'name')
serviceBus.load(config.RESOURCE_EVENTS_QUEUE)

export async function httpTrigger(context, req) {
  await serviceBus.scheduleMessages([{ body: { id: '1' } }], new Date(), context)
  await serviceBus.sendMessages([{ body: { id: '2' } }], context)
}
`)

	parser := configuredQueueFixtureParser()
	result := parser.ParseFile("PublishResources/index.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	producers := result.SqsProducers["PublishResources"]
	if len(producers) != 2 {
		t.Fatalf("expected two message producers for PublishResources, got %#v", producers)
	}
	for _, producer := range producers {
		if producer.QueueName != "RESOURCE_EVENTS_QUEUE" {
			t.Fatalf("expected queue constant name to be captured, got %#v", producer)
		}
		if producer.LineNumber <= 0 {
			t.Fatalf("expected line number on producer, got %#v", producer)
		}
	}
}

func TestJavaScriptServiceBusProducerExtractionThroughHelper(t *testing.T) {
	content := []byte(`
import { ServiceBus } from './queue-client'
import * as config from '../core/config.json'

const serviceBus = new ServiceBus('account', 'key', 'name')
serviceBus.load(config.RESOURCE_TASKS_QUEUE)

export const storeMessages = async (serviceBus, messages, scheduledDate) => {
  return serviceBus.scheduleMessages(messages, scheduledDate)
}

export async function upload(messages) {
  return storeMessages(serviceBus, messages, new Date())
}
`)

	parser := configuredQueueFixtureParser()
	result := parser.ParseFile("upload.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	producers := result.SqsProducers["upload"]
	if len(producers) != 1 {
		t.Fatalf("expected one helper-routed message producer for upload, got %#v", producers)
	}
	if producers[0].QueueName != "RESOURCE_TASKS_QUEUE" {
		t.Fatalf("expected helper-routed producer to target config queue key, got %#v", producers[0])
	}
}

func TestJavaScriptServiceBusAggregatorExportsAliasCalls(t *testing.T) {
	content := []byte(`
import execute from './execute';
import upload from './upload';

export default {
  executeQueue: execute,
  uploadQueue: upload,
};
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("src/queues/index.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	want := map[string]string{
		"executeQueue": "execute",
		"uploadQueue":  "upload",
	}
	for alias, target := range want {
		if !hasCallee(result.FunctionCalls[alias], target) {
			t.Fatalf("expected exported alias %s to call %s, got %#v", alias, target, result.FunctionCalls)
		}
	}
}

func TestJavaScriptServiceBusProducerExtractionRespectsScopeLocalPublisherNames(t *testing.T) {
	content := []byte(`
import { ServiceBus } from './queue-client'

function publishA(context) {
  const publisher = new ServiceBus('a', 'b', 'c')
  publisher.load('QUEUE_A')
  return publisher.sendMessage({ body: { id: '1' } }, context)
}

function publishB(context) {
  const publisher = new ServiceBus('a', 'b', 'c')
  publisher.load('QUEUE_B')
  return publisher.sendMessage({ body: { id: '2' } }, context)
}
`)

	parser := configuredQueueFixtureParser()
	result := parser.ParseFile("scopedPublishers.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	producersA := result.SqsProducers["publishA"]
	if len(producersA) != 1 || producersA[0].QueueName != "QUEUE_A" {
		t.Fatalf("expected publishA producer to target QUEUE_A, got %#v", producersA)
	}

	producersB := result.SqsProducers["publishB"]
	if len(producersB) != 1 || producersB[0].QueueName != "QUEUE_B" {
		t.Fatalf("expected publishB producer to target QUEUE_B, got %#v", producersB)
	}
}

func TestJavaScriptAzureServiceBusSenderAndReceiverExtraction(t *testing.T) {
	content := []byte(`
import { ServiceBusClient } from '@azure/service-bus'

const client = new ServiceBusClient(connectionString)
const sender = client.createSender('resource-events')
const receiver = client.createReceiver(config.RESOURCE_EVENTS_QUEUE)

export async function publishResource(message) {
  await sender.sendMessages(message)
}

export async function startNotifications() {
  receiver.subscribe({
    processMessage: handleNotification,
    processError: async () => {},
  })
}

export async function handleNotification(message) {
  return message.body
}
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("src/service-bus.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	producers := result.SqsProducers["publishResource"]
	if len(producers) != 1 || producers[0].QueueName != "resource-events" {
		t.Fatalf("expected Azure Service Bus sender producer, got %#v", producers)
	}

	foundConsumer := false
	for _, consumer := range result.SqsConsumers {
		if consumer.QueueName == "RESOURCE_EVENTS_QUEUE" && consumer.HandlerMethod == "handleNotification" {
			foundConsumer = true
			break
		}
	}
	if !foundConsumer {
		t.Fatalf("expected Azure Service Bus receiver consumer, got %#v", result.SqsConsumers)
	}
}

func TestJavaScriptAzureStorageQueueClientExtraction(t *testing.T) {
	content := []byte(`
import { QueueServiceClient } from '@azure/storage-queue'

export async function enqueueAudit(connectionString, body) {
  const service = QueueServiceClient.fromConnectionString(connectionString)
  const queue = service.getQueueClient('audit-work')
  await queue.sendMessage(JSON.stringify(body))
}

export async function peekAudit(queue) {
  return queue.receiveMessages()
}
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("src/auditQueue.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	producers := result.SqsProducers["enqueueAudit"]
	if len(producers) != 1 || producers[0].QueueName != "audit-work" {
		t.Fatalf("expected Azure Storage Queue producer, got %#v", producers)
	}
	if !hasJavaScriptHTTPParsedDataAccess(result.DataAccesses["enqueueAudit"], "queue:audit-work", "write") {
		t.Fatalf("expected queue write data access, got %#v", result.DataAccesses["enqueueAudit"])
	}
	if !hasJavaScriptHTTPParsedDataAccess(result.DataAccesses["peekAudit"], "queue", "read") {
		t.Fatalf("expected queue read data access for injected queue client, got %#v", result.DataAccesses["peekAudit"])
	}
}

func TestJavaScriptServiceBusLoadAloneDoesNotCreateQueueConsumer(t *testing.T) {
	content := []byte(`
import { ServiceBus } from './queue-client'
import * as config from '../core/config.json'

const serviceBus = new ServiceBus('account', 'key', 'name')
serviceBus.load(config.RESOURCE_EVENTS_QUEUE)

export async function receiveResource(id, context) {
  await serviceBus.purgeMessagesByMessageId(id, context)
}
`)

	parser := configuredQueueFixtureParser()
	result := parser.ParseFile("ReceiveResources/index.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if len(result.SqsConsumers) != 0 {
		t.Fatalf("expected no queue consumer for admin-only loaded ServiceBus client, got %#v", result.SqsConsumers)
	}
	if !hasJavaScriptHTTPParsedDataAccess(result.DataAccesses["receiveResource"], "queue:RESOURCE_EVENTS_QUEUE", "delete") {
		t.Fatalf("expected queue delete side effect for loaded ServiceBus client, got %#v", result.DataAccesses["receiveResource"])
	}
}

func TestJavaScriptServiceBusReceiveMethodsCreateQueueConsumer(t *testing.T) {
	content := []byte(`
import { ServiceBus } from './queue-client'
import * as config from '../core/config.json'

const serviceBus = new ServiceBus('account', 'key', 'name')
serviceBus.load(config.RESOURCE_EVENTS_QUEUE)

export async function receiveResource(context) {
  const messages = await serviceBus.receiveMessages({ count: 10 }, context)
  return messages
}
`)

	parser := configuredQueueFixtureParser()
	result := parser.ParseFile("ReceiveResources/index.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if len(result.SqsConsumers) != 1 {
		t.Fatalf("expected one queue consumer for ServiceBus read method, got %#v", result.SqsConsumers)
	}
	if result.SqsConsumers[0].QueueName != "RESOURCE_EVENTS_QUEUE" || result.SqsConsumers[0].HandlerMethod != "receiveResource" {
		t.Fatalf("unexpected queue consumer: %#v", result.SqsConsumers[0])
	}
}

func TestJavaScriptServiceBusQueryMethodsDoNotCreateQueueConsumer(t *testing.T) {
	content := []byte(`
import { ServiceBus } from './queue-client'
import * as config from '../core/config.json'

const serviceBus = new ServiceBus('account', 'key', 'name')
serviceBus.load(config.RESOURCE_EVENTS_QUEUE)

export async function queryResource(context) {
  const messages = await serviceBus.getMessagesByQuery({ count: 10 }, context)
  return messages
}
`)

	parser := configuredQueueFixtureParser()
	result := parser.ParseFile("QueryResources/index.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if len(result.SqsConsumers) != 0 {
		t.Fatalf("expected query/admin read to stay out of sqs_consumers, got %#v", result.SqsConsumers)
	}
	if !hasJavaScriptHTTPParsedDataAccess(result.DataAccesses["queryResource"], "queue:RESOURCE_EVENTS_QUEUE", "read") {
		t.Fatalf("expected queue read data access for query method, got %#v", result.DataAccesses["queryResource"])
	}
}

func TestJavaScriptServiceBusConsumerUsesIndexFunctionAlias(t *testing.T) {
	content := []byte(`
import { ServiceBus } from './queue-client'
import * as config from '../core/config.json'

const serviceBus = new ServiceBus('account', 'key', 'name')
serviceBus.load(config.RESOURCE_EVENTS_QUEUE)

export async function httpTrigger(context) {
  return serviceBus.receiveMessages({ count: 10 }, context)
}
`)

	parser := configuredQueueFixtureParser()
	result := parser.ParseFile("ReceiveResources/index.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if len(result.SqsConsumers) != 1 {
		t.Fatalf("expected one queue consumer for ServiceBus read method, got %#v", result.SqsConsumers)
	}
	if result.SqsConsumers[0].QueueName != "RESOURCE_EVENTS_QUEUE" ||
		result.SqsConsumers[0].HandlerMethod != "ReceiveResources" ||
		result.SqsConsumers[0].ClassName != "ReceiveResources" {
		t.Fatalf("unexpected aliased queue consumer: %#v", result.SqsConsumers[0])
	}
}

func hasJavaScriptHTTPParsedDataAccess(accesses []ParsedDataAccess, entity string, access string) bool {
	for _, item := range accesses {
		if item.EntityName == entity && item.Access == access {
			return true
		}
	}
	return false
}

func TestJavaScriptHttpCallsRecognizeConfiguredFormExports(t *testing.T) {
	content := []byte("function submitForm(url, payload) {\n" +
		"  const form = document.createElement('form')\n" +
		"  form.method = 'POST'\n" +
		"  form.action = url\n" +
		"  form.submit()\n" +
		"}\n\n" +
		"export function pdfExport(resourceType, filename) {\n" +
		"  const apimUrl = 'https://apim.example.com'\n" +
		"  const exportPdfUrl = `${apimUrl}/resources/${resourceType}/export/${filename}`\n" +
		"  submitForm(exportPdfUrl, { fileType: 'pdf' })\n" +
		"}\n")

	parser := configuredHTTPFixtureParser()
	result := parser.ParseFile("src/services/pdf-export/index.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	calls := result.HttpCalls["pdfExport"]
	if len(calls) != 1 {
		t.Fatalf("expected one HTTP call, got %#v", calls)
	}
	if calls[0].HttpMethod != "POST" || calls[0].ClientType != "form" {
		t.Fatalf("expected POST form call, got %#v", calls[0])
	}
	if calls[0].UrlPattern != "/resources/:resourceType/export/:filename" {
		t.Fatalf("expected export URL pattern, got %#v", calls[0])
	}
}

func TestJavaScriptHttpCallsFromWrapperConfigObject(t *testing.T) {
	content := []byte(`
import { callResourceApi } from './resourceClient';

export const createResource = (resource) =>
  callResourceApi({
    method: 'POST',
    endpoint: '/api/resources',
  });

export function notAnHttpCall() {
  return mergeConfigs({ method: 'merge', path: '/tmp/output' });
}
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("resource-client.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	pay := result.HttpCalls["createResource"]
	if len(pay) == 0 {
		t.Fatalf("expected wrapper config-object call to be captured, got %#v", result.HttpCalls)
	}
	if pay[0].HttpMethod != "POST" || pay[0].UrlPattern != "/api/resources" {
		t.Fatalf("unexpected captured wrapper call: %#v", pay[0])
	}

	if calls := result.HttpCalls["notAnHttpCall"]; len(calls) != 0 {
		t.Fatalf("expected non-HTTP config (method 'merge') to be ignored, got %#v", calls)
	}
}

// Configured underscore-prefixed verbs (_post/_getAll) use an API receiver with
// the route built from a class-field path constant. Both the verb and the field
// prefix must resolve so the captured URL matches the backend endpoint route; a
// non-HTTP receiver (_cache) with an underscore verb must not be captured.
func TestJavaScriptHttpCallsWrapperUnderscoreVerbAndFieldPrefix(t *testing.T) {
	content := []byte(`
class ResourceService {
  controllerUrl = '/api/resources/';
  save(resource) {
    return this._api._post(` + "`${this.controllerUrl}Save/`" + `, resource);
  }
  pending(resourceId) {
    return this._api._getAll(` + "`${this.controllerUrl}GetPending/${resourceId}`" + `);
  }
}

class CacheService {
  lookup(key) {
    return this._cache._get(key);
  }
}
`)
	result := configuredHTTPFixtureParser().ParseFile("resource.service.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	add := result.HttpCalls["ResourceService.save"]
	if len(add) == 0 || add[0].HttpMethod != "POST" || add[0].UrlPattern != "/api/resources/Save/" {
		t.Fatalf("expected POST /api/resources/Save/, got %#v", add)
	}
	inc := result.HttpCalls["ResourceService.pending"]
	if len(inc) == 0 || inc[0].HttpMethod != "GET" || inc[0].UrlPattern != "/api/resources/GetPending/:resourceId" {
		t.Fatalf("expected GET /api/resources/GetPending/:resourceId, got %#v", inc)
	}
	if calls := result.HttpCalls["CacheService.lookup"]; len(calls) != 0 {
		t.Fatalf("expected this._cache._get not to be captured as HTTP, got %#v", calls)
	}
}

// Custom method names require explicit configuration; only standard verbs are inferred.
func TestHTTPVerbFromMethodName(t *testing.T) {
	for in, want := range map[string]string{
		"post": "POST", "delete": "DELETE", "get": "GET", "request": "ANY",
	} {
		if got, ok := httpVerbFromMethodName(in); !ok || got != want {
			t.Fatalf("httpVerbFromMethodName(%q) = (%q,%v), want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"lookup", "save", "stringify", "merge", "_post", "_postAll", "_postResponse", "_getAll", "_deleteAll"} {
		if got, ok := httpVerbFromMethodName(in); ok {
			t.Fatalf("httpVerbFromMethodName(%q) unexpectedly classified as %q", in, got)
		}
	}
}
