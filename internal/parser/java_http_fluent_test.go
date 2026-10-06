package parser

import "testing"

func TestJavaFluentHttpCalls_WebClientAndWebTarget(t *testing.T) {
	parser := NewJavaParser()
	source := []byte(`
import org.springframework.http.HttpMethod;
import org.springframework.web.reactive.function.client.WebClient;
import jakarta.ws.rs.client.Entity;
import jakarta.ws.rs.client.WebTarget;

class Foo {
    void run(WebClient webClient, WebTarget webTarget) {
        webClient.get().uri("/api/users/{id}").retrieve().bodyToMono(String.class);
        webClient.method(HttpMethod.POST).uri("/api/users").retrieve().bodyToMono(String.class);

        webTarget.path("/projects/{projectId}").request().get();
        webTarget.path("/projects").request().method("PATCH", Entity.json("{}"));
    }
}
`)

	result := parser.ParseFile("Foo.java", source)
	calls := result.HttpCalls["Foo.run"]
	if len(calls) == 0 {
		t.Fatalf("expected fluent HTTP calls in Foo.run, got none")
	}

	type expectedCall struct {
		method string
		client string
	}
	expected := map[string]expectedCall{
		"/api/users/:id":       {method: "GET", client: "WebClient"},
		"/api/users":           {method: "POST", client: "WebClient"},
		"/projects/:projectId": {method: "GET", client: "WebTarget"},
		"/projects":            {method: "PATCH", client: "WebTarget"},
	}

	seen := make(map[string]map[string]ParsedHttpCall)
	for _, call := range calls {
		if call.UrlPattern == "" || call.HttpMethod == "" {
			continue
		}
		if _, ok := seen[call.UrlPattern]; !ok {
			seen[call.UrlPattern] = make(map[string]ParsedHttpCall)
		}
		seen[call.UrlPattern][call.HttpMethod] = call
	}

	for url, want := range expected {
		methodCalls, ok := seen[url]
		if !ok {
			t.Fatalf("expected fluent HTTP URL %s, got calls: %#v", url, calls)
		}
		call, ok := methodCalls[want.method]
		if !ok {
			t.Fatalf("expected %s %s, got calls at URL: %#v", want.method, url, methodCalls)
		}
		if call.ClientType != want.client {
			t.Fatalf("expected %s %s to use client %s, got %s", want.method, url, want.client, call.ClientType)
		}
		if call.LineNumber <= 0 {
			t.Fatalf("expected %s %s to include line number, got %d", want.method, url, call.LineNumber)
		}
	}
}
