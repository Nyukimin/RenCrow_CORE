package skillgovernance

import (
	"context"
	"testing"
	"time"

	domainskill "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/skillgovernance"
)

type memoryBootstrapStore struct {
	manifests []domainskill.SkillManifest
	logs      []domainskill.SkillTriggerLog
}

func (s *memoryBootstrapStore) ListSkillManifests(_ context.Context, _ int) ([]domainskill.SkillManifest, error) {
	return append([]domainskill.SkillManifest(nil), s.manifests...), nil
}

func (s *memoryBootstrapStore) SaveSkillTriggerLog(_ context.Context, log domainskill.SkillTriggerLog) error {
	s.logs = append(s.logs, log)
	return nil
}

func TestBootstrapServiceRecordSavesMissedAndTriggeredLogs(t *testing.T) {
	store := &memoryBootstrapStore{
		manifests: []domainskill.SkillManifest{
			{SkillID: "core.pr-readiness", Enabled: true, KeywordTriggers: []string{"PR"}},
			{SkillID: "core.dci-search", Enabled: true, IntentTriggers: []string{"dci_search"}},
		},
	}
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	svc := NewBootstrapService(store).WithNow(func() time.Time { return now })

	logs, err := svc.Record(context.Background(), domainskill.TaskContext{
		Text:         "PR用に原文を確認して",
		Intent:       "dci_search",
		Agent:        "Worker",
		WorkstreamID: "ws_1",
	}, []string{"core.dci-search"})
	if err != nil {
		t.Fatalf("Record failed: %v", err)
	}
	if len(logs) != 2 || len(store.logs) != 2 {
		t.Fatalf("logs=%#v store=%#v", logs, store.logs)
	}
	status := map[string]string{}
	for _, log := range store.logs {
		status[log.SkillID] = log.Status
		if log.Agent != "Worker" || log.WorkstreamID != "ws_1" {
			t.Fatalf("unexpected task context in log: %#v", log)
		}
	}
	if status["core.dci-search"] != domainskill.TriggerStatusTriggered {
		t.Fatalf("dci status=%q", status["core.dci-search"])
	}
	if status["core.pr-readiness"] != domainskill.TriggerStatusMissed {
		t.Fatalf("pr status=%q", status["core.pr-readiness"])
	}
}
