package heartbeat

import (
	"context"
	"log"
	"sort"
	"strings"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	// heartbeatFinalizationWindow は終端書き込みの再試行を保持する最長時間。
	// 本番のHeartbeat tickは5分間隔なので最大6回まで再試行し、恒久障害で
	// deferred集合とoperations枠の占有が終わりなく続かないようにする。
	heartbeatFinalizationWindow = 30 * time.Minute
	// maxWorkerFinalizationErrorRunes は終端書き込みエラーを1行で残す際の上限。
	maxWorkerFinalizationErrorRunes = 240
)

// deferredWorkerFinalization は終端書き込みに失敗してrunningのまま残った
// Heartbeat Taskの再試行単位。workerErrは元の終端意図 (成功/失敗/取消) を保つ。
type deferredWorkerFinalization struct {
	taskID    modulecore.TaskID
	workerErr error
	firstSeen time.Time
	attempts  int
	lastError string
}

// workerFinalizationSweepReport は1回のsweep結果。Deferredはsweep開始時点の
// 滞留数、Retriedは試行数、Recoveredは終端確定 (再試行成功または既に終端)、
// GivenUpは窓超過で放棄した数。放棄したTaskはrunningのまま観測可能に残り、
// プロセス再起動時の孤児回収が終端させる。
type workerFinalizationSweepReport struct {
	Deferred  int
	Retried   int
	Recovered int
	GivenUp   int
	LastError string
}

// recordDeferredWorkerFinalization は終端書き込みの失敗を滞留台帳に登録する。
// 同じTaskの再登録は初回時刻を保ち、窓の上限が延長されることを防ぐ。
func (s *HeartbeatService) recordDeferredWorkerFinalization(taskID modulecore.TaskID, workerErr error, cause error) {
	if s == nil || taskID == modulecore.TaskID("") {
		return
	}
	s.finalizationMu.Lock()
	if s.deferredFinalizations == nil {
		s.deferredFinalizations = make(map[modulecore.TaskID]*deferredWorkerFinalization)
	}
	firstSeen := time.Now().UTC()
	attempts := 1
	if existing, ok := s.deferredFinalizations[taskID]; ok {
		firstSeen = existing.firstSeen
		attempts = existing.attempts + 1
	} else {
		s.deferredFinalizations[taskID] = &deferredWorkerFinalization{
			taskID:    taskID,
			workerErr: workerErr,
			firstSeen: firstSeen,
		}
	}
	entry := s.deferredFinalizations[taskID]
	entry.attempts = attempts
	entry.lastError = sanitizeWorkerFinalizationError(cause)
	pending := len(s.deferredFinalizations)
	s.finalizationMu.Unlock()

	// 終端書き込みの失敗はTaskをrunningに固定し、operations枠を返さないため、
	// 原因を必ず1行・有界で残す。捨てると観測不能になる。
	log.Printf("[Heartbeat] worker finalization deferred task_id=%s attempts=%d pending=%d error=%s",
		taskID, attempts, pending, entry.lastError)
}

// forgetDeferredWorkerFinalization は終端書き込みに成功したTaskを滞留台帳から
// 除く。存在しないTaskIDは何もしない。
func (s *HeartbeatService) forgetDeferredWorkerFinalization(taskID modulecore.TaskID) {
	if s == nil || taskID == modulecore.TaskID("") {
		return
	}
	s.finalizationMu.Lock()
	delete(s.deferredFinalizations, taskID)
	s.finalizationMu.Unlock()
}

// deferredWorkerFinalizationCount は滞留中の終端再試行数 (監視・テスト用)。
func (s *HeartbeatService) deferredWorkerFinalizationCount() int {
	if s == nil {
		return 0
	}
	s.finalizationMu.Lock()
	defer s.finalizationMu.Unlock()
	return len(s.deferredFinalizations)
}

