package parser

import (
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// javaInterfaceHTTPRole classifies what an annotated Java interface does with
// HTTP. The classification comes from annotation and import evidence, never from
// the interface's name.
type javaInterfaceHTTPRole struct {
	// outboundClient: the interface declares calls this service makes
	// (@FeignClient, @HttpExchange, @RegisterRestClient, Micronaut @Client,
	// Retrofit). Its annotated methods are HTTP interface methods, never inbound
	// endpoints.
	outboundClient bool
	// dataMapper: a MyBatis mapper. @Select/@Delete there carry SQL, not URLs.
	dataMapper bool
	// basePath is the interface-level path every client method is relative to:
	// @HttpExchange("/api"), JAX-RS/MicroProfile @Path, @RequestMapping on a
	// Feign interface, @FeignClient(path=...), Micronaut @Client(path=...).
	basePath string
}

// javaClientBasePath reads the interface-level path of one annotation, or "".
// Only paths are kept: a service id or ${placeholder} names no route, and an
// absolute URL contributes its path component.
func javaClientBasePath(ann ParsedAnnotation) string {
	var raw string
	switch ann.Name {
	case "HttpExchange":
		raw = firstNonEmptyString(firstString(annotationPathValues(ann)), annotationStringValue(ann, "url"))
	case "Path", "RequestMapping":
		raw = firstString(annotationPathValues(ann))
	case "FeignClient", "ReactiveFeignClient", "Client":
		raw = annotationStringValue(ann, "path")
		if raw == "" && ann.Name == "Client" {
			if value := annotationStringValue(ann, "value"); strings.HasPrefix(value, "/") {
				raw = value
			}
		}
	}
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "://"); i >= 0 {
		rest := raw[i+3:]
		if slash := strings.Index(rest, "/"); slash >= 0 {
			raw = rest[slash:]
		} else {
			raw = ""
		}
	}
	if raw == "" || strings.Contains(raw, "${") {
		return ""
	}
	return raw
}

func firstString(values []string) string {
	if len(values) > 0 {
		return values[0]
	}
	return ""
}

// javaClientInterfaceAnnotations mark an interface as an outbound client.
var javaClientInterfaceAnnotations = map[string]bool{
	"FeignClient":         true,
	"ReactiveFeignClient": true,
	"HttpExchange":        true,
	"RegisterRestClient":  true,
}

// javaExchangeAnnotations are Spring 6 declarative HTTP interface methods.
var javaExchangeAnnotations = map[string]string{
	"GetExchange":    "GET",
	"PostExchange":   "POST",
	"PutExchange":    "PUT",
	"DeleteExchange": "DELETE",
	"PatchExchange":  "PATCH",
}

func javaHasImportPrefix(imports []ParsedImport, prefix string) bool {
	for _, imp := range imports {
		if strings.HasPrefix(imp.Path, prefix) {
			return true
		}
	}
	return false
}

func classifyJavaInterfaceHTTP(annotations []ParsedAnnotation, methods []ParsedFunction, imports []ParsedImport) javaInterfaceHTTPRole {
	var role javaInterfaceHTTPRole
	for _, ann := range annotations {
		switch {
		case javaClientInterfaceAnnotations[ann.Name]:
			role.outboundClient = true
		case ann.Name == "Client" && javaHasImportPrefix(imports, "io.micronaut.http.client"):
			role.outboundClient = true
		case ann.Name == "Mapper":
			role.dataMapper = true
		}
		if base := javaClientBasePath(ann); base != "" && role.basePath == "" {
			role.basePath = base
		}
	}
	for _, method := range methods {
		for _, ann := range method.Annotations {
			if _, ok := javaExchangeAnnotations[ann.Name]; ok || ann.Name == "HttpExchange" {
				role.outboundClient = true
			}
			// Retrofit's @GET("users/{id}") carries its URL; the JAX-RS @GET of a
			// server resource is a bare marker, with the route in @Path.
			switch ann.Name {
			case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS":
				if len(annotationPathValues(ann)) > 0 {
					role.outboundClient = true
				}
			}
		}
	}
	if javaHasImportPrefix(imports, "retrofit2.http.") {
		role.outboundClient = true
	}
	if javaHasImportPrefix(imports, "org.apache.ibatis.") {
		role.dataMapper = true
	}
	return role
}

// enclosingInterfaceRole returns the HTTP role of the interface a method
// declaration sits in, or ok=false when the method belongs to a class, enum,
// record or anonymous type.
func (p *JavaParser) enclosingInterfaceRole(node *sitter.Node, content []byte, result *ParsedFile) (javaInterfaceHTTPRole, bool) {
	for current := node.Parent(); current != nil; current = current.Parent() {
		switch current.Type() {
		case "interface_declaration":
			if cached, ok := p.interfaceRoles[current.StartByte()]; ok {
				return cached, true
			}
			var annotations []ParsedAnnotation
			for i := 0; i < int(current.ChildCount()); i++ {
				if child := current.Child(i); child.Type() == "modifiers" {
					_, annotations = p.extractModifiersAndAnnotations(child, content)
				}
			}
			var methods []ParsedFunction
			if body := current.ChildByFieldName("body"); body != nil {
				methods, _ = p.extractInterfaceBody(body, content, "")
			}
			role := classifyJavaInterfaceHTTP(annotations, methods, result.Imports)
			if p.interfaceRoles != nil {
				p.interfaceRoles[current.StartByte()] = role
			}
			return role, true
		case "class_declaration", "enum_declaration", "record_declaration", "class_body", "object_creation_expression":
			return javaInterfaceHTTPRole{}, false
		}
	}
	return javaInterfaceHTTPRole{}, false
}

