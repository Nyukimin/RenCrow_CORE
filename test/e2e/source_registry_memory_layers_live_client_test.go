//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/pkg/rencrowclient"
)

func TestE2E_SourceRegistryStagingValidatePromoteAndMemoryLayers(t *testing.T) {
	if os.Getenv("PICOCLAW_LIVE_E2E") != "1" || os.Getenv("PICOCLAW_LIVE_SOURCE_REGISTRY_E2E") != "1" {
		t.Skip("set PICOCLAW_LIVE_E2E=1 and PICOCLAW_LIVE_SOURCE_REGISTRY_E2E=1 to verify live Source Registry and Memory Layers")
	}

	baseURL := liveBaseURL()
	runID := strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	sourceID := "rss:e2e-source-registry-" + runID
	namespace := "kb:e2e"
	title := "Source Registry live E2E " + runID
	publishedAt := time.Now().UTC().Add(-1 * time.Hour).Format(http.TimeFormat)

	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprintf(w, `<?xml version="1.0"?>
<rss version="2.0"><channel><title>RenCrow E2E</title>
<item><title>%s</title><link>https://example.com/source-registry/%s</link><description>live source registry staging validation promotion evidence</description><pubDate>%s</pubDate></item>
</channel></rss>`, title, runID, publishedAt)
	}))
	defer feed.Close()

	httpClient := &http.Client{Timeout: 10 * time.Second}
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(httpClient))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	saveSourceRegistryEntry(t, ctx, httpClient, baseURL, map[string]any{
		"source_id":          sourceID,
		"url":                feed.URL,
		"kind":               "rss",
		"trust_score":        0.95,
		"fetch_interval_sec": 3600,
		"license_note":       "local live e2e fixture",
		"enabled":            true,
		"meta": map[string]any{
			"category":  "e2e",
			"namespace": namespace,
		},
	})

	status, err := client.SourceRegistryStatus(ctx, false)
	if err != nil {
		t.Fatalf("SourceRegistryStatus() live call failed at %s: %v", baseURL, err)
	}
	if !sourceRegistryHasEntry(status, sourceID) {
		t.Fatalf("SourceRegistryStatus() missing saved source_id=%s entries=%+v", sourceID, status.Entries)
	}

	runSourceRegistryEntry(t, ctx, httpClient, baseURL, sourceID)

	staging, err := client.SourceRegistryStaging(ctx, "validated", 20)
	if err != nil {
		t.Fatalf("SourceRegistryStaging(validated) live call failed: %v", err)
	}
	item := sourceRegistryStagingItemForSource(staging, sourceID)
	if item == nil {
		t.Fatalf("validated staging item for source_id=%s not found: %+v", sourceID, staging.Items)
	}
	if item.ValidationStatus != "validated" || item.RawText == "" || !strings.Contains(item.RawText, title) {
		t.Fatalf("unexpected validated staging item: %+v", *item)
	}

	minTrust := 0.5
	validation, err := client.ValidateSourceRegistryStaging(ctx, rencrowclient.SourceRegistryValidateRequest{
		ID:                item.ID,
		MinimumTrustScore: &minTrust,
	})
	if err != nil {
		t.Fatalf("ValidateSourceRegistryStaging() live call failed: %v", err)
	}
	if !validation.Result.Passed || validation.Result.Status != "validated" || validation.Result.ItemID != item.ID {
		t.Fatalf("unexpected validation response: %+v", validation.Result)
	}

	promotion, err := client.PromoteSourceRegistryStaging(ctx, rencrowclient.SourceRegistryPromoteRequest{
		ID:              item.ID,
		Target:          "memory",
		TargetNamespace: namespace,
		PromotedBy:      "live-e2e",
	})
	if err != nil {
		t.Fatalf("PromoteSourceRegistryStaging(memory) live call failed: %v", err)
	}
	if promotion.Target != "memory" || promotion.Item["ID"] == "" || promotion.Item["Namespace"] != namespace {
		t.Fatalf("unexpected memory promotion response: %+v", promotion)
	}

	layers, err := client.MemoryLayers(ctx, rencrowclient.MemoryLayersRequest{
		Namespace: namespace,
		Limit:     20,
	})
	if err != nil {
		t.Fatalf("MemoryLayers() live call failed: %v", err)
	}
	if !memoryLayersContainMessage(layers, title) {
		t.Fatalf("memory layers for namespace=%s do not contain promoted message %q: %+v", namespace, title, layers)
	}
	t.Logf("source registry live E2E source_id=%s staging_id=%s promoted_memory_id=%v", sourceID, item.ID, promotion.Item["ID"])
}

func saveSourceRegistryEntry(t *testing.T, ctx context.Context, client *http.Client, baseURL string, entry map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"entries": []map[string]any{entry}})
	if err != nil {
		t.Fatalf("marshal source registry entry: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/viewer/source-registry", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create source registry save request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /viewer/source-registry failed: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /viewer/source-registry status=%d body=%q", resp.StatusCode, string(respBody))
	}
}

func runSourceRegistryEntry(t *testing.T, ctx context.Context, client *http.Client, baseURL string, sourceID string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/viewer/source-registry?action=run&source_id="+sourceID, nil)
	if err != nil {
		t.Fatalf("create source registry run request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /viewer/source-registry?action=run failed: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /viewer/source-registry?action=run status=%d body=%q", resp.StatusCode, string(respBody))
	}
	if !bytes.Contains(respBody, []byte(`"Staged":1`)) && !bytes.Contains(respBody, []byte(`"staged":1`)) {
		t.Fatalf("source registry run did not report one staged item: %s", string(respBody))
	}
}

func sourceRegistryHasEntry(status rencrowclient.SourceRegistryStatus, sourceID string) bool {
	for _, entry := range status.Entries {
		if entry.SourceID == sourceID {
			return true
		}
	}
	return false
}

func sourceRegistryStagingItemForSource(status rencrowclient.SourceRegistryStagingStatus, sourceID string) *rencrowclient.SourceRegistryStagingItem {
	for i := range status.Items {
		if status.Items[i].SourceID == sourceID {
			return &status.Items[i]
		}
	}
	return nil
}

func memoryLayersContainMessage(layers rencrowclient.MemoryLayersStatus, needle string) bool {
	for _, item := range layers.L1 {
		if strings.Contains(item.Message, needle) {
			return true
		}
	}
	for _, item := range layers.L3 {
		if strings.Contains(item.Message, needle) {
			return true
		}
	}
	return false
}
