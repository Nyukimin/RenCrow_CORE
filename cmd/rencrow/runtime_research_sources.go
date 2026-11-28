package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	coreresearch "github.com/Nyukimin/RenCrow_CORE/internal/application/research"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/provider"
)

type researchSourcePort interface {
	engine.DiscoverPort
	engine.AcquirePort
}

func configuredResearchSourcePolicy(cfg config.ResearchConfig) coreresearch.SourcePolicy {
	allowed := func(hosts []string, raw string) bool {
		u, err := url.Parse(raw)
		if err != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return false
		}
		for _, host := range hosts {
			if strings.EqualFold(host, u.Hostname()) {
				return true
			}
		}
		return false
	}
	return func(_ context.Context, candidate c.Candidate) (bool, bool) {
		retained := allowed(cfg.RetentionAllowedHosts, candidate.URL) || allowed(cfg.ProviderRetentionAllowedHosts[candidate.ProviderID], candidate.URL)
		return retained, allowed(cfg.PrimaryHosts, candidate.URL)
	}
}

// This composition adapter selects only administrator-enabled implementations;
// Tool metadata and provider eligibility remain owned by CORE and the Engine.
type researchSources struct {
	web     researchSourcePort
	sources map[string]researchSourcePort
	policy  coreresearch.SourcePolicy
}

func (s *researchSources) Discover(ctx context.Context, req engine.DiscoveryRequest) (engine.DiscoveryResult, error) {
	if source, ok := s.sources[req.Provider.ProviderID]; ok {
		return source.Discover(ctx, req)
	}
	return s.web.Discover(ctx, req)
}

func (s *researchSources) Acquire(ctx context.Context, candidate c.Candidate) (engine.AcquireResult, error) {
	source, configured := s.sources[candidate.ProviderID]
	if !configured {
		switch candidate.ProviderID {
		case provider.MediaWikiID, provider.PyPIID, provider.GitHubID, provider.HuggingFaceID, provider.ArxivID:
			return engine.AcquireResult{Code: "unavailable"}, fmt.Errorf("requested source is not configured")
		}
		source = s.web
	}
	if source == nil || s.policy == nil {
		return engine.AcquireResult{Code: "unavailable"}, fmt.Errorf("Research acquisition or retention policy is unavailable")
	}
	retained, primary := s.policy(ctx, candidate)
	if !retained {
		return engine.AcquireResult{Code: "blocked_by_policy"}, nil
	}
	result, err := source.Acquire(ctx, candidate)
	if err != nil || result.Code != "" {
		return result, err
	}
	for _, raw := range []string{result.Document.OriginalURL, result.Document.FinalURL} {
		checked := candidate
		checked.URL = raw
		allowed, isPrimary := s.policy(ctx, checked)
		retained, primary = retained && allowed, primary && isPrimary
	}
	if !retained {
		return engine.AcquireResult{Code: "blocked_by_policy"}, nil
	}
	result.Document.Primary = result.Document.Primary && primary
	return result, nil
}

func configureResearchSources(cfg config.ResearchConfig, web *coreresearch.Adapters, directFetchReady bool, policy coreresearch.SourcePolicy) (*researchSources, []c.Capability, error) {
	routes := &researchSources{web: web, sources: map[string]researchSourcePort{}, policy: policy}
	caps := []c.Capability{}
	if directFetchReady {
		// The ordinary HTTP fetch route is acquisition-only. Its configured
		// capability supports explicit URL collection and registered web
		// refreshes, but never claims a search provider.
		caps = append(caps, c.Capability{ProviderID: "direct", SourceKinds: []string{"web"}, Search: false, FetchText: true, Enabled: true, Ready: true, FreeAllowed: true})
	}
	seen := map[string]bool{}
	for id := range cfg.ProviderRetentionAllowedHosts {
		switch id {
		case "direct", "google_browser_manual", "bing_rss", "bing_news_rss", "rss_atom", "sitemap", "searxng", "yacy", "local_cache", provider.MediaWikiID, provider.PyPIID, provider.GitHubID, provider.HuggingFaceID, provider.ArxivID:
		default:
			return nil, nil, fmt.Errorf("Research retention host policy has an unknown provider %q", id)
		}
	}
	for id, licenses := range cfg.ProviderAllowedLicenses {
		switch id {
		case provider.GitHubID, provider.HuggingFaceID, provider.ArxivID:
		default:
			return nil, nil, fmt.Errorf("Research retention policy has an unknown provider %q", id)
		}
		for _, license := range licenses {
			if license == "" || strings.TrimSpace(license) != license {
				return nil, nil, fmt.Errorf("Research provider %q has a noncanonical license entry", id)
			}
		}
	}
	for _, id := range cfg.PermittedProviders {
		if seen[id] {
			return nil, nil, fmt.Errorf("duplicate Research provider %q", id)
		}
		seen[id] = true
		kinds := []string{"web"}
		switch id {
		case "bing_rss", "bing_news_rss", "rss_atom", "sitemap", "searxng", "yacy", "local_cache":
			if id == "rss_atom" || id == "sitemap" {
				kinds = append(kinds, "rss")
			}
		case provider.MediaWikiID:
			source, err := provider.NewMediaWiki(cfg.MediaWikiSite, provider.Policy{Retain: true}, nil)
			if err != nil {
				return nil, nil, err
			}
			routes.sources[id] = source
			kinds = []string{provider.MediaWikiID}
		case provider.PyPIID:
			if len(cfg.PyPIAllowedLicenses) == 0 {
				return nil, nil, fmt.Errorf("PyPI requires an explicit retention license allowlist")
			}
			source, err := provider.NewPyPI(provider.Policy{Retain: true, Primary: true, AllowedLicenses: append([]string(nil), cfg.PyPIAllowedLicenses...)}, nil)
			if err != nil {
				return nil, nil, err
			}
			routes.sources[id] = source
			kinds = []string{provider.PyPIID}
		case provider.GitHubID, provider.HuggingFaceID, provider.ArxivID:
			licenses := cfg.ProviderAllowedLicenses[id]
			if len(licenses) == 0 {
				return nil, nil, fmt.Errorf("Research provider %q requires an explicit retention license allowlist", id)
			}
			policy := provider.Policy{Retain: true, Primary: true, AllowedLicenses: append([]string(nil), licenses...)}
			switch id {
			case provider.GitHubID:
				routes.sources[id] = provider.NewGitHub(policy, nil)
				kinds = []string{"github"}
			case provider.HuggingFaceID:
				routes.sources[id] = provider.NewHuggingFace(policy, nil)
				kinds = []string{"huggingface"}
			case provider.ArxivID:
				routes.sources[id] = provider.NewArxiv(policy, nil)
				kinds = []string{"arxiv"}
			}
		default:
			return nil, nil, fmt.Errorf("research provider %q is not a registered free acquisition route", id)
		}
		caps = append(caps, c.Capability{ProviderID: id, SourceKinds: kinds, Search: true, FetchText: true, Enabled: true, Ready: true, FreeAllowed: true, RequiresURL: id == "rss_atom" || id == "sitemap"})
	}
	return routes, caps, nil
}
