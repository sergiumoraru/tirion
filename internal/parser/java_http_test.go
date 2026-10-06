package parser

import "testing"

func TestJavaHttpURLConnectionCalls(t *testing.T) {
	parser := NewJavaParser()
	source := []byte(`
import java.net.HttpURLConnection;
import java.net.URL;

class Foo {
    void save() throws Exception {
        URL url = new URL("https://example.com/api/pages/save");
        HttpURLConnection conn = (HttpURLConnection) url.openConnection();
        conn.setRequestMethod("POST");
        conn.getInputStream();
    }
}
`)

	result := parser.ParseFile("Foo.java", source)
	calls := result.HttpCalls["Foo.save"]
	if len(calls) != 1 {
		t.Fatalf("expected 1 HttpURLConnection call, got %d", len(calls))
	}
	call := calls[0]
	if call.HttpMethod != "POST" {
		t.Fatalf("expected POST method, got %q", call.HttpMethod)
	}
	if call.UrlPattern != "https://example.com/api/pages/save" {
		t.Fatalf("unexpected url pattern: %q", call.UrlPattern)
	}
	if call.ClientType != "HttpURLConnection" {
		t.Fatalf("unexpected client type: %q", call.ClientType)
	}
}
