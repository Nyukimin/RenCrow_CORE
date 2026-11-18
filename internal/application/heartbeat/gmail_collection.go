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

var gmailJapanTime = time.FixedZone("JST", int((9*time.Hour)/time.Second))

var errScheduledGmailDiaryBackfillBlocked = errors.New("scheduled Gmail diary backfill has blocked messages")

type gmailCollection struct {
	intake                             GmailIntake
	userID                             string
	interval, timeout                  time.Duration
	runOnStart                         bool
	now                                func() time.Time
	mu                                 sync.Mutex
	cancel                             context.CancelFunc
	wg                                 sync.WaitGroup
	lastSuccessfulDiaryBackfillJSTDate string
}

func (s *HeartbeatService) WithGmailIntake(intake GmailIntake, userID string, interval, timeout time.Duration, runOnStart bool) *HeartbeatService {
	if intake != nil && userID != "" && interval > 0 && timeout > 0 {
		s.gmail = &gmailCollection{intake: intake, userID: userID, interval: interval, timeout: timeout, runOnStart: runOnStart, now: time.Now}
	}
	return s
}

func (s *HeartbeatService) startGmailIntake() bool {
	return s.startGmailCollection(false)
}

// startGmailCollection keeps scheduled intake and owner backfill on the same
// single-flight slot. A supported diary intake gets a daily scan before normal
// polling; both scheduled runs share this collection context and slot.
func (s *HeartbeatService) startGmailCollection(ownerDiaryBackfill bool) bool {
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
	diaryBackfillDate := ""
	if !ownerDiaryBackfill {
		if _, supported := g.intake.(GmailDiaryBackfillIntake); supported {
			jstDate := g.now().In(gmailJapanTime).Format("2006-01-02")
			if jstDate != g.lastSuccessfulDiaryBackfillJSTDate {
				diaryBackfillDate = jstDate
			}
		}
	}
	diaryBackfill := ownerDiaryBackfill || diaryBackfillDate != ""
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	g.cancel = cancel
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		defer func() { cancel(); g.mu.Lock(); g.cancel = nil; g.mu.Unlock() }()
		if diaryBackfill {
			s.emitEvent("heartbeat.gmail.diary_backfill.started", "Gmail diary backfill started")
			trigger := "owner"
			if diaryBackfillDate != "" {
				trigger = "scheduled"
			}
			report, err := s.runGmailDiaryBackfill(ctx, trigger)
			s.emitGmailDiaryBackfillResult(report, err, diaryBackfillDate)
			if trigger == "owner" || errors.Is(err, ErrHeartbeatAdmissionDeferred) || ctx.Err() != nil {
				return
			}
		}
		s.emitEvent("heartbeat.gmail.started", "Gmail intake started")
		report, err := s.runGmailIntake(ctx)
		s.emitGmailIntakeResult(report, err)
	}()
	return true
}

func (s *HeartbeatService) emitGmailDiaryBackfillResult(report gmailapp.GmailRunReport, err error, scheduledDate string) {
	if errors.Is(err, ErrHeartbeatAdmissionDeferred) {
		s.emitEvent("heartbeat.gmail.diary_backfill.deferred", "Gmail diary backfill deferred: execution capacity unavailable; retry at the next interval")
		return
	}
	if errors.Is(err, errScheduledGmailDiaryBackfillBlocked) {
		s.emitEvent("heartbeat.gmail.diary_backfill.partial", fmt.Sprintf("processed=%d blocked=%d; retry at the next interval", report.Processed, report.Blocked))
		return
	}
	if err != nil {
		// Provider errors may contain URLs or response bodies. Keep shared SSE
		// and logs free of private mail; detailed per-mail outcomes are owner-only.
		s.emitEvent("heartbeat.gmail.diary_backfill.error", "Gmail diary backfill failed; check owner intake receipts")
		return
	}
	if scheduledDate != "" {
		s.gmail.mu.Lock()
		s.gmail.lastSuccessfulDiaryBackfillJSTDate = scheduledDate
		s.gmail.mu.Unlock()
	}
	s.emitEvent("heartbeat.gmail.diary_backfill.completed", fmt.Sprintf("processed=%d created=%d blocked=%d", report.Processed, report.Created, report.Blocked))
}

func (s *HeartbeatService) emitGmailIntakeResult(report gmailapp.GmailRunReport, err error) {
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
}

// StartGmailDiaryBackfill schedules the explicit owner backfill on the same
// cancellation, worker waitgroup, and collection slot as normal Gmail intake.
func (s *HeartbeatService) StartGmailDiaryBackfill() bool {
	return s.startGmailCollection(true)
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

func (s *HeartbeatService) runGmailDiaryBackfill(ctx context.Context, trigger string) (gmailapp.GmailRunReport, error) {
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
	workerCtx, _, finish, err := s.beginWorker(ctx, "Gmailのルミナ日記を全期間取り込む", "gmail-diary-backfill", trigger)
	if err != nil {
		return report, err
	}
	report, err = intake.RunDiaryBackfill(workerCtx)
	if err == nil && trigger == "scheduled" && report.Blocked > 0 {
		err = errScheduledGmailDiaryBackfillBlocked
	}
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
