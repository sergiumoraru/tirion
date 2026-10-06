package main

import "testing"

func TestExtractSQLTablesForAccess_QuotedAndQualifiedIdentifiers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		sql    string
		access string
		want   string
		entity string
	}{
		{
			name:   "read from bracket qualified table",
			sql:    "SELECT id FROM [dbo].[USERS] u WHERE u.id = ?",
			access: "read",
			want:   "USERS",
			entity: "User",
		},
		{
			name:   "insert into double-quoted table",
			sql:    "INSERT INTO \"public\".\"INVOICES\" (id) VALUES (1)",
			access: "write",
			want:   "INVOICES",
			entity: "Invoice",
		},
		{
			name:   "update backtick qualified table",
			sql:    "UPDATE `tenant`.`resource_items` SET name = ?",
			access: "write",
			want:   "resource_items",
			entity: "ResourceItem",
		},
		{
			name:   "delete from only syntax",
			sql:    "DELETE FROM ONLY audit_logs WHERE id = ?",
			access: "write",
			want:   "audit_logs",
			entity: "AuditLog",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tables := extractSQLTablesForAccess(tt.sql, tt.access)
			if len(tables) != 1 {
				t.Fatalf("expected 1 table, got %d: %#v", len(tables), tables)
			}
			if tables[0] != tt.want {
				t.Fatalf("expected table %q, got %q (sql=%q)", tt.want, tables[0], tt.sql)
			}
			if entity := tableNameToEntity(tables[0]); entity != tt.entity {
				t.Fatalf("expected entity %q from table %q, got %q", tt.entity, tables[0], entity)
			}
		})
	}
}
