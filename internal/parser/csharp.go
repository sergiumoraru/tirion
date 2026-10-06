package parser

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type CSharpParser struct{}

type csharpAttribute struct {
	Name    string
	RawArgs string
	Args    []string
}

type csharpClassFrame struct {
	Name           string
	RouteTemplates []string
	IsController   bool
	Depth          int
	ClassIndex     int
}

type csharpMethodFrame struct {
	Name          string
	Depth         int
	FunctionIndex int
}

type csharpHttpRoute struct {
	Method   string
	Template string
}

type csharpMapCall struct {
	Token  string
	Args   string
	Line   int
	Prefix string // route prefix inherited from MapGroup receivers
}

var (
	csharpClassDeclRe = regexp.MustCompile(`\b(class|record)\s+([A-Za-z_][A-Za-z0-9_]*)(?:\s*:\s*([^{]+))?`)
	// Example: public async Task<IActionResult> Login(User req) { ... }
	csharpMethodDeclRe         = regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|virtual|override|abstract|async|sealed|partial|extern|new)\s+)+(?:[\w<>\[\],\.\?\s]+\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*\(([^)]*)\)\s*(?:where\b.*)?(?:\{|=>)?`)
	csharpTypedMethodDeclRe    = regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|virtual|override|abstract|async|sealed|partial|extern|new)\s+)*(?:\([^)]*\)|[\w\[\]\.\?]+(?:<[\w<>\[\],\.\?()\s]+>)?)\s+([A-Za-z_][A-Za-z0-9_]*)(?:\s*<[^>]+>)?\s*\(([^)]*)\)\s*(?:where\b.*)?(?:\{|=>|$)`)
	csharpBlockPropertyRe      = regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|virtual|override|abstract|sealed|new)\s+)+[\w<>\[\],\.\?]+\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:\{|$)`)
	csharpExpressionPropertyRe = regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|virtual|override|abstract|sealed|new)\s+)+[\w<>\[\],\.\?]+\s+([A-Za-z_][A-Za-z0-9_]*)\s*=>`)
	csharpUsingRe              = regexp.MustCompile(`^\s*using\s+([A-Za-z0-9_.]+)\s*;\s*$`)
	csharpAttributeRe          = regexp.MustCompile(`^\s*\[(.+)\]\s*$`)
	csharpCallRe               = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_\.]*)\s*\(`)
	csharpMapCallRe            = regexp.MustCompile(`\bMap(Get|Post|Put|Delete|Patch|Head|Options|Methods)\s*\((.*)$`)
	csharpStringRe             = regexp.MustCompile(`"([^"\\]*(?:\\.[^"\\]*)*)"|'([^'\\]*(?:\\.[^'\\]*)*)'`)
	csharpFieldDeclRe          = regexp.MustCompile(`^\s*((?:(?:public|private|protected|internal|static|readonly|const|volatile|new)\s+)+)([A-Za-z_][A-Za-z0-9_<>\[\],\.\?\s]*)\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:[=;{])`)
	csharpDbSetRe              = regexp.MustCompile(`(?i)\b([A-Za-z_][A-Za-z0-9_]*)\s*\.\s*(?:Set\s*<\s*([A-Za-z_][A-Za-z0-9_]*)\s*>\s*\(\)|([A-Za-z_][A-Za-z0-9_]*))\s*\.\s*(Add|AddOrUpdate|Attach|Remove|Where|FirstOrDefault|Find|ToList|Select|Include)\b`)
	csharpSQLTableRe           = regexp.MustCompile(`(?i)\b(?:FROM|JOIN|UPDATE|INTO)\s+([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)`)
	csharpKafkaSettingRe       = regexp.MustCompile(`\b(?:var|string)\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*ConfigurationManager\.AppSettings\["([^"]+)"\]`)
	csharpSubscribeRe          = regexp.MustCompile(`\.Subscribe\(\s*([A-Za-z_][A-Za-z0-9_]*|"[^"]+")\s*\)`)
)

var csharpControlFlowWords = map[string]bool{
	"if": true, "for": true, "foreach": true, "while": true, "switch": true, "catch": true,
	"lock": true, "using": true, "nameof": true, "typeof": true, "sizeof": true, "checked": true,
	"unchecked": true, "return": true, "new": true, "throw": true, "base": true, "this": true,
}

var csharpHttpAttributeToMethod = map[string]string{
	"httpget":     "GET",
	"httppost":    "POST",
	"httpput":     "PUT",
	"httpdelete":  "DELETE",
	"httppatch":   "PATCH",
	"httphead":    "HEAD",
	"httpoptions": "OPTIONS",
}

func NewCSharpParser() *CSharpParser {
	return &CSharpParser{}
}

func (p *CSharpParser) CanParse(filePath string) bool {
	ext := strings.ToLower(filepath.Ext(filePath))
	return ext == ".cs" || ext == ".asp" || ext == ".aspx"
}

func (p *CSharpParser) ParseFile(filePath string, content []byte) (result ParsedFile) {
	ext := strings.ToLower(filepath.Ext(filePath))
	lang := "csharp"
	if ext == ".aspx" {
		lang = "aspx"
	} else if ext == ".asp" {
		lang = "asp"
	}

	result = ParsedFile{
		Path:          filePath,
		Language:      lang,
		Functions:     []ParsedFunction{},
		Classes:       []ParsedClass{},
		Imports:       []ParsedImport{},
		Endpoints:     []ParsedEndpoint{},
		FunctionCalls: make(map[string][]ParsedFunctionCall),
		HttpCalls:     make(map[string][]ParsedHttpCall),
		DataAccesses:  make(map[string][]ParsedDataAccess),
		LocalVarTypes: make(map[string]map[string]string),
		SqsProducers:  make(map[string][]ParsedSqsProducer),
		SqsConsumers:  []ParsedSqsConsumer{},
		JpaEntities:   []ParsedJpaEntity{},
	}
	defer recoverParsePanic(&result)

	if ext == ".asp" || ext == ".aspx" {
		p.parseAspFile(filePath, &result)
		return result
	}

	p.parseCSharpFile(filePath, string(content), &result)
	return result
}

func (p *CSharpParser) parseAspFile(filePath string, result *ParsedFile) {
	path := inferAspEndpointPath(filePath)
	if path == "" {
		return
	}
	handler := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	result.Endpoints = append(result.Endpoints, ParsedEndpoint{
		Path:        path,
		Method:      "REQUEST",
		HandlerName: handler,
		LineNumber:  1,
	})
}

func (p *CSharpParser) parseCSharpFile(filePath string, content string, result *ParsedFile) {
	lines := strings.Split(content, "\n")

	braceDepth := 0
	var classStack []csharpClassFrame
	var methodStack []csharpMethodFrame
	var pendingClass *csharpClassFrame
	var pendingMethod *csharpMethodFrame
	pendingMethodLine, pendingMethodColumn := -1, 0
	declarationEnd := -1
	structuralLines := strings.Split(maskCSharpLiterals(content), "\n")
	callLines := strings.Split(maskCSharpSource(content, true), "\n")
	var pendingMap *csharpMapCall
	routeGroups := newCSharpRouteGroups()
	var pendingAttrs []csharpAttribute
	seenEndpoint := make(map[string]bool)

	for i, rawLine := range lines {
		lineNum := i + 1
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(strings.TrimSpace(structuralLines[i]), "[") {
			if attrs, remainder, prefix := splitCSharpLeadingAttributes(rawLine); prefix > 0 {
				pendingAttrs = append(pendingAttrs, attrs...)
				line = strings.TrimSpace(remainder)
				structuralLines[i] = strings.Repeat(" ", prefix) + structuralLines[i][prefix:]
				callLines[i] = strings.Repeat(" ", prefix) + callLines[i][prefix:]
				if line == "" {
					continue
				}
			}
		}
		openBraces, closeBraces := countBraces(structuralLines[i])
		if strings.TrimSpace(structuralLines[i]) == "" {
			if len(methodStack) > 0 {
				active := methodStack[len(methodStack)-1]
				result.FunctionCalls[active.Name] = append(result.FunctionCalls[active.Name], extractCSharpCalls(callLines[i], lineNum)...)
			}
			continue
		}

		if pendingMap != nil {
			pendingMap.Args += " " + line
			if strings.Contains(line, ")") {
				for _, ep := range parseMinimalMapEndpoints(pendingMap.Token, pendingMap.Args, pendingMap.Line, pendingMap.Prefix) {
					appendEndpointUnique(result, seenEndpoint, ep)
				}
				pendingMap = nil
			}
		}

		routeGroups.observe(line)
		if loc := csharpMapCallRe.FindStringSubmatchIndex(line); len(loc) == 6 {
			token := strings.ToUpper(strings.TrimSpace(line[loc[2]:loc[3]]))
			args := strings.TrimSpace(line[loc[4]:loc[5]])
			prefix := routeGroups.mapPrefix(line, loc[0])
			if strings.Contains(args, ")") {
				for _, ep := range parseMinimalMapEndpoints(token, args, lineNum, prefix) {
					appendEndpointUnique(result, seenEndpoint, ep)
				}
			} else {
				pendingMap = &csharpMapCall{
					Token:  token,
					Args:   args,
					Line:   lineNum,
					Prefix: prefix,
				}
			}
		}

		_, _, isDeclaration := extractCSharpMethodSignature(line)
		if len(methodStack) > 0 && !isDeclaration {
			active := methodStack[len(methodStack)-1]
			calls := extractCSharpCalls(callLines[i], lineNum)
			if len(calls) > 0 {
				result.FunctionCalls[active.Name] = append(result.FunctionCalls[active.Name], calls...)
			}
		}

		if pendingClass != nil && strings.Contains(line, "{") {
			pendingClass.Depth = braceDepth + openBraces - closeBraces
			classStack = append(classStack, *pendingClass)
			pendingClass = nil
		}
		if pendingMethod != nil && i == pendingMethodLine {
			pendingMethod.Depth = braceDepth + 1
			methodStack = append(methodStack, *pendingMethod)
			result.FunctionCalls[pendingMethod.Name] = append(result.FunctionCalls[pendingMethod.Name], extractCSharpCalls(callLines[i][pendingMethodColumn+1:], lineNum)...)
			pendingMethod = nil
		}

		if match := csharpUsingRe.FindStringSubmatch(line); len(match) == 2 {
			result.Imports = append(result.Imports, ParsedImport{Path: strings.TrimSpace(match[1])})
		}

		if attrs, ok := parseCSharpAttributeLine(line); ok {
			pendingAttrs = append(pendingAttrs, attrs...)
			braceDepth += openBraces - closeBraces
			popClosedFrames(result, lineNum, &braceDepth, &classStack, &methodStack)
			continue
		}

		if classMatch := csharpClassDeclRe.FindStringSubmatch(line); len(classMatch) >= 3 {
			className := classMatch[2]
			extendsClass, implements := parseCSharpBaseTypes(classMatch)
			routes := routeTemplatesFromAttrs(pendingAttrs)
			isController := isCSharpControllerClass(className, line, pendingAttrs)
			classIndex := len(result.Classes)
			result.Classes = append(result.Classes, ParsedClass{
				Name:         className,
				StartLine:    lineNum,
				EndLine:      lineNum,
				ExtendsClass: extendsClass,
				Implements:   implements,
				IsExported:   strings.Contains(line, "public "),
				Methods:      []ParsedFunction{},
			})
			if entity, ok := csharpMappedEntity(className, pendingAttrs); ok {
				result.JpaEntities = append(result.JpaEntities, entity)
			}
			frame := csharpClassFrame{
				Name:           className,
				RouteTemplates: routes,
				IsController:   isController,
				ClassIndex:     classIndex,
			}
			if strings.Contains(line, "{") {
				frame.Depth = braceDepth + openBraces - closeBraces
				classStack = append(classStack, frame)
			} else {
				pendingClass = &frame
			}
			pendingAttrs = nil
		}

		if len(methodStack) == 0 {
			if classFrame := currentClassFrame(classStack, pendingClass); classFrame != nil {
				if !csharpClassDeclRe.MatchString(line) {
					if property := csharpBlockPropertyRe.FindStringSubmatch(line); len(property) == 2 {
						appendCSharpPropertyAccessors(result, classFrame.Name, property[1], lines, structuralLines, callLines, i)
					}
				}
				if field, ok := parseCSharpField(line, lineNum, pendingAttrs); ok {
					if classFrame.ClassIndex >= 0 && classFrame.ClassIndex < len(result.Classes) {
						result.Classes[classFrame.ClassIndex].Fields = append(result.Classes[classFrame.ClassIndex].Fields, field)
					}
					pendingAttrs = nil
				}
			}
		}

		signature := line
		signatureEnd := i
		if strings.Contains(line, "(") && !strings.Contains(line, ")") && !strings.ContainsAny(line, ";{}") {
			for j := i + 1; j < len(lines); j++ {
				part := strings.TrimSpace(lines[j])
				signature += " " + part
				signatureEnd = j
				if strings.ContainsAny(structuralLines[j], "){};") {
					break
				}
			}
		}
		methodName, paramsText, ok := extractCSharpMethodSignature(signature)
		if !ok {
			if property := csharpExpressionPropertyRe.FindStringSubmatch(line); len(property) == 2 {
				methodName, paramsText, ok = property[1]+".get", "", true
			}
		}
		if i <= declarationEnd {
			ok = false
		}
		if ok {
			if !csharpControlFlowWords[strings.ToLower(methodName)] {
				classFrame := currentClassFrame(classStack, pendingClass)
				fullName := methodName
				if classFrame != nil && classFrame.Name != "" {
					fullName = classFrame.Name + "." + methodName
				}
				// A declaration inside a method body is a local function. It is
				// named under its enclosing method (Class.Method.Local, like property
				// accessors) and is not a class member: it must not appear as
				// Class.Local, in the class's method list, or as a controller action.
				isLocal := len(methodStack) > 0
				if isLocal {
					fullName = methodStack[len(methodStack)-1].Name + "." + methodName
				} else if classFrame != nil {
					// A class member starts a new group scope. A declaration outside
					// any class is a top-level local function (Program.cs): it shares
					// the file's top-level statements, whose groups must survive it.
					routeGroups.reset()
				}

				params, paramTypes := parseCSharpParams(paramsText)
				fnIndex := len(result.Functions)
				fn := ParsedFunction{
					Name:       fullName,
					StartLine:  lineNum,
					EndLine:    lineNum,
					Params:     params,
					ParamTypes: paramTypes,
					IsExported: strings.Contains(line, "public "),
					IsAsync:    strings.Contains(line, " async "),
					SourceCode: line,
				}
				result.Functions = append(result.Functions, fn)
				if !isLocal && classFrame != nil && classFrame.ClassIndex >= 0 && classFrame.ClassIndex < len(result.Classes) {
					result.Classes[classFrame.ClassIndex].Methods = append(result.Classes[classFrame.ClassIndex].Methods, fn)
				}

				// Constructors share the class name; an attribute on one is not a route.
				if !isLocal && classFrame != nil && classFrame.IsController && methodName != classFrame.Name {
					for _, ep := range extractCSharpControllerEndpoints(classFrame, methodName, fullName, pendingAttrs, lineNum) {
						appendEndpointUnique(result, seenEndpoint, ep)
					}
				}

				frame := csharpMethodFrame{
					Name:          fullName,
					FunctionIndex: fnIndex,
				}
				declarationEnd = signatureEnd
				bodyLine, bodyColumn, kind := csharpMethodBodyStart(structuralLines, i)
				switch kind {
				case "=>":
					endLine, endColumn := csharpExpressionEnd(structuralLines, bodyLine, bodyColumn+2)
					for j := bodyLine; j <= endLine; j++ {
						body := callLines[j]
						if j == endLine {
							body = body[:endColumn]
						}
						if j == bodyLine {
							body = body[bodyColumn+2:]
						}
						result.FunctionCalls[fullName] = append(result.FunctionCalls[fullName], extractCSharpCalls(body, j+1)...)
					}
					result.Functions[fnIndex].EndLine = endLine + 1
					declarationEnd = endLine
				case "{":
					declarationEnd = bodyLine
					if bodyLine == i {
						frame.Depth = braceDepth + 1
						methodStack = append(methodStack, frame)
						result.FunctionCalls[fullName] = append(result.FunctionCalls[fullName], extractCSharpCalls(callLines[i][bodyColumn+1:], lineNum)...)
					} else {
						pendingMethod = &frame
						pendingMethodLine, pendingMethodColumn = bodyLine, bodyColumn
					}
				case ";":
					result.Functions[fnIndex].EndLine = bodyLine + 1
					declarationEnd = bodyLine
				}
				pendingAttrs = nil
			}
		} else if line != "" && len(pendingAttrs) > 0 {
			pendingAttrs = nil
		}

		braceDepth += openBraces - closeBraces
		popClosedFrames(result, lineNum, &braceDepth, &classStack, &methodStack)
	}

	lastLine := len(lines)
	for len(methodStack) > 0 {
		frame := methodStack[len(methodStack)-1]
		methodStack = methodStack[:len(methodStack)-1]
		if frame.FunctionIndex >= 0 && frame.FunctionIndex < len(result.Functions) {
			result.Functions[frame.FunctionIndex].EndLine = lastLine
		}
	}
	for len(classStack) > 0 {
		frame := classStack[len(classStack)-1]
		classStack = classStack[:len(classStack)-1]
		if frame.ClassIndex >= 0 && frame.ClassIndex < len(result.Classes) {
			result.Classes[frame.ClassIndex].EndLine = lastLine
		}
	}
	fillCSharpFunctionSourceAndAccesses(result, lines)
	extractCSharpKafkaConsumers(result, strings.Join(lines, "\n"))
}

func popClosedFrames(result *ParsedFile, lineNum int, braceDepth *int, classStack *[]csharpClassFrame, methodStack *[]csharpMethodFrame) {
	for len(*methodStack) > 0 && *braceDepth < (*methodStack)[len(*methodStack)-1].Depth {
		frame := (*methodStack)[len(*methodStack)-1]
		*methodStack = (*methodStack)[:len(*methodStack)-1]
		if frame.FunctionIndex >= 0 && frame.FunctionIndex < len(result.Functions) {
			result.Functions[frame.FunctionIndex].EndLine = lineNum
		}
	}
	for len(*classStack) > 0 && *braceDepth < (*classStack)[len(*classStack)-1].Depth {
		frame := (*classStack)[len(*classStack)-1]
		*classStack = (*classStack)[:len(*classStack)-1]
		if frame.ClassIndex >= 0 && frame.ClassIndex < len(result.Classes) {
			result.Classes[frame.ClassIndex].EndLine = lineNum
		}
	}
}

func parseCSharpBaseTypes(classMatch []string) (string, []string) {
	if len(classMatch) < 4 {
		return "", nil
	}
	raw := strings.TrimSpace(classMatch[3])
	if raw == "" {
		return "", nil
	}
	raw = strings.TrimSpace(strings.Split(raw, "where ")[0])
	parts := splitTopLevel(raw, ',')
	var extendsClass string
	var implements []string
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		name = strings.TrimSpace(strings.TrimSuffix(name, "{"))
		if extendsClass == "" && !isLikelyCSharpInterfaceName(name) {
			extendsClass = name
			continue
		}
		implements = append(implements, name)
	}
	return extendsClass, implements
}

func isLikelyCSharpInterfaceName(name string) bool {
	name = normalizeCSharpTypeName(strings.TrimSpace(strings.TrimSuffix(name, "{")))
	if len(name) < 3 || name[0] != 'I' {
		return false
	}
	if strings.HasSuffix(name, "Base") {
		return false
	}
	return name[1] >= 'A' && name[1] <= 'Z'
}

func csharpMappedEntity(className string, attrs []csharpAttribute) (ParsedJpaEntity, bool) {
	for _, attr := range attrs {
		name := attr.Name
		if index := strings.LastIndex(name, "."); index >= 0 {
			name = name[index+1:]
		}
		if name != "Table" {
			continue
		}
		args := splitTopLevel(attr.RawArgs, ',')
		if len(args) == 0 {
			continue
		}
		table, err := strconv.Unquote(strings.TrimSpace(args[0]))
		if err != nil || table == "" {
			continue
		}
		entity := ParsedJpaEntity{ClassName: className, TableName: table}
		for _, arg := range args[1:] {
			key, value, ok := strings.Cut(arg, "=")
			if ok && strings.TrimSpace(key) == "Schema" {
				if schema, err := strconv.Unquote(strings.TrimSpace(value)); err == nil {
					entity.Schema = schema
				}
			}
		}
		return entity, true
	}
	return ParsedJpaEntity{}, false
}

func parseCSharpField(line string, lineNumber int, attrs []csharpAttribute) (ParsedField, bool) {
	match := csharpFieldDeclRe.FindStringSubmatch(line)
	if len(match) != 4 {
		return ParsedField{}, false
	}
	if strings.Contains(line, "(") {
		return ParsedField{}, false
	}
	rawType := strings.TrimSpace(match[2])
	name := strings.TrimSpace(match[3])
	if rawType == "" || name == "" {
		return ParsedField{}, false
	}
	lowerType := strings.ToLower(rawType)
	if lowerType == "return" || lowerType == "new" || lowerType == "class" || lowerType == "record" || lowerType == "interface" || lowerType == "enum" {
		return ParsedField{}, false
	}
	return ParsedField{
		Name:        name,
		FieldType:   normalizeCSharpTypeName(rawType),
		Modifiers:   strings.Fields(strings.TrimSpace(match[1])),
		StartLine:   lineNumber,
		Annotations: csharpAttrsToParsedAnnotations(attrs),
	}, true
}

func csharpAttrsToParsedAnnotations(attrs []csharpAttribute) []ParsedAnnotation {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]ParsedAnnotation, 0, len(attrs))
	for _, attr := range attrs {
		out = append(out, ParsedAnnotation{
			Name:       attr.Name,
			LineNumber: 0,
		})
	}
	return out
}

