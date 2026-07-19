package pronunciationcheck_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	pronunciationapp "github.com/Nyukimin/RenCrow_CORE/internal/application/pronunciationcheck"
	pronunciationtool "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/pronunciationtool"
)

func TestLiveCOREPronunciationTask(t *testing.T) {
	baseURL := os.Getenv("RENCROW_PRONUNCIATION_TOOL_E2E_URL")
	if baseURL == "" {
		t.Skip("RENCROW_PRONUNCIATION_TOOL_E2E_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	toolClient := pronunciationtool.NewClient(baseURL, &http.Client{Timeout: 4 * time.Minute}, 2*time.Second)
	service := pronunciationapp.NewService(
		toolClient,
		toolClient,
		pronunciationapp.Config{
			GPUMatch: "RTX 5060 Ti", MinFreeMB: 768, MaxUtilizationPercent: 10,
			IdleSamples: 5, SampleInterval: time.Second, RetryAfter: 5 * time.Minute,
		},
	)
	report, err := service.Run(ctx)
	if err != nil {
		t.Fatalf("CORE pronunciation task error = %v", err)
	}
	if report.Total == 0 || report.Total != report.Passed+report.Failed {
		t.Fatalf("report = %+v", report)
	}
}
