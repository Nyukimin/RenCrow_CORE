package eventstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	appresearch "github.com/Nyukimin/RenCrow_CORE/internal/application/research"
)

func TestResearchBoardPublicationJournalFreezesExactPayloadAndReadback(t *testing.T) {
	ctx := context.Background()
	store, _, _, _ := newChatAdmissionStore(t, 8)
	now := time.Date(2026, 11, 28, 8, 0, 0, 0, time.UTC)
	pin := BoardConnectionPin{
		OwnerID: "owner-core", PrincipalID: "ren-core",
		JournalInstanceID: "journal_0123456789abcdef0123456789abcdef", CutoverAt: now.Add(-time.Hour),
	}
	if _, err := store.PinBoardConnection(ctx, pin); err != nil {
		t.Fatalf("PinBoardConnection: %v", err)
	}
	scope := appresearch.BoardPublicationScope{OwnerID: pin.OwnerID, PrincipalID: pin.PrincipalID, JournalInstanceID: pin.JournalInstanceID}
	body := "verified research report"
	bodyHash := sha256.Sum256([]byte(body))
	contentDigest := hex.EncodeToString(bodyHash[:])
	destination := appresearch.BoardDestination{
		Ref: "research-weekly", OwnerID: pin.OwnerID, PrincipalID: pin.PrincipalID,
		ChannelID: "research", NewThread: true, Recipients: []string{"agent-a"},
		DataScope: "public", Title: "Research report",
	}
	operationID := appresearch.BoardOperationID(scope, destination.Ref, destination.DataScope, destination.ChannelID, destination.ThreadID, destination.NewThread, destination.Title, contentDigest, destination.Recipients)
	client := (*boardcommunication.Client)(nil)
	prepared, err := client.PrepareSend(boardcommunication.SendRequest{
		ChannelID: destination.ChannelID, Title: destination.Title, Body: body, Recipients: destination.Recipients,
		Kind: "", IdempotencyKey: operationID,
	})
	if err != nil {
		t.Fatalf("PrepareSend: %v", err)
	}
	preparedBytes := prepared.JSONBytes()
	payloadHash := sha256.Sum256(preparedBytes)
	input := appresearch.BoardPublicationClaimInput{
		Scope: scope, OperationID: operationID, DestinationRef: destination.Ref, DataScope: destination.DataScope,
		ChannelID: destination.ChannelID, NewThread: destination.NewThread, Title: destination.Title,
		Recipients: destination.Recipients, ContentDigest: contentDigest, IdempotencyKey: operationID,
		PreparedBytes: preparedBytes, PayloadSHA256: hex.EncodeToString(payloadHash[:]),
		Now: now, LeaseDuration: time.Minute,
	}
	lease, claimed, err := store.ClaimResearchBoardPublication(ctx, input)
	if err != nil || !claimed {
		t.Fatalf("first claim = claimed %t, err %v", claimed, err)
	}
	tampered := input
	tampered.PreparedBytes = append([]byte(nil), input.PreparedBytes...)
	tampered.PreparedBytes[len(tampered.PreparedBytes)-1] ^= 1
	tamperedHash := sha256.Sum256(tampered.PreparedBytes)
	tampered.PayloadSHA256 = hex.EncodeToString(tamperedHash[:])
	if _, claimedTampered, err := store.ClaimResearchBoardPublication(ctx, tampered); !errors.Is(err, ErrResearchBoardPublicationConflict) || claimedTampered {
		t.Fatalf("same operation with different prepared bytes = claimed %t, err %v", claimedTampered, err)
	}
	if again, claimedAgain, err := store.ClaimResearchBoardPublication(ctx, input); err != nil || claimedAgain || again.OperationID != "" {
		t.Fatalf("active duplicate claim = %#v, claimed %t, err %v; want fenced", again, claimedAgain, err)
	}
	attempt, err := store.RecordResearchBoardPublicationAttempt(ctx, lease, now.Add(time.Second))
	if err != nil || attempt.Ordinal != 1 || string(attempt.PreparedBytes) != string(preparedBytes) || attempt.PayloadSHA256 != input.PayloadSHA256 {
		t.Fatalf("record exact attempt = %#v, err %v", attempt, err)
	}
	receipt := appresearch.BoardPublicationSendReceipt{MessageID: "message-001", ThreadID: "thread-001", Status: "stored", StoredAt: now.Add(2 * time.Second)}
	canonical := appresearch.BoardPublicationCanonicalMessage{
		MessageID: receipt.MessageID, ThreadID: receipt.ThreadID, ChannelID: destination.ChannelID,
		SenderPrincipalID: pin.PrincipalID, Recipients: []string{"agent-a"}, BodyDigest: contentDigest,
	}
	outcome, err := store.CompleteResearchBoardPublication(ctx, appresearch.BoardPublicationCompletionInput{
		Lease: lease, Now: now.Add(3 * time.Second), AttemptOrdinal: attempt.Ordinal,
		AttemptSHA256: attempt.PayloadSHA256, SendReceipt: receipt, CanonicalMessage: canonical,
	})
	if err != nil {
		t.Fatalf("CompleteResearchBoardPublication: %v", err)
	}
	loaded, found, err := store.LoadResearchBoardPublication(ctx, scope, operationID)
	if err != nil || !found || !reflect.DeepEqual(loaded, outcome) || loaded.DestinationRef != destination.Ref || loaded.Canonical.BodyDigest != contentDigest {
		t.Fatalf("LoadResearchBoardPublication = %#v, found %t, err %v", loaded, found, err)
	}
	if _, found, err := store.LoadResearchBoardPublication(ctx, scope, operationID+"x"); err == nil || found {
		t.Fatalf("malformed operation lookup = found %t, err %v", found, err)
	}
	otherScope := scope
	otherScope.OwnerID = "owner-other"
	if _, found, err := store.LoadResearchBoardPublication(ctx, otherScope, operationID); !errors.Is(err, ErrResearchBoardPublicationScopeMismatch) || found {
		t.Fatalf("foreign owner readback = found %t, err %v", found, err)
	}
}
