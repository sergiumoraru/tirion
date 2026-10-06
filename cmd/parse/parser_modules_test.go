package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParserRegistryMatchesCoreModules(t *testing.T) {
	registry := newParserRegistry(t.TempDir())

	cases := []struct {
		path string
		want string
	}{
		{path: "src/Foo.java", want: "java"},
		{path: "src/app.ts", want: "javascript"},
		{path: "src/OrdersController.cs", want: "csharp"},
		{path: "src/WORKER.RPGLE", want: "rpg"},
		{path: "src/DRIVER.CLLE", want: "cl"},
		{path: "dds/SCREEN.DSPF", want: "dds"},
		{path: "resources/mappers/OrderMapper.xml", want: "mybatis_xml"},
		{path: "QSRVSRC/QSERVICE.BNDDIR", want: "ibmi_binder"},
		{path: "cmd/api/main.go", want: "go"},
		{path: "infra/main.tf", want: "terraform"},
		{path: "migrations/001_create_reports.sql", want: "sql"},
		{path: "scripts/deploy.ps1", want: "powershell"},
	}

	for _, tc := range cases {
		module := registry.Match(tc.path)
		if module == nil {
			t.Fatalf("expected module for %s", tc.path)
		}
		if module.ID() != tc.want {
			t.Fatalf("unexpected module for %s: got=%s want=%s", tc.path, module.ID(), tc.want)
		}
	}
}

func TestParserRegistryPrepareJavaBuildsPathIndex(t *testing.T) {
	registry := newParserRegistry(t.TempDir())
	module := registry.Match("src/main/java/com/acme/orders/OrderService.java")
	if module == nil || module.ID() != "java" {
		t.Fatalf("expected java module, got %#v", module)
	}

	registry.PrepareFile(module, "src/main/java/com/acme/orders/OrderService.java")

	got := registry.JavaPathIndex()["com/acme/orders/OrderService.java"]
	if got != "src/main/java/com/acme/orders/OrderService.java" {
		t.Fatalf("unexpected java path index entry: got=%q", got)
	}
}

func TestParserRegistrySkipFileUsesModuleHook(t *testing.T) {
	registry := newParserRegistry(t.TempDir())
	module := registry.Match("src/app.min.js")
	if module == nil || module.ID() != "javascript" {
		t.Fatalf("expected javascript module, got %#v", module)
	}

	skipCategory, ok := registry.SkipFile(module, "app.min.js", "src/app.min.js")
	if !ok {
		t.Fatal("expected javascript skip hook to match")
	}
	if skipCategory != parseSkipExcludedFilePattern {
		t.Fatalf("unexpected skip category: got=%s want=%s", skipCategory, parseSkipExcludedFilePattern)
	}
}

func TestJavaScriptSkipFileDetectsVendoredLibrariesByWholeName(t *testing.T) {
	registry := newParserRegistry(t.TempDir())
	cases := []struct {
		path string
		skip bool
	}{
		// Vendored libraries outside vendor directories.
		{"static/jquery-3.6.0.js", true},
		{"static/jquery.js", true},
		{"static/jquery.min.js", true},
		{"static/jquery.slim.min.js", true},
		{"static/jquery-3.6.0.slim.js", true},
		{"static/jquery-ui.js", true},
		{"static/jQuery-1.11.1.js", true},
		{"static/lodash.js", true},
		{"static/lodash.core.js", true},
		{"static/lodash.custom.min.js", true},
		{"static/underscore-1.13.6.js", true},
		{"static/angular.js", true},
		{"static/angular-route.js", true},
		{"static/angular-1.8.2.js", true},
		{"static/moment.js", true},
		{"static/moment-with-locales.js", true},
		{"static/backbone.js", true},
		{"static/d3.js", true},
		{"static/d3.v7.js", true},
		{"static/bootstrap.bundle.js", true},
		{"static/react.production.min.js", true},
		{"static/react.development.js", true},
		{"static/react-dom.production.min.js", true},
		{"static/Handlebars-v4.7.7.js", true},
		{"static/socket.io.js", true},
		{"static/lib.module.mjs", false},
		{"static/lodash.mjs", true},
		// Generated bundles.
		{"dist/app.min.js", true},
		{"src/main.bundle.js", true},
		{"src/main.Bundle.js", true},
		// First-party code that merely contains a library or vendor name.
		{"src/memberService.ts", false},
		{"src/Member.ts", false},
		{"src/remember.js", false},
		{"src/vendorApi.ts", false},
		{"src/swaggerClient.ts", false},
		{"src/angularRoutes.ts", false},
		{"src/lodashWrapper.ts", false},
		{"src/fooMd3.js", false},
		{"src/d3Chart.js", false},
		{"src/jquery-plugin.js", false},
		{"src/jqueryHelpers.js", false},
		{"src/moment-utils.js", false},
		{"src/my-lodash.js", false},
		{"src/underscore_helpers.js", false},
		{"src/react.js", false},
		{"src/react-router.js", false},
		{"src/reactor.js", false},
		{"src/threejs-scene.js", false},
		{"src/emberApp.js", false},
		{"src/chart.js", false},
		{"src/app.js", false},
		{"src/index.ts", false},
		{"src/jquery.ts", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			module := registry.Match(tc.path)
			if module == nil {
				t.Skipf("no module for %s", tc.path)
			}
			category, skipped := registry.SkipFile(module, filepath.Base(tc.path), tc.path)
			if skipped != tc.skip {
				t.Fatalf("SkipFile(%s) = %v, want %v", tc.path, skipped, tc.skip)
			}
			if skipped && category != parseSkipExcludedFilePattern {
				t.Fatalf("category = %s", category)
			}
		})
	}
}

