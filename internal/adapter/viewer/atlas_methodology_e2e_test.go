package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	appbacklog "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	methodology "github.com/Nyukimin/RenCrow_CORE/internal/domain/developmentmethodology"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
)

type methodologyPromoteEvaluator struct{}

func (methodologyPromoteEvaluator) Evaluate(context.Context, appbacklog.RevalidationEvaluationInput) (appbacklog.RevalidationEvaluation, error) {
	return appbacklog.RevalidationEvaluation{Proposal: appbacklog.RevalidationProposal{
		Decision: domainbacklog.RevalidationDecisionPromote, Reason: "matured specification is ready",
		Necessity: "required", Duplication: "none", Mergeability: "not applicable",
		ArchitecturalFit: "uses existing Atlas owner", TechnologyValidity: "valid",
		ImplementationValue: "high", Timing: "due", ArchitectureImpact: "no duplicate state engine",
		NextReviewTrigger: "specification revision",
	}, ReviewAgents: []string{"methodology-review-execution"}}, nil
}

func TestAtlasDevelopmentMethodologyHTTPLifecycleReachesLiveVerified(t *testing.T) {
	now := time.Date(2026, 8, 28, 20, 0, 0, 0, time.UTC)
	items := &atlasHTTPItemStore{}
	workstreamRoot := t.TempDir()
	workstream := workstreampersistence.NewJSONLStore(workstreamRoot)
	service := appbacklog.NewService(items, workstream).
		WithClock(func() time.Time { return now }).
		WithEvidenceVerifier(atlasHTTPVerifier{}).
		WithRevalidationEvaluator(methodologyPromoteEvaluator{})
	token := atlasOwnerHTTPToken
	handler := NewAtlasHandler(service, "ren", []byte(token))

	post := func(path, body string, authorized bool) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+path, bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
		request.Header.Set("X-RenCrow-Interaction-Profile", "cmd-control")
		request.RemoteAddr = "127.0.0.1:43210"
		if authorized {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		record := httptest.NewRecorder()
		handler.ServeHTTP(record, request)
		return record
	}

	marshal := func(value any) string {
		t.Helper()
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %T: %v", value, err)
		}
		return string(payload)
	}

	postArtifact := func(unitID, artifactType, traceID string, value any) *httptest.ResponseRecorder {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s payload: %v", artifactType, err)
		}
		envelope, err := json.Marshal(struct {
			ArtifactType string          `json:"artifact_type"`
			TraceID      string          `json:"trace_id"`
			Payload      json.RawMessage `json:"payload"`
		}{ArtifactType: artifactType, TraceID: traceID, Payload: raw})
		if err != nil {
			t.Fatalf("marshal %s envelope: %v", artifactType, err)
		}
		return post("/v1/atlas/development/units/"+unitID+"/artifacts", string(envelope), true)
	}

	revise := func(itemID, stage, kind, ref string, includeEvidence bool) *httptest.ResponseRecorder {
		evidence := []domainbacklog.EvidenceRef(nil)
		if includeEvidence {
			evidence = []domainbacklog.EvidenceRef{{
				Stage: stage, Kind: kind, Ref: ref, ObservedAt: now.Format(time.RFC3339), Passed: true,
			}}
		}
		return post("/v1/atlas/items/"+itemID+"/revise", marshal(map[string]any{
			"request_id":     "methodology-" + stage,
			"delivery_state": stage,
			"evidence_refs":  evidence,
			"reason":         "HTTP lifecycle evidence for " + stage,
		}), true)
	}

	itemID := "methodology-http-e2e"
	if record := post("/v1/atlas/intake", `{"item_id":"methodology-http-e2e","title":"Development Methodology HTTP E2E","purpose":"prove the canonical methodology lifecycle","body":"owner API lifecycle acceptance","source_refs":[{"type":"test","locator":"methodology-http-e2e"}]}`, true); record.Code != http.StatusCreated {
		t.Fatalf("intake status=%d body=%s", record.Code, record.Body.String())
	}
	if record := post("/v1/atlas/items/"+itemID+"/candidate", `{}`, true); record.Code != http.StatusOK {
		t.Fatalf("candidate status=%d body=%s", record.Code, record.Body.String())
	}
	// Advance the injected clock to the normal maturation boundary. This proves
	// the canonical bounded orchestrator sweep instead of the exceptional
	// forced-bypass owner route.
	now = now.Add(7 * 24 * time.Hour)
	revalidation, err := service.RunEligibleRevalidations(t.Context(), 1)
	if err != nil || revalidation.Eligible != 1 || revalidation.Attempted != 1 || revalidation.Completed != 1 || revalidation.Failed != 0 {
		t.Fatalf("due revalidation sweep=%+v err=%v", revalidation, err)
	}
	adoptionRecord := post("/v1/atlas/items/"+itemID+"/adopt", `{"reason":"owner adopted methodology implementation unit"}`, true)
	if adoptionRecord.Code != http.StatusOK {
		t.Fatalf("adopt status=%d body=%s", adoptionRecord.Code, adoptionRecord.Body.String())
	}
	var adoption appbacklog.AdoptionResult
	if err := json.Unmarshal(adoptionRecord.Body.Bytes(), &adoption); err != nil {
		t.Fatalf("decode adoption: %v body=%s", err, adoptionRecord.Body.String())
	}
	if adoption.Item.ConceptState != domainbacklog.ConceptAdopted || adoption.Item.DeliveryState != domainbacklog.DeliveryQueued || adoption.Item.ImplementationUnit == "" {
		t.Fatalf("adoption did not create an adopted queued unit: %+v", adoption)
	}
	unitID := adoption.Item.ImplementationUnit
	if adoption.Item.WorkstreamID == "" || adoption.Lease.HolderUnitID != unitID {
		t.Fatalf("adoption did not issue the implementation lease: %+v", adoption)
	}

	content := "The canonical owner API is the only methodology lifecycle route."
	spec := methodology.Specification{
		SchemaVersion:      methodology.SchemaVersion,
		SpecID:             "spec-methodology-http-e2e",
		Title:              "Development Methodology HTTP E2E",
		Revision:           1,
		Status:             methodology.SpecificationApproved,
		Source:             "authenticated Atlas owner API",
		ContentHash:        methodology.HashContent(content),
		Content:            content,
		Purpose:            "prove lifecycle gates through the canonical route",
		Problem:            "component checks can pass without a complete lifecycle receipt",
		Scope:              []string{"RenCrow_CORE", "Atlas", "Viewer"},
		Constraints:        []string{"same owner API for artifacts and lifecycle", "no human-wait state"},
		Interfaces:         []string{"POST /v1/atlas/development/units/{unit}/artifacts", "POST /v1/atlas/items/{id}/revise"},
		AcceptanceCriteria: []string{"LIVE_VERIFIED requires every ValidateLIVEGate receipt", "final projection exposes evidence and reviews"},
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if record := post("/v1/atlas/development/units/"+unitID+"/artifacts", marshal(map[string]any{
		"artifact_type": "specification", "trace_id": "trace-spec-unauthorized", "payload": spec,
	}), false); record.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized specification status=%d body=%s", record.Code, record.Body.String())
	}
	if record := postArtifact(unitID, appbacklog.DevelopmentArtifactSpecification, "trace-spec", spec); record.Code != http.StatusCreated {
		t.Fatalf("specification status=%d body=%s", record.Code, record.Body.String())
	}

	planID := "plan-methodology-http-e2e"
	taskID := "task-methodology-http-e2e"
	task := methodology.Task{
		TaskID: taskID, PlanID: planID, Purpose: "prove every methodology gate over HTTP",
		ExactFiles:         []string{"internal/adapter/viewer/atlas_methodology_e2e_test.go"},
		InterfacesConsumed: []string{"Atlas owner API", "Development projection"},
		InterfacesProduced: []string{"lifecycle receipts", "terminal projection"},
		AssignedSkill:      "global-engineering-rules", RequiredCapability: "CORE owner API",
		AuthorityRequirement: "authenticated owner request", ExactCommands: []string{"go test ./internal/adapter/viewer -run Methodology -count=1"},
		ExpectedResults: []string{"every stage has a verified receipt"}, ReviewRequirement: "independent task and branch review",
		Rollback: []string{"retain the append-only evidence ledger"}, State: methodology.TaskPending,
		CreatedAt: now, UpdatedAt: now,
	}
	plan := methodology.Plan{
		SchemaVersion: methodology.SchemaVersion, PlanID: planID, ImplementationUnitID: unitID,
		SpecRef: spec.SpecID, SpecHash: spec.ContentHash, Revision: "plan-01",
		GlobalConstraints: []string{"CORE is the owner", "all state changes use canonical routes"},
		FileMap:           map[string][]string{"viewer": {"internal/adapter/viewer/atlas_methodology_e2e_test.go"}},
		TaskDAG:           map[string][]string{taskID: {}}, Tasks: []methodology.Task{task}, CreatedAt: now, UpdatedAt: now,
	}
	if record := postArtifact(unitID, appbacklog.DevelopmentArtifactPlan, "trace-plan", plan); record.Code != http.StatusCreated {
		t.Fatalf("plan status=%d body=%s", record.Code, record.Body.String())
	}

	implementation_authorityRecord := post("/v1/atlas/development/units/"+unitID+"/implementation_authority", marshal(map[string]any{
		"issuer": "ren", "scope": []string{"implementation", "test_execution", "build", "deploy", "restart"},
		"reason": "owner-issued implementation_authority after Atlas adoption", "expires_at": now.Add(24 * time.Hour),
	}), true)
	if implementation_authorityRecord.Code != http.StatusCreated {
		t.Fatalf("implementation_authority status=%d body=%s", implementation_authorityRecord.Code, implementation_authorityRecord.Body.String())
	}
	var implementation_authorityProjection appbacklog.DevelopmentProjection
	if err := json.Unmarshal(implementation_authorityRecord.Body.Bytes(), &implementation_authorityProjection); err != nil {
		t.Fatalf("decode implementation_authority projection: %v body=%s", err, implementation_authorityRecord.Body.String())
	}
	if implementation_authorityProjection.ImplementationAuthorityToken == nil || implementation_authorityProjection.ImplementationAuthorityToken.UnitID != unitID || implementation_authorityProjection.ImplementationAuthorityToken.SpecRef != spec.SpecID || implementation_authorityProjection.ImplementationAuthorityToken.Issuer != "ren" {
		t.Fatalf("implementation_authority was not bound to adopted unit/spec: %+v", implementation_authorityProjection.ImplementationAuthorityToken)
	}

	worktreePath := t.TempDir()
	initialRevision := plan.Revision
	baseline := methodology.BaselineEvidence{
		UnitID: unitID, PlanID: planID, SpecRef: spec.SpecID, SpecHash: spec.ContentHash, ValidForRevision: initialRevision,
		WorktreePath: worktreePath, Branch: "feat/methodology-http-e2e", BaseRevision: "baseline-revision",
		Command: "git status --porcelain=v1 --branch", ExitCode: 0, ResultSummary: "clean baseline",
		GitRevision: "baseline-revision", Verified: true, CreatedAt: now,
	}
	ledger := methodology.Ledger{
		SchemaVersion: methodology.SchemaVersion, UnitID: unitID, PlanID: planID, SpecRef: spec.SpecID, SpecHash: spec.ContentHash,
		Revision: initialRevision, CurrentState: string(methodology.TaskPending), Tasks: []methodology.Task{task},
		Worktrees:        []methodology.WorktreeEvidence{{WorktreePath: worktreePath, Branch: "feat/methodology-http-e2e", BaseRevision: "baseline-revision", GitRevision: "baseline-revision", Isolated: true, Verified: true, CreatedAt: now}},
		BaselineEvidence: []methodology.BaselineEvidence{baseline}, LastCheckpointAt: now,
	}
	if record := postArtifact(unitID, appbacklog.DevelopmentArtifactLedger, "trace-ledger-01", ledger); record.Code != http.StatusCreated {
		t.Fatalf("initial ledger status=%d body=%s", record.Code, record.Body.String())
	}

	if record := revise(itemID, domainbacklog.DeliverySpec, "spec", "spec-stage", true); record.Code != http.StatusOK {
		t.Fatalf("SPEC transition status=%d body=%s", record.Code, record.Body.String())
	}

	receipts := []methodology.EvidenceReceipt{}
	reviews := []methodology.ReviewRecord{}
	appendLedger := func(checkpoint, state string, additions ...methodology.EvidenceReceipt) {
		t.Helper()
		now = now.Add(time.Second)
		receipts = append(receipts, additions...)
		next := ledger
		next.CurrentState = state
		next.TerminalOutcome = ""
		next.CheckOK = false
		next.Tasks = append([]methodology.Task(nil), ledger.Tasks...)
		if len(next.Tasks) > 0 {
			switch methodology.TaskState(state) {
			case methodology.TaskPending, methodology.TaskReady, methodology.TaskAssigned, methodology.TaskRedVerified, methodology.TaskGreenVerified, methodology.TaskRefactored, methodology.TaskReviewed, methodology.TaskDone:
				next.Tasks[0].State = methodology.TaskState(state)
			}
		}
		if state == string(methodology.TaskDone) {
			next.TerminalOutcome = methodology.OutcomeOK
			next.CheckOK = true
			next.Tasks[0].TerminalOutcome = methodology.OutcomeOK
		}
		next.EvidenceRefs = append([]methodology.EvidenceReceipt(nil), receipts...)
		next.ReviewRecords = append([]methodology.ReviewRecord(nil), reviews...)
		next.BaselineEvidence = append([]methodology.BaselineEvidence(nil), ledger.BaselineEvidence...)
		next.LastCheckpointAt = now
		if record := postArtifact(unitID, appbacklog.DevelopmentArtifactLedger, "trace-"+checkpoint, next); record.Code != http.StatusCreated {
			t.Fatalf("ledger %s status=%d body=%s", checkpoint, record.Code, record.Body.String())
		}
		ledger = next
	}
	appendLedger("ledger-ready", string(methodology.TaskReady))
	appendLedger("ledger-assigned", string(methodology.TaskAssigned))

	// The HTTP adapter must fail closed before the owner service can mutate the
	// Atlas state when the required RED evidence is absent.
	if record := revise(itemID, domainbacklog.DeliveryTDDRed, "execution_report", "red-missing", false); record.Code != http.StatusBadRequest {
		t.Fatalf("missing RED evidence status=%d body=%s", record.Code, record.Body.String())
	}

	receipt := func(id, stage, kind, _ string, exitCode int) methodology.EvidenceReceipt {
		result := methodology.EvidenceReceipt{
			EvidenceID: id, IdempotencyKey: id, UnitID: unitID, PlanID: planID, SpecHash: spec.ContentHash, TaskID: taskID, Stage: stage, EvidenceType: kind,
			Command: "machine receipt for " + stage, ExitCode: exitCode, ResultSummary: "verified " + stage,
			ArtifactRef: "receipt:" + id, GitRevision: "baseline-revision", TraceID: "trace:" + id, EventID: "event:" + id,
			ValidForRevision: initialRevision, VerificationResult: "verified", Verified: true, MachineGenerated: true, Passed: true, CreatedAt: now,
		}
		if stage == domainbacklog.DeliveryTDDRed {
			result.ExpectedFailure = "expected assertion failure"
			result.ActualFailure = "expected assertion failure: red test is failing before implementation"
		}
		if stage == domainbacklog.DeliveryBuild {
			result.ArtifactSHA256 = methodology.HashContent("built artifact")
		}
		return result
	}

	red := receipt("methodology-red", domainbacklog.DeliveryTDDRed, "tdd_red", "ledger-02", 1)
	appendLedger("ledger-02", string(methodology.TaskRedVerified), red)
	if record := revise(itemID, domainbacklog.DeliveryTDDRed, "execution_report", "red-stage", true); record.Code != http.StatusOK {
		t.Fatalf("TDD_RED transition status=%d body=%s", record.Code, record.Body.String())
	}
	green := receipt("methodology-green", domainbacklog.DeliveryTDDGreen, "tdd_green", "ledger-03", 0)
	appendLedger("ledger-03", string(methodology.TaskGreenVerified), green)
	if record := revise(itemID, domainbacklog.DeliveryTDDGreen, "execution_report", "green-stage", true); record.Code != http.StatusOK {
		t.Fatalf("TDD_GREEN transition status=%d body=%s", record.Code, record.Body.String())
	}
	refactor := receipt("methodology-refactor", domainbacklog.DeliveryRefactor, "refactor", "ledger-04", 0)
	appendLedger("ledger-04", string(methodology.TaskRefactored), refactor)
	if record := revise(itemID, domainbacklog.DeliveryRefactor, "execution_report", "refactor-stage", true); record.Code != http.StatusOK {
		t.Fatalf("REFACTOR transition status=%d body=%s", record.Code, record.Body.String())
	}

	// Recreate the owner service and JSONL store to prove that resume is driven
	// by the persisted plan-scoped ledger, not process memory.
	workstream = workstreampersistence.NewJSONLStore(workstreamRoot)
	service = appbacklog.NewService(items, workstream).
		WithClock(func() time.Time { return now }).
		WithEvidenceVerifier(atlasHTTPVerifier{}).
		WithRevalidationEvaluator(methodologyPromoteEvaluator{})
	handler = NewAtlasHandler(service, "ren", []byte(token))
	resumed, err := service.Development(t.Context(), unitID)
	if err != nil || resumed.Ledger == nil || resumed.Ledger.CurrentState != string(methodology.TaskRefactored) || resumed.Ledger.PlanID != planID {
		t.Fatalf("restart resume projection=%+v err=%v", resumed.Ledger, err)
	}

	// These role labels satisfy the review schema without impersonating a
	// CORE-managed Agent in this lifecycle-only E2E test.
	taskReview := methodology.ReviewRecord{
		ReviewID: "methodology-task-review", UnitID: unitID, TaskID: taskID, ReviewType: methodology.ReviewTypeTask,
		ImplementerAgentID: "worker-implementation", ReviewerAgentID: "worker-independent-reviewer", SpecRef: spec.SpecID,
		PlanID: planID, SpecHash: spec.ContentHash, ValidForRevision: initialRevision,
		DiffRef: "diff:methodology-http-e2e", Verdict: methodology.ReviewAccepted, EvidenceRefs: []string{red.EvidenceID, green.EvidenceID, refactor.EvidenceID}, CreatedAt: now,
	}
	branchReview := taskReview
	branchReview.ReviewID = "methodology-branch-review"
	branchReview.TaskID = ""
	branchReview.ReviewType = methodology.ReviewTypeBranch
	branchReview.DiffRef = "branch:methodology-http-e2e"
	reviews = append(reviews, taskReview, branchReview)
	appendLedger("ledger-05", string(methodology.TaskReviewed))

	if record := revise(itemID, domainbacklog.DeliveryE2EPredeploy, "execution_report", "e2e-stage", true); record.Code != http.StatusOK {
		t.Fatalf("E2E_PREDEPLOY transition status=%d body=%s", record.Code, record.Body.String())
	}
	accepted := receipt("methodology-accepted", "ACCEPTED", "accepted", "ledger-06", 0)
	regression := receipt("methodology-regression", "REGRESSION", "regression", "ledger-06", 0)
	build := receipt("methodology-build", domainbacklog.DeliveryBuild, "build", "ledger-06", 0)
	appendLedger("ledger-06", string(methodology.TaskReviewed), accepted, regression, build)
	if record := revise(itemID, domainbacklog.DeliveryBuild, "execution_report", "build-stage", true); record.Code != http.StatusOK {
		t.Fatalf("BUILD transition status=%d body=%s", record.Code, record.Body.String())
	}

	ecosystem := receipt("methodology-ecosystem", "ECOSYSTEM_VERIFIED", "ecosystem_verified", "ledger-07", 0)
	deploy := receipt("methodology-deploy", domainbacklog.DeliveryDeploy, "deploy_receipt", "ledger-08", 0)
	appendLedger("ledger-07", string(methodology.TaskReviewed), ecosystem, deploy)
	if record := revise(itemID, domainbacklog.DeliveryDeploy, "deploy_receipt", "deploy-stage", true); record.Code != http.StatusOK {
		t.Fatalf("DEPLOY transition status=%d body=%s", record.Code, record.Body.String())
	}

	restart := receipt("methodology-restart", domainbacklog.DeliveryRestart, "restart_receipt", "ledger-09", 0)
	process := receipt("methodology-process", "PROCESS_IDENTITY_VERIFIED", "process_identity", "ledger-09", 0)
	appendLedger("ledger-08", string(methodology.TaskReviewed), restart, process)
	if record := revise(itemID, domainbacklog.DeliveryRestart, "restart_receipt", "restart-stage", true); record.Code != http.StatusOK {
		t.Fatalf("RESTART transition status=%d body=%s", record.Code, record.Body.String())
	}

	readiness := receipt("methodology-readiness", domainbacklog.DeliveryPostDeployVerify, "readiness", "ledger-10", 0)
	appendLedger("ledger-09", string(methodology.TaskReviewed), readiness)
	if record := revise(itemID, domainbacklog.DeliveryPostDeployVerify, "readiness", "readiness-stage", true); record.Code != http.StatusOK {
		t.Fatalf("POST_DEPLOY_VERIFY transition status=%d body=%s", record.Code, record.Body.String())
	}
	production := receipt("methodology-production", "PRODUCTION_VERIFIED", "production_smoke", "ledger-11", 0)
	viewer := receipt("methodology-viewer", "VIEWER_VERIFIED", "viewer_verification", "ledger-12", 0)
	appendLedger("ledger-10", string(methodology.TaskReviewed), production, viewer)
	if record := revise(itemID, domainbacklog.DeliveryLiveVerified, "production_smoke", "live-stage", true); record.Code != http.StatusOK {
		t.Fatalf("LIVE_VERIFIED transition status=%d body=%s", record.Code, record.Body.String())
	}
	appendLedger("ledger-13", string(methodology.TaskDone))

	final := httptest.NewRecorder()
	handler.ServeHTTP(final, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/viewer/atlas/development/units/"+unitID, nil))
	if final.Code != http.StatusOK {
		t.Fatalf("final development GET status=%d body=%s", final.Code, final.Body.String())
	}
	var projection appbacklog.DevelopmentProjection
	if err := json.Unmarshal(final.Body.Bytes(), &projection); err != nil {
		t.Fatalf("decode final development projection: %v body=%s", err, final.Body.String())
	}
	if projection.Ledger == nil || projection.Ledger.CurrentState != string(methodology.TaskDone) || projection.Ledger.TerminalOutcome != methodology.OutcomeOK || len(projection.Ledger.EvidenceRefs) != 13 || len(projection.Ledger.ReviewRecords) != 2 {
		t.Fatalf("final methodology projection lost terminal evidence/reviews: %+v", projection.Ledger)
	}
	if len(projection.Evidence) < 13 || len(projection.Reviews) != 2 || projection.ImplementationAuthorityToken == nil || projection.Plan == nil || projection.Specification == nil {
		t.Fatalf("final projection incomplete: spec=%v plan=%v implementation_authority=%v evidence=%d reviews=%d", projection.Specification != nil, projection.Plan != nil, projection.ImplementationAuthorityToken != nil, len(projection.Evidence), len(projection.Reviews))
	}
	evidenceDetail := httptest.NewRecorder()
	handler.ServeHTTP(evidenceDetail, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/viewer/atlas/development/units/"+unitID+"/evidence/"+red.EvidenceID, nil))
	if evidenceDetail.Code != http.StatusOK || !bytes.Contains(evidenceDetail.Body.Bytes(), []byte(`"evidence_id":"`+red.EvidenceID+`"`)) {
		t.Fatalf("evidence detail status=%d body=%s", evidenceDetail.Code, evidenceDetail.Body.String())
	}

	itemRecord := httptest.NewRecorder()
	handler.ServeHTTP(itemRecord, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/viewer/atlas/items/"+itemID, nil))
	if itemRecord.Code != http.StatusOK || !bytes.Contains(itemRecord.Body.Bytes(), []byte(`"delivery_state":"DONE"`)) {
		t.Fatalf("terminal Atlas item projection status=%d body=%s", itemRecord.Code, itemRecord.Body.String())
	}
}
