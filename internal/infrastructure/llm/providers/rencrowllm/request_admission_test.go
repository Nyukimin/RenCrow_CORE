package rencrowllm

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func TestGatewayProviderRejectedRequestAdmissionDoesNotDispatchHTTP(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"unexpected"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "Mio", server.URL, time.Second)
	wantErr := errors.New("prepared turn was revoked")
	var admissions atomic.Int32
	request := llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "hello"}},
		RequestAdmission: llm.NewGenerateRequestAdmission(func() error {
			admissions.Add(1)
			return wantErr
		}),
	}
	_, err := provider.Generate(t.Context(), request)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Generate error = %v, want admission rejection %v", err, wantErr)
	}
	if requests.Load() != 0 {
		t.Fatalf("Gateway received %d requests after admission rejection", requests.Load())
	}
	if admissions.Load() != 1 {
		t.Fatalf("admission callback calls = %d, want one", admissions.Load())
	}
}

func TestGatewayProviderRunsRequestAdmissionBeforeHTTPWithoutHoldingIt(t *testing.T) {
	var guardDepth atomic.Int32
	var admissionCompleted atomic.Bool
	var serverObservedReleased atomic.Bool
	var admissionSerialized atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		admissionSerialized.Store(strings.Contains(string(body), "RequestAdmission") || strings.Contains(string(body), "request_admission"))
		serverObservedReleased.Store(guardDepth.Load() == 0 && admissionCompleted.Load())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "Mio", server.URL, time.Second)
	request := llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "hello"}},
		RequestAdmission: llm.NewGenerateRequestAdmission(func() error {
			guardDepth.Add(1)
			defer guardDepth.Add(-1)
			admissionCompleted.Store(true)
			return nil
		}),
	}
	response, err := provider.Generate(t.Context(), request)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if response.Content != "ok" || !serverObservedReleased.Load() {
		t.Fatalf("response=%q serverObservedReleased=%t; want admitted request with no guard held over HTTP", response.Content, serverObservedReleased.Load())
	}
	if admissionSerialized.Load() {
		t.Fatal("one-shot request admission capability was serialized into the Gateway HTTP body")
	}
}
