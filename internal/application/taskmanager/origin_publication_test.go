package taskmanager

import (
	"context"
	"testing"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestManagerDerivesTaskPublicationProofFromMatchingValidatedBinding(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	sessionID := modulecore.NewSessionID()
	address, err := domainconversation.NewChannelAddress("viewer", "manager-origin-chat")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := domainconversation.NewSessionBindingWithPublicationIncarnation(
		sessionID, address, domainconversation.SessionAudiencePublic, "", 0,
		"0123456789abcdef0123456789abcdef",
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := domainconversation.WithSessionBinding(context.Background(), binding)
	created, err := manager.Create(ctx, domaintask.Task{
		Title: "bound root Task", Route: domaintask.RouteGeneral, OriginSessionID: sessionID,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("Create with validated binding: %v", err)
	}
	want, err := domaintask.NewOriginPublicationProvenance(binding)
	if err != nil {
		t.Fatal(err)
	}
	if created.OriginPublication == nil || *created.OriginPublication != want {
		t.Fatalf("created origin publication = %#v, want %#v", created.OriginPublication, want)
	}
	loaded, err := manager.Get(context.Background(), created.TaskID)
	if err != nil || loaded.OriginPublication == nil || *loaded.OriginPublication != want {
		t.Fatalf("loaded origin publication = %#v err=%v, want %#v", loaded.OriginPublication, err, want)
	}
}

func TestManagerRejectsCallerProofThatDoesNotMatchValidatedBinding(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	sessionID := modulecore.NewSessionID()
	address, err := domainconversation.NewChannelAddress("viewer", "manager-proof-chat")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := domainconversation.NewSessionBindingWithPublicationIncarnation(
		sessionID, address, domainconversation.SessionAudiencePublic, "", 0,
		"0123456789abcdef0123456789abcdef",
	)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := domaintask.NewOriginPublicationProvenance(binding)
	if err != nil {
		t.Fatal(err)
	}
	proof.ChannelAddress = "different-chat"
	ctx := domainconversation.WithSessionBinding(context.Background(), binding)
	if _, err := manager.Create(ctx, domaintask.Task{
		Title: "spoofed root Task", Route: domaintask.RouteGeneral,
		OriginSessionID: sessionID, OriginPublication: &proof,
	}, domaintask.SharedRoleContext{}); err == nil {
		t.Fatal("Create accepted caller-supplied origin proof that differs from the resolved SessionBinding")
	}
}
