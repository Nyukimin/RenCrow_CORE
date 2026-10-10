package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func runtimeAcceptedOPSStoreValue(t *testing.T, runtime conversationRuntime) any {
	t.Helper()
	field, exists := reflect.TypeOf(runtime).FieldByName("AcceptedOPSInputStore")
	if !exists {
		t.Fatal("conversation runtime does not expose AcceptedOPSInputStore")
	}
	value := reflect.ValueOf(runtime).FieldByIndex(field.Index)
	if value.IsNil() {
		t.Fatal("conversation runtime AcceptedOPSInputStore is nil")
	}
	return value.Interface()
}

func TestRuntimeAcceptedOPSOwnerPortSelectsLocalAndRemoteOwnerWithoutFallback(t *testing.T) {
	localRoot := t.TempDir()
	localPath := filepath.Join(localRoot, "conversation-l1.sqlite")
	localRuntime := buildConversationRuntime(&config.Config{
		Conversation:  config.ConversationConfig{Enabled: false},
		Storage:       config.StorageConfig{Databases: config.DatabasePathsConfig{ConversationL1: localPath}},
		LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, UserID: "local-ops-user"},
	}, primaryLLMProviders{}, nil, nil, runtimeStorageOwnerBundle{})
	if localRuntime.L1Store == nil {
		t.Fatal("local L1 runtime did not initialize its configured owner")
	}
	defer localRuntime.L1Store.Close()
	if got := runtimeAcceptedOPSStoreValue(t, localRuntime); got != localRuntime.L1Store {
		t.Fatalf("local AcceptedOPSInputStore=%T %p, want local L1 owner %p", got, got, localRuntime.L1Store)
	}

	remoteRoot := t.TempDir()
	fixture := newRuntimeConversationStorageHostFixture(t, remoteRoot)
	localFallback := filepath.Join(remoteRoot, "must-not-open", "conversation-l1.sqlite")
	cfg := &config.Config{Storage: config.StorageConfig{
		Databases: config.DatabasePathsConfig{ConversationL1: localFallback},
	}}
	cfg.LocalAgentOps = config.LocalAgentOpsConfig{Enabled: true, UserID: "chat-user"}
	remoteRuntime := buildConversationRuntime(cfg, primaryLLMProviders{}, nil, nil, fixture.owners)
	if remoteRuntime.Closer != nil {
		t.Cleanup(func() { _ = remoteRuntime.Closer.Close() })
	}
	if remoteRuntime.L1Store != nil || remoteRuntime.ChatL1Store != fixture.owners.ConversationStore {
		t.Fatalf("remote owner selection used a local fallback: L1=%T ChatL1=%T", remoteRuntime.L1Store, remoteRuntime.ChatL1Store)
	}
	if got := runtimeAcceptedOPSStoreValue(t, remoteRuntime); got != remoteRuntime.ChatL1Store {
		t.Fatalf("remote AcceptedOPSInputStore=%T %p, want selected remote Conversation L1 owner %T %p", got, got, remoteRuntime.ChatL1Store, remoteRuntime.ChatL1Store)
	}
	remoteOwner, ok := remoteRuntime.ChatL1Store.(*runtimeRemoteConversationStorageOwner)
	if !ok || remoteOwner.L1StoreClient != fixture.l1 {
		t.Fatalf("remote Conversation L1 owner=%T, want fixture L1 client %p", remoteRuntime.ChatL1Store, fixture.l1)
	}
	if _, err := os.Stat(localFallback); !os.IsNotExist(err) {
		t.Fatalf("remote runtime created local Conversation L1 path %q (stat err=%v)", localFallback, err)
	}
}

func TestRuntimeAcceptedOPSPortStaysDisabledWithoutLocalAgentOps(t *testing.T) {
	localRoot := t.TempDir()
	runtime := buildConversationRuntime(&config.Config{
		Conversation: config.ConversationConfig{Enabled: false},
		Storage: config.StorageConfig{Databases: config.DatabasePathsConfig{
			ConversationL1: filepath.Join(localRoot, "conversation-l1.sqlite"),
		}},
	}, primaryLLMProviders{}, nil, nil, runtimeStorageOwnerBundle{})
	if runtime.L1Store == nil {
		t.Fatal("disabled accepted OPS feature prevented the existing local L1 owner from starting")
	}
	defer runtime.L1Store.Close()
	field, exists := reflect.TypeOf(runtime).FieldByName("AcceptedOPSInputStore")
	if !exists {
		t.Fatal("conversation runtime does not expose AcceptedOPSInputStore")
	}
	if value := reflect.ValueOf(runtime).FieldByIndex(field.Index); !value.IsNil() {
		t.Fatalf("AcceptedOPSInputStore=%T, want disabled when LocalAgentOps is not configured", value.Interface())
	}
}
