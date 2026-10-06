package parser

import "testing"

func TestDDSParser_PhysicalFile(t *testing.T) {
	p := NewDDSParser()
	content := []byte(`A          R ITEMREC
A            ITEMKEY       12A
A            LABELTXT      10A
A            ITEMNUMBER    11P 2
`)

	result := p.ParseFile("ITEMS.PF", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if result.Language != "pf" {
		t.Fatalf("expected language pf, got %q", result.Language)
	}
	if !hasClass(result.Classes, "ITEMS") {
		t.Fatalf("expected file-level class ITEMS, got %#v", result.Classes)
	}
	recordClass := findClass(result.Classes, "ITEMS.ITEMREC")
	if recordClass == nil {
		t.Fatalf("expected record class ITEMS.ITEMREC, got %#v", result.Classes)
	}
	if recordClass.ExtendsClass != "ITEMS" {
		t.Fatalf("expected ITEMS.ITEMREC to extend ITEMS, got %#v", recordClass)
	}
	if !hasField(recordClass.Fields, "ITEMKEY", "12A") {
		t.Fatalf("expected ITEMKEY field in record class, got %#v", recordClass.Fields)
	}
	if !hasField(recordClass.Fields, "ITEMNUMBER", "11P 2") {
		t.Fatalf("expected ITEMNUMBER field type 11P 2, got %#v", recordClass.Fields)
	}
}

func TestDDSParser_DisplayFile(t *testing.T) {
	p := NewDDSParser()
	content := []byte(`A                                      DSPSIZ(24 80 *DS3)
A          R EDITOR
A                                  1  2'Edit Resource'
A            ITEMKEY       12A  B  5  2
A            LABELTXT      10A  B  6  2
A  90        ERRMSG        40A  O 24  2
`)

	result := p.ParseFile("ITEMDSP.DSPF", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if result.Language != "dspf" {
		t.Fatalf("expected language dspf, got %q", result.Language)
	}
	recordClass := findClass(result.Classes, "ITEMDSP.EDITOR")
	if recordClass == nil {
		t.Fatalf("expected record class ITEMDSP.EDITOR, got %#v", result.Classes)
	}
	if !hasField(recordClass.Fields, "ITEMKEY", "12A") {
		t.Fatalf("expected ITEMKEY field in display record, got %#v", recordClass.Fields)
	}
	if !hasField(recordClass.Fields, "ERRMSG", "40A") {
		t.Fatalf("expected ERRMSG field in display record, got %#v", recordClass.Fields)
	}
}

func hasClass(classes []ParsedClass, name string) bool {
	return findClass(classes, name) != nil
}

func findClass(classes []ParsedClass, name string) *ParsedClass {
	for i := range classes {
		if classes[i].Name == name {
			return &classes[i]
		}
	}
	return nil
}

func hasField(fields []ParsedField, name string, fieldType string) bool {
	for _, field := range fields {
		if field.Name == name && field.FieldType == fieldType {
			return true
		}
	}
	return false
}
