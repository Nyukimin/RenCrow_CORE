package orchestrator

import (
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
)

// SetEventListener sets an optional listener for monitoring events.
func (o *DistributedOrchestrator) SetEventListener(l EventListener) {
	o.listener = l
	if o.events != nil {
		o.events.SetListener(l)
	}
}

func (o *DistributedOrchestrator) SetReportStore(store ReportStore) {
	o.reporter = store
	if o.evidence != nil {
		o.evidence.SetReportStore(store)
	}
	if o.autonomous != nil {
		o.autonomous.SetReportStore(store)
	}
}

func (o *DistributedOrchestrator) SetSkillBootstrapRecorder(recorder SkillBootstrapRecorder) {
	o.skillBootstrap = recorder
}

func (o *DistributedOrchestrator) SetCoderProposalEvidenceRecorder(recorder CoderProposalEvidenceRecorder) {
	if o.codeExecution != nil {
		o.codeExecution.SetCoderProposalEvidenceRecorder(recorder)
	}
}

func (o *DistributedOrchestrator) SetWorkflowEventRecorder(recorder WorkflowEventRecorder) {
	o.workflowEvents = recorder
	if o.routes != nil {
		o.routes.SetWorkflowEventRecorder(recorder)
	}
}

func (o *DistributedOrchestrator) SetCommandRegistry(registry CommandRegistryLister) {
	o.commandRegistry = registry
}

func (o *DistributedOrchestrator) SetSuperAgentRuntimeRecorder(recorder SuperAgentRuntimeRecorder) {
	o.superAgentRuns = recorder
}

func (o *DistributedOrchestrator) SetSuperAgentRunController(controller SuperAgentRunController) {
	o.superAgentRunController = controller
}

func (o *DistributedOrchestrator) SetHeavyWorkerPolicy(policy domainai.HeavyWorkerPolicy) {
	o.heavyPolicy = policy
}

// SetIdleNotifier sets an optional notifier used to control idle chat.
func (o *DistributedOrchestrator) SetIdleNotifier(n IdleNotifier) {
	o.idleNotifier = n
}

func (o *DistributedOrchestrator) SetWildAgent(wild WildAgent) {
	o.wild = wild
	if o.routes != nil {
		o.routes.SetWildAgent(wild)
	}
}

func (o *DistributedOrchestrator) SetHeavyAgent(heavy HeavyAgent) {
	o.heavy = heavy
	if o.routes != nil {
		o.routes.SetHeavyAgent(heavy)
	}
}

// SetTTSBridge sets an optional TTS bridge.
func (o *DistributedOrchestrator) SetTTSBridge(b TTSBridge) {
	o.ttsBridge = b
	if o.ttsLifecycle != nil {
		o.ttsLifecycle.SetTTSBridge(b)
	}
}

// SetVTuberBridge sets an optional VTuber bridge.
func (o *DistributedOrchestrator) SetVTuberBridge(b VTuberBridge) {
	o.vtuberBridge = b
	if o.ttsLifecycle != nil {
		o.ttsLifecycle.SetVTuberBridge(b)
	}
}

func (o *DistributedOrchestrator) emit(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {
	o.events.Emit(eventType, from, to, content, route, jobID, sessionID, channel, chatID)
}

func (o *DistributedOrchestrator) emitNote(from, to, content, route, jobID, sessionID, channel, chatID string) {
	o.events.EmitNote(from, to, content, route, jobID, sessionID, channel, chatID)
}

func (o *DistributedOrchestrator) emitProgress(eventType, from, to, content string, msg domaintransport.Message) {
	o.events.EmitProgress(eventType, from, to, content, msg)
}