func normalizeCSharpTypeName(raw string) string {
	t := strings.TrimSpace(raw)
	t = strings.TrimPrefix(t, "global::")
	t = strings.TrimSuffix(t, "?")
	t = strings.TrimSpace(t)
	if idx := strings.Index(t, "<"); idx >= 0 {
		t = strings.TrimSpace(t[:idx])
	}
	t = strings.TrimSuffix(t, "[]")
	if idx := strings.LastIndex(t, "."); idx >= 0 && idx < len(t)-1 {
		t = t[idx+1:]
	}
	return strings.TrimSpace(t)
}

func fillCSharpFunctionSourceAndAccesses(result *ParsedFile, lines []string) {
	if result == nil {
		return
	}
	for i := range result.Functions {
		fn := &result.Functions[i]
		if fn.StartLine <= 0 || fn.EndLine < fn.StartLine || fn.StartLine > len(lines) {
			continue
		}
		end := fn.EndLine
		if end > len(lines) {
			end = len(lines)
		}
		source := strings.Join(lines[fn.StartLine-1:end], "\n")
		if strings.TrimSpace(source) != "" {
			fn.SourceCode = source
		}
		for _, access := range extractCSharpDataAccessesFromSource(source, fn.StartLine) {
			result.DataAccesses[fn.Name] = append(result.DataAccesses[fn.Name], access)
		}
	}
}

