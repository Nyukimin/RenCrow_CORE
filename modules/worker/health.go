package worker

import "github.com/Nyukimin/RenCrow_CORE/modules/core"

type ExecutorHealthSnapshot struct {
	Ready bool
}

func BuildExecutorHealth(snapshot ExecutorHealthSnapshot) core.HealthReport {
	if !snapshot.Ready {
		return core.HealthReport{Module: "worker", Status: core.HealthDown, Detail: "worker execution service is nil"}
	}
	return core.HealthReport{
		Module: "worker",
		Status: core.HealthReady,
		Ready:  true,
		Detail: "worker execution service configured",
	}
}
