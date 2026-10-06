package parser

import (
	"path/filepath"
	"regexp"
	"strings"
)

type RPGParser struct{}

type rpgFunctionFrame struct {
	Name          string
	StartLine     int
	EndLine       int
	FunctionIndex int
}

type rpgSQLBlock struct {
	FunctionName string
	StartLine    int
	Parts        []string
}

var rpgSupportedExtensions = map[string]bool{
	".rpg":         true,
	".rpgle":       true,
	".sqlrpgle":    true,
	".rpgleinc":    true,
	".sqlrpgleinc": true,
}

var (
	rpgFreeProcStartRe    = regexp.MustCompile(`(?i)^\s*dcl-proc\s+([A-Za-z_#@$][A-Za-z0-9_#@$]*)`)
	rpgFreeProcEndRe      = regexp.MustCompile(`(?i)^\s*end-proc\b`)
	rpgFreeBegsrRe        = regexp.MustCompile(`(?i)^\s*begsr\s+([A-Za-z_*#@$][A-Za-z0-9_*#@$]*)`)
	rpgFreeEndsrRe        = regexp.MustCompile(`(?i)^\s*endsr\b`)
	rpgFixedProcStartRe   = regexp.MustCompile(`(?i)^\s*P\s+([A-Za-z_#@$][A-Za-z0-9_#@$]*)\s+B\b`)
	rpgFixedProcEndRe     = regexp.MustCompile(`(?i)^\s*P\s+([A-Za-z_#@$][A-Za-z0-9_#@$]*)\s+E\b`)
	rpgFixedBegsrRe       = regexp.MustCompile(`(?i)^\s*C\s+([A-Za-z_*#@$][A-Za-z0-9_*#@$]*)\s+BEGSR\b`)
	rpgFixedEndsrRe       = regexp.MustCompile(`(?i)^\s*C\b.*\bENDSR\b`)
	rpgCopyRe             = regexp.MustCompile(`(?i)^\s*/(?:copy|include)\s+(.+)$`)
	rpgCallpRe            = regexp.MustCompile(`(?i)\bcallp(?:\s*\([^)]*\))?\s+([A-Za-z_*#@$][A-Za-z0-9_*#@$]*)`)
	rpgCallRe             = regexp.MustCompile(`(?i)\bcall(?:\s*\([^)]*\))?\b\s+'?([A-Za-z_*#@$][A-Za-z0-9_*#@$]*)'?`)
	rpgExsrRe             = regexp.MustCompile(`(?i)\bexsr\s+([A-Za-z_*#@$][A-Za-z0-9_*#@$]*)`)
	rpgGenericCallRe      = regexp.MustCompile(`\b([A-Za-z_#@$][A-Za-z0-9_#@$]*)\s*\(`)
	rpgOpcodeReadRe       = regexp.MustCompile(`(?i)\b(CHAIN|SETLL|SETGT|READ|READE|READP|READPE)\b`)
	rpgOpcodeWriteRe      = regexp.MustCompile(`(?i)\b(WRITE|UPDATE|DELETE)\b`)
	rpgDisplayOpcodeRe    = regexp.MustCompile(`(?i)\b(EXFMT|READC|RCVF)\b`)
	rpgExecSQLStartRe     = regexp.MustCompile(`(?i)\bexec\s+sql\b`)
	rpgBindDirRe          = regexp.MustCompile(`(?i)\bbnddir\(([^)]*)\)`)
	rpgFreeNoMainRe       = regexp.MustCompile(`(?i)^\s*ctl-opt\b.*\bnomain\b`)
	rpgFixedNoMainRe      = regexp.MustCompile(`(?i)^\s*H\b.*\bNOMAIN\b`)
	rpgFreeDeclStartRe    = regexp.MustCompile(`(?i)^\s*(ctl-opt|dcl-s|dcl-c|dcl-ds|dcl-pr|dcl-pi|dcl-f|dcl-proc|begsr|end-proc|endsr|end-ds|end-pr|end-pi|end-if|endfor|enddo|endif|monitor|endmon)\b`)
	rpgFreeProcExportRe   = regexp.MustCompile(`(?i)^\s*dcl-proc\s+([A-Za-z_#@$][A-Za-z0-9_#@$]*)\b.*\bexport\b`)
	rpgFreePrototypeRe    = regexp.MustCompile(`(?i)^\s*dcl-pr\s+([A-Za-z_*#@$][A-Za-z0-9_*#@$]*)\b.*?\b(?:extpgm|extproc)\s*\(\s*['"]([^'"]+)['"]\s*\)`)
	rpgFixedPrototypeRe   = regexp.MustCompile(`(?i)^\s*d\s*([A-Za-z_*#@$][A-Za-z0-9_*#@$]*)\s+pr\b.*?\b(?:extpgm|extproc)\s*\(\s*['"]([^'"]+)['"]\s*\)`)
	rpgFreeFileDeclRe     = regexp.MustCompile(`(?i)^\s*dcl-f\s+([A-Za-z_#@$][A-Za-z0-9_#@$]*)\b(?:.*?\b(?:extdesc|extfile)\s*\(\s*['"]?([^'")\s]+)['"]?\s*\))?`)
	rpgFixedFileDeclRe    = regexp.MustCompile(`(?i)^\s*F([A-Za-z_#@$][A-Za-z0-9_#@$]*)\b(?:.*?\b(?:extdesc|extfile)\s*\(\s*['"]?([^'")\s]+)['"]?\s*\))?`)
	rpgFreeWorkstnDeclRe  = regexp.MustCompile(`(?i)^\s*dcl-f\s+([A-Za-z_#@$][A-Za-z0-9_#@$]*)\b.*\bworkstn\b`)
	rpgFixedWorkstnDeclRe = regexp.MustCompile(`(?i)^\s*F([A-Za-z_#@$][A-Za-z0-9_#@$]*)\b.*\bWORKSTN\b`)
	rpgSQLInsertRe        = regexp.MustCompile(`(?i)\binsert\s+into\s+([^\s,()]+)`)
	rpgSQLUpdateRe        = regexp.MustCompile(`(?i)\bupdate\s+([^\s,()]+)`)
	rpgSQLDeleteRe        = regexp.MustCompile(`(?i)\bdelete\s+from\s+([^\s,()]+)`)
	rpgSQLFromRe          = regexp.MustCompile(`(?i)\bfrom\s+([^\s,()]+)`)
	rpgMainlineConstName  = "_MAIN"
)