// sweepDeferredWorkerFinalizations は滞留する終端書き込みを再試行し、終端した
// Taskのoperations枠を新しいTask (Atlas backlog runner等) に返す。
func (s *HeartbeatService) sweepDeferredWorkerFinalizations(ctx context.Context, now time.Time) workerFinalizationSweepReport {
	var report workerFinalizationSweepReport
	if s == nil || s.taskOwner == nil {
		return report
	}
	if ctx == nil {
		ctx = context.Background()
	}
	entries := s.pendingDeferredWorkerFinalizations()
	report.Deferred = len(entries)
	if len(entries) == 0 {
		return report
	}

	attemptCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), heartbeatTaskCleanupTimeout)
	defer cancel()

	for _, entry := range entries {
		report.Retried++
		if s.workerTaskIsTerminal(attemptCtx, entry.taskID) {
			// 孤児回収など別の経路が既に終端済み。書き直さず台帳から除くだけ。
			s.forgetDeferredWorkerFinalization(entry.taskID)
			report.Recovered++
			log.Printf("[Heartbeat] worker finalization already settled task_id=%s attempts=%d", entry.taskID, entry.attempts)
			continue
		}
		err := s.finalizeWorkerTask(attemptCtx, entry.taskID, entry.workerErr)
		if err == nil {
			s.forgetDeferredWorkerFinalization(entry.taskID)
			report.Recovered++
			log.Printf("[Heartbeat] worker finalization recovered task_id=%s attempts=%d", entry.taskID, entry.attempts+1)
			continue
		}
		report.LastError = sanitizeWorkerFinalizationError(err)
		if now.Sub(entry.firstSeen) > heartbeatFinalizationWindow {
			s.forgetDeferredWorkerFinalization(entry.taskID)
			report.GivenUp++
			log.Printf("[Heartbeat] worker finalization abandoned task_id=%s attempts=%d window=%s error=%s",
				entry.taskID, entry.attempts+1, heartbeatFinalizationWindow, report.LastError)
			continue
		}
		s.recordDeferredWorkerFinalization(entry.taskID, entry.workerErr, err)
	}
	if report.Deferred > 0 {
		log.Printf("[Heartbeat] worker finalization sweep: deferred=%d retried=%d recovered=%d given_up=%d pending=%d last_error=%s",
			report.Deferred, report.Retried, report.Recovered, report.GivenUp,
			s.deferredWorkerFinalizationCount(), report.LastError)
	}
	return report
}

// pendingDeferredWorkerFinalizations は再試行順 (初回時刻→TaskID) の写しを返す。
func (s *HeartbeatService) pendingDeferredWorkerFinalizations() []deferredWorkerFinalization {
	s.finalizationMu.Lock()
	entries := make([]deferredWorkerFinalization, 0, len(s.deferredFinalizations))
	for _, entry := range s.deferredFinalizations {
		entries = append(entries, *entry)
	}
	s.finalizationMu.Unlock()
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].firstSeen.Equal(entries[j].firstSeen) {
			return entries[i].firstSeen.Before(entries[j].firstSeen)
		}
		return entries[i].taskID < entries[j].taskID
	})
	return entries
}

// workerTaskIsTerminal は別の回収経路が既にTaskを終端させたかを問い合わせる。
// 問い合わせに失敗した場合は終端と断定せず、再試行させる。
func (s *HeartbeatService) workerTaskIsTerminal(ctx context.Context, taskID modulecore.TaskID) bool {
	task, err := s.taskOwner.Get(ctx, taskID)
	if err != nil {
		return false
	}
	return domaintask.IsTerminal(task.Status)
}

// sanitizeWorkerFinalizationError は終端書き込みのエラーを1行・有界にする。
// 改行やタブが残ると1障害1行のログ契約が崩れ、巡回で見えなくなる。
func sanitizeWorkerFinalizationError(err error) string {
	if err == nil {
		return "none"
	}
	flat := strings.Join(strings.Fields(strings.ReplaceAll(err.Error(), "\x00", " ")), " ")
	runes := []rune(flat)
	if len(runes) > maxWorkerFinalizationErrorRunes {
		return string(runes[:maxWorkerFinalizationErrorRunes]) + "…"
	}
	return flat
}
