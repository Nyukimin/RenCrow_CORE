//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/pkg/rencrowclient"
)

func TestE2E_ComplexityStatusClientCurrentView(t *testing.T) {
	if os.Getenv("PICOCLAW_LIVE_E2E") != "1" {
		t.Skip("set PICOCLAW_LIVE_E2E=1 to verify live Complexity Hotspot status client")
	}

	baseURL := liveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	status, err := client.ComplexityStatus(ctx, 20)
	if err != nil {
		t.Fatalf("ComplexityStatus() live call failed at %s: %v", baseURL, err)
	}
	if len(status.Scans) == 0 {
		t.Fatalf("live Complexity status has no scans; report-only flow is not available as E2E evidence")
	}
	foundCompletedReportOnlyScan := false
	for _, scan := range status.Scans {
		if scan.Status == "completed" && scan.Mode == "report_only" && !scan.CompletedAt.IsZero() {
			foundCompletedReportOnlyScan = true
			break
		}
	}
	if !foundCompletedReportOnlyScan {
		t.Fatalf("live Complexity status did not include a completed report_only scan with completed_at")
	}
	if len(status.Hotspots) == 0 {
		t.Fatalf("live Complexity status has no hotspots for completed report-only scan")
	}
	if len(status.Evidence) == 0 {
		t.Fatalf("live Complexity status has no evidence for completed report-only scan")
	}
	foundReviewOnlyReport := false
	for _, report := range status.Reports {
		if report.Status == "pending_review" && report.Type != "" && report.Content != "" {
			foundReviewOnlyReport = true
			break
		}
	}
	if !foundReviewOnlyReport {
		t.Fatalf("live Complexity status did not include a pending_review report artifact")
	}
}