// extractHttpInterfaceMethods extracts the HTTP calls an outbound client
// interface declares (Retrofit, Feign, Spring HTTP interfaces, MicroProfile
// REST clients). Interfaces without client evidence declare inbound routes
// (generated API interfaces, JAX-RS resources), which are endpoints, not calls.
func (p *JavaParser) extractHttpInterfaceMethods(interfaceName string, methods []ParsedFunction, role javaInterfaceHTTPRole) []ParsedHttpInterfaceMethod {
	var httpMethods []ParsedHttpInterfaceMethod
	if role.dataMapper {
		return httpMethods
	}

	for _, method := range methods {
		methodPath := ""
		for _, ann := range method.Annotations {
			if ann.Name == "Path" {
				if values := annotationPathValues(ann); len(values) > 0 {
					methodPath = values[0]
				}
			}
		}
		for _, ann := range method.Annotations {
			httpMethod, urlPattern, ok := javaClientAnnotation(ann, role, methodPath)
			if !ok {
				continue
			}

			// A MyBatis annotation without mapper evidence still reads like
			// @Delete("DELETE FROM table ..."); real REST annotations carry URL paths.
			upperPattern := strings.ToUpper(urlPattern)
			if strings.Contains(upperPattern, "SELECT ") ||
				strings.Contains(upperPattern, "INSERT ") ||
				strings.Contains(upperPattern, "UPDATE ") ||
				strings.Contains(upperPattern, "DELETE FROM") ||
				strings.Contains(upperPattern, " FROM ") ||
				strings.Contains(upperPattern, " WHERE ") ||
				strings.Contains(upperPattern, " SET ") ||
				strings.Contains(upperPattern, "TRUNCATE ") {
				continue
			}

			// Client methods are relative to the interface-level base path.
			if role.basePath != "" && !strings.Contains(urlPattern, "://") {
				urlPattern = joinPaths(role.basePath, urlPattern)
			}

			// Normalize path variables: {id} -> :id
			urlPattern = strings.ReplaceAll(urlPattern, "{", ":")
			urlPattern = strings.ReplaceAll(urlPattern, "}", "")

			httpMethods = append(httpMethods, ParsedHttpInterfaceMethod{
				InterfaceName: interfaceName,
				MethodName:    strings.TrimPrefix(method.Name, interfaceName+"."),
				HttpMethod:    httpMethod,
				UrlPattern:    urlPattern,
				LineNumber:    method.StartLine,
			})
		}
	}

	return httpMethods
}

// javaClientAnnotation interprets one method annotation of an interface. ok is
// false when the annotation does not declare an outbound HTTP call.
func javaClientAnnotation(ann ParsedAnnotation, role javaInterfaceHTTPRole, methodPath string) (httpMethod, urlPattern string, ok bool) {
	pathOf := func() string {
		if values := annotationPathValues(ann); len(values) > 0 {
			return values[0]
		}
		if val, found := ann.Values["value"]; found {
			return fmt.Sprintf("%v", val)
		}
		return ""
	}

	if verb, found := javaExchangeAnnotations[ann.Name]; found {
		// Spring HTTP interface methods are outbound by themselves.
		return verb, firstNonEmptyString(pathOf(), annotationStringValue(ann, "url")), true
	}

	switch ann.Name {
	case "HttpExchange":
		verb := "REQUEST"
		if methods := annotationMethodValues(ann); len(methods) > 0 {
			verb = methods[0]
		}
		return verb, firstNonEmptyString(pathOf(), annotationStringValue(ann, "url")), true

	case "GetMapping", "PostMapping", "PutMapping", "DeleteMapping", "PatchMapping":
		// Spring MVC mappings on an interface are client calls only when the
		// interface is a declared client (@FeignClient, ...); otherwise they are
		// the routes of a generated API interface.
		if !role.outboundClient {
			return "", "", false
		}
		return strings.ToUpper(strings.TrimSuffix(ann.Name, "Mapping")), pathOf(), true

	case "RequestMapping":
		if !role.outboundClient {
			return "", "", false
		}
		verb := "REQUEST"
		if methods := annotationMethodValues(ann); len(methods) > 0 {
			verb = methods[0]
		}
		return verb, pathOf(), true

	case "GET", "POST", "PUT", "DELETE", "PATCH", "Get", "Post", "Put", "Delete", "Patch":
		verb := strings.ToUpper(ann.Name)
		url := pathOf()
		// Retrofit's @GET("users/{id}") carries the URL. A bare JAX-RS @GET marker
		// is a client call only on a declared REST client, where @Path has the URL.
		if url == "" && role.outboundClient {
			url = methodPath
		} else if url == "" {
			return "", "", false
		}
		return verb, url, true
	}
	return "", "", false
}

func annotationStringValue(ann ParsedAnnotation, key string) string {
	if ann.Values == nil {
		return ""
	}
	if val, ok := ann.Values[key]; ok {
		if s, ok := val.(string); ok {
			return s
		}
	}
	return ""
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
