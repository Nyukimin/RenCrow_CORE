package main

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	coreresearch "github.com/Nyukimin/RenCrow_CORE/internal/application/research"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/provider"
)

type researchSourceSpy struct {
	searched, acquired int
	document           c.Document
}

func (s *researchSourceSpy) Discover(context.Context, engine.DiscoveryRequest) (engine.DiscoveryResult, error) {
	s.searched++
	return engine.DiscoveryResult{}, nil
}
func (s *researchSourceSpy) Acquire(context.Context, c.Candidate) (engine.AcquireResult, error) {
	s.acquired++
	return engine.AcquireResult{Document: s.document}, nil
}

func TestResearchSourceRoutesRemainSeparateFromWebAndExplicitlyConfigured(t *testing.T) {
	web, wiki := &researchSourceSpy{}, &researchSourceSpy{}
	routes := &researchSources{web: web, sources: map[string]researchSourcePort{"mediawiki": wiki}, policy: func(context.Context, c.Candidate) (bool, bool) { return true, true }}
	_, _ = routes.Discover(context.Background(), engine.DiscoveryRequest{Provider: c.Capability{ProviderID: "mediawiki"}})
	_, _ = routes.Acquire(context.Background(), c.Candidate{ProviderID: "mediawiki"})
	_, _ = routes.Acquire(context.Background(), c.Candidate{ProviderID: "direct"})
	blocked, err := routes.Acquire(context.Background(), c.Candidate{ProviderID: "pypi"})
	if wiki.searched != 1 || wiki.acquired != 1 || web.acquired != 1 || err == nil || blocked.Code != "unavailable" {
		t.Fatalf("source route fallback: web=%+v wiki=%+v blocked=%+v err=%v", web, wiki, blocked, err)
	}
	for _, cfg := range []config.ResearchConfig{
		{PermittedProviders: []string{"mediawiki"}},
		{PermittedProviders: []string{"pypi"}},
		{PermittedProviders: []string{"unknown"}},
		{PermittedProviders: []string{"rss_atom", "rss_atom"}},
	} {
		if _, _, err := configureResearchSources(cfg, coreresearch.NewAdapters(nil, nil, nil), false, nil); err == nil {
			t.Fatalf("unverified configuration accepted: %+v", cfg)
		}
	}
	r, caps, err := configureResearchSources(config.ResearchConfig{PermittedProviders: []string{"rss_atom", "mediawiki", "pypi"}, MediaWikiSite: "ja", PyPIAllowedLicenses: []string{"MIT"}}, coreresearch.NewAdapters(nil, nil, nil), false, nil)
	if err != nil || len(caps) != 3 || len(r.sources) != 2 || caps[1].SourceKinds[0] != "mediawiki" || caps[2].SourceKinds[0] != "pypi" {
		t.Fatalf("source configuration lost typed capability: %+v %v", caps, err)
	}
}

func TestResearchDirectCapabilityTracksConfiguredFetcherWithoutAddingSearch(t *testing.T) {
	_, fixture, _ := researchOwnerFixture(t)
	web := fixture.Ports.Acquisition.(*coreresearch.Adapters)
	routes, caps, err := configureResearchSources(config.ResearchConfig{}, web, true, func(context.Context, c.Candidate) (bool, bool) { return true, true })
	if err != nil || len(caps) != 1 || caps[0].ProviderID != "direct" || len(caps[0].SourceKinds) != 1 || caps[0].SourceKinds[0] != "web" || caps[0].Search || !caps[0].FetchText || !caps[0].Enabled || !caps[0].Ready || !caps[0].FreeAllowed {
		t.Fatalf("configured direct acquisition capability is incorrect: caps=%+v err=%v", caps, err)
	}
	acquired, err := routes.Acquire(context.Background(), c.Candidate{ProviderID: "direct", SourceKind: "web", URL: "https://example.org/registered"})
	if err != nil || acquired.Document.FinalURL != "https://example.org/registered" {
		t.Fatalf("registered direct source did not use the configured web acquisition route: acquired=%+v err=%v", acquired, err)
	}
	_, unconfiguredCaps, err := configureResearchSources(config.ResearchConfig{}, coreresearch.NewAdapters(nil, nil, nil), false, nil)
	if err != nil || len(unconfiguredCaps) != 0 {
		t.Fatalf("nil fetcher was reported as a ready direct route: caps=%+v err=%v", unconfiguredCaps, err)
	}
}

