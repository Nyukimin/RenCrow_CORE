package boardresearch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	appresearch "github.com/Nyukimin/RenCrow_CORE/internal/application/research"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

const (
	boardPublicationLeaseDuration = 45 * time.Second
	boardPublicationPageSize      = 100
	boardPublicationMaxPages      = 20
)

var ErrUnavailable = errors.New("research BOARD destination is unavailable")

type Owner struct {
	client       *boardcommunication.Client
	journal      appresearch.BoardPublicationJournal
	pin          eventstore.BoardConnectionPin
	destinations map[string]appresearch.BoardDestination
	now          func() time.Time
}

// NewOwner binds the Research output owner to the existing authenticated
// BOARD client, immutable CORE pin, and administrative destination set.
func NewOwner(
	client *boardcommunication.Client,
	journal appresearch.BoardPublicationJournal,
	pin eventstore.BoardConnectionPin,
	destinations []appresearch.BoardDestination,
	now func() time.Time,
) (*Owner, error) {
	if client == nil || journal == nil || !validPin(pin) {
		return nil, ErrUnavailable
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	registered := make(map[string]appresearch.BoardDestination, len(destinations))
	for _, destination := range destinations {
		if destination.Validate() != nil || destination.OwnerID != pin.OwnerID || destination.PrincipalID != pin.PrincipalID || destination.DataScope != "user" {
			return nil, ErrUnavailable
		}
		if !validBoardID(destination.ChannelID) || destination.ThreadID != "" && !validBoardID(destination.ThreadID) {
			return nil, ErrUnavailable
		}
		for _, recipient := range destination.Recipients {
			if !validBoardID(recipient) {
				return nil, ErrUnavailable
			}
		}
		if _, exists := registered[destination.Ref]; exists {
			return nil, ErrUnavailable
		}
		destination.Recipients = append([]string(nil), destination.Recipients...)
		registered[destination.Ref] = destination
	}
	return &Owner{client: client, journal: journal, pin: pin, destinations: registered, now: now}, nil
}

func validPin(pin eventstore.BoardConnectionPin) bool {
	return validBoardID(pin.OwnerID) && validBoardID(pin.PrincipalID) &&
		len(pin.JournalInstanceID) == len("journal_")+32 && strings.HasPrefix(pin.JournalInstanceID, "journal_") &&
		validLowerHex(pin.JournalInstanceID[len("journal_"):]) && !pin.CutoverAt.IsZero() && pin.CutoverAt.Location() == time.UTC
}

func (o *Owner) Commit(ctx context.Context, request engine.CommitRequest) (c.Receipt, error) {
	if ctx == nil || o == nil || request.Target.Target != appresearch.BoardOwnerRoute || request.Document != nil || request.Report == "" || !validDigest(request.Digest) || digest([]byte(request.Report)) != request.Digest {
		return c.Receipt{}, ErrUnavailable
	}
	destination, ok := o.destinations[request.Target.DestinationRef]
	if !ok || !authorizedEnvelope(request.Envelope, destination, o.pin) || request.IdempotencyKey != appresearch.IdempotencyKey(firstRunID(request), request.Target.Target, request.Target.DestinationRef, request.Digest) {
		return c.Receipt{}, ErrUnavailable
	}
	if err := o.verifyPinnedConnection(ctx); err != nil {
		return c.Receipt{}, ErrUnavailable
	}
	scope := appresearch.BoardPublicationScope{OwnerID: o.pin.OwnerID, PrincipalID: o.pin.PrincipalID, JournalInstanceID: o.pin.JournalInstanceID}
	operationID := appresearch.BoardOperationID(scope, destination.Ref, destination.DataScope, destination.ChannelID, destination.ThreadID, destination.NewThread, destination.Title, request.Digest, destination.Recipients)
	prepared, err := o.client.PrepareSend(boardcommunication.SendRequest{
		ChannelID: destination.ChannelID, ThreadID: destination.ThreadID, Title: destination.Title,
		Body: request.Report, Recipients: destination.Recipients, Kind: "", IdempotencyKey: operationID,
	})
	if err != nil {
		return c.Receipt{}, ErrUnavailable
	}
	preparedBytes := prepared.JSONBytes()
	payloadHash := prepared.SHA256()
	claimInput := appresearch.BoardPublicationClaimInput{
		Scope: scope, OperationID: operationID, DestinationRef: destination.Ref, DataScope: destination.DataScope,
		ChannelID: destination.ChannelID, ThreadID: destination.ThreadID, NewThread: destination.NewThread,
		Title: destination.Title, Recipients: append([]string(nil), destination.Recipients...), ContentDigest: request.Digest,
		IdempotencyKey: operationID, PreparedBytes: preparedBytes, PayloadSHA256: hex.EncodeToString(payloadHash[:]),
		Now: o.now().UTC(), LeaseDuration: boardPublicationLeaseDuration,
	}
	lease, claimed, err := o.journal.ClaimResearchBoardPublication(ctx, claimInput)
	if err != nil {
		return c.Receipt{}, ErrUnavailable
	}
	if !claimed {
		outcome, found, loadErr := o.journal.LoadResearchBoardPublication(ctx, scope, operationID)
		if loadErr != nil || !found || !outcomeMatchesDestination(outcome, destination, request.Digest) {
			return c.Receipt{}, ErrUnavailable
		}
		return receiptFromOutcome(outcome), nil
	}
	return o.sendAndCommit(ctx, request.Envelope, destination, lease)
}

func (o *Owner) Reconcile(ctx context.Context, request engine.CommitRequest) (engine.ReconcileResult, error) {
	if ctx == nil || o == nil || request.Target.Target != appresearch.BoardOwnerRoute || request.Document != nil || request.Report == "" || !validDigest(request.Digest) || digest([]byte(request.Report)) != request.Digest {
		return engine.ReconcileResult{}, ErrUnavailable
	}
	destination, ok := o.destinations[request.Target.DestinationRef]
	if !ok || !authorizedEnvelope(request.Envelope, destination, o.pin) || request.IdempotencyKey != appresearch.IdempotencyKey(firstRunID(request), request.Target.Target, request.Target.DestinationRef, request.Digest) {
		return engine.ReconcileResult{}, ErrUnavailable
	}
	if err := o.verifyPinnedConnection(ctx); err != nil {
		return engine.ReconcileResult{}, ErrUnavailable
	}
	scope := appresearch.BoardPublicationScope{OwnerID: o.pin.OwnerID, PrincipalID: o.pin.PrincipalID, JournalInstanceID: o.pin.JournalInstanceID}
	operationID := appresearch.BoardOperationID(scope, destination.Ref, destination.DataScope, destination.ChannelID, destination.ThreadID, destination.NewThread, destination.Title, request.Digest, destination.Recipients)
	if outcome, found, err := o.journal.LoadResearchBoardPublication(ctx, scope, operationID); err != nil {
		return engine.ReconcileResult{}, ErrUnavailable
	} else if found {
		if !outcomeMatchesDestination(outcome, destination, request.Digest) {
			return engine.ReconcileResult{}, ErrUnavailable
		}
		return engine.ReconcileResult{Receipt: receiptFromOutcome(outcome)}, nil
	}
	prepared, err := o.client.PrepareSend(boardcommunication.SendRequest{
		ChannelID: destination.ChannelID, ThreadID: destination.ThreadID, Title: destination.Title,
		Body: request.Report, Recipients: destination.Recipients, Kind: "", IdempotencyKey: operationID,
	})
	if err != nil {
		return engine.ReconcileResult{}, ErrUnavailable
	}
	preparedBytes := prepared.JSONBytes()
	payloadHash := prepared.SHA256()
	lease, claimed, err := o.journal.ClaimResearchBoardPublication(ctx, appresearch.BoardPublicationClaimInput{
		Scope: scope, OperationID: operationID, DestinationRef: destination.Ref, DataScope: destination.DataScope,
		ChannelID: destination.ChannelID, ThreadID: destination.ThreadID, NewThread: destination.NewThread,
		Title: destination.Title, Recipients: append([]string(nil), destination.Recipients...), ContentDigest: request.Digest,
		IdempotencyKey: operationID, PreparedBytes: preparedBytes, PayloadSHA256: hex.EncodeToString(payloadHash[:]),
		Now: o.now().UTC(), LeaseDuration: boardPublicationLeaseDuration,
	})
	if err != nil || !claimed {
		return engine.ReconcileResult{}, ErrUnavailable
	}
	if lease.AttemptCount == 0 {
		if _, err := o.journal.ReleaseResearchBoardPublication(ctx, lease, o.now().UTC()); err != nil {
			return engine.ReconcileResult{}, ErrUnavailable
		}
		return engine.ReconcileResult{RetrySafe: true}, nil
	}
	receipt, err := o.sendAndCommit(ctx, request.Envelope, destination, lease)
	if err != nil {
		return engine.ReconcileResult{}, err
	}
	return engine.ReconcileResult{Receipt: receipt}, nil
}

func (o *Owner) sendAndCommit(ctx context.Context, envelope c.Envelope, destination appresearch.BoardDestination, lease appresearch.BoardPublicationLease) (c.Receipt, error) {
	preparedHash, err := hex.DecodeString(lease.PayloadSHA256)
	if err != nil || len(preparedHash) != sha256.Size {
		return c.Receipt{}, ErrUnavailable
	}
	var expected [sha256.Size]byte
	copy(expected[:], preparedHash)
	prepared, err := boardcommunication.RestorePreparedSend(lease.PreparedBytes, expected)
	if err != nil || prepared.IdempotencyKey() != lease.IdempotencyKey {
		return c.Receipt{}, ErrUnavailable
	}
	request, err := prepared.Request()
	if err != nil || request.ChannelID != lease.ChannelID || request.ThreadID != lease.ThreadID || request.Title != lease.Title ||
		request.Kind != "" || !reflect.DeepEqual(request.Recipients, lease.Recipients) || digest([]byte(request.Body)) != lease.ContentDigest {
		return c.Receipt{}, ErrUnavailable
	}
	if err := o.verifyPinnedConnection(ctx); err != nil {
		return c.Receipt{}, ErrUnavailable
	}
	attempt, err := o.journal.RecordResearchBoardPublicationAttempt(ctx, lease, o.now().UTC())
	if err != nil || attempt.PayloadSHA256 != lease.PayloadSHA256 || !reflect.DeepEqual(attempt.PreparedBytes, lease.PreparedBytes) {
		return c.Receipt{}, ErrUnavailable
	}
	sendReceipt, sendErr := o.client.SendPrepared(ctx, prepared)
	if sendErr != nil {
		o.releaseBestEffort(ctx, lease)
		return c.Receipt{}, ErrUnavailable
	}
	canonical, err := o.readExactMessage(ctx, destination, sendReceipt)
	if err != nil {
		o.releaseBestEffort(ctx, lease)
		return c.Receipt{}, ErrUnavailable
	}
	outcome, err := o.journal.CompleteResearchBoardPublication(ctx, appresearch.BoardPublicationCompletionInput{
		Lease: lease, Now: o.now().UTC(), AttemptOrdinal: attempt.Ordinal, AttemptSHA256: attempt.PayloadSHA256,
		SendReceipt: appresearch.BoardPublicationSendReceipt{
			MessageID: sendReceipt.MessageID, ThreadID: sendReceipt.ThreadID, StoredAt: sendReceipt.StoredAt.UTC(), Status: sendReceipt.Status,
		},
		CanonicalMessage: canonical,
	})
	if err != nil || !outcomeMatchesDestination(outcome, destination, lease.ContentDigest) {
		return c.Receipt{}, ErrUnavailable
	}
	return receiptFromOutcome(outcome), nil
}

func (o *Owner) Verify(ctx context.Context, envelope c.Envelope, receipt c.Receipt) (c.Receipt, error) {
	if ctx == nil || o == nil || receipt.Owner != appresearch.BoardOwnerName || receipt.OwnerRoute != appresearch.BoardOwnerRoute || receipt.DestinationRef == "" || receipt.State != "committed" || receipt.Scope != envelope.DataScope {
		return c.Receipt{}, ErrUnavailable
	}
	destination, ok := o.destinations[receipt.DestinationRef]
	if !ok || !authorizedEnvelope(envelope, destination, o.pin) || !validDigest(receipt.Digest) {
		return c.Receipt{}, ErrUnavailable
	}
	if err := o.verifyPinnedConnection(ctx); err != nil {
		return c.Receipt{}, ErrUnavailable
	}
	scope := appresearch.BoardPublicationScope{OwnerID: o.pin.OwnerID, PrincipalID: o.pin.PrincipalID, JournalInstanceID: o.pin.JournalInstanceID}
	operationID := appresearch.BoardOperationID(scope, destination.Ref, destination.DataScope, destination.ChannelID, destination.ThreadID, destination.NewThread, destination.Title, receipt.Digest, destination.Recipients)
	outcome, found, err := o.journal.LoadResearchBoardPublication(ctx, scope, operationID)
	if err != nil || !found || !outcomeMatchesDestination(outcome, destination, receipt.Digest) || receipt.AuditRef != outcome.SendReceipt.MessageID || receipt.CanonicalID != outcome.SendReceipt.MessageID || receipt.DestinationRef != outcome.DestinationRef {
		return c.Receipt{}, ErrUnavailable
	}
	canonical, err := o.readExactMessage(ctx, destination, boardcommunication.SendReceipt{
		MessageID: outcome.SendReceipt.MessageID, ThreadID: outcome.SendReceipt.ThreadID,
		StoredAt: outcome.SendReceipt.StoredAt, Status: outcome.SendReceipt.Status,
	})
	if err != nil || !reflect.DeepEqual(canonical, outcome.Canonical) {
		return c.Receipt{}, ErrUnavailable
	}
	receipt.VerifiedAt = o.now().UTC()
	return receipt, nil
}

func (o *Owner) releaseBestEffort(ctx context.Context, lease appresearch.BoardPublicationLease) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_, _ = o.journal.ReleaseResearchBoardPublication(cleanupCtx, lease, o.now().UTC())
}