var rpgIgnoredCallNames = map[string]bool{
	"if": true, "for": true, "dow": true, "dou": true, "monitor": true, "select": true,
	"when": true, "other": true, "return": true, "exec": true, "chain": true, "read": true,
	"reade": true, "readp": true, "readpe": true, "setll": true, "setgt": true, "write": true,
	"update": true, "delete": true, "call": true, "callp": true, "exsr": true, "begsr": true,
	"dcl-pr": true, "dcl-pi": true, "dcl-proc": true, "sql": true, "and": true, "or": true,
	"const": true, "options": true, "inz": true, "char": true, "varchar": true,
	"addr": true, "subst": true, "len": true, "trim": true, "trimr": true, "scan": true,
	"extproc": true, "based": true, "defined": true, "likeds": true, "like": true,
	"size": true, "dim": true, "int": true, "str": true, "pos": true, "value": true,
	"procptr": true, "qualified": true, "export": true, "proc": true, "packed": true,
	"ind": true, "timestamp": true, "date": true, "time": true, "omit": true, "varsize": true,
	"parms": true, "eof": true, "paddr": true, "editc": true, "scanrpl": true, "uns": true,
	"dsply": true,
}

func NewRPGParser() *RPGParser {
	return &RPGParser{}
}

func (p *RPGParser) CanParse(filePath string) bool {
	if rpgSupportedExtensions[strings.ToLower(filepath.Ext(filePath))] {
		return true
	}
	switch kind, ok := detectIBMISourceMemberKind(filePath); {
	case !ok:
		return false
	case kind == "rpg", kind == "rpgle", kind == "sqlrpgle", kind == "rpgleinc", kind == "sqlrpgleinc":
		return true
	default:
		return false
	}
}

