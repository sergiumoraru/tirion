package parser

import "testing"

func TestJavaStripesEndpoints(t *testing.T) {
	content := []byte(`
package com.example;

import net.sourceforge.stripes.action.DefaultHandler;
import net.sourceforge.stripes.action.HandlesEvent;
import net.sourceforge.stripes.action.Resolution;
import net.sourceforge.stripes.action.UrlBinding;

@UrlBinding("/resources/{$event}/{groupId}/{resourceId}")
public class ResourceActionBean {
    @DefaultHandler
    @HandlesEvent("webhook")
    public Resolution webhook() { return null; }
}
`)

	parser := NewJavaParser()
	result := parser.ParseFile("ResourceActionBean.java", content)

	expectedPath := "/resources/webhook/:groupId/:resourceId"
	expectedHandler := "ResourceActionBean.webhook"

	for _, endpoint := range result.Endpoints {
		if endpoint.Path == expectedPath && endpoint.Method == "REQUEST" && endpoint.HandlerName == expectedHandler {
			return
		}
	}

	t.Fatalf("expected Stripes endpoint %s -> %s (REQUEST), got %#v", expectedPath, expectedHandler, result.Endpoints)
}
