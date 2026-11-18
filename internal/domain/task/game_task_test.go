package task

import (
	"reflect"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestNormalizeGameSpecDefaultsAndRoster(t *testing.T) {
	cases := []struct {
		gameID   string
		personas []string
		turns    int
		mode     string
	}{
		{gameID: "nethack", personas: []string{"mio"}, turns: 60, mode: "auto"},
		{gameID: "territory_commander", personas: []string{"mio"}, turns: 120, mode: "auto"},
		{gameID: "survival_garden", personas: []string{"aka", "ao", "kin", "gin"}, turns: 120, mode: "auto"},
		{gameID: "herzog_zwei", personas: []string{"aka", "ao", "kin", "gin"}, turns: 40, mode: "realtime"},
	}
	for _, tc := range cases {
		t.Run(tc.gameID, func(t *testing.T) {
			spec, err := NormalizeGameSpec(GameSpec{GameID: tc.gameID})
			if err != nil {
				t.Fatalf("NormalizeGameSpec() error = %v", err)
			}
			if spec.GameID != tc.gameID || spec.Turns != tc.turns || spec.Mode != tc.mode || !reflect.DeepEqual(spec.Personas, tc.personas) {
				t.Fatalf("normalized spec = %+v", spec)
			}
			if err := spec.Validate(); err != nil {
				t.Fatalf("canonical spec validation: %v", err)
			}
		})
	}
}

func TestGameSpecRejectsNoncanonicalRosterWithoutMutatingCaller(t *testing.T) {
	personas := []string{" aka", "ao", "kin", "gin"}
	spec := GameSpec{GameID: "survival_garden", Personas: personas, Turns: 120, Mode: "auto"}
	if err := spec.Validate(); err == nil {
		t.Fatal("Validate accepted a noncanonical roster")
	}
	if !reflect.DeepEqual(personas, []string{" aka", "ao", "kin", "gin"}) {
		t.Fatalf("Validate mutated caller personas: %q", personas)
	}
	normalized, err := NormalizeGameSpec(spec)
	if err != nil || !reflect.DeepEqual(normalized.Personas, []string{"aka", "ao", "kin", "gin"}) {
		t.Fatalf("NormalizeGameSpec() = %+v, %v", normalized, err)
	}
	if !reflect.DeepEqual(personas, []string{" aka", "ao", "kin", "gin"}) {
		t.Fatalf("NormalizeGameSpec mutated caller personas: %q", personas)
	}
}

func TestNormalizeGameSpecRejectsWrongRosterModeAndTurnBounds(t *testing.T) {
	cases := []GameSpec{
		{GameID: "survival_garden", Personas: []string{"aka", "kin", "ao", "gin"}, Turns: 120, Mode: "auto"},
		{GameID: "herzog_zwei", Personas: []string{"aka", "ao", "kin", "gin"}, Turns: 120, Mode: "auto"},
		{GameID: "nethack", Personas: []string{"aka"}, Turns: 60, Mode: "auto"},
		{GameID: "territory_commander", Turns: -1},
		{GameID: "nethack", Turns: MaxGameTaskTurns + 1},
		{GameID: "unknown"},
	}
	for _, spec := range cases {
		if _, err := NormalizeGameSpec(spec); err == nil {
			t.Errorf("NormalizeGameSpec(%+v) succeeded", spec)
		}
	}
}

func TestNewGameProgressBindsOpaqueGameSessionSeparatelyFromCoreSessionIDs(t *testing.T) {
	taskID := modulecore.NewTaskID()
	spec, err := NormalizeGameSpec(GameSpec{GameID: "herzog_zwei"})
	if err != nil {
		t.Fatal(err)
	}
	progress, err := NewGameProgress(taskID, spec, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	progress.Status = GameExecutionRunning
	progress.SessionID = "herzog_zwei_20261118T123456789Z_1"
	progress.UpdatedAt = progress.CreatedAt.Add(time.Second)
	if err := progress.Validate(taskID, spec); err != nil {
		t.Fatalf("opaque GAMES session identifier rejected: %v", err)
	}
}
