package main

import (
	"bytes"
	"context"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/sourcefetcher"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type fakeWebGatherFetcher struct {
	resp modulewebgather.FetchResponse
	err  error
	req  modulewebgather.FetchRequest
}

func (f *fakeWebGatherFetcher) FetchURL(_ context.Context, req modulewebgather.FetchRequest) (modulewebgather.FetchResponse, error) {
	f.req = req
	return f.resp, f.err
}

type fakeWebGatherSearchCache struct{}

type fakeWebGatherSourceRegistry struct {
	entry l1sqlite.L1SourceRegistryEntry
}

func (s *fakeWebGatherSourceRegistry) SaveSourceRegistryEntry(_ context.Context, entry l1sqlite.L1SourceRegistryEntry) (*l1sqlite.L1SourceRegistryEntry, error) {
	s.entry = entry
	return &entry, nil
}

func (fakeWebGatherSearchCache) Get(context.Context, string, string, time.Time) ([]modulewebgather.SearchResult, bool, error) {
	return nil, false, nil
}

func (fakeWebGatherSearchCache) Save(context.Context, string, string, []modulewebgather.SearchResult, time.Duration) error {
	return nil
}

func (fakeWebGatherSearchCache) SearchLocal(context.Context, string, int, time.Time) ([]modulewebgather.SearchResult, bool, error) {
	return []modulewebgather.SearchResult{}, false, nil
}

func TestRunWebGatherCommandURLJSON(t *testing.T) {
	fetcher := &fakeWebGatherFetcher{resp: modulewebgather.FetchResponse{
		Status:           "ok",
		URL:              "https://example.com",
		FinalURL:         "https://example.com",
		RawHash:          "sha256:abc",
		StagingID:        "stage-1",
		ValidationStatus: "pending",
	}}
	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"url", "https://example.com", "--namespace", "kb:web", "--source-id", "web:example", "--json"}, webGatherCLIDeps{Fetcher: fetcher}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"staging_id": "stage-1"`) {
		t.Fatalf("expected JSON response, got %s", out.String())
	}
	if fetcher.req.Namespace != "kb:web" || fetcher.req.SourceID != "web:example" {
		t.Fatalf("unexpected request: %+v", fetcher.req)
	}
}

func TestRunWebGatherCommandURLAllowsWebwrightFetchProvider(t *testing.T) {
	fetcher := &fakeWebGatherFetcher{resp: modulewebgather.FetchResponse{Status: "ok", URL: "https://example.com"}}
	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"url", "https://example.com", "--fetch-provider", "webwright", "--json"}, webGatherCLIDeps{Fetcher: fetcher}, &out, &errOut)
	if code != 0 {
		t.Fatalf("expected success, code=%d stderr=%s", code, errOut.String())
	}
	if fetcher.req.FetchProvider != "webwright" {
		t.Fatalf("expected webwright fetch provider, got %+v", fetcher.req)
	}
}

func TestRunWebGatherCommandURLAllowsRefresh(t *testing.T) {
	fetcher := &fakeWebGatherFetcher{resp: modulewebgather.FetchResponse{Status: "ok", URL: "https://example.com"}}
	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"url", "https://example.com", "--refresh"}, webGatherCLIDeps{Fetcher: fetcher}, &out, &errOut)
	if code != 0 {
		t.Fatalf("expected success, code=%d stderr=%s", code, errOut.String())
	}
	if !fetcher.req.Refresh {
		t.Fatalf("expected refresh request, got %+v", fetcher.req)
	}
}

func TestRunWebGatherCommandUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"url"}, webGatherCLIDeps{Fetcher: &fakeWebGatherFetcher{}}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "url is required") {
		t.Fatalf("expected usage error, code=%d stderr=%s", code, errOut.String())
	}
}

func TestRunWebGatherCommandFailureShowsErrorCode(t *testing.T) {
	fetcher := &fakeWebGatherFetcher{
		resp: modulewebgather.FetchResponse{
			Status:       "failed",
			ErrorCode:    modulewebgather.ErrFetchTimeout,
			ErrorMessage: "timeout",
		},
		err: modulewebgather.NewError(modulewebgather.ErrFetchTimeout, "timeout"),
	}
	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"url", "https://example.com"}, webGatherCLIDeps{Fetcher: fetcher}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), string(modulewebgather.ErrFetchTimeout)) {
		t.Fatalf("expected fetch timeout failure, code=%d stderr=%s", code, errOut.String())
	}
}

func TestRunWebGatherCommandSearchRequiresSearXNGURL(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"search", "ren crow", "--provider", "searxng"}, webGatherCLIDeps{}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "web_gather.searxng_base_url or --searxng-url is required") {
		t.Fatalf("expected searxng url usage error, code=%d stderr=%s", code, errOut.String())
	}
}

func TestRunWebGatherCommandSearchRequiresYaCyURL(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"search", "ren crow", "--provider", "yacy"}, webGatherCLIDeps{}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "web_gather.yacy_base_url or --yacy-url is required") {
		t.Fatalf("expected yacy config error, code=%d stderr=%s", code, errOut.String())
	}
}

func TestRunWebGatherCommandSearchUsesConfiguredSearXNGURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.URL.Query().Get("q") != "ren crow" {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"url":"https://example.com","title":"Example","content":"Snippet","engine":"test"}]}`))
	}))
	defer server.Close()

	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"search", "ren crow", "--provider", "searxng", "--json"}, webGatherCLIDeps{
		SearchCache:    fakeWebGatherSearchCache{},
		SearXNGBaseURL: server.URL,
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"provider": "searxng"`) || !strings.Contains(out.String(), `"url": "https://example.com"`) {
		t.Fatalf("expected searxng JSON response, got %s", out.String())
	}
}

func TestRunWebGatherCommandSearchAndFetchLocalCacheJSON(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"search-and-fetch", "ren crow", "--provider", "local_cache", "--max-fetches", "1", "--json"}, webGatherCLIDeps{
		Fetcher:     &fakeWebGatherFetcher{},
		SearchCache: fakeWebGatherSearchCache{},
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"items": []`) {
		t.Fatalf("expected empty JSON items, got %s", out.String())
	}
}

