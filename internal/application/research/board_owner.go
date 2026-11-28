package research

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

const BoardOwnerName = "core.board_communication"
const BoardOwnerRoute = "board"

var ErrBoardOwnerUnavailable = errors.New("research BOARD owner is unavailable")

// BoardDestination is an administrative registration. Research requests may
// name Ref only; every connection and BOARD target field is owner-controlled.
type BoardDestination struct {
	Ref         string
	OwnerID     string
	PrincipalID string
	ChannelID   string
	ThreadID    string
	NewThread   bool
	Recipients  []string
	DataScope   string
	Title       string
}

func (d BoardDestination) Validate() error {
	if !validBoardDestinationRef(d.Ref) || !canonicalBoardOwnerValue(d.OwnerID) || !canonicalBoardOwnerValue(d.PrincipalID) ||
		!canonicalBoardOwnerValue(d.ChannelID) || (d.DataScope != "public" && d.DataScope != "user") ||
		(d.ThreadID != "" && d.NewThread) || (d.ThreadID == "" && !d.NewThread) || d.Recipients == nil || len(d.Recipients) == 0 || len(d.Recipients) > 32 {
		return ErrBoardOwnerUnavailable
	}
	if d.NewThread {
		if !validBoardOwnerTitle(d.Title) {
			return ErrBoardOwnerUnavailable
		}
	} else if !canonicalBoardOwnerValue(d.ThreadID) || d.Title != "" {
		return ErrBoardOwnerUnavailable
	}
	previous := ""
	for _, recipient := range d.Recipients {
		if !canonicalBoardOwnerValue(recipient) || recipient == d.PrincipalID || recipient <= previous {
			return ErrBoardOwnerUnavailable
		}
		previous = recipient
	}
	return nil
}

func validBoardDestinationRef(value string) bool {
	if len(value) == 0 || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for index, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || index > 0 && (char == '_' || char == '-') {
			continue
		}
		return false
	}
	return true
}

func canonicalBoardOwnerValue(value string) bool {
	if len(value) == 0 || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for index, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || index > 0 && (char == '_' || char == '-') {
			continue
		}
		return false
	}
	return true
}

func validBoardOwnerTitle(value string) bool {
	return strings.TrimSpace(value) == value && value != "" && len([]rune(value)) <= 120 && !strings.ContainsAny(value, "\r\n\x00")
}

// BoardPublicationScope binds the durable operation to one CORE owner and
// the already-pinned BOARD principal and journal instance.
type BoardPublicationScope struct {
	OwnerID           string
	PrincipalID       string
	JournalInstanceID string
}

// BoardPublicationClaimInput freezes the exact destination and BOARD request
// before any network mutation. PreparedBytes are never logged or returned to
// the model.
type BoardPublicationClaimInput struct {
	Scope          BoardPublicationScope
	OperationID    string
	DestinationRef string
	DataScope      string
	ChannelID      string
	ThreadID       string
	NewThread      bool
	Title          string
	Recipients     []string
	ContentDigest  string
	IdempotencyKey string
	PreparedBytes  []byte
	PayloadSHA256  string
	Now            time.Time
	LeaseDuration  time.Duration
}

type BoardPublicationLease struct {
	Scope          BoardPublicationScope
	OperationID    string
	DestinationRef string
	DataScope      string
	ChannelID      string
	ThreadID       string
	NewThread      bool
	Title          string
	Recipients     []string
	ContentDigest  string
	IdempotencyKey string
	PreparedBytes  []byte
	PayloadSHA256  string
	LeaseToken     string
	LeaseExpiresAt time.Time
	Version        int64
	AttemptCount   int
}

type BoardPublicationAttempt struct {
	OperationID   string
	Ordinal       int
	PayloadSHA256 string
	PreparedBytes []byte
	StartedAt     time.Time
}

type BoardPublicationSendReceipt struct {
	MessageID string
	ThreadID  string
	StoredAt  time.Time
	Status    string
}

type BoardPublicationCanonicalMessage struct {
	MessageID         string
	ThreadID          string
	ChannelID         string
	SenderPrincipalID string
	Recipients        []string
	BodyDigest        string
}

type BoardPublicationCompletionInput struct {
	Lease            BoardPublicationLease
	Now              time.Time
	AttemptOrdinal   int
	AttemptSHA256    string
	SendReceipt      BoardPublicationSendReceipt
	CanonicalMessage BoardPublicationCanonicalMessage
}