func extractCSharpDataAccessesFromSource(source string, startLine int) []ParsedDataAccess {
	if strings.TrimSpace(source) == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []ParsedDataAccess
	add := func(entity, access string, line int) {
		entity = normalizeCSharpDataEntityName(entity)
		access = strings.ToLower(strings.TrimSpace(access))
		if entity == "" || access == "" {
			return
		}
		key := strings.ToLower(entity) + "|" + access + "|" + strconv.Itoa(line)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, ParsedDataAccess{
			EntityName: entity,
			Access:     access,
			LineNumber: line,
			Source:     "csharp_entity_framework",
		})
	}

	lines := strings.Split(source, "\n")
	for i, line := range lines {
		lineNumber := startLine + i
		for _, match := range csharpDbSetRe.FindAllStringSubmatch(line, -1) {
			receiver := ""
			if len(match) > 1 {
				receiver = match[1]
			}
			if !isLikelyCSharpDbContextReceiver(receiver) {
				continue
			}
			entity := ""
			if len(match) > 2 && match[2] != "" {
				entity = match[2]
			} else if len(match) > 3 {
				entity = match[3]
			}
			method := ""
			if len(match) > 4 {
				method = match[4]
			}
			access := csharpDataAccessKind(method)
			add(entity, access, lineNumber)
		}
		for _, match := range csharpSQLTableRe.FindAllStringSubmatch(line, -1) {
			if len(match) < 2 {
				continue
			}
			access := "read"
			upper := strings.ToUpper(line)
			if strings.Contains(upper, "UPDATE ") || strings.Contains(upper, "INSERT INTO ") {
				access = "write"
			}
			add(match[1], access, lineNumber)
		}
	}
	return out
}