func TestRunWebGatherCommandSearchAndFetchRequiresSearXNGURL(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"search-and-fetch", "ren crow", "--provider", "searxng"}, webGatherCLIDeps{Fetcher: &fakeWebGatherFetcher{}}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "web_gather.searxng_base_url or --searxng-url is required") {
		t.Fatalf("expected searxng url usage error, code=%d stderr=%s", code, errOut.String())
	}
}

func TestRunWebGatherCommandRegisterURL(t *testing.T) {
	registry := &fakeWebGatherSourceRegistry{}
	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"register-url", "https://example.com/article", "--namespace", "kb:research", "--interval-sec", "600", "--json"}, webGatherCLIDeps{
		SourceRegistry: registry,
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	if registry.entry.Kind != l1sqlite.L1SourceKindWebGather || registry.entry.URL != "https://example.com/article" {
		t.Fatalf("unexpected entry: %+v", registry.entry)
	}
	if registry.entry.SourceID == "" || registry.entry.Meta["namespace"] != "kb:research" || registry.entry.FetchInterval != 10*time.Minute {
		t.Fatalf("unexpected entry defaults: %+v", registry.entry)
	}
	if !strings.Contains(out.String(), `"kind":"web_gather"`) {
		t.Fatalf("expected JSON entry, got %s", out.String())
	}
}

func TestRunWebGatherCommandRunSourceStagesPending(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>Run Source</title></head><body><article><h1>Run Source</h1><p>Collected web gather body for review.</p></article></body></html>`))
	}))
	defer server.Close()

	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	if _, err := store.SaveSourceRegistryEntry(ctx, l1sqlite.L1SourceRegistryEntry{
		SourceID:      "web:run",
		URL:           server.URL,
		Kind:          l1sqlite.L1SourceKindWebGather,
		TrustScore:    0.9,
		FetchInterval: time.Hour,
		LicenseNote:   "review source terms before promotion",
		Enabled:       true,
		Meta: map[string]interface{}{
			"namespace":       "kb:web",
			"allow_localhost": true,
		},
	}); err != nil {
		t.Fatalf("SaveSourceRegistryEntry failed: %v", err)
	}

	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"run-source", "web:run", "--json"}, webGatherCLIDeps{
		SourceRunner: store,
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"staged":1`) || strings.Contains(out.String(), `"promoted_news":1`) {
		t.Fatalf("expected pending-only run result, got %s", out.String())
	}
	items, err := store.RecentStagingItems(ctx, l1sqlite.L1StagingStatusPending, 10)
	if err != nil {
		t.Fatalf("RecentStagingItems failed: %v", err)
	}
	if len(items) != 1 || items[0].SourceID != "web:run" || items[0].Meta["fetcher"] != "web_gather" {
		t.Fatalf("unexpected staging items: %+v", items)
	}
}

func TestRunWebGatherCommandRunSourceRejectsNonWebGatherSource(t *testing.T) {
	store := &webGatherSourceRunnerStub{entries: []l1sqlite.L1SourceRegistryEntry{{
		SourceID: "rss:not-web",
		Kind:     l1sqlite.L1SourceKindRSS,
		Enabled:  true,
	}}}
	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"run-source", "rss:not-web"}, webGatherCLIDeps{
		SourceRunner: store,
	}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "not a web_gather source") {
		t.Fatalf("expected non-web_gather rejection, code=%d stderr=%s", code, errOut.String())
	}
}