type BoardPublicationOutcome struct {
	Scope          BoardPublicationScope
	OperationID    string
	DestinationRef string
	DataScope      string
	ContentDigest  string
	IdempotencyKey string
	AttemptOrdinal int
	PayloadSHA256  string
	SendReceipt    BoardPublicationSendReceipt
	Canonical      BoardPublicationCanonicalMessage
}

type BoardPublicationJournal interface {
	ClaimResearchBoardPublication(context.Context, BoardPublicationClaimInput) (BoardPublicationLease, bool, error)
	RecordResearchBoardPublicationAttempt(context.Context, BoardPublicationLease, time.Time) (BoardPublicationAttempt, error)
	ReleaseResearchBoardPublication(context.Context, BoardPublicationLease, time.Time) (bool, error)
	CompleteResearchBoardPublication(context.Context, BoardPublicationCompletionInput) (BoardPublicationOutcome, error)
	LoadResearchBoardPublication(context.Context, BoardPublicationScope, string) (BoardPublicationOutcome, bool, error)
}

// BoardOperationID is independent of RunID so equal report versions for the
// same registered destination deduplicate across Research invocations.
func BoardOperationID(scope BoardPublicationScope, destinationRef, dataScope, channelID, threadID string, newThread bool, title, contentDigest string, recipients []string) string {
	values := []string{scope.OwnerID, scope.PrincipalID, scope.JournalInstanceID, destinationRef, dataScope, channelID, threadID, title, contentDigest}
	if newThread {
		values = append(values, "new_thread")
	} else {
		values = append(values, "existing_thread")
	}
	values = append(values, recipients...)
	var builder strings.Builder
	for index, value := range values {
		if index > 0 {
			builder.WriteByte(0)
		}
		builder.WriteString(value)
	}
	digest := sha256.Sum256([]byte(builder.String()))
	return "research-board-" + hex.EncodeToString(digest[:])
}

// OwnerRouter sends each target to its existing typed owner. Engine-local
// artifacts never reach this boundary; the Engine owns those in its Store.
type OwnerRouter struct {
	knowledge engine.OwnerPort
	board     engine.OwnerPort
}

func NewOwnerRouter(knowledge, board engine.OwnerPort) *OwnerRouter {
	return &OwnerRouter{knowledge: knowledge, board: board}
}

func (r *OwnerRouter) Commit(ctx context.Context, request engine.CommitRequest) (c.Receipt, error) {
	owner, err := r.ownerForTarget(request.Target.Target)
	if err != nil {
		return c.Receipt{}, err
	}
	return owner.Commit(ctx, request)
}

func (r *OwnerRouter) Verify(ctx context.Context, envelope c.Envelope, receipt c.Receipt) (c.Receipt, error) {
	var owner engine.OwnerPort
	switch receipt.Owner {
	case "core.l1_staging":
		owner = r.knowledge
	case BoardOwnerName:
		owner = r.board
	default:
		return c.Receipt{}, ErrBoardOwnerUnavailable
	}
	if owner == nil {
		return c.Receipt{}, ErrBoardOwnerUnavailable
	}
	return owner.Verify(ctx, envelope, receipt)
}

func (r *OwnerRouter) Reconcile(ctx context.Context, request engine.CommitRequest) (engine.ReconcileResult, error) {
	owner, err := r.ownerForTarget(request.Target.Target)
	if err != nil {
		return engine.ReconcileResult{}, err
	}
	reconciler, ok := owner.(engine.ReconcilePort)
	if !ok {
		return engine.ReconcileResult{}, ErrBoardOwnerUnavailable
	}
	return reconciler.Reconcile(ctx, request)
}

func (r *OwnerRouter) ownerForTarget(target string) (engine.OwnerPort, error) {
	if r == nil {
		return nil, ErrBoardOwnerUnavailable
	}
	var owner engine.OwnerPort
	switch target {
	case KnowledgeStagingTarget:
		owner = r.knowledge
	case "board":
		owner = r.board
	default:
		return nil, ErrBoardOwnerUnavailable
	}
	if owner == nil {
		return nil, ErrBoardOwnerUnavailable
	}
	return owner, nil
}

var _ engine.OwnerPort = (*OwnerRouter)(nil)
var _ engine.ReconcilePort = (*OwnerRouter)(nil)
