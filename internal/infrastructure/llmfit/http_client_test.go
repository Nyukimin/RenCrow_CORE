package llmfit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newLLMFitTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPProviderHealthAndSystem(t *testing.T) {
	srv := newLLMFitTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(readFixture(t, "health.json")))
		case "/api/v1/system":
			_, _ = w.Write([]byte(readFixture(t, "system.json")))
		default:
			http.NotFound(w, r)
		}
	})
	at := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	p := NewHTTPProvider("node-a", srv.URL+"/", time.Second)
	p.Now = func() time.Time { return at }

	if err := p.Health(context.Background()); err != nil {
		t.Fatalf("health: %v", err)
	}
	profile, err := p.System(context.Background())
	if err != nil {
		t.Fatalf("system: %v", err)
	}
	if profile.NodeID != "node-a" || profile.NodeName != "node-a" || len(profile.GPUs) != 1 || profile.Backend != "SYCL" || !profile.CollectedAt.Equal(at) {
		t.Fatalf("profile mismatch (unknown-node must not leak): %+v", profile)
	}
}

func TestHTTPProviderHealthRejectsNonOKStatus(t *testing.T) {
	srv := newLLMFitTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"degraded","node":{"name":"node-a","os":"linux"}}`))
	})
	p := NewHTTPProvider("node-a", srv.URL, time.Second)
	err := p.Health(context.Background())
	if err == nil || !strings.Contains(err.Error(), "degraded") {
		t.Fatalf("expected degraded health error, got %v", err)
	}
}

func TestHTTPProviderTopModelsSendsNormalizedQuery(t *testing.T) {
	var gotPath, gotQuery string
	srv := newLLMFitTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(modelsFixture))
	})
	p := NewHTTPProvider("node-a", srv.URL, time.Second)
	models, err := p.TopModels(context.Background(), ModelFitQuery{
		Limit: 5, MinFit: " Marginal ", UseCase: "CODING", Runtime: "llamacpp", MaxContext: 32768, Sort: "score", IncludeTooTight: true,
	})
	if err != nil {
		t.Fatalf("top models: %v", err)
	}
	if gotPath != "/api/v1/models/top" {
		t.Fatalf("path=%s", gotPath)
	}
	for _, want := range []string{"limit=5", "min_fit=marginal", "use_case=coding", "runtime=llamacpp", "max_context=32768", "sort=score", "include_too_tight=true"} {
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query %q missing %s", gotQuery, want)
		}
	}
	if strings.Contains(gotQuery, "search=") {
		t.Fatalf("top query must not carry search: %q", gotQuery)
	}
	if len(models) != 3 {
		t.Fatalf("models=%d want 3 (partial element skipped)", len(models))
	}
	if models[0].NodeID != "node-a" {
		t.Fatalf("node id must be the provider node id, got %q", models[0].NodeID)
	}
}

func TestHTTPProviderSearchModelsUsesModelsEndpoint(t *testing.T) {
	var gotPath, gotQuery string
	srv := newLLMFitTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(modelsFixture))
	})
	p := NewHTTPProvider("node-a", srv.URL, time.Second)
	if _, err := p.SearchModels(context.Background(), ModelFitQuery{Keyword: "qwen3 coder", Limit: 3}); err != nil {
		t.Fatalf("search: %v", err)
	}
	if gotPath != "/api/v1/models" {
		t.Fatalf("path=%s", gotPath)
	}
	if !strings.Contains(gotQuery, "search=qwen3+coder") || !strings.Contains(gotQuery, "limit=3") {
		t.Fatalf("query=%q", gotQuery)
	}
}

func TestHTTPProviderRejectsInvalidQueryWithoutRequest(t *testing.T) {
	var calls int32
	srv := newLLMFitTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(modelsFixture))
	})
	p := NewHTTPProvider("node-a", srv.URL, time.Second)
	cases := []ModelFitQuery{
		{MinFit: "excellent"},
		{Runtime: "rm -rf"},
		{UseCase: "hacking"},
		{Sort: "random"},
		{Limit: -1},
		{MaxContext: -5},
	}
	for _, q := range cases {
		_, err := p.TopModels(context.Background(), q)
		if ErrorKindOf(err) != ErrorKindBadRequest {
			t.Fatalf("query %+v: kind=%s err=%v", q, ErrorKindOf(err), err)
		}
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("invalid queries must not reach the server, calls=%d", calls)
	}
}

func TestHTTPProviderMapsStatusCodesToErrorKinds(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantKind ErrorKind
		wantMsg  string
	}{
		{name: "400 with error json", status: http.StatusBadRequest, body: `{"error":"unknown runtime"}`, wantKind: ErrorKindBadRequest, wantMsg: "unknown runtime"},
		{name: "404 plain", status: http.StatusNotFound, body: `not here`, wantKind: ErrorKindBadRequest, wantMsg: "404"},
		{name: "500 with error json", status: http.StatusInternalServerError, body: `{"error":"detector crashed"}`, wantKind: ErrorKindServerError, wantMsg: "detector crashed"},
		{name: "502 html", status: http.StatusBadGateway, body: `<html>bad</html>`, wantKind: ErrorKindServerError, wantMsg: "502"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newLLMFitTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			p := NewHTTPProvider("node-a", srv.URL, time.Second)
			_, err := p.TopModels(context.Background(), ModelFitQuery{})
			if ErrorKindOf(err) != tt.wantKind {
				t.Fatalf("kind=%s want %s (err=%v)", ErrorKindOf(err), tt.wantKind, err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("err=%q must contain %q", err.Error(), tt.wantMsg)
			}
			var perr *ProviderError
			if !errors.As(err, &perr) || perr.NodeID != "node-a" {
				t.Fatalf("error must be a ProviderError carrying node id: %v", err)
			}
		})
	}
}

func TestHTTPProviderInvalidJSONIsInvalidResponse(t *testing.T) {
	srv := newLLMFitTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models": [`))
	})
	p := NewHTTPProvider("node-a", srv.URL, time.Second)
	_, err := p.TopModels(context.Background(), ModelFitQuery{})
	if ErrorKindOf(err) != ErrorKindInvalidResponse {
		t.Fatalf("kind=%s err=%v", ErrorKindOf(err), err)
	}
	_, err = p.System(context.Background())
	if ErrorKindOf(err) != ErrorKindInvalidResponse {
		t.Fatalf("system kind=%s err=%v", ErrorKindOf(err), err)
	}
}

