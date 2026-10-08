package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// This file is the CORE-side port of the execution profile
// shiro_native_coding_v1 (docs 02 / 03 / 04, RenCrow_Harness sections).
//
// A turn that the admission step of the orchestrator selected for the profile
// (conversation.BackendShiroNativeCodingV1) is handed to RenCrow_Harness through
// NativeCodingDelegate, exactly once, before the CodexWorkPath, the
// SubagentManager (toolloop) and the plain Generate path are considered. A
// selected turn never enters those paths, whatever the delegate returns: an
// unconfigured delegate, a start failure, a cancellation and every RunResult
// that is not a verified success end as an error or a typed result, never as a
// fallback to the old routes.

// HarnessOwner is the owner value of every reference to an ID that
// RenCrow_Harness issued. Such an ID is only ever recorded together with its
// owner, because the same text could be another owner's ID.
const HarnessOwner = "RenCrow_Harness"

// ExternalRef is an ID issued by another owner, kept with that owner.
type ExternalRef struct {
	Owner string `json:"owner"`
	ID    string `json:"id"`
}

// NativeCodingRunStatus is RunResult.status of RenCrow_Harness: the closed set
// of seven ways a Run ends. completed means a valid final answer was settled;
// it does not mean the Task is done.
type NativeCodingRunStatus string

const (
	NativeRunCompleted       NativeCodingRunStatus = "completed"
	NativeRunIncomplete      NativeCodingRunStatus = "incomplete"
	NativeRunRejected        NativeCodingRunStatus = "rejected"
	NativeRunBlocked         NativeCodingRunStatus = "blocked"
	NativeRunCancelled       NativeCodingRunStatus = "cancelled"
	NativeRunFailed          NativeCodingRunStatus = "failed"
	NativeRunRestartRequired NativeCodingRunStatus = "restart_required"
)

// NativeCodingVerification is RunResult.verification.status of RenCrow_Harness.
// It is a separate statement from the Run status.
type NativeCodingVerification string

const (
	NativeVerificationPassed  NativeCodingVerification = "passed"
	NativeVerificationFailed  NativeCodingVerification = "failed"
	NativeVerificationNotRun  NativeCodingVerification = "not_run"
	NativeVerificationUnknown NativeCodingVerification = "unknown"
)

// NativeCodingRequest is one delegation. Messages is the typed prompt context
// of the Shiro assembly (Character, Stable, Recall and Variable messages in the
// standard order, the current user message last); the classification is read
// from the types, never recovered from flattened text.
type NativeCodingRequest struct {
	Input    conversation.TurnInput
	Messages []llm.Message
}

// NativeCodingResult is CORE's projection of one delegation: the RunResult of
// RenCrow_Harness kept as the typed values it is, plus the references that
// correlate the CORE Action/Attempt with the Harness Task, Run and receipt.
type NativeCodingResult struct {
	Status       NativeCodingRunStatus
	Code         string
	FinalText    string
	Verification NativeCodingVerification
	Resumable    bool

	// Harness-owned IDs, always with their owner.
	HarnessTask    ExternalRef
	HarnessRun     ExternalRef
	HarnessReceipt ExternalRef

	// CORE-owned delegation Action and Attempt.
	ActionID  modulecore.ActionID
	AttemptID modulecore.AttemptID
}

// Accepted reports the only combination CORE reads as a verified success of the
// delegated Run: it completed and its verification passed. completed alone is
// not an acceptance of the parent Task.
func (r NativeCodingResult) Accepted() bool {
	return r.Status == NativeRunCompleted && r.Verification == NativeVerificationPassed
}

// NativeCodingDelegate is implemented by the adapter that talks to
// RenCrow_Harness. It returns a result for every end the Harness determined
// (all seven statuses) and an error only when no RunResult exists: the work
// was not admitted (ErrNativeCodingRejected, ErrNativeCodingBlocked), or its
// outcome cannot be known (ErrNativeCodingOutcomeUnknown). It never falls back
// to another executor.
type NativeCodingDelegate interface {
	DelegateNativeCoding(ctx context.Context, request NativeCodingRequest) (NativeCodingResult, error)
}

var (
	// ErrNativeCodingRejected reports a delegation that was refused because its
	// input or the profile cannot supply what the work needs (for example an
	// attachment). Nothing ran, and the old routes are not tried.
	ErrNativeCodingRejected = errors.New("native coding delegation rejected")
	// ErrNativeCodingBlocked reports a delegation that cannot run now (the
	// Harness is unavailable or not the build CORE pinned). Nothing ran, and the
	// old routes are not tried.
	ErrNativeCodingBlocked = errors.New("native coding delegation blocked")
	// ErrNativeCodingOutcomeUnknown reports a delegation that was handed to the
	// Harness but whose end CORE could not learn (the connection ended, or a stop
	// was recorded and the Run did not end in time). The work is not run again
	// under a new key.
	ErrNativeCodingOutcomeUnknown = errors.New("native coding delegation outcome unknown")
)

