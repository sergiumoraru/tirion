package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	stdpath "path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sergiumoraru/tirion/internal/buildinfo"
	"github.com/sergiumoraru/tirion/internal/config"
	"github.com/sergiumoraru/tirion/internal/graph"
	"github.com/sergiumoraru/tirion/internal/indexer"
	"github.com/sergiumoraru/tirion/internal/parser"
	"github.com/sergiumoraru/tirion/internal/runtimeconfig"
	"github.com/sergiumoraru/tirion/internal/sourceindex"
	"github.com/sergiumoraru/tirion/internal/sourcepath"
	"gopkg.in/yaml.v3"
)

type functionMeta struct {
	id        int64
	startLine int
	endLine   int
}

type dbRunner interface {
	Query(context.Context, string, ...interface{}) (pgx.Rows, error)
}

const bulkInsertChunkSize = 200

var javaParamAnnotationRe = regexp.MustCompile(`@\w+(?:\([^)]*\))?`)
var identityWhitespaceRe = regexp.MustCompile(`\s+`)
var identitySlashRe = regexp.MustCompile(`/+`)
var snapshotPathPrefixRe = regexp.MustCompile(`^\.codebase-snapshots/[^/]+/`)

var repositoryBaseNames = map[string]bool{
	"jparepository":              true,
	"crudrepository":             true,
	"pagingandsortingrepository": true,
	"repository":                 true,
}

type sqlAccess struct {
	Line   int
	Entity string
	Access string
}

type myBatisXMLAccess struct {
	Namespace string
	Method    string
	Entity    string
	Access    string
	Line      int
}

type stringLiteral struct {
	Text      string
	StartLine int
}

var (
	sqlInsertRe = regexp.MustCompile(`(?i)\binsert\s+into\s+(?:only\s+)?([^\s,()]+)`)
	sqlUpdateRe = regexp.MustCompile(`(?i)\bupdate\s+(?:only\s+)?([^\s,()]+)`)
	sqlDeleteRe = regexp.MustCompile(`(?i)\bdelete\s+from\s+(?:only\s+)?([^\s,()]+)`)
	sqlFromRe   = regexp.MustCompile(`(?i)\bfrom\s+(?:only\s+)?([^\s,()]+)`)
	sqlJoinRe   = regexp.MustCompile(`(?i)\bjoin\s+(?:only\s+)?([^\s,()]+)`)
)

var (
	myBatisNamespaceRe = regexp.MustCompile(`(?i)<mapper[^>]*\snamespace\s*=\s*['"]([^'"]+)['"]`)
	sqlMapNamespaceRe  = regexp.MustCompile(`(?i)<sqlMap[^>]*\snamespace\s*=\s*['"]([^'"]+)['"]`)
	myBatisSelectRe    = regexp.MustCompile(`(?is)<select\b([^>]*)>(.*?)</select>`)
	myBatisInsertRe    = regexp.MustCompile(`(?is)<insert\b([^>]*)>(.*?)</insert>`)
	myBatisUpdateRe    = regexp.MustCompile(`(?is)<update\b([^>]*)>(.*?)</update>`)
	myBatisDeleteRe    = regexp.MustCompile(`(?is)<delete\b([^>]*)>(.*?)</delete>`)
	myBatisStatementRe = regexp.MustCompile(`(?is)<statement\b([^>]*)>(.*?)</statement>`)
	myBatisProcedureRe = regexp.MustCompile(`(?is)<procedure\b([^>]*)>(.*?)</procedure>`)
	myBatisSqlRe       = regexp.MustCompile(`(?is)<sql\b([^>]*)>(.*?)</sql>`)
	myBatisIncludeRe   = regexp.MustCompile(`(?is)<include\b[^>]*\srefid\s*=\s*['"]([^'"]+)['"][^>]*\/?>\s*(?:</include>)?`)
	myBatisIdRe        = regexp.MustCompile(`(?i)\bid\s*=\s*['"]([^'"]+)['"]`)
	xmlTagRe           = regexp.MustCompile(`(?s)<[^>]+>`)
	javaPackageRe      = regexp.MustCompile(`(?m)^\s*package\s+([\w\.]+)\s*;`)
	javaTypeDeclRe     = regexp.MustCompile(`(?m)^\s*(?:public\s+|protected\s+|private\s+)?(?:abstract\s+|final\s+)?(class|interface|enum|record)\s+([A-Za-z0-9_]+)`)
	sqlStringStartRe   = regexp.MustCompile(`(?is)"{1,3}\s*(select|insert|update|delete|with|call)\b`)
)

