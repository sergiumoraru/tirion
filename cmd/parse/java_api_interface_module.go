package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// javaAPIInterfaceModule attributes endpoints declared on generated API
// interfaces (OpenAPI/Swagger codegen, hand-written API contracts) to the
// controller method that implements them.
//
// The parser records such an endpoint on the interface method, which has no
// body. When exactly one @RestController/@Controller in the repository
// implements the interface and defines the method, the endpoint's handler is that
// controller method so traces continue into real code. The endpoint's file and
// line stay on the interface, where the mapping annotation is. With no unique
// controller the endpoint stays on the interface.
//
// The match is recomputed from the snapshot on every run, locating endpoints by
// their interface file and line rather than by handler, so it also survives
// incremental runs in which a controller file was re-parsed and its function
// ids changed.
type javaAPIInterfaceModule struct{}

func (m *javaAPIInterfaceModule) ID() string { return "java_api_interface" }

type javaAPIEndpoint struct {
	id         int64
	functionID int64 // the interface method the mapping annotation sits on
	path       string
	method     string
	handlerID  *int64
	iface      string
	methodName string
	paramCount int
}

type javaAPIClass struct {
	id         int64
	name       string
	fileID     int64
	implements []string
	controller bool
	basePaths  []string
}

type javaAPIFunction struct {
	id         int64
	name       string
	fileID     int64
	paramCount int
}

type javaAPIRewire struct {
	endpointID int64
	handlerID  int64
	path       string
	duplicate  bool // the controller already declares this route itself
}

// javaAPIMethodMapping is the route mapping declared on an interface method.
type javaAPIMethodMapping struct {
	known bool     // the method carries at least one mapping annotation
	paths []string // relative paths of its mapping annotations
}

var javaControllerAnnotations = map[string]bool{
	"RestController": true,
	"Controller":     true,
	"Path":           true, // JAX-RS resource
}

