package taskexecution

import modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"

// HandoffKey is the canonical identity used to bind one TaskPlan handoff to
// TaskManager's durable operation receipt. It reuses the core RequestID wire
// format without reintroducing retired receipt fields into Workstream records.
type HandoffKey = modulecore.RequestID
