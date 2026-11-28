package researchembedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/research"
)

const testServiceID = "research_local"

func TestHTTPPortCapabilityAndEmbeddingUseGatewayContract(t *testing.T) {
	capability := gatewayCapabilityJSON("bge_m3", strings.Repeat("a", 64), 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer configured-gateway-token" {
			t.Errorf("authorization header did not use the injected Gateway client")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/embedding-services/"+testServiceID+"/capability":
			_, _ = w.Write([]byte(capability))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/embedding-services/"+testServiceID+"/embeddings":
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			var request map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode embedding request: %v", err)
				return
			}
			if len(request) != 2 || string(request["input"]) != `["first","second"]` || string(request["encoding_format"]) != `"float"` {
				t.Errorf("embedding request fields = %#v", request)
			}
			for _, forbidden := range []string{"model", "actor", "url"} {
				if _, ok := request[forbidden]; ok {
					t.Errorf("embedding request included forbidden field %q", forbidden)
				}
			}
			_, _ = w.Write([]byte(embeddingResultJSON(testServiceID, capability, `[{"index":1,"embedding":[0.5,0.25]},{"index":0,"embedding":[0.25,0.5],"object":"embedding"}]`)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	port, err := NewHTTPPort(server.URL, testServiceID, &http.Client{Transport: authenticatedRoundTripper{base: directTransport(t)}})
	if err != nil {
		t.Fatal(err)
	}
	wantCapability := research.SemanticEmbeddingCapability{ModelID: "bge_m3", Revision: "sha256:" + strings.Repeat("a", 64), Dimension: 2}
	gotCapability, err := port.Capability(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotCapability != wantCapability {
		t.Fatalf("Capability() = %#v, want %#v", gotCapability, wantCapability)
	}
	got, err := port.Embed(context.Background(), wantCapability, []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Capability != wantCapability || len(got.Vectors) != 2 || got.Vectors[0].Index != 1 || got.Vectors[0].Values[0] != 0.5 || got.Vectors[1].Index != 0 || got.Vectors[1].Values[0] != 0.25 {
		t.Fatalf("Embed() did not preserve response indices: %#v", got)
	}
}

func TestHTTPPortCapabilityRejectsUnavailableAndInvalidResponses(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		oversized   bool
	}{
		{name: "unavailable with reason", status: http.StatusOK, contentType: "application/json", body: `{"format_version":"rencrow.embedding-capability/v1","available":false,"reason":"MODEL_NOT_READY"}`},
		{name: "unavailable with identity field", status: http.StatusOK, contentType: "application/json", body: `{"format_version":"rencrow.embedding-capability/v1","available":false,"reason":"MODEL_NOT_READY","model_id":""}`},
		{name: "missing availability", status: http.StatusOK, contentType: "application/json", body: `{"format_version":"rencrow.embedding-capability/v1"}`},
		{name: "incomplete available identity", status: http.StatusOK, contentType: "application/json", body: `{"format_version":"rencrow.embedding-capability/v1","available":true,"model_id":"bge_m3"}`},
		{name: "available with reason", status: http.StatusOK, contentType: "application/json", body: strings.TrimSuffix(gatewayCapabilityJSON("bge_m3", strings.Repeat("a", 64), 2), "}") + `,"reason":""}`},
		{name: "wrong version", status: http.StatusOK, contentType: "application/json", body: `{"format_version":"rencrow.embedding-capability/v2","available":true}`},
		{name: "unknown field", status: http.StatusOK, contentType: "application/json", body: strings.TrimSuffix(gatewayCapabilityJSON("bge_m3", strings.Repeat("a", 64), 2), "}") + `,"endpoint":"private"}`},
		{name: "trailing JSON", status: http.StatusOK, contentType: "application/json", body: gatewayCapabilityJSON("bge_m3", strings.Repeat("a", 64), 2) + `{}`},
		{name: "invalid content type", status: http.StatusOK, contentType: "text/plain", body: gatewayCapabilityJSON("bge_m3", strings.Repeat("a", 64), 2)},
		{name: "HTTP error body is hidden", status: http.StatusUnauthorized, contentType: "application/json", body: "configured-gateway-token PRIVATE RAW TEXT"},
		{name: "oversized body", status: http.StatusOK, contentType: "application/json", oversized: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				w.WriteHeader(test.status)
				if test.oversized {
					_, _ = w.Write([]byte(strings.Repeat(" ", embeddingCapabilityMaxBody+1)))
					return
				}
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			port, err := NewHTTPPort(server.URL, testServiceID, directClient(t))
			if err != nil {
				t.Fatal(err)
			}
			_, err = port.Capability(context.Background())
			if err == nil {
				t.Fatal("Capability() succeeded for an invalid response")
			}
			for _, secret := range []string{"configured-gateway-token", "PRIVATE RAW TEXT", server.URL} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error exposed %q", secret)
				}
			}
		})
	}
}

