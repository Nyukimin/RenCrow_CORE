package orchestrator

import (
	"time"

	capdomain "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/capability"
	domainnode "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/node"
)

// SetMaxRepair は自律実行のリペア上限を設定する（デフォルト: 1）
func (o *DistributedOrchestrator) SetMaxRepair(n int) {
	if n > 0 {
		o.maxRepair = n
	}
}

func (o *DistributedOrchestrator) maxRepairOrDefault() int {
	if o.maxRepair > 0 {
		return o.maxRepair
	}
	return 1
}

// SetDistributedTimeouts は分散実行のタイムアウトとリトライ上限を設定する
func (o *DistributedOrchestrator) SetDistributedTimeouts(coderTimeoutSec, retryMax int) {
	if coderTimeoutSec > 0 {
		o.coderTimeout = time.Duration(coderTimeoutSec) * time.Second
	}
	if retryMax >= 0 {
		o.coderRetryMax = retryMax
	}
}

func (o *DistributedOrchestrator) coderTimeoutOrDefault() time.Duration {
	if o.coderTimeout > 0 {
		return o.coderTimeout
	}
	return distributedCoderTimeout
}

func (o *DistributedOrchestrator) coderRetryMaxOrDefault() int {
	if o.coderRetryMax > 0 {
		return o.coderRetryMax
	}
	return distributedCoderRetryMax
}

// SetNodeCapabilities sets capability map used by RouteCODE coder selection.
func (o *DistributedOrchestrator) SetNodeCapabilities(caps map[string]domainnode.ResourceProfile) {
	if caps == nil {
		o.nodeCaps = make(map[string]domainnode.ResourceProfile)
		if o.coderSelector != nil {
			o.coderSelector.SetNodeCapabilities(o.nodeCaps)
		}
		return
	}
	o.nodeCaps = caps
	if o.coderSelector != nil {
		o.coderSelector.SetNodeCapabilities(caps)
	}
}

// SetCoderCapabilities sets self-detected LLM quality metadata used for capability-based coder routing.
func (o *DistributedOrchestrator) SetCoderCapabilities(caps []capdomain.CoderCapability) {
	o.coderCaps = append([]capdomain.CoderCapability(nil), caps...)
	if o.coderSelector != nil {
		o.coderSelector.SetCoderCapabilities(o.coderCaps)
	}
}

// SetCoderConfigs sets CoderConfig map for SSH transport (v4.1)
func (o *DistributedOrchestrator) SetCoderConfigs(configs map[string]interface{}) {
	o.coderConfigs = configs
}
