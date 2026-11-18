package main

import (
	"reflect"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTasksOnlineGameCreateDispatchAndRequestKeyParsing(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want bool
	}{
		{name: "typed game route", args: []string{"create", "--title", "play", "--route", "GAME"}, want: true},
		{name: "game option implies attempted game create", args: []string{"create", "--title", "play", "--game-id", "herzog_zwei"}, want: true},
		{name: "ordinary chat create stays local", args: []string{"create", "--title", "write", "--route", "CHAT"}},
		{name: "other task command stays local", args: []string{"show", "tsk_01a120e8-5f16-748f-9cc6-ebe0b51d8cc7"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isTasksOnlineGameCreateSubcommand(test.args); got != test.want {
				t.Fatalf("isTasksOnlineGameCreateSubcommand(%v)=%t, want %t", test.args, got, test.want)
			}
		})
	}

	id := modulecore.NewRequestID()
	gotID, args, err := parseTaskCreateRequestID([]string{"--title", "play", "--request-id", string(id), "--route", "GAME"})
	if err != nil || gotID != id || !reflect.DeepEqual(args, []string{"--title", "play", "--route", "GAME"}) {
		t.Fatalf("parseTaskCreateRequestID()=(%q,%v,%v)", gotID, args, err)
	}
	if _, _, err := parseTaskCreateRequestID([]string{"--request-id", string(id), "--request-id", string(id)}); err == nil {
		t.Fatal("duplicate request key was accepted")
	}
	if _, _, err := parseTaskCreateRequestID([]string{"--request-id", "not-a-request-id"}); err == nil {
		t.Fatal("noncanonical request key was accepted")
	}
}

func TestTaskCreateRequestFromCLIUsesExactTypedGameInput(t *testing.T) {
	id := modulecore.NewRequestID()
	draft, shared, _, err := parseTasksCreateArgs([]string{
		"--title", "Play Herzog",
		"--route", "GAME",
		"--game-id", "herzog_zwei",
		"--game-personas", "aka,ao,kin,gin",
		"--game-turns", "120",
		"--game-mode", "realtime",
		"--assignee", "mio",
		"--owner-id", "ren",
		"--plan", "Play through the actual game",
	})
	if err != nil {
		t.Fatalf("parseTasksCreateArgs(): %v", err)
	}
	request := taskCreateRequestFromCLI(id, draft, shared)
	if request.RequestID != id || request.Title != "Play Herzog" || request.Route != domaintask.RouteGAME ||
		request.OwnerID != "ren" || request.Assignee != "mio" || request.Plan != "Play through the actual game" ||
		request.GameSpec == nil || request.GameSpec.GameID != "herzog_zwei" || request.GameSpec.Turns != 120 || request.GameSpec.Mode != "realtime" ||
		!reflect.DeepEqual(request.GameSpec.Personas, []string{"aka", "ao", "kin", "gin"}) {
		t.Fatalf("GAME create request=%+v", request)
	}
}