func csharpDataAccessKind(method string) string {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "add", "addorupdate", "attach", "remove":
		return "write"
	default:
		return "read"
	}
}

func isLikelyCSharpDbContextReceiver(receiver string) bool {
	receiver = strings.ToLower(strings.TrimSpace(receiver))
	if receiver == "" {
		return false
	}
	return receiver == "db" ||
		receiver == "context" ||
		receiver == "_context" ||
		strings.HasSuffix(receiver, "context") ||
		strings.HasSuffix(receiver, "db") ||
		strings.Contains(receiver, "dbcontext") ||
		strings.Contains(receiver, "dbentities") ||
		strings.Contains(receiver, "entities")
}

func normalizeCSharpDataEntityName(entity string) string {
	entity = strings.TrimSpace(entity)
	if entity == "" {
		return ""
	}
	entity = strings.Trim(entity, "\"'`")
	if idx := strings.LastIndex(entity, "."); idx >= 0 && idx < len(entity)-1 {
		entity = entity[idx+1:]
	}
	if strings.HasSuffix(entity, "s") && isLikelyUppercaseDataEntity(entity[:len(entity)-1]) {
		entity = strings.TrimSuffix(entity, "s")
	}
	return strings.TrimSpace(entity)
}

func isLikelyUppercaseDataEntity(entity string) bool {
	if entity == "" {
		return false
	}
	hasUpper := false
	for _, r := range entity {
		switch {
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return hasUpper
}

func extractCSharpKafkaConsumers(result *ParsedFile, content string) {
	if result == nil || strings.TrimSpace(content) == "" {
		return
	}
	if !strings.Contains(content, ".Subscribe(") || !strings.Contains(content, "IConsumer<") {
		return
	}
	settingsByClass := csharpKafkaSettingsByClass(result, content)
	seen := make(map[string]bool)
	for _, fn := range result.Functions {
		source := fn.SourceCode
		if !strings.Contains(source, ".Subscribe(") {
			continue
		}
		className, methodName := splitCSharpFunctionName(fn.Name)
		if className == "" {
			continue
		}
		settingsByVar := csharpKafkaSettingsByVar(source)
		for key, value := range settingsByClass[className] {
			if settingsByVar[key] == "" {
				settingsByVar[key] = value
			}
		}
		for _, queueName := range csharpSubscribedQueues(source, settingsByVar) {
			if queueName == "" {
				continue
			}
			handlerMethod := csharpKafkaHandlerMethod(result, className, methodName)
			if handlerMethod == "" {
				continue
			}
			key := className + "|" + handlerMethod + "|" + queueName
			if seen[key] {
				continue
			}
			seen[key] = true
			result.SqsConsumers = append(result.SqsConsumers, ParsedSqsConsumer{
				QueueName:     queueName,
				HandlerMethod: handlerMethod,
				ClassName:     className,
			})
		}
	}
}

func csharpKafkaSettingsByClass(result *ParsedFile, content string) map[string]map[string]string {
	if result == nil || strings.TrimSpace(content) == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	out := make(map[string]map[string]string)
	for _, class := range result.Classes {
		if class.Name == "" || class.StartLine <= 0 || class.StartLine > len(lines) {
			continue
		}
		endLine := class.EndLine
		if endLine <= 0 || endLine > len(lines) {
			endLine = len(lines)
		}
		if endLine < class.StartLine {
			continue
		}
		settings := csharpKafkaSettingsByVar(strings.Join(lines[class.StartLine-1:endLine], "\n"))
		if len(settings) > 0 {
			out[class.Name] = settings
		}
	}
	return out
}

func csharpKafkaSettingsByVar(source string) map[string]string {
	settingsByVar := make(map[string]string)
	for _, match := range csharpKafkaSettingRe.FindAllStringSubmatch(source, -1) {
		if len(match) == 3 {
			settingsByVar[match[1]] = match[2]
		}
	}
	return settingsByVar
}

func csharpSubscribedQueues(source string, settingsByVar map[string]string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, match := range csharpSubscribeRe.FindAllStringSubmatch(source, -1) {
		if len(match) != 2 {
			continue
		}
		queueName := strings.Trim(match[1], `"`)
		if setting := settingsByVar[queueName]; setting != "" {
			queueName = setting
		}
		if queueName == "" || seen[queueName] {
			continue
		}
		seen[queueName] = true
		out = append(out, queueName)
	}
	return out
}

func csharpKafkaHandlerMethod(result *ParsedFile, className string, subscribeMethod string) string {
	if className == "" {
		return ""
	}
	if subscribeMethod != "" && subscribeMethod != className {
		return subscribeMethod
	}
	for _, preferred := range []string{"StartAsync", "ExecuteAsync", "RunAsync", "Run", "ConsumeAsync", "Consume"} {
		if csharpClassHasFunction(result, className, preferred) {
			return preferred
		}
	}
	return ""
}

func csharpClassHasFunction(result *ParsedFile, className string, methodName string) bool {
	if result == nil || className == "" || methodName == "" {
		return false
	}
	want := className + "." + methodName
	for _, fn := range result.Functions {
		if fn.Name == want {
			return true
		}
	}
	return false
}

func splitCSharpFunctionName(name string) (className string, methodName string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ""
	}
	idx := strings.LastIndex(name, ".")
	if idx < 0 {
		return "", name
	}
	return name[:idx], name[idx+1:]
}

func parseCSharpAttributeLine(line string) ([]csharpAttribute, bool) {
	match := csharpAttributeRe.FindStringSubmatch(line)
	if len(match) != 2 {
		return nil, false
	}
	body := strings.TrimSpace(match[1])
	parts := splitTopLevel(body, ',')
	var attrs []csharpAttribute
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name := part
		rawArgs := ""
		if idx := strings.Index(part, "("); idx >= 0 {
			end := strings.LastIndex(part, ")")
			if end > idx {
				name = strings.TrimSpace(part[:idx])
				rawArgs = strings.TrimSpace(part[idx+1 : end])
			}
		}
		name = normalizeAttributeName(name)
		args := extractStringLiterals(rawArgs)
		attrs = append(attrs, csharpAttribute{
			Name:    name,
			RawArgs: rawArgs,
			Args:    args,
		})
	}
	return attrs, true
}

