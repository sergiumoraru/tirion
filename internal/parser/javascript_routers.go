package parser

import (
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// Route registration detection.
//
// A call such as `x.get('/path', handler)` is either a server route
// registration or an outbound HTTP client call, and the receiver decides. The
// receiver is classified from in-file evidence first (imports and require()
// calls of known libraries, factory calls, type annotations, `new` expressions,
// reassignments) and the classification is scoped, so a parameter or local
// client instance shadows an outer Express app. Only when a receiver cannot be
// resolved in the file (untyped parameters, relative imports, globals, results
// of unknown factories) does extractEndpoint fall back to evidence taken from
// the call itself, see routeCallLooksRegistered.
//
// All scope and binding information is gathered in one top-down pass
// (collectRouterBindings), so classifying a receiver costs O(scope depth) and
// never depends on how many functions the file declares.

const (
	rkRouter     = "router"      // an application or router instance with get/post/... route methods
	rkRoute      = "route"       // the object returned by router.route(path)
	rkFactory    = "factory"     // callable or constructible, produces rkRouter
	rkRouterType = "router_type" // a type annotation naming a router instance
	rkClient     = "client"      // an HTTP client or a module that exports one
	rkOther      = "other"       // provably not a router (literal, plain class instance, awaited value)
	rkClass      = "class"       // locally declared class that does not extend a router factory
	rkModulePref = "module:"     // the module namespace of a known library, followed by its name
)

type routerModuleSpec struct {
	callable bool
	members  map[string]string
}

var routerModules = map[string]routerModuleSpec{
	"express":                {callable: true, members: map[string]string{"Router": rkFactory, "Express": rkRouterType, "Application": rkRouterType, "IRouter": rkRouterType}},
	"express-promise-router": {callable: true},
	"koa-router":             {callable: true, members: map[string]string{"Router": rkFactory}},
	"@koa/router":            {callable: true, members: map[string]string{"Router": rkFactory}},
	"fastify":                {callable: true, members: map[string]string{"fastify": rkFactory, "FastifyInstance": rkRouterType}},
	"polka":                  {callable: true, members: map[string]string{"polka": rkFactory}},
	"restify":                {members: map[string]string{"createServer": rkFactory, "Server": rkRouterType}},
	"hono":                   {members: map[string]string{"Hono": rkFactory}},
	"hono/quick":             {members: map[string]string{"Hono": rkFactory}},
	"hono/tiny":              {members: map[string]string{"Hono": rkFactory}},
	"@hono/zod-openapi":      {members: map[string]string{"OpenAPIHono": rkFactory}},
	"elysia":                 {members: map[string]string{"Elysia": rkFactory}},
	"itty-router":            {members: map[string]string{"Router": rkFactory, "AutoRouter": rkFactory}},
	"h3":                     {members: map[string]string{"createRouter": rkFactory}},
}

// Modules whose exports are HTTP clients (or are used as one), so anything
// derived from them is never a route registration.
var routerClientModules = map[string]bool{
	"axios": true, "node-fetch": true, "got": true, "ky": true, "ky-universal": true,
	"superagent": true, "supertest": true, "undici": true, "ofetch": true,
	"isomorphic-fetch": true, "cross-fetch": true, "whatwg-fetch": true,
	"request": true, "request-promise": true, "needle": true, "wretch": true,
	"redaxios": true, "openapi-fetch": true, "http": true, "https": true,
	"node:http": true, "node:https": true, "@angular/common/http": true,
}

// Globals that denote an HTTP client when no declaration shadows them.
var routerClientGlobals = map[string]bool{
	"axios": true, "fetch": true, "$http": true, "got": true, "ky": true,
	"superagent": true, "ofetch": true, "$fetch": true, "http": true, "https": true,
	"$": true, "jQuery": true,
}

// Constructors that build a router even when imported from somewhere the file
// does not show.
var routerClassNames = map[string]bool{"Hono": true, "OpenAPIHono": true, "Elysia": true}

// Factories called as plain functions that build a router when nothing in the
// file declares the name.
var routerFactoryGlobals = map[string]bool{"express": true, "Router": true, "fastify": true, "Fastify": true, "polka": true}

// Method names whose calls need a recorded scope frame: route registration
// verbs plus the HTTP client verbs recognized by extractHttpCall.
var routerCallMethods = map[string]bool{
	"get": true, "post": true, "put": true, "delete": true, "del": true, "patch": true,
	"options": true, "head": true, "all": true, "route": true, "request": true,
}

var routerRegistrationMethods = map[string]string{
	"get": "GET", "post": "POST", "put": "PUT", "delete": "DELETE",
	"patch": "PATCH", "options": "OPTIONS", "head": "HEAD",
}

// Methods that return the application itself, so a chain keeps its role.
var routerChainMethods = map[string]bool{
	"use": true, "set": true, "enable": true, "disable": true, "engine": true, "param": true,
	"basePath": true, "onError": true, "notFound": true, "mount": true, "register": true,
	"decorate": true, "addHook": true, "setErrorHandler": true, "setNotFoundHandler": true,
}

type routerFrame struct {
	parent   *routerFrame
	function bool // function or program: `var` and hoisting stop here
	class    bool
	names    map[string][]*routerBinding

	// Function frames only: the expressions the function returns, and the
	// memoized role of its result. Async and generator functions never return
	// the router itself.
	returns  []routerReturn
	opaque   bool
	retState uint8
	retKind  string
}

type routerReturn struct {
	expr  *sitter.Node
	frame *routerFrame
	pos   uint32
}

type routerBinding struct {
	frame *routerFrame // scope the value is evaluated in
	pos   uint32       // byte offset of the declaration or assignment
	value *sitter.Node
	typ   *sitter.Node
	key   string // destructured property name, empty otherwise
	kind  string // preset role (imports); empty means derive from typ and value
	decl  string // "var", "function", "class", "import", "param"

	state  uint8
	result string
}

type routerIndex struct {
	src    []byte
	root   *routerFrame
	frames map[uintptr]*routerFrame // call node -> innermost frame, for method calls named like routes or clients
	kinds  map[uintptr]string       // call node -> memoized receiver classification

	fnFrames  map[uintptr]*routerFrame // function node -> its frame
	callKinds map[uintptr]routerCallMemo
}

// routerCallMemo is the classification of a call expression as seen from one
// frame and position, so a fluent chain is classified once per link.
type routerCallMemo struct {
	frame *routerFrame
	pos   uint32
	kind  string
}

func (p *JavaScriptParser) newRouterFrame(parent *routerFrame, function, class bool) *routerFrame {
	return &routerFrame{parent: parent, function: function, class: class}
}

func (f *routerFrame) add(name string, b *routerBinding) {
	if name == "" {
		return
	}
	if f.names == nil {
		f.names = map[string][]*routerBinding{}
	}
	if b.frame == nil {
		b.frame = f
	}
	f.names[name] = append(f.names[name], b)
}

func (f *routerFrame) functionFrame() *routerFrame {
	for ; f != nil; f = f.parent {
		if f.function {
			return f
		}
	}
	return nil
}

func (f *routerFrame) classFrame() *routerFrame {
	for ; f != nil; f = f.parent {
		if f.class {
			return f
		}
	}
	return nil
}

// collectRouterBindings walks the tree once, building the scope frames and the
// bindings declared in each, and records the frame of every method call that
// extractEndpoint or extractHttpCall may need to classify.
func (p *JavaScriptParser) collectRouterBindings(root *sitter.Node, content []byte) {
	idx := &routerIndex{
		src:    content,
		frames: map[uintptr]*routerFrame{},
		kinds:  map[uintptr]string{},

		fnFrames:  map[uintptr]*routerFrame{},
		callKinds: map[uintptr]routerCallMemo{},
	}
	idx.root = p.newRouterFrame(nil, true, false)
	p.routerIdx = idx

	cursor := sitter.NewTreeCursor(root)
	defer cursor.Close()

	type saved struct {
		frame    *routerFrame
		nodeType string
	}
	var stack []saved
	cur := idx.root
	parentType := ""
	for {
		node := cursor.CurrentNode()
		nodeType := node.Type()
		stack = append(stack, saved{cur, parentType})
		cur = p.enterRouterNode(node, nodeType, parentType, cur, content)
		parentType = nodeType
		if cursor.GoToFirstChild() {
			continue
		}
		for {
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			cur, parentType = top.frame, top.nodeType
			if cursor.GoToNextSibling() {
				break
			}
			if !cursor.GoToParent() {
				return
			}
		}
	}
}

func routerTypeOf(annotation *sitter.Node) *sitter.Node {
	if annotation == nil {
		return nil
	}
	if annotation.Type() == "type_annotation" {
		if annotation.NamedChildCount() == 0 {
			return nil
		}
		return annotation.NamedChild(0)
	}
	return annotation
}

// enterRouterNode records the declarations introduced by node and returns the
// frame in effect for its children.
func (p *JavaScriptParser) enterRouterNode(node *sitter.Node, nodeType, parentType string, cur *routerFrame, content []byte) *routerFrame {
	switch nodeType {
	case "import_statement":
		p.collectRouterImport(node, cur, content)
	case "variable_declarator":
		target := cur
		if parentType == "variable_declaration" {
			target = cur.functionFrame()
		}
		name, value := node.ChildByFieldName("name"), node.ChildByFieldName("value")
		if name == nil {
			return cur
		}
		typ := routerTypeOf(node.ChildByFieldName("type"))
		if name.Type() == "identifier" {
			target.add(name.Content(content), &routerBinding{frame: cur, pos: node.StartByte(), value: value, typ: typ, decl: "var"})
			return cur
		}
		p.bindRouterPattern(name, target, cur, node.StartByte(), value, "var", content)
	case "assignment_expression":
		left, right := node.ChildByFieldName("left"), node.ChildByFieldName("right")
		if left == nil || right == nil {
			return cur
		}
		switch left.Type() {
		case "identifier":
			// Reassignment: attach to the declaring scope so later references
			// outside the assignment's own block see it.
			target := p.routerDeclaringFrame(left.Content(content), cur)
			target.add(left.Content(content), &routerBinding{frame: cur, pos: node.StartByte(), value: right, decl: "var"})
		case "member_expression":
			if obj := left.ChildByFieldName("object"); obj != nil && obj.Type() == "this" {
				if prop := left.ChildByFieldName("property"); prop != nil {
					if class := cur.classFrame(); class != nil {
						class.add(prop.Content(content), &routerBinding{frame: cur, pos: node.StartByte(), value: right, decl: "var"})
					}
				}
			}
		}
	case "function_declaration", "generator_function_declaration":
		if name := node.ChildByFieldName("name"); name != nil {
			cur.add(name.Content(content), &routerBinding{pos: node.StartByte(), value: node, decl: "function", kind: rkOther})
		}
		return p.enterRouterFunction(node, cur, content)
	case "function", "function_expression", "generator_function", "arrow_function", "method_definition":
		return p.enterRouterFunction(node, cur, content)
	case "class_declaration", "class":
		if name := node.ChildByFieldName("name"); name != nil && nodeType == "class_declaration" {
			cur.add(name.Content(content), &routerBinding{pos: node.StartByte(), value: node, decl: "class"})
		}
	case "return_statement":
		if frame := cur.functionFrame(); frame != nil {
			for i := 0; i < int(node.NamedChildCount()); i++ {
				if arg := node.NamedChild(i); arg.Type() != "comment" {
					frame.returns = append(frame.returns, routerReturn{expr: arg, frame: cur, pos: node.StartByte()})
					break
				}
			}
		}
	case "class_body":
		return p.newRouterFrame(cur, false, true)
	case "public_field_definition", "field_definition":
		if class := cur.classFrame(); class != nil {
			name := node.ChildByFieldName("name")
			if name == nil {
				name = node.NamedChild(0)
			}
			if name != nil && name.Type() == "property_identifier" {
				class.add(name.Content(content), &routerBinding{
					value: node.ChildByFieldName("value"),
					typ:   routerTypeOf(node.ChildByFieldName("type")),
					decl:  "var",
				})
			}
		}
	case "statement_block", "for_statement", "for_in_statement", "catch_clause":
		frame := p.newRouterFrame(cur, false, false)
		switch nodeType {
		case "for_in_statement":
			if left := node.ChildByFieldName("left"); left != nil {
				p.bindRouterPattern(left, frame, frame, node.StartByte(), nil, "param", content)
			}
		case "catch_clause":
			if param := node.ChildByFieldName("parameter"); param != nil {
				p.bindRouterPattern(param, frame, frame, node.StartByte(), nil, "param", content)
			}
		}
		return frame
	case "call_expression":
		if fn := node.ChildByFieldName("function"); fn != nil && fn.Type() == "member_expression" {
			if prop := fn.ChildByFieldName("property"); prop != nil && routerCallMethods[strings.ToLower(prop.Content(content))] {
				p.routerIdx.frames[node.ID()] = cur
			}
		}
	}
	return cur
}

func (p *JavaScriptParser) enterRouterFunction(node *sitter.Node, cur *routerFrame, content []byte) *routerFrame {
	frame := p.newRouterFrame(cur, true, false)
	pos := node.StartByte()
	p.routerIdx.fnFrames[node.ID()] = frame
	for i := 0; i < int(node.ChildCount()) && i < 4; i++ {
		if node.Child(i).Type() == "async" {
			frame.opaque = true
		}
	}
	if strings.HasPrefix(node.Type(), "generator_function") {
		frame.opaque = true
	}
	if body := node.ChildByFieldName("body"); body != nil && node.Type() == "arrow_function" && body.Type() != "statement_block" {
		frame.returns = append(frame.returns, routerReturn{expr: body, frame: frame, pos: body.StartByte()})
	}
	if param := node.ChildByFieldName("parameter"); param != nil {
		frame.add(param.Content(content), &routerBinding{pos: pos, decl: "param"})
	}
	params := node.ChildByFieldName("parameters")
	if params == nil {
		return frame
	}
	for i := 0; i < int(params.NamedChildCount()); i++ {
		param := params.NamedChild(i)
		switch param.Type() {
		case "required_parameter", "optional_parameter":
			pattern := param.ChildByFieldName("pattern")
			if pattern == nil {
				continue
			}
			typ := routerTypeOf(param.ChildByFieldName("type"))
			value := param.ChildByFieldName("value")
			if pattern.Type() == "identifier" {
				name := pattern.Content(content)
				frame.add(name, &routerBinding{pos: pos, typ: typ, value: value, decl: "param"})
				// Constructor parameter properties declare a class field.
				for j := 0; j < int(param.NamedChildCount()); j++ {
					if param.NamedChild(j).Type() == "accessibility_modifier" {
						if class := cur.classFrame(); class != nil {
							class.add(name, &routerBinding{typ: typ, value: value, decl: "var"})
						}
						break
					}
				}
				continue
			}
			p.bindRouterPattern(pattern, frame, frame, pos, nil, "param", content)
		default:
			p.bindRouterPattern(param, frame, frame, pos, nil, "param", content)
		}
	}
	return frame
}

// bindRouterPattern declares every identifier of a destructuring or parameter
// pattern. Values destructured out of an object keep their property key so
// `const { Router } = require('express')` can be resolved.
func (p *JavaScriptParser) bindRouterPattern(n *sitter.Node, frame, eval *routerFrame, pos uint32, value *sitter.Node, decl string, content []byte) {
	switch n.Type() {
	case "identifier", "shorthand_property_identifier_pattern":
		frame.add(n.Content(content), &routerBinding{frame: eval, pos: pos, value: value, decl: decl, key: n.Content(content)})
	case "pair_pattern":
		key, target := n.ChildByFieldName("key"), n.ChildByFieldName("value")
		if target == nil {
			return
		}
		if target.Type() == "identifier" && key != nil {
			frame.add(target.Content(content), &routerBinding{frame: eval, pos: pos, value: value, decl: decl, key: key.Content(content)})
			return
		}
		p.bindRouterPattern(target, frame, eval, pos, nil, decl, content)
	case "assignment_pattern", "object_assignment_pattern":
		if left := n.ChildByFieldName("left"); left != nil {
			p.bindRouterPattern(left, frame, eval, pos, value, decl, content)
		}
	case "object_pattern", "array_pattern", "rest_pattern", "required_parameter", "optional_parameter":
		pass := value
		if n.Type() != "object_pattern" {
			pass = nil
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			p.bindRouterPattern(n.NamedChild(i), frame, eval, pos, pass, decl, content)
		}
	}
}

// routerDeclaringFrame finds the frame holding the declaration an assignment
// refers to, falling back to the module scope for implicit globals.
func (p *JavaScriptParser) routerDeclaringFrame(name string, from *routerFrame) *routerFrame {
	for f := from; f != nil; f = f.parent {
		if len(f.names[name]) > 0 {
			return f
		}
	}
	return p.routerIdx.root
}

func (p *JavaScriptParser) collectRouterImport(node *sitter.Node, frame *routerFrame, content []byte) {
	source := node.ChildByFieldName("source")
	module := ""
	for i := 0; i < int(node.NamedChildCount()); i++ {
		// `import x = require('m')` keeps its source inside import_require_clause.
		if clause := node.NamedChild(i); clause.Type() == "import_require_clause" {
			source = clause.ChildByFieldName("source")
		}
	}
	if source != nil {
		module = strings.Trim(source.Content(content), "\"'`")
	}
	add := func(name string, member string, whole bool) {
		kind := ""
		if whole {
			kind = routerModuleWholeKind(module)
		} else {
			kind = routerModuleMemberKind(module, member)
		}
		frame.add(name, &routerBinding{pos: node.StartByte(), decl: "import", kind: kind})
	}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Type() {
		case "import_specifier":
			name, alias := n.ChildByFieldName("name"), n.ChildByFieldName("alias")
			if name == nil {
				return
			}
			if alias == nil {
				alias = name
			}
			if name.Content(content) == "default" {
				add(alias.Content(content), "", true)
			} else {
				add(alias.Content(content), name.Content(content), false)
			}
		case "namespace_import":
			for i := 0; i < int(n.NamedChildCount()); i++ {
				if id := n.NamedChild(i); id.Type() == "identifier" {
					add(id.Content(content), "", true)
				}
			}
		case "import_clause", "import_require_clause", "named_imports":
			for i := 0; i < int(n.NamedChildCount()); i++ {
				child := n.NamedChild(i)
				if child.Type() == "identifier" {
					add(child.Content(content), "", true)
					continue
				}
				walk(child)
			}
		}
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		walk(node.NamedChild(i))
	}
}

