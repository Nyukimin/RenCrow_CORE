package orchestrator

import (
	domainnode "github.com/Nyukimin/RenCrow_CORE/internal/domain/node"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/transport"
)

// NewDistributedOrchestrator は新しいDistributedOrchestratorを作成
func NewDistributedOrchestrator(
	sessionRepo SessionRepository,
	mio MioAgent,
	router *transport.MessageRouter,
	memory *session.CentralMemory,
	sshTransports map[string]domaintransport.Transport,
) *DistributedOrchestrator {
	if sshTransports == nil {
		sshTransports = make(map[string]domaintransport.Transport)
	}
	orch := &DistributedOrchestrator{
		sessionRepo:   sessionRepo,
		mio:           mio,
		router:        router,
		memory:        memory,
		sshTransports: sshTransports,
		nodeSelector:  NewNodeSelector(),
		nodeCaps:      make(map[string]domainnode.ResourceProfile),
	}
	orch.events = newDistributedEventPort(nil)
	orch.evidence = newDistributedEvidenceReporter(nil)
	orch.ttsLifecycle = newDistributedTTSLifecycle(nil, nil, orch.emit)
	orch.sessions = newDistributedSessionLifecycle(sessionRepo)
	orch.transports = newDistributedTransportExecutor(router, sshTransports, memory, orch.emitProgress, orch.distributedWaitTimeout)
	orch.coderSelector = newDistributedCoderSelection(router, sshTransports, orch.nodeSelector, orch.nodeCaps)
	orch.attribution = newDistributedAttributionGuard(memory)
	orch.codeExecution = newDistributedCodeExecutionCoordinator(
		memory,
		orch.emit,
		orch.emitNote,
		orch.routeToCoderForMessage,
		func() map[string]interface{} { return orch.coderConfigs },
		orch.coderRetryMaxOrDefault,
		orch.executeToAgentViaMailbox,
		orch.executeToAgent,
	)
	orch.routes = newDistributedRouteDispatcher(
		mio,
		memory,
		orch.emit,
		orch.emitNote,
		orch.withStreamHooks,
		orch.pushTTS,
		orch.executeCodeViaShiro,
		orch.routeToAgent,
		orch.withAttributionGuard,
		orch.executeToAgent,
	)
	orch.autonomous = newDistributedAutonomousCoordinator(nil, orch.maxRepairOrDefault, orch.emit, orch.routes.ExecuteDirect)
	orch.routes.SetAutonomousExecutor(orch.autonomous.Execute)
	return orch
}
