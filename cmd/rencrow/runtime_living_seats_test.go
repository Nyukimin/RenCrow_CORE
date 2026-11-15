package main

import "testing"

func TestLivingConversationUsesDistinctSeatsAndTracksCurrentTurn(t *testing.T) {
	for _, room := range []string{"living", "dining"} {
		t.Run(room, func(t *testing.T) {
			runtime := &agentRoutineRuntime{}
			end := runtime.showLivingConversation("mio", "shiro", room)
			host := runtime.livingConversation["mio"]
			guest := runtime.livingConversation["shiro"]
			if host.slot == "" || guest.slot == "" || host.slot == guest.slot ||
				livingSeatRoom(host.slot) != room || livingSeatRoom(guest.slot) != room ||
				host.pose != "" || guest.pose != "" {
				t.Fatalf("unexpected conversation seats: host=%+v guest=%+v", host, guest)
			}
			runtime.livingConversationSpeaking("mio", true)
			if runtime.livingConversation["mio"].pose != "talk" || runtime.livingConversation["shiro"].pose != "" {
				t.Fatal("only the active participant should use the talk pose")
			}
			runtime.livingConversationSpeaking("mio", false)
			runtime.livingConversationSpeaking("shiro", true)
			if runtime.livingConversation["mio"].pose != "" || runtime.livingConversation["shiro"].pose != "talk" {
				t.Fatal("the second turn should transfer the talk pose")
			}
			end()
			if len(runtime.livingConversation) != 0 {
				t.Fatal("ending the conversation did not release both seat targets")
			}
		})
	}
}
