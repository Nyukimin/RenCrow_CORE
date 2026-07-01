package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSTTSharedHTTPClientUsesConnectionPooling(t *testing.T) {
	transport, ok := sttSharedHTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected shared STT client transport, got %T", sttSharedHTTPClient.Transport)
	}
	if transport.MaxIdleConnsPerHost != 4 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want 4", transport.MaxIdleConnsPerHost)
	}
	if transport.IdleConnTimeout != 90*time.Second {
		t.Fatalf("IdleConnTimeout = %s, want 90s", transport.IdleConnTimeout)
	}
	if transport.DisableKeepAlives {
		t.Fatal("shared STT client should keep connections alive")
	}
}

func TestSTTInferViaHTTPUsesRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
			_ = json.NewEncoder(w).Encode(map[string]string{"text": "too slow"})
		}
	}))
	defer server.Close()

	start := time.Now()
	_, err := sttInferViaHTTP(server.URL, []byte("RIFFfake"), 30*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("request timeout was not applied promptly: %s", time.Since(start))
	}
}