func declaredWorkspaceRootDirs(root string) map[string]bool {
	out := map[string]bool{}
	addPattern := func(pattern string) {
		if dir := workspacePatternRootDir(pattern); dir != "" {
			out[dir] = true
		}
	}
	readJSONPatterns := func(path string, key string) {
		data, err := sourceindex.ReadCurrent(filepath.Dir(path), filepath.Base(path))
		if err != nil {
			return
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(data, &obj) != nil {
			return
		}
		raw, ok := obj[key]
		if !ok {
			return
		}
		var patterns []string
		if json.Unmarshal(raw, &patterns) == nil {
			for _, pattern := range patterns {
				addPattern(pattern)
			}
			return
		}
		var nested map[string]json.RawMessage
		if json.Unmarshal(raw, &nested) != nil {
			return
		}
		if packagesRaw, ok := nested["packages"]; ok {
			if json.Unmarshal(packagesRaw, &patterns) == nil {
				for _, pattern := range patterns {
					addPattern(pattern)
				}
			}
		}
	}
	readJSONPatterns(filepath.Join(root, "package.json"), "workspaces")
	readJSONPatterns(filepath.Join(root, "lerna.json"), "packages")
	readYAMLWorkspacePatterns(filepath.Join(root, "pnpm-workspace.yaml"), addPattern)
	return out
}

func readYAMLWorkspacePatterns(path string, add func(string)) {
	data, err := sourceindex.ReadCurrent(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return
	}
	var workspace struct {
		Packages []string `yaml:"packages"`
	}
	if yaml.Unmarshal(data, &workspace) != nil {
		return
	}
	for _, pattern := range workspace.Packages {
		add(pattern)
	}
}

func workspacePatternRootDir(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || strings.HasPrefix(pattern, "!") {
		return ""
	}
	pattern = strings.Trim(pattern, `"'`)
	pattern = strings.TrimPrefix(pattern, "./")
	pattern = filepath.ToSlash(pattern)
	pattern = strings.Trim(pattern, "/")
	if pattern == "" {
		return ""
	}
	first := strings.Split(pattern, "/")[0]
	if first == "" || strings.ContainsAny(first, "*?[]{}") {
		return ""
	}
	return first
}

func findScheduledMethodID(functionMetas map[string][]functionMeta, className, methodName string, line int) *int64 {
	if className != "" && methodName != "" {
		full := className + "." + methodName
		if metas, ok := functionMetas[full]; ok {
			if id, ok := selectFunctionIDByLine(metas, line); ok && id != 0 {
				return &id
			}
		}
	}
	if methodName != "" {
		if metas, ok := functionMetas[methodName]; ok {
			if id, ok := selectFunctionIDByLine(metas, line); ok && id != 0 {
				return &id
			}
		}
	}
	return nil
}

func normalizeJavaType(raw string) string {
	t := strings.TrimSpace(raw)
	if t == "" {
		return ""
	}
	if idx := strings.Index(t, "<"); idx >= 0 {
		t = t[:idx]
	}
	t = strings.TrimSuffix(t, "[]")
	t = strings.TrimSuffix(t, "...")
	t = strings.TrimSpace(t)
	if t == "" {
		return ""
	}
	if idx := strings.LastIndex(t, "."); idx >= 0 {
		t = t[idx+1:]
	}
	return t
}

func parseRepositoryEntity(typeSpec string) (string, bool) {
	typeSpec = strings.TrimSpace(typeSpec)
	if typeSpec == "" {
		return "", false
	}
	base, args := splitGenericType(typeSpec)
	if base == "" || len(args) == 0 {
		return "", false
	}
	baseName := strings.ToLower(baseNameFromType(base))
	if !repositoryBaseNames[baseName] {
		return "", false
	}
	entity := strings.TrimSpace(args[0])
	entity = strings.TrimPrefix(entity, "?")
	entity = strings.TrimSpace(strings.TrimPrefix(entity, "extends"))
	entity = strings.TrimSpace(strings.TrimPrefix(entity, "super"))
	entity = normalizeJavaType(entity)
	if entity == "" {
		return "", false
	}
	return entity, true
}

func splitGenericType(typeSpec string) (string, []string) {
	open := strings.Index(typeSpec, "<")
	if open < 0 {
		return strings.TrimSpace(typeSpec), nil
	}
	base := strings.TrimSpace(typeSpec[:open])
	inner := strings.TrimSuffix(typeSpec[open+1:], ">")
	args := splitGenericArgs(inner)
	return base, args
}

func splitGenericArgs(inner string) []string {
	var args []string
	var current strings.Builder
	depth := 0
	for _, r := range inner {
		switch r {
		case '<':
			depth++
			current.WriteRune(r)
		case '>':
			depth--
			current.WriteRune(r)
		case ',':
			if depth == 0 {
				args = append(args, strings.TrimSpace(current.String()))
				current.Reset()
			} else {
				current.WriteRune(r)
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		args = append(args, strings.TrimSpace(current.String()))
	}
	return args
}

func baseNameFromType(typeName string) string {
	typeName = strings.TrimSpace(typeName)
	if idx := strings.LastIndex(typeName, "."); idx >= 0 {
		return typeName[idx+1:]
	}
	return typeName
}

func parseJavaParam(param string) (string, string) {
	clean := javaParamAnnotationRe.ReplaceAllString(param, "")
	clean = strings.ReplaceAll(clean, "final ", "")
	clean = strings.TrimSpace(clean)
	if clean == "" {
		return "", ""
	}
	parts := strings.Fields(clean)
	if len(parts) < 2 {
		return "", ""
	}
	name := parts[len(parts)-1]
	typ := strings.Join(parts[:len(parts)-1], " ")
	return name, typ
}

func buildReceiverTypeIndex(result parser.ParsedFile) map[string]map[string]string {
	classFields := make(map[string]map[string]string)
	for _, cls := range result.Classes {
		if cls.Name == "" {
			continue
		}
		fieldMap := make(map[string]string)
		for _, field := range cls.Fields {
			if field.Name == "" || field.FieldType == "" {
				continue
			}
			if base := normalizeJavaType(field.FieldType); base != "" {
				fieldMap[field.Name] = base
			}
		}
		if len(fieldMap) > 0 {
			classFields[cls.Name] = fieldMap
		}
	}

	receiverTypes := make(map[string]map[string]string)
	for _, fn := range result.Functions {
		if fn.Name == "" {
			continue
		}
		className := classNameFromFunctionName(fn.Name)
		local := make(map[string]string)
		if fields := classFields[className]; len(fields) > 0 {
			for name, typ := range fields {
				local[name] = typ
			}
		}
		for i, name := range fn.Params {
			if i >= len(fn.ParamTypes) {
				continue
			}
			name = strings.TrimSpace(name)
			typ := strings.TrimSpace(fn.ParamTypes[i])
			if name == "" || typ == "" {
				continue
			}
			if base := normalizeJavaType(typ); base != "" {
				local[name] = base
			}
		}
		for _, param := range fn.Params {
			name, typ := parseJavaParam(param)
			if name == "" || typ == "" {
				continue
			}
			if base := normalizeJavaType(typ); base != "" {
				local[name] = base
			}
		}
		if locals := result.LocalVarTypes[fn.Name]; len(locals) > 0 {
			for name, typ := range locals {
				if name == "" || typ == "" {
					continue
				}
				if base := normalizeJavaType(typ); base != "" {
					local[name] = base
				}
			}
		}
		if len(local) > 0 {
			receiverTypes[fn.Name] = local
		}
	}
	return receiverTypes
}

func addFunctionMeta(m map[string][]functionMeta, name string, id int64, startLine, endLine int) {
	m[name] = append(m[name], functionMeta{id: id, startLine: startLine, endLine: endLine})
}

func selectFunctionIDByLine(metas []functionMeta, line int) (int64, bool) {
	var selected *functionMeta
	ambiguous := false
	for _, meta := range metas {
		if meta.id == 0 || line < meta.startLine || line > meta.endLine {
			continue
		}
		span := meta.endLine - meta.startLine
		if selected == nil || span < selected.endLine-selected.startLine {
			candidate := meta
			selected = &candidate
			ambiguous = false
		} else if span == selected.endLine-selected.startLine && meta.id != selected.id {
			ambiguous = true
		}
	}
	if selected != nil {
		return selected.id, !ambiguous
	}
	if len(metas) == 1 && metas[0].id != 0 {
		return metas[0].id, true
	}
	return 0, false
}

func findHandlerFunctionID(functionMetas map[string][]functionMeta, handlerName string, line int) *int64 {
	if handlerName == "" {
		return nil
	}
	if metas, ok := functionMetas[handlerName]; ok {
		if id, ok := selectFunctionIDByLine(metas, line); ok {
			return &id
		}
	}

	simple := handlerName
	if idx := strings.LastIndex(handlerName, "."); idx != -1 {
		simple = handlerName[idx+1:]
	}
	if metas, ok := functionMetas[simple]; ok {
		if id, ok := selectFunctionIDByLine(metas, line); ok {
			return &id
		}
	}

	var candidates []functionMeta
	for name, metas := range functionMetas {
		if strings.HasSuffix(name, "."+simple) {
			candidates = append(candidates, metas...)
		}
	}
	if id, ok := selectFunctionIDByLine(candidates, line); ok {
		return &id
	}
	return nil
}

func classNameFromFunctionName(name string) string {
	if i := strings.LastIndex(name, "."); i > 0 {
		return name[:i]
	}
	return ""
}

func simpleFunctionName(name string) string {
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		return name[idx+1:]
	}
	return name
}

func normalizeIdentityWhitespace(raw string) string {
	return strings.TrimSpace(identityWhitespaceRe.ReplaceAllString(strings.TrimSpace(raw), " "))
}

func normalizeFilePathIdentity(raw string) string {
	p := strings.ReplaceAll(strings.TrimSpace(raw), `\`, `/`)
	p = filepath.ToSlash(p)
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "/")
	p = identitySlashRe.ReplaceAllString(p, "/")
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return ""
	}
	clean := strings.TrimPrefix(stdpath.Clean("/"+p), "/")
	if clean == "." {
		return ""
	}
	return clean
}

func normalizeFilePathCanonical(raw string) string {
	p := strings.ToLower(normalizeFilePathIdentity(raw))
	if p == "" {
		return ""
	}
	return snapshotPathPrefixRe.ReplaceAllString(p, "")
}

func normalizeFunctionNameIdentity(raw string) string {
	name := normalizeIdentityWhitespace(raw)
	name = strings.Trim(name, ".")
	return name
}

func normalizeSignatureToken(raw string) string {
	token := normalizeIdentityWhitespace(raw)
	if token == "" {
		return ""
	}
	if strings.Contains(token, " ") && !strings.Contains(token, ":") {
		if _, typ := parseJavaParam(token); typ != "" {
			token = typ
		}
	}
	if idx := strings.Index(token, "="); idx >= 0 {
		token = token[:idx]
	}
	token = strings.TrimPrefix(strings.TrimSpace(token), "...")
	if idx := strings.Index(token, ":"); idx >= 0 {
		left := strings.TrimSpace(token[:idx])
		right := strings.TrimSpace(token[idx+1:])
		if right != "" {
			token = right
		} else if left != "" {
			token = left
		}
	}
	token = strings.TrimSuffix(token, "?")
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if _, typ := parseJavaParam(token); typ != "" {
		token = typ
	}
	token = strings.ReplaceAll(token, " ", "")
	return strings.TrimSpace(token)
}

func normalizeFunctionSignatureIdentity(name string, params []string, returnType string) string {
	nameCanonical := normalizeFunctionNameIdentity(name)
	if nameCanonical == "" {
		nameCanonical = strings.TrimSpace(name)
	}
	normalizedParams := make([]string, 0, len(params))
	for _, param := range params {
		token := normalizeSignatureToken(param)
		if token == "" {
			token = "?"
		}
		normalizedParams = append(normalizedParams, token)
	}
	sig := nameCanonical + "(" + strings.Join(normalizedParams, ",") + ")"
	if ret := normalizeSignatureToken(returnType); ret != "" {
		sig += "->" + ret
	}
	return sig
}

func normalizeEndpointMethodIdentity(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}

func normalizeEndpointPathIdentity(raw string) string {
	path := strings.TrimSpace(filepath.ToSlash(raw))
	if path == "" {
		return "/"
	}
	path = identitySlashRe.ReplaceAllString(path, "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if path != "/" {
		path = strings.TrimRight(path, "/")
		if path == "" {
			path = "/"
		}
	}
	return strings.ToLower(path)
}

type functionInsertDedup struct {
	Ord                int
	Function           parser.ParsedFunction
	NameCanonical      string
	SignatureCanonical string
}

func functionInsertIdentity(fn parser.ParsedFunction) (key, nameCanonical, signatureCanonical string) {
	nameCanonical = normalizeFunctionNameIdentity(fn.Name)
	if nameCanonical == "" {
		nameCanonical = strings.TrimSpace(fn.Name)
	}
	signatureCanonical = normalizeFunctionSignatureIdentity(fn.Name, fn.Params, fn.ReturnType)
	key = fmt.Sprintf("%s|%d|%d", nameCanonical, fn.StartLine, fn.EndLine)
	return key, nameCanonical, signatureCanonical
}

func mergeParsedFunctionIdentity(existing, candidate parser.ParsedFunction) parser.ParsedFunction {
	out := existing
	if strings.TrimSpace(out.Name) == "" && strings.TrimSpace(candidate.Name) != "" {
		out.Name = candidate.Name
	}
	if len(out.Params) == 0 && len(candidate.Params) > 0 {
		out.Params = append([]string(nil), candidate.Params...)
	}
	if out.ReturnType == "" && candidate.ReturnType != "" {
		out.ReturnType = candidate.ReturnType
	}
	if out.SourceCode == "" || len(strings.TrimSpace(candidate.SourceCode)) > len(strings.TrimSpace(out.SourceCode)) {
		out.SourceCode = candidate.SourceCode
	}
	out.IsExported = out.IsExported || candidate.IsExported
	out.IsAsync = out.IsAsync || candidate.IsAsync
	if out.StartLine == 0 && candidate.StartLine != 0 {
		out.StartLine = candidate.StartLine
	}
	if out.EndLine == 0 && candidate.EndLine != 0 {
		out.EndLine = candidate.EndLine
	}
	return out
}

func dedupeFunctionInsertInputs(funcs []parser.ParsedFunction) ([]functionInsertDedup, []int) {
	if len(funcs) == 0 {
		return nil, nil
	}
	records := make([]functionInsertDedup, 0, len(funcs))
	ordAlias := make([]int, len(funcs))
	indexByKey := make(map[string]int, len(funcs))
	for ord, fn := range funcs {
		key, nameCanonical, signatureCanonical := functionInsertIdentity(fn)
		if idx, ok := indexByKey[key]; ok {
			record := records[idx]
			record.Function = mergeParsedFunctionIdentity(record.Function, fn)
			record.NameCanonical = normalizeFunctionNameIdentity(record.Function.Name)
			if record.NameCanonical == "" {
				record.NameCanonical = strings.TrimSpace(record.Function.Name)
			}
			record.SignatureCanonical = normalizeFunctionSignatureIdentity(record.Function.Name, record.Function.Params, record.Function.ReturnType)
			records[idx] = record
			ordAlias[ord] = record.Ord
			continue
		}
		record := functionInsertDedup{
			Ord:                ord,
			Function:           fn,
			NameCanonical:      nameCanonical,
			SignatureCanonical: signatureCanonical,
		}
		indexByKey[key] = len(records)
		records = append(records, record)
		ordAlias[ord] = ord
	}
	return records, ordAlias
}

func resolveCallee(functionMetas map[string][]functionMeta, callerFunc string, call parser.ParsedFunctionCall, receiverTypes map[string]string, language ...string) (string, *int64) {
	methodName := call.MethodName
	receiver := call.Receiver

	if methodName == "" && call.CalleeName != "" {
		parts := strings.Split(call.CalleeName, ".")
		methodName = parts[len(parts)-1]
		if receiver == "" && len(parts) > 1 {
			receiver = strings.Join(parts[:len(parts)-1], ".")
		}
	}

	candidateName := ""
	lexicalCalls := len(language) > 0 && (language[0] == "javascript" || language[0] == "typescript")
	if methodName != "" {
		switch receiver {
		case "", "this", "super":
			// JavaScript bare calls are lexical, not methods on the owner of a
			// dotted (possibly synthetic callback) function identity.
			if className := classNameFromFunctionName(callerFunc); className != "" && !lexicalCalls {
				candidateName = className + "." + methodName
			}
		default:
			if receiverTypes != nil {
				if receiverType := receiverTypes[receiver]; receiverType != "" {
					candidateName = receiverType + "." + methodName
				}
			}
		}
	}

	if len(language) > 0 && language[0] == "java" {
		name := candidateName
		if name == "" {
			name = call.CalleeName
		}
		if scoped := scopedJavaFunctionName(functionMetas, callerFunc, name); scoped != "" {
			id := functionMetas[scoped][0].id
			return scoped, &id
		}
	}

	if candidateName != "" {
		if metas, ok := functionMetas[candidateName]; ok && len(metas) == 1 {
			id := metas[0].id
			return candidateName, &id
		}
		return candidateName, nil
	}

	if metas, ok := functionMetas[call.CalleeName]; ok && len(metas) == 1 {
		id := metas[0].id
		return call.CalleeName, &id
	}

	return call.CalleeName, nil
}

func stripNUL(value string) string {
	if strings.IndexByte(value, 0) < 0 {
		return value
	}
	return strings.ReplaceAll(value, "\x00", "")
}

func hashContent(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func insertFunctionsBulk(ctx context.Context, db dbRunner, fileID int64, funcs []parser.ParsedFunction) ([]int64, error) {
	ids := make([]int64, len(funcs))
	if len(funcs) == 0 {
		return ids, nil
	}
	records, ordAlias := dedupeFunctionInsertInputs(funcs)
	if len(records) == 0 {
		return ids, nil
	}

	for start := 0; start < len(records); start += bulkInsertChunkSize {
		end := start + bulkInsertChunkSize
		if end > len(records) {
			end = len(records)
		}

		var values []string
		args := make([]interface{}, 0, (end-start)*13)
		argPos := 1

		for i := start; i < end; i++ {
			rec := records[i]
			fn := rec.Function
			ord := rec.Ord
			var paramsJSON interface{}
			if len(fn.Params) > 0 {
				if b, err := json.Marshal(fn.Params); err == nil {
					paramsJSON = b
				}
			}
			var returnType interface{}
			if fn.ReturnType != "" {
				returnType = fn.ReturnType
			}

			values = append(values, fmt.Sprintf("($%d::integer,$%d::integer,$%d::text,$%d::text,$%d::text,$%d::text,$%d::integer,$%d::integer,$%d::jsonb,$%d::text,$%d::boolean,$%d::boolean,$%d::text)",
				argPos, argPos+1, argPos+2, argPos+3, argPos+4,
				argPos+5, argPos+6, argPos+7, argPos+8, argPos+9, argPos+10, argPos+11, argPos+12))
			args = append(args,
				ord,
				fileID,
				fn.Name,
				rec.NameCanonical,
				rec.SignatureCanonical,
				simpleFunctionName(fn.Name),
				fn.StartLine,
				fn.EndLine,
				paramsJSON,
				returnType,
				fn.IsExported,
				fn.IsAsync,
				stripNUL(fn.SourceCode), // PostgreSQL text rejects NUL; content is normalized upstream
			)
			argPos += 13
		}

		query := fmt.Sprintf(`
				WITH data(ord, file_id, name, name_canonical, signature_canonical, simple_name, start_line, end_line, params, return_type, is_exported, is_async, source_code) AS (
					VALUES %s
				), upserted AS (
					INSERT INTO functions (file_id, name, name_canonical, signature_canonical, simple_name, start_line, end_line, params, return_type, is_exported, is_async, source_code)
					SELECT file_id, name, name_canonical, signature_canonical, simple_name, start_line, end_line, params, return_type, is_exported, is_async, source_code
					FROM data
					ORDER BY ord
					ON CONFLICT (file_id, name_canonical, start_line, end_line) DO UPDATE SET
						name = EXCLUDED.name,
						signature_canonical = EXCLUDED.signature_canonical,
						simple_name = EXCLUDED.simple_name,
						params = EXCLUDED.params,
						return_type = EXCLUDED.return_type,
						is_exported = EXCLUDED.is_exported,
						is_async = EXCLUDED.is_async,
						source_code = EXCLUDED.source_code
					RETURNING file_id, name_canonical, start_line, end_line, id
				)
				SELECT d.ord, u.id
				FROM data d
				JOIN upserted u
				  ON u.file_id = d.file_id
				 AND u.name_canonical = d.name_canonical
				 AND u.start_line = d.start_line
				 AND u.end_line = d.end_line
				ORDER BY d.ord
			`, strings.Join(values, ","))

		rows, err := db.Query(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ord int64
			var id int64
			if err := rows.Scan(&ord, &id); err != nil {
				rows.Close()
				return nil, err
			}
			if ord >= 0 && int(ord) < len(ids) {
				ids[int(ord)] = id
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	for _, record := range records {
		if ids[record.Ord] <= 0 {
			return nil, fmt.Errorf("function insert returned no ID for ordinal %d", record.Ord)
		}
	}
	for ord, aliasOrd := range ordAlias {
		if ord == aliasOrd {
			continue
		}
		if aliasOrd >= 0 && aliasOrd < len(ids) {
			ids[ord] = ids[aliasOrd]
		}
	}

	return ids, nil
}

func insertClassesBulk(ctx context.Context, db dbRunner, fileID int64, classes []parser.ParsedClass) ([]int64, error) {
	ids := make([]int64, len(classes))
	if len(classes) == 0 {
		return ids, nil
	}

	for start := 0; start < len(classes); start += bulkInsertChunkSize {
		end := start + bulkInsertChunkSize
		if end > len(classes) {
			end = len(classes)
		}

		var values []string
		args := make([]interface{}, 0, (end-start)*11)
		argPos := 1

		for i := start; i < end; i++ {
			cls := classes[i]
			ord := i
			var extendsClass interface{}
			if cls.ExtendsClass != "" {
				extendsClass = cls.ExtendsClass
			}
			var implementsJSON interface{}
			if len(cls.Implements) > 0 {
				if b, err := json.Marshal(cls.Implements); err == nil {
					implementsJSON = b
				}
			}

			values = append(values, fmt.Sprintf("($%d::integer,$%d::integer,$%d::text,$%d::integer,$%d::integer,$%d::text,$%d::jsonb,$%d::boolean,$%d::boolean,$%d::boolean,$%d::boolean)",
				argPos, argPos+1, argPos+2, argPos+3, argPos+4, argPos+5, argPos+6, argPos+7, argPos+8, argPos+9, argPos+10))
			args = append(args,
				ord,
				fileID,
				cls.Name,
				cls.StartLine,
				cls.EndLine,
				extendsClass,
				implementsJSON,
				cls.IsExported,
				cls.IsEnum,
				cls.IsAbstract,
				cls.IsRecord,
			)
			argPos += 11
		}

		query := fmt.Sprintf(`
			WITH data(ord, file_id, name, start_line, end_line, extends_class, implements, is_exported, is_enum, is_abstract, is_record) AS (
				VALUES %s
			)
			INSERT INTO classes (file_id, name, start_line, end_line, extends_class, implements, is_exported, is_enum, is_abstract, is_record)
			SELECT file_id, name, start_line, end_line, extends_class, implements, is_exported, is_enum, is_abstract, is_record
			FROM data
			ORDER BY ord
			RETURNING id
		`, strings.Join(values, ","))

		rows, err := db.Query(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		insertPos := start
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			if insertPos >= 0 && insertPos < len(ids) {
				ids[insertPos] = id
				insertPos++
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if insertPos != end {
			return nil, fmt.Errorf("class insert returned %d IDs for %d inputs", insertPos-start, end-start)
		}
	}

	return ids, nil
}

func insertInterfacesBulk(ctx context.Context, db dbRunner, fileID int64, interfaces []parser.ParsedInterface) ([]int64, error) {
	// A file can contain multiple declarations of the same interface. Preserve
	// every declaration's member writes, but share the schema's file/name identity.
	positions := make([]int, len(interfaces))
	unique := parser.MergeInterfaceDeclarations(interfaces)
	byName := make(map[string]int, len(unique))
	for i, iface := range unique {
		byName[iface.Name] = i
	}
	for i, iface := range interfaces {
		positions[i] = byName[iface.Name]
	}
	interfaces = unique
	ids := make([]int64, len(interfaces))
	if len(interfaces) == 0 {
		return ids, nil
	}

	for start := 0; start < len(interfaces); start += bulkInsertChunkSize {
		end := start + bulkInsertChunkSize
		if end > len(interfaces) {
			end = len(interfaces)
		}

		var values []string
		args := make([]interface{}, 0, (end-start)*8)
		argPos := 1

		for i := start; i < end; i++ {
			iface := interfaces[i]
			ord := i
			var extendsJSON interface{}
			if len(iface.ExtendsInterfaces) > 0 {
				if b, err := json.Marshal(iface.ExtendsInterfaces); err == nil {
					extendsJSON = b
				}
			}

			values = append(values, fmt.Sprintf("($%d::integer,$%d::integer,$%d::text,$%d::integer,$%d::integer,$%d::jsonb,$%d::boolean,$%d::boolean)",
				argPos, argPos+1, argPos+2, argPos+3, argPos+4, argPos+5, argPos+6, argPos+7))
			args = append(args,
				ord,
				fileID,
				iface.Name,
				iface.StartLine,
				iface.EndLine,
				extendsJSON,
				iface.IsFunctional,
				iface.IsExported,
			)
			argPos += 8
		}

		query := fmt.Sprintf(`
			WITH data(ord, file_id, name, start_line, end_line, extends_interfaces, is_functional, is_exported) AS (
				VALUES %s
			)
			INSERT INTO interfaces (file_id, name, start_line, end_line, extends_interfaces, is_functional, is_exported)
			SELECT file_id, name, start_line, end_line, extends_interfaces, is_functional, is_exported
			FROM data
			ORDER BY ord
			RETURNING id
		`, strings.Join(values, ","))

		rows, err := db.Query(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		insertPos := start
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			if insertPos >= 0 && insertPos < len(ids) {
				ids[insertPos] = id
				insertPos++
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if insertPos != end {
			return nil, fmt.Errorf("interface insert returned %d IDs for %d inputs", insertPos-start, end-start)
		}
	}

	declarationIDs := make([]int64, len(positions))
	for i, position := range positions {
		declarationIDs[i] = ids[position]
	}
	return declarationIDs, nil
}

func insertFileTx(ctx context.Context, tx pgx.Tx, repoID, snapshotID int64, path string, language, hash, javaPackage *string) (int64, error) {
	var id int64
	pathCanonical := normalizeFilePathCanonical(path)
	if pathCanonical == "" {
		pathCanonical = strings.ToLower(normalizeFilePathIdentity(path))
	}
	err := tx.QueryRow(ctx, `
		INSERT INTO files (repo_id, snapshot_id, path, path_canonical, language, hash, java_package)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT(snapshot_id, path) WHERE snapshot_id IS NOT NULL DO UPDATE SET
			path_canonical = EXCLUDED.path_canonical,
			language = EXCLUDED.language,
			hash = EXCLUDED.hash,
			java_package = EXCLUDED.java_package,
			updated_at = CURRENT_TIMESTAMP
		RETURNING id
	`, repoID, snapshotID, path, pathCanonical, language, hash, javaPackage).Scan(&id)
	return id, err
}

func queueInsertImport(batch *pgx.Batch, fileID int64, importPath string, importsFileID *int64, importNames []string, isDefault bool) {
	var namesJSON []byte
	if len(importNames) > 0 {
		namesJSON, _ = json.Marshal(importNames)
	}
	batch.Queue(`
		INSERT INTO file_imports (file_id, imports_file_id, import_path, import_names, is_default_import)
		VALUES ($1, $2, $3, $4, $5)
	`, fileID, importsFileID, importPath, namesJSON, isDefault)
}

func queueInsertEndpoint(batch *pgx.Batch, repoID int64, path, method string, handlerID, fileID *int64, lineNumber *int) {
	pathCanonical := normalizeEndpointPathIdentity(path)
	methodCanonical := normalizeEndpointMethodIdentity(method)
	batch.Queue(`
		INSERT INTO endpoints (repo_id, path, path_canonical, method, method_canonical, handler_function_id, file_id, line_number)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT DO NOTHING
	`, repoID, path, pathCanonical, method, methodCanonical, handlerID, fileID, lineNumber)
}

func queueInsertFunctionCall(batch *pgx.Batch, callerID int64, calleeName string, calleeID *int64, lineNumber int, isAsync bool, unresolvedReason string, callbackArgument bool) {
	var calleeParam interface{}
	var resolutionSource interface{}
	var resolutionConfidence interface{}
	var unresolvedParam interface{}
	if calleeID != nil {
		calleeParam = *calleeID
		resolutionSource = "parser"
		resolutionConfidence = "high"
	} else if unresolvedReason != "" {
		unresolvedParam = unresolvedReason
	}
	if callbackArgument {
		resolutionSource = "callback_argument"
		resolutionConfidence = "low"
	}
	batch.Queue(`
		INSERT INTO function_calls (caller_function_id, callee_function_id, callee_name, line_number, is_async, callee_resolution_source, callee_resolution_confidence, unresolved_reason, is_callback_argument)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, callerID, calleeParam, calleeName, lineNumber, isAsync, resolutionSource, resolutionConfidence, unresolvedParam, callbackArgument)
}

func classifyUnresolvedCall(call parser.ParsedFunctionCall, calleeName string) string {
	if strings.TrimSpace(calleeName) == "" {
		return ""
	}
	receiver := strings.TrimSpace(call.Receiver)
	method := strings.TrimSpace(call.MethodName)
	if method == "" {
		method = lastQualifiedSegment(calleeName)
	}
	lowerReceiver := strings.ToLower(receiver)
	lowerMethod := strings.ToLower(method)
	lowerCallee := strings.ToLower(calleeName)

	if isStdlibCall(lowerReceiver, lowerMethod, lowerCallee) {
		if isLikelyCSharpStdlib(lowerReceiver, lowerCallee) {
			return "stdlib_csharp"
		}
		return "stdlib_js"
	}
	if isFrameworkCall(lowerReceiver, lowerMethod, lowerCallee) {
		return "framework"
	}
	if isDynamicReceiver(receiver) {
		return "dynamic_receiver"
	}
	return "unknown"
}

func lastQualifiedSegment(name string) string {
	parts := strings.Split(strings.TrimSpace(name), ".")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[len(parts)-1])
}

func isStdlibCall(receiver, method, callee string) bool {
	if receiver == "" {
		switch method {
		case "parseint", "parsefloat", "isnan", "isfinite", "encodeuri", "encodeuricomponent", "decodeuri", "decodeuricomponent", "settimeout", "cleartimeout", "setinterval", "clearinterval":
			return true
		}
	}
	stdlibReceivers := map[string]bool{
		"array": true, "array.prototype": true, "boolean": true, "console": true,
		"date": true, "document": true, "error": true, "json": true, "math": true,
		"number": true, "object": true, "promise": true, "reflect": true,
		"regexp": true, "string": true, "string.prototype": true, "symbol": true,
		"window": true, "process": true, "buffer": true, "path": true, "fs": true,
		"system": true, "system.string": true, "system.linq.enumerable": true,
		"enumerable": true, "convert": true, "datetime": true, "guid": true,
		"regex": true, "task": true,
	}
	if stdlibReceivers[receiver] {
		return true
	}
	return strings.HasPrefix(callee, "system.") ||
		strings.HasPrefix(callee, "microsoft.") ||
		strings.HasPrefix(callee, "string.") ||
		strings.HasPrefix(callee, "datetime.") ||
		strings.HasPrefix(callee, "guid.") ||
		strings.HasPrefix(callee, "console.")
}

func isLikelyCSharpStdlib(receiver, callee string) bool {
	return strings.HasPrefix(callee, "system.") ||
		strings.HasPrefix(callee, "microsoft.") ||
		strings.HasPrefix(callee, "string.") ||
		strings.HasPrefix(callee, "datetime.") ||
		strings.HasPrefix(callee, "guid.") ||
		receiver == "system" ||
		strings.HasPrefix(receiver, "system.") ||
		receiver == "enumerable" ||
		strings.HasPrefix(receiver, "system.linq.")
}

func isFrameworkCall(receiver, method, callee string) bool {
	if receiver == "$" || strings.HasPrefix(receiver, "$.") || strings.HasPrefix(receiver, "jquery") {
		return true
	}
	if strings.HasPrefix(receiver, "this.$") || strings.HasPrefix(callee, "this.$") {
		return true
	}
	if strings.Contains(receiver, "axios") || strings.Contains(callee, "axios.") {
		return true
	}
	switch receiver {
	case "vue", "vuex", "pinia", "router", "route":
		return true
	}
	return strings.HasPrefix(method, "use") && (strings.Contains(callee, "route") || strings.Contains(callee, "router") || strings.Contains(callee, "store"))
}

func isDynamicReceiver(receiver string) bool {
	if receiver == "" {
		return false
	}
	return strings.ContainsAny(receiver, "()[].{}") ||
		strings.Contains(receiver, "=>") ||
		strings.HasPrefix(strings.TrimSpace(receiver), "new ")
}

func appendUniqueSnapshotID(ids []int64, id int64) []int64 {
	if id <= 0 {
		return ids
	}
	for _, existing := range ids {
		if existing == id {
			return ids
		}
	}
	return append(ids, id)
}

func queueInsertImplementation(batch *pgx.Batch, classID int64, interfaceName string, interfaceID *int64) {
	batch.Queue(`
		INSERT INTO implementations (class_id, interface_name, interface_id)
		VALUES ($1, $2, $3)
		ON CONFLICT(class_id, interface_name) DO UPDATE SET
			interface_id = EXCLUDED.interface_id
	`, classID, interfaceName, interfaceID)
}

func queueInsertField(batch *pgx.Batch, classID, interfaceID *int64, name, fieldType string, typeParams, modifiers []string, startLine int, isInjected bool, injectionType string) {
	var typeParamsJSON, modifiersJSON []byte
	if len(typeParams) > 0 {
		typeParamsJSON, _ = json.Marshal(typeParams)
	}
	if len(modifiers) > 0 {
		modifiersJSON, _ = json.Marshal(modifiers)
	}
	batch.Queue(`
		INSERT INTO fields (class_id, interface_id, name, field_type, type_parameters, modifiers, start_line, is_injected, injection_type)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, classID, interfaceID, name, fieldType, typeParamsJSON, modifiersJSON, startLine, isInjected, injectionType)
}

func queueInsertAnnotation(batch *pgx.Batch, entityType string, entityID int64, name string, values map[string]interface{}, lineNumber int) {
	var valuesJSON []byte
	if len(values) > 0 {
		valuesJSON, _ = json.Marshal(values)
	}
	batch.Queue(`
		INSERT INTO annotations (entity_type, entity_id, name, values, line_number)
		VALUES ($1, $2, $3, $4, $5)
	`, entityType, entityID, name, valuesJSON, lineNumber)
}

func queueInsertMethodSignature(batch *pgx.Batch, functionID int64, signature string, parameterTypes []string, returnType string, isOverride bool) {
	var paramTypesJSON []byte
	if len(parameterTypes) > 0 {
		paramTypesJSON, _ = json.Marshal(parameterTypes)
	}
	batch.Queue(`
		INSERT INTO method_signatures (function_id, signature, parameter_types, return_type, is_override)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT(function_id) DO UPDATE SET
			signature = EXCLUDED.signature,
			parameter_types = EXCLUDED.parameter_types,
			return_type = EXCLUDED.return_type,
			is_override = EXCLUDED.is_override
	`, functionID, signature, paramTypesJSON, returnType, isOverride)
}

func queueInsertConstructorParam(batch *pgx.Batch, classID int64, paramName, paramType string, paramIndex int, isInjected bool, annotation, annotationValue string, lineNumber int) {
	batch.Queue(`
		INSERT INTO constructor_params (class_id, param_name, param_type, param_index, is_injected, annotation, annotation_value, line_number)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, classID, paramName, paramType, paramIndex, isInjected, annotation, annotationValue, lineNumber)
}

func queueInsertTypeParameter(batch *pgx.Batch, entityType string, entityID int64, paramName string, paramIndex int, bounds []string, boundType string) {
	var boundsJSON []byte
	if len(bounds) > 0 {
		boundsJSON, _ = json.Marshal(bounds)
	}
	batch.Queue(`
		INSERT INTO type_parameters (entity_type, entity_id, param_name, param_index, bounds, bound_type)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, entityType, entityID, paramName, paramIndex, boundsJSON, boundType)
}

func queueInsertHttpInterfaceMethod(batch *pgx.Batch, interfaceID int64, methodName string, functionID *int64, httpMethod, urlPattern string, lineNumber int) {
	batch.Queue(`
		INSERT INTO http_interface_methods (interface_id, method_name, function_id, http_method, url_pattern, line_number)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT(interface_id, method_name) DO UPDATE SET
			function_id = EXCLUDED.function_id,
			http_method = EXCLUDED.http_method,
			url_pattern = EXCLUDED.url_pattern,
			line_number = EXCLUDED.line_number
	`, interfaceID, methodName, functionID, httpMethod, urlPattern, lineNumber)
}

func queueInsertRepositoryEntity(batch *pgx.Batch, repoID int64, snapshotID *int64, fileID int64, repoName, entityName string, lineNumber int) {
	batch.Queue(`
		INSERT INTO repository_entities (repo_id, snapshot_id, file_id, repository_name, entity_name, line_number)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT DO NOTHING
	`, repoID, snapshotID, fileID, repoName, entityName, lineNumber)
}

func queueInsertDataAccess(batch *pgx.Batch, repoID int64, snapshotID *int64, callerID, entityName, access string, lineNumber int) {
	batch.Queue(`
		INSERT INTO data_accesses (repo_id, snapshot_id, caller_id, entity_name, access, line_number)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT DO NOTHING
	`, repoID, snapshotID, callerID, entityName, access, lineNumber)
}

func queueInsertGraphQLOperationBatch(batch *pgx.Batch, repoID, fileID int64, operations []parser.ParsedGraphQLOperation) {
	for _, operation := range operations {
		if operation.Name == "" || operation.OperationType == "" {
			continue
		}
		batch.Queue(`
			INSERT INTO graphql_operations (repo_id, file_id, operation_name, operation_type, line_number)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (repo_id, file_id, operation_name, operation_type, line_number) DO NOTHING
		`, repoID, fileID, operation.Name, operation.OperationType, operation.LineNumber)
	}
}

func queueInsertGraphQLOperationUsageBatch(batch *pgx.Batch, repoID, fileID int64, usages []parser.ParsedGraphQLOperationUsage) {
	for _, usage := range usages {
		if usage.ImportPath == "" || usage.ImportedAs == "" {
			continue
		}
		batch.Queue(`
			INSERT INTO graphql_operation_usages (repo_id, file_id, import_path, imported_as, caller_function, line_number)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (repo_id, file_id, import_path, imported_as, caller_function, line_number) DO NOTHING
		`, repoID, fileID, usage.ImportPath, usage.ImportedAs, usage.FunctionName, usage.LineNumber)
	}
}

func queueInsertGraphQLBackendEntrypointBatch(batch *pgx.Batch, repoID, fileID int64, entrypoints []parser.ParsedGraphQLBackendEntrypoint) {
	for _, entrypoint := range entrypoints {
		if entrypoint.RegistrationKind == "" {
			continue
		}
		batch.Queue(`
			INSERT INTO graphql_backend_entrypoints (repo_id, file_id, handler_name, registration_kind, controllers_path, line_number)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (repo_id, file_id, handler_name, registration_kind, controllers_path, line_number) DO NOTHING
		`, repoID, fileID, normalizedIdentityString(entrypoint.HandlerName), entrypoint.RegistrationKind, normalizedIdentityString(entrypoint.ControllersPath), entrypoint.LineNumber)
	}
}

func queueInsertGraphQLOperationResolverBatch(batch *pgx.Batch, repoID, fileID int64, resolvers []parser.ParsedGraphQLOperationResolver) {
	for _, resolver := range resolvers {
		if resolver.OperationName == "" || resolver.OperationType == "" || resolver.ResolverName == "" {
			continue
		}
		batch.Queue(`
			INSERT INTO graphql_operation_resolvers (repo_id, file_id, operation_name, operation_type, resolver_name, line_number)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT DO NOTHING
		`, repoID, fileID, normalizedIdentityString(resolver.OperationName), normalizedIdentityString(resolver.OperationType), normalizedIdentityString(resolver.ResolverName), resolver.LineNumber)
	}
}

func queueInsertGraphQLOperationPermissionBatch(batch *pgx.Batch, repoID, fileID int64, permissions []parser.ParsedGraphQLOperationPermission) {
	for _, permission := range permissions {
		if permission.OperationName == "" || permission.OperationType == "" || permission.RuleExpression == "" {
			continue
		}
		batch.Queue(`
			INSERT INTO graphql_operation_permissions (repo_id, file_id, operation_name, operation_type, rule_expression, line_number)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT DO NOTHING
		`, repoID, fileID, normalizedIdentityString(permission.OperationName), normalizedIdentityString(permission.OperationType), normalizedIdentityString(permission.RuleExpression), permission.LineNumber)
	}
}

func queueInsertResourceAliasBatch(batch *pgx.Batch, repoID int64, snapshotID *int64, fileID int64, aliases []parser.ParsedResourceAlias) {
	for _, alias := range aliases {
		if strings.TrimSpace(alias.Alias) == "" || strings.TrimSpace(alias.Value) == "" {
			continue
		}
		batch.Queue(`
			INSERT INTO resource_aliases (repo_id, snapshot_id, file_id, alias_key, alias_value, alias_kind, line_number)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT DO NOTHING
		`, repoID, nullableInt64Ptr(snapshotID), fileID, normalizedIdentityString(alias.Alias), normalizedIdentityString(alias.Value), normalizedIdentityString(resourceAliasKind(alias.Kind)), alias.LineNumber)
	}
}

func resourceAliasKind(kind string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return "config"
	}
	return kind
}

func queueInsertAzureHostConfigBatch(batch *pgx.Batch, repoID, fileID int64, configs []parser.ParsedAzureHostConfig) {
	for _, cfg := range configs {
		batch.Queue(`
			INSERT INTO azure_host_configs (repo_id, file_id, route_prefix, line_number)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (repo_id, file_id) DO UPDATE SET
				route_prefix = EXCLUDED.route_prefix,
				line_number = EXCLUDED.line_number
		`, repoID, fileID, normalizedIdentityString(cfg.RoutePrefix), cfg.LineNumber)
	}
}

func queueInsertAzureTriggerBatch(batch *pgx.Batch, repoID, fileID int64, triggers []parser.ParsedAzureTrigger) {
	for _, trigger := range triggers {
		if trigger.FunctionName == "" || trigger.TriggerType == "" {
			continue
		}
		var methodsJSON []byte
		if len(trigger.Methods) > 0 {
			methodsJSON, _ = json.Marshal(trigger.Methods)
		}
		batch.Queue(`
			INSERT INTO azure_function_triggers (
				repo_id, file_id, function_name, trigger_type, direction, binding_name, route, http_methods,
				auth_level, connection_name, schedule_expression, resource_name, script_file, line_number
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			ON CONFLICT DO NOTHING
		`, repoID, fileID, trigger.FunctionName, trigger.TriggerType, nullIfEmpty(trigger.Direction), normalizedIdentityString(trigger.BindingName), nullIfEmpty(trigger.Route), methodsJSON, nullIfEmpty(trigger.AuthLevel), nullIfEmpty(trigger.Connection), nullIfEmpty(trigger.Schedule), normalizedIdentityString(trigger.ResourceName), nullIfEmpty(trigger.ScriptFile), trigger.LineNumber)
	}
}

func queueInsertGatewayRouteBatch(batch *pgx.Batch, repoID, fileID int64, routes []parser.ParsedGatewayRoute) {
	for _, route := range routes {
		if route.GatewayType == "" || route.PublicPath == "" {
			continue
		}
		batch.Queue(`
			INSERT INTO gateway_routes (
				repo_id, file_id, gateway_type, api_name, operation_name, public_method, public_path,
				backend_method, backend_url, backend_path, backend_id, line_number
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (repo_id, file_id, gateway_type, api_name, operation_name, public_method, public_path) DO UPDATE SET
				backend_method = EXCLUDED.backend_method,
				backend_url = EXCLUDED.backend_url,
				backend_path = EXCLUDED.backend_path,
				backend_id = EXCLUDED.backend_id,
				line_number = EXCLUDED.line_number
		`, repoID, fileID, route.GatewayType, normalizedIdentityString(route.APIName), normalizedIdentityString(route.OperationName),
			normalizedIdentityString(route.PublicMethod), normalizedIdentityString(route.PublicPath), normalizedIdentityString(route.BackendMethod),
			strings.TrimSpace(route.BackendURL), normalizedIdentityString(route.BackendPath), normalizedIdentityString(route.BackendID), route.LineNumber)
	}
}

func queueInsertTypeAliasBatch(batch *pgx.Batch, fileID int64, aliases []parser.ParsedTypeAlias) {
	for _, alias := range aliases {
		if strings.TrimSpace(alias.Name) == "" {
			continue
		}
		var typeParamsJSON []byte
		if len(alias.TypeParams) > 0 {
			typeParamsJSON, _ = json.Marshal(alias.TypeParams)
		}
		batch.Queue(`
			INSERT INTO type_aliases (file_id, name, definition, type_params, is_exported, start_line)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (file_id, name) DO UPDATE SET
				definition = EXCLUDED.definition,
				type_params = EXCLUDED.type_params,
				is_exported = EXCLUDED.is_exported,
				start_line = EXCLUDED.start_line
		`, fileID, alias.Name, nullableString(alias.Definition), typeParamsJSON, alias.IsExported, alias.StartLine)
	}
}

func queueInsertHookCallBatch(batch *pgx.Batch, fileID int64, hooks []parser.ParsedHookCall) {
	for _, hook := range hooks {
		if strings.TrimSpace(hook.HookName) == "" {
			continue
		}
		var depsJSON []byte
		if len(hook.Dependencies) > 0 {
			depsJSON, _ = json.Marshal(hook.Dependencies)
		}
		batch.Queue(`
			INSERT INTO hook_calls (file_id, function_name, hook_name, dependencies, initial_value, line_number, is_custom, origin)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (file_id, hook_name, COALESCE(function_name, ''), COALESCE(line_number, 0)) DO UPDATE SET
				dependencies = EXCLUDED.dependencies,
				initial_value = EXCLUDED.initial_value,
				is_custom = EXCLUDED.is_custom,
				origin = EXCLUDED.origin
		`, fileID, nullIfEmpty(hook.FunctionName), hook.HookName, depsJSON, nullIfEmpty(hook.InitialValue), hook.LineNumber, hook.IsCustomHook, hookOriginForInsert(hook))
	}
}

func hookOriginForInsert(hook parser.ParsedHookCall) string {
	if strings.TrimSpace(hook.Origin) != "" {
		return strings.TrimSpace(hook.Origin)
	}
	if hook.IsCustomHook {
		return "custom"
	}
	return "framework"
}

func nullIfEmpty(value string) interface{} {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

func normalizedIdentityString(value string) string {
	return strings.TrimSpace(value)
}

func queueInsertScheduledMethod(batch *pgx.Batch, classID int64, methodID *int64, methodName, cron string, fixedRate, fixedDelay, initialDelay int64, lineNumber int) {
	if classID == 0 || methodName == "" {
		return
	}
	var cronPtr, fixedRatePtr, fixedDelayPtr, initialDelayPtr interface{}
	if cron != "" {
		cronPtr = cron
	}
	if fixedRate > 0 {
		fixedRatePtr = fixedRate
	}
	if fixedDelay > 0 {
		fixedDelayPtr = fixedDelay
	}
	if initialDelay > 0 {
		initialDelayPtr = initialDelay
	}
	batch.Queue(`
		INSERT INTO scheduled_methods (class_id, method_id, method_name, cron, fixed_rate, fixed_delay, initial_delay, line_number)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT DO NOTHING
	`, classID, methodID, methodName, cronPtr, fixedRatePtr, fixedDelayPtr, initialDelayPtr, lineNumber)
}

func queueInsertJpaEntity(batch *pgx.Batch, classID int64, tableName, schemaName, catalog string) {
	batch.Queue(`
		INSERT INTO jpa_entities (class_id, table_name, schema_name, catalog)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (class_id) DO UPDATE SET
			table_name = EXCLUDED.table_name,
			schema_name = EXCLUDED.schema_name,
			catalog = EXCLUDED.catalog
	`, classID, tableName, nullableString(schemaName), nullableString(catalog))
}

func queueInsertJpaRelationship(batch *pgx.Batch, sourceClassID int64, targetEntity string, targetClassID *int64, relationType, sourceField, mappedBy, joinColumn, fetchType string, cascadeTypes []string, lineNumber int) {
	var cascadeJSON []byte
	if len(cascadeTypes) > 0 {
		cascadeJSON, _ = json.Marshal(cascadeTypes)
	}
	batch.Queue(`
		INSERT INTO jpa_relationships (source_class_id, target_entity_name, target_class_id, relation_type, source_field, mapped_by, join_column, fetch_type, cascade_types, line_number)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, sourceClassID, targetEntity, targetClassID, relationType, sourceField,
		nullableString(mappedBy), nullableString(joinColumn), nullableString(fetchType), cascadeJSON, lineNumber)
}

func queueInsertEnumConstant(batch *pgx.Batch, classID int64, constant parser.ParsedEnumConstant) {
	if classID == 0 || strings.TrimSpace(constant.Name) == "" {
		return
	}
	var argsJSON []byte
	if len(constant.Arguments) > 0 {
		argsJSON, _ = json.Marshal(constant.Arguments)
	}
	batch.Queue(`
		INSERT INTO enum_constants (class_id, name, ordinal, arguments)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (class_id, name) DO UPDATE SET
			ordinal = EXCLUDED.ordinal,
			arguments = EXCLUDED.arguments
	`, classID, constant.Name, constant.Ordinal, argsJSON)
}

func extractJavaStringLiterals(content string) []stringLiteral {
	var literals []stringLiteral
	line := 1
	for i := 0; i < len(content); i++ {
		ch := content[i]
		if ch == '\n' {
			line++
			continue
		}
		if ch != '"' {
			continue
		}
		if i+2 < len(content) && content[i+1] == '"' && content[i+2] == '"' {
			startLine := line
			i += 3
			var buf strings.Builder
			for i < len(content) {
				if content[i] == '\n' {
					line++
				}
				if content[i] == '"' && i+2 < len(content) && content[i+1] == '"' && content[i+2] == '"' {
					literals = append(literals, stringLiteral{Text: buf.String(), StartLine: startLine})
					i += 2
					break
				}
				buf.WriteByte(content[i])
				i++
			}
			continue
		}
		startLine := line
		var buf strings.Builder
		i++
		for i < len(content) {
			if content[i] == '\n' {
				line++
			}
			if content[i] == '\\' {
				if i+1 < len(content) {
					buf.WriteByte(content[i])
					i++
					buf.WriteByte(content[i])
					i++
					continue
				}
			}
			if content[i] == '"' {
				literals = append(literals, stringLiteral{Text: buf.String(), StartLine: startLine})
				break
			}
			buf.WriteByte(content[i])
			i++
		}
	}
	return literals
}

func normalizeSQLTable(raw string) string {
	value := strings.TrimSpace(raw)
	value = strings.Trim(value, ",;)")
	if idx := strings.Index(value, "::"); idx >= 0 {
		value = value[:idx]
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if idx := strings.LastIndex(value, "."); idx >= 0 {
		value = value[idx+1:]
	}
	value = stripSQLIdentifierDelimiters(strings.TrimSpace(value))
	return strings.TrimSpace(value)
}

func stripSQLIdentifierDelimiters(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		first := value[0]
		last := value[len(value)-1]
		if (first == '`' && last == '`') || (first == '"' && last == '"') || (first == '[' && last == ']') {
			return strings.TrimSpace(value[1 : len(value)-1])
		}
	}
	return strings.Trim(value, "`\"[]")
}

func singularizeName(name string) string {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, "ies") && len(name) > 3 {
		return name[:len(name)-3] + "y"
	}
	if strings.HasSuffix(lower, "s") && len(name) > 1 && !strings.HasSuffix(lower, "ss") {
		return name[:len(name)-1]
	}
	return name
}

func toCamelCase(name string) string {
	if name == "" {
		return ""
	}
	if !strings.ContainsAny(name, "_-") {
		// SQL table names may be uppercase without separators.
		// Normalize those to TitleCase to match JPA/entity naming across repos.
		if name == strings.ToUpper(name) {
			lower := strings.ToLower(name)
			return strings.ToUpper(lower[:1]) + lower[1:]
		}
		return strings.ToUpper(name[:1]) + name[1:]
	}
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return r == '_' || r == '-'
	})
	var out strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		part = strings.ToLower(part)
		out.WriteString(strings.ToUpper(part[:1]))
		if len(part) > 1 {
			out.WriteString(part[1:])
		}
	}
	return out.String()
}