func normalizeAttributeName(name string) string {
	name = strings.TrimSpace(name)
	if strings.HasSuffix(strings.ToLower(name), "attribute") {
		name = name[:len(name)-len("Attribute")]
	}
	return name
}

func extractCSharpMethodSignature(line string) (methodName string, paramsText string, ok bool) {
	trimmed := strings.TrimSpace(line)
	for _, word := range []string{"return", "throw", "await", "yield", "case"} {
		if strings.HasPrefix(trimmed, word+" ") {
			return "", "", false
		}
	}
	match := csharpMethodDeclRe.FindStringSubmatch(line)
	if len(match) != 3 {
		match = csharpTypedMethodDeclRe.FindStringSubmatch(line)
		if len(match) != 3 {
			return "", "", false
		}
	}
	// A hiding method has a return type after `new`; object construction does not.
	prefix, _, _ := strings.Cut(strings.TrimSpace(line), "(")
	tokens := strings.Fields(prefix)
	if len(tokens) == 2 && tokens[0] == "new" && tokens[1] == strings.TrimSpace(match[1]) {
		return "", "", false
	}
	return strings.TrimSpace(match[1]), strings.TrimSpace(match[2]), true
}

func parseCSharpParams(paramsText string) (params []string, paramTypes []string) {
	if strings.TrimSpace(paramsText) == "" {
		return nil, nil
	}
	parts := splitTopLevel(paramsText, ',')
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if idx := strings.Index(part, "="); idx >= 0 {
			part = strings.TrimSpace(part[:idx])
		}
		tokens := strings.Fields(part)
		if len(tokens) == 0 {
			continue
		}
		paramName := tokens[len(tokens)-1]
		paramType := ""
		if len(tokens) > 1 {
			typeTokens := make([]string, 0, len(tokens)-1)
			for _, tok := range tokens[:len(tokens)-1] {
				switch strings.ToLower(tok) {
				case "ref", "out", "in", "params", "this":
					continue
				default:
					typeTokens = append(typeTokens, tok)
				}
			}
			paramType = strings.Join(typeTokens, " ")
		}
		params = append(params, paramName)
		paramTypes = append(paramTypes, paramType)
	}
	return params, paramTypes
}

