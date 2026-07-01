package autonomous

import (
	"context"
	"fmt"
	"strings"
	"time"

	domaincontract "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/contract"
	domainexecution "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/execution"
)

type CapabilityPack string

const (
	CapabilityGenericExecution CapabilityPack = "generic_execution"
	CapabilityCodeChange       CapabilityPack = "code_change"
	CapabilityTTSDelivery      CapabilityPack = "tts_delivery"
)

type Stage string

const (
	StageReceived      Stage = "received"
	StageContractReady Stage = "contract_ready"
	StagePlanning      Stage = "planning"
	StageApplying      Stage = "applying"
	StageVerifying     Stage = "verifying"
	StageRepairing     Stage = "repairing"
	StageCompleted     Stage = "completed"
	StageFailed        Stage = "failed"
)

type AttemptResult struct {
	Response      string
	Steps         []string
	Verification  []string
	FailureKind   string
	FailureReason string
	TTSProvider   string
	TTSVoiceID    string
	TTSAudioFile  string
	TTSDurationMS int
	PlaybackCmd   string
	PlaybackCode  int
	TTSErrorKind  string
}

type ExecuteFunc func(ctx context.Context, attempt int, failureKind, failureReason string) (AttemptResult, error)

type VerifyFunc func(ctx context.Context, c domaincontract.Contract, last AttemptResult) (ok bool, failureKind, failureReason string, err error)

type StageObserver func(stage Stage)

type ExecuteRequest struct {
	JobID       string
	Route       string
	Capability  CapabilityPack
	Contract    domaincontract.Contract
	MaxRepair   int
	Execute     ExecuteFunc
	Verify      VerifyFunc
	Observe     StageObserver
	ReportStore ReportStore
}

type ExecuteResult struct {
	Response string
	Report   domainexecution.ExecutionReport
}

