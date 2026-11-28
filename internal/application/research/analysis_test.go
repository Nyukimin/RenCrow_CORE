package research

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

func TestHTTPAnalysisPortVerifiesBatchParagraphEvidence(t *testing.T) {
	var mu sync.Mutex
	requests := make([]map[string]any, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != analysisEndpointPath || r.Method != http.MethodPost {
			t.Errorf("request=%s %s", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		requests = append(requests, request)
		call := len(requests)
		mu.Unlock()
		if call == 1 {
			writeAnalysisChatResponse(t, w, `{"claims":[{"id":"claim-1","text":"Alpha is a fact.","paragraph_ids":["p000001"]},{"id":"claim-2","text":"第二段の内容です。","paragraph_ids":["p000002"]}],"unresolved":[]}`)
			return
		}
		writeAnalysisChatResponse(t, w, `{"checks":[{"claim_id":"claim-1","verdict":"supported","paragraph_ids":["p000001"]},{"claim_id":"claim-2","verdict":"supported","paragraph_ids":["p000002"]}]}`)
	}))
	defer server.Close()
	port := newTestAnalysisPort(t, server.URL, &http.Client{Timeout: time.Second})
	request := testAnalysisRequest()
	got, err := port.Analyze(context.Background(), request)
	if err != nil {
		t.Fatalf("Analyze() error=%v", err)
	}
	if got.Calls != 2 || got.Coverage != "complete" || got.SchemaVersion != "rencrow.research.analysis.v1" || got.CollectionHash != request.CollectionHash || got.EngineRevision != analysisEngineRevision+"@research-r1" {
		t.Fatalf("analysis metadata=%+v", got)
	}
	if got.Report != "" {
		t.Fatalf("Report=%q, want Engine-rendered empty value", got.Report)
	}
	if len(got.Claims) != 2 || !got.Claims[0].Verified || len(got.Claims[0].Evidence) != 1 || !got.Claims[1].Verified || len(got.Claims[1].Evidence) != 1 {
		t.Fatalf("verified claims=%+v", got.Claims)
	}
	text := request.Collection.Documents[0].Text
	first := got.Claims[0].Evidence[0]
	if first.DocumentID != request.Collection.Documents[0].ID || first.ArtifactHash != request.Collection.Documents[0].ExtractedHash || first.Start != 0 || first.End != len([]rune("Alpha is a fact.")) || first.Locator != "characters:0-16" {
		t.Fatalf("first evidence ref=%+v text=%q", first, text)
	}
	second := got.Claims[1].Evidence[0]
	secondStart := len([]rune("Alpha is a fact.\n\n"))
	secondText := "第二段の内容です。"
	if second.DocumentID != request.Collection.Documents[0].ID || second.Start != secondStart || second.End != secondStart+len([]rune(secondText)) || second.ArtifactHash != request.Collection.Documents[0].ExtractedHash {
		t.Fatalf("Unicode evidence ref=%+v text=%q", second, text)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("gateway calls=%d", len(requests))
	}
	for i, gatewayRequest := range requests {
		if gatewayRequest["alias_ref"] != "research_analysis_local" {
			t.Errorf("call %d alias_ref=%v", i+1, gatewayRequest["alias_ref"])
		}
		wantOperation := "document_understanding"
		if i == 1 {
			wantOperation = "claim_evidence_check"
		}
		if gatewayRequest["operation"] != wantOperation {
			t.Errorf("call %d operation=%v, want %s", i+1, gatewayRequest["operation"], wantOperation)
		}
		for _, forbidden := range []string{"agent_id", "execution_role", "execution_alias", "model", "url"} {
			if _, exists := gatewayRequest[forbidden]; exists {
				t.Errorf("call %d allowed arbitrary %s selection", i+1, forbidden)
			}
		}
		metadata, _ := gatewayRequest["rencrow"].(map[string]any)
		if metadata["request_id"] != "req_123" || metadata["trace_id"] != "trc_123" || metadata["task_id"] != "tsk_123" || metadata["run_id"] != "run_123" {
			t.Errorf("call %d lost envelope correlation: %#v", i+1, metadata)
		}
		for _, forbidden := range []string{"agent_id", "execution_role", "execution_alias", "model", "url", "caller", "purpose"} {
			if _, exists := metadata[forbidden]; exists {
				t.Errorf("call %d impersonated/reselected %s", i+1, forbidden)
			}
		}
		input, _ := gatewayRequest["input"].(map[string]any)
		paragraphs, ok := input["paragraphs"].([]any)
		if !ok || len(paragraphs) != 2 {
			t.Errorf("call %d omitted complete paragraph batch: %#v", i+1, input)
		}
		if input["question"] != request.Objective || input["language"] != request.Language {
			t.Errorf("call %d lost question/language context: %#v", i+1, input)
		}
		if i == 1 {
			if claims, ok := input["claims"].([]any); !ok || len(claims) != 2 {
				t.Errorf("claim check did not batch all claims: %#v", input["claims"])
			}
		}
	}
}

