package persona

import (
	"strings"
	"testing"
	"time"
)

func TestValidateDiscomfortLog(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 20, 0, 0, time.UTC)
	item := DiscomfortLog{
		EventID:     "evt_persona_discomfort_1",
		CharacterID: "mio",
		Discomfort:  "作業中なのに雑談へ広げすぎた",
		Status:      "candidate",
		CreatedAt:   now,
	}
	if err := ValidateDiscomfortLog(item); err != nil {
		t.Fatalf("ValidateDiscomfortLog() error = %v", err)
	}
	item.Discomfort = ""
	if err := ValidateDiscomfortLog(item); err == nil {
		t.Fatal("expected missing discomfort to fail")
	}
}

func TestValidateTriggerLogConfidence(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 20, 0, 0, time.UTC)
	item := TriggerLog{
		EventID:     "evt_trigger_1",
		CharacterID: "kuro",
		TriggerID:   "danger_destructive",
		Activated:   true,
		Confidence:  0.8,
		CreatedAt:   now,
	}
	if err := ValidateTriggerLog(item); err != nil {
		t.Fatalf("ValidateTriggerLog() error = %v", err)
	}
	item.Confidence = 1.2
	if err := ValidateTriggerLog(item); err == nil {
		t.Fatal("expected confidence > 1 to fail")
	}
}

func TestValidateObservationLogRequiresReviewForSensitive(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 20, 0, 0, time.UTC)
	item := ObservationLog{
		EventID:         "evt_observation_1",
		ObserverID:      "lumina",
		TargetID:        "ren",
		ObservationType: "daily",
		Summary:         "sensitive observation candidate",
		Sensitivity:     "health",
		ReviewStatus:    "pending",
		CreatedAt:       now,
	}
	if err := ValidateObservationLog(item); err != nil {
		t.Fatalf("ValidateObservationLog() error = %v", err)
	}
	item.ReviewStatus = "approved"
	if err := ValidateObservationLog(item); err == nil {
		t.Fatal("expected sensitive auto-approved observation to fail")
	}
}

func TestValidateMetaProfileUpdateReviewRequiresTerminalStatus(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 20, 0, 0, time.UTC)
	item := MetaProfileUpdate{
		UpdateID:        "meta_upd_1",
		ObserverID:      "lumina",
		TargetID:        "ren",
		Section:         "Risk Signs",
		ProposedContent: "疲労時は判断を急がない方がよい",
		Sensitivity:     "health",
		ReviewStatus:    "pending",
		CreatedAt:       now,
	}
	if err := ValidateMetaProfileUpdate(item); err != nil {
		t.Fatalf("ValidateMetaProfileUpdate() error = %v", err)
	}
	if err := ValidateMetaProfileUpdateReview(item); err == nil {
		t.Fatal("expected pending meta update review to fail")
	}
	item.ReviewStatus = "approved"
	if err := ValidateMetaProfileUpdateReview(item); err != nil {
		t.Fatalf("ValidateMetaProfileUpdateReview() error = %v", err)
	}
}

func TestValidateInterfaceSession(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 20, 0, 0, time.UTC)
	item := InterfaceSession{
		SessionID:     "persona_session_1",
		CharacterID:   "mio",
		InterfaceType: "web",
		SessionKey:    "web:viewer-session",
		CreatedAt:     now,
	}
	if err := ValidateInterfaceSession(item); err != nil {
		t.Fatalf("ValidateInterfaceSession() error = %v", err)
	}
	item.SessionKey = ""
	if err := ValidateInterfaceSession(item); err == nil {
		t.Fatal("expected missing session_key to fail")
	}
}

