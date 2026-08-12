package personrelatedcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPCollectorPostsFixedCollectionRequestAndDownloadsArtifact(t *testing.T) {
	artifact := []byte("immutable artifact\n")
	sum := sha256.Sum256(artifact)
	expectedHash := hex.EncodeToString(sum[:])
	var gotRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/person-related-catalog/collections":
			if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
				t.Fatalf("decode provider request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"artifact_url":    "/artifacts/person.jsonl",
				"artifact_sha256": expectedHash,
				"artifact_bytes":  int64(len(artifact)),
			})
		case r.Method == http.MethodGet && r.URL.Path == "/artifacts/person.jsonl":
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = w.Write(artifact)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := NewHTTPCollector(server.URL, time.Second).Collect(context.Background(), CollectionRequest{
		MovieCatalogPersonID: "p1",
		PersonName:           "役所広司",
		PersonURL:            "https://example.test/person/p1",
		Category:             CategoryDrama,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Artifact) != string(artifact) || result.ArtifactSHA256 != expectedHash || result.ArtifactBytes != int64(len(artifact)) {
		t.Fatalf("unexpected collection result: %+v", result)
	}
	if !reflectExactCollectionRequest(gotRequest, map[string]any{
		"movie_catalog_person_id": "p1",
		"name":                    "役所広司",
		"url":                     "https://example.test/person/p1",
		"category":                CategoryDrama,
	}) {
		t.Fatalf("unexpected provider request: %#v", gotRequest)
	}
}

func TestHTTPCollectorRejectsCrossOriginArtifactAndIntegrityFailure(t *testing.T) {
	artifact := []byte("artifact\n")
	sum := sha256.Sum256(artifact)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/person-related-catalog/collections" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"artifact_url":    "http://other-origin.invalid/artifact.jsonl",
			"artifact_sha256": hex.EncodeToString(sum[:]),
			"artifact_bytes":  int64(len(artifact)),
		})
	}))
	defer server.Close()
	_, err := NewHTTPCollector(server.URL, time.Second).Collect(context.Background(), CollectionRequest{MovieCatalogPersonID: "p1", PersonName: "Person", PersonURL: "https://example.test/p1", Category: CategoryDrama})
	if !errors.Is(err, ErrCollectorProtocol) {
		t.Fatalf("expected same-origin protocol error, got %v", err)
	}

	badServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/person-related-catalog/collections" {
			_ = json.NewEncoder(w).Encode(map[string]any{"artifact_url": "/artifact", "artifact_sha256": strings.Repeat("0", 64), "artifact_bytes": int64(len(artifact))})
			return
		}
		_, _ = w.Write(artifact)
	}))
	defer badServer.Close()
	_, err = NewHTTPCollector(badServer.URL, time.Second).Collect(context.Background(), CollectionRequest{MovieCatalogPersonID: "p1", PersonName: "Person", PersonURL: "https://example.test/p1", Category: CategoryDrama})
	if !errors.Is(err, ErrArtifactIntegrity) {
		t.Fatalf("expected artifact integrity error, got %v", err)
	}
}

func TestHTTPCollectorRejectsUnsetProviderInvalidCategoryAndOversize(t *testing.T) {
	if _, err := NewHTTPCollector("", time.Second).Collect(context.Background(), CollectionRequest{MovieCatalogPersonID: "p1", PersonName: "Person", PersonURL: "https://example.test/p1", Category: CategoryDrama}); !errors.Is(err, ErrCollectorUnavailable) {
		t.Fatalf("expected unavailable provider error, got %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/person-related-catalog/collections" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"artifact_url": "/artifact", "artifact_sha256": strings.Repeat("0", 64), "artifact_bytes": int64(maxCollectorArtifactBytes + 1)})
	}))
	defer server.Close()
	if _, err := NewHTTPCollector(server.URL, time.Second).Collect(context.Background(), CollectionRequest{MovieCatalogPersonID: "p1", PersonName: "Person", PersonURL: "https://example.test/p1", Category: "movie"}); !errors.Is(err, ErrCollectorProtocol) {
		t.Fatalf("expected invalid category protocol error, got %v", err)
	}
	if _, err := NewHTTPCollector(server.URL, time.Second).Collect(context.Background(), CollectionRequest{MovieCatalogPersonID: "p1", PersonName: "Person", PersonURL: "https://example.test/p1", Category: CategoryDrama}); !errors.Is(err, ErrCollectorProtocol) {
		t.Fatalf("expected oversize protocol error, got %v", err)
	}
}

func TestHTTPCollectorHonorsCancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewHTTPCollector(server.URL, time.Second).Collect(ctx, CollectionRequest{MovieCatalogPersonID: "p1", PersonName: "Person", PersonURL: "https://example.test/p1", Category: CategoryDrama}); err == nil {
		t.Fatal("cancelled provider request must fail")
	}
}

func TestHTTPCollectorReturnsSemanticNegativeWithoutArtifact(t *testing.T) {
	providerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":              "unavailable",
			"reason_code":         "no_match",
			"retryable":           false,
			"retry_after_seconds": 3600,
			"retrieved_at":        "2026-08-12T00:00:00Z",
			"source":              "jpsearch",
			"candidates":          []string{"jp:1"},
		})
	}))
	defer server.Close()
	result, err := NewHTTPCollector(server.URL, time.Second).Collect(context.Background(), CollectionRequest{
		MovieCatalogPersonID: "p1", PersonName: "Person", PersonURL: "https://example.test/p1",
		Category: CategoryDrama, Source: "jpsearch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CollectionStatusUnavailable || result.ReasonCode != "no_match" || result.Artifact != nil || providerCalls != 1 {
		t.Fatalf("semantic negative result=%#v calls=%d", result, providerCalls)
	}
}

func TestHTTPCollectorRetriesOnlyTransientResponsesAndHonorsRetryAfter(t *testing.T) {
	attempts := 0
	var sleeps []time.Duration
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/artifact" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		attempts++
		if attempts < 3 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		artifact := []byte("ok")
		sum := sha256.Sum256(artifact)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ready", "artifact_url": "/artifact", "artifact_sha256": hex.EncodeToString(sum[:]), "artifact_bytes": int64(len(artifact)),
		})
	}))
	defer server.Close()
	collector := NewHTTPCollectorWithOptions(server.URL, time.Second, HTTPCollectorOptions{
		Sleep: func(ctx context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			return nil
		},
	})
	result, err := collector.Collect(context.Background(), CollectionRequest{MovieCatalogPersonID: "p1", PersonName: "Person", PersonURL: "https://example.test/p1", Category: CategoryDrama})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CollectionStatusReady || attempts != 3 || len(sleeps) != 2 || sleeps[0] != 7*time.Second || sleeps[1] != 7*time.Second {
		t.Fatalf("result=%#v attempts=%d sleeps=%v", result, attempts, sleeps)
	}
}

func TestHTTPCollectorDoesNotRetrySemanticOrClientErrors(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":"rejected","reason_code":"rights"}`))
	}))
	defer server.Close()
	collector := NewHTTPCollectorWithOptions(server.URL, time.Second, HTTPCollectorOptions{Sleep: func(context.Context, time.Duration) error { t.Fatal("unexpected retry"); return nil }})
	if _, err := collector.Collect(context.Background(), CollectionRequest{MovieCatalogPersonID: "p1", PersonName: "Person", PersonURL: "https://example.test/p1", Category: CategoryDrama}); !errors.Is(err, ErrCollectorProtocol) {
		t.Fatalf("error=%v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d want=1", attempts)
	}
}

func reflectExactCollectionRequest(got, want map[string]any) bool {
	if len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}
