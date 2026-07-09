package chat

import "github.com/Nyukimin/RenCrow_CORE/modules/core"

type ServiceHealthSnapshot struct {
	Ready bool
}

func BuildServiceHealth(snapshot ServiceHealthSnapshot) core.HealthReport {
	if !snapshot.Ready {
		return core.HealthReport{Module: "chat", Status: core.HealthDown, Detail: "chat processor is nil"}
	}
	return core.HealthReport{
		Module: "chat",
		Status: core.HealthReady,
		Ready:  true,
		Detail: "legacy orchestrator processor configured",
	}
}
