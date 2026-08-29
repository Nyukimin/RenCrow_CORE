package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type shutdownOrderEventStore struct {
	mu      sync.Mutex
	closed  bool
	appends int
}

func (s *shutdownOrderEventStore) Append(_ context.Context, _ modulecore.EventEnvelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errRecordTest
	}
	s.appends++
	return nil
}

func (s *shutdownOrderEventStore) GetByID(context.Context, modulecore.EventID) (modulecore.EventEnvelope, bool, error) {
	return modulecore.EventEnvelope{}, false, nil
}

func (s *shutdownOrderEventStore) ListByComponent(context.Context, string, int) ([]modulecore.EventEnvelope, error) {
	return nil, nil
}

func (s *shutdownOrderEventStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// recordingSink は記録パスへ届いたイベントを数える
type recordingSink struct {
	mu       sync.Mutex
	received []orchestrator.OrchestratorEvent
	delay    time.Duration
}

func (s *recordingSink) Record(ev orchestrator.OrchestratorEvent) error {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.received = append(s.received, ev)
	return nil
}

func (s *recordingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.received)
}

// TestEventRecorderDoesNotDropUnderBurst は記録パスが輻輳時にイベントを
// 落とさないことを確認する
//
// docs/10_ログ仕様.md「記録と配信の分離」より:
// 記録パスは、バッファ満杯を理由にイベントを破棄しない。捨てるくらいなら
// 遅延させる。配信パスの drop は許容するが、記録パスは許容しない。
//
// 記録先を意図的に遅くし、バッファ容量を大幅に超える件数を投入する。
func TestEventRecorderDoesNotDropUnderBurst(t *testing.T) {
	sink := &recordingSink{delay: time.Millisecond}
	recorder := newEventRecorder(sink.Record, 4)
	t.Cleanup(recorder.Close)

	const total = 200
	for i := 0; i < total; i++ {
		recorder.Record(orchestrator.OrchestratorEvent{SessionID: "burst", Type: "message.received"})
	}
	recorder.Close()

	if got := sink.count(); got != total {
		t.Fatalf("recorded %d events, want %d (記録パスでイベントが欠落した)", got, total)
	}
	if dropped := recorder.Dropped(); dropped != 0 {
		t.Fatalf("dropped = %d, want 0", dropped)
	}
}

// TestEventRecorderReportsWriteFailure は記録先の書き込み失敗を
// 無言で握りつぶさないことを確認する
//
// docs/10_ログ仕様.md: 書き込み失敗は無言で握りつぶさず error として出力する。
func TestEventRecorderReportsWriteFailure(t *testing.T) {
	var mu sync.Mutex
	failures := 0
	recorder := newEventRecorder(func(orchestrator.OrchestratorEvent) error {
		return errRecordTest
	}, 4)
	recorder.OnFailure(func(orchestrator.OrchestratorEvent, error) {
		mu.Lock()
		failures++
		mu.Unlock()
	})
	t.Cleanup(recorder.Close)

	recorder.Record(orchestrator.OrchestratorEvent{SessionID: "fail"})
	recorder.Close()

	mu.Lock()
	defer mu.Unlock()
	if failures != 1 {
		t.Fatalf("failure callback called %d times, want 1", failures)
	}
}

// TestDependenciesShutdownFlushesEventRecorder はシャットダウン時に
// 記録キューを排出してから停止することを確認する
//
// docs/10_ログ仕様.md: 記録パスはイベントを欠落させない。プロセス終了時に
// キューへ残ったイベントを捨てると、停止直前の証跡が失われる。
func TestDependenciesShutdownFlushesEventRecorder(t *testing.T) {
	const total = 100

	// バッファを投入件数以上にして送信側をブロックさせない。こうすると
	// 大半がキューに残ったままシャットダウンを迎えるため、排出しない実装では
	// 確実に欠落する。
	sink := &recordingSink{delay: time.Millisecond}
	relay := &idleAwareEventListener{}
	relay.recorderOnce.Do(func() {
		relay.recorder = newEventRecorder(sink.Record, total*2)
	})
	for i := 0; i < total; i++ {
		relay.recorder.Record(orchestrator.OrchestratorEvent{SessionID: "shutdown", Seq: int64(i)})
	}

	deps := &Dependencies{eventRelay: relay}
	deps.Shutdown()

	if got := sink.count(); got != total {
		t.Fatalf("recorded %d events after shutdown, want %d (停止時にイベントが欠落した)", got, total)
	}
}

func TestDependenciesShutdownDrainsRecorderBeforeCanonicalStoreClose(t *testing.T) {
	store := &shutdownOrderEventStore{}
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	relay := &idleAwareEventListener{archive: archive}
	relay.recorderOnce.Do(func() {
		relay.recorder = newEventRecorder(relay.recordEventSync, 4)
	})
	relay.recorder.Record(orchestrator.OrchestratorEvent{Type: "shutdown.test", Timestamp: time.Now().UTC().Format(time.RFC3339Nano)})

	(&Dependencies{eventRelay: relay, canonicalEventStore: store}).Shutdown()

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.appends != 1 || !store.closed {
		t.Fatalf("appends=%d closed=%t, want one append before close", store.appends, store.closed)
	}
}

// TestEventRecorderPreservesOrder は記録順序が投入順と一致することを確認する
func TestEventRecorderPreservesOrder(t *testing.T) {
	sink := &recordingSink{}
	recorder := newEventRecorder(sink.Record, 2)
	t.Cleanup(recorder.Close)

	for i := 0; i < 50; i++ {
		recorder.Record(orchestrator.OrchestratorEvent{SessionID: "order", Content: string(rune('a' + i%26)), Seq: int64(i)})
	}
	recorder.Close()

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.received) != 50 {
		t.Fatalf("recorded %d events, want 50", len(sink.received))
	}
	for i, ev := range sink.received {
		if ev.Seq != int64(i) {
			t.Fatalf("received[%d].Seq = %d, want %d (順序が入れ替わった)", i, ev.Seq, i)
		}
	}
}
