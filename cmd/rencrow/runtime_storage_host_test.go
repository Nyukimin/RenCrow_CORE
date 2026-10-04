package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
)

const runtimeStorageHostTestToken = "runtime-storage-host-test-secret"

func TestRuntimeStorageHostLocalDefaultDoesNotReadTokenOrDial(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  *config.Config
	}{
		{name: "default", cfg: &config.Config{}},
		{name: "explicit local", cfg: &config.Config{Storage: config.StorageConfig{Host: config.StorageHostConfig{Mode: config.StorageHostModeLocal}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			readTokenCalls := 0
			dialCalls := 0
			newClientCalls := 0
			deps := runtimeStorageHostStartupDeps{
				readToken: func(string) (string, error) {
					readTokenCalls++
					return "", errors.New("unexpected token read")
				},
				newClient: func(storagehost.ClientConfig) (*storagehost.Client, error) {
					newClientCalls++
					return nil, errors.New("unexpected client construction")
				},
				dialContext: func(context.Context, string, string) (net.Conn, error) {
					dialCalls++
					return nil, errors.New("unexpected dial")
				},
			}
			result, err := newRuntimeStorageHostClientWithDeps(context.Background(), tt.cfg, nil, deps)
			if err != nil {
				t.Fatalf("newRuntimeStorageHostClientWithDeps() error = %v", err)
			}
			if result.Remote || result.Client != nil {
				t.Fatalf("result remote/client-present = %t/%t, want false/false", result.Remote, result.Client != nil)
			}
			if readTokenCalls != 0 || newClientCalls != 0 || dialCalls != 0 {
				t.Fatalf("token/client/dial calls = %d/%d/%d, want all zero", readTokenCalls, newClientCalls, dialCalls)
			}
		})
	}
}

func TestRuntimeStorageHostRemoteBuildsAuthenticatedClientAndChecksContract(t *testing.T) {
	host := newRuntimeStorageHostContractHandler(t, []storagehost.OperationSpec{{Group: "probe", Op: "read", Mutating: false}})
	var authValues []string
	var routes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authValues = append(authValues, r.Header.Get("Authorization"))
		routes = append(routes, r.Method+" "+r.URL.Path)
		host.ServeHTTP(w, r)
	}))
	defer server.Close()
	endpoint := server.URL

	cfg := runtimeStorageHostTestConfig(endpoint)
	cfg.Storage.Host.TimeoutSec = 29
	var gotClientConfig storagehost.ClientConfig
	readTokenCalls := 0
	dialAddresses := make([]string, 0, 2)
	deps := runtimeStorageHostStartupDeps{
		readToken: func(path string) (string, error) {
			readTokenCalls++
			if path != cfg.Storage.Host.TokenFile {
				return "", errors.New("wrong token file")
			}
			return runtimeStorageHostTestToken, nil
		},
		newClient: func(clientConfig storagehost.ClientConfig) (*storagehost.Client, error) {
			gotClientConfig = clientConfig
			return storagehost.NewClient(clientConfig)
		},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialAddresses = append(dialAddresses, address)
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}

	result, err := newRuntimeStorageHostClientWithDeps(context.Background(), cfg, []storagehost.OperationSpec{{Group: "probe", Op: "read"}}, deps)
	if err != nil {
		t.Fatalf("newRuntimeStorageHostClientWithDeps() error = %v", err)
	}
	if !result.Remote || result.Client == nil {
		t.Fatalf("result remote/client-present = %t/%t, want true/true", result.Remote, result.Client != nil)
	}
	if result.Client.Generation() == 0 {
		t.Fatal("client generation = 0, want authenticated handshake generation")
	}
	if readTokenCalls != 1 {
		t.Fatalf("token reads = %d, want 1", readTokenCalls)
	}
	if gotClientConfig.Endpoint != endpoint || gotClientConfig.Timeout != 29*time.Second {
		t.Fatalf("client config endpoint/timeout = %q/%s, want %q/29s", gotClientConfig.Endpoint, gotClientConfig.Timeout, endpoint)
	}
	if gotClientConfig.Token != runtimeStorageHostTestToken {
		t.Fatal("client config did not receive the exact configured token")
	}
	if gotClientConfig.HTTPClient == nil {
		t.Fatal("client config HTTPClient is nil, want injectable dial transport")
	}
	if len(authValues) != 2 || authValues[0] != "Bearer "+runtimeStorageHostTestToken || authValues[1] != authValues[0] {
		t.Fatal("handshake and contract did not use the configured bearer")
	}
	if !reflect.DeepEqual(routes, []string{"POST /v1/rpc", "POST /v1/rpc"}) {
		t.Fatalf("routes = %v, want only POST /v1/rpc handshake/contract calls", routes)
	}
	if len(dialAddresses) == 0 || dialAddresses[0] != strings.TrimPrefix(endpoint, "http://") {
		t.Fatalf("dial addresses = %v, want configured endpoint host", dialAddresses)
	}
}

