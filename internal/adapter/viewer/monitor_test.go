package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	domainexecution "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/execution"
)

type stubEvidenceStore struct {
	item domainexecution.ExecutionReport
	ok   bool
}

func (s stubEvidenceStore) ListRecent(ctx context.Context, limit int) ([]domainexecution.ExecutionReport, error) {
	if !s.ok {
		return nil, nil
	}
	return []domainexecution.ExecutionReport{s.item}, nil
}

func (s stubEvidenceStore) GetByJobID(ctx context.Context, jobID string) (domainexecution.ExecutionReport, error) {
	if s.ok && s.item.JobID == jobID {
		return s.item, nil
	}
	return domainexecution.ExecutionReport{}, context.Canceled
}

func (s stubEvidenceStore) ListRecentUnique(ctx context.Context, limit int) ([]domainexecution.ExecutionReport, error) {
	return s.ListRecent(ctx, limit)
}

func (s stubEvidenceStore) Summary(ctx context.Context) (map[string]map[string]int, error) {
	return map[string]map[string]int{"status": {"passed": 1}}, nil
}

func (s stubEvidenceStore) SummaryUnique(ctx context.Context) (map[string]map[string]int, error) {
	return s.Summary(ctx)
}

func TestMonitorStoreReducesAgentAndJobState(t *testing.T) {
	store := NewMonitorStore(nil, nil)
	jobID := "job-1"
	now := time.Now().Format(time.RFC3339)

	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "message.received",
		From:      "user",
		To:        "mio",
		Content:   "hello",
		JobID:     jobID,
		SessionID: "viewer",
		Timestamp: now,
	})
	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "routing.decision",
		From:      "mio",
		Route:     "CODE",
		Content:   "confidence 90%",
		JobID:     jobID,
		SessionID: "viewer",
		Timestamp: now,
	})
	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "agent.start",
		From:      "mio",
		To:        "shiro",
		Content:   "task",
		Route:     "CODE",
		JobID:     jobID,
		SessionID: "viewer",
		Timestamp: now,
	})
	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "agent.response",
		From:      "mio",
		To:        "user",
		Content:   "完了したよ",
		Route:     "CODE",
		JobID:     jobID,
		SessionID: "viewer",
		Timestamp: now,
	})

	status := store.Status()
	if status.Chat.Status != "idle" {
		t.Fatalf("chat status = %q, want idle", status.Chat.Status)
	}
	jobs := store.Jobs(JobFilter{})
	if len(jobs) != 1 {
		t.Fatalf("jobs len = %d, want 1", len(jobs))
	}
	if jobs[0].Status != "done" {
		t.Fatalf("job status = %q, want done", jobs[0].Status)
	}
	if jobs[0].Route != "CODE" {
		t.Fatalf("job route = %q, want CODE", jobs[0].Route)
	}
	if !jobs[0].MioReported {
		t.Fatalf("expected MioReported true")
	}
}

