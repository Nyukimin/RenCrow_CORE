package browsertrace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
)

func TestPairRequestsResponses(t *testing.T) {
	requests := mustRequests(t, `{"request_id":"r1","method":"GET","url":"https://example.com/api/items"}`)
	responses := mustResponses(t, `{"request_id":"r1","status":200,"body":"{\"items\":[{\"id\":1}]}"}`)
	exchanges := PairRequestsResponses(requests, responses)
	if len(exchanges) != 1 {
		t.Fatalf("exchanges=%#v", exchanges)
	}
	if exchanges[0].Response.Status != 200 {
		t.Fatalf("response not paired: %#v", exchanges[0])
	}
}

func TestTemplatizeURL(t *testing.T) {
	templated, pathTemplate, params := TemplatizeURL("https://example.com/users/123/items?page=2&q=ai")
	if pathTemplate != "/users/{id}/items" {
		t.Fatalf("pathTemplate=%q", pathTemplate)
	}
	if !strings.Contains(templated, "page=") || !strings.Contains(templated, "q=") {
		t.Fatalf("templated=%q", templated)
	}
	if len(params) != 2 || params[0].Name != "page" || params[0].Type != "integer" {
		t.Fatalf("params=%#v", params)
	}
}

func TestInferJSONSchemaEmptyArrayIsUnknown(t *testing.T) {
	schema := InferJSONSchema([]string{`{"items":[]}`})
	if !strings.Contains(schema, `"type":"array"`) || !strings.Contains(schema, `"type":"unknown"`) {
		t.Fatalf("schema=%s", schema)
	}
}

func TestDiscoverRejectsWriteMethodAndDoesNotStoreCredentialValues(t *testing.T) {
	tmp := t.TempDir()
	requestsPath := filepath.Join(tmp, "requests.jsonl")
	responsesPath := filepath.Join(tmp, "responses.jsonl")
	requests := strings.Join([]string{
		`{"request_id":"r1","method":"GET","url":"https://example.com/api/items?page=1","headers":{"authorization":"Bearer secret-token"}}`,
		`{"request_id":"r2","method":"DELETE","url":"https://example.com/api/items/1"}`,
	}, "\n")
	responses := strings.Join([]string{
		`{"request_id":"r1","status":200,"body":"{\"items\":[{\"id\":1,\"title\":\"A\"}]}"}`,
		`{"request_id":"r2","status":204,"body":""}`,
	}, "\n")
	if err := os.WriteFile(requestsPath, []byte(requests), 0644); err != nil {
		t.Fatalf("write requests: %v", err)
	}
	if err := os.WriteFile(responsesPath, []byte(responses), 0644); err != nil {
		t.Fatalf("write responses: %v", err)
	}
	d := NewDiscoverer()
	d.now = func() time.Time { return time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC) }
	result, err := d.Discover(DiscoverRequest{
		TraceRunID:    "trace_1",
		SiteID:        "example",
		TracePath:     tmp,
		RequestsPath:  requestsPath,
		ResponsesPath: responsesPath,
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("candidates=%#v", result.Candidates)
	}
	if result.Candidates[0].Method != "GET" || !result.Candidates[0].AuthRequired {
		t.Fatalf("candidate=%#v", result.Candidates[0])
	}
	encoded := mustMarshalString(t, result)
	if strings.Contains(encoded, "secret-token") {
		t.Fatalf("credential value leaked: %s", encoded)
	}
	if len(result.Schemas) != 1 || !strings.Contains(result.Schemas[0].SchemaJSON, `"items"`) {
		t.Fatalf("schemas=%#v", result.Schemas)
	}
}

func TestDiscoverRejectsTracePathsOutsideAcceptedRoots(t *testing.T) {
	tmp := t.TempDir()
	requestsPath := filepath.Join(tmp, "requests.jsonl")
	responsesPath := filepath.Join(tmp, "responses.jsonl")
	if err := os.WriteFile(requestsPath, []byte(`{"request_id":"r1","method":"GET","url":"https://example.com/api/items"}`), 0644); err != nil {
		t.Fatalf("write requests: %v", err)
	}
	if err := os.WriteFile(responsesPath, []byte(`{"request_id":"r1","status":200,"body":"{}"}`), 0644); err != nil {
		t.Fatalf("write responses: %v", err)
	}
	d := NewDiscovererWithAcceptedPaths([]string{"traces/"})
	_, err := d.Discover(DiscoverRequest{
		TraceRunID:    "trace_1",
		SiteID:        "example",
		TracePath:     tmp,
		RequestsPath:  requestsPath,
		ResponsesPath: responsesPath,
	})
	if err == nil {
		t.Fatal("expected trace outside accepted path to fail")
	}
}

func TestDiscoverAllowsTracePathsInsideAcceptedRoots(t *testing.T) {
	tmp := t.TempDir()
	traceDir := filepath.Join(tmp, "traces", "run-1")
	if err := os.MkdirAll(traceDir, 0755); err != nil {
		t.Fatalf("mkdir trace dir: %v", err)
	}
	requestsPath := filepath.Join(traceDir, "requests.jsonl")
	responsesPath := filepath.Join(traceDir, "responses.jsonl")
	if err := os.WriteFile(requestsPath, []byte(`{"request_id":"r1","method":"GET","url":"https://example.com/api/items"}`), 0644); err != nil {
		t.Fatalf("write requests: %v", err)
	}
	if err := os.WriteFile(responsesPath, []byte(`{"request_id":"r1","status":200,"body":"{}"}`), 0644); err != nil {
		t.Fatalf("write responses: %v", err)
	}
	d := NewDiscovererWithAcceptedPaths([]string{"traces/"})
	result, err := d.Discover(DiscoverRequest{
		TraceRunID:    "trace_1",
		SiteID:        "example",
		TracePath:     traceDir,
		RequestsPath:  requestsPath,
		ResponsesPath: responsesPath,
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("candidates=%#v", result.Candidates)
	}
}

func mustRequests(t *testing.T, content string) []domaintrace.NetworkRequest {
	t.Helper()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "requests.jsonl")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write requests: %v", err)
	}
	requests, err := readRequests(path)
	if err != nil {
		t.Fatalf("readRequests() error = %v", err)
	}
	return requests
}

func mustResponses(t *testing.T, content string) []domaintrace.NetworkResponse {
	t.Helper()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "responses.jsonl")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write responses: %v", err)
	}
	responses, err := readResponses(path)
	if err != nil {
		t.Fatalf("readResponses() error = %v", err)
	}
	return responses
}

func mustMarshalString(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}