func (p *RPGParser) ParseFile(filePath string, content []byte) (result ParsedFile) {
	result = ParsedFile{
		Path:          filePath,
		Language:      p.languageForPath(filePath),
		Functions:     []ParsedFunction{},
		Classes:       []ParsedClass{},
		Imports:       []ParsedImport{},
		FunctionCalls: make(map[string][]ParsedFunctionCall),
		Endpoints:     []ParsedEndpoint{},
		HttpCalls:     make(map[string][]ParsedHttpCall),
		DataAccesses:  make(map[string][]ParsedDataAccess),
	}
	defer recoverParsePanic(&result)

	lines := strings.Split(string(content), "\n")
	mainlineName := p.mainlineNameForPath(filePath)
	artifactName := NormalizeIBMiObjectName(p.programNameForPath(filePath))
	var stack []rpgFunctionFrame
	var declarationBlocks []string
	var pendingSQL *rpgSQLBlock
	externalAliases := make(map[string]string)
	displayFiles := make(map[string]bool)
	seenImports := make(map[string]bool)
	seenBindings := make(map[string]bool)
	seenExports := make(map[string]bool)
	noMain := false
	mainlineStart := 0
	mainlineEnd := 0

	appendImport := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		key := strings.ToLower(path)
		if seenImports[key] {
			return
		}
		seenImports[key] = true
		result.Imports = append(result.Imports, ParsedImport{Path: path})
	}

	appendBinding := func(binding ParsedIBMiBinding) {
		if binding.BindingName == "" {
			return
		}
		key := strings.ToLower(binding.OwnerObject) + "|" + strings.ToLower(binding.BindingName) + "|" + strings.ToLower(binding.BindingType) + "|" + strings.ToLower(binding.TargetObject)
		if seenBindings[key] {
			return
		}
		seenBindings[key] = true
		result.IBMiBindings = append(result.IBMiBindings, binding)
	}

	appendExport := func(item ParsedIBMiExport) {
		if item.ExportName == "" {
			return
		}
		key := strings.ToLower(item.FunctionName) + "|" + strings.ToLower(item.ExportName) + "|" + strings.ToLower(item.ObjectName) + "|" + strings.ToLower(item.SourceType)
		if seenExports[key] {
			return
		}
		seenExports[key] = true
		result.IBMiExports = append(result.IBMiExports, item)
	}

	ensureMainline := func(line int) string {
		if mainlineStart == 0 || line < mainlineStart {
			mainlineStart = line
		}
		if line > mainlineEnd {
			mainlineEnd = line
		}
		return mainlineName
	}

	currentFunctionName := func(line int) string {
		if len(stack) > 0 {
			return stack[len(stack)-1].Name
		}
		if noMain {
			return ""
		}
		return ensureMainline(line)
	}

	appendFunction := func(name string, startLine int) {
		isExported := isRPGProcedureExport(rawLineForLine(lines, startLine))
		idx := len(result.Functions)
		result.Functions = append(result.Functions, ParsedFunction{
			Name:       name,
			StartLine:  startLine,
			EndLine:    startLine,
			IsExported: isExported,
		})
		if isExported {
			appendExport(ParsedIBMiExport{
				FunctionName: name,
				ExportName:   NormalizeIBMiObjectName(name),
				ObjectName:   artifactName,
				ObjectType:   "procedure",
				SourceType:   "procedure_export",
				LineNumber:   startLine,
			})
		}
		stack = append(stack, rpgFunctionFrame{
			Name:          name,
			StartLine:     startLine,
			EndLine:       startLine,
			FunctionIndex: idx,
		})
	}

	closeFunction := func(line int) {
		if len(stack) == 0 {
			return
		}
		frame := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if frame.FunctionIndex >= 0 && frame.FunctionIndex < len(result.Functions) {
			result.Functions[frame.FunctionIndex].EndLine = line
		}
	}

	for i, rawLine := range lines {
		lineNum := i + 1
		clean := stripRPGLine(rawLine)
		if clean == "" {
			continue
		}
		for _, bindingName := range parseRPGBindingRefs(rawLine) {
			appendBinding(ParsedIBMiBinding{
				OwnerObject: artifactName,
				BindingName: bindingName,
				BindingType: "bnddir_ref",
				LineNumber:  lineNum,
			})
		}
		if isRPGNoMainDirective(rawLine) {
			noMain = true
		}
		if localName, externalName, ok := parseRPGPrototypeAlias(rawLine); ok {
			externalAliases[strings.ToLower(localName)] = externalName
		}
		if displayFile, ok := parseRPGDisplayFile(rawLine); ok {
			displayFiles[displayFile] = true
		}
		if pendingSQL != nil {
			sqlLine := normalizeRPGSQLLine(clean)
			if sqlLine != "" {
				pendingSQL.Parts = append(pendingSQL.Parts, sqlLine)
			}
			if strings.Contains(stripRPGStringLiterals(sqlLine), ";") {
				sqlText := strings.Join(pendingSQL.Parts, " ")
				if access := rpgSQLAccessKind(sqlText); access != "" {
					if entity := rpgSQLPrimaryTarget(sqlText, access); entity != "" {
						appendRPGDataAccess(result.DataAccesses, pendingSQL.FunctionName, ParsedDataAccess{
							EntityName: entity,
							Access:     access,
							LineNumber: pendingSQL.StartLine,
							Source:     "embedded_sql",
						})
					}
				}
				pendingSQL = nil
			}
			continue
		}

		if imp, ok := parseRPGCopyDirective(rawLine); ok {
			appendImport(imp)
			continue
		}
		if fileImport, ok := parseRPGFileImport(rawLine); ok {
			appendImport(fileImport)
			continue
		}

		if name, ok := parseRPGProcStart(rawLine); ok {
			appendFunction(name, lineNum)
			continue
		}
		if isRPGProcEnd(rawLine) {
			closeFunction(lineNum)
			continue
		}
		if name, ok := parseRPGSubroutineStart(rawLine); ok {
			appendFunction(name, lineNum)
			continue
		}
		if isRPGSubroutineEnd(rawLine) {
			closeFunction(lineNum)
			continue
		}

		if rpgExecSQLStartRe.MatchString(stripRPGStringLiterals(clean)) {
			fnName := currentFunctionName(lineNum)
			sqlLine := normalizeRPGSQLLine(clean)
			pendingSQL = &rpgSQLBlock{
				FunctionName: fnName,
				StartLine:    lineNum,
				Parts:        []string{sqlLine},
			}
			if strings.Contains(stripRPGStringLiterals(sqlLine), ";") {
				sqlText := strings.Join(pendingSQL.Parts, " ")
				if access := rpgSQLAccessKind(sqlText); access != "" {
					if entity := rpgSQLPrimaryTarget(sqlText, access); entity != "" {
						appendRPGDataAccess(result.DataAccesses, pendingSQL.FunctionName, ParsedDataAccess{
							EntityName: entity,
							Access:     access,
							LineNumber: pendingSQL.StartLine,
							Source:     "embedded_sql",
						})
					}
				}
				pendingSQL = nil
			}
			continue
		}

		opLine := normalizeRPGOperationLine(clean)
		if endToken, ok := parseRPGDeclarationBlockEnd(opLine); ok {
			if len(declarationBlocks) > 0 && declarationBlocks[len(declarationBlocks)-1] == endToken {
				declarationBlocks = declarationBlocks[:len(declarationBlocks)-1]
			}
			continue
		}
		if len(declarationBlocks) > 0 {
			continue
		}
		if endToken, ok := parseRPGDeclarationBlockStart(opLine); ok {
			declarationBlocks = append(declarationBlocks, endToken)
			continue
		}

		fnName := ""
		if len(stack) > 0 {
			fnName = stack[len(stack)-1].Name
		}

		if fnName == "" && !noMain && !looksLikeRPGDeclarationLine(opLine) {
			fnName = ensureMainline(lineNum)
		}

		if fnName == "" && !noMain && (rpgCallpRe.MatchString(opLine) || rpgCallRe.MatchString(opLine) || rpgExsrRe.MatchString(opLine) || rpgGenericCallRe.MatchString(opLine) || rpgOpcodeReadRe.MatchString(opLine) || rpgOpcodeWriteRe.MatchString(opLine)) {
			fnName = ensureMainline(lineNum)
		}

		if fnName == "" {
			continue
		}

		for _, call := range extractRPGCalls(opLine, lineNum, externalAliases) {
			result.FunctionCalls[fnName] = append(result.FunctionCalls[fnName], call)
		}
		for _, access := range extractRPGDataAccesses(opLine, lineNum, resolveSingleRPGDisplayFile(displayFiles)) {
			appendRPGDataAccess(result.DataAccesses, fnName, access)
		}
	}

	lastLine := len(lines)
	for len(stack) > 0 {
		frame := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if frame.FunctionIndex >= 0 && frame.FunctionIndex < len(result.Functions) {
			result.Functions[frame.FunctionIndex].EndLine = lastLine
		}
	}

	if mainlineStart > 0 && !noMain {
		mainFn := ParsedFunction{
			Name:       mainlineName,
			StartLine:  mainlineStart,
			EndLine:    maxInt(mainlineEnd, mainlineStart),
			IsExported: mainlineName != rpgMainlineConstName,
		}
		result.Functions = append([]ParsedFunction{mainFn}, result.Functions...)
		appendExport(ParsedIBMiExport{
			FunctionName: mainlineName,
			ExportName:   artifactName,
			ObjectName:   artifactName,
			ObjectType:   "program",
			SourceType:   "program_entry",
			LineNumber:   mainlineStart,
		})
	}

	for i := range result.Functions {
		start := clampLine(result.Functions[i].StartLine, len(lines))
		end := clampLine(result.Functions[i].EndLine, len(lines))
		if start <= 0 || end <= 0 || start > end {
			continue
		}
		source := strings.Join(lines[start-1:end], "\n")
		if len(source) > 4000 {
			source = source[:4000] + "..."
		}
		result.Functions[i].SourceCode = source
	}

	return result
}

