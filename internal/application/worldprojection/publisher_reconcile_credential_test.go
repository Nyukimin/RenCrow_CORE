package worldprojection

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"
)

// D-06/D-04: an authorized source epoch switch is a separate capability. A
// publisher built for normal projection must never carry a reconcile request
// with the projection credential, and a reconcile credential may not be the
// projection credential under another name.
func TestHTTPPublisherFailsClosedWhenReconcileHasNoCredential(t *testing.T) {
	target, envelope := testEnvelope(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("reconcile request reached WORLD without a reconcile credential: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			return
		}
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":1,"projection":{"actor_id":"mio","actor_instance_id":"instance-mio","projection_kind":"full_actor_work_set","source_epoch":"core-epoch-1","source_revision":3,"digest":"abc123"}}`))
	}))
	defer server.Close()

	publisher, err := NewHTTPPublisher(server.URL, "projection-secret", 5*time.Second)
	if err != nil {
		t.Fatalf("NewHTTPPublisher() error = %v", err)
	}
	_, err = publisher.Publish(context.Background(), target, domainprojection.KindWorkSet, envelope, true)
	if !errors.Is(err, ErrReconcileCredentialNotConfigured) {
		t.Fatalf("Publish() error = %v, want %v", err, ErrReconcileCredentialNotConfigured)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}

	// The projection credential still authorizes ordinary reads, so the failure
	// above is specific to the missing reconcile capability.
	head, err := publisher.ReadHead(context.Background(), target, domainprojection.KindWorkSet)
	if err != nil {
		t.Fatalf("ReadHead() error = %v", err)
	}
	if requests != 1 || head.SourceRevision != 3 {
		t.Fatalf("requests = %d head = %+v", requests, head)
	}
}

func TestNewHTTPPublisherRejectsReusedReconcileCredential(t *testing.T) {
	if _, err := NewHTTPPublisherWithReconcile("https://example.test", "shared-secret", "shared-secret", time.Second); err == nil {
		t.Fatal("constructor accepted a reconcile credential identical to the projection credential")
	}
	if _, err := NewHTTPPublisherWithReconcile("https://example.test", "projection-secret", "", time.Second); err == nil {
		t.Fatal("constructor accepted an empty reconcile credential")
	}
	if _, err := NewHTTPPublisherWithReconcile("https://example.test", "projection-secret", " reconcile-secret ", time.Second); err == nil {
		t.Fatal("constructor accepted an untrimmed reconcile credential")
	}
}

func TestHTTPPublisherUsesDistinctReconcileCredential(t *testing.T) {
	target, envelope := testEnvelope(t)
	type seen struct {
		authorization string
		query         string
		method        string
	}
	var got []seen
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, seen{
			authorization: r.Header.Get("Authorization"),
			query:         r.URL.RawQuery,
			method:        r.Method,
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":1,"projection":{"status":"accepted","reason":"","source_epoch":"core-epoch-2","source_revision":1,"digest":"sha256:reconciled"}}`))
	}))
	defer server.Close()

	publisher, err := NewHTTPPublisherWithReconcile(server.URL, "projection-secret", "reconcile-secret", 5*time.Second)
	if err != nil {
		t.Fatalf("NewHTTPPublisherWithReconcile() error = %v", err)
	}
	if _, err := publisher.Publish(context.Background(), target, domainprojection.KindWorkSet, envelope, true); err != nil {
		t.Fatalf("Publish(reconcile) error = %v", err)
	}
	if _, err := publisher.Publish(context.Background(), target, domainprojection.KindWorkSet, envelope, false); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("requests = %d, want 2", len(got))
	}
	if got[0].query != "reconcile=true" || got[0].authorization != "Bearer reconcile-secret" {
		t.Fatalf("reconcile request = %+v", got[0])
	}
	if got[1].query != "" || got[1].authorization != "Bearer projection-secret" {
		t.Fatalf("projection request = %+v", got[1])
	}
}

// A head read is a normal projection-scoped read and must never escalate to
// the reconcile credential.
func TestHTTPPublisherReadHeadNeverUsesReconcileCredential(t *testing.T) {
	target, _ := testEnvelope(t)
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":1,"projection":{"actor_id":"mio","actor_instance_id":"instance-mio","projection_kind":"full_actor_work_set","source_epoch":"core-epoch-1","source_revision":3,"digest":"abc123"}}`))
	}))
	defer server.Close()

	publisher, err := NewHTTPPublisherWithReconcile(server.URL, "projection-secret", "reconcile-secret", 5*time.Second)
	if err != nil {
		t.Fatalf("NewHTTPPublisherWithReconcile() error = %v", err)
	}
	head, err := publisher.ReadHead(context.Background(), target, domainprojection.KindWorkSet)
	if err != nil {
		t.Fatalf("ReadHead() error = %v", err)
	}
	if head.SourceEpoch != "core-epoch-1" || head.SourceRevision != 3 || head.Digest != "abc123" {
		t.Fatalf("head = %+v", head)
	}
	if authorization != "Bearer projection-secret" {
		t.Fatalf("Authorization = %q", authorization)
	}
}