func TestValidatePersonaRejectsMissingCreatedAt(t *testing.T) {
	cases := []struct {
		name string
		run  func() error
	}{
		{
			name: "discomfort",
			run: func() error {
				return ValidateDiscomfortLog(DiscomfortLog{EventID: "evt_1", CharacterID: "mio", Discomfort: "違和感", Status: "candidate"})
			},
		},
		{
			name: "trigger",
			run: func() error {
				return ValidateTriggerLog(TriggerLog{EventID: "evt_1", CharacterID: "kuro", TriggerID: "danger", Confidence: 0.8})
			},
		},
		{
			name: "canonical",
			run: func() error {
				return ValidateCanonicalResponseLog(CanonicalResponseLog{EventID: "evt_1", CharacterID: "kuro", ResponseID: "block_destructive"})
			},
		},
		{
			name: "observation",
			run: func() error {
				return ValidateObservationLog(ObservationLog{EventID: "evt_1", ObserverID: "lumina", TargetID: "ren", ObservationType: "daily", Sensitivity: "normal", ReviewStatus: "pending"})
			},
		},
		{
			name: "meta profile",
			run: func() error {
				return ValidateMetaProfileUpdate(MetaProfileUpdate{UpdateID: "meta_1", ObserverID: "lumina", TargetID: "ren", Section: "Risk Signs", ProposedContent: "疲労時は判断を急がない", Sensitivity: "health", ReviewStatus: "pending"})
			},
		},
		{
			name: "interface session",
			run: func() error {
				return ValidateInterfaceSession(InterfaceSession{SessionID: "session_1", CharacterID: "mio", InterfaceType: "web", SessionKey: "web:viewer"})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("expected created_at error")
			}
			if !strings.Contains(err.Error(), "created_at") {
				t.Fatalf("expected created_at error, got %v", err)
			}
		})
	}
}

