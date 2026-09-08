package superagent

import (
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
	"testing"
	"time"
)

func TestHistoricalLuminaRunIsReadableButNotExecutable(t *testing.T) {
	now := time.Now().UTC()
	r := AgentRun{RunID: core.NewRunID(), TaskID: core.NewTaskID(), ActorID: "lumina", Status: "completed", StartedAt: now, CompletedAt: now}
	if e := ValidateAgentRun(r); e != nil {
		t.Fatalf("historical terminal attribution must survive migration: %v", e)
	}
	r.Status = "running"
	r.CompletedAt = time.Time{}
	if e := ValidateAgentRun(r); e == nil {
		t.Fatal("historical actor admitted to running projection")
	}
	if e := ValidateActorID("lumina"); e == nil {
		t.Fatal("historical attribution registered as an active actor")
	}
}
