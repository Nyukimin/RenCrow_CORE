package worldprojection

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"
)

func testEnvelope(t *testing.T) (domainprojection.Target, []byte) {
	t.Helper()
	target := domainprojection.Target{
		WorldID:          "world-house",
		TimelineID:       "timeline-main",
		TimelineEpoch:    "epoch-1",
		ActorID:          "mio",
		ActorInstanceID:  "instance-mio",
		ActorTransportID: "mio",
	}
	workSet, err := domainprojection.NewWorkSet("world-house", "mio", "core-epoch-1", 3,
		time.Date(2026, 10, 20, 13, 0, 0, 0, time.UTC), []domainprojection.WorkItem{})
	if err != nil {
		t.Fatalf("NewWorkSet() error = %v", err)
	}
	env, err := domainprojection.NewEnvelope(target, domainprojection.KindWorkSet, workSet)
	if err != nil {
		t.Fatalf("NewEnvelope() error = %v", err)
	}
	raw, err := env.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return target, raw
}

func TestHTTPPublisherSendsExactProjection(t *testing.T) {
	target, envelope := testEnvelope(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/v1/worlds/world-house/timelines/timeline-main/core-projections/mio/full_actor_work_set" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.RawQuery != "reconcile=true" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(body) != string(envelope) {
			t.Errorf("body = %s, want %s", body, envelope)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":1,"projection":{"status":"accepted","reason":""}}`))
	}))
	defer server.Close()

	publisher, err := NewHTTPPublisher(server.URL, "secret", 5*time.Second)
	if err != nil {
		t.Fatalf("NewHTTPPublisher() error = %v", err)
	}
	result, err := publisher.Publish(context.Background(), target, domainprojection.KindWorkSet, envelope, true)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if result.HTTPStatus != http.StatusOK || result.Status != "accepted" || !result.Successful() || result.Retryable {
		t.Fatalf("result = %+v", result)
	}
}

func TestHTTPPublisherTreatsEpochRequiresReconcileAsTerminal(t *testing.T) {
	target, envelope := testEnvelope(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"schema_version":1,"projection":{"status":"epoch_requires_reconcile","reason":"restore"}}`))
	}))
	defer server.Close()

	publisher, err := NewHTTPPublisher(server.URL, "secret", 5*time.Second)
	if err != nil {
		t.Fatalf("NewHTTPPublisher() error = %v", err)
	}
	result, err := publisher.Publish(context.Background(), target, domainprojection.KindWorkSet, envelope, false)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if result.Successful() || result.Retryable || !result.RequiresReconcile || result.Status != "epoch_requires_reconcile" {
		t.Fatalf("result = %+v", result)
	}
}

func TestHTTPPublisherMarksServerErrorRetryable(t *testing.T) {
	target, envelope := testEnvelope(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	publisher, err := NewHTTPPublisher(server.URL, "secret", 5*time.Second)
	if err != nil {
		t.Fatalf("NewHTTPPublisher() error = %v", err)
	}
	result, err := publisher.Publish(context.Background(), target, domainprojection.KindWorkSet, envelope, false)
	if err == nil {
		t.Fatal("Publish() error = nil, want a transport/status error")
	}
	if !result.Retryable || result.Successful() {
		t.Fatalf("result = %+v", result)
	}
}

func TestHTTPPublisherDoesNotFollowRedirect(t *testing.T) {
	target, envelope := testEnvelope(t)
	var redirectedHits int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&redirectedHits, 1)
	}))
	defer redirectTarget.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	publisher, err := NewHTTPPublisher(server.URL, "secret", 5*time.Second)
	if err != nil {
		t.Fatalf("NewHTTPPublisher() error = %v", err)
	}
	result, err := publisher.Publish(context.Background(), target, domainprojection.KindWorkSet, envelope, false)
	if err == nil {
		t.Fatal("Publish() error = nil, want redirect rejection")
	}
	if result.Retryable || result.Successful() {
		t.Fatalf("result = %+v", result)
	}
	if got := atomic.LoadInt32(&redirectedHits); got != 0 {
		t.Fatalf("redirect target was called %d times", got)
	}
}

func TestNewHTTPPublisherRejectsInvalidConfiguration(t *testing.T) {
	for _, baseURL := range []string{"", "ftp://example.test", "https://user:pass@example.test", "https://example.test?x=1"} {
		if _, err := NewHTTPPublisher(baseURL, "secret", time.Second); err == nil {
			t.Fatalf("NewHTTPPublisher(%q) error = nil", baseURL)
		}
	}
	if _, err := NewHTTPPublisher("https://example.test", "", time.Second); err == nil {
		t.Fatal("NewHTTPPublisher() accepted an empty credential")
	}
}

func TestHTTPPublisherReadsAcceptedHead(t *testing.T) {
	target, _ := testEnvelope(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/v1/worlds/world-house/timelines/timeline-main/core-projections/mio/full_actor_work_set" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":1,"projection":{"actor_id":"mio","actor_instance_id":"instance-mio","projection_kind":"full_actor_work_set","source_epoch":"core-epoch-1","source_revision":3,"digest":"abc123"}}`))
	}))
	defer server.Close()
	publisher, err := NewHTTPPublisher(server.URL, "secret", 5*time.Second)
	if err != nil {
		t.Fatalf("NewHTTPPublisher() error = %v", err)
	}
	head, err := publisher.ReadHead(context.Background(), target, domainprojection.KindWorkSet)
	if err != nil {
		t.Fatalf("ReadHead() error = %v", err)
	}
	if head.ActorID != "mio" || head.ActorInstanceID != "instance-mio" ||
		head.SourceEpoch != "core-epoch-1" || head.SourceRevision != 3 || head.Digest != "abc123" {
		t.Fatalf("head = %+v", head)
	}
}