func TestRuntimeStorageHostDoesNotFollowConfiguredEndpointRedirects(t *testing.T) {
	destinationRequests := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationRequests++
		result, err := json.Marshal(storagehost.ContractInfo{
			ContractVersion: storagehost.ContractVersion,
			Generation:      1,
			Operations:      []storagehost.OperationSpec{{Group: "probe", Op: "read", Mutating: false}},
		})
		if err != nil {
			t.Errorf("marshal redirected contract: %v", err)
			return
		}
		writeRuntimeStorageHostResponse(t, w, storagehost.Response{Result: result})
	}))
	defer destination.Close()

	sourceRequests := 0
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceRequests++
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	waitCalls := 0
	deps := runtimeStorageHostTestDeps()
	deps.wait = func(context.Context, time.Duration) error {
		waitCalls++
		return nil
	}

	_, err := newRuntimeStorageHostClientWithDeps(context.Background(), runtimeStorageHostTestConfig(source.URL), []storagehost.OperationSpec{{Group: "probe", Op: "read"}}, deps)
	assertRuntimeStorageHostErrorCode(t, err, storagehost.ErrorCodeUnreachable)
	if sourceRequests != 1 || destinationRequests != 0 || waitCalls != 0 {
		t.Fatalf("source/destination/wait requests = %d/%d/%d, want 1/0/0", sourceRequests, destinationRequests, waitCalls)
	}
}

func TestRuntimeStorageHostRetriesTransientUnavailableUntilReady(t *testing.T) {
	host := newRuntimeStorageHostContractHandler(t, []storagehost.OperationSpec{{Group: "probe", Op: "read", Mutating: false}})
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		host.ServeHTTP(w, r)
	}))
	defer server.Close()

	clock := &runtimeStorageHostFakeClock{now: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)}
	dialCalls := 0
	waitCalls := 0
	deps := runtimeStorageHostTestDeps()
	deps.now = clock.read
	deps.backoff = func(int) time.Duration { return 10 * time.Millisecond }
	deps.wait = func(ctx context.Context, delay time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		waitCalls++
		clock.advance(delay)
		return nil
	}
	deps.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		dialCalls++
		if dialCalls == 1 {
			return nil, errors.New("temporary refused connection")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}

	result, err := newRuntimeStorageHostClientWithDeps(context.Background(), runtimeStorageHostTestConfig(server.URL), []storagehost.OperationSpec{{Group: "probe", Op: "read"}}, deps)
	if err != nil {
		t.Fatalf("newRuntimeStorageHostClientWithDeps() error = %v", err)
	}
	generation := int64(0)
	if result.Client != nil {
		generation = result.Client.Generation()
	}
	if !result.Remote || result.Client == nil || generation == 0 {
		t.Fatalf("result remote/client-present/generation = %t/%t/%d, want true/true/nonzero", result.Remote, result.Client != nil, generation)
	}
	if dialCalls != 2 || requests != 2 || waitCalls != 1 {
		t.Fatalf("dial/request/wait calls = %d/%d/%d, want 2/2/1 (unavailable then handshake and contract)", dialCalls, requests, waitCalls)
	}
}

