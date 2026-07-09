package viewer

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleGameObserverPageRewritesLiveEndpoint(t *testing.T) {
	uiPath := filepath.Join(t.TempDir(), "index.html")
	html := `<!doctype html><html><body><input id="liveBase" value="http://127.0.0.1:18791"></body></html>`
	if err := os.WriteFile(uiPath, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/games/observer", nil)
	HandleGameObserverPage(GameObserverProxyOptions{UIPath: uiPath}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="/viewer/games/observer-api"`) {
		t.Fatalf("observer page did not rewrite live endpoint: %s", body)
	}
	if !strings.Contains(body, "rencrowAutoLoadLiveObserver") {
		t.Fatalf("observer page did not inject live autoload script: %s", body)
	}
	if !strings.Contains(body, `window.RenCrowGameObserverLiveBase = "/viewer/games/observer-api"`) {
		t.Fatalf("observer page did not inject same-origin observer base: %s", body)
	}
	if !strings.Contains(body, `window.dispatchEvent(new Event("rencrow-observer-load-live"))`) {
		t.Fatalf("observer page did not inject live load event: %s", body)
	}
}

func TestHandleGameObserverProxyForwardsReadOnlyGameAPI(t *testing.T) {
	var upstreamPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/games/observer-api/games/sessions?limit=1", nil)
	HandleGameObserverProxy(GameObserverProxyOptions{
		ObserverBaseURL: upstream.URL,
		HTTPClient:      upstream.Client(),
	}).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamPath != "/games/sessions?limit=1" {
		t.Fatalf("upstream path=%q", upstreamPath)
	}
	if strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestHandleGameObserverProxyForwardsSessionActionPost(t *testing.T) {
	var upstreamMethod string
	var upstreamPath string
	var upstreamContentType string
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamMethod = r.Method
		upstreamPath = r.URL.RequestURI()
		upstreamContentType = r.Header.Get("Content-Type")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		upstreamBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"session_id":"sg_retry","status":"completed"}}`))
	}))
	t.Cleanup(upstream.Close)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/viewer/games/observer-api/games/sessions/sg_source/retry",
		strings.NewReader(`{"source":"ui"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	HandleGameObserverProxy(GameObserverProxyOptions{
		ObserverBaseURL: upstream.URL,
		HTTPClient:      upstream.Client(),
	}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamMethod != http.MethodPost {
		t.Fatalf("upstream method=%q", upstreamMethod)
	}
	if upstreamPath != "/games/sessions/sg_source/retry" {
		t.Fatalf("upstream path=%q", upstreamPath)
	}
	if upstreamContentType != "application/json" {
		t.Fatalf("upstream content-type=%q", upstreamContentType)
	}
	if upstreamBody != `{"source":"ui"}` {
		t.Fatalf("upstream body=%q", upstreamBody)
	}
	if !strings.Contains(rec.Body.String(), `"session_id":"sg_retry"`) {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestHandleGameObserverProxyRejectsNonGamePath(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/games/observer-api/http://example.test", nil)
	HandleGameObserverProxy(GameObserverProxyOptions{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
