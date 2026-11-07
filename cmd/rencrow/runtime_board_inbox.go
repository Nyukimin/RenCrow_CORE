package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
)

const (
	boardInboxPageSize          = 16
	boardInboxClaimLeaseSeconds = 75
)

var ErrBoardInboxRuntimeUnavailable = errors.New("BOARD inbox runtime unavailable")

type boardInboxScope struct {
	Pin               eventstore.BoardConnectionPin
	ResponderActorID  string
	AllowedChannelIDs []string
	AllowedPeerIDs    []string
	WakeTimeout       time.Duration
}

type boardInboxSource struct {
	Item            boardcommunication.InboxItem
	Message         boardcommunication.BoardMessage
	ThreadCreatedAt time.Time // canonical thread date for the peer session
	Binding         domainconversation.ExternalPeerBinding
}

// boardInboxClaim is private processing authority for the exact BOARD item.
type boardInboxClaim struct {
	ClaimID      string
	ClaimReceipt string
	LeaseUntil   time.Time
}

type boardInboxOwner interface {
	FindAcceptedBoardRequest(context.Context, boardInboxSource) (conversationruntime.ReadViewRequest, bool, error)
	AcceptBoardRequest(context.Context, boardInboxSource) (conversationruntime.ReadViewRequest, error)
	ProcessClaimedBoardRequest(context.Context, conversationruntime.ReadViewRequest, boardInboxSource, boardInboxClaim) (conversationruntime.TurnResult, error)
	// HasDurableBoardOutcome is true only after the BOARD publication receipt or
	// completion is durably recorded (B2), not when Mio's canonical intent alone
	// has committed (B1).
	HasDurableBoardOutcome(context.Context, conversationruntime.ReadViewRequest) (bool, error)
	// CorrelateTerminalBoardReply is true only when this exact terminal reply
	// receipt is durably stored for the correlated publication.
	CorrelateTerminalBoardReply(context.Context, boardInboxSource) (bool, error)
}

type boardInboxDisposition string

const (
	boardInboxPending      boardInboxDisposition = "pending"
	boardInboxAcknowledged boardInboxDisposition = "acknowledged"
	boardInboxCutover      boardInboxDisposition = "before_cutover"
)

var (
	ErrBoardInboxInvalidScope      = errors.New("BOARD inbox scope is invalid")
	ErrBoardInboxCanonicalMismatch = errors.New("BOARD inbox and canonical thread differ")
	ErrBoardInboxCursorStalled     = errors.New("BOARD inbox cursor did not advance")
)

// runBoardInboxWake performs durable catch-up, reads at most one authenticated
// SSE hint, then catches up again when the initial inbox was empty. Every
// catch-up starts from the durable inbox's first page; the SSE sequence never
// replaces an item ACK.
func runBoardInboxWake(ctx context.Context, client *boardcommunication.Client, owner boardInboxOwner, scope boardInboxScope, afterSequence int64) (int64, error) {
	if ctx == nil || client == nil || owner == nil || afterSequence < 0 || !validBoardInboxScope(scope) {
		return afterSequence, ErrBoardInboxInvalidScope
	}
	if err := ctx.Err(); err != nil {
		return afterSequence, err
	}
	identity, err := client.ReadConnection(ctx)
	if err != nil {
		return afterSequence, err
	}
	if identity.PrincipalID != scope.Pin.PrincipalID || identity.JournalInstanceID != scope.Pin.JournalInstanceID {
		return afterSequence, boardcommunication.ErrConnectionIdentityMismatch
	}

	seen := make(map[string]boardcommunication.InboxItem)
	initialHadItems, err := catchUpBoardInbox(ctx, client, owner, scope, seen)
	if err != nil {
		return afterSequence, err
	}
	nextSequence, err := readOneBoardInboxHint(ctx, client, scope.WakeTimeout, afterSequence)
	if err != nil {
		return nextSequence, err
	}
	if !initialHadItems {
		if _, err := catchUpBoardInbox(ctx, client, owner, scope, seen); err != nil {
			return nextSequence, err
		}
	}
	return nextSequence, nil
}