func isRPGNoMainDirective(rawLine string) bool {
	return rpgFreeNoMainRe.MatchString(stripRPGLine(rawLine)) || rpgFixedNoMainRe.MatchString(strings.TrimSpace(rawLine))
}

func isRPGProcedureExport(rawLine string) bool {
	clean := stripRPGLine(rawLine)
	if clean == "" {
		return false
	}
	return rpgFreeProcExportRe.MatchString(clean) || (rpgFixedProcStartRe.MatchString(strings.TrimSpace(rawLine)) && strings.Contains(strings.ToUpper(rawLine), "EXPORT"))
}

func parseRPGBindingRefs(rawLine string) []string {
	clean := stripRPGLine(rawLine)
	if clean == "" {
		return nil
	}
	match := rpgBindDirRe.FindStringSubmatch(clean)
	if len(match) != 2 {
		return nil
	}
	return SplitIBMIBindingNames(match[1])
}

func rawLineForLine(lines []string, line int) string {
	idx := line - 1
	if idx < 0 || idx >= len(lines) {
		return ""
	}
	return lines[idx]
}

func (p *RPGParser) languageForPath(filePath string) string {
	if kind, ok := detectIBMISourceMemberKind(filePath); ok {
		switch kind {
		case "sqlrpgle", "rpgle", "rpg":
			return kind
		case "rpgleinc", "sqlrpgleinc":
			return kind
		}
	}
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".sqlrpgle", ".sqlrpgleinc":
		return "sqlrpgle"
	case ".rpgle", ".rpgleinc":
		return "rpgle"
	default:
		return "rpg"
	}
}

