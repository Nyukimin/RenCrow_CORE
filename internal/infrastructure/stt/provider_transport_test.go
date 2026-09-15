package stt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestGatewayProviderOwnsCanonicalRequestAndResponse(t *testing.T) {
	owner := &sttTransportReceiptOwner{}
	ctx, err := domaintransport.WithReceiptOwner(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	var header string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get("X-RenCrow-Request-ID")
		w.Header().Set("X-Provider-Request-ID", "stt-provider-1")
		_, _ = w.Write([]byte(`{"text":"ok","language":"ja"}`))
	}))
	defer server.Close()
	wav := make([]byte, 44)
	copy(wav[0:4], "RIFF")
	copy(wav[8:12], "WAVE")

	result, err := (GatewayProvider{URL: server.URL, Timeout: time.Second}).Transcribe(ctx, wav)
	if err != nil {
		t.Fatal(err)
	}
	if len(owner.requests) != 1 || len(owner.responses) != 1 || len(owner.failed) != 0 {
		t.Fatalf("receipt calls requests=%d responses=%d failed=%d", len(owner.requests), len(owner.responses), len(owner.failed))
	}
	if header != string(owner.requests[0].RequestID) ||
		result.RequestID != owner.requests[0].RequestID ||
		result.ResponseID != owner.responses[0].ResponseID ||
		owner.responses[0].ExternalRef != "stt-provider-1" {
		t.Fatalf("transport identity header=%q result=%#v requests=%#v responses=%#v", header, result, owner.requests, owner.responses)
	}
}

type sttTransportReceiptOwner struct {
	requests  []domaintransport.Request
	responses []domaintransport.Response
	failed    []modulecore.RequestID
}

func (o *sttTransportReceiptOwner) BeginRequest(_ context.Context, operation string) (domaintransport.Request, error) {
	request := domaintransport.Request{RequestID: modulecore.NewRequestID(), Operation: operation}
	o.requests = append(o.requests, request)
	return request, nil
}

func (o *sttTransportReceiptOwner) CompleteResponse(_ context.Context, requestID modulecore.RequestID, externalRef string) (domaintransport.Response, error) {
	response := domaintransport.Response{ResponseID: modulecore.NewResponseID(), RequestID: requestID, ExternalRef: externalRef}
	o.responses = append(o.responses, response)
	return response, nil
}

func (o *sttTransportReceiptOwner) FailWithoutResponse(_ context.Context, requestID modulecore.RequestID, _ string) error {
	o.failed = append(o.failed, requestID)
	return nil
}