func tableNameToEntity(table string) string {
	table = normalizeSQLTable(table)
	if table == "" {
		return ""
	}
	table = singularizeName(table)
	return toCamelCase(table)
}

func annotationStringValues(ann parser.ParsedAnnotation) []string {
	raw, ok := ann.Values["value"]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	case []interface{}:
		var out []string
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func myBatisAccessFromAnnotation(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "select", "selectprovider", "selectkey":
		return "read"
	case "insert", "update", "delete", "insertprovider", "updateprovider", "deleteprovider":
		return "write"
	default:
		return ""
	}
}

func extractSQLTablesForAccess(sql string, access string) []string {
	// Performance + noise control: we only track the *primary* table per statement
	// (first FROM / first INSERT/UPDATE/DELETE). This keeps `data_accesses` small
	// and avoids exploding flows with incidental JOIN tables.
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return nil
	}
	find := func(re *regexp.Regexp) string {
		match := re.FindStringSubmatch(sql)
		if len(match) < 2 {
			return ""
		}
		return normalizeSQLTable(match[1])
	}
	switch access {
	case "read":
		// Prefer FROM; fall back to JOIN if a statement is missing FROM (rare).
		if table := find(sqlFromRe); table != "" {
			return []string{table}
		}
		if table := find(sqlJoinRe); table != "" {
			return []string{table}
		}
		return nil
	case "write":
		if table := find(sqlInsertRe); table != "" {
			return []string{table}
		}
		if table := find(sqlUpdateRe); table != "" {
			return []string{table}
		}
		if table := find(sqlDeleteRe); table != "" {
			return []string{table}
		}
		return nil
	default:
		return nil
	}
}

