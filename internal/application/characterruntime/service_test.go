package characterruntime

import (
	"context"
	"encoding/json"
	"strings"
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

func TestRunRoundAssignsOpaqueConversationIdentity(t *testing.T) {
	result, err := NewService().RunRound(context.Background(), RunRequest{
		UserMessage: "IDを確認して",
		Characters:  []string{"mio", "shiro"},
	})
	if err != nil {
		t.Fatalf("RunRound() error = %v", err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var body struct {
		TraceID       string `json:"trace_id"`
		UserMessageID string `json:"user_message_id"`
		Turns         []struct {
			MessageID string `json:"message_id"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !strings.HasPrefix(body.TraceID, "trc_") {
		t.Fatalf("trace_id = %q", body.TraceID)
	}
	if !strings.HasPrefix(body.UserMessageID, "msg_") {
		t.Fatalf("user_message_id = %q", body.UserMessageID)
	}
	if len(body.Turns) != 2 {
		t.Fatalf("turn count = %d", len(body.Turns))
	}
	seen := map[string]bool{body.UserMessageID: true}
	for i, turn := range body.Turns {
		if !strings.HasPrefix(turn.MessageID, "msg_") {
			t.Fatalf("turn %d message_id = %q", i, turn.MessageID)
		}
		if seen[turn.MessageID] {
			t.Fatalf("duplicate message_id = %q", turn.MessageID)
		}
		seen[turn.MessageID] = true
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
