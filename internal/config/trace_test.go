package config

import "testing"

func TestRepositoryOverridesDoNotShareSlices(t *testing.T) {
	prefixes := make([]string, 1, 8)
	prefixes[0] = "/base"
	cfg := &TraceConfig{
		HTTP:      HTTPConfig{ContextPathPrefixes: prefixes},
		SQS:       SQSConfig{QueuePrefixes: prefixes},
		NameBased: NameBasedConfig{Include: prefixes, Exclude: prefixes},
		RepoOverrides: map[string]TraceRepoConfig{
			"first":  {HTTP: HTTPConfig{ContextPathPrefixes: []string{"/first"}}, SQS: SQSConfig{QueuePrefixes: []string{"first"}}, NameBased: NameBasedConfig{Include: []string{"First"}, Exclude: []string{"FirstExcluded"}}},
			"second": {HTTP: HTTPConfig{ContextPathPrefixes: []string{"/second"}}, SQS: SQSConfig{QueuePrefixes: []string{"second"}}, NameBased: NameBasedConfig{Include: []string{"Second"}, Exclude: []string{"SecondExcluded"}}},
		},
	}
	firstHTTP, firstSQS, firstNames := cfg.HTTPForRepo("first"), cfg.SQSForRepo("first"), cfg.NameBasedForRepo("first")
	_ = cfg.HTTPForRepo("second")
	_ = cfg.SQSForRepo("second")
	_ = cfg.NameBasedForRepo("second")
	if firstHTTP.ContextPathPrefixes[1] != "/first" || firstSQS.QueuePrefixes[1] != "first" || firstNames.Include[1] != "First" || firstNames.Exclude[1] != "FirstExcluded" {
		t.Fatal("another repository lookup mutated previously returned settings")
	}
	firstHTTP.ContextPathPrefixes[0] = "/changed"
	firstSQS.QueuePrefixes[0] = "changed"
	firstNames.Include[0] = "changed"
	firstNames.Exclude[0] = "changed"
	if prefixes[0] != "/base" {
		t.Fatal("returned settings share the base configuration")
	}
}