func extractMyBatisAccesses(annotations []parser.ParsedAnnotation) []sqlAccess {
	var accesses []sqlAccess
	seen := make(map[string]bool)
	for _, ann := range annotations {
		access := myBatisAccessFromAnnotation(ann.Name)
		if access == "" || strings.HasSuffix(strings.ToLower(strings.TrimSpace(ann.Name)), "provider") {
			continue
		}
		parts := annotationStringValues(ann)
		if len(parts) == 0 {
			continue
		}
		sql := strings.Join(parts, " ")
		for _, table := range extractSQLTablesForAccess(sql, access) {
			entity := tableNameToEntity(table)
			if entity == "" {
				continue
			}
			key := strings.ToLower(entity) + "|" + access
			if seen[key] {
				continue
			}
			seen[key] = true
			accesses = append(accesses, sqlAccess{Entity: entity, Access: access})
		}
	}
	return accesses
}

func extractSpringDataQueryAccesses(annotations []parser.ParsedAnnotation) []sqlAccess {
	var accesses []sqlAccess
	seen := make(map[string]bool)

	for _, ann := range annotations {
		if strings.ToLower(strings.TrimSpace(ann.Name)) != "query" {
			continue
		}
		raw, ok := ann.Values["value"]
		if !ok {
			continue
		}

		var sqlText string
		if s, ok := raw.(string); ok {
			sqlText = strings.TrimSpace(s)
		} else {
			sqlText = strings.TrimSpace(fmt.Sprintf("%v", raw))
		}
		if sqlText == "" {
			continue
		}

		// @Query values are sometimes a concatenation expression ("a" + "b" + ...). When that's the case,
		// stitch the string literals together so table inference works.
		if strings.Contains(sqlText, "\"") && strings.Contains(sqlText, "+") {
			lits := extractJavaStringLiterals(sqlText)
			if len(lits) > 0 {
				var parts []string
				for _, lit := range lits {
					if strings.TrimSpace(lit.Text) != "" {
						parts = append(parts, lit.Text)
					}
				}
				if len(parts) > 0 {
					sqlText = strings.Join(parts, " ")
				}
			}
		}

		access := sqlAccessKind(sqlText)
		for _, table := range extractSQLTablesForAccess(sqlText, access) {
			entity := tableNameToEntity(table)
			if entity == "" {
				continue
			}
			key := strings.ToLower(entity) + "|" + access
			if seen[key] {
				continue
			}
			seen[key] = true
			accesses = append(accesses, sqlAccess{Line: ann.LineNumber, Entity: entity, Access: access})
		}
	}

	return accesses
}