func (p *RPGParser) mainlineNameForPath(filePath string) string {
	if kind, ok := detectIBMISourceMemberKind(filePath); ok {
		switch kind {
		case "rpg", "rpgle", "sqlrpgle":
			name := p.programNameForPath(filePath)
			if name != "" {
				return name
			}
		}
	}
	return rpgMainlineConstName
}

func (p *RPGParser) programNameForPath(filePath string) string {
	base := filepath.Base(filePath)
	ext := filepath.Ext(base)
	base = strings.TrimSuffix(base, ext)
	base = strings.TrimSpace(base)
	if base == "" {
		return rpgMainlineConstName
	}
	return base
}

func parseRPGCopyDirective(rawLine string) (string, bool) {
	match := rpgCopyRe.FindStringSubmatch(strings.TrimSpace(rawLine))
	if len(match) != 2 {
		return "", false
	}
	path := strings.TrimSpace(match[1])
	path = strings.TrimSuffix(path, ";")
	if path == "" {
		return "", false
	}
	return path, true
}

func parseRPGFileImport(rawLine string) (string, bool) {
	if match := rpgFreeFileDeclRe.FindStringSubmatch(stripRPGLine(rawLine)); len(match) >= 2 {
		if len(match) >= 3 {
			if external := normalizeRPGExternalSymbol(match[2]); external != "" {
				return external, true
			}
		}
		if name := normalizeRPGExternalSymbol(match[1]); name != "" {
			return name, true
		}
	}
	if match := rpgFixedFileDeclRe.FindStringSubmatch(strings.TrimSpace(rawLine)); len(match) >= 2 {
		if len(match) >= 3 {
			if external := normalizeRPGExternalSymbol(match[2]); external != "" {
				return external, true
			}
		}
		if name := normalizeRPGExternalSymbol(match[1]); name != "" {
			return name, true
		}
	}
	return "", false
}

func parseRPGDisplayFile(rawLine string) (string, bool) {
	if match := rpgFreeWorkstnDeclRe.FindStringSubmatch(stripRPGLine(rawLine)); len(match) == 2 {
		if name := normalizeRPGExternalSymbol(match[1]); name != "" {
			return name, true
		}
	}
	if match := rpgFixedWorkstnDeclRe.FindStringSubmatch(strings.TrimSpace(rawLine)); len(match) == 2 {
		if name := normalizeRPGExternalSymbol(match[1]); name != "" {
			return name, true
		}
	}
	return "", false
}

func resolveSingleRPGDisplayFile(displayFiles map[string]bool) string {
	if len(displayFiles) != 1 {
		return ""
	}
	for name := range displayFiles {
		return name
	}
	return ""
}

