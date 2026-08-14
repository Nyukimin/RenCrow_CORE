package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	complexityapp "github.com/Nyukimin/RenCrow_CORE/internal/application/complexity"
	domaincomplexity "github.com/Nyukimin/RenCrow_CORE/internal/domain/complexity"
	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	complexitypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/complexity"
	sandboxpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/sandbox"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestRuntimeDataWriteSandboxPromotionGateOwnerE2E(t *testing.T) {
	root := t.TempDir()
	store := seedRuntimeSandboxPromotionStore(t, root, domainsandbox.SandboxStatusActive)
	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteSandbox(registry, store); err != nil {
		t.Fatalf("register sandbox write: %v", err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
	ctx := runtimeDataWriteDContext(t, "sandbox-owner-1")
	payload := map[string]any{
		"sandbox_id":                          "sandbox-owner-1",
		"target_artifact_id":                  "artifact-target-1",
		"diff_artifact_id":                    "artifact-diff-1",
		"test_result_artifact_id":             "artifact-test-1",
		"rollback_plan_artifact_id":           "artifact-rollback-1",
		"post_apply_verification_artifact_id": "artifact-post-1",
		"risk_level":                          "medium",
		"reason":                              "promote the reviewed file change",
	}

	first := runtimeDataWriteDExecuteWrite(t, worker, ctx, "sandbox", "create_promotion_gate", payload)
	if first.IdempotentReplay || first.SchemaVersion != "sandbox-promotion-gate/v1" || first.MigrationState != "embedded_current" || first.ValidationState != "owner_validated" || first.AuditRef == "" || first.IdempotencyKey != "sandbox-owner-1" || first.DataScope != string(dataRecallAccessInternal) {
		t.Fatalf("first sandbox receipt = %#v", first)
	}
	expectedPromotionID := runtimeDataWriteDerivedID(runtimeSandboxPromotionIDPrefix, "sandbox-owner-1")
	if first.OwnerRoute != "sandbox/create_promotion_gate" || first.AuditRef != runtimeDataWriteDerivedID(runtimeSandboxPromotionGateIDPrefix, "sandbox-owner-1") {
		t.Fatalf("first sandbox route receipt = %#v", first)
	}
	requests, err := store.ListPromotionRequests(ctx, 10)
	if err != nil || len(requests) != 1 {
		t.Fatalf("promotion requests = %#v, err=%v", requests, err)
	}
	promotion := requests[0]
	if promotion.PromotionID != expectedPromotionID || promotion.SandboxID != "sandbox-owner-1" || promotion.WorkstreamID != "workstream-owner-1" || promotion.GoalID != "goal-owner-1" || promotion.RequestedBy != "shiro" || promotion.TargetPath != "workspace/sandbox-owner-1/target.go" || promotion.DiffPath != "workspace/sandbox-owner-1/change.diff" || promotion.TestResultPath != "workspace/sandbox-owner-1/test.txt" || promotion.RollbackPlanPath != "workspace/sandbox-owner-1/rollback.md" || promotion.PostApplyVerificationPath != "workspace/sandbox-owner-1/post-apply.txt" || promotion.RiskLevel != "medium" || promotion.Reason != "promote the reviewed file change" {
		t.Fatalf("derived promotion = %#v", promotion)
	}
	logs, err := store.ListPromotionGateLogs(ctx, 10)
	if err != nil || len(logs) != 1 {
		t.Fatalf("promotion gate logs = %#v, err=%v", logs, err)
	}
	if logs[0].EventID != first.AuditRef || logs[0].PromotionID != expectedPromotionID || logs[0].GateStatus != domainsandbox.GateStatusPassed || logs[0].PostApplyVerification != "workspace/sandbox-owner-1/post-apply.txt" {
		t.Fatalf("derived promotion gate = %#v", logs[0])
	}

	replay := runtimeDataWriteDExecuteWrite(t, worker, ctx, "sandbox", "create_promotion_gate", payload)
	if !replay.IdempotentReplay || replay.AuditRef != first.AuditRef {
		t.Fatalf("sandbox replay receipt = %#v, first=%#v", replay, first)
	}

	if err := os.WriteFile(filepath.Join(root, "promotion_gate_log.jsonl"), nil, 0644); err != nil {
		t.Fatalf("remove persisted gate for retry: %v", err)
	}
	finishedGate := runtimeDataWriteDExecuteWrite(t, worker, ctx, "sandbox", "create_promotion_gate", payload)
	if finishedGate.IdempotentReplay || finishedGate.AuditRef != first.AuditRef {
		t.Fatalf("missing-gate retry receipt = %#v", finishedGate)
	}
	logs, err = store.ListPromotionGateLogs(ctx, 10)
	if err != nil || len(logs) != 1 || logs[0].EventID != first.AuditRef {
		t.Fatalf("finished promotion gate logs = %#v, err=%v", logs, err)
	}

	changed := runtimeDataWriteDClonePayload(payload)
	changed["reason"] = "different reason"
	if response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "sandbox", "operation": "create_promotion_gate", "payload": changed}); err != nil || response == nil || !response.IsError() {
		t.Fatalf("sandbox conflict response=%#v err=%v", response, err)
	}
	requests, err = store.ListPromotionRequests(ctx, 10)
	if err != nil || len(requests) != 1 {
		t.Fatalf("sandbox conflict mutated requests=%#v err=%v", requests, err)
	}
}