func TestHTTPPortEmbedRejectsInvalidGatewayResults(t *testing.T) {
	capability := gatewayCapabilityJSON("bge_m3", strings.Repeat("a", 64), 2)
	wrongRevision := gatewayCapabilityJSON("bge_m3", strings.Repeat("b", 64), 2)
	wrongDimension := gatewayCapabilityJSON("bge_m3", strings.Repeat("a", 64), 3)
	vector := `"embedding":[0.25,0.5]`
	tests := []struct {
		name     string
		response string
	}{
		{name: "wrong service", response: embeddingResultJSON("another_service", capability, `[{"index":0,`+vector+`},{"index":1,`+vector+`}]`)},
		{name: "wrong revision", response: embeddingResultJSON(testServiceID, wrongRevision, `[{"index":0,`+vector+`},{"index":1,`+vector+`}]`)},
		{name: "wrong capability dimension", response: embeddingResultJSON(testServiceID, wrongDimension, `[{"index":0,"embedding":[0.25,0.5,0.75]},{"index":1,"embedding":[0.5,0.25,0.75]}]`)},
		{name: "wrong vector dimension", response: embeddingResultJSON(testServiceID, capability, `[{"index":0,"embedding":[0.25]},{"index":1,`+vector+`}]`)},
		{name: "wrong count", response: embeddingResultJSON(testServiceID, capability, `[{"index":0,`+vector+`}]`)},
		{name: "duplicate index", response: embeddingResultJSON(testServiceID, capability, `[{"index":0,`+vector+`},{"index":0,`+vector+`}]`)},
		{name: "missing index", response: embeddingResultJSON(testServiceID, capability, `[{`+vector+`},{"index":1,`+vector+`}]`)},
		{name: "out of range index", response: embeddingResultJSON(testServiceID, capability, `[{"index":0,`+vector+`},{"index":2,`+vector+`}]`)},
		{name: "fractional index", response: embeddingResultJSON(testServiceID, capability, `[{"index":0.5,`+vector+`},{"index":1,`+vector+`}]`)},
		{name: "quoted numeric index", response: embeddingResultJSON(testServiceID, capability, `[{"index":"0",`+vector+`},{"index":1,`+vector+`}]`)},
		{name: "null index", response: embeddingResultJSON(testServiceID, capability, `[{"index":null,`+vector+`},{"index":1,`+vector+`}]`)},
		{name: "nonnumeric vector value", response: embeddingResultJSON(testServiceID, capability, `[{"index":0,"embedding":[0.25,"bad"]},{"index":1,`+vector+`}]`)},
		{name: "quoted numeric vector value", response: embeddingResultJSON(testServiceID, capability, `[{"index":0,"embedding":["0.25",0.5]},{"index":1,`+vector+`}]`)},
		{name: "null vector value", response: embeddingResultJSON(testServiceID, capability, `[{"index":0,"embedding":[0.25,null]},{"index":1,`+vector+`}]`)},
		{name: "non-finite float32 value", response: embeddingResultJSON(testServiceID, capability, `[{"index":0,"embedding":[1e999,0.5]},{"index":1,`+vector+`}]`)},
		{name: "all zero vector", response: embeddingResultJSON(testServiceID, capability, `[{"index":0,"embedding":[0,0]},{"index":1,`+vector+`}]`)},
		{name: "wrong version", response: strings.Replace(embeddingResultJSON(testServiceID, capability, `[{"index":0,`+vector+`},{"index":1,`+vector+`}]`), embeddingResultFormat, "rencrow.embedding-result/v2", 1)},
		{name: "unknown wrapper field", response: strings.TrimSuffix(embeddingResultJSON(testServiceID, capability, `[{"index":0,`+vector+`},{"index":1,`+vector+`}]`), "}") + `,"model":"private"}`},
		{name: "unexpected item object", response: embeddingResultJSON(testServiceID, capability, `[{"index":0,"embedding":[0.25,0.5],"object":"other"},{"index":1,`+vector+`}]`)},
		{name: "null item object", response: embeddingResultJSON(testServiceID, capability, `[{"index":0,"embedding":[0.25,0.5],"object":null},{"index":1,`+vector+`}]`)},
		{name: "unknown item field", response: embeddingResultJSON(testServiceID, capability, `[{"index":0,`+vector+`,"model":"private"},{"index":1,`+vector+`}]`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			port := testResultPort(t, test.response)
			_, err := port.Embed(context.Background(), expectedCapability(), []string{"first", "second"})
			if err == nil {
				t.Fatal("Embed() succeeded for an invalid Gateway result")
			}
		})
	}
}