func TestMonitorStoreIncludesCoder4InStatusAndAgents(t *testing.T) {
	store := NewMonitorStore(nil, nil)

	status := store.Status()
	if len(status.Coders.Items) != 4 {
		t.Fatalf("coders items len = %d, want 4", len(status.Coders.Items))
	}
	if status.Coders.Items[3].ID != "coder4" {
		t.Fatalf("last coder id = %q, want coder4", status.Coders.Items[3].ID)
	}

	req := httptest.NewRequest(http.MethodGet, "/viewer/agents", nil)
	rr := httptest.NewRecorder()
	HandleMonitorAgents(store).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp struct {
		Agents []AgentSnapshot `json:"agents"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Agents) != 8 {
		t.Fatalf("agents len = %d, want 8", len(resp.Agents))
	}
	if resp.Agents[7].ID != "coder4" {
		t.Fatalf("last agent id = %q, want coder4", resp.Agents[7].ID)
	}
}

func TestMonitorStoreTracksViewerRecipientAsJobOwnerAndFinalSpeaker(t *testing.T) {
	store := NewMonitorStore(nil, nil)
	jobID := "job-kuro"
	now := time.Now().Format(time.RFC3339)

	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "message.received",
		From:      "user",
		To:        "kuro",
		Content:   "合言葉 RC_kuro_current",
		JobID:     jobID,
		SessionID: "viewer",
		Timestamp: now,
	})
	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "routing.decision",
		From:      "mio",
		Route:     "CHAT",
		Content:   "confidence 90%",
		JobID:     jobID,
		SessionID: "viewer",
		Timestamp: now,
	})
	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "agent.response",
		From:      "kuro",
		To:        "user",
		Content:   "RC_kuro_current、分析完了です。",
		Route:     "CHAT",
		JobID:     jobID,
		SessionID: "viewer",
		Timestamp: now,
	})

	agents := store.Agents()
	var kuro AgentSnapshot
	for _, agent := range agents {
		if agent.ID == "kuro" {
			kuro = agent
			break
		}
	}
	if kuro.State != "idle" || kuro.LastEvent != "agent.response" {
		t.Fatalf("kuro snapshot = %+v, want idle agent.response", kuro)
	}
	jobs := store.Jobs(JobFilter{})
	if len(jobs) != 1 {
		t.Fatalf("jobs len = %d, want 1", len(jobs))
	}
	if jobs[0].Owner != "kuro" || jobs[0].Status != "done" || jobs[0].TerminalOutcome != "ok" {
		t.Fatalf("job snapshot = %+v, want owner=kuro done/ok", jobs[0])
	}
	if jobs[0].MioReported {
		t.Fatalf("kuro final response must not be marked as MioReported: %+v", jobs[0])
	}
}

func TestMonitorStoreClearsActiveAgentsForCompletedViewerRecipientJob(t *testing.T) {
	store := NewMonitorStore(nil, nil)
	jobID := "job-midori"
	now := time.Now().Format(time.RFC3339)

	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "message.received",
		From:      "user",
		To:        "midori",
		Content:   "合言葉 RC_midori_current",
		JobID:     jobID,
		SessionID: "viewer",
		Timestamp: now,
	})
	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "routing.decision",
		From:      "mio",
		Route:     "CHAT",
		Content:   "confidence 90%",
		JobID:     jobID,
		SessionID: "viewer",
		Timestamp: now,
	})
	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "agent.thinking",
		From:      "mio",
		To:        "user",
		Content:   "midoriで取り組むね！",
		Route:     "CHAT",
		JobID:     jobID,
		SessionID: "viewer",
		Timestamp: now,
	})
	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "agent.response",
		From:      "midori",
		To:        "user",
		Content:   "RC_midori_current、発想を広げました。",
		Route:     "CHAT",
		JobID:     jobID,
		SessionID: "viewer",
		Timestamp: now,
	})

	agents := store.Agents()
	var mio AgentSnapshot
	var midori AgentSnapshot
	for _, agent := range agents {
		switch agent.ID {
		case "mio":
			mio = agent
		case "midori":
			midori = agent
		}
	}
	if mio.State != "idle" || mio.LastEvent != "agent.response" {
		t.Fatalf("mio snapshot = %+v, want idle agent.response after terminal response", mio)
	}
	if midori.State != "idle" || midori.LastEvent != "agent.response" {
		t.Fatalf("midori snapshot = %+v, want idle agent.response", midori)
	}

	jobs := store.Jobs(JobFilter{})
	if len(jobs) != 1 {
		t.Fatalf("jobs len = %d, want 1", len(jobs))
	}
	if jobs[0].Owner != "midori" || jobs[0].Status != "done" || jobs[0].TerminalOutcome != "ok" {
		t.Fatalf("job snapshot = %+v, want owner=midori done/ok", jobs[0])
	}
}

func TestHandleMonitorStatusIncludesCoder4(t *testing.T) {
	store := NewMonitorStore(nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/viewer/status", nil)
	rr := httptest.NewRecorder()
	HandleMonitorStatus(store).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp struct {
		Status StatusSnapshot `json:"status"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Status.Coders.Items) != 4 {
		t.Fatalf("coders items len = %d, want 4", len(resp.Status.Coders.Items))
	}
	if resp.Status.Coders.Items[3].ID != "coder4" {
		t.Fatalf("last coder id = %q, want coder4", resp.Status.Coders.Items[3].ID)
	}
}

