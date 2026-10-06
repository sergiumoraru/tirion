package parser

import "testing"

func TestJavaParserExtractsEndpointsForOverloadedAnnotatedMethods(t *testing.T) {
	t.Parallel()

	source := []byte(`
package example;

import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequestMapping("/resources")
public class ResourceController {
    @PostMapping("/batch")
    public Object lookup(@RequestBody java.util.List<String> ids) {
        return null;
    }

    @PostMapping("/{id}/lookup")
    public Object lookup(@RequestParam boolean includeDetails, @RequestBody Object request) {
        return null;
    }
}
`)

	result := NewJavaParser().ParseFile("ResourceController.java", source)

	if !hasEndpoint(result.Endpoints, "POST", "/resources/batch") {
		t.Fatalf("expected batch endpoint, got %#v", result.Endpoints)
	}
	if !hasEndpoint(result.Endpoints, "POST", "/resources/{id}/lookup") {
		t.Fatalf("expected lookup endpoint, got %#v", result.Endpoints)
	}
}
