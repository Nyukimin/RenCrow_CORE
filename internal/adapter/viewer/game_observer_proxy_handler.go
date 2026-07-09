package viewer

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultGameObserverLiveBase = "http://127.0.0.1:18791"
	gameObserverAPIBase         = "/viewer/games/observer-api"
	gameObserverProxyPrefix     = "/viewer/games/observer-api"
)

// GameObserverProxyOptions configures the read-only RenCrow_GAMES live
// observer passthrough served from the PicoClaw Viewer origin.
type GameObserverProxyOptions struct {
	UIPath          string
	ObserverBaseURL string
	HTTPClient      *http.Client
}

// HandleGameObserverPage serves the RenCrow_GAMES browser observer UI through
// PicoClaw's reachable Viewer port, rewriting its live endpoint to the
// same-origin proxy path.
func HandleGameObserverPage(opts GameObserverProxyOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		htmlBytes, err := os.ReadFile(gameObserverUIPath(opts.UIPath))
		if err != nil {
			http.Error(w, "game observer ui unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write([]byte(rewriteGameObserverHTML(string(htmlBytes))))
	}
}

// HandleGameObserverProxy exposes the local RenCrow_GAMES observer API under
// /viewer/games/observer-api/* so remote browsers do not need a second open
// port.
func HandleGameObserverProxy(opts GameObserverProxyOptions) http.HandlerFunc {
	baseURL, baseErr := parseGameObserverBaseURL(opts.ObserverBaseURL)
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !isAllowedGameObserverProxyMethod(r.Method) {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if baseErr != nil {
			http.Error(w, "game observer upstream unavailable", http.StatusServiceUnavailable)
			return
		}
		upstreamPath := strings.TrimPrefix(r.URL.Path, gameObserverProxyPrefix)
		if !strings.HasPrefix(upstreamPath, "/games/") {
			http.NotFound(w, r)
			return
		}
		upstream := *baseURL
		upstream.Path = joinURLPath(baseURL.Path, upstreamPath)
		upstream.RawQuery = r.URL.RawQuery
		req, err := http.NewRequestWithContext(r.Context(), r.Method, upstream.String(), r.Body)
		if err != nil {
			http.Error(w, "game observer upstream unavailable", http.StatusServiceUnavailable)
			return
		}
		if accept := strings.TrimSpace(r.Header.Get("Accept")); accept != "" {
			req.Header.Set("Accept", accept)
		}
		if contentType := strings.TrimSpace(r.Header.Get("Content-Type")); contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "game observer upstream unavailable", http.StatusServiceUnavailable)
			return
		}
		defer resp.Body.Close()
		if contentType := strings.TrimSpace(resp.Header.Get("Content-Type")); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
}

func isAllowedGameObserverProxyMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodOptions:
		return true
	default:
		return false
	}
}

func parseGameObserverBaseURL(raw string) (*url.URL, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		raw = strings.TrimRight(strings.TrimSpace(os.Getenv("RENCROW_GAMES_OBSERVER_URL")), "/")
	}
	if raw == "" {
		raw = defaultGameObserverLiveBase
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, &url.Error{Op: "parse", URL: raw, Err: errInvalidGameObserverBaseURL{}}
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return nil, &url.Error{Op: "parse", URL: raw, Err: errInvalidGameObserverBaseURL{}}
	}
	return parsed, nil
}

func gameObserverUIPath(configured string) string {
	if trimmed := strings.TrimSpace(configured); trimmed != "" {
		return trimmed
	}
	if env := strings.TrimSpace(os.Getenv("RENCROW_GAMES_OBSERVER_UI")); env != "" {
		return env
	}
	candidates := []string{
		filepath.Join("..", "RenCrow_GAMES", "ui", "observer", "index.html"),
		filepath.Join("RenCrow_GAMES", "ui", "observer", "index.html"),
		"/home/nyukimi/RenCrow/RenCrow_GAMES/ui/observer/index.html",
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return candidates[0]
}

func rewriteGameObserverHTML(html string) string {
	replacements := map[string]string{
		`value="http://127.0.0.1:18791"`: `value="` + gameObserverAPIBase + `"`,
		`value='http://127.0.0.1:18791'`: `value='` + gameObserverAPIBase + `'`,
	}
	for old, replacement := range replacements {
		html = strings.Replace(html, old, replacement, 1)
	}
	if strings.Contains(html, "rencrowAutoLoadLiveObserver") {
		return html
	}
	injection := `<script>
    window.RenCrowGameObserverLiveBase = "` + gameObserverAPIBase + `";
    window.addEventListener("DOMContentLoaded", () => {
      window.setTimeout(() => {
        window.dispatchEvent(new Event("rencrow-observer-load-live"));
      }, 50);
    }, { once: true });
    window.rencrowAutoLoadLiveObserver = true;
  </script>`
	if strings.Contains(html, "</body>") {
		return strings.Replace(html, "</body>", injection+"\n</body>", 1)
	}
	return html + "\n" + injection
}

func joinURLPath(basePath string, requestPath string) string {
	basePath = strings.TrimRight(basePath, "/")
	requestPath = "/" + strings.TrimLeft(requestPath, "/")
	if basePath == "" {
		return requestPath
	}
	return basePath + requestPath
}

type errInvalidGameObserverBaseURL struct{}

func (errInvalidGameObserverBaseURL) Error() string {
	return "invalid game observer base url"
}
