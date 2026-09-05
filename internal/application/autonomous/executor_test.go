package autonomous

import (
	"context"
	"errors"
	"strings"
	"testing"

	domaincontract "github.com/Nyukimin/RenCrow_CORE/internal/domain/contract"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRunExecutorPassesAndSavesEvidence(t *testing.T) {
	taskID := modulecore.NewTaskID()
	store := &reportStoreStub{}
	var stages []Stage
	result, err := RunExecutor(context.Background(), ExecuteRequest{
		TaskID:     taskID,
		Route:      "chat",
		Capability: CapabilityTTSDelivery,
		Contract:   testContract(),
		Execute: func(ctx context.Context, attempt int, failureKind, failureReason string) (AttemptResult, error) {
			if attempt != 0 || failureKind != "" || failureReason != "" {
				t.Fatalf("unexpected first attempt args: %d %q %q", attempt, failureKind, failureReason)
			}
			return AttemptResult{
				Response:      "done",
				Steps:         []string{"generate"},
				Verification:  []string{"audio file exists"},
				TTSProvider:   "rencrow-tts-gateway",
				TTSVoiceID:    "mio",
				TTSAudioFile:  "/tmp/out.wav",
				TTSDurationMS: 1200,
				PlaybackCmd:   "play /tmp/out.wav",
				PlaybackCode:  7,
			}, nil
		},
		Verify: func(ctx context.Context, c domaincontract.Contract, last AttemptResult) (bool, string, string, error) {
			if last.Response != "done" {
				t.Fatalf("unexpected last response: %#v", last)
			}
			return true, "", "", nil
		},
		Observe: func(stage Stage) {
			stages = append(stages, stage)
		},
		ReportStore: store,
	})
	if err != nil {
		t.Fatalf("RunExecutor failed: %v", err)
	}
	if result.Response != "done" || result.Report.Status != string(StatusPassed) {
		t.Fatalf("unexpected result: %#v", result)
	}
	if store.calls != 1 || store.last.Status != string(StatusPassed) {
		t.Fatalf("expected saved passed report, got calls=%d report=%#v", store.calls, store.last)
	}
	if store.last.TaskID != taskID || result.Report.TaskID != taskID {
		t.Fatalf("task ID was not preserved in execution evidence: result=%q saved=%q want=%q", result.Report.TaskID, store.last.TaskID, taskID)
	}
	if store.last.TTSProvider != "rencrow-tts-gateway" || store.last.TTSDuration != 1200 || store.last.PlaybackCode != 7 {
		t.Fatalf("TTS evidence was not propagated: %#v", store.last)
	}
	wantStages := []Stage{StageReceived, StageContractReady, StagePlanning, StageApplying, StageVerifying, StageCompleted}
	if strings.Join(stageNames(stages), ",") != strings.Join(stageNames(wantStages), ",") {
		t.Fatalf("unexpected stages: %#v", stages)
	}
}

func TestRunExecutorRetriesVerifyFailureThenPasses(t *testing.T) {
	var attempts []int
	var observedFailures []string
	result, err := RunExecutor(context.Background(), ExecuteRequest{
		TaskID:    modulecore.NewTaskID(),
		Contract:  testContract(),
		MaxRepair: 1,
		Execute: func(ctx context.Context, attempt int, failureKind, failureReason string) (AttemptResult, error) {
			attempts = append(attempts, attempt)
			observedFailures = append(observedFailures, failureKind+":"+failureReason)
			return AttemptResult{Response: "attempt"}, nil
		},
		Verify: func(ctx context.Context, c domaincontract.Contract, last AttemptResult) (bool, string, string, error) {
			if len(attempts) == 1 {
				return false, "verification_failed", "missing audio", nil
			}
			return true, "", "", nil
		},
	})
	if err != nil {
		t.Fatalf("RunExecutor failed: %v", err)
	}
	if result.Report.RepairCount != 1 || result.Report.AttemptCount != 2 {
		t.Fatalf("unexpected retry report: %#v", result.Report)
	}
	if observedFailures[1] != "verification_failed:missing audio" {
		t.Fatalf("repair attempt should receive previous failure, got %#v", observedFailures)
	}
}

func TestRunExecutorApplyFailureAndVerifyErrorPaths(t *testing.T) {
	t.Run("non retryable apply failure", func(t *testing.T) {
		result, err := RunExecutor(context.Background(), ExecuteRequest{
			TaskID:    modulecore.NewTaskID(),
			Contract:  testContract(),
			MaxRepair: 1,
			Execute: func(ctx context.Context, attempt int, failureKind, failureReason string) (AttemptResult, error) {
				return AttemptResult{Response: "partial", FailureKind: "permission_denied", FailureReason: "blocked"}, errors.New("permission denied")
			},
			Verify: func(ctx context.Context, c domaincontract.Contract, last AttemptResult) (bool, string, string, error) {
				t.Fatal("verify should not run after apply error")
				return false, "", "", nil
			},
		})
		if err == nil {
			t.Fatal("expected apply error")
		}
		if result.Report.RepairCount != 0 || result.Report.ErrorKind != "permission_denied" || result.Report.FailureReason != "blocked" {
			t.Fatalf("unexpected non-retry report: %#v", result.Report)
		}
	})

	t.Run("verify error retry exhausted", func(t *testing.T) {
		result, err := RunExecutor(context.Background(), ExecuteRequest{
			TaskID:    modulecore.NewTaskID(),
			Contract:  testContract(),
			MaxRepair: 1,
			Execute: func(ctx context.Context, attempt int, failureKind, failureReason string) (AttemptResult, error) {
				return AttemptResult{Response: "done"}, nil
			},
			Verify: func(ctx context.Context, c domaincontract.Contract, last AttemptResult) (bool, string, string, error) {
				return false, "", "", errors.New("verifier offline")
			},
		})
		if err == nil {
			t.Fatal("expected verify error")
		}
		if result.Report.RepairCount != 1 || result.Report.AttemptCount != 2 || result.Report.ErrorKind != "verify" {
			t.Fatalf("unexpected verify retry report: %#v", result.Report)
		}
	})
}

func TestRunExecutorValidationErrors(t *testing.T) {
	_, err := RunExecutor(context.Background(), ExecuteRequest{TaskID: modulecore.NewTaskID(), Contract: domaincontract.Contract{}})
	if err == nil {
		t.Fatal("expected contract validation error")
	}

	_, err = RunExecutor(context.Background(), ExecuteRequest{TaskID: modulecore.NewTaskID(), Contract: testContract()})
	if err == nil || !strings.Contains(err.Error(), "execute function is required") {
		t.Fatalf("expected missing execute error, got %v", err)
	}

	_, err = RunExecutor(context.Background(), ExecuteRequest{
		TaskID:   modulecore.NewTaskID(),
		Contract: testContract(),
		Execute: func(ctx context.Context, attempt int, failureKind, failureReason string) (AttemptResult, error) {
			return AttemptResult{}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "verify function is required") {
		t.Fatalf("expected missing verify error, got %v", err)
	}
}

func TestRunExecutorRejectsMissingOrMalformedTaskIDBeforeExecution(t *testing.T) {
	for _, taskID := range []modulecore.TaskID{"", "not-a-task-id"} {
		t.Run(string(taskID), func(t *testing.T) {
			executed := false
			store := &reportStoreStub{}
			_, err := RunExecutor(context.Background(), ExecuteRequest{
				TaskID:   taskID,
				Contract: testContract(),
				Execute: func(context.Context, int, string, string) (AttemptResult, error) {
					executed = true
					return AttemptResult{}, nil
				},
				Verify: func(context.Context, domaincontract.Contract, AttemptResult) (bool, string, string, error) {
					t.Fatal("verify must not run for an invalid task ID")
					return false, "", "", nil
				},
				ReportStore: store,
			})
			if err == nil || !strings.Contains(err.Error(), "task_id") {
				t.Fatalf("expected task_id validation error, got %v", err)
			}
			if executed || store.calls != 0 {
				t.Fatalf("invalid task ID crossed execution/persistence boundary: executed=%t saves=%d", executed, store.calls)
			}
		})
	}
}

func TestExecutorHelpers(t *testing.T) {
	if got := fallbackString("  ", "fallback"); got != "fallback" {
		t.Fatalf("fallbackString blank = %q", got)
	}
	if got := fallbackString("value", "fallback"); got != "value" {
		t.Fatalf("fallbackString value = %q", got)
	}

	tests := map[string]string{
		"proposal_missing_patch":        "proposal_invalid",
		"command not found":             "command_missing",
		"dependency module unavailable": "dependency_missing",
		"no such file or path":          "path_mismatch",
		"gateway model unavailable":     "provider_unavailable",
		"other failure":                 "apply",
	}
	for msg, want := range tests {
		if got := classifyApplyError(errors.New(msg)); got != want {
			t.Fatalf("classifyApplyError(%q) = %q, want %q", msg, got, want)
		}
	}
	if got := classifyApplyError(nil); got != "" {
		t.Fatalf("nil error classification = %q", got)
	}

	for _, kind := range []string{"proposal_invalid", "proposal_empty", "command_missing", "dependency_missing", "path_mismatch", "provider_unavailable", "verification_failed", "playback_failed", "verify", "apply", "non_executable_output", "tts_no_audio"} {
		if !retryableFailureKind(kind) {
			t.Fatalf("%q should be retryable", kind)
		}
	}
	if retryableFailureKind("permission_denied") {
		t.Fatal("permission_denied should not be retryable")
	}
	if retryableFailureKind("policy_blocked") {
		t.Fatal("policy_blocked should not be retryable")
	}
}

func stageNames(stages []Stage) []string {
	out := make([]string, 0, len(stages))
	for _, stage := range stages {
		out = append(out, string(stage))
	}
	return out
}
