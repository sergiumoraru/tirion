package main

import "testing"

func TestExtractMyBatisXMLAccesses(t *testing.T) {
	xml := []byte(`
<mapper namespace="com.example.PageMapper">
  <select id="findPages">
    SELECT * FROM pages WHERE id = #{id}
  </select>
  <insert id="savePage">
    INSERT INTO pages (id) VALUES (#{id})
  </insert>
</mapper>
`)

	accesses := extractMyBatisXMLAccesses(xml)
	if len(accesses) != 2 {
		t.Fatalf("expected 2 accesses, got %d", len(accesses))
	}

	seen := make(map[string]bool)
	for _, access := range accesses {
		key := access.Method + "|" + access.Access + "|" + access.Entity
		seen[key] = true
		if access.Namespace != "com.example.PageMapper" {
			t.Fatalf("unexpected namespace: %q", access.Namespace)
		}
	}

	if !seen["findPages|read|Page"] {
		t.Fatalf("missing read access for findPages")
	}
	if !seen["savePage|write|Page"] {
		t.Fatalf("missing write access for savePage")
	}
}