func routerModuleWholeKind(module string) string {
	if _, ok := routerModules[module]; ok || routerClientModules[module] {
		return rkModulePref + module
	}
	return ""
}

func routerModuleMemberKind(module, member string) string {
	if routerClientModules[module] {
		return rkClient
	}
	if spec, ok := routerModules[module]; ok {
		return spec.members[member]
	}
	return ""
}

// routerLookup resolves name as seen from frame at byte offset pos. Inside the
// declaring function, the latest declaration or assignment before pos wins.
// From a nested function the declaration order is unknowable (hoisted use), so
// the first binding that carries information wins. Class bodies are skipped:
// fields are reachable only through `this`.
func (p *JavaScriptParser) routerLookup(name string, frame *routerFrame, pos uint32, depth int) *routerBinding {
	crossed := false
	for f := frame; f != nil; f = f.parent {
		if bindings := f.names[name]; len(bindings) > 0 && !f.class {
			if !crossed {
				// Bindings are appended in source order, so the latest one at or
				// before pos is found by bisection.
				if i := sort.Search(len(bindings), func(i int) bool { return bindings[i].pos > pos }); i > 0 {
					return bindings[i-1]
				}
				return bindings[0]
			}
			return p.routerInformative(bindings, depth)
		}
		if f.function {
			crossed = true
		}
	}
	return nil
}