func (o *Owner) readExactMessage(ctx context.Context, destination appresearch.BoardDestination, receipt boardcommunication.SendReceipt) (appresearch.BoardPublicationCanonicalMessage, error) {
	if !validBoardID(receipt.ThreadID) || !validBoardID(receipt.MessageID) || receipt.Status != "stored" || receipt.StoredAt.IsZero() || (!destination.NewThread && receipt.ThreadID != destination.ThreadID) {
		return appresearch.BoardPublicationCanonicalMessage{}, ErrUnavailable
	}
	canonicalReceipt, err := o.client.MessageReceipt(ctx, receipt.MessageID)
	if err != nil || canonicalReceipt.MessageID != receipt.MessageID || canonicalReceipt.ThreadID != receipt.ThreadID ||
		canonicalReceipt.Status != receipt.Status || !canonicalReceipt.StoredAt.Equal(receipt.StoredAt) {
		return appresearch.BoardPublicationCanonicalMessage{}, ErrUnavailable
	}
	cursor := ""
	for pageNumber := 0; pageNumber < boardPublicationMaxPages; pageNumber++ {
		page, err := o.client.ReadThread(ctx, receipt.ThreadID, boardcommunication.PageRequest{Limit: boardPublicationPageSize, Cursor: cursor})
		if err != nil || page.Thread.ID != receipt.ThreadID || page.Thread.Space != destination.ChannelID || destination.NewThread && page.Thread.Title != destination.Title {
			return appresearch.BoardPublicationCanonicalMessage{}, ErrUnavailable
		}
		for _, message := range page.Thread.Messages {
			if message.ID != receipt.MessageID {
				continue
			}
			if _, parseErr := time.Parse(time.RFC3339Nano, message.At); parseErr != nil {
				return appresearch.BoardPublicationCanonicalMessage{}, ErrUnavailable
			}
			bodyHash := sha256.Sum256([]byte(message.Body))
			canonical := appresearch.BoardPublicationCanonicalMessage{
				MessageID: message.ID, ThreadID: message.ThreadID, ChannelID: message.ChannelID,
				SenderPrincipalID: message.By, Recipients: append([]string(nil), message.Recipients...), BodyDigest: hex.EncodeToString(bodyHash[:]),
			}
			if canonical.ThreadID != receipt.ThreadID || canonical.ChannelID != destination.ChannelID || canonical.SenderPrincipalID != o.pin.PrincipalID ||
				!reflect.DeepEqual(canonical.Recipients, destination.Recipients) || canonical.BodyDigest == "" {
				return appresearch.BoardPublicationCanonicalMessage{}, ErrUnavailable
			}
			return canonical, nil
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			break
		}
		cursor = page.NextCursor
	}
	return appresearch.BoardPublicationCanonicalMessage{}, ErrUnavailable
}

