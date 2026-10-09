package heartbeat

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	// heartbeatFinalizationWindow は終端書き込みの再試行を続ける最長時間。
	// 放棄したTaskはrunningのままoperations枠を再起動まで握り続けるため、一時的な
	// 遅延では放棄しない長さにする。ただし恒久障害で台帳が終わりなく育たないよう有限にする。
	heartbeatFinalizationWindow = 24 * time.Hour
	// 再試行間隔は base から倍々に伸び max で止まる。再試行1回は store 全体の走査を伴い
	// 同じグローバルロックを使うため、詰まっている store を叩き続けない上限が要る。
	heartbeatFinalizationBackoffBase = time.Minute
	heartbeatFinalizationBackoffMax  = 15 * time.Minute
	// 1回のsweepがHeartbeat tick (メインループ) を塞ぐ上限。新しい試行は件数と累計時間の
	// どちらかが上限に達したら始めず、残りは次のtickへ持ち越す。
	maxFinalizationAttemptsPerSweep  = 2
	heartbeatFinalizationSweepBudget = 90 * time.Second
	// maxWorkerFinalizationErrorRunes は終端書き込みエラーを1行で残す際の上限。
	maxWorkerFinalizationErrorRunes = 240
)

// finalizationBackoff は attempts 回失敗した後の再試行までの待ち時間。
func finalizationBackoff(attempts int) time.Duration {
	delay := heartbeatFinalizationBackoffBase
	for i := 1; i < attempts && delay < heartbeatFinalizationBackoffMax; i++ {
		delay *= 2
	}
	if delay > heartbeatFinalizationBackoffMax {
		delay = heartbeatFinalizationBackoffMax
	}
	return delay
}

// deferredWorkerFinalization は終端書き込みに失敗してrunningのまま残った
// Heartbeat Taskの再試行単位。workerErrは元の終端意図 (成功/失敗/取消) を保つ。
type deferredWorkerFinalization struct {
	taskID      modulecore.TaskID
	workerErr   error
	firstSeen   time.Time
	attempts    int
	lastError   string
	nextAttempt time.Time
}

// workerFinalizationSweepReport は1回のsweep結果。Deferredはsweep開始時点の
// 滞留数、Retriedは試行数、Recoveredは終端確定 (再試行成功または別経路で決着済み)、
// GivenUpは窓超過で放棄した数、NotDueはbackoff待ちで今回は試行しなかった数。
// 放棄したTaskはrunningのまま観測可能に残り、プロセス再起動時の孤児回収が終端させる。
type workerFinalizationSweepReport struct {
	Deferred  int
	Retried   int
	Recovered int
	GivenUp   int
	NotDue    int
	LastError string
}

// recordDeferredWorkerFinalization は終端書き込みの失敗を滞留台帳に登録する。
func (s *HeartbeatService) recordDeferredWorkerFinalization(taskID modulecore.TaskID, workerErr error, cause error) {
	s.recordDeferredWorkerFinalizationAt(taskID, workerErr, cause, time.Now().UTC(), 0)
}

