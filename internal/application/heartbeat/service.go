package heartbeat

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	revenueapp "github.com/Nyukimin/RenCrow_CORE/internal/application/revenue"
	skillbootstrap "github.com/Nyukimin/RenCrow_CORE/internal/application/skillgovernance"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	ctxbuilder "github.com/Nyukimin/RenCrow_CORE/internal/domain/context"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

// WorkerAgent はHeartbeatが作業処理を委譲するインターフェース。
type WorkerAgent interface {
	Execute(ctx context.Context, t task.Task) (string, error)
}

// NotificationSender はユーザーへの通知を送信するインターフェース
type NotificationSender interface {
	SendNotification(ctx context.Context, message string) error
}

type WorkstreamHeartbeatStore interface {
	SaveWorkstream(ctx context.Context, item domainworkstream.Workstream) error
	SaveGoal(ctx context.Context, item domainworkstream.Goal) error
	SaveArtifact(ctx context.Context, item domainworkstream.Artifact) error
	ListHeartbeatSchedules(ctx context.Context, limit int) ([]domainworkstream.HeartbeatSchedule, error)
	SaveHeartbeatSchedule(ctx context.Context, item domainworkstream.HeartbeatSchedule) error
	ListSteeringItems(ctx context.Context, limit int) ([]domainworkstream.SteeringItem, error)
	SaveSteeringItem(ctx context.Context, item domainworkstream.SteeringItem) error
	SaveVaultUpdateLog(ctx context.Context, item domainworkstream.VaultUpdateLog) error
}

type BacklogStore interface {
	List(ctx context.Context, limit int) ([]domainbacklog.Item, error)
	Save(ctx context.Context, item domainbacklog.Item) error
}

type RevenueDailyRoutineStore = revenueapp.DailyRoutineStore

type IdleChatSequenceMonitor interface {
	CheckIdleChatSequence(ctx context.Context, now time.Time) IdleChatSequenceCheck
}

type IdleChatSequenceCheck struct {
	Status     string
	Active     bool
	Recovered  bool
	Stage      string
	Detail     string
	SessionID  string
	Generation uint64
	AgeSeconds int64
	Action     string
	Error      string
	CheckedAt  time.Time
}

// HeartbeatService はHEARTBEAT.mdを定期的に読み込み、エージェントに処理させるサービス
type HeartbeatService struct {
	workerAgent       WorkerAgent
	sender            NotificationSender
	workspaceDir      string
	contextBuilder    *ctxbuilder.Builder
	listener          orchestrator.EventListener
	workstreamStore   WorkstreamHeartbeatStore
	backlogStore      BacklogStore
	revenueStore      RevenueDailyRoutineStore
	revenueRoutine    *revenueapp.DailyRoutineService
	economicDiscovery *EconomicObjectiveDiscoveryService
	skills            *skillbootstrap.BootstrapService
	idleChatMonitor   IdleChatSequenceMonitor
	interval          time.Duration
	idleChatInterval  time.Duration
	stopCh            chan struct{}
	done              chan struct{}
	mu                sync.Mutex
	running           bool
}

// NewHeartbeatService は新しいHeartbeatServiceを作成
func NewHeartbeatService(
	workerAgent WorkerAgent,
	sender NotificationSender,
	workspaceDir string,
	intervalMinutes int,
) *HeartbeatService {
	if intervalMinutes < 5 {
		intervalMinutes = 5
	}
	return &HeartbeatService{
		workerAgent:      workerAgent,
		sender:           sender,
		workspaceDir:     workspaceDir,
		contextBuilder:   ctxbuilder.NewBuilder(workspaceDir),
		interval:         time.Duration(intervalMinutes) * time.Minute,
		idleChatInterval: time.Minute,
		stopCh:           make(chan struct{}),
		done:             make(chan struct{}),
	}
}

func newHeartbeatWorkerTask(jobID task.JobID, message, channel, chatID string) task.Task {
	return task.NewTask(jobID, message, channel, chatID).
		WithRoute(routing.RouteOPS).
		WithForcedRoute(routing.RouteOPS)
}

// WithMemoryStore はメモリストアを設定する（オプション）
func (s *HeartbeatService) WithMemoryStore(store memory.Store) *HeartbeatService {
	s.contextBuilder.WithMemoryStore(store)
	return s
}

// WithEventListener sends Heartbeat results to external monitors such as Viewer SSE.
func (s *HeartbeatService) WithEventListener(listener orchestrator.EventListener) *HeartbeatService {
	s.listener = listener
	return s
}