func TestRunWebGatherCommandWebwrightFetchDryRunUsesConfiguredRunner(t *testing.T) {
	var gotCommand string
	var gotArgs []string
	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"webwright-fetch", "--task", "collect page", "--start-url", "https://example.com", "--task-id", "task-1", "--dry-run"}, webGatherCLIDeps{
		WebwrightFetch: config.WebwrightFetchConfig{
			RunnerPath:        "tools/webwright_fetch/run_webwright_fetch.py",
			ConfigPath:        "tools/webwright_fetch/config_local_worker.yaml",
			OutputDir:         "tmp/webwright_runs",
			UvxFrom:           "git+https://github.com/microsoft/Webwright.git",
			ResponsesEndpoint: "http://127.0.0.1:8082/v1/responses",
			Model:             "Coder1",
			APIKey:            "dummy",
		},
		CommandRunner: func(_ context.Context, command string, args []string, _ io.Writer, _ io.Writer) int {
			gotCommand = command
			gotArgs = append([]string(nil), args...)
			return 0
		},
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	if gotCommand != "python3" {
		t.Fatalf("unexpected command: %s", gotCommand)
	}
	joined := strings.Join(gotArgs, "\x00")
	for _, want := range []string{
		"tools/webwright_fetch/run_webwright_fetch.py",
		"--task", "collect page",
		"--start-url", "https://example.com",
		"--task-id", "task-1",
		"--dry-run",
		"--local-responses-endpoint", "http://127.0.0.1:8082/v1/responses",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected arg %q in %#v", want, gotArgs)
		}
	}
}

func TestRunWebGatherCommandWebwrightFetchRequiresEnabledForExecution(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"webwright-fetch", "--task", "collect page"}, webGatherCLIDeps{
		WebwrightFetch: config.WebwrightFetchConfig{Enabled: false},
		CommandRunner: func(context.Context, string, []string, io.Writer, io.Writer) int {
			t.Fatal("runner must not be called when webwright_fetch is disabled")
			return 0
		},
	}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "webwright_fetch.enabled=true") {
		t.Fatalf("expected enabled error, code=%d stderr=%s", code, errOut.String())
	}
}

func TestRunWebGatherCommandWebwrightFetchPreflightsResponsesEndpoint(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"webwright-fetch", "--task", "collect page"}, webGatherCLIDeps{
		WebwrightFetch: config.WebwrightFetchConfig{
			Enabled:           true,
			ResponsesEndpoint: "http://127.0.0.1:1/v1/responses",
		},
		CommandRunner: func(context.Context, string, []string, io.Writer, io.Writer) int {
			t.Fatal("runner must not be called when responses endpoint is unreachable")
			return 0
		},
	}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "preflight failed") || !strings.Contains(errOut.String(), "responses endpoint is not reachable") {
		t.Fatalf("expected preflight error, code=%d stderr=%s", code, errOut.String())
	}
}

func TestRunWebGatherCommandDoctorReportsSkippedWebwright(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"doctor", "--json"}, webGatherCLIDeps{
		StagingStore: store,
		WebwrightFetch: config.WebwrightFetchConfig{
			Enabled: false,
		},
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"ok":true`) || !strings.Contains(out.String(), `"status":"skipped"`) {
		t.Fatalf("expected skipped doctor JSON, got %s", out.String())
	}
}

func TestRunWebGatherCommandDoctorFailsUnreachableWebwrightEndpoint(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"doctor"}, webGatherCLIDeps{
		StagingStore: store,
		WebwrightFetch: config.WebwrightFetchConfig{
			Enabled:           true,
			RunnerPath:        "tools/webwright_fetch/run_webwright_fetch.py",
			Python:            "python3",
			ResponsesEndpoint: "http://127.0.0.1:1/v1/responses",
		},
	}, &out, &errOut)
	if code != 1 || !strings.Contains(out.String(), "webwright_responses_endpoint: fail") {
		t.Fatalf("expected doctor endpoint failure, code=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
}

func TestRunWebGatherCommandDoctorPassesReachableWebwrightEndpoint(t *testing.T) {
	dir := t.TempDir()
	runnerPath := filepath.Join(dir, "run_webwright_fetch.py")
	if err := os.WriteFile(runnerPath, []byte("#!/usr/bin/env python3\n"), 0o755); err != nil {
		t.Fatalf("WriteFile runner failed: %v", err)
	}
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(dir, "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()

	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"doctor"}, webGatherCLIDeps{
		StagingStore: store,
		WebwrightFetch: config.WebwrightFetchConfig{
			Enabled:           true,
			RunnerPath:        runnerPath,
			Python:            "python3",
			ResponsesEndpoint: "http://" + listener.Addr().String() + "/v1/responses",
		},
	}, &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), "webwright_responses_endpoint: ok") {
		t.Fatalf("expected doctor success, code=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
}

func TestRunWebGatherCommandImportWebwrightJSONLStagesPending(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "webwright.jsonl")
	line := `{"Kind":"external_fetch","Namespace":"kb:webwright","EventID":"webwright:task-1","SourceID":"webwright:task-1","SourceURL":"https://example.com/page","FetchedAt":"2026-05-05T12:00:00Z","RawText":"Browser collected public page text.","SummaryDraft":"Browser collected public page text.","Keywords":["webwright"],"LicenseNote":"webwright browser fetch; review source terms before promotion","ValidationStatus":"pending","Meta":{"webwright":true,"tool":"webwright_fetch","review_required":true,"auto_promote":false}}` + "\n"
	if err := os.WriteFile(inputPath, []byte(line), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(dir, "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"import-webwright-jsonl", inputPath, "--json"}, webGatherCLIDeps{
		StagingStore: store,
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"imported":1`) {
		t.Fatalf("expected imported count JSON, got %s", out.String())
	}
	items, err := store.RecentStagingItems(ctx, l1sqlite.L1StagingStatusPending, 10)
	if err != nil {
		t.Fatalf("RecentStagingItems failed: %v", err)
	}
	if len(items) != 1 || items[0].SourceID != "webwright:task-1" || items[0].Meta["tool"] != "webwright_fetch" {
		t.Fatalf("unexpected staging items: %+v", items)
	}
}

