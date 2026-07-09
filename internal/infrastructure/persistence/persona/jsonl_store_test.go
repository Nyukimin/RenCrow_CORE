package persona

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
)

func TestJSONLStorePersonaLogs(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
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
		t.Fatalf("ListDiscomfortLogs() = %#v, %v", discomforts, err)
	}
	triggers, err := store.ListTriggerLogs(ctx, 10)
	if err != nil || len(triggers) != 1 || triggers[0].TriggerID != "danger" {
		t.Fatalf("ListTriggerLogs() = %#v, %v", triggers, err)
	}
	canonicals, err := store.ListCanonicalResponseLogs(ctx, 10)
	if err != nil || len(canonicals) != 1 || canonicals[0].ResponseID != "block_destructive" {
		t.Fatalf("ListCanonicalResponseLogs() = %#v, %v", canonicals, err)
	}
	observations, err := store.ListObservationLogs(ctx, 10)
	if err != nil || len(observations) != 1 || observations[0].ReviewStatus != "pending" {
		t.Fatalf("ListObservationLogs() = %#v, %v", observations, err)
	}
	metaUpdates, err := store.ListMetaProfileUpdates(ctx, 10)
	if err != nil || len(metaUpdates) != 1 || metaUpdates[0].UpdateID != "meta_upd_1" {
		t.Fatalf("ListMetaProfileUpdates() = %#v, %v", metaUpdates, err)
	}
	sessions, err := store.ListInterfaceSessions(ctx, 10)
	if err != nil || len(sessions) != 1 || sessions[0].SessionKey != "web:viewer" {
		t.Fatalf("ListInterfaceSessions() = %#v, %v", sessions, err)
	}
}

func TestJSONLStoreRejectsSensitiveAutoApprovedObservation(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	err := store.SaveObservationLog(context.Background(), domainpersona.ObservationLog{
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

func TestJSONLStoreCompactsOperationalInterfaceSessionsOnly(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	if err := store.SaveObservationLog(ctx, domainpersona.ObservationLog{
		EventID:         "evt_observation_keep",
		ObserverID:      "lumina",
		TargetID:        "ren",
		ObservationType: "daily",
		Summary:         "有益な観測候補",
		Sensitivity:     "normal",
		ReviewStatus:    "pending",
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("SaveObservationLog() error = %v", err)
	}
	for i := 0; i < interfaceSessionMaxRecords+3; i++ {
		if err := store.SaveInterfaceSession(ctx, domainpersona.InterfaceSession{
			SessionID:     "persona_session_" + strconv.Itoa(i),
			CharacterID:   "mio",
			InterfaceType: "web",
			SessionKey:    "web:viewer",
			CreatedAt:     now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("SaveInterfaceSession(%d) error = %v", i, err)
		}
	}

	if err := store.CompactOperationalLogs(); err != nil {
		t.Fatalf("CompactOperationalLogs() error = %v", err)
	}
	sessions, err := store.ListInterfaceSessions(ctx, interfaceSessionMaxRecords+10)
	if err != nil {
		t.Fatalf("ListInterfaceSessions() error = %v", err)
	}
	if len(sessions) != interfaceSessionMaxRecords {
		t.Fatalf("sessions len=%d want %d", len(sessions), interfaceSessionMaxRecords)
	}
	if sessions[0].SessionID != "persona_session_"+strconv.Itoa(interfaceSessionMaxRecords+2) {
		t.Fatalf("newest session=%q", sessions[0].SessionID)
	}
	observations, err := store.ListObservationLogs(ctx, 10)
	if err != nil || len(observations) != 1 || observations[0].EventID != "evt_observation_keep" {
		t.Fatalf("observations=%#v err=%v", observations, err)
	}
}

func TestJSONLStoreApplyMetaProfileUpdateAppendsApprovedContent(t *testing.T) {
	root := t.TempDir()
	metaRoot := filepath.Join(root, "characters")
	store := NewJSONLStoreWithMetaRoot(filepath.Join(root, "logs"), metaRoot)

	appliedPath, err := store.ApplyMetaProfileUpdate(context.Background(), domainpersona.MetaProfileUpdate{
		UpdateID:        "meta_upd_1",
		ObserverID:      "lumina",
		TargetID:        "ren",
		Section:         "Risk Signs",
		ProposedContent: "疲労時は判断を急がない方がよい",
		EvidenceRefs:    []string{"evt_observation_1"},
		Sensitivity:     "health",
		ReviewStatus:    "approved",
		CreatedAt:       time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
		ReviewedAt:      time.Date(2026, 5, 18, 13, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ApplyMetaProfileUpdate failed: %v", err)
	}
	expected := filepath.Join(metaRoot, "observers", "lumina", "meta", "ren.md")
	if appliedPath != expected {
		t.Fatalf("path=%q want %q", appliedPath, expected)
	}
	content, err := os.ReadFile(expected)
	if err != nil {
		t.Fatalf("read applied file: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "## Risk Signs") || !strings.Contains(text, "疲労時は判断を急がない方がよい") || !strings.Contains(text, "evt_observation_1") {
		t.Fatalf("unexpected content: %q", text)
	}
}

func TestJSONLStoreApplyMetaProfileUpdateRejectsUnsafeTarget(t *testing.T) {
	store := NewJSONLStoreWithMetaRoot(t.TempDir(), t.TempDir())
	_, err := store.ApplyMetaProfileUpdate(context.Background(), domainpersona.MetaProfileUpdate{
		UpdateID:        "meta_upd_1",
		ObserverID:      "lumina",
		TargetID:        "../ren",
		Section:         "Risk Signs",
		ProposedContent: "escape",
		Sensitivity:     "normal",
		ReviewStatus:    "approved",
		CreatedAt:       time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	})
	if err == nil || !strings.Contains(err.Error(), "invalid target_id") {
		t.Fatalf("expected unsafe target id to fail, got %v", err)
	}
}

func TestJSONLStoreApplyMetaProfileUpdateRequiresApprovedReview(t *testing.T) {
	store := NewJSONLStoreWithMetaRoot(t.TempDir(), t.TempDir())
	_, err := store.ApplyMetaProfileUpdate(context.Background(), domainpersona.MetaProfileUpdate{
		UpdateID:        "meta_upd_1",
		ObserverID:      "lumina",
		TargetID:        "ren",
		Section:         "Risk Signs",
		ProposedContent: "候補",
		Sensitivity:     "normal",
		ReviewStatus:    "pending",
		CreatedAt:       time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	})
	if err == nil || !strings.Contains(err.Error(), "must be approved") {
		t.Fatalf("expected pending review to fail, got %v", err)
	}
}