// WithWorkstreamStore enables draft-only Workstream heartbeat execution.
func (s *HeartbeatService) WithWorkstreamStore(store WorkstreamHeartbeatStore) *HeartbeatService {
	s.workstreamStore = store
	return s
}

func (s *HeartbeatService) WithBacklogStore(store BacklogStore) *HeartbeatService {
	s.backlogStore = store
	return s
}

// WithRevenueDailyRoutineStore enables draft-only Revenue daily routine recording for revenue Workstream heartbeats.
func (s *HeartbeatService) WithRevenueDailyRoutineStore(store RevenueDailyRoutineStore) *HeartbeatService {
	s.revenueStore = store
	s.revenueRoutine = revenueapp.NewDailyRoutineService(store)
	return s
}

// WithEconomicObjectiveDiscovery enables safe draft-only Opportunity discovery.
func (s *HeartbeatService) WithEconomicObjectiveDiscovery(store EconomicObjectiveDiscoveryStore, goalStore EconomicObjectiveGoalStore, options EconomicObjectiveDiscoveryOptions) *HeartbeatService {
	s.economicDiscovery = NewEconomicObjectiveDiscoveryService(store, goalStore, options)
	return s
}

func (s *HeartbeatService) RunEconomicOpportunityDiscovery(ctx context.Context, now time.Time) (EconomicObjectiveDiscoveryReport, error) {
	if s == nil || s.economicDiscovery == nil {
		return EconomicObjectiveDiscoveryReport{Status: "skipped", Reason: "economic discovery not configured", ExternalActionsApplied: false}, nil
	}
	return s.economicDiscovery.Run(ctx, now)
}

func (s *HeartbeatService) WithSkillBootstrap(service *skillbootstrap.BootstrapService) *HeartbeatService {
	s.skills = service
	return s
}

func (s *HeartbeatService) WithIdleChatSequenceMonitor(monitor IdleChatSequenceMonitor) *HeartbeatService {
	s.idleChatMonitor = monitor
	return s
}

// Start はHeartbeatサービスをバックグラウンドで開始
func (s *HeartbeatService) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	go s.loop()
	log.Printf("HeartbeatService started (interval: %v, workspace: %s)", s.interval, s.workspaceDir)
}

// Stop はHeartbeatサービスを停止
func (s *HeartbeatService) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.mu.Unlock()

	close(s.stopCh)
	<-s.done
	log.Println("HeartbeatService stopped")
}

// loop はHeartbeatの定期実行ループ
func (s *HeartbeatService) loop() {
	defer close(s.done)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	idleChatTicker := time.NewTicker(s.idleChatInterval)
	defer idleChatTicker.Stop()
	s.runScheduledBacklogIntake(context.Background(), time.Now().UTC())

	for {
		select {
		case <-s.stopCh:
			return
		case <-idleChatTicker.C:
			s.runIdleChatSequenceCheck(context.Background(), time.Now().UTC())
		case <-ticker.C:
			ctx := context.Background()
			if err := s.tick(ctx); err != nil {
				log.Printf("[Heartbeat] tick error: %v", err)
			}
			if _, err := s.RunDueWorkstreamHeartbeats(ctx, time.Now().UTC()); err != nil {
				log.Printf("[Heartbeat] workstream tick error: %v", err)
			}
			s.runScheduledBacklogIntake(ctx, time.Now().UTC())
		}
	}
}

func (s *HeartbeatService) runScheduledBacklogIntake(ctx context.Context, now time.Time) {
	report, err := s.RunBacklogIntake(ctx, now)
	if err != nil {
		log.Printf("[Heartbeat] backlog intake error: %v", err)
		return
	}
	if report.Promoted > 0 || report.Failed > 0 {
		log.Printf("[Heartbeat] backlog intake: checked=%d promoted=%d skipped=%d failed=%d item=%s workstream=%s",
			report.Checked, report.Promoted, report.Skipped, report.Failed, report.ItemID, report.WorkstreamID)
	}
	runnerReport, err := s.RunBacklogRunner(ctx, now)
	if err != nil {
		log.Printf("[Heartbeat] backlog runner error: %v", err)
		return
	}
	if runnerReport.Started > 0 || runnerReport.Failed > 0 {
		log.Printf("[Heartbeat] backlog runner: checked=%d started=%d skipped=%d failed=%d item=%s",
			runnerReport.Checked, runnerReport.Started, runnerReport.Skipped, runnerReport.Failed, runnerReport.ItemID)
	}
}