func TestRuntimeStorageHostStartupDeadlineStopsRetries(t *testing.T) {
	clock := &runtimeStorageHostFakeClock{now: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)}
	cfg := runtimeStorageHostTestConfig("http://127.0.0.1:18820")
	cfg.Storage.Host.StartupWaitSec = 1
	dialCalls := 0
	var waits []time.Duration
	deps := runtimeStorageHostTestDeps()
	deps.now = clock.read
	deps.backoff = func(int) time.Duration { return 600 * time.Millisecond }
	deps.wait = func(ctx context.Context, delay time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		waits = append(waits, delay)
		clock.advance(delay)
		return nil
	}
	deps.dialContext = func(context.Context, string, string) (net.Conn, error) {
		dialCalls++
		return nil, errors.New("connection refused")
	}

	_, err := newRuntimeStorageHostClientWithDeps(context.Background(), cfg, []storagehost.OperationSpec{{Group: "probe", Op: "read"}}, deps)
	assertRuntimeStorageHostErrorCode(t, err, storagehost.ErrorCodeUnreachable)
	if dialCalls != 2 || !reflect.DeepEqual(waits, []time.Duration{600 * time.Millisecond, 400 * time.Millisecond}) {
		t.Fatalf("dial calls/waits = %d/%v, want 2 and bounded 600ms+400ms waits", dialCalls, waits)
	}
}

func TestRuntimeStorageHostCallerCancellationStopsRetry(t *testing.T) {
	cfg := runtimeStorageHostTestConfig("http://127.0.0.1:18820")
	ctx, cancel := context.WithCancel(context.Background())
	dialCalls := 0
	deps := runtimeStorageHostTestDeps()
	deps.dialContext = func(context.Context, string, string) (net.Conn, error) {
		dialCalls++
		return nil, errors.New("connection refused")
	}
	deps.wait = func(context.Context, time.Duration) error {
		cancel()
		return ctx.Err()
	}

	_, err := newRuntimeStorageHostClientWithDeps(ctx, cfg, []storagehost.OperationSpec{{Group: "probe", Op: "read"}}, deps)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if dialCalls != 1 {
		t.Fatalf("dial calls = %d, want one before cancellation", dialCalls)
	}
}

func TestRuntimeStorageHostUnauthorizedFailsImmediatelyWithoutTokenLeak(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeRuntimeStorageHostResponse(t, w, storagehost.Response{Error: storagehost.NewError(storagehost.ErrorCodeUnauthorized, runtimeStorageHostTestToken)})
	}))
	defer server.Close()
	waitCalls := 0
	deps := runtimeStorageHostTestDeps()
	deps.wait = func(context.Context, time.Duration) error {
		waitCalls++
		return nil
	}

	_, err := newRuntimeStorageHostClientWithDeps(context.Background(), runtimeStorageHostTestConfig(server.URL), []storagehost.OperationSpec{{Group: "probe", Op: "read"}}, deps)
	if err != nil && strings.Contains(err.Error(), runtimeStorageHostTestToken) {
		t.Fatal("startup error leaked bearer token")
	}
	assertRuntimeStorageHostErrorCode(t, err, storagehost.ErrorCodeUnauthorized)
	if requests != 1 || waitCalls != 0 {
		t.Fatalf("request/wait calls = %d/%d, want 1/0", requests, waitCalls)
	}
}

func TestRuntimeStorageHostContractMismatchFailsImmediately(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		result, err := json.Marshal(storagehost.ContractInfo{ContractVersion: "wrong-contract", Generation: 1})
		if err != nil {
			t.Errorf("marshal contract: %v", err)
			return
		}
		writeRuntimeStorageHostResponse(t, w, storagehost.Response{Result: result})
	}))
	defer server.Close()
	waitCalls := 0
	deps := runtimeStorageHostTestDeps()
	deps.wait = func(context.Context, time.Duration) error {
		waitCalls++
		return nil
	}

	_, err := newRuntimeStorageHostClientWithDeps(context.Background(), runtimeStorageHostTestConfig(server.URL), []storagehost.OperationSpec{{Group: "probe", Op: "read"}}, deps)
	assertRuntimeStorageHostErrorCode(t, err, storagehost.ErrorCodeContractMismatch)
	if requests != 2 || waitCalls != 0 {
		t.Fatalf("request/wait calls = %d/%d, want two contract reads and no retry", requests, waitCalls)
	}
}