func TestMonitorStoreClearsRecoveredFailureOnFinalSuccess(t *testing.T) {
	store := NewMonitorStore(nil, nil)
	jobID := "job-retry"
	now := time.Now().Format(time.RFC3339)

	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "worker.classified_failure",
		From:      "shiro",
		To:        "coder1",
		Content:   "proposal_empty: missing Plan and Patch sections",
		Route:     "CODE",
		JobID:     jobID,
		SessionID: "viewer",
		Timestamp: now,
	})
	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "mailbox.received",
		From:      "coder1",
		To:        "mio",
		Content:   "via=local type=result",
		Route:     "CODE",
		JobID:     jobID,
		SessionID: "viewer",
		Timestamp: now,
	})
	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "agent.response",
		From:      "shiro",
		To:        "mio",
		Content:   "実行: 1 件, 成功: 1 件, 失敗: 0 件",
		Route:     "CODE",
		JobID:     jobID,
		SessionID: "viewer",
		Timestamp: now,
	})
	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "agent.response",
		From:      "mio",
		To:        "user",
		Content:   "実行: 1 件, 成功: 1 件, 失敗: 0 件",
		Route:     "CODE",
		JobID:     jobID,
		SessionID: "viewer",
		Timestamp: now,
	})

	jobs := store.Jobs(JobFilter{})
	if len(jobs) != 1 {
		t.Fatalf("jobs len = %d, want 1", len(jobs))
	}
	if jobs[0].Status != "done" {
		t.Fatalf("job status = %q, want done", jobs[0].Status)
	}
	if jobs[0].FailureKind != "" || jobs[0].FailureReason != "" {
		t.Fatalf("expected cleared failure, got kind=%q reason=%q", jobs[0].FailureKind, jobs[0].FailureReason)
	}
	if !jobs[0].MioReported {
		t.Fatalf("expected MioReported true")
	}
}

func TestMonitorStoreEntryStageCompletedMarksTerminalOutcome(t *testing.T) {
	store := NewMonitorStore(nil, nil)
	jobID := "job-stage-complete"
	now := time.Now().Format(time.RFC3339)

	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "entry.stage",
		From:      "viewer",
		To:        "system",
		Content:   "received",
		Route:     "CODE2",
		JobID:     jobID,
		SessionID: "viewer",
		Timestamp: now,
	})
	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "entry.stage",
		From:      "viewer",
		To:        "system",
		Content:   "completed",
		Route:     "CODE2",
		JobID:     jobID,
		SessionID: "viewer",
		Timestamp: now,
	})

	jobs := store.Jobs(JobFilter{})
	if len(jobs) != 1 {
		t.Fatalf("jobs len = %d, want 1", len(jobs))
	}
	if jobs[0].Status != "done" || jobs[0].Phase != "done" || jobs[0].TerminalOutcome != "ok" {
		t.Fatalf("unexpected terminal job: %+v", jobs[0])
	}
}

func TestMonitorStoreEntryStageFailedMarksTerminalOutcome(t *testing.T) {
	store := NewMonitorStore(nil, nil)
	jobID := "job-stage-failed"
	now := time.Now().Format(time.RFC3339)

	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "entry.stage",
		From:      "viewer",
		To:        "system",
		Content:   "planning",
		Route:     "CODE2",
		JobID:     jobID,
		SessionID: "viewer",
		Timestamp: now,
	})
	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "entry.stage",
		From:      "viewer",
		To:        "system",
		Content:   "failed",
		Route:     "CODE2",
		JobID:     jobID,
		SessionID: "viewer",
		Timestamp: now,
	})

	jobs := store.Jobs(JobFilter{})
	if len(jobs) != 1 {
		t.Fatalf("jobs len = %d, want 1", len(jobs))
	}
	if jobs[0].Status != "error" || jobs[0].Phase != "error" || jobs[0].TerminalOutcome != "failed" {
		t.Fatalf("unexpected terminal job: %+v", jobs[0])
	}
	if jobs[0].FailureReason == "" {
		t.Fatalf("expected visible failure reason: %+v", jobs[0])
	}
}

