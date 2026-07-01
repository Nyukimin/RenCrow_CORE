package persona

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domainpersona "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/persona"
)

func TestSQLiteStorePersonaLogs(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "persona.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	if err := store.SaveDiscomfortLog(ctx, domainpersona.DiscomfortLog{
		EventID:     "evt_discomfort_1",
		CharacterID: "mio",
		Discomfort:  "期待より軽すぎた",
		Status:      "candidate",
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("SaveDiscomfortLog() error = %v", err)
	}
	if err := store.SaveTriggerLog(ctx, domainpersona.TriggerLog{
		EventID:     "evt_trigger_1",
		CharacterID: "kuro",
		TriggerID:   "danger",
		Activated:   true,
		Confidence:  0.7,
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("SaveTriggerLog() error = %v", err)
	}
	if err := store.SaveCanonicalResponseLog(ctx, domainpersona.CanonicalResponseLog{
		EventID:     "evt_canonical_1",
		CharacterID: "kuro",
		ResponseID:  "block_destructive",
		Used:        true,
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("SaveCanonicalResponseLog() error = %v", err)
	}
	if err := store.SaveObservationLog(ctx, domainpersona.ObservationLog{
		EventID:         "evt_observation_1",
		ObserverID:      "lumina",
		TargetID:        "ren",
		ObservationType: "daily",
		Summary:         "観測候補",
		Sensitivity:     "normal",
		ReviewStatus:    "pending",
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("SaveObservationLog() error = %v", err)
	}
	if err := store.SaveMetaProfileUpdate(ctx, domainpersona.MetaProfileUpdate{
		UpdateID:        "meta_upd_1",
		ObserverID:      "lumina",
		TargetID:        "ren",
		Section:         "Risk Signs",
		ProposedContent: "疲労時は判断を急がない方がよい",
		Sensitivity:     "health",
		ReviewStatus:    "pending",
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("SaveMetaProfileUpdate() error = %v", err)
	}
	if err := store.SaveInterfaceSession(ctx, domainpersona.InterfaceSession{
		SessionID:     "persona_session_1",
		CharacterID:   "mio",
		InterfaceType: "web",
		SessionKey:    "web:viewer",
		CreatedAt:     now,
	}); err != nil {
		t.Fatalf("SaveInterfaceSession() error = %v", err)
	}

	discomforts, err := store.ListDiscomfortLogs(ctx, 10)
	if err != nil || len(discomforts) != 1 || discomforts[0].EventID != "evt_discomfort_1" {
		t.Fatalf("discomforts=%#v err=%v", discomforts, err)
	}
	triggers, err := store.ListTriggerLogs(ctx, 10)
	if err != nil || len(triggers) != 1 || triggers[0].TriggerID != "danger" {
		t.Fatalf("triggers=%#v err=%v", triggers, err)
	}
	canonicals, err := store.ListCanonicalResponseLogs(ctx, 10)
	if err != nil || len(canonicals) != 1 || canonicals[0].ResponseID != "block_destructive" {
		t.Fatalf("canonicals=%#v err=%v", canonicals, err)
	}
	observations, err := store.ListObservationLogs(ctx, 10)
	if err != nil || len(observations) != 1 || observations[0].ReviewStatus != "pending" {
		t.Fatalf("observations=%#v err=%v", observations, err)
	}
	metaUpdates, err := store.ListMetaProfileUpdates(ctx, 10)
	if err != nil || len(metaUpdates) != 1 || metaUpdates[0].UpdateID != "meta_upd_1" {
		t.Fatalf("metaUpdates=%#v err=%v", metaUpdates, err)
	}
	sessions, err := store.ListInterfaceSessions(ctx, 10)
	if err != nil || len(sessions) != 1 || sessions[0].SessionKey != "web:viewer" {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
}

func TestSQLiteStoreRejectsSensitiveAutoApprovedObservation(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "persona.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	err = store.SaveObservationLog(context.Background(), domainpersona.ObservationLog{
		EventID:         "evt_observation_1",
		ObserverID:      "lumina",
		TargetID:        "ren",
		ObservationType: "daily",
		Sensitivity:     "health",
		ReviewStatus:    "approved",
		CreatedAt:       time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("expected sensitive auto-approved observation to fail")
	}
}