func TestRuntimeDataWriteSandboxPromotionGateRejectsInvalidInputsWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		status string
		mutate func(map[string]any)
	}{
		{name: "missing artifact", status: domainsandbox.SandboxStatusActive, mutate: func(payload map[string]any) { payload["target_artifact_id"] = "missing-artifact" }},
		{name: "wrong artifact type", status: domainsandbox.SandboxStatusActive, mutate: func(payload map[string]any) { payload["target_artifact_id"] = "artifact-diff-1" }},
		{name: "inactive sandbox", status: domainsandbox.SandboxStatusClosed, mutate: func(map[string]any) {}},
		{name: "cross sandbox artifact", status: domainsandbox.SandboxStatusActive, mutate: func(payload map[string]any) { payload["target_artifact_id"] = "artifact-cross-1" }},
		{name: "forbidden path", status: domainsandbox.SandboxStatusActive, mutate: func(payload map[string]any) { payload["target_path"] = "workspace/target.go" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store := seedRuntimeSandboxPromotionStore(t, root, test.status)
			if test.name == "cross sandbox artifact" {
				cross := domainsandbox.SandboxArtifact{
					ArtifactID: "artifact-cross-1", SandboxID: "other-sandbox", Type: "target_file", FilePath: "workspace/other/target.go", Status: "ready", CreatedAt: time.Now().UTC(),
				}
				if err := store.SaveSandboxArtifact(context.Background(), cross); err != nil {
					t.Fatalf("seed cross-sandbox artifact: %v", err)
				}
			}
			registry := newRuntimeDataWriteRegistry()
			if err := registerRuntimeDataWriteSandbox(registry, store); err != nil {
				t.Fatalf("register sandbox write: %v", err)
			}
			worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
			ctx := runtimeDataWriteDContext(t, "sandbox-invalid-"+test.name)
			payload := runtimeDataWriteDClonePayload(runtimeDataWriteSandboxPayload())
			test.mutate(payload)
			response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "sandbox", "operation": "create_promotion_gate", "payload": payload})
			if err != nil || response == nil || !response.IsError() {
				t.Fatalf("invalid sandbox response=%#v err=%v", response, err)
			}
			requests, requestErr := store.ListPromotionRequests(context.Background(), 10)
			logs, logErr := store.ListPromotionGateLogs(context.Background(), 10)
			if requestErr != nil || logErr != nil || len(requests) != 0 || len(logs) != 0 {
				t.Fatalf("invalid sandbox mutated requests=%#v logs=%#v requestErr=%v logErr=%v", requests, logs, requestErr, logErr)
			}
		})
	}
}

