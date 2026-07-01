package orchestrator

import (
	"context"
	"time"

	domainai "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/aiworkflow"
	capdomain "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/capability"
	domainexecution "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/execution"
	domainnode "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/node"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/session"
	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/transport"
)

const (
	distributedDefaultTimeout = 120 * time.Second
	distributedCoderTimeout   = 6 * time.Minute
	distributedWorkerTimeout  = 6 * time.Minute
	distributedCoderRetryMax  = 2
)

// DistributedOrchestrator はTransport経由でメッセージを送受信する分散オーケストレータ
type DistributedOrchestrator struct {
	sessionRepo             SessionRepository
	mio                     MioAgent
	wild                    WildAgent
	heavy                   HeavyAgent
	router                  *transport.MessageRouter
	memory                  *session.CentralMemory
	sshTransports           map[string]domaintransport.Transport // SSH経由のリモートAgent
	listener                EventListener
	reporter                ReportStore
	idleNotifier            IdleNotifier
	nodeSelector            *NodeSelector
	nodeCaps                map[string]domainnode.ResourceProfile
	coderCaps               []capdomain.CoderCapability
	coderConfigs            map[string]interface{} // v4.1: coder1-4 の CoderConfig（SSH送信用）
	ttsBridge               TTSBridge
	vtuberBridge            VTuberBridge
	dciSearcher             DCISearcher
	recallTrace             RecallTraceStore
	skillBootstrap          SkillBootstrapRecorder
	workflowEvents          WorkflowEventRecorder
	commandRegistry         CommandRegistryLister
	superAgentRuns          SuperAgentRuntimeRecorder
	superAgentRunController SuperAgentRunController
	heavyPolicy             domainai.HeavyWorkerPolicy
	maxRepair               int           // 0以下は1とみなす
	coderTimeout            time.Duration // 0以下は distributedCoderTimeout とみなす
	coderRetryMax           int           // 0以下は distributedCoderRetryMax とみなす
	events                  *distributedEventPort
	evidence                *distributedEvidenceReporter
	ttsLifecycle            *distributedTTSLifecycle
	sessions                *distributedSessionLifecycle
	autonomous              *distributedAutonomousCoordinator
	routes                  *distributedRouteDispatcher
	transports              *distributedTransportExecutor
	codeExecution           *distributedCodeExecutionCoordinator
	coderSelector           *distributedCoderSelection
	attribution             *distributedAttributionGuard
}

type ReportStore interface {
	Save(ctx context.Context, report domainexecution.ExecutionReport) error
}