// routerInformative returns the first binding that classifies to something.
func (p *JavaScriptParser) routerInformative(bindings []*routerBinding, depth int) *routerBinding {
	for _, b := range bindings {
		if p.routerBindingKind(b, depth) != "" {
			return b
		}
	}
	return bindings[0]
}

// routerLookupField resolves `this.name` against the nearest enclosing class.
func (p *JavaScriptParser) routerLookupField(name string, frame *routerFrame, depth int) *routerBinding {
	if class := frame.classFrame(); class != nil {
		if bindings := class.names[name]; len(bindings) > 0 {
			for _, b := range bindings {
				if b.typ != nil {
					return b // a declared type is authoritative
				}
			}
			return p.routerInformative(bindings, depth)
		}
	}
	return nil
}

func (p *JavaScriptParser) routerBindingKind(b *routerBinding, depth int) string {
	switch b.state {
	case 2:
		return b.result
	case 1:
		return ""
	}
	b.state = 1
	result := b.kind
	if result == "" && b.typ != nil {
		switch p.routerClassify(b.typ, b.frame, b.pos, depth+1) {
		case rkRouterType, rkFactory, rkRouter:
			result = rkRouter
		case rkClient:
			result = rkClient
		}
	}
	if result == "" && b.value != nil {
		if b.decl == "class" {
			result = p.routerClassKind(b.value, b.frame, b.pos, depth)
		} else {
			result = p.routerClassify(b.value, b.frame, b.pos, depth+1)
			if b.key != "" {
				result = p.routerMemberKind(result, b.key)
			}
		}
	}
	b.state, b.result = 2, result
	return result
}

