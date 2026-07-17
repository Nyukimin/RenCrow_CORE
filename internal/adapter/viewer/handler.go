package viewer

import (
	"net/http"
	"net/url"
	"os"
	"strings"
)

func HandlePage(w http.ResponseWriter, r *http.Request) {
	if target, ok := portalRedirectTarget(r, os.Getenv("RENCROW_PORTAL_URL")); ok {
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
		return
	}
	data, err := viewerFS.ReadFile("viewer.html")
	if err != nil {
		http.Error(w, "viewer page not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func portalRedirectTarget(r *http.Request, configuredURL string) (string, bool) {
	if r == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return "", false
	}
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	if mode != "lab" && mode != "view" && mode != "live" {
		return "", false
	}
	portalURL, err := url.Parse(strings.TrimSpace(configuredURL))
	if err != nil || (portalURL.Scheme != "http" && portalURL.Scheme != "https") || portalURL.Host == "" || portalURL.User != nil {
		return "", false
	}
	query := r.URL.Query()
	query.Set("mode", mode)
	portalURL.RawQuery = query.Encode()
	if portalURL.Path == "" {
		portalURL.Path = "/"
	}
	return portalURL.String(), true
}

// MessageHandler processes a user message from the viewer.