func parseRPGDeclarationBlockStart(cleanLine string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(cleanLine))
	switch {
	case strings.HasPrefix(lower, "dcl-pr "):
		if strings.Contains(lower, " end-pr") {
			return "", false
		}
		return "end-pr", true
	case lower == "dcl-pr":
		return "end-pr", true
	case strings.HasPrefix(lower, "dcl-pi "):
		if strings.Contains(lower, " end-pi") {
			return "", false
		}
		return "end-pi", true
	case lower == "dcl-pi":
		return "end-pi", true
	case strings.HasPrefix(lower, "dcl-ds "):
		if strings.Contains(lower, " end-ds") {
			return "", false
		}
		return "end-ds", true
	case lower == "dcl-ds":
		return "end-ds", true
	default:
		return "", false
	}
}

func parseRPGDeclarationBlockEnd(cleanLine string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(cleanLine))
	switch {
	case strings.HasPrefix(lower, "end-pr"):
		return "end-pr", true
	case strings.HasPrefix(lower, "end-pi"):
		return "end-pi", true
	case strings.HasPrefix(lower, "end-ds"):
		return "end-ds", true
	default:
		return "", false
	}
}

func parseRPGProcStart(rawLine string) (string, bool) {
	if match := rpgFreeProcStartRe.FindStringSubmatch(stripRPGLine(rawLine)); len(match) == 2 {
		return match[1], true
	}
	if match := rpgFixedProcStartRe.FindStringSubmatch(strings.TrimSpace(rawLine)); len(match) == 2 {
		return match[1], true
	}
	return "", false
}

func parseRPGPrototypeAlias(rawLine string) (string, string, bool) {
	if match := rpgFreePrototypeRe.FindStringSubmatch(stripRPGLine(rawLine)); len(match) == 3 {
		return match[1], normalizeRPGExternalSymbol(match[2]), true
	}
	if match := rpgFixedPrototypeRe.FindStringSubmatch(strings.TrimSpace(rawLine)); len(match) == 3 {
		return match[1], normalizeRPGExternalSymbol(match[2]), true
	}
	return "", "", false
}

func isRPGProcEnd(rawLine string) bool {
	clean := stripRPGLine(rawLine)
	return rpgFreeProcEndRe.MatchString(clean) || rpgFixedProcEndRe.MatchString(strings.TrimSpace(rawLine))
}

func parseRPGSubroutineStart(rawLine string) (string, bool) {
	if match := rpgFreeBegsrRe.FindStringSubmatch(stripRPGLine(rawLine)); len(match) == 2 {
		return match[1], true
	}
	if match := rpgFixedBegsrRe.FindStringSubmatch(strings.TrimSpace(rawLine)); len(match) == 2 {
		return match[1], true
	}

	fields := strings.Fields(strings.TrimSpace(rawLine))
	if len(fields) >= 2 && strings.EqualFold(fields[0], "BEGSR") {
		if name := normalizeRPGExternalSymbol(fields[1]); name != "" {
			return name, true
		}
	}
	if len(fields) >= 3 && strings.EqualFold(fields[0], "C") && strings.EqualFold(fields[len(fields)-1], "BEGSR") {
		if name := normalizeRPGExternalSymbol(fields[len(fields)-2]); name != "" {
			return name, true
		}
	}
	return "", false
}

func isRPGSubroutineEnd(rawLine string) bool {
	clean := stripRPGLine(rawLine)
	return rpgFreeEndsrRe.MatchString(clean) || rpgFixedEndsrRe.MatchString(strings.TrimSpace(rawLine))
}

func stripRPGLine(rawLine string) string {
	if rawLine == "" {
		return ""
	}
	trimmed := strings.TrimSpace(rawLine)
	if trimmed == "" {
		return ""
	}
	upperTrimmed := strings.ToUpper(trimmed)
	if strings.HasPrefix(upperTrimmed, "**FREE") || strings.HasPrefix(upperTrimmed, "/FREE") || strings.HasPrefix(upperTrimmed, "/END-FREE") {
		return ""
	}
	if len(rawLine) > 6 && rawLine[6] == '*' && !strings.HasPrefix(upperTrimmed, "**FREE") {
		return ""
	}
	if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
		return ""
	}

	return stripRPGTrailingComment(trimmed, "//")
}