func TestRuntimeStorageHostMalformedContractFailsImmediately(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		result, err := json.Marshal(storagehost.ContractInfo{
			ContractVersion: storagehost.ContractVersion,
			Generation:      1,
			Operations:      []storagehost.OperationSpec{{Group: "Invalid", Op: "read", Mutating: false}},
		})
		if err != nil {
			t.Errorf("marshal contract: %v", err)
			return
		}
		writeRuntimeStorageHostResponse(t, w, storagehost.Response{Result: result})
	}))
	defer server.Close()
	waitCalls := 0
	deps := runtimeStorageHostTestDeps()
	deps.wait = func(context.Context, time.Duration) error {
		waitCalls++
		return nil
	}

	_, err := newRuntimeStorageHostClientWithDeps(context.Background(), runtimeStorageHostTestConfig(server.URL), []storagehost.OperationSpec{{Group: "probe", Op: "read"}}, deps)
	assertRuntimeStorageHostErrorCode(t, err, storagehost.ErrorCodeSchemaRejected)
	if requests != 2 || waitCalls != 0 {
		t.Fatalf("request/wait calls = %d/%d, want two contract reads and no retry", requests, waitCalls)
	}
}

func TestRuntimeStorageHostMissingAndMutatingRequiredOperationsFailImmediately(t *testing.T) {
	for _, tt := range []struct {
		name     string
		provided storagehost.OperationSpec
		required storagehost.OperationSpec
		wantCode string
	}{
		{
			name:     "missing",
			provided: storagehost.OperationSpec{Group: "probe", Op: "other", Mutating: false},
			required: storagehost.OperationSpec{Group: "probe", Op: "read", Mutating: false},
			wantCode: storagehost.ErrorCodeOperationUnsupported,
		},
		{
			name:     "mutating mismatch",
			provided: storagehost.OperationSpec{Group: "probe", Op: "write", Mutating: false},
			required: storagehost.OperationSpec{Group: "probe", Op: "write", Mutating: true},
			wantCode: storagehost.ErrorCodeContractMismatch,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			host := newRuntimeStorageHostContractHandler(t, []storagehost.OperationSpec{tt.provided})
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				host.ServeHTTP(w, r)
			}))
			defer server.Close()
			waitCalls := 0
			deps := runtimeStorageHostTestDeps()
			deps.wait = func(context.Context, time.Duration) error {
				waitCalls++
				return nil
			}

			_, err := newRuntimeStorageHostClientWithDeps(context.Background(), runtimeStorageHostTestConfig(server.URL), []storagehost.OperationSpec{tt.required}, deps)
			assertRuntimeStorageHostErrorCode(t, err, tt.wantCode)
			if requests != 2 || waitCalls != 0 {
				t.Fatalf("request/wait calls = %d/%d, want two contract reads and no retry", requests, waitCalls)
			}
		})
	}
}

func TestRuntimeStorageHostRequiredOperationSetIsClosedAndDeterministic(t *testing.T) {
	valid := []storagehost.OperationSpec{
		{Group: "event", Op: "append", Mutating: true},
		{Group: "host", Op: "contract", Mutating: false},
	}
	normalized, err := validateRuntimeStorageHostRequiredOps(valid)
	if err != nil {
		t.Fatalf("validateRuntimeStorageHostRequiredOps(valid) error = %v", err)
	}
	if !reflect.DeepEqual(normalized, valid) {
		t.Fatalf("normalized operations = %+v, want deterministic group/op order", normalized)
	}
	if _, err := validateRuntimeStorageHostRequiredOps([]storagehost.OperationSpec{valid[0], valid[0]}); err == nil {
		t.Fatal("duplicate requested operation was accepted")
	}
	if _, err := validateRuntimeStorageHostRequiredOps([]storagehost.OperationSpec{{Group: "Event", Op: "append"}}); err == nil {
		t.Fatal("invalid requested operation name was accepted")
	}
	if _, err := validateRuntimeStorageHostRequiredOps(nil); err == nil {
		t.Fatal("empty requested operation set was accepted")
	}

	info := storagehost.ContractInfo{ContractVersion: storagehost.ContractVersion, Generation: 1, Operations: []storagehost.OperationSpec{{Group: "event", Op: "append", Mutating: false}}}
	requiredInOrder := []storagehost.OperationSpec{
		{Group: "host", Op: "missing", Mutating: false},
		{Group: "event", Op: "append", Mutating: true},
	}
	orderedFirst, err := validateRuntimeStorageHostRequiredOps(requiredInOrder)
	if err != nil {
		t.Fatalf("validateRuntimeStorageHostRequiredOps(first order) error = %v", err)
	}
	orderedSecond, err := validateRuntimeStorageHostRequiredOps([]storagehost.OperationSpec{requiredInOrder[1], requiredInOrder[0]})
	if err != nil {
		t.Fatalf("validateRuntimeStorageHostRequiredOps(second order) error = %v", err)
	}
	errFirst := verifyRuntimeStorageHostRequiredOps(orderedFirst, info)
	errSecond := verifyRuntimeStorageHostRequiredOps(orderedSecond, info)
	if errFirst == nil || errSecond == nil || errFirst.Error() != errSecond.Error() {
		t.Fatalf("verification errors = %v/%v, want same deterministic closed-set failure", errFirst, errSecond)
	}
}