// A provider may be a member of the mapper or one of its enclosing types.
// Stop at the first lexical match; overloaded/ambiguous methods are not guessed.
func scopedJavaFunctionName(functions map[string][]functionMeta, caller, name string) string {
	for scope := classNameFromFunctionName(caller); ; scope = classNameFromFunctionName(scope) {
		candidate := name
		if scope != "" {
			candidate = scope + "." + name
		}
		if methods := functions[candidate]; len(methods) > 0 {
			if len(methods) == 1 {
				return candidate
			}
			return ""
		}
		if scope == "" {
			return ""
		}
	}
}

func extractMyBatisProviderBinding(annotations []parser.ParsedAnnotation) (providerFunc string, access string, ok bool) {
	for _, ann := range annotations {
		lowerName := strings.ToLower(strings.TrimSpace(ann.Name))
		if !strings.HasSuffix(lowerName, "provider") {
			continue
		}
		access = myBatisAccessFromAnnotation(ann.Name)
		if access == "" {
			continue
		}
		rawType, hasType := ann.Values["type"]
		rawMethod, hasMethod := ann.Values["method"]
		if !hasType || !hasMethod {
			continue
		}
		typeText := strings.TrimSpace(fmt.Sprintf("%v", rawType))
		methodText := strings.TrimSpace(fmt.Sprintf("%v", rawMethod))
		if typeText == "" || methodText == "" {
			continue
		}
		// Provider is specified as Foo.class (or package.Foo.class). Use the simple name.
		typeText = strings.TrimSuffix(typeText, ".class")
		if idx := strings.LastIndex(typeText, "."); idx >= 0 && idx < len(typeText)-1 {
			typeText = typeText[idx+1:]
		}
		if typeText == "" {
			continue
		}
		return typeText + "." + methodText, access, true
	}
	return "", "", false
}

func looksLikeSQLFragment(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "select") ||
		strings.Contains(lower, " from ") ||
		strings.Contains(lower, "join ") ||
		strings.Contains(lower, "where ") ||
		strings.Contains(lower, "insert") ||
		strings.Contains(lower, " into ") ||
		strings.Contains(lower, "update") ||
		strings.Contains(lower, " delete") ||
		strings.Contains(lower, " set ")
}

