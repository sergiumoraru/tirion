package parser

import "testing"

func TestJavaScriptAzureBlobSideEffects(t *testing.T) {
	content := []byte(`
const { BlobServiceClient } = require('@azure/storage-blob')

export async function writeResource(connectionString, buffer) {
  const blobServiceClient = BlobServiceClient.fromConnectionString(connectionString)
  const containerClient = blobServiceClient.getContainerClient('resources')
  const blockBlobClient = containerClient.getBlockBlobClient('resource.bin')
  await blockBlobClient.uploadData(buffer)
  for await (const blob of containerClient.listBlobsFlat()) {}
}

export async function deleteResource(containerClient, blobs) {
  const blobClient = containerClient.getBlockBlobClient('previous.bin')
  await blobClient.delete()
}
`)

	result := NewJavaScriptParser().ParseFile("blob.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if !hasParsedDataAccess(result.DataAccesses["writeResource"], "blob:resources", "write") {
		t.Fatalf("expected writeResource to write blob:resources, got %#v", result.DataAccesses["writeResource"])
	}
	if !hasParsedDataAccess(result.DataAccesses["writeResource"], "blob:resources", "read") {
		t.Fatalf("expected writeResource to read blob:resources, got %#v", result.DataAccesses["writeResource"])
	}
	if !hasParsedDataAccess(result.DataAccesses["deleteResource"], "blob", "delete") {
		t.Fatalf("expected deleteResource to delete a blob, got %#v", result.DataAccesses["deleteResource"])
	}
}

func TestJavaScriptDocumentGenerationSideEffects(t *testing.T) {
	content := []byte(`
const puppeteer = require('puppeteer')
const { Packer } = require('docx')

export async function renderPdf(document) {
  const browser = await puppeteer.launch()
  const page = await browser.newPage()
  await page.pdf({ format: 'A4' })
  return Packer.toBuffer(document)
}
`)

	result := NewJavaScriptParser().ParseFile("documents.js", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	accesses := result.DataAccesses["renderPdf"]
	if !hasParsedDataAccess(accesses, "document:browser_render", "generate") {
		t.Fatalf("expected browser render generation, got %#v", accesses)
	}
	if !hasParsedDataAccess(accesses, "document:pdf", "generate") {
		t.Fatalf("expected PDF generation, got %#v", accesses)
	}
	if !hasParsedDataAccess(accesses, "document:docx", "generate") {
		t.Fatalf("expected DOCX generation, got %#v", accesses)
	}
}

func TestJavaScriptContentstackFluentChainSideEffects(t *testing.T) {
	content := []byte(`
const contentstack = require('contentstack-management')

export async function publishResource(stackApiKey, entryUid) {
  const stack = contentstack.Stack({ api_key: stackApiKey })
  const entry = await stack.ContentType('resources').Entry(entryUid).fetch()
  await entry.publish({ locale: 'en-us' })
}

export async function deleteAsset(stack) {
  await stack.Asset('asset-1').delete()
}
`)

	result := NewJavaScriptParser().ParseFile("src/cms.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if !hasParsedDataAccess(result.DataAccesses["publishResource"], "cms:contentstack:entry:entryUid", "read") {
		t.Fatalf("expected publishResource to read Contentstack entry, got %#v", result.DataAccesses)
	}
	if !hasParsedDataAccess(result.DataAccesses["publishResource"], "cms:contentstack:entry:entryUid", "write") {
		t.Fatalf("expected publishResource to publish Contentstack entry, got %#v", result.DataAccesses)
	}
	if !hasParsedDataAccess(result.DataAccesses["deleteAsset"], "cms:contentstack:asset:asset-1", "delete") {
		t.Fatalf("expected deleteAsset to delete Contentstack asset, got %#v", result.DataAccesses)
	}
}

func TestJavaScriptServiceBusAdminSideEffectsFromLoadedQueue(t *testing.T) {
	content := []byte(`
import { ServiceBus } from './queue-client'
import * as config from '../core/config.json'

const serviceBus = new ServiceBus('account', 'key', 'name')
serviceBus.load(config.RESOURCE_EVENTS_QUEUE)

export async function removeResourceMessage(id, context) {
  await serviceBus.purgeMessagesByMessageId(id, context)
}
`)

	result := configuredQueueFixtureParser().ParseFile("RemoveResourceMessage/index.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	accesses := result.DataAccesses["removeResourceMessage"]
	if !hasParsedDataAccess(accesses, "queue:RESOURCE_EVENTS_QUEUE", "delete") {
		t.Fatalf("expected queue delete side effect for loaded ServiceBus client, got %#v", accesses)
	}
}

func hasParsedDataAccess(accesses []ParsedDataAccess, entity string, access string) bool {
	for _, item := range accesses {
		if item.EntityName == entity && item.Access == access {
			return true
		}
	}
	return false
}
