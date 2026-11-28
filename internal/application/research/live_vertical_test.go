package research

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/webgather"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	taskstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	webgatherinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/webgather"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

const (
	liveResearchFlagEnv    = "RENCROW_RESEARCH_LIVE_TEST"
	liveResearchGatewayEnv = "RENCROW_RESEARCH_LIVE_GATEWAY_ROOT"
	liveResearchReportEnv  = "RENCROW_RESEARCH_LIVE_REPORT_PATH"
	liveResearchGateway    = "http://127.0.0.1:8090"
	liveResearchPolicy     = "rencrow-research-live-rfc.v1"
	liveResearchOwner      = "ren"
)

type liveSourceEvidence struct {
	SourceRef       string `json:"source_ref"`
	OriginalURL     string `json:"original_url"`
	FinalURL        string `json:"final_url"`
	ExtractedHash   string `json:"extracted_hash"`
	IndependentHost string `json:"independent_host"`
	ContentState    string `json:"content_state"`
	Complete        bool   `json:"complete"`
	Primary         bool   `json:"primary"`
}

type liveReceiptEvidence struct {
	Owner       string `json:"owner"`
	Route       string `json:"route"`
	CanonicalID string `json:"canonical_id"`
	Digest      string `json:"digest"`
	State       string `json:"state"`
	Scope       string `json:"scope"`
	Readback    string `json:"readback"`
}

type liveVerticalEvidence struct {
	Profile                      string                          `json:"profile"`
	AcceptanceBoundary           string                          `json:"acceptance_boundary"`
	SourceRightsBasis            string                          `json:"source_rights_basis"`
	GatewayRoot                  string                          `json:"gateway_root"`
	TaskID                       string                          `json:"task_id"`
	ExecutionRunID               string                          `json:"execution_run_id"`
	ResearchRunID                string                          `json:"research_run_id"`
	Status                       string                          `json:"status"`
	TaskStatus                   string                          `json:"task_status"`
	RunStatus                    string                          `json:"run_status"`
	Counts                       c.Counts                        `json:"counts"`
	AnalysisCalls                int                             `json:"analysis_calls"`
	AnalysisError                string                          `json:"analysis_error,omitempty"`
	CollectionHash               string                          `json:"collection_artifact_hash"`
	Missing                      []string                        `json:"missing"`
	Errors                       []c.Fault                       `json:"errors"`
	Stages                       []c.Stage                       `json:"stages"`
	EngineError                  bool                            `json:"engine_error"`
	Documents                    []liveSourceEvidence            `json:"documents"`
	Receipts                     []liveReceiptEvidence           `json:"receipts"`
	TaskFactsDigest              string                          `json:"task_facts_digest"`
	TaskCounts                   domaintask.ResearchResultCounts `json:"task_counts"`
	TaskReceiptState             string                          `json:"task_receipt_state"`
	TaskRequiredReceiptsComplete bool                            `json:"task_required_receipts_complete"`
	Report                       string                          `json:"report"`
}

