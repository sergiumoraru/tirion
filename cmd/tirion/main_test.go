package main

import (
	"testing"
)

func TestAPIEndpointAddsAPIPrefixOnce(t *testing.T) {
	tests := []struct {
		name string
		base string
		path string
		want string
	}{
		{
			name: "server root",
			base: "http://localhost:8080",
			path: "/search",
			want: "http://localhost:8080/api/search",
		},
		{
			name: "api root",
			base: "http://localhost:8080/api",
			path: "trace",
			want: "http://localhost:8080/api/trace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := apiEndpoint(tt.base, tt.path); got != tt.want {
				t.Fatalf("apiEndpoint(%q, %q) = %q, want %q", tt.base, tt.path, got, tt.want)
			}
		})
	}
}