// A local class that extends a router factory is itself a factory.
func (p *JavaScriptParser) routerClassKind(class *sitter.Node, frame *routerFrame, pos uint32, depth int) string {
	for i := 0; i < int(class.NamedChildCount()); i++ {
		heritage := class.NamedChild(i)
		if heritage.Type() != "class_heritage" {
			continue
		}
		expr := heritage
		if heritage.NamedChildCount() > 0 {
			expr = heritage.NamedChild(0)
			if expr.Type() == "extends_clause" {
				expr = expr.ChildByFieldName("value")
			}
		}
		if expr != nil && p.routerClassify(expr, frame, pos, depth+1) == rkFactory {
			return rkFactory
		}
	}
	return rkClass
}

// routerMemberKind resolves `<kind>.member`.
func (p *JavaScriptParser) routerMemberKind(kind, member string) string {
	switch {
	case kind == rkClient:
		return rkClient
	case strings.HasPrefix(kind, rkModulePref):
		return routerModuleMemberKind(strings.TrimPrefix(kind, rkModulePref), member)
	}
	return ""
}

// routerNameLooksLikeClientFactory matches factories named for what they
// return: createClient, getAxios, useHttp, makeApi. The last word decides, so
// createApiApp and buildHttpServer are not clients.
func routerNameLooksLikeClientFactory(name string) bool {
	tokens := splitIdentifierTokens(name)
	if len(tokens) == 0 {
		return false
	}
	switch tokens[len(tokens)-1] {
	case "axios", "client", "http", "fetch", "request", "ky", "got", "api", "agent", "superagent":
		return true
	}
	return false
}

func routerNodeName(n *sitter.Node, content []byte) string {
	switch n.Type() {
	case "identifier", "type_identifier":
		return n.Content(content)
	case "member_expression":
		if prop := n.ChildByFieldName("property"); prop != nil {
			return prop.Content(content)
		}
	}
	return ""
}