func TestHTTPAnalysisPortReturnsIncompleteForInsufficientEvidence(t *testing.T) {
	calls := 0
	server := analysisServer(t, func(call int) string {
		if call == 1 {
			return `{"claims":[{"id":"c1","text":"Claim","paragraph_ids":["p000001"]}],"unresolved":["Important detail unresolved"]}`
		}
		return `{"checks":[{"claim_id":"c1","verdict":"insufficient","paragraph_ids":[]}]}`
	}, &calls)
	defer server.Close()
	got, err := newTestAnalysisPort(t, server.URL, nil).Analyze(context.Background(), testAnalysisRequest())
	if err != nil {
		t.Fatalf("Analyze() error=%v", err)
	}
	if got.Coverage != "incomplete" || len(got.Unresolved) < 2 || len(got.Claims) != 0 || got.Calls != 2 {
		t.Fatalf("incomplete analysis=%+v", got)
	}
}

func TestHTTPAnalysisPortRejectsOutputAboveStructuredBudget(t *testing.T) {
	makeClaims := func(count int) []map[string]any {
		claims := make([]map[string]any, 0, count)
		for i := 0; i < count; i++ {
			claims = append(claims, map[string]any{"id": fmt.Sprintf("c%d", i), "text": "A supported fact.", "paragraph_ids": []string{"p000001"}})
		}
		return claims
	}
	marshal := func(t *testing.T, value any) string {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal model fixture: %v", err)
		}
		return string(encoded)
	}
	tests := []struct {
		name   string
		output any
		want   string
	}{
		{name: "too many claims", output: map[string]any{"claims": makeClaims(9), "unresolved": []string{}}, want: "claims exceeds"},
		{name: "claim exceeds Unicode length", output: map[string]any{"claims": []map[string]any{{"id": "c1", "text": strings.Repeat("界", 601), "paragraph_ids": []string{"p000001"}}}, "unresolved": []string{}}, want: "claim text exceeds"},
		{name: "too many claim paragraph refs", output: map[string]any{"claims": []map[string]any{{"id": "c1", "text": "A supported fact.", "paragraph_ids": []string{"p000001", "p000001", "p000001", "p000001"}}}, "unresolved": []string{}}, want: "paragraph IDs exceeds"},
		{name: "too many unresolved items", output: map[string]any{"claims": []map[string]any{}, "unresolved": []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}}, want: "unresolved exceeds"},
		{name: "unresolved item exceeds Unicode length", output: map[string]any{"claims": []map[string]any{}, "unresolved": []string{strings.Repeat("界", 601)}}, want: "unresolved item exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := analysisServer(t, func(call int) string {
				if call == 1 {
					return marshal(t, test.output)
				}
				return `{"checks":[]}`
			}, &calls)
			defer server.Close()
			got, err := newTestAnalysisPort(t, server.URL, nil).Analyze(context.Background(), testAnalysisRequest())
			if err == nil || !strings.Contains(err.Error(), test.want) || got.Calls != 1 || calls != 1 {
				t.Fatalf("calls=%d err=%v, want rejection %q after one call", calls, err, test.want)
			}
		})
	}
	t.Run("too many evidence checks", func(t *testing.T) {
		calls := 0
		server := analysisServer(t, func(call int) string {
			if call == 1 {
				return marshal(t, map[string]any{"claims": makeClaims(8), "unresolved": []string{}})
			}
			checks := make([]map[string]any, 0, 9)
			for i := 0; i < 9; i++ {
				checks = append(checks, map[string]any{"claim_id": "c0", "verdict": "supported", "paragraph_ids": []string{"p000001"}})
			}
			return marshal(t, map[string]any{"checks": checks})
		}, &calls)
		defer server.Close()
		got, err := newTestAnalysisPort(t, server.URL, nil).Analyze(context.Background(), testAnalysisRequest())
		if err == nil || !strings.Contains(err.Error(), "checks exceeds") || got.Calls != 2 || calls != 2 {
			t.Fatalf("calls=%d err=%v, want bounded rejection after two calls", calls, err)
		}
	})
}

