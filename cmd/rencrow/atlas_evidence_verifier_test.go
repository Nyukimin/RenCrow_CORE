package main

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	debug "runtime/debug"
	"strings"
	"testing"
	"time"

	backlogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	backlogfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/backlog"
	executionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/execution"
)

func TestAtlasEvidenceVerifierEmbeddedSpecificationRejectsWrongMetadata(t *testing.T) {
	pkg, err := backlogfeature.LoadBackfillPackage()
	if err != nil {
		t.Fatal(err)
	}
	artifact, ok := pkg.Specification("spec_atlas_lifecycle_functional_v1")
	if !ok {
		t.Fatal("functional Atlas specification fixture is missing")
	}
	verifier, err := newAtlasEvidenceVerifierWithOptions(atlasEvidenceVerifierOptions{
		ReceiptPath:    filepath.Join(t.TempDir(), "binary-redeployment.jsonl"),
		Specifications: []domainbacklog.SpecificationArtifact{artifact},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	valid := domainbacklog.EvidenceRef{
		Kind:       "spec",
		Ref:        artifact.SpecID,
		Revision:   "1",
		SHA256:     artifact.ContentSHA256,
		ObservedAt: artifact.CapturedAt,
		Passed:     true,
		Verified:   true,
	}
	verificationRequest := backlogapp.EvidenceVerificationRequest{
		Ref: valid, ItemID: "item-spec", ImplementationUnitID: "unit-spec", ImplementationRevision: 1,
		TargetDeliveryState: domainbacklog.DeliverySpec,
	}
	if ok, err := verifier.Verify(ctx, verificationRequest); err != nil || !ok {
		t.Fatalf("valid embedded specification = %v, %v", ok, err)
	}
	for name, mutate := range map[string]func(*domainbacklog.EvidenceRef){
		"wrong hash":            func(ref *domainbacklog.EvidenceRef) { ref.SHA256 = strings.Repeat("0", 64) },
		"wrong revision":        func(ref *domainbacklog.EvidenceRef) { ref.Revision = "2" },
		"stale observation":     func(ref *domainbacklog.EvidenceRef) { ref.ObservedAt = "2020-01-01T00:00:00Z" },
		"unknown specification": func(ref *domainbacklog.EvidenceRef) { ref.Ref = "spec_missing" },
	} {
		request := verificationRequest
		ref := valid
		mutate(&ref)
		request.Ref = ref
		if ok, err := verifier.Verify(ctx, request); err == nil || ok {
			t.Fatalf("%s unexpectedly verified: %v, %v", name, ok, err)
		}
	}
}

func TestAtlasEvidenceVerifierExecutionReportRequiresExactSuccessfulFinishedReport(t *testing.T) {
	root := t.TempDir()
	store, err := executionpersistence.NewJSONLReportStore(filepath.Join(root, "execution_report.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	finished := time.Date(2026, 8, 22, 10, 11, 12, 0, time.UTC)
	stage := domainbacklog.DeliveryTDDGreen
	sourceRevision := strings.Repeat("a", 40)
	if err := store.Save(context.Background(), domainexecution.ExecutionReport{
		JobID: "job-pass", Goal: "focused test", Status: "passed", CreatedAt: finished.Add(-time.Minute), FinishedAt: finished,
		Verification: []string{
			"atlas.item=item-execution", "atlas.unit=unit-pass", "atlas.implementation_revision=2",
			"atlas.stage=" + stage, "atlas.source_revision=" + sourceRevision,
		},
	}); err != nil {
		t.Fatal(err)
	}
	pkg, err := backlogfeature.LoadBackfillPackage()
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := newAtlasEvidenceVerifierWithOptions(atlasEvidenceVerifierOptions{
		ReportStore: store, ReceiptPath: filepath.Join(root, "receipts.jsonl"), Specifications: pkg.SpecificationArtifacts,
	})
	if err != nil {
		t.Fatal(err)
	}
	valid := domainbacklog.EvidenceRef{
		Stage: stage, Kind: "execution_report", Ref: "execution_report:job-pass", Repository: domainbacklog.LifecycleOwnerModule,
		Revision: sourceRevision, ObservedAt: finished.Format(time.RFC3339Nano), Passed: true, Verified: true,
	}
	verificationRequest := backlogapp.EvidenceVerificationRequest{
		Ref: valid, ItemID: "item-execution", ImplementationUnitID: "unit-pass", ImplementationRevision: 2,
		TargetDeliveryState: stage,
	}
	if ok, err := verifier.Verify(context.Background(), verificationRequest); err != nil || !ok {
		t.Fatalf("valid execution report = %v, %v", ok, err)
	}
	for name, mutate := range map[string]func(*domainbacklog.EvidenceRef){
		"stale observation": func(ref *domainbacklog.EvidenceRef) {
			ref.ObservedAt = finished.Add(-time.Hour).Format(time.RFC3339Nano)
		},
		"unknown job":        func(ref *domainbacklog.EvidenceRef) { ref.Ref = "execution_report:job-missing" },
		"missing repository": func(ref *domainbacklog.EvidenceRef) { ref.Repository = "" },
		"wrong repository":   func(ref *domainbacklog.EvidenceRef) { ref.Repository = "other-repository" },
		"missing source":     func(ref *domainbacklog.EvidenceRef) { ref.Revision = "" },
		"wrong source":       func(ref *domainbacklog.EvidenceRef) { ref.Revision = strings.Repeat("b", 40) },
		"non-BUILD hash":     func(ref *domainbacklog.EvidenceRef) { ref.SHA256 = strings.Repeat("c", 64) },
		"cross-stage":        func(ref *domainbacklog.EvidenceRef) { ref.Stage = domainbacklog.DeliveryBuild },
	} {
		request := verificationRequest
		ref := valid
		mutate(&ref)
		request.Ref = ref
		if ok, err := verifier.Verify(context.Background(), request); err == nil || ok {
			t.Fatalf("%s unexpectedly verified: %v, %v", name, ok, err)
		}
	}
	for name, mutate := range map[string]func(*backlogapp.EvidenceVerificationRequest){
		"cross-item":     func(request *backlogapp.EvidenceVerificationRequest) { request.ItemID = "item-other" },
		"cross-unit":     func(request *backlogapp.EvidenceVerificationRequest) { request.ImplementationUnitID = "unit-other" },
		"cross-revision": func(request *backlogapp.EvidenceVerificationRequest) { request.ImplementationRevision = 3 },
		"cross-stage-context": func(request *backlogapp.EvidenceVerificationRequest) {
			request.TargetDeliveryState = domainbacklog.DeliveryBuild
		},
	} {
		request := verificationRequest
		mutate(&request)
		if ok, err := verifier.Verify(context.Background(), request); err == nil || ok {
			t.Fatalf("%s unexpectedly verified: %v, %v", name, ok, err)
		}
	}
}

func TestAtlasEvidenceVerifierExecutionReportRequiresExactMachineMarkers(t *testing.T) {
	sourceRevision := strings.Repeat("d", 40)
	stage := domainbacklog.DeliveryTDDGreen
	validRequest := atlasExecutionFixtureRequest(stage, sourceRevision, "")
	validReport := atlasExecutionFixtureReport(stage, sourceRevision)
	verifier := newAtlasExecutionFixtureVerifier(t, validReport)
	if ok, err := verifier.Verify(context.Background(), validRequest); err != nil || !ok {
		t.Fatalf("valid machine markers = %v, %v", ok, err)
	}

	for name, mutate := range map[string]func(*domainexecution.ExecutionReport){
		"missing item marker": func(report *domainexecution.ExecutionReport) {
			removeAtlasFixtureMarker(report, "atlas.item")
		},
		"wrong item marker": func(report *domainexecution.ExecutionReport) {
			setAtlasFixtureMarker(report, "atlas.item", "item-other")
		},
		"missing implementation revision marker": func(report *domainexecution.ExecutionReport) {
			removeAtlasFixtureMarker(report, "atlas.implementation_revision")
		},
		"wrong implementation revision marker": func(report *domainexecution.ExecutionReport) {
			setAtlasFixtureMarker(report, "atlas.implementation_revision", "8")
		},
		"missing stage marker": func(report *domainexecution.ExecutionReport) {
			removeAtlasFixtureMarker(report, "atlas.stage")
		},
		"wrong stage marker": func(report *domainexecution.ExecutionReport) {
			setAtlasFixtureMarker(report, "atlas.stage", domainbacklog.DeliveryBuild)
		},
		"missing source marker": func(report *domainexecution.ExecutionReport) {
			removeAtlasFixtureMarker(report, "atlas.source_revision")
		},
		"short source marker": func(report *domainexecution.ExecutionReport) {
			setAtlasFixtureMarker(report, "atlas.source_revision", "deadbeef")
		},
	} {
		report := validReport
		mutate(&report)
		candidate := newAtlasExecutionFixtureVerifier(t, report)
		if ok, err := candidate.Verify(context.Background(), validRequest); err == nil || ok {
			t.Fatalf("%s unexpectedly verified: %v, %v", name, ok, err)
		}
	}
}

func TestAtlasEvidenceVerifierExecutionReportRedRequiresObservedMarker(t *testing.T) {
	sourceRevision := strings.Repeat("e", 40)
	stage := domainbacklog.DeliveryTDDRed
	report := atlasExecutionFixtureReport(stage, sourceRevision)
	request := atlasExecutionFixtureRequest(stage, sourceRevision, "")
	verifier := newAtlasExecutionFixtureVerifier(t, report)
	if ok, err := verifier.Verify(context.Background(), request); err == nil || ok {
		t.Fatalf("RED report without observed marker unexpectedly verified: %v, %v", ok, err)
	}

	setAtlasFixtureMarker(&report, "atlas.red_observed", "true")
	verifier = newAtlasExecutionFixtureVerifier(t, report)
	if ok, err := verifier.Verify(context.Background(), request); err != nil || !ok {
		t.Fatalf("RED report with observed marker = %v, %v", ok, err)
	}

	setAtlasFixtureMarker(&report, "atlas.red_observed", "false")
	verifier = newAtlasExecutionFixtureVerifier(t, report)
	if ok, err := verifier.Verify(context.Background(), request); err == nil || ok {
		t.Fatalf("RED report with false observed marker unexpectedly verified: %v, %v", ok, err)
	}
}

func TestAtlasEvidenceVerifierExecutionReportBuildRequiresArtifactHashMarker(t *testing.T) {
	sourceRevision := strings.Repeat("f", 40)
	artifactHash := strings.Repeat("1", 64)
	stage := domainbacklog.DeliveryBuild
	request := atlasExecutionFixtureRequest(stage, sourceRevision, artifactHash)
	report := atlasExecutionFixtureReport(stage, sourceRevision)
	verifier := newAtlasExecutionFixtureVerifier(t, report)
	if ok, err := verifier.Verify(context.Background(), request); err == nil || ok {
		t.Fatalf("BUILD report without artifact marker unexpectedly verified: %v, %v", ok, err)
	}

	setAtlasFixtureMarker(&report, "atlas.artifact.sha256", artifactHash)
	verifier = newAtlasExecutionFixtureVerifier(t, report)
	if ok, err := verifier.Verify(context.Background(), request); err != nil || !ok {
		t.Fatalf("BUILD report with artifact marker = %v, %v", ok, err)
	}

	setAtlasFixtureMarker(&report, "atlas.artifact.sha256", strings.Repeat("2", 64))
	verifier = newAtlasExecutionFixtureVerifier(t, report)
	if ok, err := verifier.Verify(context.Background(), request); err == nil || ok {
		t.Fatalf("BUILD report with mismatched artifact marker unexpectedly verified: %v, %v", ok, err)
	}

	missingHash := request
	missingHash.Ref.SHA256 = ""
	verifier = newAtlasExecutionFixtureVerifier(t, report)
	if ok, err := verifier.Verify(context.Background(), missingHash); err == nil || ok {
		t.Fatalf("BUILD report without requested artifact hash unexpectedly verified: %v, %v", ok, err)
	}
}

type atlasExecutionFixtureStore struct {
	report domainexecution.ExecutionReport
}

func (s atlasExecutionFixtureStore) GetByJobID(_ context.Context, jobID string) (domainexecution.ExecutionReport, error) {
	if s.report.JobID != jobID {
		return domainexecution.ExecutionReport{}, os.ErrNotExist
	}
	return s.report, nil
}

func newAtlasExecutionFixtureVerifier(t *testing.T, report domainexecution.ExecutionReport) *atlasEvidenceVerifier {
	t.Helper()
	pkg, err := backlogfeature.LoadBackfillPackage()
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := newAtlasEvidenceVerifierWithOptions(atlasEvidenceVerifierOptions{
		ReportStore: atlasExecutionFixtureStore{report: report}, ReceiptPath: filepath.Join(t.TempDir(), "receipts.jsonl"),
		Specifications: pkg.SpecificationArtifacts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func atlasExecutionFixtureReport(stage, sourceRevision string) domainexecution.ExecutionReport {
	finished := time.Date(2026, 8, 22, 14, 15, 16, 0, time.UTC)
	return domainexecution.ExecutionReport{
		JobID: "job-fixture", Goal: "Atlas execution fixture", Status: "passed",
		CreatedAt: finished.Add(-time.Minute), FinishedAt: finished,
		Verification: []string{
			"atlas.item=item-execution", "atlas.unit=unit-execution", "atlas.implementation_revision=7",
			"atlas.stage=" + stage, "atlas.source_revision=" + sourceRevision,
		},
	}
}

func atlasExecutionFixtureRequest(stage, sourceRevision, artifactHash string) backlogapp.EvidenceVerificationRequest {
	return backlogapp.EvidenceVerificationRequest{
		Ref: domainbacklog.EvidenceRef{
			Stage: stage, Kind: "execution_report", Ref: "execution_report:job-fixture",
			Repository: domainbacklog.LifecycleOwnerModule, Revision: sourceRevision, SHA256: artifactHash,
		},
		ItemID: "item-execution", ImplementationUnitID: "unit-execution", ImplementationRevision: 7,
		TargetDeliveryState: stage,
	}
}

func setAtlasFixtureMarker(report *domainexecution.ExecutionReport, key, value string) {
	prefix := key + "="
	for index, entry := range report.Verification {
		if strings.HasPrefix(strings.TrimSpace(entry), prefix) {
			report.Verification[index] = prefix + value
			return
		}
	}
	report.Verification = append(report.Verification, prefix+value)
}

func removeAtlasFixtureMarker(report *domainexecution.ExecutionReport, key string) {
	prefix := key + "="
	filtered := report.Verification[:0]
	for _, entry := range report.Verification {
		if !strings.HasPrefix(strings.TrimSpace(entry), prefix) {
			filtered = append(filtered, entry)
		}
	}
	report.Verification = filtered
}

func TestAtlasEvidenceVerifierDeploymentReceiptChecksOwnerAndTarget(t *testing.T) {
	root := t.TempDir()
	receiptPath := filepath.Join(root, "binary-redeployment.jsonl")
	finished := time.Date(2026, 8, 22, 11, 12, 13, 0, time.UTC)
	receipt := map[string]any{
		"schema_version": 1, "receipt_id": "receipt-core-1", "component": "core",
		"target_revision": strings.Repeat("a", 40), "phase": "complete", "outcome": "success",
		"finished_at": finished.Format(time.RFC3339Nano), "binary_path": "",
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiptPath, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	pkg, err := backlogfeature.LoadBackfillPackage()
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := newAtlasEvidenceVerifierWithOptions(atlasEvidenceVerifierOptions{
		ReceiptPath: receiptPath, Specifications: pkg.SpecificationArtifacts,
	})
	if err != nil {
		t.Fatal(err)
	}
	valid := domainbacklog.EvidenceRef{Stage: domainbacklog.DeliveryDeploy, Kind: "deploy_receipt", Ref: "receipt-core-1", Revision: strings.Repeat("a", 40), ObservedAt: finished.Format(time.RFC3339Nano), Passed: true, Verified: true}
	verificationRequest := backlogapp.EvidenceVerificationRequest{
		Ref: valid, ItemID: "item-deploy", ImplementationUnitID: "unit-deploy", ImplementationRevision: 3,
		TargetDeliveryState: domainbacklog.DeliveryDeploy,
	}
	if ok, err := verifier.Verify(context.Background(), verificationRequest); err != nil || !ok {
		t.Fatalf("valid deployment receipt = %v, %v", ok, err)
	}
	for name, mutate := range map[string]func(*domainbacklog.EvidenceRef){
		"wrong target revision": func(ref *domainbacklog.EvidenceRef) { ref.Revision = strings.Repeat("b", 40) },
		"stale observation": func(ref *domainbacklog.EvidenceRef) {
			ref.ObservedAt = finished.Add(-time.Hour).Format(time.RFC3339Nano)
		},
		"unknown receipt":  func(ref *domainbacklog.EvidenceRef) { ref.Ref = "receipt-missing" },
		"unsupported kind": func(ref *domainbacklog.EvidenceRef) { ref.Kind = "arbitrary_path" },
	} {
		request := verificationRequest
		ref := valid
		mutate(&ref)
		request.Ref = ref
		if ok, err := verifier.Verify(context.Background(), request); err == nil || ok {
			t.Fatalf("%s unexpectedly verified: %v, %v", name, ok, err)
		}
	}
}

func TestAtlasEvidenceVerifierRestartReceiptRequiresExactRunningUnit(t *testing.T) {
	root := t.TempDir()
	receiptPath := filepath.Join(root, "binary-redeployment.jsonl")
	revision := strings.Repeat("d", 40)
	finished := "2026-08-22T13:00:00Z"
	receipts := []map[string]any{
		{
			"receipt_id": "receipt-restart-good", "component": "core", "target_revision": revision,
			"phase": "complete", "outcome": "success", "finished_at": finished,
			"running_units": []string{"rencrow.service"},
		},
		{
			"receipt_id": "receipt-restart-missing", "component": "core", "target_revision": revision,
			"phase": "complete", "outcome": "success", "finished_at": finished,
		},
		{
			"receipt_id": "receipt-restart-wrong", "component": "core", "target_revision": revision,
			"phase": "complete", "outcome": "success", "finished_at": finished,
			"running_units": []string{"other.service"},
		},
	}
	var data []byte
	for _, receipt := range receipts {
		encoded, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, encoded...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(receiptPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	pkg, err := backlogfeature.LoadBackfillPackage()
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := newAtlasEvidenceVerifierWithOptions(atlasEvidenceVerifierOptions{
		ReceiptPath: receiptPath, Specifications: pkg.SpecificationArtifacts,
	})
	if err != nil {
		t.Fatal(err)
	}
	valid := domainbacklog.EvidenceRef{
		Stage: domainbacklog.DeliveryRestart, Kind: "deploy_receipt", Ref: "receipt-restart-good", Revision: revision,
	}
	verificationRequest := backlogapp.EvidenceVerificationRequest{
		Ref: valid, ItemID: "item-restart", ImplementationUnitID: "unit-restart", ImplementationRevision: 5,
		TargetDeliveryState: domainbacklog.DeliveryRestart,
	}
	if ok, err := verifier.Verify(context.Background(), verificationRequest); err != nil || !ok {
		t.Fatalf("valid restart receipt = %v, %v", ok, err)
	}
	for _, receiptID := range []string{"receipt-restart-missing", "receipt-restart-wrong"} {
		request := verificationRequest
		request.Ref.Ref = receiptID
		if ok, err := verifier.Verify(context.Background(), request); err == nil || ok {
			t.Fatalf("restart receipt %q unexpectedly verified: %v, %v", receiptID, ok, err)
		}
	}
	wrongTarget := verificationRequest
	wrongTarget.TargetDeliveryState = domainbacklog.DeliveryPostDeployVerify
	wrongTarget.Ref.Stage = domainbacklog.DeliveryPostDeployVerify
	if ok, err := verifier.Verify(context.Background(), wrongTarget); err == nil || ok {
		t.Fatalf("deploy receipt for unrelated target unexpectedly verified: %v, %v", ok, err)
	}
	shortRevision := verificationRequest
	shortRevision.Ref.Revision = "deadbeef"
	if ok, err := verifier.Verify(context.Background(), shortRevision); err == nil || ok {
		t.Fatalf("short deployment revision unexpectedly verified: %v, %v", ok, err)
	}
}

func TestAtlasEvidenceVerifierRecordedBinaryHashIsCheckedWhenPresent(t *testing.T) {
	root := t.TempDir()
	binaryPath := filepath.Join(root, "rencrow")
	binary := []byte("installed-core-binary")
	if err := os.WriteFile(binaryPath, binary, 0o600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(binary)
	receipt := map[string]any{
		"receipt_id": "receipt-hash-1", "component": "core", "target_revision": strings.Repeat("a", 40),
		"phase": "complete", "outcome": "success", "finished_at": "2026-08-22T12:00:00Z",
		"binary_path": binaryPath, "installed_sha256": hex.EncodeToString(hash[:]),
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(root, "receipts.jsonl")
	if err := os.WriteFile(receiptPath, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	pkg, err := backlogfeature.LoadBackfillPackage()
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := newAtlasEvidenceVerifierWithOptions(atlasEvidenceVerifierOptions{ReceiptPath: receiptPath, Specifications: pkg.SpecificationArtifacts})
	if err != nil {
		t.Fatal(err)
	}
	ref := domainbacklog.EvidenceRef{Stage: domainbacklog.DeliveryDeploy, Kind: "deployment_receipt", Ref: "receipt-hash-1", Revision: strings.Repeat("a", 40), SHA256: hex.EncodeToString(hash[:])}
	verificationRequest := backlogapp.EvidenceVerificationRequest{
		Ref: ref, ItemID: "item-hash", ImplementationUnitID: "unit-hash", ImplementationRevision: 4,
		TargetDeliveryState: domainbacklog.DeliveryDeploy,
	}
	if ok, err := verifier.Verify(context.Background(), verificationRequest); err != nil || !ok {
		t.Fatalf("recorded installed hash = %v, %v", ok, err)
	}
	ref.SHA256 = strings.Repeat("0", 64)
	verificationRequest.Ref = ref
	if ok, err := verifier.Verify(context.Background(), verificationRequest); err == nil || ok {
		t.Fatalf("wrong installed hash unexpectedly verified: %v, %v", ok, err)
	}
}

func TestAtlasEvidenceVerifierReadinessFixedLoopbackProbe(t *testing.T) {
	revision := strings.Repeat("a", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"status":"ready","ready":true}`))
	}))
	defer server.Close()
	verifier := newAtlasProbeFixtureVerifier(t, server.URL, revision)
	request := backlogapp.EvidenceVerificationRequest{
		Ref:    domainbacklog.EvidenceRef{Kind: "readiness", Ref: atlasReadinessEvidenceRef, Revision: revision, Verified: true},
		ItemID: "item-ready", ImplementationUnitID: "unit-ready", ImplementationRevision: 2,
		TargetDeliveryState: domainbacklog.DeliveryPostDeployVerify,
	}
	if ok, err := verifier.Verify(context.Background(), request); err != nil || !ok {
		t.Fatalf("fixed readiness probe = %v, %v", ok, err)
	}
}

func TestAtlasEvidenceVerifierProductionSmokeRequiresReconstructedItem(t *testing.T) {
	pkg, err := backlogfeature.LoadBackfillPackage()
	if err != nil {
		t.Fatal(err)
	}
	artifact, ok := pkg.Specification("spec_atlas_idea_recording_v1")
	if !ok {
		t.Fatal("smoke fixture specification is missing")
	}
	item := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "smoke-item", ImplementationUnit: "unit-smoke",
		ImplementationRevision: 7, Title: "Smoke item", Purpose: "prove runtime ownership", Problem: "unproven runtime state", Idea: "probe the fixed owner route",
		SourceRefs: []domainbacklog.SourceRef{{Type: "fixture", Locator: "atlas-smoke"}}, SpecificationRefs: []string{artifact.SpecID},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/viewer/atlas/items/smoke-item" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"item": item, "resolved_specifications": []domainbacklog.SpecificationArtifact{artifact}})
	}))
	defer server.Close()
	revision := strings.Repeat("b", 40)
	verifier := newAtlasProbeFixtureVerifier(t, server.URL, revision)
	request := backlogapp.EvidenceVerificationRequest{
		Ref:    domainbacklog.EvidenceRef{Kind: "production_smoke", Ref: atlasSmokeEvidenceRef, Revision: revision, Verified: true},
		ItemID: item.ItemID, ImplementationUnitID: item.ImplementationUnit, ImplementationRevision: item.ImplementationRevision,
		TargetDeliveryState: domainbacklog.DeliveryLiveVerified,
	}
	if ok, err := verifier.Verify(context.Background(), request); err != nil || !ok {
		t.Fatalf("fixed production smoke probe = %v, %v", ok, err)
	}

	for name, mutate := range map[string]func(*backlogapp.EvidenceVerificationRequest){
		"wrong unit":     func(req *backlogapp.EvidenceVerificationRequest) { req.ImplementationUnitID = "unit-other" },
		"wrong revision": func(req *backlogapp.EvidenceVerificationRequest) { req.ImplementationRevision = 8 },
		"arbitrary URL token": func(req *backlogapp.EvidenceVerificationRequest) {
			req.Ref.Ref = server.URL + "/viewer/atlas/items/smoke-item"
		},
	} {
		candidate := request
		mutate(&candidate)
		if ok, err := verifier.Verify(context.Background(), candidate); err == nil || ok {
			t.Fatalf("%s unexpectedly verified: %v, %v", name, ok, err)
		}
	}
}

func TestAtlasEvidenceVerifierProbeRejectsRedirectAndNonLoopbackBase(t *testing.T) {
	revision := strings.Repeat("c", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ready" {
			http.Redirect(w, r, "/ready-final", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	verifier := newAtlasProbeFixtureVerifier(t, server.URL, revision)
	request := backlogapp.EvidenceVerificationRequest{
		Ref:    domainbacklog.EvidenceRef{Kind: "readiness", Ref: atlasReadinessEvidenceRef, Revision: revision},
		ItemID: "item-ready", ImplementationUnitID: "unit-ready", ImplementationRevision: 1,
		TargetDeliveryState: domainbacklog.DeliveryPostDeployVerify,
	}
	if ok, err := verifier.Verify(context.Background(), request); err == nil || ok {
		t.Fatalf("redirect unexpectedly verified: %v, %v", ok, err)
	}
	if _, err := newAtlasEvidenceVerifierWithOptions(atlasEvidenceVerifierOptions{
		ReceiptPath: filepath.Join(t.TempDir(), "receipts.jsonl"), OwnerBaseURL: "http://192.0.2.1:18790",
		ExecutablePath: verifier.executablePath, Specifications: verifier.specsAsSlice(),
	}); err == nil {
		t.Fatal("non-loopback probe base unexpectedly accepted")
	}
}

func newAtlasProbeFixtureVerifier(t *testing.T, baseURL, revision string) *atlasEvidenceVerifier {
	t.Helper()
	pkg, err := backlogfeature.LoadBackfillPackage()
	if err != nil {
		t.Fatal(err)
	}
	executablePath := filepath.Join(t.TempDir(), "rencrow-core")
	if err := os.WriteFile(executablePath, []byte("fixture executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	verifier, err := newAtlasEvidenceVerifierWithOptions(atlasEvidenceVerifierOptions{
		ReceiptPath: filepath.Join(t.TempDir(), "receipts.jsonl"), Specifications: pkg.SpecificationArtifacts,
		OwnerBaseURL: baseURL, ExecutablePath: executablePath, HTTPClient: &http.Client{},
		BuildInfo: func(path string) (*buildinfo.BuildInfo, error) {
			if path != executablePath {
				t.Fatalf("unexpected executable path %q", path)
			}
			return &buildinfo.BuildInfo{Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: revision}, {Key: "vcs.modified", Value: "false"},
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func (v *atlasEvidenceVerifier) specsAsSlice() []domainbacklog.SpecificationArtifact {
	artifacts := make([]domainbacklog.SpecificationArtifact, 0, len(v.specs))
	for _, artifact := range v.specs {
		artifacts = append(artifacts, artifact)
	}
	return artifacts
}