func TestHTTPPublisherDistinguishesMissingHeadAndTransientReadFailure(t *testing.T) {
	target, _ := testEnvelope(t)
	for name, tc := range map[string]struct {
		status    int
		retryable bool
		notFound  bool
	}{
		"missing":   {status: http.StatusNotFound, notFound: true},
		"temporary": {status: http.StatusServiceUnavailable, retryable: true},
		"forbidden": {status: http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			publisher, err := NewHTTPPublisher(server.URL, "secret", 5*time.Second)
			if err != nil {
				t.Fatalf("NewHTTPPublisher() error = %v", err)
			}
			_, err = publisher.ReadHead(context.Background(), target, domainprojection.KindWorkSet)
			if tc.notFound {
				if !errors.Is(err, ErrProjectionHeadNotFound) {
					t.Fatalf("ReadHead() error = %v, want ErrProjectionHeadNotFound", err)
				}
				return
			}
			var readErr *HeadReadError
			if !errors.As(err, &readErr) || readErr.Retryable != tc.retryable {
				t.Fatalf("ReadHead() error = %#v, retryable want %v", err, tc.retryable)
			}
		})
	}
}

func TestHTTPPublisherParsesReconcileAck(t *testing.T) {
	target, envelope := testEnvelope(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":1,"projection":{"status":"duplicate","reason":"","accepted_at":"2026-10-20T12:00:00Z"}}`))
	}))
	defer server.Close()
	publisher, err := NewHTTPPublisher(server.URL+"/", "secret", 5*time.Second)
	if err != nil {
		t.Fatalf("NewHTTPPublisher() error = %v", err)
	}
	result, err := publisher.Publish(context.Background(), target, domainprojection.KindWorkSet, envelope, false)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if !result.Successful() || result.Status != "duplicate" || result.AcceptedAt.IsZero() {
		t.Fatalf("result = %+v", result)
	}
	var decoded map[string]any
	if err := json.Unmarshal(envelope, &decoded); err != nil {
		t.Fatalf("envelope is not JSON: %v", err)
	}
	if !strings.Contains(server.URL, "http") {
		t.Fatal("unreachable")
	}
}