func TestValidatePersonaRejectsMissingRequiredFields(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 20, 0, 0, time.UTC)
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "discomfort missing event id", err: ValidateDiscomfortLog(DiscomfortLog{CharacterID: "mio", Discomfort: "違和感", Status: "candidate", CreatedAt: now}), want: "event_id"},
		{name: "discomfort missing character id", err: ValidateDiscomfortLog(DiscomfortLog{EventID: "evt_1", Discomfort: "違和感", Status: "candidate", CreatedAt: now}), want: "character_id"},
		{name: "discomfort missing status", err: ValidateDiscomfortLog(DiscomfortLog{EventID: "evt_1", CharacterID: "mio", Discomfort: "違和感", CreatedAt: now}), want: "status"},
		{name: "trigger missing event id", err: ValidateTriggerLog(TriggerLog{CharacterID: "kuro", TriggerID: "danger", Confidence: 0.8, CreatedAt: now}), want: "event_id"},
		{name: "trigger missing character id", err: ValidateTriggerLog(TriggerLog{EventID: "evt_1", TriggerID: "danger", Confidence: 0.8, CreatedAt: now}), want: "character_id"},
		{name: "trigger missing trigger id", err: ValidateTriggerLog(TriggerLog{EventID: "evt_1", CharacterID: "kuro", Confidence: 0.8, CreatedAt: now}), want: "trigger_id"},
		{name: "trigger negative confidence", err: ValidateTriggerLog(TriggerLog{EventID: "evt_1", CharacterID: "kuro", TriggerID: "danger", Confidence: -0.1, CreatedAt: now}), want: "confidence"},
		{name: "canonical missing event id", err: ValidateCanonicalResponseLog(CanonicalResponseLog{CharacterID: "kuro", ResponseID: "block_destructive", CreatedAt: now}), want: "event_id"},
		{name: "canonical missing character id", err: ValidateCanonicalResponseLog(CanonicalResponseLog{EventID: "evt_1", ResponseID: "block_destructive", CreatedAt: now}), want: "character_id"},
		{name: "canonical missing response id", err: ValidateCanonicalResponseLog(CanonicalResponseLog{EventID: "evt_1", CharacterID: "kuro", CreatedAt: now}), want: "response_id"},
		{name: "observation missing event id", err: ValidateObservationLog(ObservationLog{ObserverID: "lumina", TargetID: "ren", ObservationType: "daily", Sensitivity: "normal", ReviewStatus: "pending", CreatedAt: now}), want: "event_id"},
		{name: "observation missing observer id", err: ValidateObservationLog(ObservationLog{EventID: "evt_1", TargetID: "ren", ObservationType: "daily", Sensitivity: "normal", ReviewStatus: "pending", CreatedAt: now}), want: "observer_id"},
		{name: "observation missing target id", err: ValidateObservationLog(ObservationLog{EventID: "evt_1", ObserverID: "lumina", ObservationType: "daily", Sensitivity: "normal", ReviewStatus: "pending", CreatedAt: now}), want: "target_id"},
		{name: "observation missing type", err: ValidateObservationLog(ObservationLog{EventID: "evt_1", ObserverID: "lumina", TargetID: "ren", Sensitivity: "normal", ReviewStatus: "pending", CreatedAt: now}), want: "observation_type"},
		{name: "observation missing sensitivity", err: ValidateObservationLog(ObservationLog{EventID: "evt_1", ObserverID: "lumina", TargetID: "ren", ObservationType: "daily", ReviewStatus: "pending", CreatedAt: now}), want: "sensitivity"},
		{name: "observation missing review status", err: ValidateObservationLog(ObservationLog{EventID: "evt_1", ObserverID: "lumina", TargetID: "ren", ObservationType: "daily", Sensitivity: "normal", CreatedAt: now}), want: "review_status"},
		{name: "meta missing update id", err: ValidateMetaProfileUpdate(MetaProfileUpdate{ObserverID: "lumina", TargetID: "ren", Section: "Risk Signs", ProposedContent: "疲労時は判断を急がない", Sensitivity: "health", ReviewStatus: "pending", CreatedAt: now}), want: "update_id"},
		{name: "meta missing observer id", err: ValidateMetaProfileUpdate(MetaProfileUpdate{UpdateID: "meta_1", TargetID: "ren", Section: "Risk Signs", ProposedContent: "疲労時は判断を急がない", Sensitivity: "health", ReviewStatus: "pending", CreatedAt: now}), want: "observer_id"},
		{name: "meta missing target id", err: ValidateMetaProfileUpdate(MetaProfileUpdate{UpdateID: "meta_1", ObserverID: "lumina", Section: "Risk Signs", ProposedContent: "疲労時は判断を急がない", Sensitivity: "health", ReviewStatus: "pending", CreatedAt: now}), want: "target_id"},
		{name: "meta missing section", err: ValidateMetaProfileUpdate(MetaProfileUpdate{UpdateID: "meta_1", ObserverID: "lumina", TargetID: "ren", ProposedContent: "疲労時は判断を急がない", Sensitivity: "health", ReviewStatus: "pending", CreatedAt: now}), want: "section"},
		{name: "meta missing proposed content", err: ValidateMetaProfileUpdate(MetaProfileUpdate{UpdateID: "meta_1", ObserverID: "lumina", TargetID: "ren", Section: "Risk Signs", Sensitivity: "health", ReviewStatus: "pending", CreatedAt: now}), want: "proposed_content"},
		{name: "meta missing sensitivity", err: ValidateMetaProfileUpdate(MetaProfileUpdate{UpdateID: "meta_1", ObserverID: "lumina", TargetID: "ren", Section: "Risk Signs", ProposedContent: "疲労時は判断を急がない", ReviewStatus: "pending", CreatedAt: now}), want: "sensitivity"},
		{name: "meta invalid review status", err: ValidateMetaProfileUpdate(MetaProfileUpdate{UpdateID: "meta_1", ObserverID: "lumina", TargetID: "ren", Section: "Risk Signs", ProposedContent: "疲労時は判断を急がない", Sensitivity: "health", ReviewStatus: "done", CreatedAt: now}), want: "review_status"},
		{name: "interface missing session id", err: ValidateInterfaceSession(InterfaceSession{CharacterID: "mio", InterfaceType: "web", SessionKey: "web:viewer", CreatedAt: now}), want: "session_id"},
		{name: "interface missing character id", err: ValidateInterfaceSession(InterfaceSession{SessionID: "session_1", InterfaceType: "web", SessionKey: "web:viewer", CreatedAt: now}), want: "character_id"},
		{name: "interface missing type", err: ValidateInterfaceSession(InterfaceSession{SessionID: "session_1", CharacterID: "mio", SessionKey: "web:viewer", CreatedAt: now}), want: "interface_type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil || !strings.Contains(tc.err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", tc.err, tc.want)
			}
		})
	}
}
