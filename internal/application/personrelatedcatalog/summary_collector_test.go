package personrelatedcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHTTPSummaryCollectorPostsExactTargetsAndValidatesArtifact(t *testing.T) {
	descriptionHash := sha256.Sum256([]byte("日本語概要"))
	artifact := []byte(strings.Join([]string{
		`{"schema_version":"rencrow.person-related-catalog.v1","record_type":"summary_manifest","run_id":"run-1","request_id":"req-1","source":"jpsearch","retrieved_at":"2026-08-12T00:00:00Z","status":"ready","item_count":1,"ready_count":1,"unavailable_count":0}`,
		`{"schema_version":"rencrow.person-related-catalog.v1","record_type":"summary_patch","category":"drama","item_id":"d1","source":"jpsearch","source_record_id":"jp:1","canonical_url":"https://jpsearch.go.jp/item/1","evidence_url":"https://jpsearch.go.jp/item/1","description_original":"日本語概要","description_language":"ja","description_ja":"日本語概要","description_translation_state":"not_required","source_status":"ready","translation_status":"not_required","retrieved_at":"2026-08-12T00:00:00Z","content_sha256":"` + hex.EncodeToString(descriptionHash[:]) + `","rights":"CC0"}`,
	}, "\n") + "\n")
	sum := sha256.Sum256(artifact)
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/person-related-catalog/summaries":
			if r.Method != http.MethodPost {
				t.Fatalf("method=%s", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ready", "source": "jpsearch", "retrieved_at": "2026-08-12T00:00:00Z", "artifact_url": "/artifacts/summary.jsonl", "artifact_sha256": hex.EncodeToString(sum[:]), "artifact_bytes": len(artifact)})
		case "/artifacts/summary.jsonl":
			_, _ = w.Write(artifact)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	target := SummaryTarget{Category: CategoryDrama, ItemID: "d1", Source: "jpsearch", SourceRecordID: "jp:1", CanonicalURL: "https://jpsearch.go.jp/item/1", JapanSearchID: "jp:1"}
	result, err := NewHTTPSummaryCollector(server.URL, time.Second).CollectSummaries(context.Background(), SummaryCollectionRequest{RequestID: "req-1", Targets: []SummaryTarget{target}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CollectionStatusReady || len(result.Patches) != 1 || result.Patches[0].DescriptionJA != "日本語概要" {
		t.Fatalf("result=%#v", result)
	}
	want := map[string]any{"request_id": "req-1", "targets": []any{map[string]any{"category": "drama", "item_id": "d1", "source": "jpsearch", "source_record_id": "jp:1", "canonical_url": "https://jpsearch.go.jp/item/1", "jpsearch_id": "jp:1"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request=%#v want=%#v", got, want)
	}
}

func TestHTTPSummaryCollectorRejectsTargetMismatchUnknownFieldsAndOversize(t *testing.T) {
	target := SummaryTarget{Category: CategoryDrama, ItemID: "d1", Source: "jpsearch", SourceRecordID: "jp:1", CanonicalURL: "https://jpsearch.go.jp/item/1", JapanSearchID: "jp:1"}
	for _, mutation := range []string{
		`"item_id":"other"`,
		`"item_id":"d1","display_name":"invented"`,
	} {
		artifact := []byte(`{"schema_version":"rencrow.person-related-catalog.v1","record_type":"summary_manifest","run_id":"run-1","request_id":"req-1","source":"jpsearch","retrieved_at":"2026-08-12T00:00:00Z","status":"unavailable","item_count":1,"ready_count":0,"unavailable_count":1}` + "\n" +
			`{"schema_version":"rencrow.person-related-catalog.v1","record_type":"summary_patch","category":"drama",` + mutation + `,"source":"jpsearch","source_record_id":"jp:1","canonical_url":"https://jpsearch.go.jp/item/1","evidence_url":"https://jpsearch.go.jp/item/1","source_status":"unavailable","translation_status":"not_attempted","retrieved_at":"2026-08-12T00:00:00Z","reason":"missing"}` + "\n")
		sum := sha256.Sum256(artifact)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/artifacts/") {
				_, _ = w.Write(artifact)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "unavailable", "retrieved_at": "2026-08-12T00:00:00Z", "artifact_url": "/artifacts/x", "artifact_sha256": hex.EncodeToString(sum[:]), "artifact_bytes": len(artifact)})
		}))
		_, err := NewHTTPSummaryCollector(server.URL, time.Second).CollectSummaries(context.Background(), SummaryCollectionRequest{RequestID: "req-1", Targets: []SummaryTarget{target}})
		server.Close()
		if err == nil {
			t.Fatalf("mutation %q accepted", mutation)
		}
	}
	targets := make([]SummaryTarget, 21)
	for index := range targets {
		targets[index] = target
		targets[index].ItemID = string(rune('a' + index))
	}
	if _, err := NewHTTPSummaryCollector("http://127.0.0.1:1", time.Second).CollectSummaries(context.Background(), SummaryCollectionRequest{RequestID: "req", Targets: targets}); err == nil {
		t.Fatal("more than twenty targets accepted")
	}
}

func TestValidateSummaryArtifactRejectsContentHashAndManifestCountMismatch(t *testing.T) {
	target := SummaryTarget{Category: CategoryDrama, ItemID: "d1", Source: "jpsearch", SourceRecordID: "jp:1", CanonicalURL: "https://jpsearch.go.jp/item/1"}
	request := SummaryCollectionRequest{RequestID: "req-1", Targets: []SummaryTarget{target}}
	for name, manifestCounts := range map[string]string{
		"content_hash":   `"ready_count":1,"unavailable_count":0`,
		"manifest_count": `"ready_count":0,"unavailable_count":1`,
	} {
		artifact := []byte(`{"schema_version":"rencrow.person-related-catalog.v1","record_type":"summary_manifest","run_id":"run-1","request_id":"req-1","source":"jpsearch","retrieved_at":"2026-08-12T00:00:00Z","status":"ready","item_count":1,` + manifestCounts + `}` + "\n" +
			`{"schema_version":"rencrow.person-related-catalog.v1","record_type":"summary_patch","category":"drama","item_id":"d1","source":"jpsearch","source_record_id":"jp:1","canonical_url":"https://jpsearch.go.jp/item/1","evidence_url":"https://jpsearch.go.jp/item/1","description_original":"日本語概要","description_language":"ja","description_ja":"日本語概要","description_translation_state":"not_required","source_status":"ready","translation_status":"not_required","retrieved_at":"2026-08-12T00:00:00Z","content_sha256":"` + strings.Repeat("0", 64) + `","rights":"CC0"}` + "\n")
		if _, err := validateSummaryArtifact(artifact, request); err == nil {
			t.Fatalf("%s mismatch accepted", name)
		}
	}
}