func (s *HeartbeatService) runIdleChatSequenceCheck(ctx context.Context, now time.Time) IdleChatSequenceCheck {
	if s.idleChatMonitor == nil {
		return IdleChatSequenceCheck{Status: "disabled", CheckedAt: now.UTC()}
	}
	report := s.idleChatMonitor.CheckIdleChatSequence(ctx, now.UTC())
	if report.CheckedAt.IsZero() {
		report.CheckedAt = now.UTC()
	}
	status := strings.TrimSpace(report.Status)
	if status == "" {
		status = "unknown"
		report.Status = status
	}
	if report.Error != "" {
		log.Printf("[Heartbeat] idlechat sequence check error: %s", report.Error)
		s.emitEvent("heartbeat.idlechat_sequence.error", report.Error)
		return report
	}
	log.Printf("[Heartbeat] idlechat sequence check: status=%s active=%t recovered=%t stage=%s detail=%s session=%s age=%ds generation=%d action=%s",
		report.Status, report.Active, report.Recovered, report.Stage, report.Detail, report.SessionID, report.AgeSeconds, report.Generation, report.Action)
	s.emitEvent("heartbeat.idlechat_sequence."+status, fmt.Sprintf("active=%t recovered=%t stage=%s detail=%s session=%s age=%ds action=%s",
		report.Active, report.Recovered, report.Stage, report.Detail, report.SessionID, report.AgeSeconds, report.Action))
	return report
}

// tick は1回のHeartbeat処理を実行
func (s *HeartbeatService) tick(ctx context.Context) error {
	if report, err := s.RunEconomicOpportunityDiscovery(ctx, time.Now().UTC()); err != nil {
		log.Printf("[Heartbeat] economic opportunity discovery error: %v", err)
		s.emitEvent("heartbeat.economic_objective.error", err.Error())
	} else if report.Created > 0 || report.Failed > 0 {
		log.Printf("[Heartbeat] economic opportunity discovery: status=%s checked=%d created=%d duplicate_skipped=%d failed=%d",
			report.Status, report.Checked, report.Created, report.DuplicateSkipped, report.Failed)
		s.emitEvent("heartbeat.economic_objective."+report.Status, fmt.Sprintf("created=%d duplicate_skipped=%d failed=%d", report.Created, report.DuplicateSkipped, report.Failed))
	}
	// HEARTBEAT.md を読み込み
	heartbeatPath := filepath.Join(s.workspaceDir, "HEARTBEAT.md")
	data, err := os.ReadFile(heartbeatPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Println("[Heartbeat] HEARTBEAT.md not found, skipping")
			s.emitEvent("heartbeat.skip", "HEARTBEAT.md not found")
			return nil
		}
		wrapped := fmt.Errorf("failed to read HEARTBEAT.md: %w", err)
		s.emitEvent("heartbeat.error", wrapped.Error())
		return wrapped
	}

	heartbeatContent := strings.TrimSpace(string(data))
	if heartbeatContent == "" {
		log.Println("[Heartbeat] HEARTBEAT.md is empty, skipping")
		s.emitEvent("heartbeat.skip", "HEARTBEAT.md is empty")
		return nil
	}

	// ContextBuilder でShiro向けコンテキスト + HEARTBEAT.md を組み立て
	message := s.contextBuilder.BuildMessageWithTask(routing.RouteOPS.String(), "HEARTBEAT TASKS", heartbeatContent)

	// タスクを作成してShiroに処理させる
	jobID := task.NewJobID()
	t := newHeartbeatWorkerTask(jobID, message, "heartbeat", "heartbeat")

	response, err := s.workerAgent.Execute(ctx, t)
	if err != nil {
		s.logHeartbeat("ERROR", fmt.Sprintf("worker failed: %v", err))
		s.emitEvent("heartbeat.error", fmt.Sprintf("worker failed: %v", err))
		return fmt.Errorf("worker failed: %w", err)
	}

	// HEARTBEAT_OK なら正常終了（サイレント）
	if strings.TrimSpace(response) == "HEARTBEAT_OK" {
		s.logHeartbeat("OK", "silent")
		s.emitEvent("heartbeat.ok", "silent")
		return nil
	}

	// HEARTBEAT_OK 以外はユーザーに通知
	s.logHeartbeat("NOTIFY", response)
	s.emitEvent("heartbeat.notify", response)
	if s.sender != nil {
		if err := s.sender.SendNotification(ctx, response); err != nil {
			s.emitEvent("heartbeat.error", fmt.Sprintf("failed to send notification: %v", err))
			return fmt.Errorf("failed to send notification: %w", err)
		}
	}

	return nil
}

type WorkstreamHeartbeatRunReport struct {
	Checked int
	Run     int
	Skipped int
	Failed  int
}

