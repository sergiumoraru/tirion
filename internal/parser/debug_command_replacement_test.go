package parser

import "testing"

func TestJavaParserDebugCommandCoverage(t *testing.T) {
	t.Parallel()

	source := []byte(`
package example;

import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequestMapping("/orders")
public class OrderController {
    private final OrderService service;

    public OrderController(OrderService service) {
        this.service = service;
    }

    @GetMapping("/{id}")
    public Order getById(String id) {
        return service.getById(id);
    }
}
`)

	result := NewJavaParser().ParseFile("OrderController.java", source)

	if result.Language != "java" {
		t.Fatalf("expected java language, got %q", result.Language)
	}
	if len(result.Classes) != 1 || result.Classes[0].Name != "OrderController" {
		t.Fatalf("expected OrderController class, got %#v", result.Classes)
	}
	if !hasEndpoint(result.Endpoints, "GET", "/orders/{id}") {
		t.Fatalf("expected GET /orders/{id} endpoint, got %#v", result.Endpoints)
	}
}

func TestTypeScriptParserDebugCommandCoverage(t *testing.T) {
	t.Parallel()

	source := []byte(`
export interface Resource {
  id: number
}

export type ResourceState = 'active' | 'inactive'

export class ResourceService {
  load(resource: Resource): ResourceState {
    return this.normalize(resource)
  }

  private normalize(resource: Resource): ResourceState {
    return resource.id > 0 ? 'active' : 'inactive'
  }
}

export function createService() {
  const service = new ResourceService()
  return service.load({ id: 1 })
}
`)

	result := NewJavaScriptParser().ParseFile("resource.service.ts", source)

	if len(result.Interfaces) != 1 || result.Interfaces[0].Name != "Resource" {
		t.Fatalf("expected Resource interface, got %#v", result.Interfaces)
	}
	if len(result.TypeAliases) != 1 || result.TypeAliases[0].Name != "ResourceState" {
		t.Fatalf("expected ResourceState type alias, got %#v", result.TypeAliases)
	}
	if len(result.Classes) != 1 || result.Classes[0].Name != "ResourceService" {
		t.Fatalf("expected ResourceService class, got %#v", result.Classes)
	}
	if calls := result.FunctionCalls["ResourceService.load"]; !hasCall(calls, "ResourceService.normalize") {
		t.Fatalf("expected ResourceService.load to call normalize, got %#v", calls)
	}
	if calls := result.FunctionCalls["createService"]; !hasCall(calls, "ResourceService.load") {
		t.Fatalf("expected createService to call ResourceService.load, got %#v", calls)
	}
}