func (o *Owner) verifyPinnedConnection(ctx context.Context) error {
	identity, err := o.client.ReadConnection(ctx)
	if err != nil || identity.PrincipalID != o.pin.PrincipalID || identity.JournalInstanceID != o.pin.JournalInstanceID {
		return ErrUnavailable
	}
	return nil
}

func authorizedEnvelope(envelope c.Envelope, destination appresearch.BoardDestination, pin eventstore.BoardConnectionPin) bool {
	return destination.DataScope == "user" && envelope.DataScope == "user" &&
		envelope.OwnerID != "" && envelope.OwnerID == destination.OwnerID && destination.OwnerID == pin.OwnerID &&
		destination.PrincipalID == pin.PrincipalID && strings.TrimSpace(envelope.Actor) != ""
}

func outcomeMatchesDestination(outcome appresearch.BoardPublicationOutcome, destination appresearch.BoardDestination, digestValue string) bool {
	return outcome.DestinationRef == destination.Ref && outcome.Scope.OwnerID == destination.OwnerID &&
		outcome.Scope.PrincipalID == destination.PrincipalID && outcome.DataScope == destination.DataScope &&
		outcome.ContentDigest == digestValue && outcome.Canonical.BodyDigest == digestValue && outcome.Canonical.ChannelID == destination.ChannelID &&
		outcome.Canonical.SenderPrincipalID == destination.PrincipalID && reflect.DeepEqual(outcome.Canonical.Recipients, destination.Recipients) &&
		(!destination.NewThread && outcome.SendReceipt.ThreadID == destination.ThreadID || destination.NewThread && outcome.Canonical.ThreadID == outcome.SendReceipt.ThreadID)
}

func receiptFromOutcome(outcome appresearch.BoardPublicationOutcome) c.Receipt {
	return c.Receipt{
		DestinationRef: outcome.DestinationRef, Owner: appresearch.BoardOwnerName, OwnerRoute: appresearch.BoardOwnerRoute,
		AuditRef: outcome.SendReceipt.MessageID, CanonicalID: outcome.SendReceipt.MessageID, Digest: outcome.ContentDigest,
		State: "committed", Scope: outcome.DataScope, CommittedAt: outcome.SendReceipt.StoredAt,
	}
}

func firstRunID(request engine.CommitRequest) string {
	if request.ResearchRunID != "" {
		return request.ResearchRunID
	}
	return request.Envelope.RunID
}

func digest(value []byte) string {
	computed := sha256.Sum256(value)
	return hex.EncodeToString(computed[:])
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validBoardID(value string) bool {
	if len(value) == 0 || len(value) > 64 || strings.TrimSpace(value) != value {
		return false
	}
	for index, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || index > 0 && (char == '_' || char == '-')) {
			return false
		}
	}
	return true
}

func validLowerHex(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

var _ engine.OwnerPort = (*Owner)(nil)
var _ engine.ReconcilePort = (*Owner)(nil)