type BacklogIntakeReport struct {
	Checked      int
	Promoted     int
	Skipped      int
	Failed       int
	Active       int
	ItemID       string
	WorkstreamID string
	GoalID       string
	ArtifactID   string
}

type BacklogRunnerReport struct {
	Checked int
	Started int
	Skipped int
	Failed  int
	ItemID  string
}

const backlogRunnerStartedMarker = "Backlog Runner Heartbeat started"

func backlogActiveItems(items []domainbacklog.Item) []domainbacklog.Item {
	active := make([]domainbacklog.Item, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		id := strings.TrimSpace(item.ItemID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if item.CheckOK {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(item.Status)) {
		case "implementing", "testing", "fixing":
			active = append(active, item)
		}
	}
	sort.SliceStable(active, func(i, j int) bool {
		leftStarted := backlogRunnerAlreadyStarted(active[i])
		rightStarted := backlogRunnerAlreadyStarted(active[j])
		if leftStarted != rightStarted {
			return leftStarted
		}
		leftRank := backlogPriorityRank(active[i].Priority)
		rightRank := backlogPriorityRank(active[j].Priority)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		leftTime := backlogItemTime(active[i])
		rightTime := backlogItemTime(active[j])
		if !leftTime.Equal(rightTime) {
			return leftTime.Before(rightTime)
		}
		return active[i].ItemID < active[j].ItemID
	})
	return active
}

func backlogIntakeCandidates(items []domainbacklog.Item) []domainbacklog.Item {
	candidates := make([]domainbacklog.Item, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		id := strings.TrimSpace(item.ItemID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if item.CheckOK || strings.TrimSpace(item.Title) == "" {
			continue
		}
		if strings.ToLower(strings.TrimSpace(item.Status)) != "open" {
			continue
		}
		candidates = append(candidates, item)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		leftRank := backlogPriorityRank(candidates[i].Priority)
		rightRank := backlogPriorityRank(candidates[j].Priority)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		leftTime := backlogItemTime(candidates[i])
		rightTime := backlogItemTime(candidates[j])
		if !leftTime.Equal(rightTime) {
			return leftTime.Before(rightTime)
		}
		return candidates[i].ItemID < candidates[j].ItemID
	})
	return candidates
}

func backlogRunnerAlreadyStarted(item domainbacklog.Item) bool {
	return strings.Contains(item.Implementation, backlogRunnerStartedMarker)
}

func backlogRunnerMessage(item domainbacklog.Item) string {
	return fmt.Sprintf(`/code2 Backlog item %s を1件だけ実装してください。

目的:
- %s

本文:
%s

実装メモ:
%s

制約:
- 対象 module / repo を確認し、実在する具体ファイルだけ変更する。
- placeholder path、sample path、説明だけの未接続ファイルは禁止。
- service restart / make install / live binary overwrite は patch に含めない。
- 変更は小さく、対象 Backlog item の完了に必要な範囲へ限定する。
- 検証コマンドを必ず実行する。
- 成功したら /viewer/backlog に item_id=%s を status=ok, check_ok=true, checked_by=coder, test_result に検証結果つきで POST する。
- 失敗または実装不能なら /viewer/backlog に status=blocked, check_ok=false とし、implementation/test_result に理由を残す。`,
		strings.TrimSpace(item.ItemID),
		strings.TrimSpace(item.Title),
		strings.TrimSpace(item.Body),
		strings.TrimSpace(item.Implementation),
		strings.TrimSpace(item.ItemID),
	)
}

func backlogPriorityRank(priority string) int {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "urgent":
		return 0
	case "high":
		return 1
	case "normal", "":
		return 2
	case "low":
		return 3
	default:
		return 2
	}
}

