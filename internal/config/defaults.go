package config

// DefaultHttpClients contains the built-in HTTP client patterns
var DefaultHttpClients = []HttpClientPattern{
	// JavaScript/TypeScript patterns
	{
		Name:     "axios",
		Language: "javascript",
		Objects:  []string{"axios", "axiosClient"},
		Methods: map[string]string{
			"get":     "GET",
			"post":    "POST",
			"put":     "PUT",
			"delete":  "DELETE",
			"patch":   "PATCH",
			"head":    "HEAD",
			"options": "OPTIONS",
			"request": "ANY",
		},
	},
	{
		Name:     "fetch",
		Language: "javascript",
		Objects:  []string{"fetch"},
		Methods: map[string]string{
			"": "GET", // fetch() without method defaults to GET
		},
	},
	{
		Name:     "got",
		Language: "javascript",
		Objects:  []string{"got"},
		Methods: map[string]string{
			"":        "GET", // got(url) defaults to GET
			"get":     "GET",
			"post":    "POST",
			"put":     "PUT",
			"delete":  "DELETE",
			"patch":   "PATCH",
			"head":    "HEAD",
			"options": "OPTIONS",
		},
	},
	{
		Name:     "ky",
		Language: "javascript",
		Objects:  []string{"ky"},
		Methods: map[string]string{
			"":        "GET", // ky(url) defaults to GET
			"get":     "GET",
			"post":    "POST",
			"put":     "PUT",
			"delete":  "DELETE",
			"patch":   "PATCH",
			"head":    "HEAD",
			"options": "OPTIONS",
		},
	},
	{
		Name:     "$http",
		Language: "javascript",
		Objects:  []string{"$http"},
		Methods: map[string]string{
			"get":    "GET",
			"post":   "POST",
			"put":    "PUT",
			"delete": "DELETE",
			"patch":  "PATCH",
		},
	},
	{
		Name:     "jquery",
		Language: "javascript",
		Objects:  []string{"$", "jQuery"},
		Methods: map[string]string{
			"ajax":   "ANY",
			"get":    "GET",
			"post":   "POST",
			"put":    "PUT",
			"delete": "DELETE",
			"patch":  "PATCH",
		},
	},

	// Java patterns
	{
		Name:     "RestTemplate",
		Language: "java",
		Contains: []string{"restTemplate.", "RestTemplate.", "template."},
		Methods: map[string]string{
			"getForObject":   "GET",
			"getForEntity":   "GET",
			"postForObject":  "POST",
			"postForEntity":  "POST",
			"put":            "PUT",
			"delete":         "DELETE",
			"patchForObject": "PATCH",
			"exchange":       "REQUEST", // method determined by HttpMethod param
		},
	},
	{
		Name:     "WebClient",
		Language: "java",
		Contains: []string{"webClient.", "WebClient."},
		Methods: map[string]string{
			".get()":    "GET",
			".post()":   "POST",
			".put()":    "PUT",
			".delete()": "DELETE",
			".patch()":  "PATCH",
		},
	},
	{
		Name:     "HttpClient",
		Language: "java",
		Contains: []string{"HttpRequest.", "HttpClient."},
		Methods: map[string]string{
			".GET()":    "GET",
			".POST(":    "POST",
			".PUT(":     "PUT",
			".DELETE()": "DELETE",
		},
	},
	{
		Name:     "OkHttp",
		Language: "java",
		Contains: []string{"Request.Builder", "OkHttpClient"},
		Methods: map[string]string{
			".get()":    "GET",
			".post(":    "POST",
			".put(":     "PUT",
			".delete()": "DELETE",
		},
	},
	{
		Name:     "WebTarget",
		Language: "java",
		Contains: []string{".target(", "WebTarget", "webTarget", "webResource"},
		Methods: map[string]string{
			".get()":    "GET",
			".get(":     "GET",
			".post(":    "POST",
			".put(":     "PUT",
			".delete()": "DELETE",
			".delete(":  "DELETE",
		},
	},
}

// DefaultContextPathVariables are variable names that typically hold URL prefixes
var DefaultContextPathVariables = []string{
	"API_CONTEXT_PATH",
	"baseUrl",
	"apiUrl",
	"BASE_URL",
	"context",
	"base",
	"api_",
	"apiurl",
	"host",
	"origin",
}