// RPG quoted values escape their delimiter by doubling it.
func stripRPGTrailingComment(line, marker string) string {
	var quote byte
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if quote != 0 {
			if ch == quote {
				if i+1 < len(line) && line[i+1] == quote {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if strings.HasPrefix(line[i:], marker) {
			return strings.TrimSpace(line[:i])
		}
	}
	return strings.TrimSpace(line)
}

func extractRPGCalls(cleanLine string, lineNum int, externalAliases map[string]string) []ParsedFunctionCall {
	cleanLine = normalizeRPGOperationLine(cleanLine)
	if cleanLine == "" {
		return nil
	}
	if looksLikeRPGDeclarationLine(cleanLine) {
		return nil
	}

	var calls []ParsedFunctionCall
	seen := make(map[string]bool)
	appendCall := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if externalName, ok := externalAliases[strings.ToLower(name)]; ok && externalName != "" {
			name = externalName
		}
		key := strings.ToLower(name)
		if seen[key] {
			return
		}
		seen[key] = true
		calls = append(calls, ParsedFunctionCall{
			CalleeName: name,
			MethodName: name,
			LineNumber: lineNum,
		})
	}

	for _, match := range rpgCallpRe.FindAllStringSubmatch(cleanLine, -1) {
		if len(match) == 2 {
			appendCall(match[1])
		}
	}
	for _, match := range rpgCallRe.FindAllStringSubmatch(cleanLine, -1) {
		if len(match) == 2 {
			appendCall(match[1])
		}
	}
	for _, match := range rpgExsrRe.FindAllStringSubmatch(cleanLine, -1) {
		if len(match) == 2 {
			appendCall(match[1])
		}
	}
	genericLine := stripRPGStringLiterals(cleanLine)
	for _, match := range rpgGenericCallRe.FindAllStringSubmatch(genericLine, -1) {
		if len(match) != 2 {
			continue
		}
		name := strings.TrimSpace(match[1])
		if rpgIgnoredCallNames[strings.ToLower(name)] {
			continue
		}
		appendCall(name)
	}
	return calls
}

func stripRPGStringLiterals(line string) string {
	if line == "" {
		return ""
	}
	var out strings.Builder
	inQuote := byte(0)
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if inQuote != 0 {
			if ch == inQuote {
				if i+1 < len(line) && line[i+1] == inQuote {
					i++
					continue
				}
				inQuote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			inQuote = ch
			continue
		}
		out.WriteByte(ch)
	}
	return out.String()
}

func looksLikeRPGDeclarationLine(cleanLine string) bool {
	trimmed := strings.TrimSpace(cleanLine)
	if trimmed == "" {
		return true
	}
	if rpgFreeDeclStartRe.MatchString(trimmed) {
		return true
	}

	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return true
	}

	switch strings.ToUpper(fields[0]) {
	case "D", "H", "F", "I", "O", "P":
		return true
	}
	return false
}

func normalizeRPGOperationLine(cleanLine string) string {
	trimmed := strings.TrimSpace(cleanLine)
	fields := strings.Fields(trimmed)
	if len(fields) > 1 && len(fields[0]) == 1 && strings.EqualFold(fields[0], "c") {
		return strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
	}
	return trimmed
}

func extractRPGDataAccess(cleanLine string, lineNum int) (ParsedDataAccess, bool) {
	cleanLine = normalizeRPGOperationLine(cleanLine)
	upper := strings.ToUpper(cleanLine)
	fields := strings.Fields(upper)
	if len(fields) == 0 {
		return ParsedDataAccess{}, false
	}

	if match := rpgOpcodeReadRe.FindStringSubmatch(upper); len(match) == 2 {
		target := rpgDataTarget(fields, match[1])
		if target == "" {
			return ParsedDataAccess{}, false
		}
		return ParsedDataAccess{
			EntityName: target,
			Access:     "read",
			LineNumber: lineNum,
			Source:     "rpg_opcode",
		}, true
	}
	if match := rpgOpcodeWriteRe.FindStringSubmatch(upper); len(match) == 2 {
		target := rpgDataTarget(fields, match[1])
		if target == "" {
			return ParsedDataAccess{}, false
		}
		return ParsedDataAccess{
			EntityName: target,
			Access:     "write",
			LineNumber: lineNum,
			Source:     "rpg_opcode",
		}, true
	}
	return ParsedDataAccess{}, false
}

func extractRPGDataAccesses(cleanLine string, lineNum int, displayFile string) []ParsedDataAccess {
	var accesses []ParsedDataAccess
	if access, ok := extractRPGDataAccess(cleanLine, lineNum); ok {
		accesses = append(accesses, access)
	}
	if displayAccesses := extractRPGDisplayAccesses(cleanLine, lineNum, displayFile); len(displayAccesses) > 0 {
		accesses = append(accesses, displayAccesses...)
	}
	return accesses
}

