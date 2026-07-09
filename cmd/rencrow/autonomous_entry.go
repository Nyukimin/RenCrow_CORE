package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	entryadapter "github.com/Nyukimin/RenCrow_CORE/internal/adapter/entry"
	autonomousapp "github.com/Nyukimin/RenCrow_CORE/internal/application/autonomous"
	contractapp "github.com/Nyukimin/RenCrow_CORE/internal/application/contract"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domaincontract "github.com/Nyukimin/RenCrow_CORE/internal/domain/contract"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	executionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/execution"
	ttsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tts"
)

// messageProcessor is used by viewer/entry adapters.
type messageProcessor = orchestrator.Orchestrator

type ttsSynthesizer interface {
	Synthesize(ctx context.Context, in ttsinfra.SynthesisInput) (ttsinfra.SynthesisOutput, error)
}

type ttsPlayer interface {
	Play(ctx context.Context, audioPath string) (ttsinfra.PlaybackResult, error)
}

type ttsEntryRuntime struct {
	synthesizer ttsSynthesizer
	player      ttsPlayer
	outputDir   string
	voiceID     string
}

func (r ttsEntryRuntime) configured() bool {
	return r.synthesizer != nil && r.player != nil
}

type ttsEntryPlanner struct {
	requireTTS bool
}

type ttsEntryApplier struct {
	proc    messageProcessor
	req     entryadapter.Request
	latest  orchestrator.ProcessMessageResponse
	runtime ttsEntryRuntime
	synth   ttsinfra.SynthesisOutput
	play    ttsinfra.PlaybackResult
	errKind string
}

type ttsEntryVerifier struct {
	applier         *ttsEntryApplier
	requirePlayback bool
}

type ttsEntryRepairer struct {
	requireTTS bool
}

func processEntryRequest(ctx context.Context, proc messageProcessor, req entryadapter.Request, reportPath string) (entryadapter.Result, error) {
	return processEntryRequestWithRuntime(ctx, proc, req, reportPath, ttsEntryRuntime{})
}

func processEntryRequestWithRuntime(ctx context.Context, proc messageProcessor, req entryadapter.Request, reportPath string, runtime ttsEntryRuntime) (entryadapter.Result, error) {
	contract, err := contractapp.NormalizeRequest(req.Message)
	if err != nil {
		return entryadapter.Result{}, err
	}
	log.Printf("[entry] contract goal=%q acceptance=%d verification=%d", contract.Goal, len(contract.Acceptance), len(contract.Verification))

	if !isTTSRequest(req.Message) {
		resp, err := runProcessMessage(ctx, proc, req)
		if err != nil {
			return entryadapter.Result{}, err
		}
		return toEntryResult(req.SessionID, resp, ""), nil
	}
	if !runtime.configured() {
		return entryadapter.Result{}, fmt.Errorf("tts runtime is not configured")
	}

	store, err := executionpersistence.NewJSONLReportStore(reportPath)
	if err != nil {
		return entryadapter.Result{}, fmt.Errorf("create report store: %w", err)
	}

	applier := &ttsEntryApplier{proc: proc, req: req, runtime: runtime}
	svc := autonomousapp.NewService(
		ttsEntryPlanner{requireTTS: true},
		applier,
		ttsEntryVerifier{applier: applier, requirePlayback: true},
		ttsEntryRepairer{requireTTS: true},
		1,
	).WithReportStore(store)

	runReport, err := svc.Run(ctx, contract)
	writeErr := saveTTSEvidence(ctx, store, contract, runReport, applier, err)
	if writeErr != nil {
		log.Printf("WARN: save tts evidence failed: %v", writeErr)
	}
	if err != nil {
		return entryadapter.Result{}, err
	}
	evidenceJobID := runReport.JobID
	if evidenceJobID == "" {
		evidenceJobID = applier.latest.JobID
	}
	evidenceRef := "execution_report:" + evidenceJobID
	return toEntryResult(req.SessionID, applier.latest, evidenceRef), nil
}

func runProcessMessage(ctx context.Context, proc messageProcessor, req entryadapter.Request) (orchestrator.ProcessMessageResponse, error) {
	return proc.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
		SessionID:   req.SessionID,
		Channel:     req.Channel,
		ChatID:      req.UserID,
		UserMessage: req.Message,
	})
}

func toEntryResult(sessionID string, resp orchestrator.ProcessMessageResponse, evidenceRef string) entryadapter.Result {
	return entryadapter.Result{
		SessionID:   sessionID,
		Route:       string(resp.Route),
		JobID:       resp.JobID,
		Response:    resp.Response,
		EvidenceRef: evidenceRef,
	}
}

func isTTSRequest(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	if strings.Contains(lower, "tts") {
		return true
	}
	return strings.Contains(message, "音声")
}

func defaultExecutionReportPath(workspaceDir string) string {
	return filepath.Join(workspaceDir, "execution_report.jsonl")
}