func RunExecutor(ctx context.Context, req ExecuteRequest) (ExecuteResult, error) {
	if err := req.Contract.Validate(); err != nil {
		return ExecuteResult{}, err
	}
	if req.Execute == nil {
		return ExecuteResult{}, fmt.Errorf("execute function is required")
	}
	if req.Verify == nil {
		return ExecuteResult{}, fmt.Errorf("verify function is required")
	}

	maxRepair := req.MaxRepair
	if maxRepair < 0 {
		maxRepair = 0
	}
	observe := func(stage Stage) {
		if req.Observe != nil {
			req.Observe(stage)
		}
	}

	now := time.Now().UTC()
	report := domainexecution.ExecutionReport{
		JobID:        fallbackID(req.JobID, fmt.Sprintf("job-%d", now.UnixNano())),
		Goal:         req.Contract.Goal,
		Route:        req.Route,
		Capability:   string(req.Capability),
		Status:       string(StatusFailed),
		Acceptance:   append([]string{}, req.Contract.Acceptance...),
		Constraints:  append([]string{}, req.Contract.Constraints...),
		Artifacts:    append([]string{}, req.Contract.Artifacts...),
		Verification: append([]string{}, req.Contract.Verification...),
		Rollback:     append([]string{}, req.Contract.Rollback...),
		CreatedAt:    now,
		FinishedAt:   now,
	}

	observe(StageReceived)
	observe(StageContractReady)
	observe(StagePlanning)

	var last AttemptResult
	failureKind := ""
	failureReason := ""

	for attempt := 0; ; attempt++ {
		report.AttemptCount++
		observe(StageApplying)

		res, err := req.Execute(ctx, attempt, failureKind, failureReason)
		last = res
		report.Steps = append(report.Steps, res.Steps...)
		report.Verification = append(report.Verification, res.Verification...)
		report.TTSProvider = fallbackString(res.TTSProvider, report.TTSProvider)
		report.TTSVoiceID = fallbackString(res.TTSVoiceID, report.TTSVoiceID)
		report.TTSAudioFile = fallbackString(res.TTSAudioFile, report.TTSAudioFile)
		if res.TTSDurationMS > 0 {
			report.TTSDuration = res.TTSDurationMS
		}
		report.PlaybackCmd = fallbackString(res.PlaybackCmd, report.PlaybackCmd)
		if res.PlaybackCode != 0 {
			report.PlaybackCode = res.PlaybackCode
		}
		report.TTSErrorKind = fallbackString(res.TTSErrorKind, report.TTSErrorKind)

		if err != nil {
			failureKind = fallbackString(res.FailureKind, classifyApplyError(err))
			failureReason = fallbackString(res.FailureReason, err.Error())
			report.ErrorKind = failureKind
			report.FailureReason = failureReason
			report.Error = err.Error()
			if attempt < maxRepair && retryableFailureKind(failureKind) {
				report.RepairCount++
				observe(StageRepairing)
				continue
			}
			report.FinishedAt = time.Now().UTC()
			observe(StageFailed)
			saveExecutionEvidence(ctx, req.ReportStore, report)
			return ExecuteResult{Response: last.Response, Report: report}, fmt.Errorf("apply failed: %w", err)
		}

		observe(StageVerifying)
		ok, verifyKind, verifyReason, verifyErr := req.Verify(ctx, req.Contract, last)
		if verifyErr != nil {
			failureKind = fallbackString(verifyKind, "verify")
			failureReason = fallbackString(verifyReason, verifyErr.Error())
			report.ErrorKind = failureKind
			report.FailureReason = failureReason
			report.Error = verifyErr.Error()
			if attempt < maxRepair && retryableFailureKind(failureKind) {
				report.RepairCount++
				observe(StageRepairing)
				continue
			}
			report.FinishedAt = time.Now().UTC()
			observe(StageFailed)
			saveExecutionEvidence(ctx, req.ReportStore, report)
			return ExecuteResult{Response: last.Response, Report: report}, fmt.Errorf("verify failed: %w", verifyErr)
		}
		if ok {
			report.Status = string(StatusPassed)
			report.ErrorKind = ""
			report.FailureReason = ""
			report.Error = ""
			report.FinishedAt = time.Now().UTC()
			observe(StageCompleted)
			saveExecutionEvidence(ctx, req.ReportStore, report)
			return ExecuteResult{Response: last.Response, Report: report}, nil
		}

		failureKind = fallbackString(verifyKind, "verification_failed")
		failureReason = fallbackString(verifyReason, "verification failed")
		report.ErrorKind = failureKind
		report.FailureReason = failureReason
		report.Error = failureReason
		if attempt < maxRepair && retryableFailureKind(failureKind) {
			report.RepairCount++
			observe(StageRepairing)
			continue
		}
		report.FinishedAt = time.Now().UTC()
		observe(StageFailed)
		saveExecutionEvidence(ctx, req.ReportStore, report)
		return ExecuteResult{Response: last.Response, Report: report}, fmt.Errorf("verification failed: %s", failureReason)
	}
}

func saveExecutionEvidence(ctx context.Context, store ReportStore, report domainexecution.ExecutionReport) {
	if store == nil {
		return
	}
	_ = store.Save(ctx, report)
}

func fallbackID(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func fallbackString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func classifyApplyError(err error) string {
	if err == nil {
		return ""
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "approval required"):
		return "approval_required"
	case strings.Contains(lower, "proposal_empty"), strings.Contains(lower, "proposal_missing"), strings.Contains(lower, "proposal_invalid"):
		return "proposal_invalid"
	case strings.Contains(lower, "command not found"), strings.Contains(lower, "exit status 127"), strings.Contains(lower, "not found"):
		return "command_missing"
	case strings.Contains(lower, "dependency"), strings.Contains(lower, "module"), strings.Contains(lower, "package"):
		return "dependency_missing"
	case strings.Contains(lower, "path"), strings.Contains(lower, "no such file"):
		return "path_mismatch"
	case strings.Contains(lower, "provider"), strings.Contains(lower, "ollama"), strings.Contains(lower, "model"):
		return "provider_unavailable"
	default:
		return "apply"
	}
}

func retryableFailureKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "proposal_invalid", "proposal_empty", "command_missing", "dependency_missing", "path_mismatch", "provider_unavailable",
		"verification_failed", "playback_failed", "verify", "apply",
		"non_executable_output", "tts_no_audio":
		return true
	default:
		return false
	}
}