// routerClassify returns the role of the value expr evaluates to, as seen from
// frame at byte offset pos. The empty string means "cannot tell".
func (p *JavaScriptParser) routerClassify(expr *sitter.Node, frame *routerFrame, pos uint32, depth int) string {
	idx := p.routerIdx
	if expr == nil || idx == nil || depth > 16 {
		return ""
	}
	content := idx.src
	switch expr.Type() {
	case "identifier", "type_identifier":
		name := expr.Content(content)
		if b := p.routerLookup(name, frame, pos, depth+1); b != nil {
			return p.routerBindingKind(b, depth)
		}
		if routerClientGlobals[name] {
			return rkClient
		}
	case "generic_type":
		return p.routerClassify(expr.ChildByFieldName("name"), frame, pos, depth+1)
	case "nested_type_identifier":
		module, name := expr.ChildByFieldName("module"), expr.ChildByFieldName("name")
		if module != nil && name != nil {
			return p.routerMemberKind(p.routerClassify(module, frame, pos, depth+1), name.Content(content))
		}
	case "union_type":
		result := ""
		for i := 0; i < int(expr.NamedChildCount()); i++ {
			member := expr.NamedChild(i)
			if member.Type() == "literal_type" {
				continue // `| undefined`, `| null`
			}
			kind := p.routerClassify(member, frame, pos, depth+1)
			if kind == rkFactory || kind == rkRouterType {
				kind = rkRouter
			}
			if result != "" && result != kind {
				return ""
			}
			result = kind
		}
		return result
	case "member_expression":
		obj, prop := expr.ChildByFieldName("object"), expr.ChildByFieldName("property")
		if obj == nil || prop == nil {
			return ""
		}
		if obj.Type() == "this" {
			if b := p.routerLookupField(prop.Content(content), frame, depth+1); b != nil {
				return p.routerBindingKind(b, depth)
			}
			return ""
		}
		return p.routerMemberKind(p.routerClassify(obj, frame, pos, depth+1), prop.Content(content))
	case "call_expression", "new_expression":
		return p.routerClassifyCall(expr, frame, pos, depth)
	case "parenthesized_expression", "non_null_expression":
		if expr.NamedChildCount() > 0 {
			return p.routerClassify(expr.NamedChild(0), frame, pos, depth+1)
		}
	case "as_expression", "satisfies_expression", "type_assertion":
		// `<T>value` stores the type first; `value as T` stores it last.
		var value, typ *sitter.Node
		if n := int(expr.NamedChildCount()); n >= 2 {
			if expr.Type() == "type_assertion" {
				typ, value = expr.NamedChild(0), expr.NamedChild(n-1)
			} else {
				value, typ = expr.NamedChild(0), expr.NamedChild(n-1)
			}
		}
		if typ != nil && typ.Type() == "type_arguments" && typ.NamedChildCount() > 0 {
			typ = typ.NamedChild(0)
		}
		kind := p.routerClassify(value, frame, pos, depth+1)
		if kind == "" {
			switch p.routerClassify(typ, frame, pos, depth+1) {
			case rkRouter:
				return rkRouter
			case rkRouterType, rkFactory:
				return rkRouter
			case rkClient:
				return rkClient
			}
		}
		return kind
	case "assignment_expression":
		return p.routerClassify(expr.ChildByFieldName("right"), frame, pos, depth+1)
	case "ternary_expression":
		a := p.routerClassify(expr.ChildByFieldName("consequence"), frame, pos, depth+1)
		if a == p.routerClassify(expr.ChildByFieldName("alternative"), frame, pos, depth+1) {
			return a
		}
	case "await_expression":
		// An awaited factory may well resolve to a router; only a client stays one.
		if expr.NamedChildCount() > 0 && p.routerClassify(expr.NamedChild(0), frame, pos, depth+1) == rkClient {
			return rkClient
		}
	case "binary_expression":
		if op := expr.ChildByFieldName("operator"); op != nil {
			switch op.Content(content) {
			case "||", "??":
				if kind := p.routerClassify(expr.ChildByFieldName("left"), frame, pos, depth+1); kind != "" {
					return kind
				}
				return p.routerClassify(expr.ChildByFieldName("right"), frame, pos, depth+1)
			}
		}
		return rkOther
	case "object", "array", "string", "template_string", "number", "regex", "true", "false",
		"arrow_function", "function_expression", "function", "generator_function":
		return rkOther
	}
	return ""
}

// routerClassifyCall memoizes per call node: every link of a fluent chain is
// asked for its role by the link after it and again by the extractor.
func (p *JavaScriptParser) routerClassifyCall(expr *sitter.Node, frame *routerFrame, pos uint32, depth int) string {
	idx := p.routerIdx
	id := expr.ID()
	if memo, ok := idx.callKinds[id]; ok && memo.frame == frame && memo.pos == pos {
		return memo.kind
	}
	kind := p.routerClassifyCallUncached(expr, frame, pos, depth)
	idx.callKinds[id] = routerCallMemo{frame: frame, pos: pos, kind: kind}
	return kind
}

func (p *JavaScriptParser) routerClassifyCallUncached(expr *sitter.Node, frame *routerFrame, pos uint32, depth int) string {
	content := p.routerIdx.src
	fn := expr.ChildByFieldName("function")
	if fn == nil {
		fn = expr.ChildByFieldName("constructor")
	}
	args := expr.ChildByFieldName("arguments")
	if fn == nil {
		return ""
	}
	isNew := expr.Type() == "new_expression"
	if !isNew && fn.Type() == "identifier" && fn.Content(content) == "require" {
		// A locally declared require is not the CommonJS loader.
		if p.routerLookup("require", frame, pos, depth+1) == nil && args != nil && args.NamedChildCount() == 1 && args.NamedChild(0).Type() == "string" {
			return routerModuleWholeKind(strings.Trim(args.NamedChild(0).Content(content), "\"'`"))
		}
		return ""
	}

	// A method call classifies its receiver once and derives the callee from
	// it; descending a chain is structural and does not consume depth.
	var obj, prop *sitter.Node
	receiver := ""
	var callee string
	if fn.Type() == "member_expression" {
		obj, prop = fn.ChildByFieldName("object"), fn.ChildByFieldName("property")
	}
	if obj != nil && prop != nil && obj.Type() != "this" {
		receiver = p.routerClassify(obj, frame, pos, depth)
		callee = p.routerMemberKind(receiver, prop.Content(content))
	} else {
		callee = p.routerClassify(fn, frame, pos, depth+1)
	}
	switch {
	case callee == rkOther && !isNew:
		// A function declared in this file is classified by what it returns,
		// whatever it is called.
		if local := p.routerLocalFunction(fn, frame, pos, depth); local != nil {
			if kind := p.routerFunctionResult(local, depth); kind != "" {
				return kind
			}
		}
	case callee == rkFactory:
		return rkRouter
	case callee == rkClient:
		return rkClient
	case callee == rkClass:
		return rkOther
	case strings.HasPrefix(callee, rkModulePref):
		module := strings.TrimPrefix(callee, rkModulePref)
		if routerClientModules[module] {
			return rkClient
		}
		if routerModules[module].callable {
			return rkRouter
		}
		return ""
	}

	if !isNew && fn.Type() == "identifier" && routerFactoryGlobals[fn.Content(content)] && p.routerLookup(fn.Content(content), frame, pos, depth+1) == nil {
		return rkRouter
	}

	if isNew {
		name := routerNodeName(fn, content)
		if fn.Type() == "identifier" && p.routerLookup(name, frame, pos, depth+1) == nil && routerClassNames[name] {
			return rkRouter
		}
		if tokens := splitIdentifierTokens(name); len(tokens) > 0 {
			switch tokens[len(tokens)-1] {
			case "router", "routes", "app", "server", "application":
				return "" // may be a router subclass defined elsewhere
			}
		}
		return rkOther
	}

	if fn.Type() == "member_expression" {
		if obj == nil || prop == nil {
			return ""
		}
		method := prop.Content(content)
		switch receiver {
		case rkRouter:
			switch {
			case routerChainMethods[method]:
				return rkRouter
			case method == "route" && routerArgCount(args) == 1:
				return rkRoute
			case method == "route" && routerArgCount(args) > 1:
				return rkRouter // Hono: app.route('/base', subApp) returns the app
			case routerRegistrationMethods[strings.ToLower(method)] != "" && routerArgCount(args) >= 2:
				return rkRouter // one argument is a settings getter such as app.get('env')
			}
			return ""
		case rkRoute:
			if routerRegistrationMethods[strings.ToLower(method)] != "" || method == "all" {
				return rkRoute
			}
			return ""
		case rkClient:
			return rkClient
		case "":
			// router.route('/x') on a conventionally named receiver whose
			// provenance is not visible: the chain that follows is routes.
			if method == "route" && args != nil && args.NamedChildCount() > 0 && args.NamedChild(0).Type() == "string" && routerReceiverNameConventional(obj, content) {
				return rkRoute
			}
		}
	}
	if name := routerNodeName(fn, content); name != "" && routerNameLooksLikeClientFactory(name) {
		return rkClient
	}
	return ""
}