func TestHTTPAnalysisPortRejectsForeignParagraphAndProfileRevisionMismatch(t *testing.T) {
	t.Run("foreign paragraph", func(t *testing.T) {
		calls := 0
		server := analysisServer(t, func(call int) string {
			if call == 1 {
				return `{"claims":[{"id":"c1","text":"Claim","paragraph_ids":["p000001"]}],"unresolved":[]}`
			}
			return `{"checks":[{"claim_id":"c1","verdict":"supported","paragraph_ids":["p999999"]}]}`
		}, &calls)
		defer server.Close()
		got, err := newTestAnalysisPort(t, server.URL, nil).Analyze(context.Background(), testAnalysisRequest())
		if err == nil || got.Calls != 2 || len(got.Claims) != 0 {
			t.Fatalf("foreign evidence accepted: analysis=%+v err=%v", got, err)
		}
	})
	t.Run("revision mismatch", func(t *testing.T) {
		calls := 0
		server := analysisServerWithRevision(t, func(call int) string {
			if call == 1 {
				return `{"claims":[{"id":"c1","text":"Claim","paragraph_ids":["p000001"]}],"unresolved":[]}`
			}
			return `{"checks":[{"claim_id":"c1","verdict":"supported","paragraph_ids":["p000001"]}]}`
		}, &calls, func(call int) string {
			if call == 1 {
				return "research-r1"
			}
			return "research-r2"
		})
		defer server.Close()
		got, err := newTestAnalysisPort(t, server.URL, nil).Analyze(context.Background(), testAnalysisRequest())
		if err == nil || got.Calls != 2 {
			t.Fatalf("revision mismatch accepted: analysis=%+v err=%v", got, err)
		}
	})
}

func TestHTTPAnalysisPortRejectsSchemaCorruptionAndIncompleteFinish(t *testing.T) {
	for name, finish := range map[string]string{"unknown content field": "stop", "truncated output": "length"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				content := `{"claims":[],"unresolved":[]}`
				if name == "unknown content field" {
					content = `{"claims":[],"unresolved":[],"extra":"ignored?"}`
				}
				writeAnalysisChatResponseWithFinish(t, w, content, "research-r1", finish)
			}))
			defer server.Close()
			got, err := newTestAnalysisPort(t, server.URL, nil).Analyze(context.Background(), testAnalysisRequest())
			if err == nil || got.Calls != 1 || calls != 1 {
				t.Fatalf("analysis=%+v calls=%d err=%v", got, calls, err)
			}
		})
	}
}

