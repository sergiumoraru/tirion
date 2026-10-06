package main

import (
	"testing"

	"github.com/sergiumoraru/tirion/internal/parser"
)

func TestResolveCalleeJavaScriptCallbackIsNotAClass(t *testing.T) {
	metas := map[string][]functionMeta{"send": {{id: 1}}, "timer.send": {{id: 2}}}
	for _, language := range []string{"javascript", "typescript"} {
		for _, owner := range []string{"timer.setTimeout_L3", "promise.then_L7", "dom.click_L8"} {
			name, id := resolveCallee(metas, owner, parser.ParsedFunctionCall{CalleeName: "send", MethodName: "send"}, nil, language)
			if name != "send" || id == nil || *id != 1 {
				t.Fatalf("%s %s: got %s %v, want lexical send", language, owner, name, id)
			}
		}
	}
}

func TestResolveCallee_ReceiverTypeUnique(t *testing.T) {
	functionMetas := map[string][]functionMeta{
		"ResourceService.submit": {
			{id: 101, startLine: 10, endLine: 40},
		},
	}
	receiverTypes := map[string]string{
		"resourceService": "ResourceService",
	}
	call := parser.ParsedFunctionCall{
		CalleeName: "resourceService.submit",
		Receiver:   "resourceService",
		MethodName: "submit",
		LineNumber: 88,
	}

	calleeName, calleeID := resolveCallee(functionMetas, "ResourceController.save", call, receiverTypes)
	if calleeName != "ResourceService.submit" {
		t.Fatalf("expected callee name %q, got %q", "ResourceService.submit", calleeName)
	}
	if calleeID == nil || *calleeID != 101 {
		t.Fatalf("expected resolved callee id 101, got %#v", calleeID)
	}
}

func TestResolveCallee_ThisReceiverUnique(t *testing.T) {
	functionMetas := map[string][]functionMeta{
		"ResourceController.validate": {
			{id: 202, startLine: 120, endLine: 180},
		},
	}
	call := parser.ParsedFunctionCall{
		Receiver:   "this",
		MethodName: "validate",
		LineNumber: 42,
	}

	calleeName, calleeID := resolveCallee(functionMetas, "ResourceController.save", call, nil)
	if calleeName != "ResourceController.validate" {
		t.Fatalf("expected callee name %q, got %q", "ResourceController.validate", calleeName)
	}
	if calleeID == nil || *calleeID != 202 {
		t.Fatalf("expected resolved callee id 202, got %#v", calleeID)
	}
}

func TestResolveCallee_OverloadAmbiguousNotResolved(t *testing.T) {
	functionMetas := map[string][]functionMeta{
		"ResourceService.save": {
			{id: 301, startLine: 10, endLine: 20},
			{id: 302, startLine: 30, endLine: 40},
		},
	}
	receiverTypes := map[string]string{
		"resourceService": "ResourceService",
	}
	call := parser.ParsedFunctionCall{
		CalleeName: "resourceService.save",
		Receiver:   "resourceService",
		MethodName: "save",
		LineNumber: 77,
	}

	calleeName, calleeID := resolveCallee(functionMetas, "ResourceController.save", call, receiverTypes)
	if calleeName != "ResourceService.save" {
		t.Fatalf("expected candidate callee name %q, got %q", "ResourceService.save", calleeName)
	}
	if calleeID != nil {
		t.Fatalf("expected unresolved overload callee id, got %#v", calleeID)
	}
}

func TestResolveCallee_CalleeNameFallbackUnique(t *testing.T) {
	functionMetas := map[string][]functionMeta{
		"ResourceFactory.create": {
			{id: 401, startLine: 90, endLine: 130},
		},
	}
	call := parser.ParsedFunctionCall{
		CalleeName: "ResourceFactory.create",
		LineNumber: 15,
	}

	calleeName, calleeID := resolveCallee(functionMetas, "ResourceController.create", call, nil)
	if calleeName != "ResourceFactory.create" {
		t.Fatalf("expected callee name %q, got %q", "ResourceFactory.create", calleeName)
	}
	if calleeID == nil || *calleeID != 401 {
		t.Fatalf("expected resolved callee id 401, got %#v", calleeID)
	}
}

func TestClassifyUnresolvedCallReasons(t *testing.T) {
	tests := []struct {
		name       string
		call       parser.ParsedFunctionCall
		calleeName string
		want       string
	}{
		{
			name: "javascript stdlib receiver",
			call: parser.ParsedFunctionCall{
				CalleeName: "JSON.stringify",
				Receiver:   "JSON",
				MethodName: "stringify",
			},
			calleeName: "JSON.stringify",
			want:       "stdlib_js",
		},
		{
			name: "csharp stdlib qualified call",
			call: parser.ParsedFunctionCall{
				CalleeName: "System.Linq.Enumerable.Concat",
				Receiver:   "System.Linq.Enumerable",
				MethodName: "Concat",
			},
			calleeName: "System.Linq.Enumerable.Concat",
			want:       "stdlib_csharp",
		},
		{
			name: "framework call",
			call: parser.ParsedFunctionCall{
				CalleeName: "this.$router.push",
				Receiver:   "this.$router",
				MethodName: "push",
			},
			calleeName: "this.$router.push",
			want:       "framework",
		},
		{
			name: "dynamic receiver",
			call: parser.ParsedFunctionCall{
				CalleeName: "getClient().send",
				Receiver:   "getClient()",
				MethodName: "send",
			},
			calleeName: "getClient().send",
			want:       "dynamic_receiver",
		},
		{
			name: "unknown application call",
			call: parser.ParsedFunctionCall{
				CalleeName: "publisher.publish",
				Receiver:   "publisher",
				MethodName: "publish",
			},
			calleeName: "publisher.publish",
			want:       "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyUnresolvedCall(tt.call, tt.calleeName); got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestBuildReceiverTypeIndex_CSharpClassFields(t *testing.T) {
	result := parser.NewCSharpParser().ParseFile("ResourceHandler.cs", []byte(`
public class ResourceHandler
{
    private readonly IResourceRepository _repository;
    private readonly ResourceService _service;

    private void Process(int id)
    {
        var resource = _service.Load(id);
        _repository.Update(resource);
    }
}
`))

	receiverTypes := buildReceiverTypeIndex(result)["ResourceHandler.Process"]
	if receiverTypes["_service"] != "ResourceService" {
		t.Fatalf("expected _service receiver type, got %#v", receiverTypes)
	}
	if receiverTypes["_repository"] != "IResourceRepository" {
		t.Fatalf("expected _repository receiver type, got %#v", receiverTypes)
	}

	calleeName, calleeID := resolveCallee(map[string][]functionMeta{}, "ResourceHandler.Process", parser.ParsedFunctionCall{
		CalleeName: "_service.Load",
		Receiver:   "_service",
		MethodName: "Load",
		LineNumber: 9,
	}, receiverTypes)
	if calleeName != "ResourceService.Load" {
		t.Fatalf("expected typed candidate name, got %q (calleeID=%#v)", calleeName, calleeID)
	}

	calleeName, calleeID = resolveCallee(map[string][]functionMeta{}, "ResourceHandler.Process", parser.ParsedFunctionCall{
		CalleeName: "_repository.Update",
		Receiver:   "_repository",
		MethodName: "Update",
		LineNumber: 10,
	}, receiverTypes)
	if calleeName != "IResourceRepository.Update" {
		t.Fatalf("expected interface candidate name, got %q (calleeID=%#v)", calleeName, calleeID)
	}
}
