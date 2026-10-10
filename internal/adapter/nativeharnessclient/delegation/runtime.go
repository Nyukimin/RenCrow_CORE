package delegation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_Harness/pkg/client"
	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/nativeharnessclient"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Client is the part of the Harness protocol client (pkg/client) this package
// uses. *client.Client satisfies it; the narrow interface is the seam that lets
// the delegation be tested against a scripted Harness without a child process.
type Client interface {
	Capabilities() protocol.CapabilitiesResult
	SessionOpen(ctx context.Context, in protocol.SessionOpenInput) (protocol.SessionOpenResult, error)
	TurnStart(ctx context.Context, in protocol.StartInput) (protocol.StartResult, error)
	RunResume(ctx context.Context, in protocol.ResumeInput) (protocol.ResumeResult, error)
	RunGet(ctx context.Context, in protocol.RunGetInput) (protocol.RunInfo, error)
	SessionGet(ctx context.Context, in protocol.SessionGetInput) (protocol.SessionInfo, error)
	ReceiptGet(ctx context.Context, in protocol.ReceiptGetInput) (protocol.ReceiptRecord, error)
	EventsRead(ctx context.Context, in protocol.EventsReadInput) (protocol.EventsReadResult, error)
	EvidenceRead(ctx context.Context, in protocol.EvidenceReadInput) (protocol.EvidenceReadResult, error)
	InterruptRun(ctx context.Context, runID, keyPrefix string) (protocol.InterruptReceipt, error)
	AwaitRun(ctx context.Context, runID string, opts client.AwaitOptions) (protocol.RunResult, error)
	Notifications() <-chan client.Notification
	Done() <-chan struct{}
	Shutdown(ctx context.Context, in protocol.ShutdownInput) error
	Abort()
}

// StartFunc starts a Harness client. The production one is client.Start.
type StartFunc func(ctx context.Context, cfg client.Config) (Client, error)

// OriginProofSigner is the narrow issuer port used only when the accepted
// Conversation record declares a Human relay. Automation never calls it.
type OriginProofSigner interface {
	Sign(nativeharnessclient.OriginProofRequest) (nativeharnessclient.OriginProof, error)
}

