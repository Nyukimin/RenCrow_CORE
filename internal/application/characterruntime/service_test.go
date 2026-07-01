package characterruntime

import (
	"context"
	"testing"
)

func TestRunRoundIncludesAllSixCharacters(t *testing.T) {
	result, err := NewService().RunRound(context.Background(), RunRequest{UserMessage: "実装を進めて"})
	if err != nil {
		t.Fatalf("RunRound() error = %v", err)
	}
	if result.Mode != "six_character_round" || len(result.Participants) != 6 || len(result.Turns) != 6 {
		t.Fatalf("unexpected result: %#v", result)
	}
	want := []string{"mio", "shiro", "ao", "aka", "kin", "gin"}
	for i, id := range want {
		if result.Turns[i].CharacterID != id || result.Turns[i].TurnIndex != i+1 {
			t.Fatalf("turn %d = %#v", i, result.Turns[i])
		}
	}
}

func TestRunRoundSupportsScopedCharacters(t *testing.T) {
	result, err := NewService().RunRound(context.Background(), RunRequest{
		UserMessage: "確認して",
		Characters:  []string{"gin", "mio", "gin"},
		MaxTurns:    2,
	})
	if err != nil {
		t.Fatalf("RunRound() error = %v", err)
	}
	if len(result.Participants) != 2 || result.Turns[0].CharacterID != "gin" || result.Turns[1].CharacterID != "mio" {
		t.Fatalf("unexpected scoped result: %#v", result)
	}
}

func TestRunRoundRejectsUnknownCharacter(t *testing.T) {
	_, err := NewService().RunRound(context.Background(), RunRequest{
		UserMessage: "確認して",
		Characters:  []string{"unknown"},
	})
	if err == nil {
		t.Fatal("expected unknown character error")
	}
}
