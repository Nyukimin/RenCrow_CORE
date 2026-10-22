package conversationruntime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var ErrReportDeliveryUnavailable = errors.New("Task report delivery is unavailable")

// ReportDeliveryTaskOwner exposes only the Task-owner operations needed to
// validate and acknowledge a durable report.
type ReportDeliveryTaskOwner interface {
	ListPendingReports(context.Context) ([]domaintask.DelegationReport, error)
	VerifyRunCompletion(context.Context, modulecore.TaskID, modulecore.RunID, string, domaintask.Status) error
	MarkReportDelivered(context.Context, modulecore.ResponseID) error
}

// ReportAcceptanceInput carries immutable Task-owned report data and fresh
// envelope metadata issued by the conversation runtime for this attempt.
type ReportAcceptanceInput struct {
	Report        conversation.ReportEnvelope
	TrustedSender string
	EventID       modulecore.EventID
	MessageID     modulecore.MessageID
	TurnID        modulecore.TurnID
	TraceID       modulecore.TraceID
	OccurredAt    time.Time
}

// ReportDeliveryJournal is the EventStore application port. Its implementation
// returns the first committed accepted turn when a report ID is replayed.
type ReportDeliveryJournal interface {
	AcceptReport(context.Context, ReportAcceptanceInput) (AcceptedTurn, error)
}

type ReportDeliveryDependencies struct {
	Tasks   ReportDeliveryTaskOwner
	Journal ReportDeliveryJournal
	Now     func() time.Time
	// BindAcceptedTurnContext carries request-local diagnostic context from
	// the canonical EventStore receipt into the subsequent turn processing.
	BindAcceptedTurnContext func(context.Context, AcceptedTurn) context.Context
}

type ReportDeliveryService struct {
	tasks                   ReportDeliveryTaskOwner
	journal                 ReportDeliveryJournal
	now                     func() time.Time
	bindAcceptedTurnContext func(context.Context, AcceptedTurn) context.Context
}

type ReportDeliveryResult struct {
	Delivered    bool
	ReportID     modulecore.ResponseID
	AcceptedTurn AcceptedTurn
	// ProcessContext retains the context bound to the canonical receipt.
	ProcessContext context.Context
}

func NewReportDeliveryService(dependencies ReportDeliveryDependencies) (*ReportDeliveryService, error) {
	if dependencies.Tasks == nil || dependencies.Journal == nil {
		return nil, errors.New("report delivery Task owner and journal are required")
	}
	now := dependencies.Now
	if now == nil {
		now = time.Now
	}
	return &ReportDeliveryService{
		tasks: dependencies.Tasks, journal: dependencies.Journal, now: now,
		bindAcceptedTurnContext: dependencies.BindAcceptedTurnContext,
	}, nil
}

// DeliverOne submits at most one durable Task-owned report to EventStore.
// EventStore acceptance and the Task delivered mark are separate owner
// commits; a failed mark intentionally leaves the same saved report pending
// so the EventStore receipt can return its original accepted Turn on retry.
func (s *ReportDeliveryService) DeliverOne(ctx context.Context) (ReportDeliveryResult, error) {
	if ctx == nil {
		return ReportDeliveryResult{}, errors.New("report delivery context is required")
	}
	if err := ctx.Err(); err != nil {
		return ReportDeliveryResult{}, err
	}
	if s == nil || s.tasks == nil || s.journal == nil || s.now == nil {
		return ReportDeliveryResult{}, ErrReportDeliveryUnavailable
	}

	reports, err := s.tasks.ListPendingReports(ctx)
	if err != nil {
		return ReportDeliveryResult{}, fmt.Errorf("list pending Task reports: %w", err)
	}
	if len(reports) == 0 {
		return ReportDeliveryResult{}, nil
	}

	report := reports[0]
	if report.State != domaintask.DelegationReportPending {
		return ReportDeliveryResult{}, fmt.Errorf("Task report %s is not pending", report.ReportID)
	}
	if err := report.Validate(); err != nil {
		return ReportDeliveryResult{}, fmt.Errorf("validate stored Task report %s: %w", report.ReportID, err)
	}
	if err := ctx.Err(); err != nil {
		return ReportDeliveryResult{}, err
	}
	if err := s.tasks.VerifyRunCompletion(ctx, report.TaskID, report.RunID, report.TrustedSender, report.TaskStatus); err != nil {
		return ReportDeliveryResult{}, fmt.Errorf("verify Task report %s completion: %w", report.ReportID, err)
	}
	if err := ctx.Err(); err != nil {
		return ReportDeliveryResult{}, err
	}

	occurredAt := s.now().UTC()
	if occurredAt.IsZero() {
		return ReportDeliveryResult{}, fmt.Errorf("Task report %s delivery timestamp is required", report.ReportID)
	}
	envelope := report.Envelope
	// Sender is not part of the report body. Reconstruct it only from the
	// Task owner's authenticated completion metadata.
	envelope.Sender = report.TrustedSender
	accepted, err := s.journal.AcceptReport(ctx, ReportAcceptanceInput{
		Report: envelope, TrustedSender: report.TrustedSender,
		EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(),
		TurnID: modulecore.NewTurnID(), TraceID: modulecore.NewTraceID(), OccurredAt: occurredAt,
	})
	if err != nil {
		return ReportDeliveryResult{}, fmt.Errorf("accept Task report %s in EventStore: %w", report.ReportID, err)
	}
	processCtx := ctx
	if s.bindAcceptedTurnContext != nil {
		if bound := s.bindAcceptedTurnContext(ctx, accepted); bound != nil {
			processCtx = bound
		}
	}
	if err := ctx.Err(); err != nil {
		return ReportDeliveryResult{}, err
	}
	if err := s.tasks.MarkReportDelivered(ctx, report.ReportID); err != nil {
		return ReportDeliveryResult{}, fmt.Errorf("mark Task report %s delivered after EventStore acceptance: %w", report.ReportID, err)
	}
	return ReportDeliveryResult{
		Delivered: true, ReportID: report.ReportID, AcceptedTurn: accepted, ProcessContext: processCtx,
	}, nil
}