// TestResearchLiveVertical exercises the real specialist API, public HTTP
// retrieval, Engine, temporary L1 owner, Task admission, result recorder, and
// receipt readback. It is deliberately opt-in and locked to the approved
// specialist Gateway endpoint; the Task, L1, and artifact owners remain temporary.
func TestResearchLiveVertical(t *testing.T) {
	if os.Getenv(liveResearchFlagEnv) != "1" {
		t.Skip("set " + liveResearchFlagEnv + "=1 to run the isolated live vertical test")
	}
	gatewayRoot := strings.TrimSpace(os.Getenv(liveResearchGatewayEnv))
	if gatewayRoot != liveResearchGateway {
		t.Fatalf("%s must equal the approved specialist Gateway %q", liveResearchGatewayEnv, liveResearchGateway)
	}
	reportPath := strings.TrimSpace(os.Getenv(liveResearchReportEnv))
	if reportPath == "" || reportPath != os.Getenv(liveResearchReportEnv) {
		t.Fatalf("%s must name the parent-selected temporary evidence JSON path", liveResearchReportEnv)
	}
	evidence := liveVerticalEvidence{
		Profile:            "production-specialist-gateway-live-source-with-isolated-owners",
		AcceptanceBoundary: "production LLM route with real sources and temporary Task/L1 owners; not full production CORE or Agent acceptance",
		SourceRightsBasis:  "RFC Editor rfc-use terms; fixed RFC 1149 and RFC 2549 sources only",
		GatewayRoot:        gatewayRoot, Status: "not_started", Documents: []liveSourceEvidence{}, Receipts: []liveReceiptEvidence{},
		Missing: []string{}, Errors: []c.Fault{}, Stages: []c.Stage{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	root := t.TempDir()
	tasksStore, err := taskstore.NewJSONLStoreWithOptions(filepath.Join(root, "tasks"), taskstore.OpenOptions{Index: true, Persist: true})
	if err != nil {
		t.Fatal("open temporary Task owner")
	}
	defer tasksStore.Close()
	tasks := taskmanager.New(tasksStore, taskmanager.DefaultParallelLimits())
	defer func() {
		if evidence.TaskID != "" {
			if task, err := tasks.Get(context.Background(), core.TaskID(evidence.TaskID)); err == nil {
				evidence.TaskStatus = string(task.Status)
				if task.ResearchResult != nil {
					evidence.TaskFactsDigest = task.ResearchResult.ResultDigest
					evidence.TaskCounts = task.ResearchResult.Counts
					evidence.TaskReceiptState = task.ResearchResult.ReceiptState
					evidence.TaskRequiredReceiptsComplete = task.ResearchResult.RequiredReceiptsComplete
				}
			}
			if run, err := tasks.GetRun(context.Background(), core.RunID(evidence.ExecutionRunID)); err == nil {
				evidence.RunStatus = string(run.Status)
			}
		}
		if err := writeLiveVerticalEvidence(reportPath, evidence); err != nil {
			t.Errorf("write redacted live evidence: %v", err)
		}
	}()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(root, "l1.sqlite"))
	if err != nil {
		t.Fatal("open temporary L1 owner")
	}
	defer l1.Close()
	artifacts, err := engine.NewFileStore(filepath.Join(root, "research-artifacts"))
	if err != nil {
		t.Fatal("open temporary Research artifact owner")
	}
	defer artifacts.Close()

	request := c.Request{
		SchemaVersion: c.RequestSchema, Mode: "investigate",
		Objective:   "Summarize the shared technical premise and stated limits of RFC 1149 and RFC 2549, with evidence from both RFC Editor sources.",
		SourceKinds: []string{"web"},
		SeedURLs: []string{
			"https://www.rfc-editor.org/rfc/rfc1149.txt",
			"https://www.rfc-editor.org/rfc/rfc2549.txt",
		},
		Outputs: c.Outputs{
			Save:     []c.SaveTarget{{Target: KnowledgeStagingTarget, Required: true}},
			Evidence: c.EvidenceRequirements{MinDocuments: 2, MinIndependentSources: 1, RequirePrimary: true},
		},
		Limits:   c.Limits{Candidates: 2, FetchAttempts: 2, MaxDocuments: 2, DeadlineSeconds: 150, AnalysisCalls: 2},
		Language: "en", CostProfile: "zero_incremental_cost",
	}
	normalized, err := engine.Normalize(request)
	if err != nil {
		t.Fatal("normalize fixed live request")
	}
	requestBytes, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal("encode fixed live request")
	}
	requestDigest := sha256.Sum256(requestBytes)
	requestHash := hex.EncodeToString(requestDigest[:])
	clientKey := "live-rfc-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	admission, _, err := tasks.EnsureResearchAdmission(ctx, liveResearchOwner, clientKey, requestHash, liveResearchPolicy, normalized.Mode)
	if err != nil {
		t.Fatal("admit Research Task through the existing Task owner")
	}
	evidence.TaskID = string(admission.TaskID)
	evidence.ExecutionRunID = string(admission.InitialRunID)
	closeOutcome := taskmanager.RunOutcome{Status: domaintask.StatusFailed, Summary: "Research live test stopped before completion"}
	runClosed := false
	defer func() {
		if runClosed {
			return
		}
		if err := tasks.CloseStartedRun(context.Background(), core.TaskID(admission.TaskID), core.RunID(admission.InitialRunID), liveResearchOwner, closeOutcome, nil); err != nil {
			t.Errorf("close admitted Research Run during cleanup: %v", err)
		}
	}()
	envelope := c.Envelope{
		TaskID: string(admission.TaskID), RunID: string(admission.InitialRunID),
		TraceID: string(admission.TraceID), RequestID: string(admission.RequestID),
		Actor: liveResearchOwner, OwnerID: liveResearchOwner, DataScope: "user", PolicyRevision: admission.PolicyRevision,
	}

	allowedSources := map[string]bool{
		"https://www.rfc-editor.org/rfc/rfc1149.txt": true,
		"https://www.rfc-editor.org/rfc/rfc2549.txt": true,
	}
	sourcePolicy := func(_ context.Context, candidate c.Candidate) (bool, bool) {
		if candidate.SourceKind != "web" {
			return false, false
		}
		parsed, parseErr := url.Parse(candidate.URL)
		if parseErr != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "www.rfc-editor.org") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return false, false
		}
		if parsed.Path != "/rfc/rfc1149.txt" && parsed.Path != "/rfc/rfc2549.txt" {
			return false, false
		}
		return allowedSources[candidate.URL], true
	}
	fetcher := webgather.NewUseCase(webgatherinfra.NewHTTPFetcher(), webgatherinfra.NewBasicExtractor(), nil)
	adapters := NewAdapters(nil, fetcher, l1).WithSourcePolicy(sourcePolicy)
	analysis, err := NewHTTPAnalysisPort(gatewayRoot, "research_analysis_local", &http.Client{Timeout: 45 * time.Second})
	if err != nil {
		t.Fatal("configure isolated specialist Gateway")
	}
	analysisCapture := &liveAnalysisCapture{port: analysis}
	service := &engine.Engine{Ports: engine.Ports{
		Store: artifacts, Acquisition: adapters, Owner: adapters, Analysis: analysisCapture,
	}}
	result, runErr := service.Run(ctx, envelope, normalized)
	evidence.ResearchRunID = result.RunID
	evidence.Status = result.Status
	evidence.Counts = result.Counts
	evidence.AnalysisCalls = result.Counts.AnalysisCalls
	evidence.AnalysisError = analysisCapture.failureCode
	evidence.Missing = append([]string(nil), result.Missing...)
	evidence.Errors = append([]c.Fault(nil), result.Errors...)
	evidence.Stages = append([]c.Stage(nil), result.Stages...)
	evidence.Report = result.Report
	if runErr != nil {
		evidence.EngineError = true
		if evidence.Status == "" {
			evidence.Status = "error"
		}
		closeOutcome = taskmanager.RunOutcomeOf(runErr, "")
		t.Fatal("live Research Engine execution failed")
	}
	collectionRef, foundCollection := collectionArtifact(result.Artifacts)
	if !foundCollection {
		evidence.Missing = append(evidence.Missing, "collection_artifact_missing")
	} else {
		evidence.CollectionHash = collectionRef.Hash
	}
	if foundCollection {
		collectionJSON, readErr := artifacts.Get(ctx, collectionRef)
		if readErr != nil {
			evidence.Missing = append(evidence.Missing, "collection_artifact_readback_failed")
		} else {
			var collection c.Collection
			if decodeErr := json.Unmarshal(collectionJSON, &collection); decodeErr != nil {
				evidence.Missing = append(evidence.Missing, "collection_artifact_decode_failed")
			} else {
				for _, document := range collection.Documents {
					documents := &evidence.Documents
					*documents = append(*documents, liveSourceEvidence{
						SourceRef: document.SourceRef, OriginalURL: document.OriginalURL, FinalURL: document.FinalURL,
						ExtractedHash: document.ExtractedHash, IndependentHost: document.IndependentSource,
						ContentState: document.ContentState, Complete: document.Complete, Primary: document.Primary,
					})
				}
			}
		}
	}
	for _, receipt := range result.Receipts {
		verified, verifyErr := adapters.Verify(ctx, envelope, receipt)
		state := "unavailable"
		if verifyErr == nil && !verified.VerifiedAt.IsZero() {
			state = "verified"
		}
		evidence.Receipts = append(evidence.Receipts, liveReceiptEvidence{
			Owner: receipt.Owner, Route: receipt.OwnerRoute, CanonicalID: receipt.CanonicalID,
			Digest: receipt.Digest, State: receipt.State, Scope: receipt.Scope, Readback: state,
		})
	}
	seenRefs := map[string]bool{}
	independentHosts := map[string]bool{}
	validSources := true
	for _, document := range evidence.Documents {
		if !document.Primary || !document.Complete || document.ContentState != "fetched" || !allowedResearchFinalURL(document.FinalURL) || document.ExtractedHash == "" || document.SourceRef == "" || seenRefs[document.SourceRef] {
			validSources = false
		}
		seenRefs[document.SourceRef] = true
		if document.IndependentHost != "" {
			independentHosts[document.IndependentHost] = true
		} else {
			validSources = false
		}
	}
	allReceiptsVerified := len(evidence.Receipts) == 2
	for _, receipt := range evidence.Receipts {
		allReceiptsVerified = allReceiptsVerified && receipt.Readback == "verified"
	}
	if result.Status != "succeeded" || result.Counts.DocumentsFetched != 2 || result.Counts.DocumentsAnalyzed != 2 || result.Counts.RecordsSaved != 2 || result.Counts.PaidExternalCalls != 0 || result.Counts.AnalysisCalls < 1 || result.Counts.AnalysisCalls > 2 || result.Report == "" || len(evidence.Documents) != 2 || len(seenRefs) != 2 || len(independentHosts) < 1 || !validSources || !allReceiptsVerified {
		t.Fatalf("live Research result did not meet acceptance bounds (status=%s, fetched=%d, analyzed=%d, saved=%d, docs=%d, receipts=%d, analysis_calls=%d, paid=%d)", result.Status, result.Counts.DocumentsFetched, result.Counts.DocumentsAnalyzed, result.Counts.RecordsSaved, len(evidence.Documents), len(evidence.Receipts), result.Counts.AnalysisCalls, result.Counts.PaidExternalCalls)
	}

	recorder := &ResultRecorder{Store: artifacts, Tasks: tasks}
	if err := recorder.Record(ctx, envelope, result); err != nil {
		t.Fatal("record deterministic Research facts in the Task owner")
	}
	task, err := tasks.Get(ctx, core.TaskID(admission.TaskID))
	if err != nil || task.ResearchResult == nil || task.ResearchResult.Status != result.Status || task.ResearchResult.Counts.DocumentsFetched != 2 || task.ResearchResult.Counts.DocumentsAnalyzed != 2 || task.ResearchResult.Counts.RecordsSaved != 2 || task.ResearchResult.Counts.BoardPostsCreated != 0 || task.ResearchResult.ReceiptCount != 2 || task.ResearchResult.ReceiptState != "verified" || !task.ResearchResult.RequiredReceiptsComplete {
		t.Fatal("Task owner did not read back exact Research result facts")
	}
	closeOutcome = taskmanager.RunOutcomeOf(nil, "Research run completed")
	if err := tasks.CloseStartedRun(ctx, core.TaskID(admission.TaskID), core.RunID(admission.InitialRunID), liveResearchOwner, closeOutcome, nil); err != nil {
		t.Fatal("close admitted Research Run through the Task owner")
	}
	runClosed = true
	task, err = tasks.Get(ctx, core.TaskID(admission.TaskID))
	run, runErr := tasks.GetRun(ctx, core.RunID(admission.InitialRunID))
	if err != nil || runErr != nil || task.Status != domaintask.StatusSucceeded || run.Status != domaintask.RunStatusSucceeded || task.ResearchResult == nil || task.ResearchResult.ResultDigest == "" {
		t.Fatal("terminal Task readback did not preserve Research facts")
	}

	evidence.TaskStatus = string(task.Status)
	evidence.RunStatus = string(run.Status)
	evidence.TaskFactsDigest = task.ResearchResult.ResultDigest
	evidence.TaskCounts = task.ResearchResult.Counts
	evidence.TaskReceiptState = task.ResearchResult.ReceiptState
	evidence.TaskRequiredReceiptsComplete = task.ResearchResult.RequiredReceiptsComplete
}