func TestHTTPPortEmbedBoundsRequestsAndSanitizesErrors(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("Authorization"); got != "Bearer configured-gateway-token" {
			t.Errorf("authorization header did not use the injected Gateway client")
		}
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, "configured-gateway-token https://private.invalid PRIVATE RAW TEXT", http.StatusUnauthorized)
	}))
	defer server.Close()
	port, err := NewHTTPPort(server.URL, testServiceID, &http.Client{Transport: authenticatedRoundTripper{base: directTransport(t)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, inputs := range [][]string{nil, {" \t "}, {strings.Repeat("x", embeddingRequestMaxBody+1)}} {
		if _, err := port.Embed(context.Background(), expectedCapability(), inputs); err == nil {
			t.Fatal("Embed() accepted an invalid or oversized request")
		}
	}
	if calls != 0 {
		t.Fatalf("invalid requests reached Gateway %d times", calls)
	}
	const rawInput = "USER PRIVATE RAW TEXT"
	_, err = port.Embed(context.Background(), expectedCapability(), []string{rawInput})
	if err == nil {
		t.Fatal("Embed() accepted an HTTP error")
	}
	for _, secret := range []string{"configured-gateway-token", "private.invalid", "PRIVATE RAW TEXT", rawInput, server.URL} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error exposed %q", secret)
		}
	}
}

func TestHTTPPortBoundsEmbeddingResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Repeat(" ", embeddingResponseMaxBody+1)))
	}))
	defer server.Close()
	port, err := NewHTTPPort(server.URL, testServiceID, directClient(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := port.Embed(context.Background(), expectedCapability(), []string{"input"}); err == nil {
		t.Fatal("Embed() accepted an oversized response")
	}
}

func TestHTTPPortUsesDirectNoProxyTransportAndBlocksRedirects(t *testing.T) {
	var targetCalls, proxyCalls int
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls++ }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	transport := &http.Transport{Proxy: func(*http.Request) (*url.URL, error) {
		proxyCalls++
		return nil, nil
	}}
	port, err := NewHTTPPort(source.URL, testServiceID, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := port.Capability(context.Background()); err == nil {
		t.Fatal("Capability() followed or accepted a redirect")
	}
	if targetCalls != 0 {
		t.Fatalf("redirect target received %d requests", targetCalls)
	}
	if proxyCalls != 0 {
		t.Fatalf("configured proxy received %d requests", proxyCalls)
	}
	if transport.Proxy == nil {
		t.Fatal("constructor mutated the injected Gateway transport")
	}
}

func TestHTTPPortRequestDeadlinesAreBounded(t *testing.T) {
	transport := &deadlineTrackingRoundTripper{base: directTransport(t)}
	capability := gatewayCapabilityJSON("bge_m3", strings.Repeat("a", 64), 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(capability))
			return
		}
		_, _ = w.Write([]byte(embeddingResultJSON(testServiceID, capability, `[{"index":0,"embedding":[0.25,0.5]}]`)))
	}))
	defer server.Close()
	port, err := NewHTTPPort(server.URL, testServiceID, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := port.Capability(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := port.Embed(context.Background(), expectedCapability(), []string{"first"}); err != nil {
		t.Fatal(err)
	}
	if transport.capabilityRemaining <= 0 || transport.capabilityRemaining > embeddingCapabilityTimeout || transport.embeddingRemaining <= 0 || transport.embeddingRemaining > embeddingHTTPTimeout {
		t.Fatalf("request deadlines remaining: capability=%s embedding=%s", transport.capabilityRemaining, transport.embeddingRemaining)
	}
}

func TestNewHTTPPortRejectsUntrustedConfiguration(t *testing.T) {
	client := directClient(t)
	tests := []struct {
		name      string
		baseURL   string
		serviceID string
	}{
		{name: "remote host", baseURL: "https://example.com", serviceID: testServiceID},
		{name: "userinfo", baseURL: "http://user@127.0.0.1:8000", serviceID: testServiceID},
		{name: "query", baseURL: "http://127.0.0.1:8000?token=secret", serviceID: testServiceID},
		{name: "fragment", baseURL: "http://127.0.0.1:8000#path", serviceID: testServiceID},
		{name: "path prefix", baseURL: "http://127.0.0.1:8000/gateway", serviceID: testServiceID},
		{name: "path traversal service", baseURL: "http://127.0.0.1:8000", serviceID: "../other"},
		{name: "uppercase service", baseURL: "http://127.0.0.1:8000", serviceID: "Research_local"},
		{name: "empty service", baseURL: "http://127.0.0.1:8000", serviceID: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewHTTPPort(test.baseURL, test.serviceID, client); err == nil {
				t.Fatal("NewHTTPPort() accepted invalid configuration")
			}
		})
	}
}

func testResultPort(t *testing.T, response string) *HTTPPort {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	port, err := NewHTTPPort(server.URL, testServiceID, directClient(t))
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func gatewayCapabilityJSON(modelID, assetSHA256 string, dimension int) string {
	return `{"format_version":"rencrow.embedding-capability/v1","available":true,"model_id":"` + modelID + `","model_revision":"sha256:` + assetSHA256 + `","asset_sha256":"` + assetSHA256 + `","dimension":` + jsonNumber(dimension) + `,"identity_basis":"configured_asset_sha256+loaded_model_probe"}`
}

func embeddingResultJSON(serviceID, capability, data string) string {
	return `{"format_version":"rencrow.embedding-result/v1","service_id":"` + serviceID + `","capability":` + capability + `,"data":` + data + `}`
}

func jsonNumber(value int) string {
	return strconv.Itoa(value)
}

func expectedCapability() research.SemanticEmbeddingCapability {
	return research.SemanticEmbeddingCapability{ModelID: "bge_m3", Revision: "sha256:" + strings.Repeat("a", 64), Dimension: 2}
}

func directClient(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Transport: directTransport(t)}
}

func directTransport(t *testing.T) *http.Transport {
	t.Helper()
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatal("default HTTP transport is not an *http.Transport")
	}
	localTransport := transport.Clone()
	localTransport.Proxy = nil
	return localTransport
}

type authenticatedRoundTripper struct{ base http.RoundTripper }

func (t authenticatedRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	request := r.Clone(r.Context())
	request.Header = r.Header.Clone()
	request.Header.Set("Authorization", "Bearer configured-gateway-token")
	return t.base.RoundTrip(request)
}

type deadlineTrackingRoundTripper struct {
	base                http.RoundTripper
	capabilityRemaining time.Duration
	embeddingRemaining  time.Duration
}

func (t *deadlineTrackingRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	deadline, ok := r.Context().Deadline()
	if !ok {
		return nil, context.DeadlineExceeded
	}
	if r.Method == http.MethodGet {
		t.capabilityRemaining = time.Until(deadline)
	} else {
		t.embeddingRemaining = time.Until(deadline)
	}
	return t.base.RoundTrip(r)
}
