//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/Nyukimin/RenCrow_CORE/pkg/rencrowclient"
)

func TestE2E_ToolHarnessAndDCIStatusClientCurrentView(t *testing.T) {
	if os.Getenv("RENCROW_LIVE_E2E") != "1" {
		t.Skip("set RENCROW_LIVE_E2E=1 to verify live Tool Harness and DCI status clients")
	}

	baseURL := liveBaseURL()
	tokenPath := strings.TrimSpace(os.Getenv("RENCROW_LIVE_E2E_OWNER_TOKEN_FILE"))
	if tokenPath == "" {
		t.Fatal("RENCROW_LIVE_E2E_OWNER_TOKEN_FILE must be set for live DCI owner search")
	}
	ownerUserID := strings.TrimSpace(os.Getenv("RENCROW_LIVE_E2E_OWNER_USER_ID"))
	if ownerUserID == "" || strings.IndexFunc(ownerUserID, unicode.IsSpace) >= 0 {
		t.Fatal("RENCROW_LIVE_E2E_OWNER_USER_ID must contain one non-whitespace owner ID")
	}
	rawToken, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal("read live DCI owner token file failed")
	}
	ownerToken := strings.TrimSpace(string(rawToken))
	if ownerToken == "" || strings.IndexFunc(ownerToken, unicode.IsSpace) >= 0 {
		t.Fatal("live DCI owner token file must contain one non-whitespace token")
	}
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}), rencrowclient.WithOwnerBearerToken(ownerToken))
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
	if err := search.Trace.TraceID.Validate(); err != nil {
		t.Fatalf("live DCI trace_id is invalid: %v", err)
	}
	if err := search.Trace.ActionID.Validate(); err != nil {
		t.Fatalf("live DCI action_id is invalid: %v", err)
	}
	if search.Trace.Status != "completed" || search.Trace.EndedAt.IsZero() || search.Trace.ActorAttribution != "authenticated" || search.Trace.ActorKind != "user" || search.Trace.ActorID != ownerUserID {
		t.Fatalf("live DCI search trace=%+v, want completed authenticated configured-owner trace with ended_at", search.Trace)
	}
	if search.Pack.ActionID != search.Trace.ActionID || len(search.Pack.Evidence) == 0 {
		t.Fatalf("live DCI search pack=%+v trace=%+v, want matching action_id and non-empty evidence", search.Pack, search.Trace)
	}
	for _, evidence := range search.Pack.Evidence {
		if err := evidence.EvidenceID.Validate(); err != nil {
			t.Fatalf("live DCI evidence_id is invalid: %v", err)
		}
		if err := evidence.CreatedByEventID.Validate(); err != nil {
			t.Fatalf("live DCI created_by_event_id is invalid: %v", err)
		}
		if string(evidence.EvidenceID) == string(evidence.CreatedByEventID) {
			t.Fatalf("live DCI evidence reused its event id: evidence=%q event=%q", evidence.EvidenceID, evidence.CreatedByEventID)
		}
	}

	recent, err := client.DCIRecent(ctx, 10)
	if err != nil {
		t.Fatalf("DCIRecent() live call failed at %s: %v", baseURL, err)
	}
	foundSearchTrace := false
	for _, item := range recent.Items {
		if item.ActionID == search.Trace.ActionID && item.TraceID == search.Trace.TraceID && item.Status == "completed" && !item.EndedAt.IsZero() {
			foundSearchTrace = true
			break
		}
	}
	if !foundSearchTrace {
		t.Fatalf("live DCI recent status did not include completed action %q and trace %q", search.Trace.ActionID, search.Trace.TraceID)
	}
}