func routeTemplatesFromAttrs(attrs []csharpAttribute) []string {
	if len(attrs) == 0 {
		return nil
	}
	var out []string
	for _, attr := range attrs {
		if strings.EqualFold(attr.Name, "Route") || strings.EqualFold(attr.Name, "RoutePrefix") {
			if len(attr.Args) > 0 {
				out = append(out, attr.Args[0])
			}
		}
	}
	return uniqueNonEmptyStrings(out)
}

func isCSharpControllerClass(className string, line string, attrs []csharpAttribute) bool {
	if strings.HasSuffix(className, "Controller") {
		return true
	}
	if strings.Contains(line, "ControllerBase") || strings.Contains(line, "Controller") {
		return true
	}
	for _, attr := range attrs {
		if strings.EqualFold(attr.Name, "ApiController") {
			return true
		}
	}
	return false
}

func extractCSharpControllerEndpoints(classFrame *csharpClassFrame, methodName string, handlerName string, attrs []csharpAttribute, lineNumber int) []ParsedEndpoint {
	if classFrame == nil {
		return nil
	}
	var httpRoutes []csharpHttpRoute
	var methodRoutes []string

	for _, attr := range attrs {
		lowerName := strings.ToLower(attr.Name)
		if method, ok := csharpHttpAttributeToMethod[lowerName]; ok {
			template := ""
			if len(attr.Args) > 0 {
				template = attr.Args[0]
			}
			httpRoutes = append(httpRoutes, csharpHttpRoute{
				Method:   method,
				Template: template,
			})
			continue
		}

		if strings.EqualFold(attr.Name, "Route") && len(attr.Args) > 0 {
			methodRoutes = append(methodRoutes, attr.Args[0])
			continue
		}

		if strings.EqualFold(attr.Name, "AcceptVerbs") {
			for _, arg := range attr.Args {
				method := strings.ToUpper(strings.TrimSpace(arg))
				if method != "" {
					httpRoutes = append(httpRoutes, csharpHttpRoute{
						Method:   method,
						Template: "",
					})
				}
			}
		}
	}

	if len(httpRoutes) == 0 && len(methodRoutes) == 0 {
		return nil
	}
	if len(httpRoutes) == 0 {
		httpRoutes = append(httpRoutes, csharpHttpRoute{Method: "REQUEST"})
	}
	if len(classFrame.RouteTemplates) == 0 {
		classFrame.RouteTemplates = []string{""}
	}

	var endpoints []ParsedEndpoint
	for _, route := range httpRoutes {
		templates := []string{route.Template}
		if route.Template == "" && len(methodRoutes) > 0 {
			templates = methodRoutes
		}
		for _, base := range classFrame.RouteTemplates {
			for _, tpl := range templates {
				path := joinAspNetRoute(base, tpl, classFrame.Name, methodName)
				if path == "" {
					continue
				}
				endpoints = append(endpoints, ParsedEndpoint{
					Path:        path,
					Method:      route.Method,
					HandlerName: handlerName,
					LineNumber:  lineNumber,
				})
			}
		}
	}

	if len(endpoints) == 0 {
		controller := strings.TrimSuffix(classFrame.Name, "Controller")
		if controller != "" {
			path := normalizeEndpointPath("/" + controller + "/" + methodName)
			endpoints = append(endpoints, ParsedEndpoint{
				Path:        path,
				Method:      httpRoutes[0].Method,
				HandlerName: handlerName,
				LineNumber:  lineNumber,
			})
		}
	}
	return dedupeParsedEndpoints(endpoints)
}

