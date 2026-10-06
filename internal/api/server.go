package api

import (
	"net/http"
	"os"
	"strings"

	"github.com/sergiumoraru/tirion/internal/api/handlers"
	"github.com/sergiumoraru/tirion/internal/api/middleware"
	"github.com/sergiumoraru/tirion/internal/config"
	"github.com/sergiumoraru/tirion/internal/graph"
	"github.com/sergiumoraru/tirion/internal/runtimeconfig"
)

type Server struct {
	storage  *graph.Storage
	handlers *handlers.Handlers
	handler  http.Handler
}

func NewServer(dbURL string) (*Server, error) {
	if err := config.GetEffectivePatterns().Err(); err != nil {
		return nil, err
	}
	limits, err := middleware.LimitsFromEnv()
	if err != nil {
		return nil, err
	}
	storage, err := graph.NewStorage(dbURL)
	if err != nil {
		return nil, err
	}

	h := handlers.New(storage, dbURL)

	mux := newMux(h)
	app := middleware.Logging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := config.GetEffectivePatterns().Err(); err != nil {
			middleware.WriteError(w, http.StatusServiceUnavailable, "CONFIG_INVALID", err.Error())
			return
		}
		mux.ServeHTTP(w, r)
	}))

	// The authenticated layer is bound last: the token file is created only
	// after the database is reachable and every other setting validated, so a
	// failed start never leaves a credential behind.
	var protected http.Handler
	handler := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protected.ServeHTTP(w, r)
	}))
	origins := []string{"http://localhost:3000", "http://127.0.0.1:3000"}
	if configured, exists := os.LookupEnv("TIRION_ALLOWED_ORIGINS"); exists {
		origins = strings.Split(configured, ",")
	}
	handler, err = middleware.NewCORS(handler, origins)
	if err != nil {
		h.Close()
		storage.Close()
		return nil, err
	}

	hosts := []string{"localhost", "127.0.0.1", "[::1]"}
	if configured, exists := os.LookupEnv("TIRION_ALLOWED_HOSTS"); exists {
		hosts = strings.Split(configured, ",")
	}
	handler, err = middleware.TrustedHosts(handler, hosts)
	if err != nil {
		h.Close()
		storage.Close()
		return nil, err
	}

	token, err := runtimeconfig.APIToken(true)
	if err != nil {
		h.Close()
		storage.Close()
		return nil, err
	}
	protected = middleware.Authenticate(middleware.ResourceLimits(app, limits), token)

	return &Server{
		storage:  storage,
		handlers: h,
		handler:  handler,
	}, nil
}

func newMux(h *handlers.Handlers) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", h.Health)
	mux.HandleFunc("GET /api/stats", h.Stats)
	mux.HandleFunc("GET /api/admin/health", h.AdminHealth)
	mux.HandleFunc("GET /api/admin/repos", h.AdminRepos)
	mux.HandleFunc("POST /api/admin/repos/{id}/fetch", h.AdminRepoFetch)
	mux.HandleFunc("POST /api/admin/repos/{id}/checkout", h.AdminRepoCheckout)
	mux.HandleFunc("POST /api/admin/repos/{id}/parse", h.AdminRepoParse)
	mux.HandleFunc("GET /api/workspaces", h.ListWorkspaces)
	mux.HandleFunc("POST /api/workspaces", h.CreateWorkspace)
	mux.HandleFunc("POST /api/workspaces/{slug}/default", h.SetDefaultWorkspace)
	mux.HandleFunc("POST /api/workspaces/{slug}/index", h.IndexWorkspace)
	mux.HandleFunc("POST /api/workspaces/{slug}/bulk-checkout", h.BulkCheckoutWorkspace)
	mux.HandleFunc("POST /api/workspaces/{slug}/bulk-index", h.BulkIndexWorkspace)
	mux.HandleFunc("GET /api/workspaces/{slug}/bulk-index/active", h.ActiveWorkspaceBulkIndex)
	mux.HandleFunc("POST /api/workspaces/{slug}/repos/{repo}/ref", h.SetWorkspaceRepoRef)
	mux.HandleFunc("POST /api/workspaces/{slug}/repos/{repo}/fetch", h.FetchWorkspaceRepo)
	mux.HandleFunc("POST /api/workspaces/{slug}/repos/{repo}/checkout", h.CheckoutWorkspaceRepo)
	mux.HandleFunc("POST /api/workspaces/{slug}/repos/{repo}/index", h.IndexWorkspaceRepo)
	mux.HandleFunc("GET /api/repos", h.ListRepos)
	mux.HandleFunc("GET /api/repos/{id}", h.GetRepo)
	mux.HandleFunc("GET /api/search", h.Search)
	mux.HandleFunc("POST /api/search/integrations", h.SearchIntegrations)
	mux.HandleFunc("GET /api/functions/data-access", h.GetFunctionDataAccess)
	mux.HandleFunc("GET /api/functions/integrations", h.GetFunctionIntegrations)
	mux.HandleFunc("GET /api/classes/integrations", h.GetClassIntegrations)
	mux.HandleFunc("POST /api/trace", h.Trace)
	mux.HandleFunc("POST /api/trace/expand", h.TraceExpand)
	mux.HandleFunc("POST /api/impact", h.Impact)
	mux.HandleFunc("POST /api/verify", h.Impact)
	mux.HandleFunc("GET /api/verify/runs", h.ListVerifyRuns)
	mux.HandleFunc("POST /api/flow", h.Flow)
	mux.HandleFunc("GET /api/contracts", h.ListContracts)
	mux.HandleFunc("GET /api/contracts/{repo}", h.GetContract)
	mux.HandleFunc("GET /api/endpoints", h.ListEndpoints)
	mux.HandleFunc("GET /api/graphql/operations", h.ListGraphQLOperations)
	mux.HandleFunc("GET /api/graphql/usages", h.ListGraphQLOperationUsages)
	mux.HandleFunc("GET /api/graphql/entrypoints", h.ListGraphQLBackendEntrypoints)
	mux.HandleFunc("GET /api/graphql/controllers", h.ListGraphQLControllers)
	mux.HandleFunc("GET /api/graphql/flow", h.GraphQLFlow)
	mux.HandleFunc("GET /api/azure/functions", h.ListAzureFunctionTriggers)
	mux.HandleFunc("GET /api/azure/flow", h.AzureFunctionFlow)

	mux.HandleFunc("GET /api/graph/path", h.FindPath)
	mux.HandleFunc("GET /api/graph/repo-dependencies", h.GetRepoDependencies)

	return mux
}

func (s *Server) Handler() http.Handler {
	return s.handler
}

func (s *Server) Close() {
	if s.handlers != nil {
		s.handlers.Close()
	}
	s.storage.Close()
}
