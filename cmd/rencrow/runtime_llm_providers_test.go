package main

import "testing"

func TestSelectChatConversationProviderPrefersChatForMio(t *testing.T) {
	chat := fakeConversationProvider{name: "chat-provider"}
	chatWorker := fakeConversationProvider{name: "chatworker-provider"}

	got := selectChatConversationProvider(chatWorker, chat)
	if got == nil || got.Name() != "chat-provider" {
		t.Fatalf("provider = %#v, want chat-provider", got)
	}
}

func TestSelectChatConversationProviderFallsBackToChatWorker(t *testing.T) {
	chatWorker := fakeConversationProvider{name: "chatworker-provider"}

	got := selectChatConversationProvider(chatWorker, nil)
	if got == nil || got.Name() != "chatworker-provider" {
		t.Fatalf("provider = %#v, want chatworker-provider", got)
	}
}