func joinAspNetRoute(base string, methodTemplate string, className string, methodName string) string {
	controllerName := strings.TrimSuffix(className, "Controller")
	replacer := strings.NewReplacer(
		"[controller]", controllerName,
		"[Controller]", controllerName,
		"[action]", methodName,
		"[Action]", methodName,
	)

	base = strings.TrimSpace(replacer.Replace(base))
	methodTemplate = strings.TrimSpace(replacer.Replace(methodTemplate))

	base = strings.TrimPrefix(base, "~/")
	methodTemplate = strings.TrimPrefix(methodTemplate, "~/")

	base = strings.Trim(base, "\"'")
	methodTemplate = strings.Trim(methodTemplate, "\"'")

	if methodTemplate != "" && strings.HasPrefix(methodTemplate, "/") {
		return normalizeEndpointPath(methodTemplate)
	}
	if base == "" {
		if methodTemplate == "" {
			return ""
		}
		return normalizeEndpointPath("/" + methodTemplate)
	}
	if methodTemplate == "" {
		return normalizeEndpointPath("/" + base)
	}
	return normalizeEndpointPath("/" + strings.Trim(base, "/") + "/" + strings.Trim(methodTemplate, "/"))
}

func parseMinimalMapEndpoints(mapToken string, args string, lineNumber int, prefix string) []ParsedEndpoint {
	args = strings.TrimSpace(args)
	if idx := strings.LastIndex(args, ")"); idx >= 0 {
		args = strings.TrimSpace(args[:idx])
	}
	literals := extractStringLiterals(args)
	// extractStringLiterals drops empty strings, but MapGet("") inside a group is
	// the group's own path.
	pathLiteral, rest := "", literals
	if first := splitTopLevel(args, ','); len(first) > 0 && isEmptyCSharpStringLiteral(first[0]) {
		pathLiteral = ""
	} else if len(literals) > 0 {
		pathLiteral, rest = literals[0], literals[1:]
	} else {
		return nil
	}
	path := normalizeEndpointPath(pathLiteral)
	if prefix != "" {
		path = joinRoutePrefix(prefix, pathLiteral)
	}
	if path == "" {
		return nil
	}

	handlerName := ""
	if parsed := extractMapHandlerName(args); parsed != "" {
		handlerName = parsed
	}

	var methods []string
	if mapToken == "METHODS" {
		for _, lit := range rest {
			method := strings.ToUpper(strings.TrimSpace(lit))
			switch method {
			case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
				methods = append(methods, method)
			}
		}
		if len(methods) == 0 {
			methods = []string{"REQUEST"}
		}
	} else {
		methods = []string{mapToken}
	}

	var endpoints []ParsedEndpoint
	for _, method := range methods {
		endpoints = append(endpoints, ParsedEndpoint{
			Path:        path,
			Method:      method,
			HandlerName: handlerName,
			LineNumber:  lineNumber,
		})
	}
	return dedupeParsedEndpoints(endpoints)
}

func isEmptyCSharpStringLiteral(arg string) bool {
	switch strings.TrimSpace(arg) {
	case `""`, `@""`, `$""`, `""""""`:
		return true
	}
	return false
}

