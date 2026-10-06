package indexer

import (
	"slices"
	"sort"
	"strings"
)

// languageFamilies groups files.language values whose code can call each other by
// bare name across repositories. A bare name such as handler() says nothing about
// which language it belongs to, so cross-repository name matching must stay inside
// one family: a JavaScript handler() must never bind to a Go Handler. Languages
// not listed here form a family of their own (their lower-cased language name).
var languageFamilies = map[string]string{
	"java": "jvm", "kotlin": "jvm", "scala": "jvm", "groovy": "jvm",
	"javascript": "js", "typescript": "js", "tsx": "js", "jsx": "js", "vue": "js",
	"csharp": "dotnet", "aspx": "dotnet", "asp": "dotnet",
	"rpg": "ibmi", "rpgle": "ibmi", "sqlrpgle": "ibmi", "rpgleinc": "ibmi", "sqlrpgleinc": "ibmi",
	"cl": "ibmi", "clle": "ibmi", "clp": "ibmi",
}

// caseInsensitiveFamilies are the families whose identifiers compare without
// regard to case (IBM i RPG/CL, SQL, PowerShell). Everywhere else (Java, JS/TS,
// C#, Go, ...) names are case-sensitive, so Handler and handler are different
// functions and must not be matched.
var caseInsensitiveFamilies = []string{"ibmi", "powershell", "sql"}

func languageFamily(language string) string {
	language = strings.ToLower(strings.TrimSpace(language))
	if family, ok := languageFamilies[language]; ok {
		return family
	}
	return language
}

// languageFamilySQL renders languageFamily as a SQL expression over column.
func languageFamilySQL(column string) string {
	names := make([]string, 0, len(languageFamilies))
	for name := range languageFamilies {
		names = append(names, name)
	}
	sort.Strings(names)
	normalized := "lower(btrim(COALESCE(" + column + ",'')))"
	var b strings.Builder
	b.WriteString("(CASE " + normalized)
	for _, name := range names {
		b.WriteString(" WHEN '" + name + "' THEN '" + languageFamilies[name] + "'")
	}
	b.WriteString(" ELSE " + normalized + " END)")
	return b.String()
}

// nameKeySQL renders the comparison key for a bare name of the given family
// expression: lower-cased for case-insensitive families, exact otherwise.
func nameKeySQL(familyExpr, nameExpr string) string {
	quoted := make([]string, len(caseInsensitiveFamilies))
	for i, family := range caseInsensitiveFamilies {
		quoted[i] = "'" + family + "'"
	}
	return "(CASE WHEN " + familyExpr + " IN (" + strings.Join(quoted, ",") + ") THEN lower(" + nameExpr + ") ELSE " + nameExpr + " END)"
}

func foldsCase(family string) bool {
	return slices.Contains(caseInsensitiveFamilies, family)
}
