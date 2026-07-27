package main

import (
	"errors"
	"log"
	"sync"
	"sync/atomic"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

// 記録パス専用の非同期レコーダー。
//
// docs/10_ログ仕様.md「記録と配信の分離」に従う。
//
//   - 記録パスは、バッファ満杯を理由にイベントを破棄しない。捨てるくらいなら
//     遅延させる。したがって送信側は空きが出るまでブロックする。
//   - 配信パス（EventHub の SSE 配信）は低遅延を優先し drop を許容するが、
//     このレコーダーは記録専用であり drop しない。
//   - 書き込み失敗は無言で握りつぶさず error として出力する。
//
// 記録と配信で同じキューを共有すると、配信のための drop が記録の欠落を招く。
// そのため配信用の SideEffects キューとは別経路にする。

var errRecordTest = errors.New("record failed")

type eventRecordFunc func(orchestrator.OrchestratorEvent) error

type eventRecorder struct {
	ch         chan orchestrator.OrchestratorEvent
	record     eventRecordFunc
	done       chan struct{}
	finishedCh chan struct{}
	closeOnce  sync.Once

	dropped atomic.Int64

	mu        sync.RWMutex
	onFailure func(orchestrator.OrchestratorEvent, error)
}

// newEventRecorder は記録専用のレコーダーを生成する
//
// buffer は遅延を吸収するための容量であり、上限ではない。満杯時は送信側が
// ブロックして待つため、イベントは失われない。
func newEventRecorder(record eventRecordFunc, buffer int) *eventRecorder {
	if buffer <= 0 {
		buffer = 256
	}
	r := &eventRecorder{
		ch:         make(chan orchestrator.OrchestratorEvent, buffer),
		record:     record,
		done:       make(chan struct{}),
		finishedCh: make(chan struct{}),
	}
	go r.run()
	return r
}

// OnFailure は記録失敗時のコールバックを設定する
func (r *eventRecorder) OnFailure(fn func(orchestrator.OrchestratorEvent, error)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onFailure = fn
}

// Record はイベントを記録キューへ渡す
//
// キューが満杯の場合はブロックする。記録パスでイベントを落とさないための
// 意図的な設計である。
func (r *eventRecorder) Record(ev orchestrator.OrchestratorEvent) {
	if r == nil || r.record == nil {
		return
	}
	select {
	case <-r.done:
		// Close 後は記録しない。落とした件数として計上する
		r.dropped.Add(1)
		return
	default:
	}
	r.ch <- ev
}

// Dropped は記録できなかった件数を返す
//
// Close 後の投入だけがここに計上される。通常運用では 0 である。
func (r *eventRecorder) Dropped() int64 {
	if r == nil {
		return 0
	}
	return r.dropped.Load()
}

// Close は残っているイベントを記録し終えてから停止する
func (r *eventRecorder) Close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		close(r.done)
		close(r.ch)
	})
	<-r.finished()
}

func (r *eventRecorder) finished() chan struct{} {
	return r.finishedCh
}

func (r *eventRecorder) run() {
	defer close(r.finishedCh)
	for ev := range r.ch {
		if err := r.record(ev); err != nil {
			r.reportFailure(ev, err)
		}
	}
}

func (r *eventRecorder) reportFailure(ev orchestrator.OrchestratorEvent, err error) {
	log.Printf("ERROR: event record failed session_id=%s type=%s seq=%d err=%v", ev.SessionID, ev.Type, ev.Seq, err)
	r.mu.RLock()
	fn := r.onFailure
	r.mu.RUnlock()
	if fn != nil {
		fn(ev, err)
	}
}
