package sourcefetcher

import (
	"strings"
	"testing"

	conversationpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation"
)

func TestPlanSourceAPIUsesSpecificProviderEndpoints(t *testing.T) {
	tests := []struct {
		name    string
		source  conversationpersistence.L1SourceRegistryEntry
		wantURL string
		fetcher string
	}{
		{
			name:    "github releases",
			source:  conversationpersistence.L1SourceRegistryEntry{Kind: conversationpersistence.L1SourceKindGitHub, URL: "https://github.com/openclaw/openclaw", Meta: map[string]interface{}{"per_page": float64(12)}},
			wantURL: "https://api.github.com/repos/openclaw/openclaw/releases?per_page=12",
			fetcher: "github_releases_api",
		},
		{
			name:    "hugging face model",
			source:  conversationpersistence.L1SourceRegistryEntry{Kind: conversationpersistence.L1SourceKindHuggingFace, URL: "https://huggingface.co/org/model"},
			wantURL: "https://huggingface.co/api/models/org/model",
			fetcher: "huggingface_model_api",
		},
		{
			name:    "mediawiki recent changes",
			source:  conversationpersistence.L1SourceRegistryEntry{Kind: conversationpersistence.L1SourceKindMediaWiki, URL: "https://wiki.example.org/", Meta: map[string]interface{}{"limit": float64(7)}},
			wantURL: "https://wiki.example.org/w/api.php",
			fetcher: "mediawiki_recentchanges_api",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := planSourceAPI(tt.source)
			if !strings.HasPrefix(plan.FetchURL, tt.wantURL) || plan.Fetcher != tt.fetcher {
				t.Fatalf("plan=%#v", plan)
			}
		})
	}
}

func TestPlanSourceAPIHonorsOverride(t *testing.T) {
	plan := planSourceAPI(conversationpersistence.L1SourceRegistryEntry{
		Kind: conversationpersistence.L1SourceKindGitHub,
		URL:  "https://github.com/openclaw/openclaw",
		Meta: map[string]interface{}{"api_url": "http://127.0.0.1:1234/api"},
	})
	if plan.FetchURL != "http://127.0.0.1:1234/api" || plan.Fetcher != "github_api" {
		t.Fatalf("plan=%#v", plan)
	}
}
