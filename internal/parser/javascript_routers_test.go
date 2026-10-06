package parser

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func routerTestEndpoints(result ParsedFile) []string {
	out := []string{}
	for _, ep := range result.Endpoints {
		out = append(out, fmt.Sprintf("%s %s -> %s", ep.Method, ep.Path, ep.HandlerName))
	}
	sort.Strings(out)
	return out
}

func routerTestHTTPCalls(result ParsedFile) []string {
	out := []string{}
	for _, calls := range result.HttpCalls {
		for _, call := range calls {
			out = append(out, fmt.Sprintf("%s %s", call.HttpMethod, call.UrlPattern))
		}
	}
	sort.Strings(out)
	return out
}

func parseRouterTestSource(t *testing.T, name, src string) ParsedFile {
	t.Helper()
	result := NewJavaScriptParser().ParseFile(name, []byte(src))
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics for %s: %#v", name, result.ParseDiagnostics)
	}
	return result
}

// Receivers whose provenance the file shows are classified from it; the rest
// fall back to the shape of the call. Every row says which side it exercises.
func TestJavaScriptRouteRegistrationReceivers(t *testing.T) {
	tests := []struct {
		name string
		file string
		src  string
		want []string
	}{
		// Provenance visible in the file: routers.
		{"express app", "a.js", "const express = require('express')\nconst app = express()\napp.get('/a', h)", []string{"GET /a -> h"}},
		{"express router factory", "a.js", "const {Router} = require('express')\nconst r = Router()\nr.post('/a', h)", []string{"POST /a -> h"}},
		{"esm express router", "a.ts", "import { Router } from 'express'\nconst r = Router()\nr.get('/a', h)", []string{"GET /a -> h"}},
		{"ts import require", "a.ts", "import express = require('express')\nconst app = express()\napp.get('/a', h)", []string{"GET /a -> h"}},
		{"namespace import", "a.ts", "import * as express from 'express'\nconst app = express()\napp.get('/a', h)", []string{"GET /a -> h"}},
		{"default as express", "a.ts", "import { default as ex } from 'express'\nex().get('/a', h)\nconst app = ex()\napp.get('/b', h)", []string{"GET /a -> h", "GET /b -> h"}},
		{"var app = module.exports = express()", "a.js", "var express = require('express')\nvar app = module.exports = express()\napp.get('/a', h)", []string{"GET /a -> h"}},
		{"express chain keeps the app", "a.js", "const express = require('express')\nconst app = express().disable('x-powered-by')\napp.get('/a', h)\nconst b = express().set('view engine', 'ejs').use(cors())\nb.get('/b', h)", []string{"GET /a -> h", "GET /b -> h"}},
		{"satisfies", "a.ts", "import express from 'express'\nconst app = express() satisfies Application\napp.get('/a', h)", []string{"GET /a -> h"}},
		{"type assertion", "a.ts", "import express from 'express'\nconst r = <Router>express.Router()\nr.get('/a', h)\nconst s = express.Router() as Router\ns.get('/b', h)", []string{"GET /a -> h", "GET /b -> h"}},
		{"express-promise-router", "a.js", "const router = require('express-promise-router')()\nrouter.get('/a', h)", []string{"GET /a -> h"}},
		{"hono", "a.ts", "import { Hono } from 'hono'\nconst app = new Hono<{ Bindings: Env }>()\napp.get('/a', h)\nconst sub = new Hono().basePath('/v1')\nsub.post('/b', h)", []string{"GET /a -> h", "POST /b -> h"}},
		{"elysia", "a.ts", "import { Elysia } from 'elysia'\nconst app = new Elysia()\napp.get('/a', () => 'hi')", []string{"GET /a -> _anonymous_L3_C15"}},
		{"restify", "a.js", "var restify = require('restify')\nvar server = restify.createServer()\nserver.get('/a', h)", []string{"GET /a -> h"}},
		{"polka", "a.js", "const polka = require('polka')\nconst p = polka()\np.get('/a', h)", []string{"GET /a -> h"}},
		{"koa-router", "a.js", "const Router = require('koa-router')\nconst r = new Router()\nr.get('/a', async ctx => {})", []string{"GET /a -> _anonymous_L3_C13"}},
		{"fastify instance", "a.ts", "import Fastify from 'fastify'\nconst f = Fastify()\nf.get('/a', h)", []string{"GET /a -> h"}},
		{"hoisted use before declaration", "a.js", "function start() { app.get('/a', h) }\nconst app = express()\nfunction later() { app.post('/b', h) }", []string{"GET /a -> h", "POST /b -> h"}},
		{"union with undefined", "a.ts", "import { Express } from 'express'\nlet app: Express | undefined\nfunction reg(a?: Express | undefined) { a.get('/a', h) }\nfunction reg2() { app.get('/b', h) }", []string{"GET /a -> h", "GET /b -> h"}},
		{"typed this field", "a.ts", "import { Express } from 'express'\nclass S { private app: Express\n  r() { this.app.get('/a', this.list) } }", []string{"GET /a -> this.list"}},
		{"this field from constructor", "a.ts", "class S { constructor() { this.app = express() }\n  r() { this.app.post('/a', (req, res) => {}) } }", []string{"POST /a -> _anonymous_L2_C29"}},
		{"constructor parameter property", "a.ts", "import { Router } from 'express'\nclass S { constructor(private readonly router: Router) {}\n  r() { this.router.get('/a', h) } }", []string{"GET /a -> h"}},
		{"conditional assignment", "a.js", "let app\nif (x) { app = express() } else { app = fastify() }\napp.get('/a', h)", []string{"GET /a -> h"}},
		{"initialised null then assigned in a function", "a.js", "const express = require('express')\nlet app = null\nfunction init() { app = express() }\nfunction use() { app.get('/a', h) }", []string{"GET /a -> h"}},
		{"route object chain", "a.js", "const express = require('express')\nconst r = express.Router()\nr.route('/a').get(l).post(c).put(u)", []string{"GET /a -> l", "POST /a -> c", "PUT /a -> u"}},
		{"route chain with all", "a.js", "const express = require('express')\nconst r = express.Router()\nr.route('/a').all(auth).get(l)", []string{"GET /a -> l"}},
		{"fastify route options", "a.ts", "import Fastify from 'fastify'\nconst f = Fastify()\nf.route({ method: ['GET', 'POST'], url: '/a', handler: h })", []string{"GET /a -> h", "POST /a -> h"}},
		{"template path", "a.js", "const express = require('express')\nconst app = express()\napp.get(`/u/${id}/x`, h)\napp.get(`${BASE}/y`, h)", []string{"GET /u/:id/x -> h"}},
		{"api named express app is still a router", "a.js", "const express = require('express')\nconst api = express()\napi.get('/a', h)\nconst apiRouter = express.Router()\napiRouter.get('/b', h)", []string{"GET /a -> h", "GET /b -> h"}},
		{"local factory is classified by what it returns", "a.js", "function createApi() { return express() }\nconst app = createApi()\napp.get('/a', h)\nfunction makeClient() { return axios.create() }\nconst c = makeClient()\nc.get('/b', h)", []string{"GET /a -> h"}},
		{"template substitutions with nested braces", "a.js", "const app = express()\napp.get(`/u/${fn({ a: 1 })}/x`, h)\napp.get(`/v/${a ? 'x' : 'y'}`, h)", []string{"GET /u/:param/x -> h", "GET /v/:param -> h"}},
		{"app settings are not routes", "a.js", "const express = require('express')\nconst app = express()\napp.get('port')\napp.get('env')\nconst p = app.get('port')", []string{}},

		// Provenance not visible: the shape of the call decides.
		{"commonjs export function param", "a.js", "module.exports = function (app) {\n  app.get('/a', h)\n}", []string{"GET /a -> h"}},
		{"exports property param", "a.js", "exports.init = function(server) { server.get('/a', function (req, res) {}); server.post('/b', handle) }", []string{"GET /a -> _anonymous_L1_C52", "POST /b -> handle"}},
		{"arrow export param", "a.js", "module.exports = (app) => app.get('/a', h)", []string{"GET /a -> h"}},
		{"relative import router", "a.js", "import router from './router'\nrouter.get('/a', list)", []string{"GET /a -> list"}},
		{"unknown factory", "a.js", "const app = createApp()\napp.get('/a', h)", []string{"GET /a -> h"}},
		{"unknown factory router", "a.js", "const router = makeRouter()\nrouter.get('/a', h)", []string{"GET /a -> h"}},
		{"global router", "a.js", "router.get('/a', h)\napp.get('/b', (req, res) => {})", []string{"GET /a -> h", "GET /b -> _anonymous_L2_C15"}},
		{"fastify plugin untyped param", "a.ts", "import { FastifyPluginAsync } from 'fastify'\nconst plugin: FastifyPluginAsync = async (fastify) => {\n  fastify.get('/a', async (request, reply) => ({}))\n}", []string{"GET /a -> _anonymous_L3_C21"}},
		{"this.app with unknown provenance", "a.ts", "class S { r() { this.app.get('/a', this.list) } }", []string{"GET /a -> this.list"}},
		{"conventional names", "a.js", "adminRouter.get('/a', h)\nv1Routes.get('/b', h)\nwebServer.get('/c', h)", []string{"GET /a -> h", "GET /b -> h", "GET /c -> h"}},
		{"request-shaped handler on any receiver", "a.js", "backend.get('/a', (req, res) => {})", []string{"GET /a -> _anonymous_L1_C19"}},
		{"unknown router route chain", "a.js", "router.route('/a').get(h).post(h2)", []string{"GET /a -> h", "POST /a -> h2"}},
		{"conventional receiver with a used result", "a.js", "import app from './app'\napp.get('/a', makeHandler())\napp.get('/b', ...handlers)\nexport default app.get('/c', h)\nconst x = app.get('/d', h)\nasync function f() { await app.get('/e', h); await api.get('/f', h); return api.get('/g', h) }", []string{"GET /a -> ", "GET /b -> ", "GET /c -> h", "GET /d -> h"}},
		{"unknown receiver options route", "a.js", "app.route({ method: 'GET', url: '/a', handler: h })", []string{"GET /a -> h"}},

		// Clients and shadowing stay out.
		{"axios instance", "a.js", "import axios from 'axios'\nconst api = axios.create()\nexport function f() { api.get('/u'); api.post('/u', body); api.get('/w', handler) }", []string{}},
		{"axios instance named app", "a.ts", "import axios from 'axios'\nconst app = axios.create()\napp.get('/u', handler)", []string{}},
		{"class instance named server", "a.js", "const server = new ApiClient()\nserver.get('/health')\nserver.get('/health2', cb)", []string{}},
		{"local class instance", "a.js", "class Registry { get(p, h) {} }\nconst app = new Registry()\napp.get('/a', h)", []string{}},
		{"object literal", "a.js", "const app = { get(p, h) {} }\napp.get('/a', h)", []string{}},
		{"http server has no get", "a.js", "const http = require('http')\nconst server = http.createServer()\nserver.get('/a', h)", []string{}},
		{"param shadows express app", "a.js", "const express = require('express')\nconst app = express()\napp.get('/outer', h)\nfunction f(app) { app.get('/inner') }", []string{"GET /outer -> h"}},
		{"local axios shadows express app", "a.js", "const express = require('express')\nconst app = express()\nfunction f() { const app = axios.create(); app.get('/inner', h); app.post('/inner', data) }\nif (x) { const app = axios.create(); app.get('/blk', h) }\napp.get('/outer', h)", []string{"GET /outer -> h"}},
		{"reassigned to a client", "a.js", "const express = require('express')\nlet app = express()\napp.get('/before', h)\napp = axios.create()\napp.get('/after', h)", []string{"GET /before -> h"}},
		{"awaited and chained client calls", "a.js", "export async function f() {\n  await api.get('/a')\n  api.get('/b').then(r => r)\n  const r = await http.get('/c')\n  const d = api.get('/d')\n  return api.get('/e')\n}", []string{}},
		{"client with config object", "a.js", "import client from './client'\nclient.get('/a', { params: {} })\nclient.post('/a', payload, config)\napi.get('/b', params)", []string{}},
		{"unnamed receiver with plain callback", "a.js", "api.get('/a', cb)\napi.get('/b', buildConfig())\nthis.http.get('/c', cb)", []string{}},
		{"node http get with callback", "a.js", "const http = require('http')\nhttp.get('http://example.com/x', (res) => {})\nhttps.get('https://example.com/x', function (res) {})", []string{}},
		{"supertest", "a.js", "const request = require('supertest')\nrequest(app).get('/a').expect(200)\nrequest(app).get('/b', cb)", []string{}},
		{"mock handler arrays and wrappers", "a.js", "const handlers = [ rest.get('/a', (req, res, ctx) => res(ctx.json({}))) ]\nserver.use(rest.get('/b', (req, res, ctx) => res()))", []string{}},
		{"result used", "a.js", "const r = foo.get('/a', h)\nreturn foo.get('/b', h)", []string{}},
		{"non-path first argument", "a.js", "app.get(PATH, h)\napp.get(['/a', '/b'], h)\napp.get(/re/, h)\napp.get(`${BASE}/t`, h)\napp.get('relative', h)", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := routerTestEndpoints(parseRouterTestSource(t, tt.file, tt.src))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("endpoints\n got: %q\nwant: %q\nsource:\n%s", got, tt.want, tt.src)
			}
		})
	}
}

