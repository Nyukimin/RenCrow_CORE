package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTaskOriginPublicationProvenancePersistsAndCannotChangeAfterReopen(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		t.Run(map[bool]string{false: "fold", true: "indexed"}[indexed], func(t *testing.T) {
			root := t.TempDir()
			options := OpenOptions{Index: indexed}
			store, err := NewJSONLStoreWithOptions(root, options)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			sessionID := modulecore.NewSessionID()
			address, err := conversation.NewChannelAddress("viewer", "task-origin-chat")
			if err != nil {
				t.Fatal(err)
			}
			binding, err := conversation.NewSessionBindingWithPublicationIncarnation(
				sessionID, address, conversation.SessionAudiencePublic, "", 0,
				"0123456789abcdef0123456789abcdef",
			)
			if err != nil {
				t.Fatal(err)
			}
			provenance, err := domaintask.NewOriginPublicationProvenance(binding)
			if err != nil {
				t.Fatal(err)
			}
			created := snapshotTestTask(time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))
			created.OriginSessionID = sessionID
			created.OriginPublication = &provenance
			if err := store.SaveTask(ctx, created); err != nil {
				t.Fatalf("save initial Task: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("close Task store: %v", err)
			}

			store, err = NewJSONLStoreWithOptions(root, options)
			if err != nil {
				t.Fatalf("reopen Task store: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			loaded, err := store.GetTask(ctx, created.TaskID)
			if err != nil {
				t.Fatalf("read Task after reopen: %v", err)
			}
			if loaded.OriginPublication == nil || *loaded.OriginPublication != provenance {
				t.Fatalf("reopened Task provenance = %#v, want %#v", loaded.OriginPublication, provenance)
			}

			changed := loaded
			changed.UpdatedAt = changed.UpdatedAt.Add(time.Second)
			changedProof := *changed.OriginPublication
			changedProof.PublicationIncarnation = "abcdef0123456789abcdef0123456789"
			changed.OriginPublication = &changedProof
			if err := store.SaveTask(ctx, changed); !errors.Is(err, ErrTaskOriginPublicationProvenanceImmutable) {
				t.Fatalf("changed publication incarnation SaveTask error = %v, want immutable provenance error", err)
			}
			changed = loaded
			changed.UpdatedAt = changed.UpdatedAt.Add(time.Second)
			changedProof = *changed.OriginPublication
			changedProof.ChannelAddress = "different-chat"
			changed.OriginPublication = &changedProof
			if err := store.SaveTask(ctx, changed); !errors.Is(err, ErrTaskOriginPublicationProvenanceImmutable) {
				t.Fatalf("changed publication address SaveTask error = %v, want immutable provenance error", err)
			}

			legacy := snapshotTestTask(time.Date(2026, 10, 24, 12, 1, 0, 0, time.UTC))
			if err := store.SaveTask(ctx, legacy); err != nil {
				t.Fatalf("save legacy Task without publication proof: %v", err)
			}
			backfilled := legacy
			backfilled.OriginSessionID = sessionID
			backfilled.OriginPublication = &provenance
			backfilled.UpdatedAt = backfilled.UpdatedAt.Add(time.Second)
			if err := store.SaveTask(ctx, backfilled); !errors.Is(err, ErrTaskOriginPublicationProvenanceImmutable) {
				t.Fatalf("legacy proof backfill SaveTask error = %v, want immutable provenance error", err)
			}
		})
	}
}
