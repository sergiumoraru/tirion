package owners

import (
	"sync"

	"github.com/sergiumoraru/tirion/internal/config"
	"github.com/sergiumoraru/tirion/internal/graph"
)

type Resolver struct {
	storage        *graph.Storage
	config         *config.OwnersConfig
	mu             sync.Mutex
	repoCodeowners map[string]*CodeOwners
	repoPaths      map[string]string
}

type Match struct {
	Owners []string
	Source string
}

func NewResolver(storage *graph.Storage) *Resolver {
	cfg, _ := config.LoadOwnersConfig()
	if cfg == nil {
		cfg = config.DefaultOwnersConfig()
	}
	return &Resolver{
		storage:        storage,
		config:         cfg,
		repoCodeowners: map[string]*CodeOwners{},
		repoPaths:      map[string]string{},
	}
}

func (r *Resolver) Resolve(repoName, filePath string) Match {
	if repoName == "" || filePath == "" {
		return Match{}
	}
	co := r.getCodeOwners(repoName)
	if co != nil {
		// The last matching rule decides, including one that clears ownership.
		if owners, matched := co.MatchRule(filePath); matched {
			return Match{Owners: owners, Source: "codeowners"}
		}
	}
	if override, ok := r.config.RepoOverrides[repoName]; ok && len(override.Owners) > 0 {
		return Match{Owners: override.Owners, Source: "repo_override"}
	}
	if len(r.config.DefaultOwners) > 0 {
		return Match{Owners: r.config.DefaultOwners, Source: "default"}
	}
	return Match{}
}

func (r *Resolver) getCodeOwners(repoName string) *CodeOwners {
	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.repoCodeowners[repoName]; ok {
		return existing
	}
	repoPath := r.getRepoPath(repoName)
	if repoPath == "" {
		r.repoCodeowners[repoName] = nil
		return nil
	}
	co, err := LoadCodeOwners(repoPath)
	if err != nil {
		r.repoCodeowners[repoName] = nil
		return nil
	}
	r.repoCodeowners[repoName] = co
	return co
}

func (r *Resolver) getRepoPath(repoName string) string {
	if path, ok := r.repoPaths[repoName]; ok {
		return path
	}
	repo, err := r.storage.GetRepoByName(repoName)
	if err != nil || repo == nil {
		r.repoPaths[repoName] = ""
		return ""
	}
	r.repoPaths[repoName] = repo.Path
	return repo.Path
}