func backlogItemTime(item domainbacklog.Item) time.Time {
	for _, raw := range []string{item.UpdatedAt, item.CreatedAt} {
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(raw)); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func backlogWorkstreamDescription(item domainbacklog.Item) string {
	parts := []string{
		fmt.Sprintf("Backlog item: %s", strings.TrimSpace(item.ItemID)),
		fmt.Sprintf("kind: %s", strings.TrimSpace(item.Kind)),
		fmt.Sprintf("priority: %s", strings.TrimSpace(item.Priority)),
		fmt.Sprintf("source: %s", strings.TrimSpace(item.Source)),
	}
	if body := strings.TrimSpace(item.Body); body != "" {
		parts = append(parts, "", body)
	}
	if implementation := strings.TrimSpace(item.Implementation); implementation != "" {
		parts = append(parts, "", "Existing implementation note:", implementation)
	}
	return strings.Join(parts, "\n")
}

func backlogGoalDescription(item domainbacklog.Item) string {
	body := strings.TrimSpace(item.Body)
	if body == "" {
		body = strings.TrimSpace(item.Title)
	}
	return fmt.Sprintf("Backlog item %s を実装可能な作業単位に落とし込み、実装・検証・Backlog更新まで完了させる。\n\n%s",
		strings.TrimSpace(item.ItemID),
		body,
	)
}

func appendBacklogImplementation(existing, note string) string {
	existing = strings.TrimSpace(existing)
	note = strings.TrimSpace(note)
	if note == "" {
		return existing
	}
	if existing == "" {
		return note
	}
	if strings.Contains(existing, note) {
		return existing
	}
	return existing + "\n\n" + note
}

func (s *HeartbeatService) RunBacklogIntake(ctx context.Context, now time.Time) (BacklogIntakeReport, error) {
	var report BacklogIntakeReport
	if s.backlogStore == nil || s.workstreamStore == nil {
		return report, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	items, err := s.backlogStore.List(ctx, 500)
	if err != nil {
		report.Failed++
		s.emitEvent("backlog.intake.error", fmt.Sprintf("failed to list backlog: %v", err))
		return report, err
	}
	candidates := backlogIntakeCandidates(items)
	report.Checked = len(items)
	active := backlogActiveItems(items)
	report.Active = len(active)
	if len(active) > 0 {
		report.Skipped = len(items)
		report.ItemID = active[0].ItemID
		s.emitEvent("backlog.runner.waiting_active", fmt.Sprintf("%s status=%s", active[0].ItemID, active[0].Status))
		return report, nil
	}
	report.Skipped = len(items) - len(candidates)
	if len(candidates) == 0 {
		return report, nil
	}
	item := candidates[0]
	workstreamID := "ws_backlog_" + safePathSegment(item.ItemID)
	goalID := "goal_backlog_" + safePathSegment(item.ItemID)
	artifactID := "art_backlog_" + safePathSegment(item.ItemID)

	if err := s.workstreamStore.SaveWorkstream(ctx, domainworkstream.Workstream{
		WorkstreamID: workstreamID,
		Name:         "Backlog: " + item.Title,
		Description:  backlogWorkstreamDescription(item),
		Status:       domainworkstream.StatusActive,
		PrimaryAgent: "Coder",
		CreatedAt:    now.UTC(),
		UpdatedAt:    now.UTC(),
	}); err != nil {
		report.Failed++
		s.emitEvent("backlog.intake.error", fmt.Sprintf("failed to save workstream for %s: %v", item.ItemID, err))
		return report, err
	}
	if err := s.workstreamStore.SaveGoal(ctx, domainworkstream.Goal{
		GoalID:       goalID,
		WorkstreamID: workstreamID,
		Title:        item.Title,
		Description:  backlogGoalDescription(item),
		SuccessCriteria: []string{
			"Backlog item の要求が実装または明確な非採用判断に変換されている",
			"対象 module と根拠ファイルが実装・検証記録から追える",
			"Backlog item に test_result と最終状態が追記されている",
		},
		Verification: []string{
			"対象範囲の unit / integration test または代替検証を実行する",
			"必要な場合は live Viewer / API で実動作を確認する",
			"ユーザー承認が必要な変更は Sandbox / AI Workflow gate を通す",
		},
		Status:    domainworkstream.StatusWaiting,
		CreatedAt: now.UTC(),
	}); err != nil {
		report.Failed++
		s.emitEvent("backlog.intake.error", fmt.Sprintf("failed to save goal for %s: %v", item.ItemID, err))
		return report, err
	}
	if err := s.workstreamStore.SaveArtifact(ctx, domainworkstream.Artifact{
		ArtifactID:   artifactID,
		WorkstreamID: workstreamID,
		Type:         "backlog_intake",
		Title:        "Backlog intake: " + item.Title,
		Status:       "pending_review",
		CreatedAt:    now.UTC(),
	}); err != nil {
		report.Failed++
		s.emitEvent("backlog.intake.error", fmt.Sprintf("failed to save artifact for %s: %v", item.ItemID, err))
		return report, err
	}

	item.Status = "implementing"
	item.Implementer = "coder"
	item.Implementation = appendBacklogImplementation(item.Implementation, fmt.Sprintf(
		"Backlog Intake Heartbeat promoted this item to workstream_id=%s goal_id=%s artifact_id=%s at %s.",
		workstreamID,
		goalID,
		artifactID,
		now.UTC().Format(time.RFC3339),
	))
	if err := s.backlogStore.Save(ctx, item); err != nil {
		report.Failed++
		s.emitEvent("backlog.intake.error", fmt.Sprintf("failed to update backlog %s: %v", item.ItemID, err))
		return report, err
	}
	report.Promoted = 1
	report.ItemID = item.ItemID
	report.WorkstreamID = workstreamID
	report.GoalID = goalID
	report.ArtifactID = artifactID
	s.emitEvent("backlog.intake.promoted", fmt.Sprintf("%s -> %s", item.ItemID, workstreamID))
	return report, nil
}

func (s *HeartbeatService) RunBacklogRunner(ctx context.Context, now time.Time) (BacklogRunnerReport, error) {
	var report BacklogRunnerReport
	if s.backlogStore == nil || s.workerAgent == nil {
		return report, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	items, err := s.backlogStore.List(ctx, 500)
	if err != nil {
		report.Failed++
		s.emitEvent("backlog.runner.error", fmt.Sprintf("failed to list backlog: %v", err))
		return report, err
	}
	active := backlogActiveItems(items)
	report.Checked = len(items)
	report.Skipped = len(items)
	if len(active) == 0 {
		return report, nil
	}
	item := active[0]
	report.ItemID = item.ItemID
	if backlogRunnerAlreadyStarted(item) {
		s.emitEvent("backlog.runner.waiting_active", fmt.Sprintf("%s status=%s runner=started", item.ItemID, item.Status))
		return report, nil
	}

	startedNote := fmt.Sprintf("%s item_id=%s at %s.", backlogRunnerStartedMarker, item.ItemID, now.UTC().Format(time.RFC3339))
	item.Implementation = appendBacklogImplementation(item.Implementation, startedNote)
	item.Status = "implementing"
	item.Implementer = "coder"
	if err := s.backlogStore.Save(ctx, item); err != nil {
		report.Failed++
		s.emitEvent("backlog.runner.error", fmt.Sprintf("failed to mark runner start for %s: %v", item.ItemID, err))
		return report, err
	}

	jobID := task.NewJobID()
	t := newHeartbeatWorkerTask(jobID, backlogRunnerMessage(item), "backlog-runner", "heartbeat")
	s.emitEvent("backlog.runner.started", fmt.Sprintf("%s job_id=%s", item.ItemID, jobID.String()))
	if _, err := s.workerAgent.Execute(ctx, t); err != nil {
		item.Status = "blocked"
		item.TestResult = fmt.Sprintf("Backlog Runner failed to start job_id=%s: %v", jobID.String(), err)
		item.Implementation = appendBacklogImplementation(item.Implementation, item.TestResult)
		_ = s.backlogStore.Save(ctx, item)
		report.Failed++
		s.emitEvent("backlog.runner.error", fmt.Sprintf("%s job_id=%s err=%v", item.ItemID, jobID.String(), err))
		return report, err
	}
	report.Started = 1
	report.Skipped = len(items) - 1
	return report, nil
}

func (s *HeartbeatService) RunDueWorkstreamHeartbeats(ctx context.Context, now time.Time) (WorkstreamHeartbeatRunReport, error) {
	var report WorkstreamHeartbeatRunReport
	if s.workstreamStore == nil {
		return report, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	schedules, err := s.workstreamStore.ListHeartbeatSchedules(ctx, 1000)
	if err != nil {
		s.emitEvent("workstream.heartbeat.error", fmt.Sprintf("failed to list schedules: %v", err))
		return report, err
	}
	seen := map[string]struct{}{}
	for _, schedule := range schedules {
		if _, ok := seen[schedule.HeartbeatID]; ok {
			continue
		}
		seen[schedule.HeartbeatID] = struct{}{}
		report.Checked++
		if schedule.Status != domainworkstream.StatusActive || !heartbeatDue(schedule, now) {
			report.Skipped++
			continue
		}
		if err := s.runWorkstreamHeartbeat(ctx, schedule, now); err != nil {
			report.Failed++
			s.emitEvent("workstream.heartbeat.error", err.Error())
			return report, err
		}
		report.Run++
	}
	if report.Run > 0 {
		s.emitEvent("workstream.heartbeat.completed", fmt.Sprintf("run=%d skipped=%d", report.Run, report.Skipped))
	}
	return report, nil
}

func (s *HeartbeatService) runWorkstreamHeartbeat(ctx context.Context, schedule domainworkstream.HeartbeatSchedule, now time.Time) error {
	if s.skills != nil {
		if _, err := s.skills.Record(ctx, domainskill.TaskContext{
			Text:         schedule.Task,
			Intent:       "workstream_heartbeat",
			Agent:        "Worker",
			WorkstreamID: schedule.WorkstreamID,
		}, []string{"core.workstream-heartbeat", "core.workstream"}); err != nil {
			return fmt.Errorf("workstream heartbeat %s skill bootstrap failed: %w", schedule.HeartbeatID, err)
		}
	}
	pendingSteering, err := s.pendingSteeringForWorkstream(ctx, schedule.WorkstreamID)
	if err != nil {
		return fmt.Errorf("workstream heartbeat %s steering checkpoint failed: %w", schedule.HeartbeatID, err)
	}
	message := s.contextBuilder.BuildMessageWithTask(
		routing.RouteOPS.String(),
		"WORKSTREAM HEARTBEAT DRAFT",
		fmt.Sprintf("workstream_id: %s\nheartbeat_id: %s\nschedule: %s\ntask: %s\n\nsafe_checkpoint_steering:\n%s\n\n制約: draft report only。投稿、送信、販売、外部書き込みは行わない。",
			schedule.WorkstreamID,
			schedule.HeartbeatID,
			schedule.ScheduleText,
			schedule.Task,
			formatSteeringForPrompt(pendingSteering),
		),
	)
	jobID := task.NewJobID()
	t := newHeartbeatWorkerTask(jobID, message, "workstream-heartbeat", "heartbeat")
	response, err := s.workerAgent.Execute(ctx, t)
	if err != nil {
		return fmt.Errorf("workstream heartbeat %s worker failed: %w", schedule.HeartbeatID, err)
	}
	reportPath, err := s.writeWorkstreamHeartbeatReport(schedule, now, response)
	if err != nil {
		return fmt.Errorf("workstream heartbeat %s report failed: %w", schedule.HeartbeatID, err)
	}
	update := domainworkstream.VaultUpdateLog{
		UpdateID:     fmt.Sprintf("vul_%s_%d", schedule.HeartbeatID, now.UnixNano()),
		WorkstreamID: schedule.WorkstreamID,
		FilePath:     reportPath,
		UpdateType:   "heartbeat_draft_report",
		ReviewStatus: "pending",
		CreatedAt:    now.UTC(),
	}
	if err := s.workstreamStore.SaveVaultUpdateLog(ctx, update); err != nil {
		return fmt.Errorf("workstream heartbeat %s vault update log failed: %w", schedule.HeartbeatID, err)
	}
	if shouldRunRevenueDailyRoutine(schedule) {
		if err := s.runRevenueDailyRoutine(ctx, schedule, now); err != nil {
			return fmt.Errorf("workstream heartbeat %s revenue daily routine failed: %w", schedule.HeartbeatID, err)
		}
	}
	if err := s.markSteeringApplied(ctx, pendingSteering, now); err != nil {
		return fmt.Errorf("workstream heartbeat %s steering apply failed: %w", schedule.HeartbeatID, err)
	}
	schedule.LastRunAt = now.UTC()
	schedule.NextRunAt = nextHeartbeatRun(schedule, now)
	if err := s.workstreamStore.SaveHeartbeatSchedule(ctx, schedule); err != nil {
		return fmt.Errorf("workstream heartbeat %s schedule update failed: %w", schedule.HeartbeatID, err)
	}
	s.emitEvent("workstream.heartbeat.draft_report", reportPath)
	return nil
}

func shouldRunRevenueDailyRoutine(schedule domainworkstream.HeartbeatSchedule) bool {
	text := strings.ToLower(strings.Join([]string{schedule.HeartbeatID, schedule.WorkstreamID, schedule.Task}, "\n"))
	keywords := []string{
		"revenue",
		"収益",
		"売上",
		"市場調査",
		"sns",
		"商品",
		"顧客の声",
	}
	for _, keyword := range keywords {
		if strings.Contains(text, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

func (s *HeartbeatService) runRevenueDailyRoutine(ctx context.Context, schedule domainworkstream.HeartbeatSchedule, now time.Time) error {
	if s.revenueRoutine == nil {
		return nil
	}
	result, err := s.revenueRoutine.RunDailyRoutine(ctx, revenueapp.DailyRoutineRequest{
		ReportID:     fmt.Sprintf("rev_daily_%s_%d", safePathSegment(schedule.HeartbeatID), now.UnixNano()),
		WorkstreamID: schedule.WorkstreamID,
		Date:         now.UTC().Format("2006-01-02"),
		Now:          now.UTC(),
	})
	if err != nil {
		return err
	}
	s.emitEvent("revenue.daily_routine.draft_report", fmt.Sprintf("%s:%s", result.Agent, result.Report.ReportID))
	return nil
}

func (s *HeartbeatService) pendingSteeringForWorkstream(ctx context.Context, workstreamID string) ([]domainworkstream.SteeringItem, error) {
	if s.workstreamStore == nil {
		return nil, nil
	}
	items, err := s.workstreamStore.ListSteeringItems(ctx, 1000)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var pending []domainworkstream.SteeringItem
	for _, item := range items {
		if item.WorkstreamID != workstreamID {
			continue
		}
		if _, ok := seen[item.SteeringID]; ok {
			continue
		}
		seen[item.SteeringID] = struct{}{}
		if strings.TrimSpace(item.Status) == "pending" {
			pending = append(pending, item)
		}
	}
	return pending, nil
}

func (s *HeartbeatService) markSteeringApplied(ctx context.Context, items []domainworkstream.SteeringItem, now time.Time) error {
	for _, item := range items {
		item.Status = "applied"
		item.AppliedAt = now.UTC()
		if err := s.workstreamStore.SaveSteeringItem(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func formatSteeringForPrompt(items []domainworkstream.SteeringItem) string {
	if len(items) == 0 {
		return "- none"
	}
	lines := make([]string, 0, len(items))
	for _, item := range items {
		target := strings.TrimSpace(item.TargetArtifactID)
		if target == "" {
			target = "workstream"
		}
		lines = append(lines, fmt.Sprintf("- %s [%s]: %s", item.SteeringID, target, strings.TrimSpace(item.Instruction)))
	}
	return strings.Join(lines, "\n")
}

func (s *HeartbeatService) writeWorkstreamHeartbeatReport(schedule domainworkstream.HeartbeatSchedule, now time.Time, body string) (string, error) {
	dir := filepath.Join(s.workspaceDir, "workstream_heartbeats", safePathSegment(schedule.WorkstreamID))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s.md", safePathSegment(schedule.HeartbeatID), now.UTC().Format("20060102T150405Z"))
	path := filepath.Join(dir, name)
	content := fmt.Sprintf("# Workstream Heartbeat Draft\n\n- workstream_id: %s\n- heartbeat_id: %s\n- schedule: %s\n- created_at: %s\n\n## Task\n\n%s\n\n## Draft Report\n\n%s\n",
		schedule.WorkstreamID,
		schedule.HeartbeatID,
		schedule.ScheduleText,
		now.UTC().Format(time.RFC3339),
		schedule.Task,
		strings.TrimSpace(body),
	)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", err
	}
	return path, nil
}

func heartbeatDue(schedule domainworkstream.HeartbeatSchedule, now time.Time) bool {
	return schedule.NextRunAt.IsZero() || !schedule.NextRunAt.After(now.UTC())
}

func nextHeartbeatRun(schedule domainworkstream.HeartbeatSchedule, now time.Time) time.Time {
	text := strings.TrimSpace(strings.ToLower(schedule.ScheduleText))
	if strings.HasPrefix(text, "daily ") {
		clock := strings.TrimSpace(strings.TrimPrefix(text, "daily "))
		parts := strings.Split(clock, ":")
		if len(parts) == 2 {
			hour, hourErr := parseTwoDigitInt(parts[0])
			minute, minuteErr := parseTwoDigitInt(parts[1])
			if hourErr == nil && minuteErr == nil && hour >= 0 && hour <= 23 && minute >= 0 && minute <= 59 {
				next := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), hour, minute, 0, 0, time.UTC)
				if !next.After(now.UTC()) {
					next = next.Add(24 * time.Hour)
				}
				return next
			}
		}
	}
	return now.UTC().Add(24 * time.Hour)
}

func parseTwoDigitInt(raw string) (int, error) {
	if len(raw) == 0 || len(raw) > 2 {
		return 0, fmt.Errorf("invalid number")
	}
	value := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid number")
		}
		value = value*10 + int(r-'0')
	}
	return value, nil
}

func safePathSegment(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", "..", "_", " ", "_")
	return replacer.Replace(raw)
}

// logHeartbeat はHeartbeat結果をheartbeat.logに記録
func (s *HeartbeatService) logHeartbeat(status, message string) {
	logPath := filepath.Join(s.workspaceDir, "heartbeat.log")
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	entry := fmt.Sprintf("[%s] [%s] %s\n", timestamp, status, message)

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[Heartbeat] failed to write log: %v", err)
		return
	}
	defer f.Close()
	f.WriteString(entry)
}

func (s *HeartbeatService) emitEvent(eventType, content string) {
	if s.listener == nil {
		return
	}
	s.listener.OnEvent(orchestrator.NewEvent(
		eventType,
		"heartbeat",
		"viewer",
		content,
		"HEARTBEAT",
		"",
		"heartbeat",
		"heartbeat",
		"viewer",
	))
}