func extractProviderSQLFromLiterals(literals []stringLiteral, startLine, endLine int) string {
	if startLine <= 0 || endLine <= 0 || endLine < startLine || len(literals) == 0 {
		return ""
	}
	var parts []string
	for _, lit := range literals {
		if lit.StartLine < startLine || lit.StartLine > endLine {
			continue
		}
		if !looksLikeSQLFragment(lit.Text) {
			continue
		}
		trimmed := strings.TrimSpace(lit.Text)
		if trimmed == "" {
			continue
		}
		parts = append(parts, trimmed)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

func sqlAccessKind(stmt string) string {
	lowerStmt := strings.ToLower(strings.TrimSpace(stmt))
	if lowerStmt == "" {
		return "read"
	}
	switch {
	case strings.HasPrefix(lowerStmt, "insert") ||
		strings.HasPrefix(lowerStmt, "update") ||
		strings.HasPrefix(lowerStmt, "delete") ||
		strings.HasPrefix(lowerStmt, "call"):
		return "write"
	case strings.HasPrefix(lowerStmt, "select") || strings.HasPrefix(lowerStmt, "with"):
		return "read"
	case strings.Contains(lowerStmt, " insert ") ||
		strings.Contains(lowerStmt, " update ") ||
		strings.Contains(lowerStmt, " delete ") ||
		strings.Contains(lowerStmt, " call "):
		return "write"
	default:
		return "read"
	}
}

func extractSQLAccesses(content []byte) ([]sqlAccess, []stringLiteral) {
	// Most Java files don't contain SQL. Avoid scanning all string literals unless we see a string
	// that plausibly starts an SQL statement (including Java text blocks).
	if !sqlStringStartRe.Match(content) {
		return nil, nil
	}

	text := string(content)
	literals := extractJavaStringLiterals(text)
	if len(literals) == 0 {
		return nil, nil
	}

	var accesses []sqlAccess
	seen := make(map[string]bool)
	addAccess := func(line int, table string, access string) {
		entity := tableNameToEntity(table)
		if entity == "" || access == "" {
			return
		}
		key := fmt.Sprintf("%s|%s|%d", strings.ToLower(entity), access, line)
		if seen[key] {
			return
		}
		seen[key] = true
		accesses = append(accesses, sqlAccess{Line: line, Entity: entity, Access: access})
	}

	for _, lit := range literals {
		if !looksLikeSQL(lit.Text) {
			continue
		}
		// Avoid misclassifying write statements (DELETE/UPDATE/INSERT) as reads just because they contain
		// "FROM"/"JOIN". This also reduces regex work by only running the relevant matchers.
		kind := sqlAccessKind(lit.Text)

		applyMatches := func(re *regexp.Regexp, access string) {
			matches := re.FindAllStringSubmatchIndex(lit.Text, -1)
			for _, match := range matches {
				if len(match) < 4 {
					continue
				}
				start := match[2]
				end := match[3]
				if start < 0 || end > len(lit.Text) || start >= end {
					continue
				}
				table := lit.Text[start:end]
				line := lit.StartLine + strings.Count(lit.Text[:start], "\n")
				addAccess(line, table, access)
			}
		}
		if kind == "write" {
			applyMatches(sqlInsertRe, "write")
			applyMatches(sqlUpdateRe, "write")
			applyMatches(sqlDeleteRe, "write")
		} else {
			applyMatches(sqlFromRe, "read")
			applyMatches(sqlJoinRe, "read")
		}
	}
	return accesses, literals
}

func registerJavaPathIndex(index map[string]string, counts map[string]int, relPath string) {
	suffix := relPath
	for _, marker := range []string{"src/main/java/", "src/test/java/", "src/main/kotlin/", "src/test/kotlin/"} {
		if idx := strings.Index(relPath, marker); idx >= 0 {
			suffix = relPath[idx+len(marker):]
			break
		}
	}
	suffix = strings.TrimPrefix(suffix, string(os.PathSeparator))
	counts[suffix]++
	if counts[suffix] == 1 {
		index[suffix] = relPath
	} else {
		index[suffix] = ""
	}
}

func resolveNamespacePathFromIndex(namespace string, index map[string]string) string {
	if namespace == "" {
		return ""
	}
	suffix := strings.ReplaceAll(namespace, ".", string(os.PathSeparator)) + ".java"
	if rel, ok := index[suffix]; ok && rel != "" {
		return rel
	}
	return ""
}

func extractMyBatisXMLAccesses(content []byte) []myBatisXMLAccess {
	text := string(content)
	if !strings.Contains(text, "<mapper") && !strings.Contains(text, "<sqlMap") && !strings.Contains(text, "<sqlmap") {
		return nil
	}
	namespaceMatch := myBatisNamespaceRe.FindStringSubmatch(text)
	if len(namespaceMatch) < 2 {
		namespaceMatch = sqlMapNamespaceRe.FindStringSubmatch(text)
		if len(namespaceMatch) < 2 {
			return nil
		}
	}
	namespace := strings.TrimSpace(namespaceMatch[1])
	if namespace == "" {
		return nil
	}

	fragments := extractMyBatisSQLFragments(text)

	var accesses []myBatisXMLAccess
	seen := make(map[string]bool)
	type stmtSpec struct {
		re                *regexp.Regexp
		access            string
		fallbackNamespace bool
	}
	stmts := []stmtSpec{
		{re: myBatisSelectRe, access: "read"},
		{re: myBatisInsertRe, access: "write"},
		{re: myBatisUpdateRe, access: "write"},
		{re: myBatisDeleteRe, access: "write"},
		// Legacy iBATIS <statement> can be read or write. Infer access from SQL text.
		{re: myBatisStatementRe},
		// iBATIS procedure blocks typically call stored procedures, so table inference is unavailable.
		// Fall back to the sqlMap namespace as the entity.
		{re: myBatisProcedureRe, access: "write", fallbackNamespace: true},
	}
	for _, stmt := range stmts {
		matches := stmt.re.FindAllStringSubmatchIndex(text, -1)
		for _, match := range matches {
			if len(match) < 6 {
				continue
			}
			attrText := text[match[2]:match[3]]
			body := text[match[4]:match[5]]

			idMatch := myBatisIdRe.FindStringSubmatch(attrText)
			if len(idMatch) < 2 {
				continue
			}
			method := strings.TrimSpace(idMatch[1])
			if method == "" {
				continue
			}

			expanded := expandMyBatisIncludes(body, fragments)
			clean := xmlTagRe.ReplaceAllString(expanded, " ")

			accessKinds := []string{stmt.access}
			if stmt.access == "" {
				// Access not known upfront (e.g. <statement>): infer from SQL body.
				hasWrite := len(extractSQLTablesForAccess(clean, "write")) > 0
				hasRead := len(extractSQLTablesForAccess(clean, "read")) > 0
				accessKinds = nil
				if hasWrite {
					accessKinds = append(accessKinds, "write")
				}
				if hasRead {
					accessKinds = append(accessKinds, "read")
				}
				if len(accessKinds) == 0 {
					accessKinds = []string{"read"}
				}
			}

			for _, accessKind := range accessKinds {
				tables := extractSQLTablesForAccess(clean, accessKind)
				if len(tables) == 0 && stmt.fallbackNamespace {
					entity := tableNameToEntity(simpleNameFromNamespace(namespace))
					if entity == "" {
						continue
					}
					key := namespace + "|" + method + "|" + strings.ToLower(entity) + "|" + accessKind
					if !seen[key] {
						seen[key] = true
						line := 1 + strings.Count(text[:match[0]], "\n")
						accesses = append(accesses, myBatisXMLAccess{
							Namespace: namespace,
							Method:    method,
							Entity:    entity,
							Access:    accessKind,
							Line:      line,
						})
					}
					continue
				}

				for _, table := range tables {
					entity := tableNameToEntity(table)
					if entity == "" {
						continue
					}
					key := namespace + "|" + method + "|" + strings.ToLower(entity) + "|" + accessKind
					if seen[key] {
						continue
					}
					seen[key] = true
					line := 1 + strings.Count(text[:match[0]], "\n")
					accesses = append(accesses, myBatisXMLAccess{
						Namespace: namespace,
						Method:    method,
						Entity:    entity,
						Access:    accessKind,
						Line:      line,
					})
				}
			}
		}
	}

	return accesses
}

func extractMyBatisSQLFragments(text string) map[string]string {
	if !strings.Contains(text, "<sql") {
		return nil
	}
	fragments := make(map[string]string)
	matches := myBatisSqlRe.FindAllStringSubmatchIndex(text, -1)
	for _, match := range matches {
		if len(match) < 6 {
			continue
		}
		attrText := text[match[2]:match[3]]
		body := text[match[4]:match[5]]
		idMatch := myBatisIdRe.FindStringSubmatch(attrText)
		if len(idMatch) < 2 {
			continue
		}
		id := strings.TrimSpace(idMatch[1])
		if id == "" {
			continue
		}
		fragments[id] = body
	}
	if len(fragments) == 0 {
		return nil
	}
	return fragments
}

func myBatisFragmentKey(refid string) string {
	refid = strings.TrimSpace(refid)
	if refid == "" {
		return ""
	}
	// MyBatis allows namespace-qualified fragment refs. We only support in-file fragments, so use the
	// suffix if a namespace is present.
	if idx := strings.LastIndex(refid, "."); idx >= 0 && idx < len(refid)-1 {
		refid = refid[idx+1:]
	}
	return refid
}

func expandMyBatisIncludes(body string, fragments map[string]string) string {
	if len(fragments) == 0 {
		return body
	}
	if !strings.Contains(strings.ToLower(body), "<include") {
		return body
	}

	out := body
	for i := 0; i < 8; i++ { // depth limit to avoid cycles
		if !strings.Contains(strings.ToLower(out), "<include") {
			break
		}
		out = myBatisIncludeRe.ReplaceAllStringFunc(out, func(tag string) string {
			sub := myBatisIncludeRe.FindStringSubmatch(tag)
			if len(sub) < 2 {
				return ""
			}
			key := myBatisFragmentKey(sub[1])
			if key == "" {
				return ""
			}
			frag, ok := fragments[key]
			if !ok {
				return ""
			}
			return frag
		})
	}
	return out
}

func looksLikeSQL(text string) bool {
	lower := strings.ToLower(text)
	hasStatement := strings.Contains(lower, "select ") ||
		strings.Contains(lower, "insert ") ||
		strings.Contains(lower, "update ") ||
		strings.Contains(lower, "delete ")
	if !hasStatement {
		return false
	}
	hasClause := strings.Contains(lower, " from ") ||
		strings.Contains(lower, " into ") ||
		strings.Contains(lower, " join ") ||
		strings.Contains(lower, " where ")
	if !hasClause {
		return false
	}
	return len(strings.TrimSpace(text)) > 10
}

func simpleNameFromNamespace(namespace string) string {
	if namespace == "" {
		return ""
	}
	if idx := strings.LastIndex(namespace, "."); idx >= 0 && idx < len(namespace)-1 {
		return namespace[idx+1:]
	}
	return namespace
}

func nullableString(value string) interface{} {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func maybeMyBatisXMLPath(path string) bool {
	lower := strings.ToLower(path)
	return strings.Contains(lower, "mapper") || strings.Contains(lower, "mybatis") || strings.Contains(lower, "sqlmap")
}

func isTestLikePath(relPath string) bool { return sourcepath.IsTest(relPath) }

func findFunctionForLine(funcs []parser.ParsedFunction, line int) string {
	if line <= 0 {
		return ""
	}
	bestName := ""
	bestSpan := 0
	for _, fn := range funcs {
		if fn.StartLine <= 0 || fn.EndLine <= 0 {
			continue
		}
		if line < fn.StartLine || line > fn.EndLine {
			continue
		}
		span := fn.EndLine - fn.StartLine
		if bestName == "" || span < bestSpan {
			bestName = fn.Name
			bestSpan = span
		}
	}
	return bestName
}

func gitHeadSHA(repoPath string) string {
	cmd := exec.CommandContext(context.Background(), "git", "-C", repoPath, "rev-parse", "HEAD")
	cmd.Env = runtimeconfig.GitEnvironment()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitCurrentBranch(repoPath string) string {
	cmd := exec.CommandContext(context.Background(), "git", "-C", repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Env = runtimeconfig.GitEnvironment()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func fallbackSnapshotSHA(repoPath string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(repoPath)))
	return "unversioned-" + hex.EncodeToString(sum[:])[:16]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func main() {
	dbURL := flag.String("db", "", "PostgreSQL connection string (required; defaults to DATABASE_URL)")
	workspaceSlug := flag.String("workspace", graph.DefaultWorkspaceSlug, "Tirion workspace slug for indexed snapshot metadata")
	verbose := flag.Bool("v", false, "Verbose output")
	incremental := flag.Bool("incremental", false, "Only reindex changed files")
	resetAll := flag.Bool("reset-all", false, "Truncate all tables before indexing (destructive)")
	repoNameOverride := flag.String("repo-name", "", "Logical repository name to store; defaults to the repo directory basename")
	candidateID := flag.Int64("candidate", 0, "Internal unpublished snapshot ID")
	deferResolution := flag.Bool("defer-resolution", false, "Internal: resolve and build Trace during pipeline publication")
	allowFileFailures := flag.Bool("allow-file-failures", false, "Exit successfully even when files lost all facts to a parser timeout or internal error")
	flag.Parse()
	if *deferResolution && *candidateID == 0 {
		fmt.Fprintln(os.Stderr, "-defer-resolution requires an unpublished -candidate")
		os.Exit(1)
	}
	if err := config.GetEffectivePatterns().Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if flag.NArg() < 1 {
		fmt.Println("Usage: parse [options] <repo-path>")
		fmt.Println("Options:")
		flag.PrintDefaults()
		fmt.Println("\nEnvironment:")
		fmt.Println("  DATABASE_URL  PostgreSQL connection string (overrides -db default)")
		os.Exit(1)
	}

	if flag.NArg() > 1 {
		// flag stops at the first positional argument; anything after it, flags
		// included, would otherwise be silently ignored.
		fmt.Fprintf(os.Stderr, "parse: unexpected argument(s) %q after the repository path; put every flag before it\n", flag.Args()[1:])
		os.Exit(2)
	}

	repoPath := flag.Arg(0)

	// Resolve absolute path
	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		fmt.Printf("Error resolving path: %v\n", err)
		os.Exit(1)
	}

	// Check if path exists
	info, err := os.Stat(absPath)
	if err != nil {
		fmt.Printf("Error accessing path: %v\n", err)
		os.Exit(1)
	}
	if !info.IsDir() {
		fmt.Printf("Path is not a directory: %s\n", absPath)
		os.Exit(1)
	}

	repoName := strings.TrimSpace(*repoNameOverride)
	if repoName == "" {
		repoName = filepath.Base(absPath)
	}
	fmt.Printf("Parsing repository: %s\n", repoName)
	skipTestLike := os.Getenv("PARSE_SKIP_TESTS") == "1"

	startTime := time.Now()
	ctx := context.Background()

	metadataInputs, err := sourceindex.CaptureInputs(absPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Capture repository metadata: %v\n", err)
		os.Exit(1)
	}

	// Initialize storage
	databaseURL := firstNonEmpty(*dbURL, os.Getenv("DATABASE_URL"))
	storage, err := graph.NewStorage(databaseURL)
	if err != nil {
		fmt.Printf("Error initializing database: %v\n", err)
		os.Exit(1)
	}
	defer storage.Close()

	// Track statistics
	var filesProcessed, functionsFound, classesFound, interfacesFound, endpointsFound int
	parseCounters := newParseCounters()
	var parseDuration, dbDuration, dbDeleteDuration, cleanupDuration time.Duration
	var resolveCallsDuration, traceEdgesDuration, traceImplsDuration time.Duration

	useIncremental := *incremental && !*resetAll
	if *resetAll {
		cleanupStart := time.Now()
		if err := storage.ResetAll(); err != nil {
			fmt.Printf("Error resetting database: %v\n", err)
			os.Exit(1)
		}
		cleanupDuration = time.Since(cleanupStart)
	}

	activeWorkspace := strings.TrimSpace(*workspaceSlug)
	if activeWorkspace == "" {
		activeWorkspace = graph.DefaultWorkspaceSlug
	}
	if *candidateID == 0 {
		binary, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		_, err = indexer.RunRepositories(indexer.PipelineOptions{DBURL: databaseURL, Workspace: activeWorkspace, ParseBinary: binary, Repos: []indexer.RepoEntry{{Name: repoName, Path: absPath}}, SkipTests: skipTestLike, SkipExtractors: true, Incremental: useIncremental, Verbose: *verbose})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// Insert repository. Non-default workspace parses must not replace the
	// logical repo source path with a workspace worktree path.
	repoID, err := storage.InsertRepositoryWithPathPolicy(repoName, absPath, nil, nil, false)
	if err != nil {
		fmt.Printf("Error inserting repository: %v\n", err)
		os.Exit(1)
	}
	workspace, err := storage.ResolveWorkspace(activeWorkspace)
	if err != nil {
		fmt.Printf("Error ensuring workspace %q: %v\n", activeWorkspace, err)
		os.Exit(1)
	}
	snapshotSHA := gitHeadSHA(absPath)
	if snapshotSHA == "" {
		snapshotSHA = fallbackSnapshotSHA(absPath)
	}
	snapshotBranch := gitCurrentBranch(absPath)
	if activeWorkspace != graph.DefaultWorkspaceSlug && (snapshotBranch == "" || snapshotBranch == "HEAD" || snapshotBranch == "detached") {
		if wr, err := storage.GetWorkspaceRepo(activeWorkspace, repoName); err == nil {
			snapshotBranch = firstNonEmpty(wr.TargetRef, wr.ResolvedBranch, snapshotBranch)
		}
	}

	var snapshotID int64
	err = storage.Pool().QueryRow(ctx, `SELECT id FROM repo_snapshots WHERE id=$1 AND workspace_id=$2 AND repo_id=$3 AND sha=$4 AND source_path=$5 AND status='building' AND generation<>''`, *candidateID, workspace.ID, repoID, snapshotSHA, absPath).Scan(&snapshotID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid candidate snapshot: %v\n", err)
		os.Exit(1)
	}

	activeSnapshotIDs := []int64{snapshotID}
	if refs, err := storage.ActiveSnapshotsForWorkspace(activeWorkspace); err == nil {
		for _, ref := range refs {
			if ref.RepoID == repoID {
				continue
			}
			activeSnapshotIDs = appendUniqueSnapshotID(activeSnapshotIDs, ref.SnapshotID)
		}
	} else {
		fmt.Printf("Error loading active workspace snapshots for %s: %v\n", activeWorkspace, err)
		os.Exit(1)
	}
	if !*resetAll && !useIncremental {
		cleanupStart := time.Now()
		if err := storage.DeleteFilesByRepoSnapshot(repoID, snapshotID); err != nil {
			fmt.Printf("Error clearing existing snapshot data: %v\n", err)
			os.Exit(1)
		}
		cleanupDuration += time.Since(cleanupStart)
	}

	moduleRegistry := newParserRegistry(absPath)

	seenFiles := make(map[string]bool)
	workspaceRoots := declaredWorkspaceRootDirs(absPath)

	// Walk directory
	err = filepath.Walk(absPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", path, err)
		}

		// Skip directories
		if info.IsDir() {
			if path == absPath {
				return nil
			}
			// Dependency, tool-state and generated-output directories. The same
			// predicate prunes metadata capture and ignored-input capture so the
			// indexed file set and the input fingerprints agree.
			name := info.Name()
			if sourcepath.SkipDir(filepath.Dir(path), name) {
				return filepath.SkipDir
			}
			// lib/libs/packages are source roots in many JS workspaces. Only
			// skip root-level instances when the repo did not declare them as
			// workspace roots.
			switch name {
			case "lib", "libs", "packages":
				if rel, err := filepath.Rel(absPath, path); err == nil && filepath.ToSlash(rel) == name && !workspaceRoots[name] {
					return filepath.SkipDir
				}
			}
			return nil
		}

		parseCounters.markScanned()

		module := moduleRegistry.Match(path)
		if module == nil {
			parseCounters.markSkipped(parseSkipUnsupportedExtension)
			return nil
		}

		// Skip minified, vendor, and library files
		fileName := info.Name()
		relPath, _ := filepath.Rel(absPath, path)
		relPath = normalizeFilePathIdentity(relPath)
		if relPath == "" {
			parseCounters.markSkipped(parseSkipExcludedPath)
			return nil
		}
		if skipTestLike && isTestLikePath(relPath) {
			parseCounters.markSkipped(parseSkipExcludedPath)
			return nil
		}

		if module != nil {
			if skipCategory, ok := moduleRegistry.SkipFile(module, fileName, relPath); ok {
				parseCounters.markSkipped(skipCategory)
				return nil
			}
		}

		excluded, err := sourceindex.ExcludedLink(absPath, relPath)
		if err != nil {
			return err
		}
		if excluded {
			parseCounters.markSkipped(parseSkipExcludedPath)
			fmt.Fprintf(os.Stderr, "Skipping non-source symlink: %s\n", relPath)
			return nil
		}
		if module != nil {
			moduleRegistry.PrepareFile(module, relPath)
		}

		// Read file
		content, err := sourceindex.ReadCurrent(absPath, relPath)
		if err != nil {
			parseCounters.markSkipped(parseSkipReadError)
			return fmt.Errorf("read %s: %w", relPath, err)
		}

		fileHash := hashContent(content)
		seenFiles[relPath] = true
		if useIncremental {
			existingHash, exists, err := storage.GetFileHashForSnapshot(repoID, snapshotID, relPath)
			if err != nil {
				return fmt.Errorf("read indexed hash for %s: %w", relPath, err)
			}
			if exists && existingHash != "" && existingHash == fileHash && module.ID() != "rpg" {
				parseCounters.markSkipped(parseSkipIncrementalUnchanged)
				return nil
			}
			deleteStart := time.Now()
			if err := storage.DeleteFileByRepoSnapshotPath(repoID, snapshotID, relPath); err != nil {
				if *verbose {
					fmt.Printf("  Error clearing file data for %s: %v\n", relPath, err)
				}
				return err
			}
			dbDeleteDuration += time.Since(deleteStart)
		}
		// The hash above covers the original bytes; parsers and PostgreSQL text
		// require valid UTF-8 without NUL. Unknown legacy encodings must not be
		// silently guessed as a code page, but BOM-marked UTF-16 (Windows
		// PowerShell, SSMS) is decoded.
		content, textChanges := sourceindex.NormalizeText(content)
		logTextNormalization(os.Stderr, relPath, textChanges)
		if skipCategory, ok := moduleRegistry.SkipContent(module, relPath, path, content); ok {
			parseCounters.markSkipped(skipCategory)
			return nil
		}

		// Parse file with appropriate parser
		parseStart := time.Now()
		result := parseModuleFile(module, relPath, path, content)
		parseDuration += time.Since(parseStart)
		if result.ParseDiagnostics.Failed() {
			parseCounters.markFailed(classifyFailureCategory(result.ParseDiagnostics))
			parseCounters.recordFailure(relPath, result.ParseDiagnostics)
			// A timeout or recovered panic loses this file's facts and must be
			// visible without -verbose; the run continues with the next file.
			if *verbose || result.ParseDiagnostics.FailureKind != parser.ParseFailureSyntaxUnsupported {
				fmt.Fprintf(os.Stderr, "Parse failure [%s] %s: %s\n", result.ParseDiagnostics.FailureKind, relPath, result.ParseDiagnostics.Message)
			}
		} else {
			parseCounters.markParsed()
		}
		moduleRegistry.AfterParse(module, relPath, &result)
		// Internal failures that are not tied to this file's content (for example
		// unreadable queue properties) affect every file and still abort the run.
		if result.ParseDiagnostics.Systemic() {
			return fmt.Errorf("parse %s: %s", relPath, result.ParseDiagnostics.Message)
		}
		result.Interfaces = parser.MergeInterfaceDeclarations(result.Interfaces)

		// relPath already set above for lib filtering
		lang := result.Language

		dbStart := time.Now()
		tx, err := storage.Pool().Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin file transaction for %s: %w", relPath, err)
		}

		var javaPackage *string
		if lang == "java" {
			javaPackage = &result.JavaPackage
		}
		fileID, err := insertFileTx(ctx, tx, repoID, snapshotID, relPath, &lang, &fileHash, javaPackage)
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("store file %s: %w", relPath, err)
		}

		dbBatch := &pgx.Batch{}
		seenAccess := make(map[string]bool)

		// Store functions
		functionMetas := make(map[string][]functionMeta)
		functionIDs, err := insertFunctionsBulk(ctx, tx, fileID, result.Functions)
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("store functions for %s: %w", relPath, err)
		}
		for i, fn := range result.Functions {
			fnID := int64(0)
			if i < len(functionIDs) {
				fnID = functionIDs[i]
			}
			if fnID != 0 {
				addFunctionMeta(functionMetas, fn.Name, fnID, fn.StartLine, fn.EndLine)

				// Phase 1: Store method signature if available
				if fn.Signature != nil {
					queueInsertMethodSignature(dbBatch, fnID, fn.Signature.Signature, fn.Signature.ParameterTypes, fn.Signature.ReturnType, fn.Signature.IsOverride)
				}

				// Phase 1: Store method annotations
				for _, ann := range fn.Annotations {
					queueInsertAnnotation(dbBatch, "method", fnID, ann.Name, ann.Values, ann.LineNumber)
				}

				if lang == "java" {
					if accesses := extractMyBatisAccesses(fn.Annotations); len(accesses) > 0 {
						receiver := ""
						if idx := strings.LastIndex(fn.Name, "."); idx > 0 {
							receiver = strings.TrimSpace(fn.Name[:idx])
						}
						callerID := fmt.Sprintf("%s:%s:%s", repoName, relPath, fn.Name)
						for _, access := range accesses {
							key := callerID + "|" + strings.ToLower(access.Entity) + "|" + access.Access
							if seenAccess[key] {
								continue
							}
							seenAccess[key] = true
							queueInsertDataAccess(dbBatch, repoID, &snapshotID, callerID, access.Entity, access.Access, fn.StartLine)
							if receiver != "" {
								queueInsertRepositoryEntity(dbBatch, repoID, &snapshotID, fileID, receiver, access.Entity, fn.StartLine)
							}
						}
					}

					if accesses := extractSpringDataQueryAccesses(fn.Annotations); len(accesses) > 0 {
						receiver := ""
						if idx := strings.LastIndex(fn.Name, "."); idx > 0 {
							receiver = strings.TrimSpace(fn.Name[:idx])
						}
						callerID := fmt.Sprintf("%s:%s:%s", repoName, relPath, fn.Name)
						for _, access := range accesses {
							key := callerID + "|" + strings.ToLower(access.Entity) + "|" + access.Access
							if seenAccess[key] {
								continue
							}
							seenAccess[key] = true
							line := access.Line
							if line <= 0 {
								line = fn.StartLine
							}
							queueInsertDataAccess(dbBatch, repoID, &snapshotID, callerID, access.Entity, access.Access, line)
							if receiver != "" {
								queueInsertRepositoryEntity(dbBatch, repoID, &snapshotID, fileID, receiver, access.Entity, line)
							}
						}
					}
				}

				// Phase 1: Store method type parameters
				for _, tp := range fn.TypeParameters {
					queueInsertTypeParameter(dbBatch, "method", fnID, tp.Name, tp.Index, tp.Bounds, tp.BoundType)
				}
			}
		}

		for name, producers := range result.SqsProducers {
			for _, producer := range producers {
				dbBatch.Queue(`INSERT INTO sqs_producers (repo_id, snapshot_id, caller_id, queue_name, line_number)
					VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, repoID, snapshotID,
					repoName+":"+relPath+":"+name, producer.QueueName, producer.LineNumber)
			}
		}
		for _, consumer := range result.SqsConsumers {
			dbBatch.Queue(`INSERT INTO sqs_consumers (repo_id, snapshot_id, consumer_id, queue_name, handler_method)
				VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, repoID, snapshotID,
				repoName+":"+relPath+":"+consumer.ClassName, consumer.QueueName, consumer.HandlerMethod)
		}

		if err := moduleRegistry.PersistExtras(module, &modulePersistContext{
			repoID:        repoID,
			snapshotID:    &snapshotID,
			repoName:      repoName,
			relPath:       relPath,
			fileID:        fileID,
			functionMetas: functionMetas,
			batch:         dbBatch,
			storage:       storage,
		}, result); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("store module extras for %s: %w", relPath, err)
		}

		if lang == "java" {
			sqlAccesses, sqlLiterals := extractSQLAccesses(content)
			if len(sqlAccesses) > 0 {
				for _, access := range sqlAccesses {
					fnName := findFunctionForLine(result.Functions, access.Line)
					if fnName == "" {
						continue
					}
					callerID := fmt.Sprintf("%s:%s:%s", repoName, relPath, fnName)
					key := callerID + "|" + strings.ToLower(access.Entity) + "|" + access.Access
					if seenAccess[key] {
						continue
					}
					seenAccess[key] = true
					queueInsertDataAccess(dbBatch, repoID, &snapshotID, callerID, access.Entity, access.Access, access.Line)
				}
			}

			// Handle @SelectProvider/@InsertProvider/... by stitching together the provider method SQL fragments.
			// Provider resolution here is limited to declarations in the same file.
			if len(sqlLiterals) > 0 {
				for _, fn := range result.Functions {
					providerFunc, accessKind, ok := extractMyBatisProviderBinding(fn.Annotations)
					if !ok {
						continue
					}
					metas := functionMetas[scopedJavaFunctionName(functionMetas, fn.Name, providerFunc)]
					if len(metas) == 0 {
						continue
					}
					sqlText := extractProviderSQLFromLiterals(sqlLiterals, metas[0].startLine, metas[0].endLine)
					if sqlText == "" {
						continue
					}
					tables := extractSQLTablesForAccess(sqlText, accessKind)
					if len(tables) == 0 {
						continue
					}

					callerID := fmt.Sprintf("%s:%s:%s", repoName, relPath, fn.Name)
					receiver := ""
					if idx := strings.LastIndex(fn.Name, "."); idx > 0 {
						receiver = strings.TrimSpace(fn.Name[:idx])
					}
					for _, table := range tables {
						entity := tableNameToEntity(table)
						if entity == "" {
							continue
						}
						key := callerID + "|" + strings.ToLower(entity) + "|" + accessKind
						if seenAccess[key] {
							continue
						}
						seenAccess[key] = true
						queueInsertDataAccess(dbBatch, repoID, &snapshotID, callerID, entity, accessKind, fn.StartLine)
						if receiver != "" {
							queueInsertRepositoryEntity(dbBatch, repoID, &snapshotID, fileID, receiver, entity, fn.StartLine)
						}
					}
				}
			}

		}

		// AST- or parser-derived accesses (Java criteria API, RPG opcodes/SQL, etc.).
		for fnName, accesses := range result.DataAccesses {
			if fnName == "" {
				continue
			}
			callerID := fmt.Sprintf("%s:%s:%s", repoName, relPath, fnName)
			for _, access := range accesses {
				if access.EntityName == "" || access.Access == "" {
					continue
				}
				key := callerID + "|" + strings.ToLower(access.EntityName) + "|" + access.Access
				if seenAccess[key] {
					continue
				}
				seenAccess[key] = true
				queueInsertDataAccess(dbBatch, repoID, &snapshotID, callerID, access.EntityName, access.Access, access.LineNumber)
			}
		}

		// Store classes
		classIDs := make(map[string]int64)
		classInsertIDs, err := insertClassesBulk(ctx, tx, fileID, result.Classes)
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("store classes for %s: %w", relPath, err)
		}
		for i, cls := range result.Classes {
			classID := int64(0)
			if i < len(classInsertIDs) {
				classID = classInsertIDs[i]
			}
			if classID != 0 {
				classIDs[cls.Name] = classID

				// Phase 1: Store class annotations
				for _, ann := range cls.Annotations {
					queueInsertAnnotation(dbBatch, "class", classID, ann.Name, ann.Values, ann.LineNumber)
				}

				// Phase 1: Store class type parameters
				for _, tp := range cls.TypeParameters {
					queueInsertTypeParameter(dbBatch, "class", classID, tp.Name, tp.Index, tp.Bounds, tp.BoundType)
				}

				for _, constant := range cls.EnumConstants {
					queueInsertEnumConstant(dbBatch, classID, constant)
				}

				// Phase 1: Store class fields
				for _, field := range cls.Fields {
					queueInsertField(dbBatch, &classID, nil, field.Name, field.FieldType, field.TypeParameters, field.Modifiers, field.StartLine, field.IsInjected, field.InjectionType)
				}

				// Phase 1: Store constructors and their parameters
				for _, ctor := range cls.Constructors {
					for _, param := range ctor.Parameters {
						queueInsertConstructorParam(dbBatch, classID, param.Name, param.ParamType, param.Index, param.IsInjected, param.Annotation, param.AnnotationValue, ctor.StartLine)
					}
				}

				// Phase 1: Store implementations (class implements interfaces)
				for _, ifaceName := range cls.Implements {
					queueInsertImplementation(dbBatch, classID, ifaceName, nil) // interface_id resolved later if needed
					if entityName, ok := parseRepositoryEntity(ifaceName); ok && cls.Name != "" {
						queueInsertRepositoryEntity(dbBatch, repoID, &snapshotID, fileID, cls.Name, entityName, cls.StartLine)
					}
				}
			}
		}

		if lang == "java" && len(result.ScheduledMethods) > 0 {
			for _, sched := range result.ScheduledMethods {
				classID, ok := classIDs[sched.ClassName]
				if !ok || classID == 0 {
					continue
				}
				methodID := findScheduledMethodID(functionMetas, sched.ClassName, sched.MethodName, sched.LineNumber)
				queueInsertScheduledMethod(dbBatch, classID, methodID, sched.MethodName, sched.Cron, sched.FixedRate, sched.FixedDelay, sched.InitialDelay, sched.LineNumber)
			}
		}

		// Store entity metadata when a parser can identify ORM/table-backed classes.
		if len(result.JpaEntities) > 0 {
			for _, entity := range result.JpaEntities {
				if classID, ok := classIDs[entity.ClassName]; ok {
					queueInsertJpaEntity(dbBatch, classID, entity.TableName, entity.Schema, entity.Catalog)
				}
			}
		}

		// Phase 1: Store JPA relationships
		if lang == "java" {
			for _, rel := range result.JpaRelationships {
				sourceClassID, ok := classIDs[rel.SourceEntity]
				if !ok {
					continue
				}
				var targetClassID *int64
				if id, ok := classIDs[rel.TargetEntity]; ok {
					targetClassID = &id
				}
				queueInsertJpaRelationship(dbBatch, sourceClassID, rel.TargetEntity, targetClassID, rel.RelationType, rel.SourceField, rel.MappedBy, rel.JoinColumnName, rel.FetchType, rel.CascadeTypes, rel.LineNumber)
			}
		}

		// Phase 1: Store interfaces
		interfaceIDs := make(map[string]int64)
		interfaceInsertIDs, err := insertInterfacesBulk(ctx, tx, fileID, result.Interfaces)
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("store interfaces for %s: %w", relPath, err)
		}
		for i, iface := range result.Interfaces {
			ifaceID := int64(0)
			if i < len(interfaceInsertIDs) {
				ifaceID = interfaceInsertIDs[i]
			}
			if ifaceID != 0 {
				interfaceIDs[iface.Name] = ifaceID

				// Store interface annotations
				for _, ann := range iface.Annotations {
					queueInsertAnnotation(dbBatch, "interface", ifaceID, ann.Name, ann.Values, ann.LineNumber)
				}

				// Store interface type parameters
				for _, tp := range iface.TypeParameters {
					queueInsertTypeParameter(dbBatch, "interface", ifaceID, tp.Name, tp.Index, tp.Bounds, tp.BoundType)
				}

				// Store interface fields (constants)
				for _, field := range iface.Fields {
					queueInsertField(dbBatch, nil, &ifaceID, field.Name, field.FieldType, field.TypeParameters, field.Modifiers, field.StartLine, field.IsInjected, field.InjectionType)
				}

				for _, extendsName := range iface.ExtendsInterfaces {
					if entityName, ok := parseRepositoryEntity(extendsName); ok && iface.Name != "" {
						queueInsertRepositoryEntity(dbBatch, repoID, &snapshotID, fileID, iface.Name, entityName, iface.StartLine)
					}
				}
			}
		}

		// Phase 1: Store HTTP interface methods (Retrofit/Feign)
		for _, httpMethod := range result.HttpInterfaceMethods {
			if ifaceID, ok := interfaceIDs[httpMethod.InterfaceName]; ok {
				// Try to find the function ID for this method
				methodFullName := httpMethod.InterfaceName + "." + httpMethod.MethodName
				var fnID *int64
				if metas, ok := functionMetas[methodFullName]; ok {
					if id, ok := selectFunctionIDByLine(metas, httpMethod.LineNumber); ok {
						fnID = &id
					}
				}
				queueInsertHttpInterfaceMethod(dbBatch, ifaceID, httpMethod.MethodName, fnID, httpMethod.HttpMethod, httpMethod.UrlPattern, httpMethod.LineNumber)
			}
		}

		// Store imports
		for _, imp := range result.Imports {
			queueInsertImport(dbBatch, fileID, imp.Path, nil, imp.Names, imp.IsDefault)
		}

		queueInsertTypeAliasBatch(dbBatch, fileID, result.TypeAliases)
		queueInsertHookCallBatch(dbBatch, fileID, result.HookCalls)

		// Store endpoints
		for _, ep := range result.Endpoints {
			lineNum := ep.LineNumber
			handlerID := findHandlerFunctionID(functionMetas, ep.HandlerName, lineNum)
			queueInsertEndpoint(dbBatch, repoID, ep.Path, ep.Method, handlerID, &fileID, &lineNum)
		}

		receiverTypesByFunction := buildReceiverTypeIndex(result)

		// Store function calls (best-effort callee resolution within the same file)
		for fnName, calls := range result.FunctionCalls {
			metas, ok := functionMetas[fnName]
			if !ok {
				continue
			}
			receiverTypes := receiverTypesByFunction[fnName]
			for _, call := range calls {
				callerID, ok := selectFunctionIDByLine(metas, call.LineNumber)
				if !ok || callerID == 0 {
					continue
				}
				calleeName, calleeID := resolveCallee(functionMetas, fnName, call, receiverTypes, lang)
				unresolvedReason := ""
				if calleeID == nil {
					unresolvedReason = classifyUnresolvedCall(call, calleeName)
				}
				queueInsertFunctionCall(dbBatch, callerID, calleeName, calleeID, call.LineNumber, call.IsAsync, unresolvedReason, call.IsCallbackArgument)
			}
		}

		if dbBatch.Len() > 0 {
			batchResults := tx.SendBatch(ctx, dbBatch)
			if err := batchResults.Close(); err != nil {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("flush file batch for %s: %w", relPath, err)
			}
		}

		if err := tx.Commit(ctx); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("commit file %s: %w", relPath, err)
		}

		dbDuration += time.Since(dbStart)

		filesProcessed++
		functionsFound += len(result.Functions)
		classesFound += len(result.Classes)
		interfacesFound += len(result.Interfaces)
		endpointsFound += len(result.Endpoints)

		if *verbose {
			fmt.Printf("  %s: %d functions, %d classes, %d interfaces\n", relPath, len(result.Functions), len(result.Classes), len(result.Interfaces))
		}

		return nil
	})

	if err != nil {
		fmt.Printf("Error walking directory: %v\n", err)
		os.Exit(1)
	}

	finalizeStart := time.Now()
	if err := moduleRegistry.FinalizeRepo(&repoFinalizeContext{
		repoID:            repoID,
		snapshotID:        &snapshotID,
		repoName:          repoName,
		workspaceSlug:     activeWorkspace,
		activeSnapshotIDs: activeSnapshotIDs,
		storage:           storage,
	}); err != nil {
		fmt.Printf("Error finalizing parser modules: %v\n", err)
		os.Exit(1)
	}
	dbDuration += time.Since(finalizeStart)

	if useIncremental {
		paths, err := storage.GetFilesByRepoSnapshot(repoID, snapshotID)
		if err == nil {
			for _, path := range paths {
				if !seenFiles[path] {
					deleteStart := time.Now()
					if err := storage.DeleteFileByRepoSnapshotPath(repoID, snapshotID, path); err != nil {
						fmt.Printf("Error removing stale file %s: %v\n", path, err)
						os.Exit(1)
					}
					dbDeleteDuration += time.Since(deleteStart)
				}
			}
		} else {
			fmt.Printf("Error listing repo files for cleanup: %v\n", err)
			os.Exit(1)
		}
	}

	// The full pipeline builds these relationships once, after enrichment, inside
	// the publication transaction. Standalone/partial parsing keeps its early pass.
	if !*deferResolution {
		traceEdgeStart := time.Now()
		if err := storage.ResolveFunctionCallCalleesForSnapshot(repoID, &snapshotID); err != nil {
			fmt.Printf("Error resolving function call targets: %v\n", err)
			os.Exit(1)
		} else {
			if err := moduleRegistry.ResolveRepo(&repoFinalizeContext{
				repoID:            repoID,
				snapshotID:        &snapshotID,
				repoName:          repoName,
				workspaceSlug:     activeWorkspace,
				activeSnapshotIDs: activeSnapshotIDs,
				storage:           storage,
			}); err != nil {
				fmt.Printf("Error running parser module resolution hooks: %v\n", err)
				os.Exit(1)
			}
			resolveCallsDuration = time.Since(traceEdgeStart)
			dbDuration += resolveCallsDuration
			traceEdgeStart = time.Now()
		}
		if err := storage.RefreshTraceCallEdgesForSnapshot(repoID, &snapshotID); err != nil {
			fmt.Printf("Error refreshing trace edges: %v\n", err)
			os.Exit(1)
		} else {
			traceEdgesDuration = time.Since(traceEdgeStart)
			dbDuration += traceEdgesDuration
		}

		traceImplStart := time.Now()
		if err := storage.RefreshTraceInterfaceImplsForSnapshot(repoID, &snapshotID); err != nil {
			fmt.Printf("Error refreshing trace interface impls: %v\n", err)
			os.Exit(1)
		} else {
			traceImplsDuration = time.Since(traceImplStart)
			dbDuration += traceImplsDuration
		}
	}

	if err := sourceindex.CheckInputs(absPath, metadataInputs); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if moduleRegistry.includes.err != nil {
		fmt.Fprintln(os.Stderr, moduleRegistry.includes.err)
		os.Exit(1)
	}
	if err := sourceindex.CheckIncludes(moduleRegistry.includes.inputs); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if _, err := storage.Pool().Exec(context.Background(), `UPDATE repo_snapshots SET input_manifest=$2,include_manifest=$3,parser_version=$4 WHERE id=$1`, snapshotID, metadataInputs, moduleRegistry.includes.inputs, buildinfo.Version); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	elapsed := time.Since(startTime)

	fmt.Println(strings.Repeat("-", 40))
	fmt.Printf("Parsing completed in %.2fs\n", elapsed.Seconds())
	fmt.Printf("Parse time:  %.2fs\n", parseDuration.Seconds())
	fmt.Printf("DB time:     %.2fs\n", dbDuration.Seconds())
	if resolveCallsDuration > 0 || traceEdgesDuration > 0 || traceImplsDuration > 0 {
		fmt.Printf("Resolve calls: %.2fs\n", resolveCallsDuration.Seconds())
		fmt.Printf("Trace edges:   %.2fs\n", traceEdgesDuration.Seconds())
		fmt.Printf("Trace impls:   %.2fs\n", traceImplsDuration.Seconds())
	}
	if cleanupDuration > 0 {
		fmt.Printf("Cleanup:    %.2fs\n", cleanupDuration.Seconds())
	}
	if dbDeleteDuration > 0 {
		fmt.Printf("DB delete:  %.2fs\n", dbDeleteDuration.Seconds())
	}
	otherDuration := elapsed - parseDuration - dbDuration - cleanupDuration - dbDeleteDuration
	if otherDuration < 0 {
		otherDuration = 0
	}
	fmt.Printf("Other:      %.2fs\n", otherDuration.Seconds())
	fmt.Printf("Files:      %d\n", filesProcessed)
	writeParseCounters(os.Stdout, parseCounters)
	fmt.Printf("Functions:  %d\n", functionsFound)
	fmt.Printf("Classes:    %d\n", classesFound)
	fmt.Printf("Interfaces: %d\n", interfacesFound)
	fmt.Printf("Endpoints:  %d\n", endpointsFound)
	// Fail closed: a candidate missing whole files must not replace a complete
	// published graph. The pipeline aborts on this exit, keeping the old snapshot.
	if lost := parseCounters.failedByCategory[parseFailureTimeout] + parseCounters.failedByCategory[parseFailureInternal]; lost > 0 && !*allowFileFailures {
		fmt.Fprintf(os.Stderr, "parse: %d file(s) lost all facts to a parser timeout or internal error (listed above); the result is incomplete and will not be published. Fix or exclude those files, or rerun with -allow-file-failures to publish anyway.\n", lost)
		os.Exit(3)
	}
}