func (m *javaAPIInterfaceModule) FinalizeRepo(ctx *repoFinalizeContext) error {
	if ctx == nil || ctx.storage == nil {
		return nil
	}
	background := context.Background()
	pool := ctx.storage.Pool()
	snapshot := nullableInt64Ptr(ctx.snapshotID)

	// Endpoints declared on interface methods, found through the interface file
	// and the method's declaration line.
	rows, err := pool.Query(background, `
		SELECT DISTINCT e.id, e.path, e.method, e.handler_function_id, i.name, f.name, f.id,
		       COALESCE(jsonb_array_length(CASE WHEN jsonb_typeof(f.params)='array' THEN f.params ELSE '[]'::jsonb END), 0)
		FROM endpoints e
		JOIN files fl ON fl.id = e.file_id
		JOIN interfaces i ON i.file_id = fl.id
		JOIN functions f ON f.file_id = fl.id
		 AND f.start_line = e.line_number
		 AND left(f.name, length(i.name) + 1) = i.name || '.'
		WHERE e.repo_id = $1
		  AND fl.snapshot_id IS NOT DISTINCT FROM $2::bigint
		  AND fl.language = 'java'
	`, ctx.repoID, snapshot)
	if err != nil {
		return err
	}
	var endpoints []javaAPIEndpoint
	for rows.Next() {
		var e javaAPIEndpoint
		var functionName string
		if err := rows.Scan(&e.id, &e.path, &e.method, &e.handlerID, &e.iface, &functionName, &e.functionID, &e.paramCount); err != nil {
			rows.Close()
			return err
		}
		e.methodName = strings.TrimPrefix(functionName, e.iface+".")
		endpoints = append(endpoints, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(endpoints) == 0 {
		return err
	}

	ifaceNames := map[string]int{}
	iRows, err := pool.Query(background, `
		SELECT i.id, i.name FROM interfaces i JOIN files fl ON fl.id = i.file_id
		WHERE fl.repo_id = $1 AND fl.snapshot_id IS NOT DISTINCT FROM $2::bigint`, ctx.repoID, snapshot)
	if err != nil {
		return err
	}
	ifaceIDs := map[string]int64{}
	for iRows.Next() {
		var id int64
		var name string
		if err := iRows.Scan(&id, &name); err != nil {
			iRows.Close()
			return err
		}
		ifaceNames[name]++
		ifaceIDs[name] = id
	}
	err = iRows.Err()
	iRows.Close()
	if err != nil {
		return err
	}
	ifaceBases, err := loadJavaAPIBasePaths(ctx, "interface", ifaceIDs)
	if err != nil {
		return err
	}

	classRows, err := pool.Query(background, `
		SELECT c.id, c.name, c.file_id, c.implements
		FROM classes c JOIN files cf ON cf.id = c.file_id
		WHERE cf.repo_id = $1 AND cf.snapshot_id IS NOT DISTINCT FROM $2::bigint
		  AND cf.language = 'java'
		  AND c.implements IS NOT NULL AND jsonb_typeof(c.implements) = 'array'
		  AND jsonb_array_length(c.implements) > 0`, ctx.repoID, snapshot)
	if err != nil {
		return err
	}
	wanted := map[string]bool{}
	for _, e := range endpoints {
		wanted[e.iface] = true
	}
	var classes []javaAPIClass
	for classRows.Next() {
		var c javaAPIClass
		var raw []byte
		if err := classRows.Scan(&c.id, &c.name, &c.fileID, &raw); err != nil {
			classRows.Close()
			return err
		}
		var implements []string
		if json.Unmarshal(raw, &implements) != nil {
			continue
		}
		for _, impl := range implements {
			if name := javaSimpleTypeName(impl); wanted[name] {
				c.implements = append(c.implements, name)
			}
		}
		if len(c.implements) > 0 {
			classes = append(classes, c)
		}
	}
	err = classRows.Err()
	classRows.Close()
	if err != nil || len(classes) == 0 {
		return err
	}

	ids := make([]int64, 0, len(classes))
	for _, c := range classes {
		ids = append(ids, c.id)
	}
	annotationRows, err := pool.Query(background, `
		SELECT entity_id, name, COALESCE(values, 'null'::jsonb)
		FROM annotations WHERE entity_type = 'class' AND entity_id = ANY($1)`, ids)
	if err != nil {
		return err
	}
	byID := map[int64]*javaAPIClass{}
	for i := range classes {
		byID[classes[i].id] = &classes[i]
	}
	for annotationRows.Next() {
		var id int64
		var name string
		var raw []byte
		if err := annotationRows.Scan(&id, &name, &raw); err != nil {
			annotationRows.Close()
			return err
		}
		class := byID[id]
		if class == nil {
			continue
		}
		if javaControllerAnnotations[name] {
			class.controller = true
		}
		if name == "RequestMapping" || name == "Path" {
			class.basePaths = append(class.basePaths, javaAnnotationPaths(raw)...)
		}
	}
	err = annotationRows.Err()
	annotationRows.Close()
	if err != nil {
		return err
	}

	fileIDs := make([]int64, 0, len(classes))
	seenFiles := map[int64]bool{}
	for _, c := range classes {
		if !seenFiles[c.fileID] {
			seenFiles[c.fileID] = true
			fileIDs = append(fileIDs, c.fileID)
		}
	}
	functionRows, err := pool.Query(background, `
		SELECT id, name, file_id,
		       COALESCE(jsonb_array_length(CASE WHEN jsonb_typeof(params)='array' THEN params ELSE '[]'::jsonb END), 0)
		FROM functions WHERE file_id = ANY($1)`, fileIDs)
	if err != nil {
		return err
	}
	var functions []javaAPIFunction
	for functionRows.Next() {
		var f javaAPIFunction
		if err := functionRows.Scan(&f.id, &f.name, &f.fileID, &f.paramCount); err != nil {
			functionRows.Close()
			return err
		}
		functions = append(functions, f)
	}
	err = functionRows.Err()
	functionRows.Close()
	if err != nil {
		return err
	}

	existing := map[string]bool{}
	existingRows, err := pool.Query(background, `
		SELECT e.method, e.path, e.handler_function_id FROM endpoints e
		WHERE e.repo_id = $1 AND e.handler_function_id = ANY($2)`, ctx.repoID, functionIDs(functions))
	if err != nil {
		return err
	}
	for existingRows.Next() {
		var method, path string
		var handler int64
		if err := existingRows.Scan(&method, &path, &handler); err != nil {
			existingRows.Close()
			return err
		}
		existing[fmt.Sprintf("%s|%s|%d", method, path, handler)] = true
	}
	err = existingRows.Err()
	existingRows.Close()
	if err != nil {
		return err
	}

	methodMappings, err := loadJavaAPIMethodMappings(ctx, endpoints)
	if err != nil {
		return err
	}
	rewires := planJavaAPIEndpointRewires(endpoints, ifaceNames, ifaceBases, methodMappings, classes, functions, existing)
	if len(rewires) == 0 {
		return nil
	}
	tx, err := pool.Begin(background)
	if err != nil {
		return err
	}
	for _, r := range rewires {
		if r.duplicate {
			// The controller declares the same route itself; the interface copy would
			// be a second row for one handler.
			if _, err := tx.Exec(background, `DELETE FROM endpoints WHERE id = $1`, r.endpointID); err != nil {
				_ = tx.Rollback(background)
				return err
			}
			continue
		}
		if _, err := tx.Exec(background, `
			UPDATE endpoints SET handler_function_id = $2, path = $3, path_canonical = $4
			WHERE id = $1
			  AND NOT EXISTS (
			    SELECT 1 FROM endpoints o, endpoints me
			    WHERE me.id = $1 AND o.id <> me.id AND o.repo_id = me.repo_id AND o.path = $3
			      AND o.method = me.method AND o.handler_function_id = $2
			      AND o.file_id IS NOT DISTINCT FROM me.file_id
			      AND o.line_number IS NOT DISTINCT FROM me.line_number)`, r.endpointID, r.handlerID, r.path, normalizeEndpointPathIdentity(r.path)); err != nil {
			_ = tx.Rollback(background)
			return err
		}
	}
	return tx.Commit(background)
}

func functionIDs(functions []javaAPIFunction) []int64 {
	ids := make([]int64, 0, len(functions))
	for _, f := range functions {
		ids = append(ids, f.id)
	}
	return ids
}

// loadJavaAPIBasePaths reads class-level @RequestMapping/@Path values for the
// named declarations (interface rows keyed by name).
func loadJavaAPIBasePaths(ctx *repoFinalizeContext, entityType string, idsByName map[string]int64) (map[string][]string, error) {
	out := map[string][]string{}
	if len(idsByName) == 0 {
		return out, nil
	}
	ids := make([]int64, 0, len(idsByName))
	nameByID := map[int64]string{}
	for name, id := range idsByName {
		ids = append(ids, id)
		nameByID[id] = name
	}
	rows, err := ctx.storage.Pool().Query(context.Background(), `
		SELECT entity_id, COALESCE(values, 'null'::jsonb) FROM annotations
		WHERE entity_type = $1 AND entity_id = ANY($2) AND name IN ('RequestMapping', 'Path')`, entityType, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		out[nameByID[id]] = append(out[nameByID[id]], javaAnnotationPaths(raw)...)
	}
	return out, rows.Err()
}

// javaAnnotationPaths extracts the path strings of an annotation's JSON values
// ("value" or "path", a string or an array of strings).
func javaAnnotationPaths(raw []byte) []string {
	var values map[string]interface{}
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	var out []string
	for _, key := range []string{"path", "value"} {
		v, ok := values[key]
		if !ok {
			continue
		}
		switch typed := v.(type) {
		case string:
			out = append(out, typed)
		case []interface{}:
			for _, item := range typed {
				if s, ok := item.(string); ok {
					out = append(out, s)
				}
			}
		}
		break
	}
	return out
}

// javaSimpleTypeName reduces "com.acme.UsersApi<User>" to "UsersApi".
func javaSimpleTypeName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.IndexByte(name, '<'); i >= 0 {
		name = name[:i]
	}
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return strings.TrimSpace(name)
}

// planJavaAPIEndpointRewires decides which interface endpoints move to a
// controller method. It is pure so the matching rules can be tested without a
// database.
//
//   - The interface name must be unique in the snapshot; two interfaces sharing a
//     simple name cannot be told apart from a class's implements list.
//   - Exactly one controller class must implement the interface. Two
//     implementations (versioned controllers) leave the endpoint on the interface.
//   - The controller must define the method; overloads are told apart by
//     parameter count and left alone when still ambiguous.
//   - A controller with its own class-level mapping replaces the interface's
//     base path, as Spring does.
func planJavaAPIEndpointRewires(endpoints []javaAPIEndpoint, ifaceNames map[string]int, ifaceBases map[string][]string, methodMappings map[int64]javaAPIMethodMapping, classes []javaAPIClass, functions []javaAPIFunction, existing map[string]bool) []javaAPIRewire {
	controllers := map[string][]javaAPIClass{}
	for _, class := range classes {
		if !class.controller {
			continue
		}
		for _, iface := range class.implements {
			controllers[iface] = append(controllers[iface], class)
		}
	}
	var out []javaAPIRewire
	for _, e := range endpoints {
		if ifaceNames[e.iface] != 1 || len(controllers[e.iface]) != 1 {
			continue
		}
		class := controllers[e.iface][0]
		var candidates []javaAPIFunction
		for _, f := range functions {
			if f.fileID == class.fileID && f.name == class.name+"."+e.methodName {
				candidates = append(candidates, f)
			}
		}
		if len(candidates) > 1 {
			var sameArity []javaAPIFunction
			for _, f := range candidates {
				if f.paramCount == e.paramCount {
					sameArity = append(sameArity, f)
				}
			}
			candidates = sameArity
		}
		if len(candidates) != 1 {
			continue
		}
		target := candidates[0]
		path := rebaseJavaEndpointPath(e.path, ifaceBases[e.iface], methodMappings[e.functionID], class.basePaths)
		rewire := javaAPIRewire{endpointID: e.id, handlerID: target.id, path: path}
		if existing[fmt.Sprintf("%s|%s|%d", e.method, path, target.id)] {
			rewire.duplicate = true
		}
		if e.handlerID != nil && *e.handlerID == target.id && path == e.path {
			continue // already attributed
		}
		out = append(out, rewire)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].endpointID < out[j].endpointID })
	return out
}

