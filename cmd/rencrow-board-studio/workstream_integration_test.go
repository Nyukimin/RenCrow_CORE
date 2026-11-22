package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	vieweradapter "github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	persistenceworkstream "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
)

func TestWorkstreamProjectDomainFlowsThroughViewerStoreAndStudioSnapshot(t *testing.T) {
	for _, test := range []struct {
		name string
		open func(*testing.T) (vieweradapter.WorkstreamLister, func())
	}{
		{
			name: "JSONL",
			open: func(t *testing.T) (vieweradapter.WorkstreamLister, func()) {
				return persistenceworkstream.NewJSONLStore(filepath.Join(t.TempDir(), "workstream")), func() {}
			},
		},
		{
			name: "SQLite",
			open: func(t *testing.T) (vieweradapter.WorkstreamLister, func()) {
				store, err := persistenceworkstream.NewSQLiteStore(filepath.Join(t.TempDir(), "workstream.sqlite"))
				if err != nil {
					t.Fatalf("open SQLite Workstream store: %v", err)
				}
				return store, func() {
					if err := store.Close(); err != nil {
						t.Errorf("close SQLite Workstream store: %v", err)
					}
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, closeStore := test.open(t)
			defer closeStore()

			mux := http.NewServeMux()
			mux.HandleFunc("/viewer/workstreams", vieweradapter.HandleWorkstreamStatus(store))
			mux.HandleFunc("GET /viewer/agents", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"agents":[]}`))
			})
			mux.HandleFunc("GET /viewer/tasks", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"items":[]}`))
			})
			mux.HandleFunc("GET /viewer/atlas", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"current":[],"backlog":[],"pipeline":[]}`))
			})
			mux.HandleFunc("GET /viewer/board/agent-work", func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "not configured", http.StatusServiceUnavailable)
			})
			server := httptest.NewServer(mux)
			defer server.Close()

			client := server.Client()
			createdAt := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
			game := "game"
			create := domainworkstream.Workstream{
				WorkstreamID: "ws-domain-flow", Name: "Domain flow", Status: domainworkstream.StatusActive,
				ProjectDomain: &game, CreatedAt: createdAt,
			}
			postWorkstreamForStudioTest(t, client, server.URL+"/viewer/workstreams", create)

			// Omitting project_domain from a partial update must preserve the stored value.
			update := domainworkstream.Workstream{
				WorkstreamID: create.WorkstreamID, Name: create.Name, Status: domainworkstream.StatusPaused, CreatedAt: createdAt,
			}
			postWorkstreamForStudioTest(t, client, server.URL+"/viewer/workstreams", update)
			assertWorkstreamDomainHTTPForStudioTest(t, client, server.URL+"/viewer/workstreams?limit=100", game, domainworkstream.StatusPaused)
			assertStudioWorkstreamForStudioTest(t, server.URL, client, create.WorkstreamID, game, "waiting")

			clear := ""
			update.ProjectDomain = &clear
			postWorkstreamForStudioTest(t, client, server.URL+"/viewer/workstreams", update)
			assertWorkstreamDomainHTTPForStudioTest(t, client, server.URL+"/viewer/workstreams?limit=100", clear, domainworkstream.StatusPaused)
			assertStudioWorkstreamForStudioTest(t, server.URL, client, create.WorkstreamID, clear, "waiting")
		})
	}
}

func postWorkstreamForStudioTest(t *testing.T, client *http.Client, endpoint string, item domainworkstream.Workstream) {
	t.Helper()
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal Workstream request: %v", err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("create Workstream request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST Workstream: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("POST Workstream status=%d", response.StatusCode)
	}
}

func assertWorkstreamDomainHTTPForStudioTest(t *testing.T, client *http.Client, endpoint, wantDomain, wantState string) {
	t.Helper()
	response, err := client.Get(endpoint)
	if err != nil {
		t.Fatalf("GET Workstream status: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET Workstream status=%d", response.StatusCode)
	}
	var body struct {
		Workstreams []domainworkstream.Workstream `json:"workstreams"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode Workstream status: %v", err)
	}
	if len(body.Workstreams) == 0 {
		t.Fatal("GET Workstream status returned no rows")
	}
	latest := body.Workstreams[0]
	if latest.ProjectDomain == nil || *latest.ProjectDomain != wantDomain || latest.Status != wantState {
		t.Fatalf("GET latest Workstream=%+v, want project_domain=%q status=%q", latest, wantDomain, wantState)
	}
}

func assertStudioWorkstreamForStudioTest(t *testing.T, upstream string, client *http.Client, workstreamID, wantDomain, wantState string) {
	t.Helper()
	origin, err := url.Parse(upstream)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	snapshot, ok := (source{viewer: origin, client: client}).snapshot(t.Context())
	if !ok {
		t.Fatal("BOARD source snapshot unavailable although Workstream API is available")
	}
	if len(snapshot.Projects) != 1 {
		t.Fatalf("BOARD projects=%+v, want one project", snapshot.Projects)
	}
	project := snapshot.Projects[0]
	if project.ID != workstreamID || project.ProjectDomain == nil || *project.ProjectDomain != wantDomain || project.State != wantState {
		t.Fatalf("BOARD project=%+v, want id=%q domain=%q state=%q", project, workstreamID, wantDomain, wantState)
	}
}
