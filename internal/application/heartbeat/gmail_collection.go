package heartbeat

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	gmailapp "github.com/Nyukimin/RenCrow_CORE/internal/application/gmailintake"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type GmailIntake interface {
	Run(context.Context) (gmailapp.GmailRunReport, error)
}

type GmailDiaryBackfillIntake interface {
	RunDiaryBackfill(context.Context) (gmailapp.GmailRunReport, error)
}

type gmailCollection struct {
	intake            GmailIntake
	userID            string
	interval, timeout time.Duration
	runOnStart        bool
	mu                sync.Mutex
	cancel            context.CancelFunc
	wg                sync.WaitGroup
}

func (s *HeartbeatService) WithGmailIntake(intake GmailIntake, userID string, interval, timeout time.Duration, runOnStart bool) *HeartbeatService {
	if intake != nil && userID != "" && interval > 0 && timeout > 0 {
		s.gmail = &gmailCollection{intake: intake, userID: userID, interval: interval, timeout: timeout, runOnStart: runOnStart}
	}
	return s
}

func (s *HeartbeatService) startGmailIntake() bool {
	g := s.gmail
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if s.stopRequested() {
		return false
	}
	if g.cancel != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	g.cancel = cancel
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		defer func() { cancel(); g.mu.Lock(); g.cancel = nil; g.mu.Unlock() }()
		s.emitEvent("heartbeat.gmail.started", "Gmail intake started")
		report, err := s.runGmailIntake(ctx)
		if errors.Is(err, ErrHeartbeatAdmissionDeferred) {
			s.emitEvent("heartbeat.gmail.deferred", "Gmail intake deferred: execution capacity unavailable; retry at the next interval")
			return
		}
		if err != nil {
			// Provider errors may contain URLs or response bodies. Keep shared SSE
			// and logs free of private mail; detailed per-mail outcomes are owner-only.
			s.emitEvent("heartbeat.gmail.error", "Gmail intake failed; check Google authentication and owner intake receipts")
			return
		}
		s.emitEvent("heartbeat.gmail.completed", fmt.Sprintf("processed=%d created=%d skipped=%d blocked=%d has_more=%t knowledge_items=%d", report.Processed, report.Created, report.Skipped, report.Blocked, report.HasMore, len(report.KnowledgeItemIDs)))
	}()
	return true
}

// StartGmailDiaryBackfill schedules the explicit owner backfill on the same
// cancellation, worker waitgroup, and collection slot as normal Gmail intake.
func (s *HeartbeatService) StartGmailDiaryBackfill() bool {
	g := s.gmail
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if s.stopRequested() {
		return false
	}
	if g.cancel != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	g.cancel = cancel
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		defer func() { cancel(); g.mu.Lock(); g.cancel = nil; g.mu.Unlock() }()
		s.emitEvent("heartbeat.gmail.diary_backfill.started", "Gmail diary backfill started")
		report, err := s.runGmailDiaryBackfill(ctx)
		if err != nil {
			s.emitEvent("heartbeat.gmail.diary_backfill.error", "Gmail diary backfill failed; check owner intake receipts")
			return
		}
		s.emitEvent("heartbeat.gmail.diary_backfill.completed", fmt.Sprintf("processed=%d created=%d blocked=%d", report.Processed, report.Created, report.Blocked))
	}()
	return true
}

func (s *HeartbeatService) runGmailIntake(ctx context.Context) (gmailapp.GmailRunReport, error) {
	var report gmailapp.GmailRunReport
	if s.gmail == nil {
		return report, errors.New("Gmail intake unavailable")
	}
	release, err := s.acquireCollectionWorker(ctx)
	if err != nil {
		return report, err
	}
	defer release()
	scope, err := domaintool.NewToolExecutionScope(string(modulecore.NewRequestID()), domaintool.ActorKindAgent, s.workerActor, s.gmail.userID, []string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		return report, err
	}
	ctx = domaintool.WithToolExecutionScope(ctx, scope)
	workerCtx, _, finish, err := s.beginWorker(ctx, "Gmailの設定済み対象を検証しAtlas候補と知識へ取り込む", "gmail-intake", "scheduled")
	if err != nil {
		return report, err
	}
	report, err = s.gmail.intake.Run(workerCtx)
	return report, errors.Join(err, finish(err))
}

func (s *HeartbeatService) runGmailDiaryBackfill(ctx context.Context) (gmailapp.GmailRunReport, error) {
	var report gmailapp.GmailRunReport
	if s.gmail == nil {
		return report, errors.New("Gmail intake unavailable")
	}
	intake, ok := s.gmail.intake.(GmailDiaryBackfillIntake)
	if !ok {
		return report, errors.New("Gmail diary backfill unavailable")
	}
	release, err := s.acquireCollectionWorker(ctx)
	if err != nil {
		return report, err
	}
	defer release()
	scope, err := domaintool.NewToolExecutionScope(string(modulecore.NewRequestID()), domaintool.ActorKindAgent, s.workerActor, s.gmail.userID, []string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		return report, err
	}
	ctx = domaintool.WithToolExecutionScope(ctx, scope)
	workerCtx, _, finish, err := s.beginWorker(ctx, "Gmailのルミナ日記を全期間取り込む", "gmail-diary-backfill", "owner")
	if err != nil {
		return report, err
	}
	report, err = intake.RunDiaryBackfill(workerCtx)
	return report, errors.Join(err, finish(err))
}

func (s *HeartbeatService) cancelGmailIntake() {
	if s == nil || s.gmail == nil {
		return
	}
	s.gmail.mu.Lock()
	cancel := s.gmail.cancel
	s.gmail.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *HeartbeatService) waitForGmailIntake() {
	if s == nil || s.gmail == nil {
		return
	}
	s.gmail.wg.Wait()
}

func (s *HeartbeatService) stopGmailIntake() {
	if s.gmail == nil {
		return
	}
	s.cancelGmailIntake()
	s.waitForGmailIntake()
}