func buildTTSEntryRuntime(cfg *config.Config) ttsEntryRuntime {
	if cfg == nil || !cfg.TTS.Enabled {
		return ttsEntryRuntime{}
	}

	synthesizer := buildFallbackTTSSynthesizer(cfg)
	if synthesizer == nil {
		return ttsEntryRuntime{}
	}

	cmds := buildTTSCommandSpecs(cfg)
	if len(cmds) == 0 {
		return ttsEntryRuntime{}
	}

	return ttsEntryRuntime{
		synthesizer: synthesizer,
		player:      ttsinfra.NewCommandPlayer(cmds),
		outputDir:   cfg.TTS.OutputDir,
		voiceID:     chooseTTSVoiceID(cfg),
	}
}

func (p ttsEntryPlanner) Plan(_ context.Context, _ domaincontract.Contract) (autonomousapp.Plan, error) {
	steps := []autonomousapp.Step{{Name: "process-message"}}
	if p.requireTTS {
		steps = append(steps, autonomousapp.Step{Name: "tts-synthesize"}, autonomousapp.Step{Name: "tts-playback"})
	}
	return autonomousapp.Plan{Steps: steps}, nil
}

func (a *ttsEntryApplier) Apply(ctx context.Context, step autonomousapp.Step) error {
	if step.Name == "" {
		return fmt.Errorf("step name is required")
	}
	switch step.Name {
	case "process-message", "retry-process-message":
		resp, err := runProcessMessage(ctx, a.proc, a.req)
		if err != nil {
			return err
		}
		a.latest = resp
		return nil
	case "tts-synthesize":
		text := strings.TrimSpace(a.latest.Response)
		if text == "" {
			text = strings.TrimSpace(a.req.Message)
		}
		out, err := a.runtime.synthesizer.Synthesize(ctx, ttsinfra.SynthesisInput{
			Text:       text,
			OutputDir:  a.runtime.outputDir,
			FilePrefix: "entry-tts",
			VoiceProfile: ttsinfra.VoiceProfile{
				VoiceID: a.runtime.voiceID,
			},
		})
		if err != nil {
			a.errKind = "synthesize"
			return err
		}
		a.synth = out
		return nil
	case "tts-playback":
		if strings.TrimSpace(a.synth.AudioFilePath) == "" {
			a.errKind = "synthesize"
			return fmt.Errorf("audio file path is empty")
		}
		result, err := a.runtime.player.Play(ctx, a.synth.AudioFilePath)
		a.play = result
		if err != nil {
			a.errKind = "playback"
			return err
		}
		if result.ExitCode != 0 {
			a.errKind = "playback"
			return fmt.Errorf("playback exit code=%d", result.ExitCode)
		}
		return nil
	default:
		return fmt.Errorf("unknown step: %s", step.Name)
	}
}

func (v ttsEntryVerifier) Verify(_ context.Context, _ domaincontract.Contract) (bool, string, error) {
	if strings.TrimSpace(v.applier.latest.Response) == "" {
		return false, "empty response", nil
	}
	if v.requirePlayback && v.applier.play.ExitCode != 0 {
		return false, fmt.Sprintf("playback exit code=%d", v.applier.play.ExitCode), nil
	}
	return true, "", nil
}

func (r ttsEntryRepairer) Repair(_ context.Context, _ domaincontract.Contract, _ string) (autonomousapp.Plan, error) {
	steps := []autonomousapp.Step{{Name: "retry-process-message"}}
	if r.requireTTS {
		steps = append(steps, autonomousapp.Step{Name: "tts-synthesize"}, autonomousapp.Step{Name: "tts-playback"})
	}
	return autonomousapp.Plan{Steps: steps}, nil
}

func saveTTSEvidence(
	ctx context.Context,
	store *executionpersistence.JSONLReportStore,
	contract domaincontract.Contract,
	runReport autonomousapp.Report,
	applier *ttsEntryApplier,
	runErr error,
) error {
	if store == nil {
		return nil
	}
	now := time.Now().UTC()
	status := "failed"
	if runReport.Status == autonomousapp.StatusPassed {
		status = "passed"
	}
	verification := make([]string, 0, len(contract.Verification)+len(runReport.VerificationLog))
	verification = append(verification, contract.Verification...)
	verification = append(verification, runReport.VerificationLog...)
	reason := runReport.Reason
	if reason == "" && runErr != nil {
		reason = runErr.Error()
	}
	report := domainexecution.ExecutionReport{
		JobID:         runReport.JobID,
		Goal:          contract.Goal,
		Route:         string(applier.latest.Route),
		Capability:    string(autonomousapp.CapabilityTTSDelivery),
		Status:        status,
		ErrorKind:     runReport.ErrorKind,
		FailureReason: reason,
		TTSErrorKind:  applier.errKind,
		TTSProvider:   applier.synth.Provider,
		TTSVoiceID:    applier.synth.VoiceID,
		TTSAudioFile:  applier.synth.AudioFilePath,
		TTSDuration:   applier.synth.DurationMS,
		PlaybackCmd:   applier.play.Command,
		PlaybackCode:  applier.play.ExitCode,
		Acceptance:    contract.Acceptance,
		Constraints:   contract.Constraints,
		Artifacts:     contract.Artifacts,
		Verification:  verification,
		Rollback:      contract.Rollback,
		Steps:         append([]string{}, runReport.ExecutedSteps...),
		AttemptCount:  runReport.AttemptCount,
		RepairCount:   runReport.RepairCount,
		Error:         reason,
		CreatedAt:     now,
		FinishedAt:    now,
	}
	return store.Save(ctx, report)
}
