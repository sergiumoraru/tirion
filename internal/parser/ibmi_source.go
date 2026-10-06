package parser

import (
	"path/filepath"
	"strings"
)

var ibmiMemberPathKinds = map[string]string{
	"QRPGSRC":      "rpg",
	"QRPGLESRC":    "rpgle",
	"QSQLRPGLESRC": "sqlrpgle",
	"QCLSRC":       "cl",
	"QCLLESRC":     "clle",
	"QCPYSRC":      "rpgleinc",
	"QDDSSRC":      "dds",
	"QDSPSRC":      "dspf",
	"QPRTSRC":      "prtf",
	"QSRVSRC":      "binder",
	"QDDSSRC2":     "dds",
}

var ibmiGenericMemberExtensions = map[string]bool{
	"":        true,
	".mbr":    true,
	".src":    true,
	".txt":    true,
	".txtmbr": true,
}

func detectIBMISourceMemberKind(filePath string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(filePath))
	if !ibmiGenericMemberExtensions[ext] {
		return "", false
	}

	dirNames := ibmiParentDirNames(filePath)
	for _, dir := range dirNames {
		if kind, ok := ibmiMemberPathKinds[dir]; ok {
			return kind, true
		}
	}
	return "", false
}

func ibmiParentDirNames(filePath string) []string {
	clean := filepath.Clean(filePath)
	var dirs []string
	current := filepath.Dir(clean)
	for current != "." && current != string(filepath.Separator) && current != "" {
		name := strings.ToUpper(strings.TrimSpace(filepath.Base(current)))
		if name != "" && name != "." {
			dirs = append(dirs, name)
		}
		next := filepath.Dir(current)
		if next == current {
			break
		}
		current = next
	}
	return dirs
}
