package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type distributedIdentityTTSBridge struct {
	startCalls    int
	startIdentity domainexecution.Identity
	startErr      error
	pushSessions  []string
	pushIdentity  domainexecution.Identity
	pushErr       error
	ended         []string
	endIdentity   domainexecution.Identity
	endErr        error
}

func (b *distributedIdentityTTSBridge) StartSession(ctx context.Context, _ TTSSessionStart) error {
	b.startCalls++
	b.startIdentity, b.startErr = domainexecution.IdentityFromContext(ctx)
	if b.startErr != nil {
		return fmt.Errorf("TTS start identity: %w", b.startErr)
	}
	return nil
}

func (b *distributedIdentityTTSBridge) PushText(ctx context.Context, sessionID, _ string, _ *moduletts.EmotionState) error {
	b.pushSessions = append(b.pushSessions, sessionID)
	b.pushIdentity, b.pushErr = distributedTTSCallerIdentity(ctx, b.startIdentity)
	if b.pushErr != nil {
		return fmt.Errorf("TTS push identity: %w", b.pushErr)
	}
	return nil
}

func (b *distributedIdentityTTSBridge) EndSession(ctx context.Context, sessionID string) error {
	b.ended = append(b.ended, sessionID)
	b.endIdentity, b.endErr = distributedTTSCallerIdentity(ctx, b.startIdentity)
	if b.endErr != nil {
		return fmt.Errorf("TTS end identity: %w", b.endErr)
	}
	return nil
}

// The TTS bridge binds a missing caller identity from its stored session and
// rejects an identity when the caller has already bound a different one.
func distributedTTSCallerIdentity(ctx context.Context, sessionIdentity domainexecution.Identity) (domainexecution.Identity, error) {
	callerIdentity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		return sessionIdentity, nil
	}
	if callerIdentity != sessionIdentity {
		return domainexecution.Identity{}, fmt.Errorf("caller identity %#v differs from session identity %#v", callerIdentity, sessionIdentity)
	}
	return callerIdentity, nil
}

func TestDistributedOrchestratorUsesCanonicalIdentityForTTSLifecycle(t *testing.T) {
	rootTaskID := modulecore.NewTaskID()
	traceID := modulecore.NewTraceID()
	manager := newRecordingTaskLifecycleManager()
	bridge := &distributedIdentityTTSBridge{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	orch := NewDistributedOrchestrator(&distMockSessionRepo{}, &distMockMioAgent{chatResponse: "canonical distributed speech"}, router, session.NewCentralMemory(), nil)
	orch.SetTaskLifecycleManager(manager)
	orch.SetTTSBridge(bridge)

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		RootTaskID: rootTaskID.String(), TraceID: string(traceID),
		SessionID: "distributed-tts-identity", Channel: "viewer", ChatID: "viewer-user",
		UserMessage: "speak with canonical identity",
	})
	if err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	if bridge.startCalls != 1 {
		t.Fatalf("TTS StartSession calls = %d, want 1", bridge.startCalls)
	}
	if bridge.startErr != nil {
		t.Fatalf("TTS StartSession identity error = %v", bridge.startErr)
	}
	if bridge.startIdentity.TaskID != rootTaskID || bridge.startIdentity.TraceID != traceID {
		t.Fatalf("TTS StartSession identity = %#v, want task=%s trace=%s", bridge.startIdentity, rootTaskID, traceID)
	}
	if err := bridge.startIdentity.RunID.Validate(); err != nil {
		t.Fatalf("TTS StartSession run identity %q is invalid: %v", bridge.startIdentity.RunID, err)
	}
	if bridge.startIdentity.TaskID.String() != resp.TaskID {
		t.Fatalf("TTS identity task=%s, response task=%s", bridge.startIdentity.TaskID, resp.TaskID)
	}
	if len(bridge.pushSessions) == 0 || bridge.pushErr != nil || bridge.pushIdentity != bridge.startIdentity {
		t.Fatalf("TTS push sessions=%#v identity=%#v error=%v; want push with start identity %#v", bridge.pushSessions, bridge.pushIdentity, bridge.pushErr, bridge.startIdentity)
	}
	if len(bridge.ended) != 1 || bridge.endErr != nil || bridge.endIdentity != bridge.startIdentity {
		t.Fatalf("TTS end sessions=%#v identity=%#v error=%v; want end with start identity %#v", bridge.ended, bridge.endIdentity, bridge.endErr, bridge.startIdentity)
	}
	run, ok := manager.runs[bridge.startIdentity.RunID]
	if !ok || run.TaskID != rootTaskID || run.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("canonical TTS run = %#v, exists=%v; want succeeded run for task %s", run, ok, rootTaskID)
	}
}

func TestDistributedOrchestratorRejectsPreboundForeignIdentityBeforeTTSStart(t *testing.T) {
	foreignCtx, err := domainexecution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	bridge := &distributedIdentityTTSBridge{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	orch := NewDistributedOrchestrator(&distMockSessionRepo{}, &distMockMioAgent{chatResponse: "unused"}, router, session.NewCentralMemory(), nil)
	attachCanonicalTestTaskOwner(t, orch)
	orch.SetTTSBridge(bridge)

	_, err = orch.ProcessMessage(foreignCtx, ProcessMessageRequest{
		RootTaskID: modulecore.NewTaskID().String(), TraceID: string(modulecore.NewTraceID()),
		SessionID: "distributed-foreign-identity", Channel: "viewer", ChatID: "viewer-user",
		UserMessage: "reject foreign identity",
	})
	if err == nil || !strings.Contains(err.Error(), "execution identity is already bound") {
		t.Fatalf("ProcessMessage() error = %v, want prebound identity mismatch", err)
	}
	if bridge.startCalls != 0 {
		t.Fatalf("TTS StartSession calls = %d, want 0 after identity mismatch", bridge.startCalls)
	}
}
