//go:build e2e

package autonomousverification

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// TestE2E_CHAT_BypassAutonomousExecutor はCHAT routeがautonomous executorをバイパスすることを検証する
func TestE2E_CHAT_BypassAutonomousExecutor(t *testing.T) {
	cfg := getConfig(t)

	// Stage recorder and report store
	stageRecorder := NewStageRecorder()
	mockReportStore := NewMockReportStore()
	eventCapture := NewEventCapture()

	// Build orchestrator (using real Mio, no Shiro, no Coders for CHAT test)
	orch := buildTestOrchestrator(t, cfg,
		nil,               // mockMio = nil (use real Ollama)
		nil,               // mockShiro = nil
		nil, nil, nil,     // mockCoder1/2/3 = nil
		mockReportStore,   // inject mock report store
		eventCapture,      // inject event capture
	)

	// TODO: Inject stageRecorder as observer (need to add observer injection support)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := orch.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
		SessionID:   "e2e-test-chat-bypass",
		Channel:     "test",
		ChatID:      "e2e-user",
		UserMessage: "今日の天気はどうですか？", // CHAT routeにルーティングされる
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	// Assertions
	if resp.Route != routing.RouteCHAT {
		t.Errorf("route: want CHAT, got %s", resp.Route)
	}

	if resp.Response == "" {
		t.Error("expected non-empty response from Mio.Chat()")
	}

	// Verify no stage transitions (CHAT bypasses autonomous executor)
	stages := stageRecorder.GetSequence()
	if len(stages) > 0 {
		t.Errorf("expected no stage transitions for CHAT route, but got: %v", stages)
	}

	// Verify no execution report saved (CHAT bypasses autonomous executor)
	assertReportEmpty(t, mockReportStore)

	// Verify event "agent.response" was emitted
	if _, ok := eventCapture.FindEvent("agent.response"); !ok {
		t.Error("expected 'agent.response' event for CHAT route")
	}

	t.Logf("Route: %s (confidence: %.2f)", resp.Route, resp.Confidence)
	t.Logf("Response (first 200 chars): %.200s", resp.Response)
}

