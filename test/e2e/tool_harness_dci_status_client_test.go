//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/pkg/rencrowclient"
)

func TestE2E_ToolHarnessAndDCIStatusClientCurrentView(t *testing.T) {
	if os.Getenv("RENCROW_LIVE_E2E") != "1" {
		t.Skip("set RENCROW_LIVE_E2E=1 to verify live Tool Harness and DCI status clients")
	}

	baseURL := liveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	toolHarness, err := client.ToolHarnessStatus(ctx, 10)
	if err != nil {
		t.Fatalf("ToolHarnessStatus() live call failed at %s: %v", baseURL, err)
	}
	if len(toolHarness.Items) == 0 {
		t.Fatalf("live Tool Harness status has no events; cannot verify mediation event current view")
	}
	foundValidToolEvent := false
	for _, item := range toolHarness.Items {
		if item.ValidationStatus == "valid" && item.ToolName != "" && item.RawInputHash != "" {
			foundValidToolEvent = true
			break
		}
	}
	if !foundValidToolEvent {
		t.Fatalf("live Tool Harness status did not include a valid event with tool_name and raw_input_hash")
	}

	search, err := client.DCISearch(ctx, rencrowclient.DCISearchRequest{Query: "ToolRunner context budget"})
	if err != nil {
		t.Fatalf("DCISearch() live call failed at %s: %v", baseURL, err)
	}
	if search.Trace.Status != "completed" || search.Trace.EventID == "" || search.Trace.EndedAt.IsZero() {
		t.Fatalf("live DCI search trace=%+v, want completed trace with ended_at", search.Trace)
	}
	if search.Pack.EventID != search.Trace.EventID || len(search.Pack.Evidence) == 0 {
		t.Fatalf("live DCI search pack=%+v trace=%+v, want same event_id and non-empty evidence", search.Pack, search.Trace)
	}

	recent, err := client.DCIRecent(ctx, 10)
	if err != nil {
		t.Fatalf("DCIRecent() live call failed at %s: %v", baseURL, err)
	}
	foundSearchTrace := false
	for _, item := range recent.Items {
		if item.EventID == search.Trace.EventID && item.Status == "completed" && !item.EndedAt.IsZero() {
			foundSearchTrace = true
			break
		}
	}
	if !foundSearchTrace {
		t.Fatalf("live DCI recent status did not include completed search trace %q", search.Trace.EventID)
	}
}