func validBoardInboxScope(scope boardInboxScope) bool {
	pin := scope.Pin
	if strings.TrimSpace(pin.OwnerID) == "" || strings.TrimSpace(pin.OwnerID) != pin.OwnerID ||
		pin.PrincipalID == "" || pin.JournalInstanceID == "" || pin.CutoverAt.IsZero() || pin.CutoverAt.Location() != time.UTC ||
		scope.WakeTimeout <= 0 || len(scope.AllowedChannelIDs) == 0 || len(scope.AllowedPeerIDs) == 0 {
		return false
	}
	if scope.ResponderActorID != "" &&
		(!domainconversation.IsExternalPeerResponderActor(scope.ResponderActorID) || scope.ResponderActorID != pin.PrincipalID) {
		return false
	}
	return validBoardInboxAllowlist(scope.AllowedChannelIDs) && validBoardInboxAllowlist(scope.AllowedPeerIDs)
}

func validBoardInboxAllowlist(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func readOneBoardInboxHint(ctx context.Context, client *boardcommunication.Client, timeout time.Duration, afterSequence int64) (int64, error) {
	wakeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stream, err := client.OpenEvents(wakeCtx, afterSequence)
	if err != nil {
		if ctx.Err() != nil {
			return afterSequence, ctx.Err()
		}
		if errors.Is(err, boardcommunication.ErrUnavailable) && errors.Is(wakeCtx.Err(), context.DeadlineExceeded) {
			return afterSequence, nil
		}
		return afterSequence, err
	}
	defer stream.Close()
	event, err := stream.Next()
	if ctx.Err() != nil {
		return afterSequence, ctx.Err()
	}
	if err != nil {
		if errors.Is(err, boardcommunication.ErrUnavailable) && errors.Is(wakeCtx.Err(), context.DeadlineExceeded) {
			return afterSequence, nil
		}
		return afterSequence, err
	}
	if event.Seq <= afterSequence {
		return afterSequence, boardcommunication.ErrProtocol
	}
	return event.Seq, nil
}

func catchUpBoardInbox(ctx context.Context, client *boardcommunication.Client, owner boardInboxOwner, scope boardInboxScope, seen map[string]boardcommunication.InboxItem) (bool, error) {
	sawItem, _, err := catchUpBoardInboxWithDue(ctx, client, owner, scope, seen)
	return sawItem, err
}

func catchUpBoardInboxWithDue(ctx context.Context, client *boardcommunication.Client, owner boardInboxOwner, scope boardInboxScope, seen map[string]boardcommunication.InboxItem) (bool, time.Time, error) {
	cursor := ""
	visitedCursors := make(map[string]struct{})
	sawItem := false
	var due time.Time
	for {
		if err := ctx.Err(); err != nil {
			return sawItem, due, err
		}
		page, err := client.Inbox(ctx, boardcommunication.PageRequest{Limit: boardInboxPageSize, Cursor: cursor})
		if err != nil {
			return sawItem, due, err
		}
		for _, item := range page.Items {
			sawItem = true
			if previous, exists := seen[item.ItemID]; exists {
				if !equalBoardInboxItems(previous, item) {
					return sawItem, due, ErrBoardInboxCanonicalMismatch
				}
				continue
			}
			seen[item.ItemID] = item
			itemDue, err := handleBoardInboxItemWithDue(ctx, client, owner, scope, item)
			due = earlierBoardInboxDue(due, itemDue)
			if err != nil {
				if errors.Is(err, boardcommunication.ErrClaimActive) {
					continue
				}
				return sawItem, due, err
			}
		}
		if page.NextCursor == "" {
			return sawItem, due, nil
		}
		if page.NextCursor == cursor {
			return sawItem, due, ErrBoardInboxCursorStalled
		}
		if _, exists := visitedCursors[page.NextCursor]; exists {
			return sawItem, due, ErrBoardInboxCursorStalled
		}
		visitedCursors[cursor] = struct{}{}
		cursor = page.NextCursor
	}
}

func handleBoardInboxItem(ctx context.Context, client *boardcommunication.Client, owner boardInboxOwner, scope boardInboxScope, item boardcommunication.InboxItem) error {
	_, err := handleBoardInboxItemWithDue(ctx, client, owner, scope, item)
	return err
}

func handleBoardInboxItemWithDue(ctx context.Context, client *boardcommunication.Client, owner boardInboxOwner, scope boardInboxScope, item boardcommunication.InboxItem) (time.Time, error) {
	if !item.CreatedAt.After(scope.Pin.CutoverAt) || item.SenderID == scope.Pin.PrincipalID ||
		!containsBoardInboxID(scope.AllowedChannelIDs, item.ChannelID) ||
		!containsBoardInboxID(scope.AllowedPeerIDs, item.SenderID) ||
		!containsBoardInboxID(item.Recipients, scope.Pin.PrincipalID) {
		return time.Time{}, nil
	}
	source, err := readCanonicalBoardInboxSource(ctx, client, scope.Pin, item)
	if err != nil {
		return time.Time{}, err
	}
	if scope.ResponderActorID != "" {
		if source.Binding.RecipientPrincipalID != scope.ResponderActorID {
			return time.Time{}, ErrBoardInboxCanonicalMismatch
		}
		source.Binding.ResponderActorID = scope.ResponderActorID
	}
	switch {
	case isBoardInboxRequest(source.Message):
		source.Binding.Classification = domainconversation.ExternalPeerClassificationRequest
		if err := source.Binding.ValidateNewRequest(); err != nil {
			return time.Time{}, nil
		}
		return processBoardInboxRequestWithDue(ctx, client, owner, scope, source)
	case isBoardInboxReplyCandidate(source.Message):
		source.Binding.Classification = domainconversation.ExternalPeerClassificationUnknownReply
		if err := source.Binding.Validate(); err != nil {
			return time.Time{}, nil
		}
		terminal, err := owner.CorrelateTerminalBoardReply(ctx, source)
		if err != nil || !terminal {
			return time.Time{}, err
		}
		source.Binding.Classification = domainconversation.ExternalPeerClassificationCorrelatedReplyTerminal
		return claimAndAckDurableTerminalWithDue(ctx, client, scope.Pin, source)
	default:
		return time.Time{}, nil
	}
}

func readCanonicalBoardInboxSource(ctx context.Context, client *boardcommunication.Client, pin eventstore.BoardConnectionPin, item boardcommunication.InboxItem) (boardInboxSource, error) {
	cursor := ""
	visitedCursors := make(map[string]struct{})
	seenMessages := make(map[string]struct{})
	var canonicalThreadCreatedAt time.Time
	for {
		page, err := client.ReadThread(ctx, item.ThreadID, boardcommunication.PageRequest{Limit: boardInboxPageSize, Cursor: cursor})
		if err != nil {
			return boardInboxSource{}, err
		}
		thread := page.Thread
		if thread.ID != item.ThreadID || thread.Space != item.ChannelID ||
			!containsBoardInboxID(thread.Participants, pin.PrincipalID) || !containsBoardInboxID(thread.Participants, item.SenderID) {
			return boardInboxSource{}, ErrBoardInboxCanonicalMismatch
		}
		threadCreatedAt, err := time.Parse(time.RFC3339Nano, thread.CreatedAt)
		if err != nil || threadCreatedAt.IsZero() ||
			(!canonicalThreadCreatedAt.IsZero() && !threadCreatedAt.Equal(canonicalThreadCreatedAt)) {
			return boardInboxSource{}, ErrBoardInboxCanonicalMismatch
		}
		canonicalThreadCreatedAt = threadCreatedAt
		for _, message := range thread.Messages {
			if _, exists := seenMessages[message.ID]; exists {
				return boardInboxSource{}, ErrBoardInboxCanonicalMismatch
			}
			seenMessages[message.ID] = struct{}{}
			if message.ID != item.MessageID {
				continue
			}
			if !canonicalBoardInboxMessageMatches(message, item) {
				return boardInboxSource{}, ErrBoardInboxCanonicalMismatch
			}
			binding := domainconversation.ExternalPeerBinding{
				BoardInstanceID: pin.JournalInstanceID, RecipientPrincipalID: pin.PrincipalID,
				ChannelID: item.ChannelID, ThreadID: item.ThreadID, MessageID: item.MessageID,
				SenderPrincipalID: item.SenderID, WireKind: message.Kind, ReplyTo: message.ReplyTo,
			}
			return boardInboxSource{
				Item: item, Message: message, ThreadCreatedAt: canonicalThreadCreatedAt, Binding: binding,
			}, nil
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			return boardInboxSource{}, ErrBoardInboxCanonicalMismatch
		}
		if _, exists := visitedCursors[page.NextCursor]; exists {
			return boardInboxSource{}, ErrBoardInboxCursorStalled
		}
		visitedCursors[cursor] = struct{}{}
		cursor = page.NextCursor
	}
}

func canonicalBoardInboxMessageMatches(message boardcommunication.BoardMessage, item boardcommunication.InboxItem) bool {
	messageTime, err := time.Parse(time.RFC3339Nano, message.At)
	return err == nil && !messageTime.IsZero() && message.ID == item.MessageID && message.By == item.SenderID && message.Body == item.Body &&
		message.ChannelID == item.ChannelID && message.ThreadID == item.ThreadID &&
		equalBoardInboxIDs(message.Mentions, item.Mentions) && equalBoardInboxIDs(message.Recipients, item.Recipients)
}

func isBoardInboxRequest(message boardcommunication.BoardMessage) bool {
	return (message.Kind == "" || message.Kind == "request") && message.ReplyTo == ""
}

func isBoardInboxReplyCandidate(message boardcommunication.BoardMessage) bool {
	return (message.Kind == "" || message.Kind == "reply") && message.ReplyTo != ""
}

func processBoardInboxRequest(ctx context.Context, client *boardcommunication.Client, owner boardInboxOwner, scope boardInboxScope, source boardInboxSource) error {
	_, err := processBoardInboxRequestWithDue(ctx, client, owner, scope, source)
	return err
}

func processBoardInboxRequestWithDue(ctx context.Context, client *boardcommunication.Client, owner boardInboxOwner, scope boardInboxScope, source boardInboxSource) (time.Time, error) {
	claimID := boardInboxClaimID(scope.Pin, source.Item)
	claim, err := client.ClaimProcessing(ctx, source.Item.ItemID, boardcommunication.ClaimRequest{
		Receipt: source.Item.AckReceipt, ClaimID: claimID, LeaseSeconds: boardInboxClaimLeaseSeconds,
	})
	if err != nil {
		if errors.Is(err, boardcommunication.ErrClaimActive) {
			return fallbackBoardInboxClaimDue(), err
		}
		return time.Time{}, err
	}
	if claim.MessageID != source.Item.MessageID {
		return time.Time{}, ErrBoardInboxCanonicalMismatch
	}
	switch claim.Status {
	case "already_processed":
		accepted, found, err := owner.FindAcceptedBoardRequest(ctx, source)
		if err != nil || !found {
			return time.Time{}, err
		}
		if err := validateAcceptedBoardRequest(accepted, scope.Pin, source.Binding); err != nil {
			return time.Time{}, err
		}
		outcome, err := owner.HasDurableBoardOutcome(ctx, accepted)
		if err != nil || !outcome {
			return time.Time{}, err
		}
		return time.Time{}, client.AckInbox(ctx, source.Item.ItemID, source.Item.AckReceipt)
	case "already_claimed":
		return boardInboxClaimLeaseDue(claim.LeaseUntil), nil
	case "claimed", "reacquired":
		if claim.ClaimID != claimID || !claim.CanProcess || claim.ClaimReceipt == "" || claim.LeaseUntil == nil || claim.LeaseUntil.IsZero() {
			return time.Time{}, boardcommunication.ErrUnknownOutcome
		}
		proof := boardInboxClaim{ClaimID: claim.ClaimID, ClaimReceipt: claim.ClaimReceipt, LeaseUntil: *claim.LeaseUntil}
		accepted, found, err := owner.FindAcceptedBoardRequest(ctx, source)
		if err != nil {
			return time.Time{}, err
		}
		if found {
			if err := validateAcceptedBoardRequest(accepted, scope.Pin, source.Binding); err != nil {
				return time.Time{}, err
			}
		} else {
			accepted, err = owner.AcceptBoardRequest(ctx, source)
			if err != nil {
				return time.Time{}, err
			}
			if err := validateAcceptedBoardRequest(accepted, scope.Pin, source.Binding); err != nil {
				return time.Time{}, err
			}
		}
		if _, err := owner.ProcessClaimedBoardRequest(ctx, accepted, source, proof); err != nil {
			return time.Time{}, err
		}
		outcome, err := owner.HasDurableBoardOutcome(ctx, accepted)
		if err != nil || !outcome {
			if err != nil {
				return time.Time{}, err
			}
			return boardInboxClaimLeaseDue(&proof.LeaseUntil), nil
		}
		return time.Time{}, client.AckInbox(ctx, source.Item.ItemID, source.Item.AckReceipt)
	default:
		return time.Time{}, fmt.Errorf("%w: unexpected claim status", boardcommunication.ErrProtocol)
	}
}

func claimAndAckDurableTerminal(ctx context.Context, client *boardcommunication.Client, pin eventstore.BoardConnectionPin, source boardInboxSource) error {
	_, err := claimAndAckDurableTerminalWithDue(ctx, client, pin, source)
	return err
}

func claimAndAckDurableTerminalWithDue(ctx context.Context, client *boardcommunication.Client, pin eventstore.BoardConnectionPin, source boardInboxSource) (time.Time, error) {
	claim, err := client.ClaimProcessing(ctx, source.Item.ItemID, boardcommunication.ClaimRequest{
		Receipt: source.Item.AckReceipt, ClaimID: boardInboxClaimID(pin, source.Item), LeaseSeconds: boardInboxClaimLeaseSeconds,
	})
	if err != nil {
		if errors.Is(err, boardcommunication.ErrClaimActive) {
			return fallbackBoardInboxClaimDue(), err
		}
		return time.Time{}, err
	}
	if claim.MessageID != source.Item.MessageID {
		return time.Time{}, ErrBoardInboxCanonicalMismatch
	}
	switch claim.Status {
	case "claimed", "reacquired", "already_processed":
		return time.Time{}, client.AckInbox(ctx, source.Item.ItemID, source.Item.AckReceipt)
	case "already_claimed":
		return boardInboxClaimLeaseDue(claim.LeaseUntil), nil
	default:
		return time.Time{}, fmt.Errorf("%w: unexpected claim status", boardcommunication.ErrProtocol)
	}
}

func boardInboxClaimLeaseDue(leaseUntil *time.Time) time.Time {
	if leaseUntil == nil || leaseUntil.IsZero() {
		return time.Time{}
	}
	due := leaseUntil.UTC()
	if !due.After(time.Now().UTC()) {
		return time.Now().UTC().Add(time.Second)
	}
	return due
}

func fallbackBoardInboxClaimDue() time.Time {
	return time.Now().UTC().Add(time.Duration(boardInboxClaimLeaseSeconds) * time.Second)
}

func earlierBoardInboxDue(current, candidate time.Time) time.Time {
	if candidate.IsZero() || (!current.IsZero() && !candidate.Before(current)) {
		return current
	}
	return candidate
}

func boardInboxClaimID(pin eventstore.BoardConnectionPin, item boardcommunication.InboxItem) string {
	preimage := "rencrow.board.processing-claim.v1\x00" + pin.JournalInstanceID + "\x00" + pin.PrincipalID + "\x00" + item.ItemID + "\x00" + item.MessageID
	digest := sha256.Sum256([]byte(preimage))
	return hex.EncodeToString(digest[:])
}

func validateAcceptedBoardRequest(accepted conversationruntime.ReadViewRequest, pin eventstore.BoardConnectionPin, binding domainconversation.ExternalPeerBinding) error {
	if accepted.Principal.OwnerID != pin.OwnerID || accepted.Session.OwnerID != pin.OwnerID {
		return ErrBoardInboxCanonicalMismatch
	}
	stored, err := domainconversation.ParseExternalPeerBinding(accepted.Accepted.InputEvent.Payload)
	if err != nil || !stored.Equal(binding) {
		return ErrBoardInboxCanonicalMismatch
	}
	actorID, err := binding.SourceActorID()
	if err != nil || string(accepted.Accepted.ActorID) != actorID || accepted.Accepted.TriggerKind != "external_peer_message" {
		return ErrBoardInboxCanonicalMismatch
	}
	return nil
}

func containsBoardInboxID(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func equalBoardInboxIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalBoardInboxItems(left, right boardcommunication.InboxItem) bool {
	return left.ItemID == right.ItemID && left.MessageID == right.MessageID && left.ChannelID == right.ChannelID &&
		left.ThreadID == right.ThreadID && left.SenderID == right.SenderID && left.CreatedAt.Equal(right.CreatedAt) &&
		left.Body == right.Body && equalBoardInboxIDs(left.Mentions, right.Mentions) &&
		equalBoardInboxIDs(left.Recipients, right.Recipients) && left.AckReceipt == right.AckReceipt
}