func TestHTTPProviderTimeoutIsUnreachable(t *testing.T) {
	release := make(chan struct{})
	srv := newLLMFitTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	p := NewHTTPProvider("node-a", srv.URL, 50*time.Millisecond)
	start := time.Now()
	_, err := p.System(context.Background())
	if ErrorKindOf(err) != ErrorKindUnreachable {
		t.Fatalf("kind=%s err=%v", ErrorKindOf(err), err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout must be enforced by the client")
	}
}

func TestHTTPProviderConnectionRefusedIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	p := NewHTTPProvider("node-a", url, time.Second)
	if err := p.Health(context.Background()); ErrorKindOf(err) != ErrorKindUnreachable {
		t.Fatalf("health kind=%s err=%v", ErrorKindOf(err), err)
	}
	if _, err := p.System(context.Background()); ErrorKindOf(err) != ErrorKindUnreachable {
		t.Fatalf("system kind=%s err=%v", ErrorKindOf(err), err)
	}
}

func TestHTTPProviderRequiresBaseURL(t *testing.T) {
	p := NewHTTPProvider("node-a", "   ", time.Second)
	if err := p.Health(context.Background()); ErrorKindOf(err) != ErrorKindConfiguration {
		t.Fatalf("kind=%s err=%v", ErrorKindOf(err), err)
	}
}