func extractRPGDisplayAccesses(cleanLine string, lineNum int, displayFile string) []ParsedDataAccess {
	cleanLine = normalizeRPGOperationLine(cleanLine)
	fields := strings.Fields(cleanLine)
	if len(fields) == 0 {
		return nil
	}

	opcodeIdx := -1
	opcode := ""
	for i, field := range fields {
		normalized := strings.ToUpper(normalizeRPGOpcodeField(field))
		if rpgDisplayOpcodeRe.MatchString(normalized) {
			opcodeIdx = i
			opcode = normalized
			break
		}
	}
	if opcodeIdx < 0 {
		return nil
	}

	target := ""
	for i := len(fields) - 1; i > opcodeIdx; i-- {
		candidate := normalizeRPGIdentifier(fields[i])
		if candidate == "" || strings.HasPrefix(candidate, "%") {
			continue
		}
		target = candidate
		break
	}
	if target == "" {
		return nil
	}

	accesses := []ParsedDataAccess{
		{
			EntityName: target,
			Access:     "display",
			LineNumber: lineNum,
			Source:     "rpg_display_opcode",
		},
	}
	if displayFile != "" && !strings.EqualFold(displayFile, target) && opcode == "EXFMT" {
		accesses = append(accesses, ParsedDataAccess{
			EntityName: displayFile,
			Access:     "display",
			LineNumber: lineNum,
			Source:     "rpg_display_file",
		})
	}
	return accesses
}

func rpgDataTarget(fields []string, opcode string) string {
	opcodeIdx := -1
	for i, field := range fields {
		if strings.EqualFold(normalizeRPGOpcodeField(field), opcode) {
			opcodeIdx = i
			break
		}
	}
	if opcodeIdx < 0 {
		return ""
	}

	for i := len(fields) - 1; i > opcodeIdx; i-- {
		field := normalizeRPGIdentifier(fields[i])
		if field == "" || strings.HasPrefix(field, "%") {
			continue
		}
		if rpgIgnoredCallNames[strings.ToLower(field)] {
			continue
		}
		return field
	}
	return ""
}

func normalizeRPGIdentifier(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, ";")
	value = strings.Trim(value, "'\"(),:")
	value = strings.TrimPrefix(value, "*")
	return strings.TrimSpace(value)
}

func normalizeRPGExternalSymbol(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, ";")
	value = strings.Trim(value, "'\"(),:")
	return strings.TrimSpace(value)
}

func normalizeRPGOpcodeField(value string) string {
	value = strings.TrimSpace(value)
	if idx := strings.IndexRune(value, '('); idx >= 0 {
		value = value[:idx]
	}
	return strings.TrimSpace(value)
}

func normalizeRPGSQLLine(line string) string {
	return stripRPGTrailingComment(line, "--")
}

func rpgSQLAccessKind(sqlText string) string {
	upper := strings.ToUpper(sqlText)
	switch {
	case strings.Contains(upper, "INSERT INTO"), strings.Contains(upper, "UPDATE "), strings.Contains(upper, "DELETE FROM"):
		return "write"
	case strings.Contains(upper, "SELECT "), strings.Contains(upper, "FETCH "), strings.Contains(upper, "WITH "):
		return "read"
	default:
		return ""
	}
}

func rpgSQLPrimaryTarget(sqlText string, access string) string {
	find := func(re *regexp.Regexp) string {
		match := re.FindStringSubmatch(sqlText)
		if len(match) != 2 {
			return ""
		}
		target := strings.TrimSpace(match[1])
		target = strings.Trim(target, "\"'`")
		if idx := strings.LastIndex(target, "/"); idx >= 0 {
			target = target[idx+1:]
		}
		if idx := strings.LastIndex(target, "."); idx >= 0 {
			target = target[idx+1:]
		}
		return strings.ToUpper(target)
	}

	switch access {
	case "read":
		return find(rpgSQLFromRe)
	case "write":
		if target := find(rpgSQLInsertRe); target != "" {
			return target
		}
		if target := find(rpgSQLUpdateRe); target != "" {
			return target
		}
		return find(rpgSQLDeleteRe)
	default:
		return ""
	}
}

func appendRPGDataAccess(target map[string][]ParsedDataAccess, fnName string, access ParsedDataAccess) {
	if fnName == "" || access.EntityName == "" || access.Access == "" {
		return
	}
	key := strings.ToLower(access.EntityName) + "|" + access.Access
	for _, existing := range target[fnName] {
		existingKey := strings.ToLower(existing.EntityName) + "|" + existing.Access
		if existingKey == key {
			return
		}
	}
	target[fnName] = append(target[fnName], access)
}

func clampLine(line int, total int) int {
	switch {
	case line < 1:
		return 1
	case total > 0 && line > total:
		return total
	default:
		return line
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
