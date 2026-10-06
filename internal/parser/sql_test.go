package parser

import "testing"

func TestSQLParserExtractsRoutineAndDataAccesses(t *testing.T) {
	content := []byte(`
CREATE PROCEDURE dbo.RefreshAudit
AS
BEGIN
  SELECT id FROM dbo.Users u
  JOIN audit.Logs l ON l.user_id = u.id;
  INSERT INTO dbo.RefreshRuns (id) VALUES (1);
  UPDATE dbo.Users SET refreshed_at = CURRENT_TIMESTAMP;
  DELETE FROM audit.TempRows WHERE expired = 1;
END
`)

	result := NewSQLParser().ParseFile("migrations/refresh_audit.sql", content)
	if result.Language != "sql" {
		t.Fatalf("expected sql language, got %q", result.Language)
	}
	if len(result.Functions) != 2 {
		t.Fatalf("expected module and routine functions, got %#v", result.Functions)
	}

	accesses := result.DataAccesses["dbo.RefreshAudit"]
	want := map[string]string{
		"dbo.Users":       "write",
		"audit.Logs":      "read",
		"dbo.RefreshRuns": "write",
		"audit.TempRows":  "write",
	}
	for entity, access := range want {
		if !hasSQLAccess(accesses, entity, access) {
			t.Fatalf("expected %s %s access, got %#v", access, entity, accesses)
		}
	}
}

func TestSQLParserExtractsModuleScriptAccesses(t *testing.T) {
	content := []byte(`
CREATE TABLE IF NOT EXISTS public.ExportRuns (id int);
SELECT id FROM public.ExportRuns;
`)

	result := NewSQLParser().ParseFile("migrations/export_runs.sql", content)
	accesses := result.DataAccesses["_module_"]
	if !hasSQLAccess(accesses, "public.ExportRuns", "write") {
		t.Fatalf("expected create table write access, got %#v", accesses)
	}
	if !hasSQLAccess(accesses, "public.ExportRuns", "read") {
		t.Fatalf("expected select read access, got %#v", accesses)
	}
}

// Dialect constructs that used to hide statements: each case must keep the real
// access and must not report the table named only inside a literal or comment.
func TestSQLParserKeepsDialectAndDynamicSQLAccesses(t *testing.T) {
	cases := []struct {
		name, sql string
		want      map[string]string // entity -> access
		absent    []string
	}{
		{"update join", "UPDATE t1 INNER JOIN t2 ON t1.a = t2.a SET t1.x = 1;\nUPDATE dbo.O WITH (ROWLOCK) SET s = 1;\nUPDATE LOW_PRIORITY m SET a = 1;",
			map[string]string{"t1": "write", "dbo.O": "write", "m": "write"}, []string{"LOW_PRIORITY"}},
		{"update clauses", "CREATE TABLE c (id int REFERENCES p(id) ON UPDATE CASCADE);\nSELECT * FROM q FOR UPDATE;",
			map[string]string{"c": "write", "q": "read"}, []string{"CASCADE", "p"}},
		{"delimiter", "DELIMITER $$\nCREATE PROCEDURE p()\nBEGIN\n  SELECT * FROM a1;\nEND$$\nDELIMITER ;\nSELECT * FROM a2;",
			map[string]string{"a1": "read", "a2": "read"}, nil},
		{"mysql escapes", "INSERT INTO m VALUES ('Christie\\'s');\n# it's FROM fake\nSELECT * FROM real1;",
			map[string]string{"m": "write", "real1": "read"}, []string{"fake"}},
		{"executable comment", "/*!50003 CREATE*/ /*!50003 TRIGGER t AFTER INSERT ON s FOR EACH ROW BEGIN INSERT INTO audit VALUES (1); END */;",
			map[string]string{"audit": "write"}, nil},
		{"dynamic sql", "EXEC('SELECT * FROM dbo.Orders');\nSET @s = 'DELETE FROM dbo.Audit';\nSELECT 'from fake_table', 'it''s FROM nowhere';",
			map[string]string{"dbo.Orders": "read", "dbo.Audit": "write"}, []string{"fake_table", "nowhere"}},
		{"tsql backslash", "SELECT 'C:\\' FROM dbo.Files;\n#tmp\nSELECT * FROM dbo.After;",
			map[string]string{"dbo.Files": "read", "dbo.After": "read"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var all []ParsedDataAccess
			for _, accesses := range NewSQLParser().ParseFile("x.sql", []byte(tc.sql)).DataAccesses {
				all = append(all, accesses...)
			}
			for entity, access := range tc.want {
				if !hasSQLAccess(all, entity, access) {
					t.Errorf("expected %s %s, got %#v", access, entity, all)
				}
			}
			for _, entity := range tc.absent {
				for _, item := range all {
					if item.EntityName == entity {
						t.Errorf("unexpected access to %s: %#v", entity, all)
					}
				}
			}
		})
	}
}

func hasSQLAccess(accesses []ParsedDataAccess, entity, access string) bool {
	for _, item := range accesses {
		if item.EntityName == entity && item.Access == access {
			return true
		}
	}
	return false
}