// routerLocalFunction returns the frame of the function an identifier callee
// names, when that function is declared in the file.
func (p *JavaScriptParser) routerLocalFunction(callee *sitter.Node, frame *routerFrame, pos uint32, depth int) *routerFrame {
	if callee.Type() != "identifier" {
		return nil
	}
	b := p.routerLookup(callee.Content(p.routerIdx.src), frame, pos, depth+1)
	if b == nil || b.key != "" || b.value == nil {
		return nil
	}
	if b.decl != "function" && (b.decl != "var" || !routerFunctionNode(b.value)) {
		return nil
	}
	return p.routerIdx.fnFrames[b.value.ID()]
}

// routerFunctionResult is the role shared by the values a function returns.
// Returns of unknown or plain values are ignored, so `if (!x) return null`
// does not hide `return express()`; a function returning both a router and a
// client is unclassified.
func (p *JavaScriptParser) routerFunctionResult(fn *routerFrame, depth int) string {
	switch fn.retState {
	case 2:
		return fn.retKind
	case 1:
		return ""
	}
	fn.retState = 1
	result := ""
	if !fn.opaque {
		for _, ret := range fn.returns {
			kind := p.routerClassify(ret.expr, ret.frame, ret.pos, depth+1)
			if kind != rkRouter && kind != rkClient {
				continue
			}
			if result != "" && result != kind {
				result = ""
				break
			}
			result = kind
		}
	}
	fn.retState, fn.retKind = 2, result
	return result
}

func routerArgCount(args *sitter.Node) int {
	if args == nil {
		return 0
	}
	count := 0
	for i := 0; i < int(args.NamedChildCount()); i++ {
		if args.NamedChild(i).Type() != "comment" {
			count++
		}
	}
	return count
}

// routerReceiverNameConventional reports whether the receiver is named like a
// server object: app, router, routes, server, fastify, or a camel-case name
// ending in one of them (apiRouter, adminApp, this.app).
func routerReceiverNameConventional(receiver *sitter.Node, content []byte) bool {
	name := ""
	switch receiver.Type() {
	case "identifier":
		name = receiver.Content(content)
	case "member_expression":
		if prop := receiver.ChildByFieldName("property"); prop != nil {
			name = prop.Content(content)
		}
	}
	tokens := splitIdentifierTokens(name)
	if len(tokens) == 0 {
		return false
	}
	switch tokens[len(tokens)-1] {
	case "app", "router", "routes", "route", "server", "fastify", "application":
		return true
	}
	return false
}

// callReceiverKind classifies the receiver of a method call, memoized per call.
func (p *JavaScriptParser) callReceiverKind(call *sitter.Node) string {
	idx := p.routerIdx
	if idx == nil {
		return ""
	}
	id := call.ID()
	frame := idx.frames[id]
	if frame == nil {
		return "" // not a method call that can be a registration or client call
	}
	if kind, ok := idx.kinds[id]; ok {
		return kind
	}
	kind := ""
	if fn := call.ChildByFieldName("function"); fn != nil {
		if obj := fn.ChildByFieldName("object"); obj != nil {
			kind = p.routerClassify(obj, frame, call.StartByte(), 0)
		}
	}
	idx.kinds[id] = kind
	return kind
}

// callOnRouter reports whether the call is a method call on a known router,
// which must never be recorded as an outbound HTTP client call.
func (p *JavaScriptParser) callOnRouter(call *sitter.Node) bool {
	if p.routerIdx == nil {
		return false
	}
	kind := p.callReceiverKind(call)
	return kind == rkRouter || kind == rkRoute
}

// routeCallLooksRegistered decides whether a call on a receiver of unknown
// provenance registers a route, using only the shape of the call: the first
// argument is a path and a later argument is a handler. A receiver named like
// a server object (app, router, ...) registers even when the result is kept,
// as in `export default app.get(...)`, unless the call is awaited or chained
// into a promise. Any other receiver must discard the result and pass a
// handler that takes request/response parameters.
func (p *JavaScriptParser) routeCallLooksRegistered(call, object *sitter.Node, args []*sitter.Node, frame *routerFrame, content []byte) bool {
	if len(args) < 2 || frame == nil {
		return false
	}
	path := routerPathText(args[0], content)
	if !strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "*") {
		return false
	}
	if object.Type() == "call_expression" {
		return false
	}
	handler := false
	shaped := false
	for _, arg := range args[1:] {
		if p.routeArgIsHandler(arg, frame, call.StartByte(), content, 0) {
			handler = true
			if routerFunctionTakesRequest(arg, content) {
				shaped = true
			}
		}
	}
	if !handler {
		return false
	}
	if routerReceiverNameConventional(object, content) {
		return !routerCallResultAwaited(call, content)
	}
	return shaped && routerCallResultUnused(call)
}