func writeLiveVerticalEvidence(path string, evidence liveVerticalEvidence) error {
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := output.Write(append(encoded, '\n')); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func collectionArtifact(refs []c.ArtifactRef) (c.ArtifactRef, bool) {
	for _, ref := range refs {
		if ref.Kind == "collection" {
			return ref, true
		}
	}
	return c.ArtifactRef{}, false
}

func allowedResearchFinalURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "www.rfc-editor.org") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.Path == "/rfc/rfc1149.txt" || parsed.Path == "/rfc/rfc2549.txt"
}

type liveAnalysisCapture struct {
	port        engine.AnalysisPort
	failureCode string
}

func (p *liveAnalysisCapture) Analyze(ctx context.Context, request engine.AnalysisRequest) (c.Analysis, error) {
	analysis, err := p.port.Analyze(ctx, request)
	if err != nil {
		p.failureCode = safeLiveAnalysisErrorCode(err)
	}
	return analysis, err
}

func safeLiveAnalysisErrorCode(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "research analysis document_understanding response:"):
		return "document_understanding_response_invalid"
	case strings.HasPrefix(message, "research analysis document_understanding:"):
		return "document_understanding_request_failed"
	case strings.HasPrefix(message, "research analysis claim_evidence_check response:"):
		return "claim_evidence_check_response_invalid"
	case strings.HasPrefix(message, "research analysis claim_evidence_check:"):
		return "claim_evidence_check_request_failed"
	default:
		return "analysis_port_failed"
	}
}

type liveFailingAnalysisPort struct{ err error }

func (p liveFailingAnalysisPort) Analyze(context.Context, engine.AnalysisRequest) (c.Analysis, error) {
	return c.Analysis{Calls: 1}, p.err
}

func TestLiveAnalysisCaptureRecordsOnlySafeFailureCode(t *testing.T) {
	secretDetails := "research analysis document_understanding: Get https://private.example/path?token=secret Authorization: bearer-secret"
	capture := &liveAnalysisCapture{port: liveFailingAnalysisPort{err: errors.New(secretDetails)}}
	_, err := capture.Analyze(context.Background(), engine.AnalysisRequest{})
	if err == nil {
		t.Fatal("Analyze() error = nil, want fixture error")
	}
	if capture.failureCode != "document_understanding_request_failed" {
		t.Fatalf("failure code = %q", capture.failureCode)
	}
	if strings.Contains(capture.failureCode, "private.example") || strings.Contains(capture.failureCode, "secret") || capture.failureCode == secretDetails {
		t.Fatal("captured live evidence contains raw analysis error details")
	}
}