func TestHandleMonitorLogsFiltersByJobID(t *testing.T) {
	store := NewMonitorStore(nil, nil)
	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "agent.note",
		From:      "mio",
		To:        "user",
		Content:   "job1",
		Route:     "CHAT",
		JobID:     "job-1",
		Timestamp: time.Now().Format(time.RFC3339),
	})
	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "agent.note",
		From:      "mio",
		To:        "user",
		Content:   "job2",
		Route:     "CHAT",
		JobID:     "job-2",
		Timestamp: time.Now().Format(time.RFC3339),
	})

	req := httptest.NewRequest(http.MethodGet, "/viewer/logs?job_id=job-1&limit=10", nil)
	rr := httptest.NewRecorder()
	HandleMonitorLogs(store).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp struct {
		Items []orchestrator.OrchestratorEvent `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items len = %d, want 1", len(resp.Items))
	}
	if resp.Items[0].JobID != "job-1" {
		t.Fatalf("job_id = %q, want job-1", resp.Items[0].JobID)
	}
}

func TestHandleMonitorJobDetailIncludesEvidence(t *testing.T) {
	ev := domainexecution.ExecutionReport{
		JobID:      "job-1",
		Goal:       "goal",
		Status:     "passed",
		CreatedAt:  time.Now(),
		FinishedAt: time.Now(),
	}
	store := NewMonitorStore(stubEvidenceStore{item: ev, ok: true}, nil)
	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "agent.response",
		From:      "mio",
		To:        "user",
		Content:   "done",
		Route:     "CHAT",
		JobID:     "job-1",
		Timestamp: time.Now().Format(time.RFC3339),
	})

	req := httptest.NewRequest(http.MethodGet, "/viewer/job/detail?job_id=job-1", nil)
	rr := httptest.NewRecorder()
	HandleMonitorJobDetail(store).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp struct {
		Item     JobSnapshot                      `json:"item"`
		Evidence *domainexecution.ExecutionReport `json:"evidence"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Item.JobID != "job-1" {
		t.Fatalf("job_id = %q, want job-1", resp.Item.JobID)
	}
	if resp.Evidence == nil || resp.Evidence.JobID != "job-1" {
		t.Fatalf("expected evidence for job-1")
	}
}

func TestMonitorStoreSetAgentUnavailableShowsReasonInStatus(t *testing.T) {
	store := NewMonitorStore(nil, nil)

	store.SetAgentUnavailable("coder3", "ssh connect failed: connection reset by peer")

	status := store.Status()
	if status.Coders.Status != "degraded" {
		t.Fatalf("coders status = %q, want degraded", status.Coders.Status)
	}
	if len(status.Coders.Items) < 3 {
		t.Fatalf("coders items len = %d, want at least 3", len(status.Coders.Items))
	}

	var coder3 AgentSnapshot
	for _, item := range status.Coders.Items {
		if item.ID == "coder3" {
			coder3 = item
			break
		}
	}
	if coder3.State != "unavailable" {
		t.Fatalf("coder3 state = %q, want unavailable", coder3.State)
	}
	if coder3.Reason != "ssh connect failed: connection reset by peer" {
		t.Fatalf("coder3 reason = %q", coder3.Reason)
	}
}

func TestHandleMonitorAgentDetailReturnsAgentHistory(t *testing.T) {
	store := NewMonitorStore(nil, nil)
	now := time.Now().Format(time.RFC3339)
	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "agent.start",
		From:      "mio",
		To:        "shiro",
		Content:   "task",
		Route:     "CODE",
		JobID:     "job-1",
		SessionID: "viewer",
		Timestamp: now,
	})
	store.OnEvent(orchestrator.OrchestratorEvent{
		Type:      "agent.note",
		From:      "shiro",
		To:        "mio",
		Content:   "processing",
		Route:     "CODE",
		JobID:     "job-1",
		SessionID: "viewer",
		Timestamp: now,
	})

	req := httptest.NewRequest(http.MethodGet, "/viewer/agent/detail?id=shiro&limit=10", nil)
	rr := httptest.NewRecorder()
	HandleMonitorAgentDetail(store).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp AgentDetail
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Agent.ID != "shiro" {
		t.Fatalf("agent id = %q, want shiro", resp.Agent.ID)
	}
	if len(resp.Events) == 0 {
		t.Fatalf("expected agent events")
	}
}