// recordDeferredWorkerFinalizationAt は失敗を at 時刻の出来事として台帳に登録し、
// 次回の再試行時刻を backoff で決める。同じTaskの再登録は初回時刻を保ち、窓の上限が
// 延長されることを防ぐ。elapsed は失敗した書き込みの所要時間 (ログ用)。
func (s *HeartbeatService) recordDeferredWorkerFinalizationAt(taskID modulecore.TaskID, workerErr error, cause error, at time.Time, elapsed time.Duration) {
	if s == nil || taskID == modulecore.TaskID("") {
		return
	}
	s.finalizationMu.Lock()
	if s.deferredFinalizations == nil {
		s.deferredFinalizations = make(map[modulecore.TaskID]*deferredWorkerFinalization)
	}
	entry, ok := s.deferredFinalizations[taskID]
	if !ok {
		entry = &deferredWorkerFinalization{taskID: taskID, workerErr: workerErr, firstSeen: at}
		s.deferredFinalizations[taskID] = entry
	}
	entry.attempts++
	entry.lastError = sanitizeWorkerFinalizationError(cause)
	backoff := finalizationBackoff(entry.attempts)
	entry.nextAttempt = at.Add(backoff)
	attempts := entry.attempts
	lastError := entry.lastError
	pending := len(s.deferredFinalizations)
	s.finalizationMu.Unlock()

	// 終端書き込みの失敗はTaskをrunningに固定し、operations枠を返さないため、
	// 原因を必ず1行・有界で残す。捨てると観測不能になる。
	log.Printf("[Heartbeat] worker finalization deferred task_id=%s attempts=%d pending=%d elapsed=%s retry_in=%s error=%s",
		taskID, attempts, pending, elapsed.Round(time.Millisecond), backoff, lastError)
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

// noteFinalizationLatency は成功した終端書き込みが予算の半分を超えたら1行で残す。
// store の遅延は履歴量とともに増えるため、失敗する前に傾向を観測できるようにする。
func (s *HeartbeatService) noteFinalizationLatency(taskID modulecore.TaskID, elapsed time.Duration) {
	budget := s.finalizationTimeout()
	if elapsed <= budget/2 {
		return
	}
	log.Printf("[Heartbeat] worker finalization slow task_id=%s elapsed=%s budget=%s",
		taskID, elapsed.Round(time.Millisecond), budget)
}

// stopRequested は Heartbeat の停止要求が出ているかを非ブロッキングで返す。
func (s *HeartbeatService) stopRequested() bool {
	select {
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

// finalizationAttemptContext は再試行1回分の独立した予算付きcontextを返す。親のcancelは
// 引き継がず (tickは背景contextで動く)、停止要求でだけ打ち切る。
func (s *HeartbeatService) finalizationAttemptContext(parent context.Context) (context.Context, context.CancelFunc) {
	attemptCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), s.finalizationTimeout())
	stop := s.stopCh
	if stop == nil {
		return attemptCtx, cancel
	}
	go func() {
		select {
		case <-stop:
			cancel()
		case <-attemptCtx.Done():
		}
	}()
	return attemptCtx, cancel
}

// sweepDeferredWorkerFinalizations は滞留する終端書き込みを再試行し、終端した
// Taskのoperations枠を新しいTask (Atlas backlog runner等) に返す。
//
// 再試行は entry ごと・書き込みごとに独立した予算で行い、書き込み前の読み取りはしない。
// 別経路 (再起動時の孤児回収など) が先に終端させていた場合は、終端状態からの無効遷移として
// 書き込みが拒否されるので、それを「決着済み」とみなして台帳から除く。
// 失敗した entry は backoff で次回時刻を延ばし、窓 (heartbeatFinalizationWindow) を超えたものだけを放棄する。
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

	started := time.Now()
	due := make([]deferredWorkerFinalization, 0, len(entries))
	for _, entry := range entries {
		if entry.nextAttempt.After(now) {
			report.NotDue++
			continue
		}
		due = append(due, entry)
	}

	for _, entry := range due {
		if report.Retried >= maxFinalizationAttemptsPerSweep ||
			time.Since(started) >= heartbeatFinalizationSweepBudget ||
			s.stopRequested() {
			break
		}
		report.Retried++
		attemptCtx, cancel := s.finalizationAttemptContext(ctx)
		attemptStarted := time.Now()
		err := s.finalizeWorkerTask(attemptCtx, entry.taskID, entry.workerErr)
		elapsed := time.Since(attemptStarted)
		cancel()
		// 停止要求で打ち切られた試行は失敗として数えない。台帳はそのまま残し、
		// 枠は再起動時の孤児回収に委ねる。
		if err != nil && s.stopRequested() {
			break
		}
		at := now.Add(time.Since(started))
		switch {
		case err == nil:
			s.forgetDeferredWorkerFinalization(entry.taskID)
			report.Recovered++
			log.Printf("[Heartbeat] worker finalization recovered task_id=%s attempts=%d elapsed=%s",
				entry.taskID, entry.attempts+1, elapsed.Round(time.Millisecond))
			s.noteFinalizationLatency(entry.taskID, elapsed)
		case domaintask.AlreadyTerminal(err):
			// 孤児回収など別の経路が既に終端済み。書き直さず台帳から除くだけ。
			s.forgetDeferredWorkerFinalization(entry.taskID)
			report.Recovered++
			log.Printf("[Heartbeat] worker finalization already settled task_id=%s attempts=%d elapsed=%s reason=%s",
				entry.taskID, entry.attempts+1, elapsed.Round(time.Millisecond), sanitizeWorkerFinalizationError(err))
		default:
			report.LastError = sanitizeWorkerFinalizationError(err)
			if at.Sub(entry.firstSeen) > heartbeatFinalizationWindow {
				s.abandonDeferredWorkerFinalization(entry, report.LastError)
				report.GivenUp++
				continue
			}
			s.recordDeferredWorkerFinalizationAt(entry.taskID, entry.workerErr, err, at, elapsed)
		}
	}
	// 再試行が無かったtickでも、枠を握り続けているTaskの存在が見えるようにする。
	log.Printf("[Heartbeat] worker finalization sweep: deferred=%d retried=%d recovered=%d given_up=%d not_due=%d carried_over=%d pending=%d last_error=%s",
		report.Deferred, report.Retried, report.Recovered, report.GivenUp, report.NotDue,
		len(due)-report.Retried, s.deferredWorkerFinalizationCount(), report.LastError)
	return report
}

// abandonDeferredWorkerFinalization は窓を超えた entry を台帳から外す。Taskはrunningのまま
// operations枠を握り続けるため、ログとイベントの両方で観測可能にする。
func (s *HeartbeatService) abandonDeferredWorkerFinalization(entry deferredWorkerFinalization, lastError string) {
	s.forgetDeferredWorkerFinalization(entry.taskID)
	log.Printf("[Heartbeat] worker finalization abandoned task_id=%s attempts=%d window=%s error=%s (task stays running until restart recovery)",
		entry.taskID, entry.attempts+1, heartbeatFinalizationWindow, lastError)
	content := fmt.Sprintf("task_id=%s attempts=%d window=%s", entry.taskID, entry.attempts+1, heartbeatFinalizationWindow)
	if err := s.emitEvent("heartbeat.worker_finalization.abandoned", content); err != nil {
		log.Printf("[Heartbeat] worker finalization abandon event failed task_id=%s error=%s",
			entry.taskID, sanitizeWorkerFinalizationError(err))
	}
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