func TestJavaScriptRouteRegistrationNotRecordedAsHTTPCall(t *testing.T) {
	tests := []struct {
		name     string
		src      string
		wantHTTP []string
	}{
		{"api named express app", "const express = require('express')\nconst api = express()\napi.get('/a', h)", []string{}},
		{"apiRouter", "const express = require('express')\nconst apiRouter = express.Router()\napiRouter.get('/a', h)", []string{}},
		{"unknown apiRouter registration", "import apiRouter from './r'\napiRouter.post('/a', h)", []string{}},
		{"unknown apiRouter without handler", "import apiRouter from './r'\napiRouter.get('/a')", []string{}},
		{"settings getter on a router", "const express = require('express')\nconst api = express()\napi.get('/a')", []string{}},
		{"route options form", "import Fastify from 'fastify'\nconst f = Fastify()\nf.route({ method: 'POST', url: '/r', handler: h })", []string{}},
		{"client keeps its call", "import axios from 'axios'\nconst api = axios.create()\nexport function f() { return api.get('/a') }", []string{"GET /a"}},
		{"api client", "export function f() { return api.get('/a') }", []string{"GET /a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := routerTestHTTPCalls(parseRouterTestSource(t, "a.ts", tt.src))
			if !reflect.DeepEqual(got, tt.wantHTTP) {
				t.Fatalf("http calls\n got: %q\nwant: %q", got, tt.wantHTTP)
			}
		})
	}
}