func TestParserRegistrySkipContentUsesMyBatisModuleHook(t *testing.T) {
	registry := newParserRegistry(t.TempDir())
	module := registry.Match("resources/mappers/OrderMapper.xml")
	if module == nil || module.ID() != "mybatis_xml" {
		t.Fatalf("expected mybatis_xml module, got %#v", module)
	}

	skipCategory, ok := registry.SkipContent(module, "resources/mappers/OrderMapper.xml", "resources/mappers/OrderMapper.xml", []byte(`<mapper namespace="com.acme.OrderMapper"></mapper>`))
	if !ok {
		t.Fatal("expected mybatis content skip hook to match empty mapper")
	}
	if skipCategory != parseSkipNoExtractedContent {
		t.Fatalf("unexpected skip category: got=%s want=%s", skipCategory, parseSkipNoExtractedContent)
	}
}

func TestMyBatisXMLModuleParsesMapperFile(t *testing.T) {
	registry := newParserRegistry(t.TempDir())
	module := registry.Match("resources/mappers/OrderMapper.xml")
	if module == nil || module.ID() != "mybatis_xml" {
		t.Fatalf("expected mybatis_xml module, got %#v", module)
	}

	content := []byte(`<mapper namespace="com.acme.OrderMapper">
  <select id="getOrder">select * from orders</select>
</mapper>`)
	if skipCategory, ok := registry.SkipContent(module, "resources/mappers/OrderMapper.xml", "resources/mappers/OrderMapper.xml", content); ok {
		t.Fatalf("unexpected content skip: %s", skipCategory)
	}

	result := module.Parse("resources/mappers/OrderMapper.xml", "resources/mappers/OrderMapper.xml", content)
	if result.Language != "xml" {
		t.Fatalf("unexpected language: got=%q want=%q", result.Language, "xml")
	}
}

func TestParserRegistryAfterParseExpandsRPGCopyAliases(t *testing.T) {
	repoRoot := t.TempDir()
	sharedDir := filepath.Join(repoRoot, "shared")
	srcDir := filepath.Join(repoRoot, "worker")
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		t.Fatalf("mkdir shared: %v", err)
	}
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	copyPath := filepath.Join(sharedDir, "SHARED.rpgleinc")
	if err := os.WriteFile(copyPath, []byte(`**free
dcl-pr AuditEvent extproc('LOGEVENT');
  Domain char(12) const;
  RefId char(12) const;
end-pr;
`), 0o644); err != nil {
		t.Fatalf("write copy: %v", err)
	}

	registry := newParserRegistry(repoRoot)
	module := registry.Match("worker/WORKER.RPGLE")
	if module == nil || module.ID() != "rpg" {
		t.Fatalf("expected rpg module, got %#v", module)
	}

	content := []byte(`**free
/copy ../shared/SHARED.rpgleinc

dcl-proc WORKER export;
  dcl-pi *n end-pi;
  AuditEvent('WORKER': 'R1');
end-proc;
`)

	result := module.Parse("worker/WORKER.RPGLE", filepath.Join(srcDir, "WORKER.RPGLE"), content)
	registry.AfterParse(module, "worker/WORKER.RPGLE", &result)

	if !hasCallName(result.FunctionCalls["WORKER"], "LOGEVENT") {
		t.Fatalf("expected imported AuditEvent to normalize to LOGEVENT, got %#v", result.FunctionCalls["WORKER"])
	}
}