// rebaseJavaEndpointPath places an interface endpoint under the controller's own
// class-level base path when the controller declares exactly one, replacing the
// interface's base as Spring does.
//
// The result is computed from the endpoint's original path (the interface's class
// mapping joined with the method mapping), never from the stored path alone: a
// stored row may already carry an earlier run's rebase, and rebasing that again
// would stack bases whenever the controller's base changes between runs. With no
// controller base (or several) the original path is restored; when the method
// mapping is unavailable or does not explain the stored path it is left alone.
func rebaseJavaEndpointPath(path string, ifaceBases []string, mapping javaAPIMethodMapping, implBases []string) string {
	original, rel, originalKnown, ok := originalJavaEndpointPath(path, ifaceBases, mapping)
	if !ok {
		return path
	}
	if len(implBases) == 1 {
		implBase := strings.Trim(implBases[0], "/")
		return "/" + strings.Trim(implBase+"/"+strings.Trim(rel, "/"), "/")
	}
	if originalKnown {
		return original
	}
	return path
}

// originalJavaEndpointPath recovers an interface endpoint's un-rebased path and
// its method-relative part from the stored path. The stored path is either an
// original (an interface base joined with one of the method's mapping paths) or
// the result of an earlier rebase, which ends in one of the method's mapping
// paths. originalKnown is false when several interface bases leave the original
// of a rebased row ambiguous; ok is false when the stored path fits neither.
func originalJavaEndpointPath(path string, ifaceBases []string, mapping javaAPIMethodMapping) (original, rel string, originalKnown, ok bool) {
	if !mapping.known {
		return "", "", false, false
	}
	bases := ifaceBases
	if len(bases) == 0 {
		bases = []string{""}
	}
	methodPaths := mapping.paths
	if len(methodPaths) == 0 {
		methodPaths = []string{""}
	}
	key := javaPathKey(path)
	for _, base := range bases {
		for _, m := range methodPaths {
			if javaPathKey(javaJoinPaths(base, m)) == key {
				return path, m, true, true
			}
		}
	}
	// An earlier rebase: controller base + method path. Prefer the longest
	// matching method path; a method with no path of its own ends at the base.
	best, empty := -1, false
	for i, m := range methodPaths {
		suffix := javaPathKey(m)
		if suffix == "/" {
			empty = true
			continue
		}
		if strings.HasSuffix(key, suffix) && (best < 0 || len(suffix) > len(javaPathKey(methodPaths[best]))) {
			best = i
		}
	}
	switch {
	case best >= 0:
		rel = methodPaths[best]
	case empty:
		rel = ""
	default:
		return "", "", false, false
	}
	if len(bases) == 1 {
		return javaJoinPaths(bases[0], rel), rel, true, true
	}
	return "", rel, false, true
}