func TestRunWebGatherCommandImportWebwrightJSONLRejectsUnsafeItem(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "unsafe.jsonl")
	line := `{"Kind":"external_fetch","Namespace":"kb:webwright","EventID":"webwright:unsafe","SourceID":"webwright:unsafe","SourceURL":"https://example.com/page","RawText":"Authorization: Bearer abcdefghijklmnop","SummaryDraft":"unsafe","Keywords":["webwright"],"LicenseNote":"webwright browser fetch; review source terms before promotion","ValidationStatus":"pending","Meta":{"webwright":true,"tool":"webwright_fetch","review_required":true,"auto_promote":false}}` + "\n"
	if err := os.WriteFile(inputPath, []byte(line), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(dir, "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	var out, errOut bytes.Buffer
	code := runWebGatherCommand([]string{"import-webwright-jsonl", inputPath}, webGatherCLIDeps{
		StagingStore: store,
	}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "credential material") {
		t.Fatalf("expected credential rejection, code=%d stderr=%s", code, errOut.String())
	}
	items, err := store.RecentStagingItems(context.Background(), l1sqlite.L1StagingStatusPending, 10)
	if err != nil {
		t.Fatalf("RecentStagingItems failed: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("unsafe item must not be staged: %+v", items)
	}
}

type webGatherSourceRunnerStub struct {
	entries []l1sqlite.L1SourceRegistryEntry
}

func (s *webGatherSourceRunnerStub) ListSourceRegistryEntries(_ context.Context, _ bool) ([]l1sqlite.L1SourceRegistryEntry, error) {
	return s.entries, nil
}

func (s *webGatherSourceRunnerStub) DueSourceRegistryEntries(context.Context, time.Time) ([]l1sqlite.L1SourceRegistryEntry, error) {
	return nil, nil
}

func (s *webGatherSourceRunnerStub) SourceTrustScores(context.Context) (map[string]float64, error) {
	return nil, nil
}

func (s *webGatherSourceRunnerStub) StageSourceRegistryFetch(context.Context, string, l1sqlite.L1SourceFetchPayload) (*l1sqlite.L1StagingItem, error) {
	return nil, nil
}

func (s *webGatherSourceRunnerStub) ValidateStagingItem(context.Context, string, l1sqlite.L1StagingValidationPolicy) (*l1sqlite.L1StagingValidationResult, error) {
	return nil, nil
}

func (s *webGatherSourceRunnerStub) PromoteValidatedStagingItemToNews(context.Context, string, string) (*l1sqlite.L1NewsItem, error) {
	return nil, nil
}

func (s *webGatherSourceRunnerStub) PromoteValidatedStagingItemToKnowledge(context.Context, string, string) (*l1sqlite.L1KnowledgeItem, error) {
	return nil, nil
}

func (s *webGatherSourceRunnerStub) MarkSourceRegistryFetched(context.Context, string, time.Time, string, string) error {
	return nil
}

var _ interface {
	sourcefetcher.RegistryStore
	sourcefetcher.RegistrySourceLister
} = (*webGatherSourceRunnerStub)(nil)
