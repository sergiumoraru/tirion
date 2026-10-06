package sourcepath

import (
	"path"
	"strings"
	"unicode"
)

// testDirSegments are directories whose contents are test code in every
// language. "spec"/"specs" are handled separately because they also name
// production packages (Java com/acme/spec, Specification builders).
var testDirSegments = map[string]bool{
	"test":      true,
	"tests":     true,
	"__tests__": true,
	"__mocks__": true,
	"mocks":     true,
	"testdata":  true,
}

// Go and Java have strict test layouts (_test.go, src/test), so a "spec"
// directory there is a package name rather than a test marker.
var specDirNotTest = map[string]bool{".go": true, ".java": true}

// jvmSourceExts are the JVM languages whose src/main trees hold packages that may
// be named "spec". Web assets under src/main/webapp/spec (Jasmine specs and
// fixtures) are tests, so the src/main exemption does not cover them.
var jvmSourceExts = map[string]bool{
	".kt": true, ".kts": true, ".scala": true, ".sc": true, ".groovy": true, ".clj": true, ".cljc": true,
}

var testFileSuffixes = []string{
	"_test.go", "_test.java", "_test.cs",
	".feature.cs", ".feature.vb",
	"-spec.js", "-spec.ts", // NestJS e2e-spec
}

var scriptTestExts = []string{"js", "jsx", "ts", "tsx", "mjs", "cjs", "mts", "cts"}

// Java/C# test class stems use case boundaries, so they are matched on the
// original-case stem: FooTest is a test, Contest and Latest are not.
var testStemSuffixes = map[string][]string{
	// Surefire/Failsafe defaults: *Test, *Tests, *TestCase (which covers abstract
	// bases such as BaseTestCase), *IT, *ITCase. A "Spec" is typically a Spring
	// Data JPA Specification in Java (ProductSpec.java), so it is not listed.
	".java": {"Test", "Tests", "TestCase", "IT", "ITCase"},
	// Spec/Specs are NSpec/MSpec conventions in C#.
	".cs": {"Test", "Tests", "TestCase", "Spec", "Specs", "IT", "ITCase"},
}

// IsTest identifies directory segments and conventional filename suffixes, never
// substrings such as the "test" in Latest.java or Contest.java.
func IsTest(value string) bool {
	value = strings.ReplaceAll(value, `\`, "/")
	base := path.Base(value)
	lower := strings.ToLower(base)
	ext := path.Ext(lower)

	segments := strings.Split(strings.ToLower(path.Dir(value)), "/")
	for i, segment := range segments {
		if testDirSegments[segment] {
			return true
		}
		if (segment == "spec" || segment == "specs") && !specDirNotTest[ext] && !(jvmSourceExts[ext] && underProductionRoot(segments, i)) {
			return true
		}
	}

	for _, suffix := range testFileSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	for _, marker := range []string{".test.", ".spec."} {
		for _, scriptExt := range scriptTestExts {
			if strings.HasSuffix(lower, marker+scriptExt) {
				return true
			}
		}
	}

	stem := strings.TrimSuffix(base, path.Ext(base))
	// Jasmine names specs in camel case (RadioGroupSpec.js), wherever they live.
	// TypeScript is left to the .spec. marker and spec directories: a FooSpec.ts
	// is as likely a Specification-pattern class.
	if len(stem) > len("Spec") && strings.HasSuffix(stem, "Spec") {
		switch ext {
		case ".js", ".jsx", ".mjs", ".cjs":
			return true
		}
	}
	for _, suffix := range testStemSuffixes[ext] {
		if !strings.HasSuffix(stem, suffix) {
			continue
		}
		// Uppercase-only suffixes need a word boundary: FooIT is an integration
		// test, SPLIT is not.
		if (suffix == "IT" || suffix == "ITCase") && len(stem) > len(suffix) && unicode.IsUpper(rune(stem[len(stem)-len(suffix)-1])) {
			continue
		}
		return true
	}
	// Surefire's default Test*.java include: TestFoo.java, never Testament.java.
	// Under a Maven/Gradle src/main root such a name is production code
	// (TestRunner, TestRunnablesBuilder in a test-framework library).
	if ext == ".java" && len(stem) > 4 && strings.HasPrefix(stem, "Test") && !underProductionRoot(segments, len(segments)) {
		if r := rune(stem[4]); unicode.IsUpper(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// underProductionRoot reports whether the segment at index sits below a Maven or
// Gradle src/main root; pass len(segments) to ask about the directory itself.
func underProductionRoot(segments []string, index int) bool {
	for i := 0; i+1 < index && i+1 < len(segments); i++ {
		if segments[i] == "src" && segments[i+1] == "main" {
			return true
		}
	}
	return false
}