func extractMapHandlerName(args string) string {
	parts := splitTopLevel(args, ',')
	if len(parts) < 2 {
		return ""
	}
	last := strings.TrimSpace(parts[len(parts)-1])
	if last == "" {
		return ""
	}
	if idx := strings.Index(last, "=>"); idx >= 0 {
		return ""
	}
	if strings.HasPrefix(last, "(") || strings.HasPrefix(last, "async ") {
		return ""
	}
	last = strings.TrimSuffix(last, ")")
	last = strings.TrimSpace(last)
	if strings.Contains(last, "new[]") || strings.HasPrefix(last, "{") {
		return ""
	}
	return last
}

func currentClassFrame(classStack []csharpClassFrame, pendingClass *csharpClassFrame) *csharpClassFrame {
	if len(classStack) > 0 {
		return &classStack[len(classStack)-1]
	}
	if pendingClass != nil {
		return pendingClass
	}
	return nil
}

func extractCSharpCalls(line string, lineNumber int) []ParsedFunctionCall {
	matches := csharpCallRe.FindAllStringSubmatch(line, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	out := make([]ParsedFunctionCall, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		target := strings.TrimSpace(match[1])
		if target == "" {
			continue
		}
		lower := strings.ToLower(target)
		if csharpControlFlowWords[lower] {
			continue
		}
		receiver := ""
		method := target
		if idx := strings.LastIndex(target, "."); idx >= 0 && idx < len(target)-1 {
			receiver = strings.TrimSpace(target[:idx])
			method = strings.TrimSpace(target[idx+1:])
		}
		if method == "" {
			continue
		}
		key := receiver + "|" + method
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ParsedFunctionCall{
			CalleeName: target,
			Receiver:   receiver,
			MethodName: method,
			LineNumber: lineNumber,
		})
	}
	return out
}

func countBraces(line string) (open int, close int) {
	for _, ch := range line {
		switch ch {
		case '{':
			open++
		case '}':
			close++
		}
	}
	return open, close
}

func splitTopLevel(input string, delimiter rune) []string {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	var out []string
	var b strings.Builder
	depth := 0
	inSingle := false
	inDouble := false
	escaped := false

	flush := func() {
		part := strings.TrimSpace(b.String())
		if part != "" {
			out = append(out, part)
		}
		b.Reset()
	}

	for _, ch := range input {
		if escaped {
			b.WriteRune(ch)
			escaped = false
			continue
		}
		if ch == '\\' {
			b.WriteRune(ch)
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			b.WriteRune(ch)
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			b.WriteRune(ch)
			continue
		}
		if inSingle || inDouble {
			b.WriteRune(ch)
			continue
		}

		switch ch {
		case '(', '[', '{', '<':
			depth++
		case ')', ']', '}', '>':
			if depth > 0 {
				depth--
			}
		}
		if ch == delimiter && depth == 0 {
			flush()
			continue
		}
		b.WriteRune(ch)
	}
	flush()
	return out
}

func extractStringLiterals(input string) []string {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	matches := csharpStringRe.FindAllStringSubmatch(input, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		value := ""
		if len(match) > 1 && match[1] != "" {
			value = match[1]
		} else if len(match) > 2 {
			value = match[2]
		}
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func normalizeEndpointPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.Trim(path, "\"'")
	path = strings.TrimPrefix(path, "~/")
	path = strings.TrimPrefix(path, "~")
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = strings.ReplaceAll(path, "\\", "/")
	for strings.Contains(path, "//") {
		path = strings.ReplaceAll(path, "//", "/")
	}
	if len(path) > 1 && strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}
	return path
}

func inferAspEndpointPath(filePath string) string {
	normalized := filepath.ToSlash(strings.TrimSpace(filePath))
	if normalized == "" {
		return ""
	}
	lower := strings.ToLower(normalized)
	markers := []string{"/wwwroot/", "/public/", "/www/"}
	for _, marker := range markers {
		if idx := strings.Index(lower, marker); idx >= 0 {
			normalized = normalized[idx+len(marker)-1:]
			break
		}
	}
	normalized = strings.TrimPrefix(normalized, "./")
	normalized = strings.TrimPrefix(normalized, "/./")
	return normalizeEndpointPath(normalized)
}

func appendEndpointUnique(result *ParsedFile, seen map[string]bool, ep ParsedEndpoint) {
	key := ep.Method + "\x00" + ep.Path + "\x00" + ep.HandlerName
	if seen[key] {
		return
	}
	seen[key] = true
	result.Endpoints = append(result.Endpoints, ep)
}

func uniqueNonEmptyStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		item := strings.TrimSpace(raw)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func dedupeParsedEndpoints(in []ParsedEndpoint) []ParsedEndpoint {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]ParsedEndpoint, 0, len(in))
	for _, ep := range in {
		key := ep.Method + "\x00" + ep.Path + "\x00" + ep.HandlerName
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ep)
	}
	return out
}

// Strip complete leading attribute lists while preserving original byte columns
// for expression-body ownership and original lines for source evidence.
func splitCSharpLeadingAttributes(line string) ([]csharpAttribute, string, int) {
	masked := maskCSharpLiterals(line)
	var attrs []csharpAttribute
	offset := 0
	for {
		start := offset
		for start < len(masked) && (masked[start] == ' ' || masked[start] == '\t') {
			start++
		}
		if start >= len(masked) || masked[start] != '[' {
			break
		}
		depth, end := 0, -1
		for i := start; i < len(masked); i++ {
			if masked[i] == '[' {
				depth++
			}
			if masked[i] == ']' {
				depth--
				if depth == 0 {
					end = i + 1
					break
				}
			}
		}
		if end < 0 {
			break
		}
		parsed, ok := parseCSharpAttributeLine(line[start:end])
		if !ok {
			break
		}
		attrs = append(attrs, parsed...)
		offset = end
	}
	return attrs, line[offset:], offset
}