// routerCallResultAwaited reports whether the call is awaited or consumed as a
// promise (`.then`, `.catch`, `.finally`), which only an HTTP client call is.
func routerCallResultAwaited(call *sitter.Node, content []byte) bool {
	cur := call
	for i := 0; i < 32; i++ {
		parent := cur.Parent()
		if parent == nil {
			return false
		}
		switch parent.Type() {
		case "await_expression":
			return true
		case "parenthesized_expression", "non_null_expression":
			cur = parent
		case "member_expression":
			if prop := parent.ChildByFieldName("property"); prop != nil {
				switch prop.Content(content) {
				case "then", "catch", "finally":
					return true
				}
			}
			return false
		default:
			return false
		}
	}
	return false
}

// routerCallResultUnused reports whether the call is an expression statement
// (possibly the tail of a chain such as app.get(...).post(...)).
func routerCallResultUnused(call *sitter.Node) bool {
	cur := call
	for i := 0; i < 32; i++ {
		parent := cur.Parent()
		if parent == nil {
			return false
		}
		switch parent.Type() {
		case "expression_statement":
			return true
		case "arrow_function":
			// `app => app.get('/x', h)`: the value is returned only to satisfy the arrow's shape.
			body := parent.ChildByFieldName("body")
			return body != nil && body.ID() == cur.ID()
		case "parenthesized_expression", "non_null_expression", "sequence_expression":
			cur = parent
		case "member_expression":
			grand := parent.Parent()
			if grand == nil || grand.Type() != "call_expression" {
				return false
			}
			if fn := grand.ChildByFieldName("function"); fn == nil || fn.ID() != parent.ID() {
				return false
			}
			cur = grand
		default:
			return false
		}
	}
	return false
}

// routerPathText returns the route path of a literal first argument.
func routerPathText(arg *sitter.Node, content []byte) string {
	switch arg.Type() {
	case "string":
		return strings.Trim(arg.Content(content), "\"'")
	case "template_string":
		// Each ${...} becomes one :param segment, however many braces it nests.
		var out strings.Builder
		prev := arg.StartByte() + 1
		for i := 0; i < int(arg.NamedChildCount()); i++ {
			sub := arg.NamedChild(i)
			if sub.Type() != "template_substitution" {
				continue
			}
			out.Write(content[prev:sub.StartByte()])
			out.WriteString(":" + routerSubstitutionName(sub, content))
			prev = sub.EndByte()
		}
		if end := arg.EndByte(); end > prev {
			out.Write(content[prev : end-1])
		}
		// A template that opens with a substitution (`${BASE}/x`) has an
		// unknown prefix, so it is not a path this parser can state.
		if path := out.String(); strings.HasPrefix(path, "/") || strings.HasPrefix(path, "*") {
			return path
		}
	}
	return ""
}

// routerSubstitutionName names the parameter of a template substitution: the
// identifier or the last member of a plain reference, otherwise "param".
func routerSubstitutionName(sub *sitter.Node, content []byte) string {
	if sub.NamedChildCount() == 1 {
		expr := sub.NamedChild(0)
		for expr.Type() == "parenthesized_expression" && expr.NamedChildCount() == 1 {
			expr = expr.NamedChild(0)
		}
		switch expr.Type() {
		case "identifier":
			return expr.Content(content)
		case "member_expression":
			if prop := expr.ChildByFieldName("property"); prop != nil && prop.Type() == "property_identifier" {
				return prop.Content(content)
			}
		}
	}
	return "param"
}

func routerFunctionNode(n *sitter.Node) bool {
	switch n.Type() {
	case "arrow_function", "function_expression", "function", "generator_function":
		return true
	}
	return false
}

// routerFunctionTakesRequest reports whether an inline function declares
// parameters in the shape of a request handler: (req, res), (request, reply),
// or a single ctx.
func routerFunctionTakesRequest(fn *sitter.Node, content []byte) bool {
	if !routerFunctionNode(fn) {
		return false
	}
	var names []string
	if param := fn.ChildByFieldName("parameter"); param != nil {
		names = append(names, param.Content(content))
	}
	if params := fn.ChildByFieldName("parameters"); params != nil {
		for i := 0; i < int(params.NamedChildCount()); i++ {
			param := params.NamedChild(i)
			if pattern := param.ChildByFieldName("pattern"); pattern != nil {
				param = pattern
			}
			names = append(names, param.Content(content))
		}
	}
	if len(names) == 1 {
		return names[0] == "ctx" || names[0] == "context"
	}
	if len(names) < 2 {
		return false
	}
	switch names[0] {
	case "req", "request":
		switch names[1] {
		case "res", "response", "reply", "resp":
			return true
		}
	}
	return false
}

// routerIsErrorMiddleware reports whether arg is an inline function with the
// four parameters of Express error middleware, (err, req, res, next).
func routerIsErrorMiddleware(arg *sitter.Node) bool {
	if !routerFunctionNode(arg) {
		return false
	}
	params := arg.ChildByFieldName("parameters")
	if params == nil {
		return false
	}
	count := 0
	for i := 0; i < int(params.NamedChildCount()); i++ {
		if params.NamedChild(i).Type() != "comment" {
			count++
		}
	}
	return count == 4
}