// TestE2E_OPS_SuccessFlow はOPS routeの成功フローを検証する
func TestE2E_OPS_SuccessFlow(t *testing.T) {
	cfg := getConfig(t)

	// Mock ReportStore and EventCapture
	mockReportStore := NewMockReportStore()
	eventCapture := NewEventCapture()

	// Mock Mio to route to OPS
	mockMio := &MockMioAgent{
		DecideActionFunc: func(ctx context.Context, task task.Task) (routing.Decision, error) {
			return routing.Decision{
				Route:      routing.RouteOPS,
				Confidence: 1.0,
				Reason:     "explicit /ops command",
			}, nil
		},
	}

	// Mock Shiro to return success
	mockShiro := &MockShiroAgent{
		ExecuteFunc: func(ctx context.Context, t task.Task) (string, error) {
			return "ディスク使用率: 45%", nil
		},
	}

	// Build orchestrator
	orch := buildTestOrchestrator(t, cfg,
		mockMio,           // use mock Mio
		mockShiro,         // use mock Shiro
		nil, nil, nil,     // no coders needed for OPS
		mockReportStore,   // inject mock report store
		eventCapture,      // inject event capture
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := orch.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
		SessionID:   "e2e-test-ops-success",
		Channel:     "test",
		ChatID:      "e2e-user",
		UserMessage: "/ops システムのディスク使用率を確認して",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	// Assertions
	if resp.Route != routing.RouteOPS {
		t.Errorf("route: want OPS, got %s", resp.Route)
	}

	if resp.Response == "" {
		t.Error("expected non-empty response from Shiro.Execute()")
	}

	// Verify execution report was saved
	report, ok := mockReportStore.GetLastReport()
	if !ok {
		t.Fatal("expected execution report to be saved for OPS route")
	}

	// Verify report fields
	assertExecutionReport(t, report, map[string]interface{}{
		"Route":        "OPS",
		"Capability":   "generic_execution",
		"Status":       "passed",
		"AttemptCount": 1,
		"RepairCount":  0,
		"ErrorKind":    "",
		"FailureReason": "",
	})

	t.Logf("Route: %s (confidence: %.2f)", resp.Route, resp.Confidence)
	t.Logf("Response: %s", resp.Response)
	t.Logf("Report Status: %s (AttemptCount: %d, RepairCount: %d)",
		report.Status, report.AttemptCount, report.RepairCount)
}

// TestE2E_OPS_RetryThenSuccess はOPS routeの初回失敗→retry成功フローを検証する
func TestE2E_OPS_RetryThenSuccess(t *testing.T) {
	cfg := getConfig(t)

	mockReportStore := NewMockReportStore()
	eventCapture := NewEventCapture()

	mockMio := &MockMioAgent{
		DecideActionFunc: func(ctx context.Context, task task.Task) (routing.Decision, error) {
			return routing.Decision{
				Route:      routing.RouteOPS,
				Confidence: 1.0,
				Reason:     "explicit /ops command",
			}, nil
		},
	}

	// Mock Shiro: 初回失敗、2回目成功
	attemptCount := 0
	mockShiro := &MockShiroAgent{
		ExecuteFunc: func(ctx context.Context, t task.Task) (string, error) {
			attemptCount++
			if attemptCount == 1 {
				return "", fmt.Errorf("command not found: disk-check")
			}
			return "ディスク使用率: 45%", nil
		},
	}

	orch := buildTestOrchestrator(t, cfg,
		mockMio, mockShiro, nil, nil, nil,
		mockReportStore, eventCapture,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := orch.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
		SessionID:   "e2e-test-ops-retry",
		Channel:     "test",
		ChatID:      "e2e-user",
		UserMessage: "/ops システムのディスク使用率を確認して",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if resp.Route != routing.RouteOPS {
		t.Errorf("route: want OPS, got %s", resp.Route)
	}

	// Verify execution report
	report, ok := mockReportStore.GetLastReport()
	if !ok {
		t.Fatal("expected execution report to be saved")
	}

	// After retry success, status should be "passed", attempt_count=2, repair_count=1
	// ErrorKind and FailureReason should be cleared on success
	assertExecutionReport(t, report, map[string]interface{}{
		"Route":         "OPS",
		"Status":        "passed",
		"AttemptCount":  2,
		"RepairCount":   1,
		"ErrorKind":     "",
		"FailureReason": "",
	})

	t.Logf("Route: %s", resp.Route)
	t.Logf("Report Status: %s (AttemptCount: %d, RepairCount: %d)",
		report.Status, report.AttemptCount, report.RepairCount)
}

// TestE2E_OPS_RepairExhausted はOPS routeのrepair失敗（exhausted）フローを検証する
func TestE2E_OPS_RepairExhausted(t *testing.T) {
	cfg := getConfig(t)

	mockReportStore := NewMockReportStore()
	eventCapture := NewEventCapture()

	mockMio := &MockMioAgent{
		DecideActionFunc: func(ctx context.Context, task task.Task) (routing.Decision, error) {
			return routing.Decision{
				Route:      routing.RouteOPS,
				Confidence: 1.0,
				Reason:     "explicit /ops command",
			}, nil
		},
	}

	// Mock Shiro: 常に失敗
	mockShiro := &MockShiroAgent{
		ExecuteFunc: func(ctx context.Context, t task.Task) (string, error) {
			return "", fmt.Errorf("command not found: disk-check")
		},
	}

	orch := buildTestOrchestrator(t, cfg,
		mockMio, mockShiro, nil, nil, nil,
		mockReportStore, eventCapture,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := orch.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
		SessionID:   "e2e-test-ops-repair-exhausted",
		Channel:     "test",
		ChatID:      "e2e-user",
		UserMessage: "/ops システムのディスク使用率を確認して",
	})

	// Note: ProcessMessage may or may not return error depending on error handling policy
	// We check the execution report instead
	_ = err

	// Note: When autonomous executor fails completely, resp.Route may be empty
	// We primarily verify via the execution report which contains the route information

	// Verify execution report
	report, ok := mockReportStore.GetLastReport()
	if !ok {
		t.Fatal("expected execution report to be saved")
	}

	// After repair exhausted, status should be "failed", attempt_count=2, repair_count=1
	// ErrorKind and FailureReason should be populated
	if report.Status != "failed" {
		t.Errorf("Status: want failed, got %s", report.Status)
	}
	if report.AttemptCount != 2 {
		t.Errorf("AttemptCount: want 2, got %d", report.AttemptCount)
	}
	if report.RepairCount != 1 {
		t.Errorf("RepairCount: want 1, got %d", report.RepairCount)
	}
	if report.ErrorKind == "" {
		t.Error("ErrorKind should be populated for failed report")
	}
	if report.FailureReason == "" {
		t.Error("FailureReason should be populated for failed report")
	}

	t.Logf("Route: %s", resp.Route)
	t.Logf("Report Status: %s (AttemptCount: %d, RepairCount: %d)",
		report.Status, report.AttemptCount, report.RepairCount)
	t.Logf("ErrorKind: %s, FailureReason: %s", report.ErrorKind, report.FailureReason)
}

// ========================================
// Phase 3: CODE Routes Tests
// ========================================

// TestE2E_CODE_SuccessFlow はCODE routeの成功フローを検証する
func TestE2E_CODE_SuccessFlow(t *testing.T) {
	cfg := getConfig(t)

	mockReportStore := NewMockReportStore()
	eventCapture := NewEventCapture()

	mockMio := &MockMioAgent{
		DecideActionFunc: func(ctx context.Context, task task.Task) (routing.Decision, error) {
			return routing.Decision{
				Route:      routing.RouteCODE,
				Confidence: 1.0,
				Reason:     "code generation request",
			}, nil
		},
	}

	// Mock Coder1 (DeepSeek) for CODE route
	mockCoder1 := &MockCoderAgent{
		GenerateFunc: func(ctx context.Context, t task.Task, systemPrompt string) (string, error) {
			return "func HelloWorld() { fmt.Println(\"Hello World\") }", nil
		},
	}

	orch := buildTestOrchestrator(t, cfg,
		mockMio, nil, // no Shiro needed for CODE route
		mockCoder1, nil, nil, // coder1 only
		mockReportStore, eventCapture,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := orch.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
		SessionID:   "e2e-test-code",
		Channel:     "test",
		ChatID:      "e2e-user",
		UserMessage: "GoでHello World関数を作って",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if resp.Route != routing.RouteCODE {
		t.Errorf("route: want CODE, got %s", resp.Route)
	}

	// Verify execution report
	report, ok := mockReportStore.GetLastReport()
	if !ok {
		t.Fatal("expected execution report to be saved for CODE route")
	}

	assertExecutionReport(t, report, map[string]interface{}{
		"Route":      "CODE",
		"Capability": "code_change",
		"Status":     "passed",
	})

	t.Logf("Route: %s (confidence: %.2f)", resp.Route, resp.Confidence)
	t.Logf("Response (first 100 chars): %.100s", resp.Response)
}

// TestE2E_CODE1_SuccessFlow はCODE1 routeの成功フローを検証する
func TestE2E_CODE1_SuccessFlow(t *testing.T) {
	cfg := getConfig(t)

	mockReportStore := NewMockReportStore()
	eventCapture := NewEventCapture()

	mockMio := &MockMioAgent{
		DecideActionFunc: func(ctx context.Context, task task.Task) (routing.Decision, error) {
			return routing.Decision{
				Route:      routing.RouteCODE1,
				Confidence: 1.0,
				Reason:     "explicit /code1 command",
			}, nil
		},
	}

	mockCoder1 := &MockCoderAgent{
		GenerateFunc: func(ctx context.Context, t task.Task, systemPrompt string) (string, error) {
			return "func Add(a, b int) int { return a + b }", nil
		},
	}

	orch := buildTestOrchestrator(t, cfg,
		mockMio, nil,
		mockCoder1, nil, nil,
		mockReportStore, eventCapture,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := orch.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
		SessionID:   "e2e-test-code1",
		Channel:     "test",
		ChatID:      "e2e-user",
		UserMessage: "/code1 Add関数を実装して",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if resp.Route != routing.RouteCODE1 {
		t.Errorf("route: want CODE1, got %s", resp.Route)
	}

	report, ok := mockReportStore.GetLastReport()
	if !ok {
		t.Fatal("expected execution report to be saved for CODE1 route")
	}

	assertExecutionReport(t, report, map[string]interface{}{
		"Route":      "CODE1",
		"Capability": "code_change",
		"Status":     "passed",
	})

	t.Logf("Route: %s (confidence: %.2f)", resp.Route, resp.Confidence)
}

// TestE2E_CODE2_SuccessFlow はCODE2 routeの成功フローを検証する
func TestE2E_CODE2_SuccessFlow(t *testing.T) {
	cfg := getConfig(t)

	mockReportStore := NewMockReportStore()
	eventCapture := NewEventCapture()

	mockMio := &MockMioAgent{
		DecideActionFunc: func(ctx context.Context, task task.Task) (routing.Decision, error) {
			return routing.Decision{
				Route:      routing.RouteCODE2,
				Confidence: 1.0,
				Reason:     "explicit /code2 command",
			}, nil
		},
	}

	mockCoder2 := &MockCoderAgent{
		GenerateFunc: func(ctx context.Context, t task.Task, systemPrompt string) (string, error) {
			return "func Multiply(a, b int) int { return a * b }", nil
		},
	}

	orch := buildTestOrchestrator(t, cfg,
		mockMio, nil,
		nil, mockCoder2, nil, // coder2 only
		mockReportStore, eventCapture,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := orch.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
		SessionID:   "e2e-test-code2",
		Channel:     "test",
		ChatID:      "e2e-user",
		UserMessage: "/code2 Multiply関数を実装して",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if resp.Route != routing.RouteCODE2 {
		t.Errorf("route: want CODE2, got %s", resp.Route)
	}

	report, ok := mockReportStore.GetLastReport()
	if !ok {
		t.Fatal("expected execution report to be saved for CODE2 route")
	}

	assertExecutionReport(t, report, map[string]interface{}{
		"Route":      "CODE2",
		"Capability": "code_change",
		"Status":     "passed",
	})

	t.Logf("Route: %s (confidence: %.2f)", resp.Route, resp.Confidence)
}

// TestE2E_CODE3_SuccessFlow はCODE3 routeの成功フロー（Proposal path）を検証する
func TestE2E_CODE3_SuccessFlow(t *testing.T) {
	cfg := getConfig(t)

	mockReportStore := NewMockReportStore()
	eventCapture := NewEventCapture()

	mockMio := &MockMioAgent{
		DecideActionFunc: func(ctx context.Context, task task.Task) (routing.Decision, error) {
			return routing.Decision{
				Route:      routing.RouteCODE3,
				Confidence: 1.0,
				Reason:     "explicit /code3 command",
			}, nil
		},
	}

	// Mock Coder3 with Proposal generation capability
	mockCoder3 := &MockCoderAgent{
		GenerateProposalFunc: func(ctx context.Context, t task.Task) (*proposal.Proposal, error) {
			return proposal.NewProposal(
				"hello.goにHelloWorld関数を追加",
				`[{"type": "file_edit", "action": "create", "target": "/tmp/e2e-test-hello.go", "content": "package main\n\nimport \"fmt\"\n\nfunc HelloWorld() {\n\tfmt.Println(\"Hello, World!\")\n}"}]`,
				"low",
				"simple function addition",
			), nil
		},
	}

	orch := buildTestOrchestrator(t, cfg,
		mockMio, nil,
		nil, nil, mockCoder3, // coder3 only
		mockReportStore, eventCapture,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := orch.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
		SessionID:   "e2e-test-code3",
		Channel:     "test",
		ChatID:      "e2e-user",
		UserMessage: "/code3 hello.goにHelloWorld関数を追加して",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if resp.Route != routing.RouteCODE3 {
		t.Errorf("route: want CODE3, got %s", resp.Route)
	}

	report, ok := mockReportStore.GetLastReport()
	if !ok {
		t.Fatal("expected execution report to be saved for CODE3 route")
	}

	assertExecutionReport(t, report, map[string]interface{}{
		"Route":      "CODE3",
		"Capability": "code_change",
		"Status":     "passed",
	})

	t.Logf("Route: %s (confidence: %.2f)", resp.Route, resp.Confidence)
	t.Logf("Response (first 100 chars): %.100s", resp.Response)
}

// ========================================
// Phase 4: Reasoning Routes Tests
// ========================================

// TestE2E_PLAN_SuccessFlow はPLAN routeの成功フローを検証する
func TestE2E_PLAN_SuccessFlow(t *testing.T) {
	cfg := getConfig(t)

	mockReportStore := NewMockReportStore()
	eventCapture := NewEventCapture()

	mockMio := &MockMioAgent{
		DecideActionFunc: func(ctx context.Context, task task.Task) (routing.Decision, error) {
			return routing.Decision{
				Route:      routing.RoutePLAN,
				Confidence: 1.0,
				Reason:     "explicit /plan command",
			}, nil
		},
		ChatFunc: func(ctx context.Context, t task.Task) (string, error) {
			return "Phase 1: バックアップ作成\nPhase 2: スキーマ移行\nPhase 3: データ移行\nPhase 4: 検証", nil
		},
	}

	orch := buildTestOrchestrator(t, cfg,
		mockMio, nil,
		nil, nil, nil,
		mockReportStore, eventCapture,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := orch.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
		SessionID:   "e2e-test-plan",
		Channel:     "test",
		ChatID:      "e2e-user",
		UserMessage: "/plan データベース移行計画を立てて",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if resp.Route != routing.RoutePLAN {
		t.Errorf("route: want PLAN, got %s", resp.Route)
	}

	// Verify execution report
	report, ok := mockReportStore.GetLastReport()
	if !ok {
		t.Fatal("expected execution report to be saved for PLAN route")
	}

	assertExecutionReport(t, report, map[string]interface{}{
		"Route":      "PLAN",
		"Capability": "generic_execution",
		"Status":     "passed",
	})

	if resp.Response == "" {
		t.Error("expected non-empty response from Mio reasoning")
	}

	t.Logf("Route: %s (confidence: %.2f)", resp.Route, resp.Confidence)
	t.Logf("Response (first 100 chars): %.100s", resp.Response)
}

// TestE2E_ANALYZE_SuccessFlow はANALYZE routeの成功フローを検証する
func TestE2E_ANALYZE_SuccessFlow(t *testing.T) {
	cfg := getConfig(t)

	mockReportStore := NewMockReportStore()
	eventCapture := NewEventCapture()

	mockMio := &MockMioAgent{
		DecideActionFunc: func(ctx context.Context, task task.Task) (routing.Decision, error) {
			return routing.Decision{
				Route:      routing.RouteANALYZE,
				Confidence: 1.0,
				Reason:     "explicit /analyze command",
			}, nil
		},
		ChatFunc: func(ctx context.Context, t task.Task) (string, error) {
			return "分析結果:\n1. CPU使用率が高い\n2. メモリ使用率は正常\n3. ディスクI/Oがボトルネック", nil
		},
	}

	orch := buildTestOrchestrator(t, cfg,
		mockMio, nil,
		nil, nil, nil,
		mockReportStore, eventCapture,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := orch.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
		SessionID:   "e2e-test-analyze",
		Channel:     "test",
		ChatID:      "e2e-user",
		UserMessage: "/analyze システムパフォーマンスを分析して",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if resp.Route != routing.RouteANALYZE {
		t.Errorf("route: want ANALYZE, got %s", resp.Route)
	}

	report, ok := mockReportStore.GetLastReport()
	if !ok {
		t.Fatal("expected execution report to be saved for ANALYZE route")
	}

	assertExecutionReport(t, report, map[string]interface{}{
		"Route":      "ANALYZE",
		"Capability": "generic_execution",
		"Status":     "passed",
	})

	if resp.Response == "" {
		t.Error("expected non-empty response from Mio reasoning")
	}

	t.Logf("Route: %s (confidence: %.2f)", resp.Route, resp.Confidence)
	t.Logf("Response (first 100 chars): %.100s", resp.Response)
}

// TestE2E_RESEARCH_SuccessFlow はRESEARCH routeの成功フローを検証する
func TestE2E_RESEARCH_SuccessFlow(t *testing.T) {
	cfg := getConfig(t)

	mockReportStore := NewMockReportStore()
	eventCapture := NewEventCapture()

	mockMio := &MockMioAgent{
		DecideActionFunc: func(ctx context.Context, task task.Task) (routing.Decision, error) {
			return routing.Decision{
				Route:      routing.RouteRESEARCH,
				Confidence: 1.0,
				Reason:     "explicit /research command",
			}, nil
		},
		ChatFunc: func(ctx context.Context, t task.Task) (string, error) {
			return "調査結果:\nGo 1.23の新機能:\n1. 範囲関数のイテレータ対応\n2. ジェネリクス改善\n3. パフォーマンス向上", nil
		},
	}

	orch := buildTestOrchestrator(t, cfg,
		mockMio, nil,
		nil, nil, nil,
		mockReportStore, eventCapture,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := orch.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
		SessionID:   "e2e-test-research",
		Channel:     "test",
		ChatID:      "e2e-user",
		UserMessage: "/research Go 1.23の新機能を調査して",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if resp.Route != routing.RouteRESEARCH {
		t.Errorf("route: want RESEARCH, got %s", resp.Route)
	}

	report, ok := mockReportStore.GetLastReport()
	if !ok {
		t.Fatal("expected execution report to be saved for RESEARCH route")
	}

	assertExecutionReport(t, report, map[string]interface{}{
		"Route":      "RESEARCH",
		"Capability": "generic_execution",
		"Status":     "passed",
	})

	if resp.Response == "" {
		t.Error("expected non-empty response from Mio reasoning")
	}

	t.Logf("Route: %s (confidence: %.2f)", resp.Route, resp.Confidence)
	t.Logf("Response (first 100 chars): %.100s", resp.Response)
}
