package main

import "testing"

func TestExtractMyBatisXMLAccesses_StatementReadWrite(t *testing.T) {
	xml := []byte(`
<sqlMap namespace="com.example.catalog.ResourceMapper">
  <statement id="getResourceById">
    SELECT ID, NAME FROM RESOURCES WHERE ID = #id#
  </statement>
  <statement id="deleteResourceById">
    DELETE FROM RESOURCES WHERE ID = #id#
  </statement>
</sqlMap>
`)

	accesses := extractMyBatisXMLAccesses(xml)
	if len(accesses) < 2 {
		t.Fatalf("expected at least 2 accesses, got %d: %#v", len(accesses), accesses)
	}

	readFound := false
	writeFound := false
	for _, a := range accesses {
		if a.Method == "getResourceById" && a.Entity == "Resource" && a.Access == "read" {
			readFound = true
		}
		if a.Method == "deleteResourceById" && a.Entity == "Resource" && a.Access == "write" {
			writeFound = true
		}
	}

	if !readFound {
		t.Fatalf("expected read access for getResourceById, got %#v", accesses)
	}
	if !writeFound {
		t.Fatalf("expected write access for deleteResourceById, got %#v", accesses)
	}
}