// routeArgIsHandler reports whether arg can be a route handler: an inline
// function, a reference that is not provably a plain value, a call that
// produces one, a spread of handlers, or an array of them.
func (p *JavaScriptParser) routeArgIsHandler(arg *sitter.Node, frame *routerFrame, pos uint32, content []byte, depth int) bool {
	if depth > 4 {
		return false
	}
	switch arg.Type() {
	case "arrow_function", "function_expression", "function", "generator_function":
		return true
	case "member_expression":
		return true
	case "identifier":
		b := p.routerLookup(arg.Content(content), frame, pos, depth+1)
		if b == nil || b.value == nil || b.decl != "var" {
			return true
		}
		switch b.value.Type() {
		case "object", "array", "string", "template_string", "number", "true", "false", "null", "new_expression", "await_expression":
			return false
		}
		return true
	case "parenthesized_expression", "as_expression", "satisfies_expression", "non_null_expression":
		return arg.NamedChildCount() > 0 && p.routeArgIsHandler(arg.NamedChild(0), frame, pos, content, depth+1)
	case "array":
		for i := 0; i < int(arg.NamedChildCount()); i++ {
			if p.routeArgIsHandler(arg.NamedChild(i), frame, pos, content, depth+1) {
				return true
			}
		}
	case "call_expression", "spread_element":
		return true // makeHandler(), ...handlers
	}
	return false
}

// routeHandlerName names the handler a registration argument refers to.
// Wrapper calls are looked through: wrap(h) is h, asyncHandler(c.list) is
// c.list, and c.method.bind(c) is c.method. An inline function is named like
// the synthetic function extracted for it.
func (p *JavaScriptParser) routeHandlerName(arg *sitter.Node, content []byte, depth int) string {
	if depth > 4 {
		return ""
	}
	switch arg.Type() {
	case "identifier", "member_expression":
		return arg.Content(content)
	case "arrow_function", "function_expression", "function", "generator_function":
		if name := arg.ChildByFieldName("name"); name != nil {
			return name.Content(content)
		}
		if name := p.syntheticCallbackName(arg, content); name != "" {
			return name
		}
		return anonymousFunctionName(arg)
	case "parenthesized_expression", "as_expression", "satisfies_expression", "non_null_expression":
		if arg.NamedChildCount() > 0 {
			return p.routeHandlerName(arg.NamedChild(0), content, depth+1)
		}
	case "array":
		for i := int(arg.NamedChildCount()) - 1; i >= 0; i-- {
			if name := p.routeHandlerName(arg.NamedChild(i), content, depth+1); name != "" {
				return name
			}
		}
	case "call_expression":
		fn, args := arg.ChildByFieldName("function"), arg.ChildByFieldName("arguments")
		if fn != nil && fn.Type() == "member_expression" {
			if prop := fn.ChildByFieldName("property"); prop != nil && prop.Content(content) == "bind" {
				if obj := fn.ChildByFieldName("object"); obj != nil {
					return p.routeHandlerName(obj, content, depth+1)
				}
			}
		}
		if args == nil {
			return ""
		}
		// Prefer an inline function or a reference the wrapper receives;
		// plain values such as options objects are skipped.
		for i := 0; i < int(args.NamedChildCount()); i++ {
			switch inner := args.NamedChild(i); inner.Type() {
			case "identifier", "member_expression", "arrow_function", "function_expression", "function", "generator_function", "call_expression":
				if name := p.routeHandlerName(inner, content, depth+1); name != "" {
					return name
				}
			}
		}
	}
	return ""
}

// routeChainPath finds the path of `x.route('/p').get(...).post(...)` by
// walking from the registration call down to the route() call.
func routeChainPath(object *sitter.Node, content []byte) string {
	for cur := object; cur != nil && cur.Type() == "call_expression"; {
		fn := cur.ChildByFieldName("function")
		if fn == nil || fn.Type() != "member_expression" {
			return ""
		}
		prop := fn.ChildByFieldName("property")
		if prop != nil && prop.Content(content) == "route" {
			if args := cur.ChildByFieldName("arguments"); args != nil && args.NamedChildCount() > 0 {
				return routerPathText(args.NamedChild(0), content)
			}
			return ""
		}
		cur = fn.ChildByFieldName("object")
	}
	return ""
}

// extractRouteObjectEndpoints reads the options form of route registration,
// fastify.route({ method: 'GET', url: '/x', handler }), where method may be a
// list. The receiver must be a known router, or be named like one with the
// result of the call unused.
func (p *JavaScriptParser) extractRouteObjectEndpoints(node *sitter.Node, content []byte) []ParsedEndpoint {
	idx := p.routerIdx
	if idx == nil || idx.frames[node.ID()] == nil {
		return nil
	}
	fn := node.ChildByFieldName("function")
	args := node.ChildByFieldName("arguments")
	if fn == nil || fn.Type() != "member_expression" || args == nil || args.NamedChildCount() != 1 {
		return nil
	}
	if prop := fn.ChildByFieldName("property"); prop == nil || prop.Content(content) != "route" {
		return nil
	}
	options := args.NamedChild(0)
	if options.Type() != "object" {
		return nil
	}
	switch p.callReceiverKind(node) {
	case rkRouter:
	case "":
		object := fn.ChildByFieldName("object")
		if object == nil || !routerReceiverNameConventional(object, content) || !routerCallResultUnused(node) {
			return nil
		}
	default:
		return nil
	}

	var methods []string
	var path, handler string
	for i := 0; i < int(options.NamedChildCount()); i++ {
		pair := options.NamedChild(i)
		if pair.Type() != "pair" {
			continue
		}
		key, value := pair.ChildByFieldName("key"), pair.ChildByFieldName("value")
		if key == nil || value == nil {
			continue
		}
		switch strings.Trim(key.Content(content), "\"'") {
		case "method":
			values := []*sitter.Node{value}
			if value.Type() == "array" {
				values = values[:0]
				for j := 0; j < int(value.NamedChildCount()); j++ {
					values = append(values, value.NamedChild(j))
				}
			}
			for _, v := range values {
				if v.Type() != "string" {
					continue
				}
				if verb := routerRegistrationMethods[strings.ToLower(strings.Trim(v.Content(content), "\"'"))]; verb != "" {
					methods = append(methods, verb)
				}
			}
		case "url", "path":
			path = routerPathText(value, content)
		case "handler":
			handler = p.routeHandlerName(value, content, 0)
		}
	}
	if path == "" || len(methods) == 0 {
		return nil
	}
	endpoints := make([]ParsedEndpoint, 0, len(methods))
	for _, method := range methods {
		endpoints = append(endpoints, ParsedEndpoint{Path: path, Method: method, HandlerName: handler, LineNumber: int(node.StartPoint().Row) + 1})
	}
	return endpoints
}