func TestHTTPAnalysisPortBoundariesAndFailureCalls(t *testing.T) {
	t.Run("max calls below two", func(t *testing.T) {
		calls := 0
		server := analysisServer(t, func(int) string { return `{}` }, &calls)
		defer server.Close()
		request := testAnalysisRequest()
		request.MaxCalls = 1
		got, err := newTestAnalysisPort(t, server.URL, nil).Analyze(context.Background(), request)
		if err == nil || got.Calls != 0 || calls != 0 {
			t.Fatalf("analysis=%+v calls=%d err=%v", got, calls, err)
		}
	})
	t.Run("complete input exceeds limit", func(t *testing.T) {
		calls := 0
		server := analysisServer(t, func(int) string { return `{}` }, &calls)
		defer server.Close()
		request := testAnalysisRequest()
		request.Collection.Documents[0].Text = strings.Repeat("資料", 80_000)
		hash := sha256.Sum256([]byte(request.Collection.Documents[0].Text))
		request.Collection.Documents[0].ExtractedHash = hex.EncodeToString(hash[:])
		got, err := newTestAnalysisPort(t, server.URL, nil).Analyze(context.Background(), request)
		if err == nil || got.Calls != 0 || calls != 0 {
			t.Fatalf("analysis=%+v calls=%d err=%v", got, calls, err)
		}
	})
	t.Run("cancelled context counts attempted call", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			time.Sleep(100 * time.Millisecond)
			writeAnalysisChatResponse(t, w, `{"claims":[],"unresolved":[]}`)
		}))
		defer server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancel()
		got, err := newTestAnalysisPort(t, server.URL, nil).Analyze(ctx, testAnalysisRequest())
		if !errors.Is(err, context.DeadlineExceeded) || got.Calls != 1 || calls != 1 {
			t.Fatalf("analysis=%+v calls=%d err=%v", got, calls, err)
		}
	})
	t.Run("invalid base URL", func(t *testing.T) {
		for _, baseURL := range []string{"https://example.org", "https://8.8.8.8", "http://192.168.1.2/prefix"} {
			if _, err := NewHTTPAnalysisPort(baseURL, "research_analysis_local", &http.Client{}); err == nil {
				t.Errorf("unsafe/non-canonical Gateway URL accepted: %q", baseURL)
			}
		}
	})
	t.Run("private managed Gateway URL", func(t *testing.T) {
		for _, baseURL := range []string{"http://127.0.0.1:8090", "https://10.2.3.4:8090/", "http://172.20.1.2:8090", "http://192.168.1.20:8090"} {
			port, err := NewHTTPAnalysisPort(baseURL, "research_analysis_local", &http.Client{})
			if err != nil {
				t.Errorf("private Gateway URL %q rejected: %v", baseURL, err)
				continue
			}
			if !strings.HasSuffix(port.endpoint, analysisEndpointPath) {
				t.Errorf("endpoint=%q", port.endpoint)
			}
		}
	})
}

func testAnalysisRequest() engine.AnalysisRequest {
	text := "Alpha is a fact.\n\n第二段の内容です。"
	hash := sha256.Sum256([]byte(text))
	return engine.AnalysisRequest{
		Envelope:  c.Envelope{TaskID: "tsk_123", RunID: "run_123", TraceID: "trc_123", RequestID: "req_123"},
		Objective: "Research the source facts", Language: "ja", MaxCalls: 2,
		CollectionHash: "collection-hash",
		Collection:     c.Collection{Documents: []c.Document{{ID: "doc-1", Text: text, ExtractedHash: hex.EncodeToString(hash[:])}}},
	}
}

func newTestAnalysisPort(t *testing.T, baseURL string, client *http.Client) *HTTPAnalysisPort {
	t.Helper()
	if client == nil {
		client = &http.Client{Timeout: time.Second}
	}
	port, err := NewHTTPAnalysisPort(baseURL, "research_analysis_local", client)
	if err != nil {
		t.Fatalf("NewHTTPAnalysisPort() error=%v", err)
	}
	return port
}

func analysisServer(t *testing.T, output func(int) string, calls *int) *httptest.Server {
	t.Helper()
	return analysisServerWithRevision(t, output, calls, func(int) string { return "research-r1" })
}

func analysisServerWithRevision(t *testing.T, output func(int) string, calls *int, revision func(int) string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		writeAnalysisChatResponseWithRevision(t, w, output(*calls), revision(*calls))
	}))
}

func writeAnalysisChatResponse(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	writeAnalysisChatResponseWithRevision(t, w, content, "research-r1")
}

func writeAnalysisChatResponseWithRevision(t *testing.T, w http.ResponseWriter, content, revision string) {
	t.Helper()
	writeAnalysisChatResponseWithFinish(t, w, content, revision, "stop")
}

func writeAnalysisChatResponseWithFinish(t *testing.T, w http.ResponseWriter, content, revision, finish string) {
	t.Helper()
	w.Header().Set(analysisProfileRevisionHeader, revision)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "chat-1", "object": "chat.completion", "model": "research_analysis_local", "choices": []any{map[string]any{
		"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": finish,
	}}})
}