func TestRuntimeDataWriteComplexityConcreteDiffReviewOwnerE2E(t *testing.T) {
	root := t.TempDir()
	store := complexitypersistence.NewJSONLStore(root)
	hotspot := runtimeDataWriteComplexityHotspot()
	if err := store.SaveHotspot(context.Background(), hotspot); err != nil {
		t.Fatalf("seed complexity hotspot: %v", err)
	}
	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteComplexityHotspot(registry, store); err != nil {
		t.Fatalf("register complexity write: %v", err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
	ctx := runtimeDataWriteDContext(t, "complexity-owner-1")
	diff := runtimeDataWriteComplexityDiff()
	payload := map[string]any{"hotspot_id": "hotspot-owner-1", "concrete_diff": diff}

	first := runtimeDataWriteDExecuteWrite(t, worker, ctx, "complexity_hotspot", "record_concrete_diff_review", payload)
	if first.IdempotentReplay || first.SchemaVersion != "complexity-concrete-diff-review/v1" || first.MigrationState != "embedded_current" || first.ValidationState != "owner_validated" || first.AuditRef == "" || first.IdempotencyKey != "complexity-owner-1" || first.OwnerRoute != "complexity_hotspot/record_concrete_diff_review" || first.DataScope != string(dataRecallAccessInternal) {
		t.Fatalf("first complexity receipt = %#v", first)
	}
	reports, err := store.ListReportArtifacts(ctx, 10)
	if err != nil || len(reports) != 1 {
		t.Fatalf("complexity reports = %#v, err=%v", reports, err)
	}
	report := reports[0]
	if report.ArtifactID != runtimeDataWriteDerivedID(runtimeComplexityReviewIDPrefix, "complexity-owner-1") || report.ScanID != hotspot.ScanID || report.WorkstreamID != "" || report.Type != "complexity_concrete_diff_review" || report.Title != "Concrete diff review: hotspot-owner-1" || report.Status != "generated" {
		t.Fatalf("derived complexity report = %#v", report)
	}
	if want := complexityReportContent(hotspot, diff); report.Content != want {
		t.Fatalf("complexity report content mismatch\nwant:\n%s\ngot:\n%s", want, report.Content)
	}

	replay := runtimeDataWriteDExecuteWrite(t, worker, ctx, "complexity_hotspot", "record_concrete_diff_review", payload)
	if !replay.IdempotentReplay || replay.AuditRef != first.AuditRef {
		t.Fatalf("complexity replay receipt = %#v, first=%#v", replay, first)
	}
	changed := runtimeDataWriteDClonePayload(payload)
	changed["concrete_diff"] = runtimeDataWriteComplexityDiffChanged()
	if response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "complexity_hotspot", "operation": "record_concrete_diff_review", "payload": changed}); err != nil || response == nil || !response.IsError() {
		t.Fatalf("complexity conflict response=%#v err=%v", response, err)
	}
	reports, err = store.ListReportArtifacts(ctx, 10)
	if err != nil || len(reports) != 1 {
		t.Fatalf("complexity conflict mutated reports=%#v err=%v", reports, err)
	}

	invalidCases := []struct {
		name    string
		request string
		mutate  func(map[string]any)
	}{
		{name: "wrong file", request: "complexity-wrong-file", mutate: func(payload map[string]any) { payload["concrete_diff"] = runtimeDataWriteComplexityWrongFileDiff() }},
		{name: "missing hotspot", request: "complexity-missing-hotspot", mutate: func(payload map[string]any) { payload["hotspot_id"] = "missing-hotspot" }},
		{name: "forbidden fields", request: "complexity-forbidden-fields", mutate: func(payload map[string]any) {
			payload["sandbox_id"] = "model-owned"
			payload["file_path"] = "workspace/target.go"
		}},
	}
	for _, test := range invalidCases {
		invalidPayload := runtimeDataWriteDClonePayload(payload)
		test.mutate(invalidPayload)
		invalidCtx := runtimeDataWriteDContext(t, test.request)
		response, responseErr := worker.ExecuteV2(invalidCtx, "data.write", map[string]any{"store": "complexity_hotspot", "operation": "record_concrete_diff_review", "payload": invalidPayload})
		if responseErr != nil || response == nil || !response.IsError() {
			t.Fatalf("invalid complexity %s response=%#v err=%v", test.name, response, responseErr)
		}
		reports, err = store.ListReportArtifacts(ctx, 10)
		if err != nil || len(reports) != 1 {
			t.Fatalf("invalid complexity %s mutated reports=%#v err=%v", test.name, reports, err)
		}
	}
}

