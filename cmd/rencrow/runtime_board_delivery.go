package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var ErrBoardPublicationDeliveryUnavailable = errors.New("BOARD publication delivery runtime unavailable")

type boardPublicationRetryScheduledError struct {
	cause error
}

func (err *boardPublicationRetryScheduledError) Error() string {
	if err == nil || err.cause == nil {
		return "BOARD publication retry scheduled"
	}
	return err.cause.Error()
}

func (err *boardPublicationRetryScheduledError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

// boardPublicationDeliveryJournal is the only persistence surface used by the
// BOARD send worker. The SQLite EventStore implements it with typed B2 methods.
type boardPublicationDeliveryJournal interface {
	ClaimBoardPublication(context.Context, eventstore.BoardPublicationClaimInput) (eventstore.BoardPublicationLease, bool, error)
	RecordBoardPublicationAttempt(context.Context, eventstore.BoardPublicationAttemptInput) (eventstore.BoardPublicationAttempt, error)
	RecordBoardPublicationClaimFence(context.Context, eventstore.BoardPublicationClaimFenceInput) (eventstore.BoardPublicationClaimFence, error)
	RetryBoardPublication(context.Context, eventstore.BoardPublicationRetryInput) error
	CompleteBoardPublication(context.Context, eventstore.BoardPublicationCompletionInput) (eventstore.BoardPublicationOutcome, error)
}

// boardPublicationClaimHint is valid only while it is paired with the exact
// accepted inbox source from C2a.
type boardPublicationClaimHint struct {
	Source                     boardInboxSource
	Claim                      boardInboxClaim
	CanonicalResponseMessageID modulecore.MessageID
}

type boardPublicationDeliveryInput struct {
	Scope         eventstore.BoardPublicationScope
	Now           time.Time
	LeaseDuration time.Duration
	ClaimHint     *boardPublicationClaimHint
}

// boardPublicationDeliveryRoute keeps a new-thread destination separate from
// a peer reply. C2b initially handles the peer-reply route.
type boardPublicationDeliveryRoute struct {
	Kind      boardPublicationRouteKind
	ChannelID string
	ThreadID  string
	Title     string
	ReplyTo   string
}

type boardPublicationRouteKind string

const (
	boardPublicationPeerReplyRoute boardPublicationRouteKind = "peer_reply"
	boardPublicationNewThreadRoute boardPublicationRouteKind = "new_thread"
)

// runBoardPublicationDelivery sends at most one durable BOARD publication.
// All delivery state belongs to the EventStore outbox and its immutable B2
// receipt; C2a performs inbox ACK only after it reads that durable outcome.
func runBoardPublicationDelivery(ctx context.Context, client *boardcommunication.Client, journal boardPublicationDeliveryJournal, input boardPublicationDeliveryInput) (bool, error) {
	if ctx == nil || client == nil || journal == nil || !validBoardPublicationDeliveryInput(input) {
		return false, ErrBoardPublicationDeliveryUnavailable
	}
	if input.ClaimHint != nil {
		if input.ClaimHint.CanonicalResponseMessageID == "" || input.ClaimHint.CanonicalResponseMessageID.Validate() != nil {
			return false, ErrBoardPublicationDeliveryUnavailable
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	identity, err := client.ReadConnection(ctx)
	if err != nil {
		return false, fmt.Errorf("read pinned BOARD delivery identity: %w", err)
	}
	if identity.PrincipalID != input.Scope.PrincipalID || identity.JournalInstanceID != input.Scope.JournalInstanceID {
		return false, boardcommunication.ErrConnectionIdentityMismatch
	}
	lease, available, err := journal.ClaimBoardPublication(ctx, eventstore.BoardPublicationClaimInput{
		Scope: input.Scope, CanonicalResponseMessageID: boardPublicationClaimResponseID(input.ClaimHint),
		Now: normalizedBoardDeliveryNow(input.Now), LeaseDuration: input.LeaseDuration,
	})
	if err != nil || !available {
		return false, err
	}
	now := normalizedBoardDeliveryNow(input.Now)
	if err := validateBoardPublicationDeliveryLease(input.Scope, lease, now); err != nil {
		return false, scheduleBoardPublicationRetry(ctx, journal, lease, now, err)
	}
	contexts, err := client.ListContexts(ctx)
	if err != nil {
		return false, scheduleBoardPublicationRetry(ctx, journal, lease, now, err)
	}
	if !containsBoardDeliveryContext(contexts, lease.PublicationIntent.Target.ChannelID) {
		return false, scheduleBoardPublicationRetry(ctx, journal, lease, now, boardcommunication.ErrProtocol)
	}
	route, err := boardPublicationRouteForLease(lease)
	if err != nil {
		return false, scheduleBoardPublicationRetry(ctx, journal, lease, now, err)
	}
	var source boardInboxSource
	var claim boardInboxClaim
	if route.Kind == boardPublicationPeerReplyRoute {
		if input.ClaimHint != nil {
			source, claim, err = validateBoardPublicationClaimHint(lease, input.Scope, *input.ClaimHint, now)
			if err == nil {
				source, err = rereadBoardPublicationSource(ctx, client, input.Scope, lease, source)
			}
		} else {
			source, err = findBoardPublicationSource(ctx, client, input.Scope, lease)
		}
	} else if input.ClaimHint != nil {
		err = ErrBoardInboxCanonicalMismatch
	}
	if err != nil {
		return false, scheduleBoardPublicationRetry(ctx, journal, lease, now, err)
	}
	if len(lease.Attempts) == 0 {
		if route.Kind == boardPublicationPeerReplyRoute && input.ClaimHint == nil {
			claim, err = acquireFreshBoardPublicationClaim(ctx, client, input.Scope, source, now, "")
			if err != nil {
				return false, scheduleBoardPublicationRetry(ctx, journal, lease, now, err)
			}
		}
		attempt, prepared, err := prepareAndRecordBoardPublication(ctx, client, journal, lease, route, claim, now, 1)
		if err != nil {
			return false, scheduleBoardPublicationRetry(ctx, journal, lease, now, err)
		}
		return sendAndCompleteBoardPublication(ctx, client, journal, lease, route, source, attempt, prepared, now)
	}
	prior, prepared, err := restoreBoardPublicationAttempt(client, lease, lease.Attempts[len(lease.Attempts)-1])
	if err != nil {
		return false, scheduleBoardPublicationRetry(ctx, journal, lease, now, err)
	}
	recorded, err := journal.RecordBoardPublicationAttempt(ctx, eventstore.BoardPublicationAttemptInput{
		Lease: lease, Now: now, LogicalKey: prior.LogicalKey, PreparedBytes: prior.PreparedBytes,
		PayloadSHA256: prior.PayloadSHA256, ProcessingClaimLeaseUntil: prior.ProcessingClaimLeaseUntil,
	})
	if err != nil || !sameBoardPublicationAttempt(recorded, prior) {
		if err == nil {
			err = eventstore.ErrBoardPublicationAttemptConflict
		}
		return false, scheduleBoardPublicationRetry(ctx, journal, lease, now, err)
	}
	return sendAndCompleteBoardPublication(ctx, client, journal, lease, route, source, prior, prepared, now)
}

func boardPublicationClaimResponseID(hint *boardPublicationClaimHint) modulecore.MessageID {
	if hint == nil {
		return ""
	}
	return hint.CanonicalResponseMessageID
}

func validBoardPublicationDeliveryInput(input boardPublicationDeliveryInput) bool {
	return strings.TrimSpace(input.Scope.OwnerID) != "" && input.Scope.OwnerID == strings.TrimSpace(input.Scope.OwnerID) &&
		input.Scope.JournalInstanceID != "" && input.Scope.PrincipalID != "" && input.LeaseDuration > 0
}

func normalizedBoardDeliveryNow(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func validateBoardPublicationDeliveryLease(scope eventstore.BoardPublicationScope, lease eventstore.BoardPublicationLease, now time.Time) error {
	target := lease.PublicationIntent.Target
	if lease.Scope != scope || lease.WorkID == "" || lease.LogicalKey == "" || lease.LeaseToken == "" || lease.Version < 1 ||
		!lease.LeaseExpiresAt.After(now) || lease.PublicationIntent.StableLogicalKey != lease.LogicalKey ||
		lease.CanonicalResponse.MessageID == "" || lease.CanonicalResponse.MessageID != lease.PublicationIntent.CanonicalResponseMessageID ||
		lease.CanonicalResponse.EventType != "agent.response" || lease.CanonicalResponse.Payload["visibility"] != "public" ||
		boardPublicationResponseText(lease.CanonicalResponse) == "" || target.BoardInstanceID != scope.JournalInstanceID ||
		target.CorePrincipalID != scope.PrincipalID || target.PeerPrincipalID == "" || target.ChannelID == "" {
		return ErrBoardInboxCanonicalMismatch
	}
	return nil
}

func containsBoardDeliveryContext(contexts []boardcommunication.BoardContext, channelID string) bool {
	for _, item := range contexts {
		if item.ID == channelID {
			return true
		}
	}
	return false
}

func boardPublicationRouteForLease(lease eventstore.BoardPublicationLease) (boardPublicationDeliveryRoute, error) {
	target := lease.PublicationIntent.Target
	targetKind := lease.TargetKind
	if targetKind == "" {
		targetKind = "reply"
	}
	switch targetKind {
	case "reply":
		if target.ThreadID == "" || target.ReplyTo == "" || lease.Title != "" {
			return boardPublicationDeliveryRoute{}, ErrBoardInboxCanonicalMismatch
		}
		return boardPublicationDeliveryRoute{
			Kind: boardPublicationPeerReplyRoute, ChannelID: target.ChannelID, ThreadID: target.ThreadID, ReplyTo: target.ReplyTo,
		}, nil
	case "new_root":
		if target.ThreadID != "" || target.ReplyTo != "" || strings.TrimSpace(lease.Title) == "" {
			return boardPublicationDeliveryRoute{}, ErrBoardInboxCanonicalMismatch
		}
		return boardPublicationDeliveryRoute{
			Kind: boardPublicationNewThreadRoute, ChannelID: target.ChannelID, Title: lease.Title,
		}, nil
	default:
		return boardPublicationDeliveryRoute{}, ErrBoardInboxCanonicalMismatch
	}
}

func validateBoardPublicationClaimHint(lease eventstore.BoardPublicationLease, scope eventstore.BoardPublicationScope, hint boardPublicationClaimHint, now time.Time) (boardInboxSource, boardInboxClaim, error) {
	target := lease.PublicationIntent.Target
	if hint.CanonicalResponseMessageID == "" || hint.CanonicalResponseMessageID != lease.PublicationIntent.CanonicalResponseMessageID ||
		hint.Claim.ClaimID == "" || hint.Claim.ClaimID != boardInboxClaimID(eventstore.BoardConnectionPin{
		OwnerID: scope.OwnerID, JournalInstanceID: scope.JournalInstanceID, PrincipalID: scope.PrincipalID,
	}, hint.Source.Item) || hint.Claim.ClaimReceipt == "" || !hint.Claim.LeaseUntil.After(now) ||
		hint.Source.Item.MessageID != target.ReplyTo || hint.Source.Item.ChannelID != target.ChannelID ||
		hint.Source.Item.ThreadID != target.ThreadID || hint.Source.Item.SenderID != target.PeerPrincipalID ||
		hint.Source.Message.ID != hint.Source.Item.MessageID || hint.Source.Binding.BoardInstanceID != target.BoardInstanceID ||
		hint.Source.Binding.RecipientPrincipalID != target.CorePrincipalID || hint.Source.Binding.ChannelID != target.ChannelID ||
		hint.Source.Binding.ThreadID != target.ThreadID || hint.Source.Binding.MessageID != target.ReplyTo ||
		hint.Source.Binding.SenderPrincipalID != target.PeerPrincipalID || hint.Source.Binding.Classification != "request" {
		return boardInboxSource{}, boardInboxClaim{}, ErrBoardInboxCanonicalMismatch
	}
	return hint.Source, hint.Claim, nil
}

func findBoardPublicationSource(ctx context.Context, client *boardcommunication.Client, scope eventstore.BoardPublicationScope, lease eventstore.BoardPublicationLease) (boardInboxSource, error) {
	var cursor string
	visited := make(map[string]struct{})
	var matched *boardcommunication.InboxItem
	for {
		page, err := client.Inbox(ctx, boardcommunication.PageRequest{Limit: boardInboxPageSize, Cursor: cursor})
		if err != nil {
			return boardInboxSource{}, err
		}
		for index := range page.Items {
			item := page.Items[index]
			if item.MessageID != lease.PublicationIntent.Target.ReplyTo {
				continue
			}
			if matched != nil || item.ChannelID != lease.PublicationIntent.Target.ChannelID ||
				item.ThreadID != lease.PublicationIntent.Target.ThreadID || item.SenderID != lease.PublicationIntent.Target.PeerPrincipalID ||
				!containsBoardInboxID(item.Recipients, scope.PrincipalID) {
				return boardInboxSource{}, ErrBoardInboxCanonicalMismatch
			}
			copy := item
			matched = &copy
		}
		if matched != nil || page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			return boardInboxSource{}, ErrBoardInboxCursorStalled
		}
		if _, exists := visited[page.NextCursor]; exists {
			return boardInboxSource{}, ErrBoardInboxCursorStalled
		}
		visited[cursor] = struct{}{}
		cursor = page.NextCursor
	}
	if matched == nil {
		return boardInboxSource{}, ErrBoardInboxCanonicalMismatch
	}
	return readCanonicalBoardPublicationSource(ctx, client, scope, *matched, lease)
}

func rereadBoardPublicationSource(ctx context.Context, client *boardcommunication.Client, scope eventstore.BoardPublicationScope, lease eventstore.BoardPublicationLease, previous boardInboxSource) (boardInboxSource, error) {
	if previous.Item.MessageID != lease.PublicationIntent.Target.ReplyTo || previous.Item.ItemID == "" {
		return boardInboxSource{}, ErrBoardInboxCanonicalMismatch
	}
	return readCanonicalBoardPublicationSource(ctx, client, scope, previous.Item, lease)
}

func readCanonicalBoardPublicationSource(ctx context.Context, client *boardcommunication.Client, scope eventstore.BoardPublicationScope, item boardcommunication.InboxItem, lease eventstore.BoardPublicationLease) (boardInboxSource, error) {
	if item.MessageID != lease.PublicationIntent.Target.ReplyTo || item.ChannelID != lease.PublicationIntent.Target.ChannelID ||
		item.ThreadID != lease.PublicationIntent.Target.ThreadID || item.SenderID != lease.PublicationIntent.Target.PeerPrincipalID ||
		!containsBoardInboxID(item.Recipients, scope.PrincipalID) {
		return boardInboxSource{}, ErrBoardInboxCanonicalMismatch
	}
	pin := eventstore.BoardConnectionPin{OwnerID: scope.OwnerID, JournalInstanceID: scope.JournalInstanceID, PrincipalID: scope.PrincipalID}
	source, err := readCanonicalBoardInboxSource(ctx, client, pin, item)
	if err != nil {
		return boardInboxSource{}, err
	}
	if !isBoardInboxRequest(source.Message) ||
		source.Binding.BoardInstanceID != lease.PublicationIntent.Target.BoardInstanceID ||
		source.Binding.RecipientPrincipalID != lease.PublicationIntent.Target.CorePrincipalID ||
		source.Binding.SenderPrincipalID != lease.PublicationIntent.Target.PeerPrincipalID ||
		source.Binding.MessageID != lease.PublicationIntent.Target.ReplyTo ||
		source.Binding.ChannelID != lease.PublicationIntent.Target.ChannelID || source.Binding.ThreadID != lease.PublicationIntent.Target.ThreadID {
		return boardInboxSource{}, ErrBoardInboxCanonicalMismatch
	}
	source.Binding.Classification = domainconversation.ExternalPeerClassificationRequest
	if err := source.Binding.ValidateNewRequest(); err != nil {
		return boardInboxSource{}, ErrBoardInboxCanonicalMismatch
	}
	return source, nil
}

func acquireFreshBoardPublicationClaim(ctx context.Context, client *boardcommunication.Client, scope eventstore.BoardPublicationScope, source boardInboxSource, now time.Time, priorReceiptSHA256 string) (boardInboxClaim, error) {
	freshInboxReceipt, err := client.InboxReceipt(ctx, source.Item.ItemID)
	if err != nil {
		return boardInboxClaim{}, err
	}
	if freshInboxReceipt.ItemID != source.Item.ItemID || freshInboxReceipt.MessageID != source.Item.MessageID || freshInboxReceipt.AckReceipt == "" {
		return boardInboxClaim{}, ErrBoardInboxCanonicalMismatch
	}
	claimID := boardInboxClaimID(eventstore.BoardConnectionPin{
		OwnerID: scope.OwnerID, JournalInstanceID: scope.JournalInstanceID, PrincipalID: scope.PrincipalID,
	}, source.Item)
	claim, err := client.ClaimProcessing(ctx, source.Item.ItemID, boardcommunication.ClaimRequest{
		Receipt: freshInboxReceipt.AckReceipt, ClaimID: claimID, LeaseSeconds: boardInboxClaimLeaseSeconds,
	})
	if err != nil {
		return boardInboxClaim{}, err
	}
	if claim.MessageID != source.Item.MessageID || claim.ClaimID != claimID ||
		(claim.Status != "claimed" && claim.Status != "reacquired") || !claim.CanProcess || claim.ClaimReceipt == "" ||
		claim.LeaseUntil == nil || !claim.LeaseUntil.After(now) {
		return boardInboxClaim{}, ErrBoardInboxCanonicalMismatch
	}
	if priorReceiptSHA256 != "" && boardPublicationReceiptDigest(claim.ClaimReceipt) == priorReceiptSHA256 {
		return boardInboxClaim{}, eventstore.ErrBoardPublicationAttemptConflict
	}
	return boardInboxClaim{ClaimID: claim.ClaimID, ClaimReceipt: claim.ClaimReceipt, LeaseUntil: *claim.LeaseUntil}, nil
}

func prepareAndRecordBoardPublication(ctx context.Context, client *boardcommunication.Client, journal boardPublicationDeliveryJournal, lease eventstore.BoardPublicationLease, route boardPublicationDeliveryRoute, claim boardInboxClaim, now time.Time, ordinal int) (eventstore.BoardPublicationAttempt, boardcommunication.PreparedSend, error) {
	mentions, err := boardPublicationResponseMentions(lease.CanonicalResponse)
	if err != nil {
		return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, err
	}
	request := boardcommunication.SendRequest{
		ChannelID: route.ChannelID, Body: boardPublicationResponseText(lease.CanonicalResponse),
		Recipients: []string{lease.PublicationIntent.Target.PeerPrincipalID}, Mentions: mentions,
		IdempotencyKey: lease.LogicalKey,
	}
	switch route.Kind {
	case boardPublicationPeerReplyRoute:
		if !claim.LeaseUntil.After(now) || claim.ClaimID == "" || claim.ClaimReceipt == "" {
			return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, ErrBoardPublicationDeliveryUnavailable
		}
		request.ThreadID, request.ReplyTo, request.Kind = route.ThreadID, route.ReplyTo, "reply"
		request.ProcessingClaimID, request.ProcessingClaimReceipt = claim.ClaimID, claim.ClaimReceipt
	case boardPublicationNewThreadRoute:
		if claim.ClaimID != "" || claim.ClaimReceipt != "" || !claim.LeaseUntil.IsZero() || route.ThreadID != "" || route.ReplyTo != "" || strings.TrimSpace(route.Title) == "" {
			return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, ErrBoardInboxCanonicalMismatch
		}
		if len(mentions) > 0 && (len(mentions) != 1 || mentions[0] != lease.PublicationIntent.Target.PeerPrincipalID) {
			return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, ErrBoardInboxCanonicalMismatch
		}
		request.Title, request.Kind = route.Title, "request"
	default:
		return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, ErrBoardInboxCanonicalMismatch
	}
	prepared, err := client.PrepareSend(request)
	if err != nil {
		return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, err
	}
	bytes := prepared.JSONBytes()
	digest := prepared.SHA256()
	input := eventstore.BoardPublicationAttemptInput{
		Lease: lease, Now: now, LogicalKey: lease.LogicalKey, PreparedBytes: bytes,
		PayloadSHA256: hex.EncodeToString(digest[:]), ProcessingClaimLeaseUntil: claim.LeaseUntil,
	}
	attempt, err := journal.RecordBoardPublicationAttempt(ctx, input)
	if err != nil {
		return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, err
	}
	if attempt.WorkID != lease.WorkID || attempt.Ordinal != ordinal || attempt.LogicalKey != lease.LogicalKey ||
		attempt.PayloadSHA256 != input.PayloadSHA256 || !bytesEqualBoardPublication(attempt.PreparedBytes, bytes) {
		return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, eventstore.ErrBoardPublicationAttemptConflict
	}
	if route.Kind == boardPublicationPeerReplyRoute && (attempt.ProcessingClaimID != claim.ClaimID ||
		attempt.ProcessingClaimReceiptSHA256 != boardPublicationReceiptDigest(claim.ClaimReceipt) ||
		!attempt.ProcessingClaimLeaseUntil.Equal(claim.LeaseUntil)) {
		return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, eventstore.ErrBoardPublicationAttemptConflict
	}
	if route.Kind == boardPublicationNewThreadRoute && (attempt.ProcessingClaimID != "" ||
		attempt.ProcessingClaimReceiptSHA256 != boardPublicationReceiptDigest("") || !attempt.ProcessingClaimLeaseUntil.IsZero()) {
		return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, eventstore.ErrBoardPublicationAttemptConflict
	}
	return attempt, prepared, nil
}

func sendAndCompleteBoardPublication(ctx context.Context, client *boardcommunication.Client, journal boardPublicationDeliveryJournal, lease eventstore.BoardPublicationLease, route boardPublicationDeliveryRoute, source boardInboxSource, attempt eventstore.BoardPublicationAttempt, prepared boardcommunication.PreparedSend, now time.Time) (bool, error) {
	attempt, prepared, err := sendBoardPublicationAttempt(ctx, client, journal, lease, route, attempt, prepared, now)
	if err == nil {
		return true, nil
	}
	if route.Kind != boardPublicationPeerReplyRoute || !errors.Is(err, boardcommunication.ErrClaimFence) {
		return false, scheduleBoardPublicationRetry(ctx, journal, lease, now, err)
	}
	freshSource, err := rereadBoardPublicationSource(ctx, client, lease.Scope, lease, source)
	if err != nil {
		return false, scheduleBoardPublicationRetry(ctx, journal, lease, now, err)
	}
	fresh, err := acquireFreshBoardPublicationClaim(ctx, client, lease.Scope, freshSource, now, attempt.ProcessingClaimReceiptSHA256)
	if err != nil {
		return false, scheduleBoardPublicationRetry(ctx, journal, lease, now, err)
	}
	proof := eventstore.BoardPublicationClaimFenceProof{
		Scope: lease.Scope, PriorClaimID: attempt.ProcessingClaimID, FreshClaimID: fresh.ClaimID,
		FreshReceiptSHA256: boardPublicationReceiptDigest(fresh.ClaimReceipt), FreshLeaseUntil: fresh.LeaseUntil,
		Disposition: eventstore.BoardPublicationClaimFenceProvenUncommitted,
	}
	if _, err := journal.RecordBoardPublicationClaimFence(ctx, eventstore.BoardPublicationClaimFenceInput{
		Lease: lease, Now: now, AttemptOrdinal: attempt.Ordinal, AttemptSHA256: attempt.PayloadSHA256, Proof: proof,
	}); err != nil {
		return false, scheduleBoardPublicationRetry(ctx, journal, lease, now, err)
	}
	next, nextPrepared, err := prepareAndRecordBoardPublication(ctx, client, journal, lease, route, fresh, now, attempt.Ordinal+1)
	if err != nil {
		return false, scheduleBoardPublicationRetry(ctx, journal, lease, now, err)
	}
	if _, _, err := sendBoardPublicationAttempt(ctx, client, journal, lease, route, next, nextPrepared, now); err != nil {
		return false, scheduleBoardPublicationRetry(ctx, journal, lease, now, err)
	}
	return true, nil
}

func sendBoardPublicationAttempt(ctx context.Context, client *boardcommunication.Client, journal boardPublicationDeliveryJournal, lease eventstore.BoardPublicationLease, route boardPublicationDeliveryRoute, attempt eventstore.BoardPublicationAttempt, prepared boardcommunication.PreparedSend, now time.Time) (eventstore.BoardPublicationAttempt, boardcommunication.PreparedSend, error) {
	if !lease.LeaseExpiresAt.After(now) || attempt.LogicalKey != lease.LogicalKey || attempt.WorkID != lease.WorkID ||
		attempt.PayloadSHA256 == "" || prepared.IdempotencyKey() != lease.LogicalKey {
		return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, eventstore.ErrWorkConflict
	}
	receipt, err := client.SendPrepared(ctx, prepared)
	if err != nil {
		return attempt, prepared, err
	}
	if receipt.MessageID == "" || receipt.ThreadID == "" || receipt.Status != "stored" || receipt.StoredAt.IsZero() ||
		(route.ThreadID != "" && receipt.ThreadID != route.ThreadID) {
		return attempt, prepared, boardcommunication.ErrUnknownOutcome
	}
	canonical, err := readBoardPublicationCanonicalResult(ctx, client, lease, route, receipt, prepared)
	if err != nil {
		return attempt, prepared, err
	}
	if _, err := journal.CompleteBoardPublication(ctx, eventstore.BoardPublicationCompletionInput{
		Lease: lease, Now: now, AttemptOrdinal: attempt.Ordinal, AttemptSHA256: attempt.PayloadSHA256,
		SendReceipt: eventstore.BoardPublicationSendReceipt{
			MessageID: receipt.MessageID, ThreadID: receipt.ThreadID, StoredAt: receipt.StoredAt, Status: receipt.Status,
		}, CanonicalMessage: canonical,
	}); err != nil {
		return attempt, prepared, err
	}
	return attempt, prepared, nil
}

func readBoardPublicationCanonicalResult(ctx context.Context, client *boardcommunication.Client, lease eventstore.BoardPublicationLease, route boardPublicationDeliveryRoute, receipt boardcommunication.SendReceipt, prepared boardcommunication.PreparedSend) (eventstore.BoardPublicationCanonicalMessage, error) {
	target := lease.PublicationIntent.Target
	var found *boardcommunication.BoardMessage
	cursor := ""
	seenCursors := make(map[string]struct{})
	for {
		page, err := client.ReadThread(ctx, receipt.ThreadID, boardcommunication.PageRequest{Limit: boardInboxPageSize, Cursor: cursor})
		if err != nil {
			return eventstore.BoardPublicationCanonicalMessage{}, err
		}
		if page.Thread.ID != receipt.ThreadID || page.Thread.Space != target.ChannelID ||
			(route.Kind == boardPublicationNewThreadRoute && page.Thread.Title != route.Title) ||
			(route.ThreadID != "" && page.Thread.ID != route.ThreadID) ||
			!containsBoardInboxID(page.Thread.Participants, target.CorePrincipalID) ||
			!containsBoardInboxID(page.Thread.Participants, target.PeerPrincipalID) {
			return eventstore.BoardPublicationCanonicalMessage{}, ErrBoardInboxCanonicalMismatch
		}
		for index := range page.Thread.Messages {
			if page.Thread.Messages[index].ID != receipt.MessageID {
				continue
			}
			if found != nil {
				return eventstore.BoardPublicationCanonicalMessage{}, ErrBoardInboxCanonicalMismatch
			}
			copy := page.Thread.Messages[index]
			found = &copy
		}
		if found != nil || page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			return eventstore.BoardPublicationCanonicalMessage{}, ErrBoardInboxCursorStalled
		}
		if _, exists := seenCursors[page.NextCursor]; exists {
			return eventstore.BoardPublicationCanonicalMessage{}, ErrBoardInboxCursorStalled
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
	mentions := preparedBoardPublicationMentions(prepared)
	wantKind, wantCausationID, wantHopCount := "reply", route.ReplyTo, 1
	if route.Kind == boardPublicationNewThreadRoute {
		wantKind, wantCausationID, wantHopCount = "request", receipt.MessageID, 0
	}
	if found == nil || found.ID != receipt.MessageID || found.By != target.CorePrincipalID ||
		found.Body != boardPublicationResponseText(lease.CanonicalResponse) || !equalBoardInboxIDs(found.Mentions, mentions) ||
		!equalBoardInboxIDs(found.Recipients, []string{target.PeerPrincipalID}) || found.ChannelID != target.ChannelID ||
		found.ThreadID != receipt.ThreadID || found.ReplyTo != route.ReplyTo || found.Kind != wantKind ||
		found.CausationID != wantCausationID || found.HopCount != wantHopCount {
		return eventstore.BoardPublicationCanonicalMessage{}, ErrBoardInboxCanonicalMismatch
	}
	return eventstore.BoardPublicationCanonicalMessage{
		MessageID: found.ID, ThreadID: found.ThreadID, ChannelID: found.ChannelID, SenderPrincipalID: found.By,
		Recipients: append([]string(nil), found.Recipients...), ReplyTo: found.ReplyTo,
		CausationID: found.CausationID, HopCount: found.HopCount,
	}, nil
}

func restoreBoardPublicationAttempt(client *boardcommunication.Client, lease eventstore.BoardPublicationLease, attempt eventstore.BoardPublicationAttempt) (eventstore.BoardPublicationAttempt, boardcommunication.PreparedSend, error) {
	if attempt.WorkID != lease.WorkID || attempt.LogicalKey != lease.LogicalKey || attempt.Ordinal < 1 || attempt.PayloadSHA256 == "" {
		return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, eventstore.ErrBoardPublicationAttemptConflict
	}
	digestBytes, err := hex.DecodeString(attempt.PayloadSHA256)
	if err != nil || len(digestBytes) != sha256.Size {
		return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, eventstore.ErrBoardPublicationAttemptConflict
	}
	var digest [sha256.Size]byte
	copy(digest[:], digestBytes)
	prepared, err := boardcommunication.RestorePreparedSend(attempt.PreparedBytes, digest)
	if err != nil || prepared.IdempotencyKey() != lease.LogicalKey {
		return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, eventstore.ErrBoardPublicationAttemptConflict
	}
	request, err := prepared.Request()
	if err != nil {
		return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, eventstore.ErrBoardPublicationAttemptConflict
	}
	if request.ProcessingClaimID != attempt.ProcessingClaimID || boardPublicationReceiptDigest(request.ProcessingClaimReceipt) != attempt.ProcessingClaimReceiptSHA256 {
		return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, eventstore.ErrBoardPublicationAttemptConflict
	}
	route, err := boardPublicationRouteForLease(lease)
	if err != nil {
		return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, eventstore.ErrBoardPublicationAttemptConflict
	}
	if route.Kind == boardPublicationNewThreadRoute {
		mentions, err := boardPublicationResponseMentions(lease.CanonicalResponse)
		if err != nil {
			return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, eventstore.ErrBoardPublicationAttemptConflict
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(attempt.PreparedBytes, &fields); err != nil {
			return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, eventstore.ErrBoardPublicationAttemptConflict
		}
		if prepared.Path() != "/api/v1/threads" ||
			request.ChannelID != route.ChannelID || request.Title != route.Title || request.Body != boardPublicationResponseText(lease.CanonicalResponse) ||
			request.Kind != "request" || request.ThreadID != "" || request.ReplyTo != "" || request.ProcessingClaimID != "" ||
			request.ProcessingClaimReceipt != "" || !reflect.DeepEqual(request.Recipients, []string{lease.PublicationIntent.Target.PeerPrincipalID}) ||
			!equalBoardInboxIDs(request.Mentions, mentions) || fieldsContainAny(fields, "thread_id", "reply_to", "processing_claim_id", "processing_claim_receipt") {
			return eventstore.BoardPublicationAttempt{}, boardcommunication.PreparedSend{}, eventstore.ErrBoardPublicationAttemptConflict
		}
	}
	return attempt, prepared, nil
}

func fieldsContainAny(fields map[string]json.RawMessage, names ...string) bool {
	for _, name := range names {
		if _, exists := fields[name]; exists {
			return true
		}
	}
	return false
}

func boardPublicationResponseText(event modulecore.EventEnvelope) string {
	text, _ := event.Payload["text"].(string)
	content, _ := event.Payload["content"].(string)
	if text == "" || content != text {
		return ""
	}
	return text
}

func boardPublicationResponseMentions(event modulecore.EventEnvelope) ([]string, error) {
	value, exists := event.Payload["mentions"]
	if !exists || value == nil {
		return []string{}, nil
	}
	switch mentions := value.(type) {
	case []string:
		return append([]string(nil), mentions...), nil
	case []any:
		result := make([]string, len(mentions))
		for index, mention := range mentions {
			value, ok := mention.(string)
			if !ok || value == "" {
				return nil, ErrBoardInboxCanonicalMismatch
			}
			result[index] = value
		}
		return result, nil
	default:
		return nil, ErrBoardInboxCanonicalMismatch
	}
}

func preparedBoardPublicationMentions(prepared boardcommunication.PreparedSend) []string {
	request, err := prepared.Request()
	if err != nil {
		return nil
	}
	return request.Mentions
}

func boardPublicationReceiptDigest(receipt string) string {
	digest := sha256.Sum256([]byte(receipt))
	return hex.EncodeToString(digest[:])
}

func sameBoardPublicationAttempt(left, right eventstore.BoardPublicationAttempt) bool {
	return left.WorkID == right.WorkID && left.Ordinal == right.Ordinal && left.LogicalKey == right.LogicalKey &&
		left.PayloadSHA256 == right.PayloadSHA256 && left.ProcessingClaimID == right.ProcessingClaimID &&
		left.ProcessingClaimReceiptSHA256 == right.ProcessingClaimReceiptSHA256 &&
		left.ProcessingClaimLeaseUntil.Equal(right.ProcessingClaimLeaseUntil) && bytesEqualBoardPublication(left.PreparedBytes, right.PreparedBytes)
}

func bytesEqualBoardPublication(left, right []byte) bool { return reflect.DeepEqual(left, right) }

func scheduleBoardPublicationRetry(ctx context.Context, journal boardPublicationDeliveryJournal, lease eventstore.BoardPublicationLease, now time.Time, cause error) error {
	if err := journal.RetryBoardPublication(ctx, eventstore.BoardPublicationRetryInput{Lease: lease, Now: now}); err != nil {
		return fmt.Errorf("%v; schedule typed BOARD publication retry: %w", cause, err)
	}
	return &boardPublicationRetryScheduledError{cause: cause}
}