func TestRuntimeStorageHostTokenReadErrorIsSanitized(t *testing.T) {
	cfg := runtimeStorageHostTestConfig("http://127.0.0.1:18820")
	deps := runtimeStorageHostTestDeps()
	deps.readToken = func(string) (string, error) {
		return "", fmt.Errorf("invalid token %s", runtimeStorageHostTestToken)
	}

	_, err := newRuntimeStorageHostClientWithDeps(context.Background(), cfg, []storagehost.OperationSpec{{Group: "probe", Op: "read"}}, deps)
	if err == nil || strings.Contains(err.Error(), runtimeStorageHostTestToken) {
		t.Fatal("token-read error was nil or included bearer token")
	}
}

func newRuntimeStorageHostContractHandler(t *testing.T, operations []storagehost.OperationSpec) http.Handler {
	t.Helper()
	journalDir := filepath.Join(t.TempDir(), "journal")
	handler, err := storagehost.NewHandler(storagehost.HandlerConfig{Token: runtimeStorageHostTestToken, JournalDir: journalDir})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	for _, operation := range operations {
		if err := handler.Register(operation.Group, operation.Op, operation.Mutating, func(context.Context, json.RawMessage) (any, error) {
			return nil, nil
		}); err != nil {
			handler.Close()
			t.Fatalf("Register() error = %v", err)
		}
	}
	t.Cleanup(func() {
		if err := handler.Close(); err != nil {
			t.Errorf("Handler.Close() error = %v", err)
		}
	})
	return handler
}

func runtimeStorageHostTestConfig(endpoint string) *config.Config {
	return &config.Config{Storage: config.StorageConfig{Host: config.StorageHostConfig{
		Mode:           config.StorageHostModeRemote,
		Transport:      config.StorageHostTransportTunnel,
		Endpoint:       endpoint,
		TokenFile:      "/private/storage-host.token",
		TimeoutSec:     30,
		StartupWaitSec: 300,
	}}}
}

func runtimeStorageHostTestDeps() runtimeStorageHostStartupDeps {
	return runtimeStorageHostStartupDeps{
		readToken: func(path string) (string, error) {
			if path != "/private/storage-host.token" {
				return "", errors.New("unexpected token path")
			}
			return runtimeStorageHostTestToken, nil
		},
		newClient: func(cfg storagehost.ClientConfig) (*storagehost.Client, error) {
			return storagehost.NewClient(cfg)
		},
		dialContext: (&net.Dialer{}).DialContext,
		now:         time.Now,
		wait: func(context.Context, time.Duration) error {
			return errors.New("unexpected retry wait")
		},
		backoff: func(int) time.Duration { return 10 * time.Millisecond },
	}
}

type runtimeStorageHostFakeClock struct {
	now time.Time
}

func (c *runtimeStorageHostFakeClock) read() time.Time { return c.now }

func (c *runtimeStorageHostFakeClock) advance(duration time.Duration) { c.now = c.now.Add(duration) }

func writeRuntimeStorageHostResponse(t *testing.T, w http.ResponseWriter, response storagehost.Response) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		t.Errorf("encode storage-host response: %v", err)
	}
}

func assertRuntimeStorageHostErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	var hostErr *storagehost.Error
	if !errors.As(err, &hostErr) || hostErr.Code != want {
		t.Fatalf("error = %v, want storage-host error code %q", err, want)
	}
}