func TestJavaScriptRouteHandlerNames(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string
	}{
		{"identifier", "'/a', h", "h"},
		{"last identifier wins over middleware", "'/a', auth, getUser", "getUser"},
		{"wrapper call", "'/a', auth, wrap(h)", "h"},
		{"async handler around member", "'/a', asyncHandler(controller.list)", "controller.list"},
		{"bind", "'/a', controller.method.bind(controller)", "controller.method"},
		{"nested wrappers", "'/a', wrap(trace(h))", "h"},
		{"curried wrapper", "'/a', validate(schema)(h)", "h"},
		{"wrapper with options object", "'/a', withAuth(h, { roles: ['admin'] })", "h"},
		{"array of middleware", "'/a', [auth, validate], h", "h"},
		{"array as last argument", "'/a', [auth, h]", "h"},
		{"middleware call without handler is skipped", "'/a', h, auth()", "h"},
		{"inline arrow", "'/a', (req, res) => {}", "_anonymous_L3_C15"},
		{"inline function with name", "'/a', function named(req, res) {}", "named"},
		{"inline function in wrapper", "'/a', auth, wrap(async (req, res) => {})", "_anonymous_L3_C26"},
		{"member handler", "'/a', this.list", "this.list"},
		{"error middleware is skipped", "'/a', real, (err, req, res, next) => {}", "real"},
		{"parenthesized and cast", "'/a', (h as RequestHandler)", "h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "const express = require('express')\nconst app = express()\napp.get(" + tt.args + ")"
			result := parseRouterTestSource(t, "a.ts", src)
			if len(result.Endpoints) != 1 {
				t.Fatalf("expected one endpoint, got %#v", result.Endpoints)
			}
			if got := result.Endpoints[0].HandlerName; got != tt.want {
				t.Fatalf("handler name = %q, want %q", got, tt.want)
			}
			if strings.HasPrefix(tt.want, "_anonymous_") {
				found := false
				for _, fn := range result.Functions {
					if fn.Name == tt.want {
						found = true
					}
				}
				if !found {
					t.Fatalf("synthetic handler %q is not among extracted functions %v", tt.want, routerTestFunctionNames(result))
				}
			}
		})
	}
}

func routerTestFunctionNames(result ParsedFile) []string {
	names := []string{}
	for _, fn := range result.Functions {
		names = append(names, fn.Name)
	}
	sort.Strings(names)
	return names
}