func TestRuntimeDataWriteSandboxAndComplexitySnapshotContracts(t *testing.T) {
	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteSandbox(registry, seedRuntimeSandboxPromotionStore(t, t.TempDir(), domainsandbox.SandboxStatusActive)); err != nil {
		t.Fatalf("register sandbox write: %v", err)
	}
	if err := registerRuntimeDataWriteComplexityHotspot(registry, complexitypersistence.NewJSONLStore(t.TempDir())); err != nil {
		t.Fatalf("register complexity write: %v", err)
	}
	routes := registry.Snapshot()
	if len(routes) != 2 {
		t.Fatalf("write routes = %#v", routes)
	}
	var sandboxRoute, complexityRoute *runtimeDataWriteRoute
	for i := range routes {
		switch routes[i].Store {
		case "sandbox":
			sandboxRoute = &routes[i]
		case "complexity_hotspot":
			complexityRoute = &routes[i]
		}
	}
	if sandboxRoute == nil || sandboxRoute.Operation != "create_promotion_gate" || sandboxRoute.Access != dataRecallAccessInternal || !reflect.DeepEqual(sandboxRoute.RequiredPayloadFields, []string{"diff_artifact_id", "reason", "rollback_plan_artifact_id", "sandbox_id", "target_artifact_id", "test_result_artifact_id"}) || !reflect.DeepEqual(sandboxRoute.OptionalPayloadFields, []string{"post_apply_verification_artifact_id", "risk_level"}) {
		t.Fatalf("sandbox route contract = %#v", sandboxRoute)
	}
	if complexityRoute == nil || complexityRoute.Operation != "record_concrete_diff_review" || complexityRoute.Access != dataRecallAccessInternal || !reflect.DeepEqual(complexityRoute.RequiredPayloadFields, []string{"concrete_diff", "hotspot_id"}) || len(complexityRoute.OptionalPayloadFields) != 0 {
		t.Fatalf("complexity route contract = %#v", complexityRoute)
	}
}

func seedRuntimeSandboxPromotionStore(t *testing.T, root, status string) *sandboxpersistence.JSONLStore {
	t.Helper()
	store := sandboxpersistence.NewJSONLStore(root)
	now := time.Date(2026, 8, 14, 4, 0, 0, 0, time.UTC)
	sandbox := domainsandbox.SandboxRecord{
		SandboxID: "sandbox-owner-1", WorkstreamID: "workstream-owner-1", GoalID: "goal-owner-1", Type: "code", Path: "workspace/sandbox-owner-1", Status: status, CreatedAt: now,
	}
	if err := store.SaveSandbox(context.Background(), sandbox); err != nil {
		t.Fatalf("seed sandbox: %v", err)
	}
	artifacts := []domainsandbox.SandboxArtifact{
		{ArtifactID: "artifact-target-1", SandboxID: sandbox.SandboxID, Type: "target_file", FilePath: "workspace/sandbox-owner-1/target.go", Title: "target", Status: "ready", CreatedAt: now},
		{ArtifactID: "artifact-diff-1", SandboxID: sandbox.SandboxID, Type: "diff", FilePath: "workspace/sandbox-owner-1/change.diff", Title: "diff", Status: "ready", CreatedAt: now},
		{ArtifactID: "artifact-test-1", SandboxID: sandbox.SandboxID, Type: "test_result", FilePath: "workspace/sandbox-owner-1/test.txt", Title: "test", Status: "passed", CreatedAt: now},
		{ArtifactID: "artifact-rollback-1", SandboxID: sandbox.SandboxID, Type: "rollback_plan", FilePath: "workspace/sandbox-owner-1/rollback.md", Title: "rollback", Status: "ready", CreatedAt: now},
		{ArtifactID: "artifact-post-1", SandboxID: sandbox.SandboxID, Type: "post_apply_verification", FilePath: "workspace/sandbox-owner-1/post-apply.txt", Title: "post apply", Status: "ready", CreatedAt: now},
	}
	for _, artifact := range artifacts {
		if err := store.SaveSandboxArtifact(context.Background(), artifact); err != nil {
			t.Fatalf("seed sandbox artifact %q: %v", artifact.ArtifactID, err)
		}
	}
	return store
}

