package idlechat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// generationCheckpointRunIssuer is intentionally narrower than the general
// first-run issuer. A persisted generation checkpoint may issue a successor
// only through the owner's compare-and-set boundary.
type generationCheckpointRunIssuer interface {
	StartRunFromCheckpoint(context.Context, modulecore.TaskID, modulecore.RunID, string, domaintask.RunStartReason, string) (domaintask.Run, error)
}

// generationRunEffectExecutor is the owner boundary for a synchronous leaf
// effect. The callback must contain only the external generation/provider call.
type generationRunEffectExecutor interface {
	ExecuteRunEffect(context.Context, modulecore.TaskID, modulecore.RunID, string, func(context.Context) error) error
}

func canonicalGenerationCheckpoint(checkpoint GenerationCheckpoint) ([]byte, error) {
	checkpoint = cloneGenerationCheckpoint(checkpoint)
	checkpoint.UpdatedAt = time.Time{}
	return json.Marshal(checkpoint)
}

// generationCheckpointSHA256 is the one source of checkpoint evidence used by
// both successor issuance and recovery adoption. UpdatedAt is store metadata;
// every other checkpoint field remains part of the digest.
func generationCheckpointSHA256(checkpoint GenerationCheckpoint) (string, error) {
	payload, err := canonicalGenerationCheckpoint(checkpoint)
	if err != nil {
		return "", fmt.Errorf("marshal generation checkpoint: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func generationCheckpointIntent(ctx context.Context, checkpoint *GenerationCheckpoint, store *GenerationCheckpointStore, stage string) (GenerationCheckpoint, string, error) {
	if ctx == nil {
		return GenerationCheckpoint{}, "", errors.New("generation checkpoint context is nil")
	}
	if err := ctx.Err(); err != nil {
		return GenerationCheckpoint{}, "", err
	}
	if checkpoint == nil || checkpoint.Stage != stage {
		return GenerationCheckpoint{}, "", errors.New("generation successor requires matching persisted intent")
	}
	if store == nil || strings.TrimSpace(store.path) == "" {
		return GenerationCheckpoint{}, "", errors.New("generation successor requires persistent checkpoint store")
	}
	if err := store.LoadError(); err != nil {
		return GenerationCheckpoint{}, "", err
	}
	persisted, found := store.Get(checkpoint.Key)
	if !found {
		return GenerationCheckpoint{}, "", errors.New("generation successor intent does not match checkpoint store")
	}
	want, err := canonicalGenerationCheckpoint(*checkpoint)
	if err != nil {
		return GenerationCheckpoint{}, "", err
	}
	got, err := canonicalGenerationCheckpoint(persisted)
	if err != nil {
		return GenerationCheckpoint{}, "", err
	}
	if string(want) != string(got) {
		return GenerationCheckpoint{}, "", errors.New("generation successor intent does not match checkpoint store")
	}
	if err := validateIdleChatRunIdentity(persisted.TaskID, persisted.RunID); err != nil {
		return GenerationCheckpoint{}, "", fmt.Errorf("generation checkpoint identity: %w", err)
	}
	if persisted.Stage != stage {
		return GenerationCheckpoint{}, "", errors.New("generation successor checkpoint stage does not match persisted intent")
	}
	digest, err := generationCheckpointSHA256(persisted)
	if err != nil {
		return GenerationCheckpoint{}, "", err
	}
	return persisted, digest, nil
}

func checkpointSuccessorStage(reason domaintask.RunStartReason) string {
	switch reason {
	case domaintask.RunStartReasonCheckpointResume:
		return "resume_pending"
	case domaintask.RunStartReasonExplicitRerun:
		return "rerun_pending"
	default:
		return ""
	}
}

// startGenerationSuccessor performs the owner-side CAS after inspecting the
// exact persisted predecessor and its actual assignee. It never falls back to
// StartRunWithReason: an issuer without the bounded API fails closed.
func startGenerationSuccessor(ctx context.Context, issuer idlechatRunIssuer, checkpoint *GenerationCheckpoint, store *GenerationCheckpointStore, reason domaintask.RunStartReason) (domaintask.Run, error) {
	stage := checkpointSuccessorStage(reason)
	if stage == "" {
		return domaintask.Run{}, fmt.Errorf("generation successor reason is not accepted: %s", reason)
	}
	persisted, digest, err := generationCheckpointIntent(ctx, checkpoint, store, stage)
	if err != nil {
		return domaintask.Run{}, err
	}
	predecessor, err := inspectGenerationRun(ctx, issuer, persisted.TaskID, persisted.RunID)
	if err != nil {
		return domaintask.Run{}, err
	}
	if !domaintask.CanStartRunFromCheckpoint(predecessor.Status, reason) {
		return domaintask.Run{}, fmt.Errorf("generation predecessor Run %s is not valid for %s: %s", predecessor.RunID, reason, predecessor.Status)
	}
	owner, ok := issuer.(generationCheckpointRunIssuer)
	if !ok || owner == nil {
		return domaintask.Run{}, errors.New("checkpoint run issuer is not configured")
	}
	actorID := strings.TrimSpace(predecessor.Assignee)
	if actorID == "" {
		return domaintask.Run{}, errors.New("generation predecessor assignee is empty")
	}
	issued, err := owner.StartRunFromCheckpoint(ctx, persisted.TaskID, persisted.RunID, actorID, reason, digest)
	if err != nil {
		return domaintask.Run{}, err
	}
	if err := issued.Validate(); err != nil {
		return domaintask.Run{}, fmt.Errorf("owner returned invalid checkpoint successor: %w", err)
	}
	if issued.TaskID != persisted.TaskID || issued.RunID == persisted.RunID || issued.Assignee != actorID || issued.StartReason != reason || issued.StartCheckpointSHA256 != digest || issued.Status != domaintask.RunStatusRunning {
		return domaintask.Run{}, errors.New("owner returned checkpoint successor that does not match persisted intent")
	}
	if err := ctx.Err(); err != nil {
		// The owner may have committed immediately before cancellation became
		// visible. Close that exact Run through the canonical lifecycle boundary
		// so a cancelled request cannot strand a running successor.
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		closeErr := completeGenerationRun(closeCtx, issuer, issued.TaskID, issued.RunID, domaintask.StatusWaiting, "checkpoint successor start cancelled", "retry from saved generation checkpoint")
		return issued, errors.Join(err, closeErr)
	}
	return issued, nil
}

// resumeIdleChatRun is the checkpoint-bound replacement for the old task-only
// resume API. The caller must persist the pending intent before invoking it.
func resumeIdleChatRun(ctx context.Context, issuer idlechatRunIssuer, checkpoint *GenerationCheckpoint, store *GenerationCheckpointStore) (domaintask.Run, error) {
	return startGenerationSuccessor(ctx, issuer, checkpoint, store, domaintask.RunStartReasonCheckpointResume)
}

func rerunIdleChatRun(ctx context.Context, issuer idlechatRunIssuer, checkpoint *GenerationCheckpoint, store *GenerationCheckpointStore) (domaintask.Run, error) {
	return startGenerationSuccessor(ctx, issuer, checkpoint, store, domaintask.RunStartReasonExplicitRerun)
}

// executeIdleChatRunEffect admits one synchronous provider/generator leaf.
// Owner calls and checkpoint/state completion remain outside the callback.
func executeIdleChatRunEffect(ctx context.Context, issuer idlechatRunIssuer, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, effect func(context.Context) error) error {
	if ctx == nil {
		return errors.New("generation effect context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateIdleChatRunIdentity(taskID, runID); err != nil {
		return err
	}
	if strings.TrimSpace(actorID) == "" {
		return errors.New("generation effect actor is empty")
	}
	if effect == nil {
		return errors.New("generation effect callback is required")
	}
	executor, ok := issuer.(generationRunEffectExecutor)
	if !ok || executor == nil {
		return errors.New("generation run effect executor is not configured")
	}
	err := executor.ExecuteRunEffect(ctx, taskID, runID, strings.TrimSpace(actorID), func(effectCtx context.Context) error {
		if effectCtx == nil {
			return errors.New("generation effect context is nil")
		}
		if err := effectCtx.Err(); err != nil {
			return err
		}
		if err := effect(effectCtx); err != nil {
			return err
		}
		return effectCtx.Err()
	})
	if err != nil {
		return err
	}
	return ctx.Err()
}

func generateIdleChatCodexWithRun(ctx context.Context, issuer idlechatRunIssuer, taskID modulecore.TaskID, runID modulecore.RunID, generator IdleChatCodexGenerator, prompt string) (string, error) {
	if generator == nil {
		return "", errors.New("CodexExe generator is not configured")
	}
	run, err := inspectGenerationRun(ctx, issuer, taskID, runID)
	if err != nil {
		return "", err
	}
	var text string
	err = executeIdleChatRunEffect(ctx, issuer, taskID, runID, run.Assignee, func(effectCtx context.Context) error {
		var err error
		text, err = generator.Generate(effectCtx, prompt)
		return err
	})
	if err != nil {
		return "", err
	}
	return text, nil
}

type runGuardedIdleChatCodexGenerator struct {
	issuer    idlechatRunIssuer
	taskID    modulecore.TaskID
	runID     modulecore.RunID
	actorID   string
	generator IdleChatCodexGenerator
}

func newRunGuardedIdleChatCodexGenerator(issuer idlechatRunIssuer, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, generator IdleChatCodexGenerator) IdleChatCodexGenerator {
	if generator == nil {
		return nil
	}
	return runGuardedIdleChatCodexGenerator{issuer: issuer, taskID: taskID, runID: runID, actorID: actorID, generator: generator}
}

func (g runGuardedIdleChatCodexGenerator) Generate(ctx context.Context, prompt string) (string, error) {
	var text string
	err := executeIdleChatRunEffect(ctx, g.issuer, g.taskID, g.runID, g.actorID, func(effectCtx context.Context) error {
		var err error
		text, err = g.generator.Generate(effectCtx, prompt)
		return err
	})
	return text, err
}

type runGuardedLLMProvider struct {
	issuer   idlechatRunIssuer
	taskID   modulecore.TaskID
	runID    modulecore.RunID
	actorID  string
	provider llm.LLMProvider
}

func newRunGuardedLLMProvider(issuer idlechatRunIssuer, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, provider llm.LLMProvider) llm.LLMProvider {
	if provider == nil {
		return nil
	}
	return runGuardedLLMProvider{issuer: issuer, taskID: taskID, runID: runID, actorID: actorID, provider: provider}
}

func (p runGuardedLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	var response llm.GenerateResponse
	err := executeIdleChatRunEffect(ctx, p.issuer, p.taskID, p.runID, p.actorID, func(effectCtx context.Context) error {
		var err error
		response, err = p.provider.Generate(effectCtx, req)
		return err
	})
	return response, err
}

func (p runGuardedLLMProvider) Name() string {
	if p.provider == nil {
		return ""
	}
	return p.provider.Name()
}
