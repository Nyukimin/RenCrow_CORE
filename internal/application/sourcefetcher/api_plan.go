package sourcefetcher

import (
	"fmt"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/l1sqlite"
	"net/url"
	"path"
	"strings"
)

type sourceAPIPlan struct {
	FetchURL string
	Fetcher  string
}

func planSourceAPI(source l1sqlite.L1SourceRegistryEntry) sourceAPIPlan {
	if override := stringFromMeta(source.Meta, "api_url", ""); override != "" {
		return sourceAPIPlan{FetchURL: override, Fetcher: source.Kind + "_api"}
	}
	switch source.Kind {
	case l1sqlite.L1SourceKindGitHub:
		if owner, repo := githubOwnerRepo(source.URL); owner != "" && repo != "" {
			limit := int64FromMeta(source.Meta, "per_page", 30)
			return sourceAPIPlan{
				FetchURL: fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=%d", url.PathEscape(owner), url.PathEscape(repo), limit),
				Fetcher:  "github_releases_api",
			}
		}
	case l1sqlite.L1SourceKindHuggingFace:
		if repo := huggingFaceRepoID(source.URL); repo != "" {
			return sourceAPIPlan{
				FetchURL: "https://huggingface.co/api/models/" + repo,
				Fetcher:  "huggingface_model_api",
			}
		}
	case l1sqlite.L1SourceKindMediaWiki:
		if endpoint := mediaWikiRecentChangesEndpoint(source.URL, int64FromMeta(source.Meta, "limit", 50)); endpoint != "" {
			return sourceAPIPlan{FetchURL: endpoint, Fetcher: "mediawiki_recentchanges_api"}
		}
	}
	return sourceAPIPlan{FetchURL: source.URL, Fetcher: "source_registry_http"}
}

func githubOwnerRepo(rawURL string) (string, string) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", ""
	}
	parts := strings.Split(strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/"), "/")
	if len(parts) < 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func huggingFaceRepoID(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])
}

func mediaWikiRecentChangesEndpoint(rawURL string, limit int64) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	if limit <= 0 {
		limit = 50
	}
	u.Path = path.Join(strings.TrimSuffix(u.Path, "/"), "w", "api.php")
	q := u.Query()
	q.Set("action", "query")
	q.Set("list", "recentchanges")
	q.Set("rcprop", "title|timestamp|ids|sizes|user|comment")
	q.Set("rclimit", fmt.Sprintf("%d", limit))
	q.Set("format", "json")
	q.Set("origin", "*")
	u.RawQuery = q.Encode()
	return u.String()
}
