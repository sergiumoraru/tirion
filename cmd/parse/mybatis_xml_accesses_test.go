package main

import (
	"testing"

	"github.com/sergiumoraru/tirion/internal/parser"
)

func TestExtractMyBatisXMLAccesses_ProcedureFallsBackToNamespace(t *testing.T) {
	xml := []byte(`
<?xml version="1.0" encoding="UTF-8" ?>
<!DOCTYPE sqlMap PUBLIC "-//iBATIS.com//DTD SQL Map 2.0//EN" "http://ibatis.apache.org/dtd/sql-map-2.dtd">
<sqlMap namespace="Resource">
  <procedure id="deleteResource" parameterClass="hashmap">
    {CALL DELETE_RESOURCE(#resourceId#)}
  </procedure>
</sqlMap>
`)

	accesses := extractMyBatisXMLAccesses(xml)
	if len(accesses) != 1 {
		t.Fatalf("expected 1 access, got %d: %#v", len(accesses), accesses)
	}
	if accesses[0].Namespace != "Resource" {
		t.Fatalf("expected namespace Resource, got %q", accesses[0].Namespace)
	}
	if accesses[0].Method != "deleteResource" {
		t.Fatalf("expected method deleteResource, got %q", accesses[0].Method)
	}
	if accesses[0].Entity != "Resource" {
		t.Fatalf("expected entity Resource, got %q", accesses[0].Entity)
	}
	if accesses[0].Access != "write" {
		t.Fatalf("expected access write, got %q", accesses[0].Access)
	}
}

func TestExtractMyBatisXMLAccesses_ExpandsIncludeFragments(t *testing.T) {
	xml := []byte(`
<mapper namespace="PagesMapper">
  <sql id="Pages_From">
    FROM PAGES
  </sql>
  <select id="getPage">
    SELECT *
    <include refid="Pages_From" />
  </select>
</mapper>
`)

	accesses := extractMyBatisXMLAccesses(xml)
	if len(accesses) != 1 {
		t.Fatalf("expected 1 access, got %d: %#v", len(accesses), accesses)
	}
	if accesses[0].Namespace != "PagesMapper" {
		t.Fatalf("expected namespace PagesMapper, got %q", accesses[0].Namespace)
	}
	if accesses[0].Method != "getPage" {
		t.Fatalf("expected method getPage, got %q", accesses[0].Method)
	}
	if accesses[0].Entity != "Page" {
		t.Fatalf("expected entity Page, got %q", accesses[0].Entity)
	}
	if accesses[0].Access != "read" {
		t.Fatalf("expected access read, got %q", accesses[0].Access)
	}
}

func TestExtractProviderSQLFromLiterals_SelectProvider(t *testing.T) {
	javaSource := []byte(`
import org.apache.ibatis.annotations.SelectProvider;

interface InvoiceMapper {
  @SelectProvider(type=Provider.class, method="provideSql")
  int getInvoice();

  class Provider {
    public String provideSql() {
      return "SELECT ID" + " FROM INVOICE" + " WHERE ID = #{id}";
    }
  }
}

`)

	p := parser.NewJavaParser()
	parsed := p.ParseFile("InvoiceMapper.java", javaSource)

	var mapperFn parser.ParsedFunction
	var providerFn parser.ParsedFunction
	for _, fn := range parsed.Functions {
		switch fn.Name {
		case "InvoiceMapper.getInvoice":
			mapperFn = fn
		case "InvoiceMapper.Provider.provideSql":
			providerFn = fn
		}
	}
	if mapperFn.Name == "" || providerFn.Name == "" {
		t.Fatalf("expected mapper + provider functions, got mapper=%q provider=%q", mapperFn.Name, providerFn.Name)
	}

	providerFunc, accessKind, ok := extractMyBatisProviderBinding(mapperFn.Annotations)
	if !ok {
		t.Fatalf("expected provider binding, got annotations=%#v", mapperFn.Annotations)
	}
	if providerFunc != "Provider.provideSql" {
		t.Fatalf("expected provider func Provider.provideSql, got %q", providerFunc)
	}
	if accessKind != "read" {
		t.Fatalf("expected read accessKind, got %q", accessKind)
	}

	metas := make(map[string][]functionMeta)
	addFunctionMeta(metas, providerFn.Name, 42, providerFn.StartLine, providerFn.EndLine)
	matched := metas[scopedJavaFunctionName(metas, mapperFn.Name, providerFunc)]
	if len(matched) != 1 || matched[0].id != 42 {
		t.Fatalf("nested provider binding lost its scoped method: %+v", matched)
	}

	lits := extractJavaStringLiterals(string(javaSource))
	sqlText := extractProviderSQLFromLiterals(lits, providerFn.StartLine, providerFn.EndLine)
	if sqlText == "" {
		t.Fatalf("expected provider SQL text, got empty")
	}

	tables := extractSQLTablesForAccess(sqlText, accessKind)
	if len(tables) != 1 || tables[0] != "INVOICE" {
		t.Fatalf("expected table INVOICE, got %#v (sql=%q)", tables, sqlText)
	}
}

func TestExtractSpringDataQueryAccesses_ConcatenatedValue(t *testing.T) {
	annotations := []parser.ParsedAnnotation{
		{
			Name: "Query",
			Values: map[string]interface{}{
				"value": "\"SELECT ID \" + \"FROM RESOURCE \" + \"WHERE ID = :id\"",
			},
			LineNumber: 12,
		},
	}

	accesses := extractSpringDataQueryAccesses(annotations)
	if len(accesses) != 1 {
		t.Fatalf("expected 1 access, got %d: %#v", len(accesses), accesses)
	}
	if accesses[0].Entity != "Resource" {
		t.Fatalf("expected entity Resource, got %q", accesses[0].Entity)
	}
	if accesses[0].Access != "read" {
		t.Fatalf("expected read access, got %q", accesses[0].Access)
	}
	if accesses[0].Line != 12 {
		t.Fatalf("expected line 12, got %d", accesses[0].Line)
	}
}
