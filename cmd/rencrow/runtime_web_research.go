package main

import (
	"github.com/Nyukimin/RenCrow_CORE/internal/application/toolloop"
)

// applyRuntimeAgentWebResearch connects the shared pre-run gather to Agent
// generation, using the same late-bound Runner that supplies the capability
// snapshot. It preserves Kuro's native Codex generation and Shiro's native
// coding and Worker Tool execution/permission owners.
func applyRuntimeAgentWebResearch(agents agentRuntime, runtime toolRuntime, coders ...*coderAdapter) {
	if runtime.WorkerRuntimeRunnerV2 == nil {
		return
	}
	research := toolloop.NewWebResearcher(runtime.WorkerRuntimeRunnerV2, runtime.ActionManager)
	if agents.Mio != nil {
		agents.Mio.WithWebResearch(research)
	}
	if agents.ShiroChat != nil {
		agents.ShiroChat.WithWebResearch(research)
	}
	if agents.Shiro != nil {
		agents.Shiro.WithWebResearch(research)
	}
	if agents.Wild != nil {
		agents.Wild.WithWebResearch(research)
	}
	if agents.Heavy != nil {
		agents.Heavy.WithWebResearch(research)
	}
	for _, coder := range coders {
		if coder != nil && coder.domainCoder != nil {
			coder.domainCoder.WithWebResearch(research)
		}
	}
}