func runtimeDataWriteSandboxPayload() map[string]any {
	return map[string]any{
		"sandbox_id": "sandbox-owner-1", "target_artifact_id": "artifact-target-1", "diff_artifact_id": "artifact-diff-1",
		"test_result_artifact_id": "artifact-test-1", "rollback_plan_artifact_id": "artifact-rollback-1", "reason": "promote the reviewed file change",
	}
}

func runtimeDataWriteComplexityHotspot() domaincomplexity.Hotspot {
	return domaincomplexity.Hotspot{
		HotspotID: "hotspot-owner-1", ScanID: "scan-owner-1", FilePath: "internal/application/example.go", LineStart: 1, LineEnd: 3,
		HotspotType: "nested_loop", EstimatedComplexity: "O(n^2)", RiskLevel: "medium", Summary: "nested lookup", CreatedAt: time.Date(2026, 8, 14, 4, 0, 0, 0, time.UTC),
	}
}

func runtimeDataWriteComplexityDiff() string {
	return `--- a/internal/application/example.go
+++ b/internal/application/example.go
@@ -1,3 +1,3 @@
-old
+new
`
}

func runtimeDataWriteComplexityDiffChanged() string {
	return `--- a/internal/application/example.go
+++ b/internal/application/example.go
@@ -1,3 +1,3 @@
-old
+changed
`
}

func runtimeDataWriteComplexityWrongFileDiff() string {
	return `--- a/internal/application/other.go
+++ b/internal/application/other.go
@@ -1,3 +1,3 @@
-old
+new
`
}

func complexityReportContent(hotspot domaincomplexity.Hotspot, diff string) string {
	return complexityapp.BuildConcreteDiffProposalMarkdown(hotspot, diff, "", "")
}

func runtimeDataWriteDExecuteWrite(t *testing.T, worker *toolsinfra.ToolRunner, ctx context.Context, store, operation string, payload map[string]any) runtimeDataWriteReceipt {
	t.Helper()
	response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": store, "operation": operation, "payload": payload})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("data.write response=%#v err=%v", response, err)
	}
	receipt, ok := response.Result.(runtimeDataWriteReceipt)
	if !ok {
		t.Fatalf("data.write result type=%T value=%#v", response.Result, response.Result)
	}
	return receipt
}

func runtimeDataWriteDContext(t *testing.T, requestID string) context.Context {
	t.Helper()
	scope := domaintool.ToolExecutionScope{
		RequestID: requestID, ActorKind: domaintool.ActorKindAgent, ActorID: "shiro", AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator,
		AgentRole: "worker", Purpose: "ops", AllowedDataScopes: []string{domaintool.DataScopeInternal},
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("internal scope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func runtimeDataWriteDClonePayload(payload map[string]any) map[string]any {
	clone := make(map[string]any, len(payload))
	for key, value := range payload {
		clone[key] = value
	}
	return clone
}