// javaJoinPaths joins a class-level base and a method path the way the Java
// parser does when it records an endpoint ("/" when both are empty).
func javaJoinPaths(base, sub string) string {
	norm := func(p string) string {
		p = strings.TrimSpace(p)
		if p != "" && !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		return p
	}
	base, sub = norm(base), norm(sub)
	joined := sub
	switch {
	case base == "":
	case sub == "":
		joined = base
	default:
		joined = strings.TrimRight(base, "/") + sub
	}
	if joined == "" {
		return "/"
	}
	return joined
}

// javaPathKey compares route paths ignoring repeated and trailing slashes.
func javaPathKey(p string) string {
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '/' })
	return "/" + strings.Join(parts, "/")
}

var javaMethodMappingAnnotations = map[string]bool{
	"GetMapping": true, "PostMapping": true, "PutMapping": true, "DeleteMapping": true,
	"PatchMapping": true, "RequestMapping": true, "Path": true,
	"GET": true, "POST": true, "PUT": true, "DELETE": true, "PATCH": true,
}

// loadJavaAPIMethodMappings reads the mapping annotations of the interface
// methods that carry endpoints, so each endpoint's original (un-rebased) path can
// be recovered from the interface's own class and method mappings.
func loadJavaAPIMethodMappings(ctx *repoFinalizeContext, endpoints []javaAPIEndpoint) (map[int64]javaAPIMethodMapping, error) {
	out := map[int64]javaAPIMethodMapping{}
	seen := map[int64]bool{}
	var ids []int64
	for _, e := range endpoints {
		if !seen[e.functionID] {
			seen[e.functionID] = true
			ids = append(ids, e.functionID)
		}
	}
	rows, err := ctx.storage.Pool().Query(context.Background(), `
		SELECT entity_id, name, COALESCE(values, 'null'::jsonb) FROM annotations
		WHERE entity_type = 'method' AND entity_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		var raw []byte
		if err := rows.Scan(&id, &name, &raw); err != nil {
			return nil, err
		}
		if !javaMethodMappingAnnotations[name] {
			continue
		}
		mapping := out[id]
		mapping.known = true
		mapping.paths = append(mapping.paths, javaSplitMappingPaths(javaAnnotationPaths(raw))...)
		out[id] = mapping
	}
	return out, rows.Err()
}

// javaSplitMappingPaths mirrors the parser's annotation path handling: quotes
// are stripped and "{a, b}" or "a, b" lists are split.
func javaSplitMappingPaths(values []string) []string {
	var out []string
	for _, value := range values {
		value = strings.Trim(strings.TrimSpace(value), `"`)
		if strings.HasPrefix(value, "{") && strings.HasSuffix(value, "}") {
			value = value[1 : len(value)-1]
		}
		for _, part := range strings.Split(value, ",") {
			if part = strings.Trim(strings.TrimSpace(part), `"`); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}