func TestResearchTechnicalSourcesRequireExplicitPoliciesAndNeverFallBackToWeb(t *testing.T) {
	ids := []string{provider.GitHubID, provider.HuggingFaceID, provider.ArxivID}
	licenses := map[string][]string{
		provider.GitHubID: {"MIT"}, provider.HuggingFaceID: {"apache-2.0", "cc-by-4.0"}, provider.ArxivID: {"CC0-1.0"},
	}
	for _, id := range ids {
		if _, _, err := configureResearchSources(config.ResearchConfig{PermittedProviders: []string{id}}, nil, false, nil); err == nil {
			t.Fatalf("technical source %q accepted without a retention policy", id)
		}
	}
	for _, policy := range []map[string][]string{{"unknown": {"MIT"}}, {provider.GitHubID: {" MIT"}}} {
		if _, _, err := configureResearchSources(config.ResearchConfig{ProviderAllowedLicenses: policy}, nil, false, nil); err == nil {
			t.Fatalf("noncanonical provider retention policy accepted: %+v", policy)
		}
	}
	routes, caps, err := configureResearchSources(config.ResearchConfig{PermittedProviders: ids, ProviderAllowedLicenses: licenses}, nil, false, nil)
	if err != nil || len(caps) != len(ids) || len(routes.sources) != len(ids) {
		t.Fatalf("technical source composition is unavailable: caps=%+v err=%v", caps, err)
	}
	wantKinds := []string{"github", "huggingface", "arxiv"}
	for i, cap := range caps {
		if cap.ProviderID != ids[i] || len(cap.SourceKinds) != 1 || cap.SourceKinds[0] != wantKinds[i] || !cap.Search || !cap.FetchText || !cap.FreeAllowed {
			t.Fatalf("technical source contract mismatch: %+v", cap)
		}
	}
	web := &researchSourceSpy{}
	disabled := &researchSources{web: web, sources: map[string]researchSourcePort{}}
	for _, id := range ids {
		got, err := disabled.Acquire(context.Background(), c.Candidate{ProviderID: id})
		if err == nil || got.Code != "unavailable" || web.acquired != 0 {
			t.Fatalf("disabled %q fell back to generic web: result=%+v calls=%d err=%v", id, got, web.acquired, err)
		}
	}
}

func TestResearchSourcePolicyDeniesTextBeforeEngineRetention(t *testing.T) {
	for _, finalURL := range []string{"https://example.org/allowed", "https://elsewhere.example/document"} {
		source := &researchSourceSpy{document: c.Document{OriginalURL: "https://example.org/allowed", FinalURL: finalURL, Text: "retained source text", RetentionAllowed: true}}
		routes := &researchSources{sources: map[string]researchSourcePort{"mediawiki": source}, policy: func(_ context.Context, candidate c.Candidate) (bool, bool) {
			return candidate.URL == "https://example.org/allowed", false
		}}
		blocked, err := routes.Acquire(context.Background(), c.Candidate{ProviderID: "mediawiki", URL: "https://forbidden.example/source"})
		if err != nil || blocked.Code != "blocked_by_policy" || blocked.Document.Text != "" || source.acquired != 0 {
			t.Fatalf("denied original source reached acquisition/retention: result=%+v err=%v", blocked, err)
		}
		got, err := routes.Acquire(context.Background(), c.Candidate{ProviderID: "mediawiki", URL: "https://example.org/allowed"})
		if err != nil || source.acquired != 1 {
			t.Fatalf("allowed source acquisition failed: %+v %v", got, err)
		}
		if finalURL != "https://example.org/allowed" && (got.Code != "blocked_by_policy" || got.Document.Text != "") {
			t.Fatalf("denied final source text escaped into Engine: %+v", got)
		}
	}
}

func TestResearchDirectSourcePolicyDeniesAcquisitionBeforeFetch(t *testing.T) {
	web := &researchSourceSpy{}
	routes := &researchSources{web: web, policy: func(context.Context, c.Candidate) (bool, bool) { return false, false }}
	result, err := routes.Acquire(context.Background(), c.Candidate{ProviderID: "direct", URL: "https://forbidden.example/source"})
	if err != nil || result.Code != "blocked_by_policy" || web.acquired != 0 {
		t.Fatalf("unapproved direct source reached fetch: result=%+v calls=%d err=%v", result, web.acquired, err)
	}
}

func TestResearchProviderRetentionDoesNotAuthorizeDirectFetching(t *testing.T) {
	policy := configuredResearchSourcePolicy(config.ResearchConfig{
		RetentionAllowedHosts:         []string{"www.rfc-editor.org"},
		ProviderRetentionAllowedHosts: map[string][]string{provider.GitHubID: {"github.com"}},
	})
	for _, fixture := range []struct {
		provider, url string
		retained      bool
	}{
		{provider.GitHubID, "https://github.com/owner/repo", true},
		{"direct", "https://github.com/owner/repo", false},
		{"direct", "https://www.rfc-editor.org/rfc/rfc1149.txt", true},
		{provider.GitHubID, "https://github.com.evil.example/owner/repo", false},
		{provider.GitHubID, "https://user@github.com/owner/repo", false},
	} {
		retained, _ := policy(context.Background(), c.Candidate{ProviderID: fixture.provider, URL: fixture.url})
		if retained != fixture.retained {
			t.Fatalf("provider-scoped retention leaked into another route: %+v retained=%v", fixture, retained)
		}
	}
}
