package idlechat

import (
	"context"
	"strings"
	"testing"

	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
)

type idlePersonaRecorder struct {
	sessions     []domainpersona.InterfaceSession
	observations []domainpersona.ObservationLog
	triggers     []domainpersona.TriggerLog
	canonical    []domainpersona.CanonicalResponseLog
	metaUpdates  []domainpersona.MetaProfileUpdate
}

func (r *idlePersonaRecorder) SaveInterfaceSession(_ context.Context, item domainpersona.InterfaceSession) error {
	r.sessions = append(r.sessions, item)
	return nil
}

func (r *idlePersonaRecorder) SaveObservationLog(_ context.Context, item domainpersona.ObservationLog) error {
	r.observations = append(r.observations, item)
	return nil
}

func (r *idlePersonaRecorder) SaveTriggerLog(_ context.Context, item domainpersona.TriggerLog) error {
	r.triggers = append(r.triggers, item)
	return nil
}

func (r *idlePersonaRecorder) SaveMetaProfileUpdate(_ context.Context, item domainpersona.MetaProfileUpdate) error {
	r.metaUpdates = append(r.metaUpdates, item)
	return nil
}

func (r *idlePersonaRecorder) SaveCanonicalResponseLog(_ context.Context, item domainpersona.CanonicalResponseLog) error {
	r.canonical = append(r.canonical, item)
	return nil
}

func (r *idlePersonaRecorder) ListCanonicalResponseLogs(_ context.Context, _ int) ([]domainpersona.CanonicalResponseLog, error) {
	return append([]domainpersona.CanonicalResponseLog(nil), r.canonical...), nil
}

func TestIdleChatRecordsPersonaTimelineObservation(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	recorder := &idlePersonaRecorder{}
	o.SetPersonaRuntimeRecorder(recorder, []domainpersona.TriggerDefinition{{
		TriggerID:   "mio_tired",
		CharacterID: "mio",
		Category:    "tiredness",
		Keywords:    []string{"疲れた"},
	}})

	o.emitTimelineEvent(TimelineEvent{
		Type:      "idlechat.message",
		From:      "mio",
		To:        "user",
		Content:   "今日は疲れた時に効く話にしよう。",
		SessionID: "idle-1",
	})

	if len(recorder.sessions) != 1 {
		t.Fatalf("sessions len = %d, want 1", len(recorder.sessions))
	}
	if got := recorder.sessions[0].SessionKey; got != "idlechat:idle-1" {
		t.Fatalf("session key = %q", got)
	}
	if recorder.sessions[0].InterfaceType != "idlechat" || recorder.sessions[0].CharacterID != "mio" {
		t.Fatalf("unexpected session: %#v", recorder.sessions[0])
	}
	if len(recorder.observations) != 1 {
		t.Fatalf("observations len = %d, want 1", len(recorder.observations))
	}
	obs := recorder.observations[0]
	if obs.ObserverID != "mio" || obs.TargetID != "ren" || obs.ObservationType != "idlechat_message" || obs.ReviewStatus != "pending" {
		t.Fatalf("unexpected observation: %#v", obs)
	}
	if len(recorder.triggers) != 1 {
		t.Fatalf("triggers len = %d, want 1", len(recorder.triggers))
	}
	if recorder.triggers[0].TriggerID != "mio_tired" || recorder.triggers[0].TriggerCategory != "tiredness" {
		t.Fatalf("unexpected trigger: %#v", recorder.triggers[0])
	}
}

func TestIdleChatPersonaRecorderIgnoresTTSAudioChunks(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	recorder := &idlePersonaRecorder{}
	o.SetPersonaRuntimeRecorder(recorder, []domainpersona.TriggerDefinition{{
		TriggerID:   "mio_tired",
		CharacterID: "mio",
		Category:    "tiredness",
		Keywords:    []string{"疲れた"},
	}})

	o.emitTimelineEvent(TimelineEvent{
		Type:      "idlechat.tts",
		From:      "mio",
		To:        "user",
		Content:   "疲れた",
		SessionID: "idle-tts",
	})

	if len(recorder.sessions) != 0 || len(recorder.observations) != 0 || len(recorder.triggers) != 0 {
		t.Fatalf("tts event should not be recorded: sessions=%d observations=%d triggers=%d", len(recorder.sessions), len(recorder.observations), len(recorder.triggers))
	}
}

func TestIdleChatCreatesPendingMetaUpdateCandidateFromTimelineEvent(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	recorder := &idlePersonaRecorder{}
	o.SetPersonaRuntimeRecorder(recorder, nil)

	o.emitTimelineEvent(TimelineEvent{
		Type:      "idlechat.message",
		From:      "user",
		To:        "mio",
		Content:   "私は映画の話題をアイデア源にする",
		SessionID: "idle-meta",
	})

	if len(recorder.metaUpdates) != 1 {
		t.Fatalf("metaUpdates = %#v", recorder.metaUpdates)
	}
	got := recorder.metaUpdates[0]
	if got.TargetID != "mio" || got.ReviewStatus != "pending" || got.Section != "flow_observation" {
		t.Fatalf("unexpected meta update = %#v", got)
	}
	if !strings.Contains(got.ProposedContent, "Human review is required") || !strings.Contains(got.ProposedContent, "映画の話題") {
		t.Fatalf("proposed content = %q", got.ProposedContent)
	}
}

func TestIdleChatAppliesPersonaCanonicalResponse(t *testing.T) {
	provider := &capturingIdleProvider{response: "削除は危険だから一回止めよう。理由を確認しよう。"}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"kuro", "mio"}, 5, 10, 0.8, nil, "")
	recorder := &idlePersonaRecorder{}
	o.SetPersonaRuntimeRecorder(recorder, []domainpersona.TriggerDefinition{{
		TriggerID:   "kuro_danger",
		CharacterID: "kuro",
		Category:    "danger",
		Keywords:    []string{"削除"},
	}})
	o.SetPersonaCanonicalResponses([]domainpersona.CanonicalResponseDefinition{{
		ResponseID:       "kuro_destructive_block",
		CharacterID:      "kuro",
		Category:         "danger",
		Response:         "その操作は止めます。",
		RequiredContexts: []string{"danger"},
		CooldownTurns:    5,
		MaxPerSession:    3,
		Priority:         10,
	}})

	got, raw, err := o.generateResponseWithRaw("kuro", "mio", "idle-canonical", 0, 0, "危険操作")
	if err != nil {
		t.Fatalf("generateResponseWithRaw() error = %v", err)
	}
	if got != "その操作は止めます。" {
		t.Fatalf("response = %q", got)
	}
	if raw == got {
		t.Fatalf("raw response should preserve model output, got %q", raw)
	}
	if len(recorder.canonical) != 1 || recorder.canonical[0].ResponseID != "kuro_destructive_block" || !recorder.canonical[0].Used {
		t.Fatalf("canonical logs = %#v", recorder.canonical)
	}
}
