package audit

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type UnresolvedHTTPRepoSummary struct {
	Repo           string `json:"repo"`
	Count          int    `json:"count"`
	PathUnmatched  int    `json:"pathUnmatched"`
	MethodMismatch int    `json:"methodMismatched"`
}

type UnresolvedHTTPSample struct {
	Repo             string   `json:"repo"`
	CallerID         string   `json:"callerId"`
	Method           string   `json:"method"`
	Path             string   `json:"path"`
	LineNumber       int      `json:"lineNumber"`
	ClientType       string   `json:"clientType"`
	Reason           string   `json:"reason"`
	AvailableMethods []string `json:"availableMethods,omitempty"`
}

type UnresolvedHTTPDrilldown struct {
	TopRepos []UnresolvedHTTPRepoSummary `json:"topRepos"`
	Samples  []UnresolvedHTTPSample      `json:"samples"`
}

func CollectUnresolvedHTTPDrilldown(ctx context.Context, pool *pgxpool.Pool, repoLimit, sampleLimit int) (UnresolvedHTTPDrilldown, error) {
	classifications, err := collectHTTPClassifications(ctx, pool)
	if err != nil {
		return UnresolvedHTTPDrilldown{}, err
	}
	return BuildUnresolvedHTTPDrilldown(classifications, repoLimit, sampleLimit), nil
}