func startHarness(ctx context.Context, cfg client.Config) (Client, error) {
	c, err := client.Start(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func clientDefaultCapabilities() []string { return client.DefaultRequiredCapabilities() }

// ActionOwner is the typed persistence boundary of one native delegation.
// actionmanager.Manager is the canonical owner and sole issuer of ActionID and
// AttemptID; this adapter does not create a second store or generic retry path.
type ActionOwner interface {
	EnsureNativeDelegation(ctx context.Context, input actionmanager.EnsureNativeDelegationInput) (domainaction.Action, domainaction.Attempt, error)
	PrepareNativeMutation(ctx context.Context, input actionmanager.PrepareNativeMutationInput) (domainaction.Action, domainaction.Attempt, error)
	MarkNativeMutationDeliveryUnknown(ctx context.Context, actionID modulecore.ActionID, attemptID modulecore.AttemptID, slot domainaction.NativeMutationSlot) (domainaction.Action, domainaction.Attempt, error)
	RecordNativeMutationOutcome(ctx context.Context, input actionmanager.RecordNativeMutationOutcomeInput) (domainaction.Action, domainaction.Attempt, error)
	RecordNativeObservation(ctx context.Context, input actionmanager.RecordNativeObservationInput) (domainaction.Action, domainaction.Attempt, error)
	CompleteNativeDelegation(ctx context.Context, input actionmanager.CompleteNativeDelegationInput) (domainaction.Action, domainaction.Attempt, error)
}

// ResumeActionReader is the read-only action history needed to bind a user
// Resume to a saved terminal native delegation. It is intentionally separate
// from the write port so non-Resume test adapters need no broader capability.
type ResumeActionReader interface {
	ListActions(ctx context.Context, filter domainaction.Filter) ([]domainaction.Action, error)
	ListAttempts(ctx context.Context, filter domainaction.AttemptFilter) ([]domainaction.Attempt, error)
}

// TaskOwner is the existing canonical Task lifecycle owner. Get is used before
// entering the fence to bind the accepted OPS receipt; ExecuteRunEffect holds
// the current Task/Run generation fence during each short external effect.
type TaskOwner interface {
	Get(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error)
	ExecuteRunEffect(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, effect func(context.Context) error) error
	ExecuteNativeOPSResumeActionEffect(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, claim domaintask.NativeOPSResumeClaim, effect func(context.Context) error) error
	CompleteNativeOPSResumeRun(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, claim domaintask.NativeOPSResumeClaim, status domaintask.Status, summary, waitingReason string) (domaintask.Task, error)
	VerifyNativeOPSResumeReplay(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, claim domaintask.NativeOPSResumeClaim, status domaintask.Status) error
}

// Options are the injection points of a Runtime. The zero value is the
// production composition.
type Options struct {
	// Start replaces client.Start (tests).
	Start StartFunc
	// Now replaces the clock (tests).
	Now func() time.Time
	// Log receives one JSON line per event; nil writes to the standard logger.
	Log func(line string)
	// Backoff is the wait before resending after an unknown outcome, per resend
	// (1-based). Nil is 200ms doubling per resend.
	Backoff func(resend int) time.Duration
	// OriginProofSigner signs only a validated, owner-read Human source record.
	// Nil keeps Automation available and blocks a Human relay before Start.
	OriginProofSigner OriginProofSigner
}

// Failure codes of a start, in refusals and logs. They name the cause without
// carrying a path or the Harness's own message.
const (
	codeClosed                   = "runtime_closed"
	codeStartFailed              = "harness_start_failed"
	codeIncompatible             = "harness_incompatible"
	codeBuildMismatch            = "harness_build_revision_mismatch"
	codeCooldown                 = "harness_restart_cooldown"
	codeWorkspaceAbsent          = "workspace_unavailable"
	codeTaskUnavailable          = "task_run_unavailable"
	codeScopeInvalid             = "execution_scope_invalid"
	codeAcceptedInputUnavailable = "accepted_input_unavailable"
	codeOriginProofUnavailable   = "origin_proof_unavailable"
)

// Runtime supervises one Harness client for the profile and implements both
// ports of the profile: the admission of the orchestrator
// (AdmitNativeCoding) and the delegate of Shiro (DelegateNativeCoding). It is
// safe for concurrent use: the client is acquired and replaced under one mutex,
// so a start is never done twice and a closed Runtime never starts again.
type Runtime struct {
	settings          Settings
	start             StartFunc
	actions           ActionOwner
	tasks             TaskOwner
	log               *logger
	now               func() time.Time
	backoff           func(resend int) time.Duration
	originProofSigner OriginProofSigner

	mu          sync.Mutex
	current     Client
	closed      bool
	failedAt    time.Time
	failureCode string
	runGates    map[string]*runGate

	threads correlations // Harness thread ID -> the delegation that used it
}

// NewRuntime validates the settings and returns a Runtime that has not started
// the Harness. Call Warm to start it eagerly (the first admission otherwise
// does), and Close at shutdown.
func NewRuntime(settings Settings, actions ActionOwner, tasks TaskOwner, opts Options) (*Runtime, error) {
	settings = settings.withDefaults()
	if err := settings.validate(); err != nil {
		return nil, err
	}
	if actions == nil || tasks == nil {
		return nil, fmt.Errorf("%w: canonical Action and Task owners are required", ErrInvalidSettings)
	}
	r := &Runtime{settings: settings, start: opts.Start, actions: actions, tasks: tasks, now: opts.Now, backoff: opts.Backoff, originProofSigner: opts.OriginProofSigner, runGates: make(map[string]*runGate)}
	if r.start == nil {
		r.start = startHarness
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.backoff == nil {
		r.backoff = func(resend int) time.Duration { return 200 * time.Millisecond << (resend - 1) }
	}
	r.log = newLogger(opts.Log, r.now)
	return r, nil
}

func blocked(code string) error {
	return fmt.Errorf("%w: %s", agent.ErrNativeCodingBlocked, code)
}

func rejected(code string) error {
	return fmt.Errorf("%w: %s", agent.ErrNativeCodingRejected, code)
}

// Warm starts the Harness now and reports the refusal that the next admission
// would give. Composition calls it once so a Harness that cannot be used shows
// at startup; a failure does not stop CORE (an admission is then blocked).
func (r *Runtime) Warm(ctx context.Context) error {
	_, err := r.acquire(ctx)
	return err
}

// AdmitNativeCoding implements the admission of the profile
// (orchestrator.NativeCodingAdmission). It checks that the work can be handed
// over now: nothing in the turn the Harness cannot receive (an attachment), the
// workspace is there, and a Harness of the pinned build is running. A refusal is
// typed (agent.ErrNativeCodingRejected or agent.ErrNativeCodingBlocked) and ends
// the turn; no old route is tried.
func (r *Runtime) AdmitNativeCoding(ctx context.Context, input conversation.TurnInput) error {
	fail := func(err error, reason string) error {
		r.log.emit("warn", eventAdmissionRefused, withTurn(map[string]any{"reason": reason}, input))
		return err
	}
	if len(input.Attachments()) > 0 {
		return fail(rejected("attachments_unsupported"), "attachments_unsupported")
	}
	if strings.TrimSpace(input.MessageText()) == "" {
		return fail(rejected("empty_request"), "empty_request")
	}
	if _, _, _, err := r.canonicalExecution(ctx, input); err != nil {
		return fail(err, refusalDetail(err))
	}
	// The Harness validates the workspace again when it opens the session; this
	// check only keeps a turn from starting for a directory that is not there.
	if info, err := os.Stat(r.settings.Workspace.Path); err != nil || !info.IsDir() {
		return fail(blocked(codeWorkspaceAbsent), codeWorkspaceAbsent)
	}
	if _, err := r.acquire(ctx); err != nil {
		return fail(err, strings.TrimPrefix(err.Error(), agent.ErrNativeCodingBlocked.Error()+": "))
	}
	return nil
}

// acquire returns the live client, starting one if there is none. The start,
// the check of the build and the registration of the client are one critical
// section.
func (r *Runtime) acquire(ctx context.Context) (Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, blocked(codeClosed)
	}
	if r.current != nil {
		select {
		case <-r.current.Done():
			r.log.emit("warn", eventClientEnded, nil)
			r.current = nil
		default:
			return r.current, nil
		}
	}
	if !r.failedAt.IsZero() && r.now().Sub(r.failedAt) < r.settings.RestartCooldown {
		return nil, blocked(codeCooldown + ":" + r.failureCode)
	}
	c, code, err := r.startLocked(ctx)
	if err != nil {
		r.failedAt, r.failureCode = r.now(), code
		r.log.emit("error", eventClientStartFailed, map[string]any{"code": code})
		return nil, blocked(code)
	}
	r.failedAt, r.failureCode = time.Time{}, ""
	r.current = c
	return c, nil
}

// startLocked starts the Harness and checks it is the build CORE pinned. Any
// failure leaves no child behind.
func (r *Runtime) startLocked(ctx context.Context) (Client, string, error) {
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.settings.StartTimeout)
	defer cancel()
	c, err := r.start(startCtx, client.Config{
		Binary:              r.settings.HarnessBinary,
		ConfigPath:          r.settings.HarnessConfig,
		Dir:                 filepath.Dir(r.settings.HarnessConfig),
		Env:                 nil, // an empty environment: nothing is inherited, nothing secret is passed
		Stderr:              r.log.stderrWriter(),
		ClientName:          ClientName,
		ClientVersion:       ClientVersion,
		RequireCapabilities: r.settings.requiredCapabilities(),
	})
	if err != nil {
		switch {
		case errors.Is(err, client.ErrIncompatible):
			return nil, codeIncompatible, err
		default:
			return nil, codeStartFailed, err
		}
	}
	capabilities := c.Capabilities()
	if capabilities.BuildRevision != r.settings.ExpectedBuildRevision {
		c.Abort()
		return nil, codeBuildMismatch, fmt.Errorf("build revision is not the pinned one")
	}
	r.log.emit("info", eventClientStarted, map[string]any{"protocol_version": capabilities.ProtocolVersion, "build_revision": capabilities.BuildRevision})
	go r.pump(c)
	return c, "", nil
}

// Close stops the Harness in two stages: an orderly shutdown that cancels the
// active Runs within a deadline, then the immediate kill if that does not
// finish. It is idempotent, and after it no client is started again.
func (r *Runtime) Close(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	c := r.current
	r.current = nil
	r.mu.Unlock()
	if c == nil {
		return nil
	}
	defer c.Abort()
	err := c.Shutdown(ctx, protocol.ShutdownInput{Mode: "cancel", DeadlineSeconds: defaultCloseDeadline})
	r.log.emit("info", eventClientStopped, map[string]any{"orderly": err == nil})
	return err
}

// withTurn adds CORE's own correlation IDs of a turn to a log entry.
func withTurn(fields map[string]any, input conversation.TurnInput) map[string]any {
	fields["trace_id"] = string(input.TraceID())
	fields["task_id"] = string(input.RootTaskID())
	fields["turn_id"] = string(input.TurnID())
	return fields
}
