//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/pkg/rencrowclient"
)

func TestE2E_SuperAgentRunQueueClientManualLedgerFlow(t *testing.T) {
	if os.Getenv("RENCROW_LIVE_E2E") != "1" {
		t.Skip("set RENCROW_LIVE_E2E=1 to verify live SuperAgent run queue client")
	}

	baseURL := liveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}

	suffix := time.Now().UTC().Format("20060102150405")
	queueID := "rq_client_e2e_" + suffix
	runID := "run_client_e2e_" + suffix
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := client.CreateRunQueueItem(ctx, rencrowclient.RunQueueItem{
		QueueID:      queueID,
		RunID:        runID,
		WorkstreamID: "ws_client_e2e_" + suffix,
		Goal:         "live client E2E: verify manual run queue ledger terminal state without scheduler execution",
		Action:       "resume",
		Status:       "queued",
		Priority:     20,
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRunQueueItem() live call failed at %s: %v", baseURL, err)
	}

	claim, err := client.ClaimRunQueueItem(ctx)
	if err != nil {
		t.Fatalf("ClaimRunQueueItem() live call failed at %s: %v", baseURL, err)
	}
	if !claim.Claimed || claim.Item.QueueID != queueID || claim.Item.Status != "claimed" {
		t.Fatalf("claim=%+v, want claimed queue_id=%s", claim, queueID)
	}

	complete, err := client.CompleteRunQueueItem(ctx, rencrowclient.RunQueueCompleteRequest{
		QueueID: queueID,
		Status:  "completed",
		Reason:  "manual ledger client E2E completed; scheduler execution not used",
	})
	if err != nil {
		t.Fatalf("CompleteRunQueueItem() live call failed at %s: %v", baseURL, err)
	}
	if !complete.Completed || complete.Item.QueueID != queueID || complete.Item.Status != "completed" {
		t.Fatalf("complete=%+v, want completed queue_id=%s", complete, queueID)
	}

	statusResp, err := http.Get(baseURL + "/viewer/superagent?limit=50")
	if err != nil {
		t.Fatalf("live /viewer/superagent failed at %s: %v", baseURL, err)
	}
	defer statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("live /viewer/superagent status=%d, want 200", statusResp.StatusCode)
	}
	var body struct {
		RunQueue []struct {
			QueueID string `json:"queue_id"`
			Status  string `json:"status"`
			Reason  string `json:"reason"`
		} `json:"run_queue"`
	}
	if err := json.NewDecoder(statusResp.Body).Decode(&body); err != nil {
		t.Fatalf("decode live SuperAgent status: %v", err)
	}
	seen := 0
	for _, item := range body.RunQueue {
		if item.QueueID == queueID {
			seen++
			if item.Status != "completed" {
				t.Fatalf("run queue item=%+v, want completed", item)
			}
		}
	}
	if seen != 1 {
		t.Fatalf("live SuperAgent status returned %d current items for %s; run_queue=%+v", seen, queueID, body.RunQueue)
	}
}

func TestE2E_SuperAgentPauseResumeAndQueueReentryClientFlow(t *testing.T) {
	if os.Getenv("RENCROW_LIVE_E2E") != "1" {
		t.Skip("set RENCROW_LIVE_E2E=1 to verify live SuperAgent pause/resume client")
	}

	baseURL := liveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}

	suffix := time.Now().UTC().Format("20060102150405")
	runID := "run_resume_client_e2e_" + suffix
	workstreamID := "ws_resume_client_e2e_" + suffix
	queueID := "rq_resume_client_e2e_" + suffix
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := client.CreateAgentRun(ctx, rencrowclient.AgentRun{
		RunID:        runID,
		WorkstreamID: workstreamID,
		AgentType:    "LeadAgent",
		Goal:         "live client E2E: pause resume and queue reentry ledger flow",
		Status:       "running",
		StartedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateAgentRun() live call failed at %s: %v", baseURL, err)
	}

	paused, err := client.PauseRun(ctx, runID, "pause for resume ledger E2E")
	if err != nil {
		t.Fatalf("PauseRun() live call failed at %s: %v", baseURL, err)
	}
	if paused.RunID != runID || paused.Status != "paused" {
		t.Fatalf("pause=%+v, want paused run_id=%s", paused, runID)
	}

	resumed, err := client.ResumeRun(ctx, runID, "resume marker clear for queue reentry E2E")
	if err != nil {
		t.Fatalf("ResumeRun() live call failed at %s: %v", baseURL, err)
	}
	if resumed.RunID != runID || resumed.Status != "running" || resumed.RuntimeControlAction != "resume_marker_cleared" {
		t.Fatalf("resume=%+v, want running resume_marker_cleared run_id=%s", resumed, runID)
	}

	if err := client.CreateRunQueueItem(ctx, rencrowclient.RunQueueItem{
		QueueID:      queueID,
		RunID:        runID,
		WorkstreamID: workstreamID,
		Goal:         "resume run from queue after pause marker clear; do not execute external actions",
		Action:       "resume",
		Status:       "queued",
		Priority:     1000000,
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRunQueueItem() live call failed at %s: %v", baseURL, err)
	}
	claim, err := client.ClaimRunQueueItem(ctx)
	if err != nil {
		t.Fatalf("ClaimRunQueueItem() live call failed at %s: %v", baseURL, err)
	}
	if !claim.Claimed || claim.Item.QueueID != queueID || claim.Item.RunID != runID || claim.Item.Status != "claimed" {
		t.Fatalf("claim=%+v, want claimed queue_id=%s run_id=%s", claim, queueID, runID)
	}
	complete, err := client.CompleteRunQueueItem(ctx, rencrowclient.RunQueueCompleteRequest{
		QueueID: queueID,
		Status:  "completed",
		Reason:  "pause/resume queue reentry ledger E2E completed without scheduler execution",
	})
	if err != nil {
		t.Fatalf("CompleteRunQueueItem() live call failed at %s: %v", baseURL, err)
	}
	if !complete.Completed || complete.Item.QueueID != queueID || complete.Item.RunID != runID || complete.Item.Status != "completed" {
		t.Fatalf("complete=%+v, want completed queue_id=%s run_id=%s", complete, queueID, runID)
	}

	status, err := client.SuperAgentStatus(ctx, 100)
	if err != nil {
		t.Fatalf("SuperAgentStatus() live call failed at %s: %v", baseURL, err)
	}
	foundRun := false
	for _, run := range status.AgentRuns {
		if run.RunID == runID && run.WorkstreamID == workstreamID && run.Status == "running" {
			foundRun = true
			break
		}
	}
	if !foundRun {
		t.Fatalf("live SuperAgent status did not include resumed running run_id=%s workstream_id=%s", runID, workstreamID)
	}
	foundPaused := false
	foundResumed := false
	for _, event := range status.TraceEvents {
		if event.RunID != runID {
			continue
		}
		switch event.EventType {
		case "lead_agent_paused":
			foundPaused = true
		case "lead_agent_resumed":
			foundResumed = true
		}
	}
	if !foundPaused || !foundResumed {
		t.Fatalf("live SuperAgent status missing pause/resume trace for run_id=%s paused=%v resumed=%v", runID, foundPaused, foundResumed)
	}
	foundQueue := false
	for _, item := range status.RunQueue {
		if item.QueueID == queueID && item.RunID == runID && item.Status == "completed" {
			foundQueue = true
			break
		}
	}
	if !foundQueue {
		t.Fatalf("live SuperAgent status did not include completed queue_id=%s run_id=%s", queueID, runID)
	}
}