// NativeCodingError is returned by Execute for a Run that the Harness
// determined and that is not a verified success to nil error: any status other
// than completed, and completed with a failed verification. The typed result is
// kept, so a caller reads the Harness status, code and references with
// errors.As instead of parsing text.
type NativeCodingError struct {
	Result NativeCodingResult
}

func (e *NativeCodingError) Error() string {
	code := e.Result.Code
	if code == "" {
		code = "-"
	}
	return fmt.Sprintf("native coding run ended %s (code=%s, verification=%s)", e.Result.Status, code, e.Result.Verification)
}

// IsNativeCodingFailure reports whether err is any outcome of the native
// coding profile that is not a success: a typed Run end, a refusal, a block or
// an unknown outcome. The orchestrator uses it so such an end is neither
// repaired by re-delegating nor retried through another route.
func IsNativeCodingFailure(err error) bool {
	var runErr *NativeCodingError
	return errors.As(err, &runErr) ||
		errors.Is(err, ErrNativeCodingRejected) ||
		errors.Is(err, ErrNativeCodingBlocked) ||
		errors.Is(err, ErrNativeCodingOutcomeUnknown)
}

// unverifiedNote is the one fixed sentence CORE appends to a completed answer
// whose verification is not passed. It is generated by CORE, so it cannot be
// mistaken for text of the Harness, and it carries the verification status.
func unverifiedNote(status NativeCodingVerification) string {
	return fmt.Sprintf("\n\n※ この結果は検証されていません（verification=%s）。", status)
}

// WithNativeCodingDelegate sets the delegate of the shiro_native_coding_v1
// profile. Without it, a turn that the admission step selected for the profile
// ends as ErrNativeCodingBlocked; it does not run on an old route.
func (s *ShiroAgent) WithNativeCodingDelegate(delegate NativeCodingDelegate) *ShiroAgent {
	s.nativeCoding = delegate
	return s
}

// executeNativeCoding is the branch of Execute for a turn that carries the
// backend selection shiro_native_coding_v1. It is the only thing that happens
// to such a turn: no other route is tried, whatever the outcome.
func (s *ShiroAgent) executeNativeCoding(ctx context.Context, t conversation.TurnInput, characterPrompt string, dynamic []llm.Message) (string, error) {
	if s.nativeCoding == nil {
		return "", fmt.Errorf("%w: no delegate is configured", ErrNativeCodingBlocked)
	}
	if len(t.Attachments()) > 0 {
		return "", fmt.Errorf("%w: attachments cannot be passed to the Harness", ErrNativeCodingRejected)
	}
	messages := append([]llm.Message(nil), dynamic...)
	var recallPack *conversation.RecallPack
	if s.conversation != nil {
		pack, err := s.conversation.BeginTurn(ctx, t.SessionID(), t.MessageText())
		if err != nil {
			log.Printf("[Shiro] BeginTurn failed for the native coding delegation: %v", err)
		} else if pack != nil {
			filtered := pack.FilterForRole("worker").WithoutPersonaSystemPrompt()
			recallPack = &filtered
			messages = append(messages, recallPack.ToPromptMessages()...)
		}
	}
	typed := assemblePromptContext(
		characterPrompt,
		currentRuntimeContext(ctx, s.runtimeContextProvider, "shiro", s.stableRuntimeContext),
		messages,
		llm.Message{Role: "user", Content: strings.TrimSpace(t.MessageText())},
	)
	result, err := s.nativeCoding.DelegateNativeCoding(ctx, NativeCodingRequest{Input: t, Messages: typed})
	if err != nil {
		return "", err
	}
	response, err := nativeCodingResponse(result)
	if err != nil {
		return response, err
	}
	if s.conversation != nil {
		if commitErr := commitConversationTurn(ctx, s.conversation, t, t.SessionID(), t.MessageText(), response, conversation.SpeakerShiro, recallPack); commitErr != nil {
			return response, commitErr
		}
	}
	return response, nil
}

// nativeCodingResponse converts a delegation result to the string/error form of
// Execute. Only a completed Run is a nil error; completed is not read as the
// Task's success: a failed verification is an error, and a verification that
// was not run is said so in the answer.
func nativeCodingResponse(result NativeCodingResult) (string, error) {
	switch {
	case result.Status != NativeRunCompleted:
		return result.FinalText, &NativeCodingError{Result: result}
	case result.Verification == NativeVerificationFailed:
		return result.FinalText, &NativeCodingError{Result: result}
	case result.Verification == NativeVerificationPassed:
		return result.FinalText, nil
	default:
		return result.FinalText + unverifiedNote(result.Verification), nil
	}
}
