package turntiming

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func smallMemoryRecorder(t *testing.T, batchCapacity, stampsPerTurn int) *BatchRecorder {
	t.Helper()
	recorder, err := NewBatchRecorderWithConfig(RecorderConfig{
		BatchCapacity: batchCapacity,
		StampsPerTurn: stampsPerTurn,
	})
	if err != nil {
		t.Fatal(err)
	}
	return recorder
}

func TestBatchRecorderRetainsRepeatedPointsAndCountsOverflow(t *testing.T) {
	recorder := smallMemoryRecorder(t, 1, 2)
	turn, err := recorder.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}

	if !turn.Mark(10, 1, 1, 100) || !turn.Mark(10, 1, 2, 100) {
		t.Fatal("repeated point attempts should both be recorded")
	}
	if turn.Mark(11, 1, 1, 100) {
		t.Fatal("stamp beyond fixed capacity should be dropped")
	}
	if !turn.FinishForeground() {
		t.Fatal("foreground completion should succeed")
	}
	if err := recorder.Freeze(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := recorder.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := snapshot.Turn(0)
	if !ok {
		t.Fatal("turn 0 missing from frozen snapshot")
	}
	if got.StampCount() != 2 || got.Dropped() != 1 {
		t.Fatalf("stamp_count=%d dropped=%d, want 2 and 1", got.StampCount(), got.Dropped())
	}
	first, _ := got.Stamp(0)
	second, _ := got.Stamp(1)
	if first.Point != 10 || first.Attempt != 1 || second.Point != 10 || second.Attempt != 2 {
		t.Fatalf("repeated attempts were not retained in insertion order: %#v %#v", first, second)
	}
	if first.OffsetNS > second.OffsetNS {
		t.Fatalf("offsets went backwards: %d then %d", first.OffsetNS, second.OffsetNS)
	}
	if snapshot.IsIncomplete() {
		t.Fatal("drained batch should be complete")
	}
}

func TestBatchTurnRecorderContextPropagation(t *testing.T) {
	recorder := smallMemoryRecorder(t, 1, 4)
	turn, err := recorder.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithBatchTurnRecorder(context.Background(), turn)
	got, ok := BatchTurnFromContext(ctx)
	if !ok {
		t.Fatal("batch turn was not present in context")
	}
	if !got.Mark(12, 1, 1, 3) || !turn.FinishForeground() {
		t.Fatal("context-propagated turn did not record")
	}
	if err := recorder.Freeze(); err != nil {
		t.Fatal(err)
	}
	if _, ok := BatchTurnFromContext(nil); ok {
		t.Fatal("nil context unexpectedly contained a batch turn")
	}
	if _, ok := BatchTurnFromContext(context.Background()); ok {
		t.Fatal("unmarked context unexpectedly contained a batch turn")
	}
}

func TestBatchTurnRecorderStoresBoundedIdentityAndExposesStableIndex(t *testing.T) {
	recorder := smallMemoryRecorder(t, 2, 8)
	if _, ok := (TurnRecorder{}).Index(); ok {
		t.Fatal("zero recorder unexpectedly has a batch index")
	}
	turn, err := recorder.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	if index, ok := turn.Index(); !ok || index != 0 {
		t.Fatalf("first turn index=(%d,%t), want (0,true)", index, ok)
	}
	if _, ok := (TurnRecorder{batch: recorder, index: 1}).Index(); ok {
		t.Fatal("unreserved slot unexpectedly has a valid index")
	}
	turnID, traceID := string(modulecore.NewTurnID()), string(modulecore.NewTraceID())
	if turn.SetIdentity("turn_"+string(make([]byte, 36)), traceID) {
		t.Fatal("malformed fixed-length TurnID was accepted")
	}
	if !turn.SetIdentity(turnID, traceID) {
		t.Fatalf("canonical IDs were not accepted: turn=%q trace=%q", turnID, traceID)
	}
	if turn.SetIdentity(turnID, traceID) {
		t.Fatal("turn identity was mutable after its first assignment")
	}
	if !turn.Mark(12, ComponentV2ViewerHandler, 1, 0) || !turn.FinishForeground() {
		t.Fatal("turn could not be recorded and completed")
	}
	if err := recorder.Freeze(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := recorder.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	turnSnapshot, ok := snapshot.Turn(0)
	if !ok {
		t.Fatal("turn snapshot is missing")
	}
	gotTurnID, gotTraceID, ok := turnSnapshot.Identity()
	if !ok || gotTurnID != turnID || gotTraceID != traceID {
		t.Fatalf("snapshot identity=(%q,%q,%t), want (%q,%q,true)", gotTurnID, gotTraceID, ok, turnID, traceID)
	}
	unreserved, ok := snapshot.Turn(1)
	if ok {
		t.Fatal("unreserved slot appeared in the frozen snapshot")
	}
	if _, _, ok := unreserved.Identity(); ok {
		t.Fatal("unreserved slot exposed identity metadata")
	}
}

func TestBatchRecorderRequiresDrainBeforeSnapshotAndFreezes(t *testing.T) {
	recorder := smallMemoryRecorder(t, 2, 8)
	turn, err := recorder.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.Snapshot(); !errors.Is(err, ErrNotFrozen) {
		t.Fatalf("snapshot before freeze error=%v, want ErrNotFrozen", err)
	}
	if err := recorder.Freeze(); !errors.Is(err, ErrNotDrained) {
		t.Fatalf("freeze before foreground completion error=%v, want ErrNotDrained", err)
	}
	child, registered := turn.RegisterChild()
	if !registered || !turn.FinishForeground() {
		t.Fatal("could not register child and finish foreground")
	}
	if err := recorder.Freeze(); !errors.Is(err, ErrNotDrained) {
		t.Fatalf("freeze with a live child error=%v, want ErrNotDrained", err)
	}
	if !turn.Mark(20, 2, 1, 7) {
		t.Fatal("registered child phase should still be recordable")
	}
	if !child.Complete() {
		t.Fatal("child completion should succeed")
	}
	if err := recorder.Freeze(); err != nil {
		t.Fatal(err)
	}
	if turn.Mark(21, 2, 1, 7) {
		t.Fatal("frozen recorder accepted a new stamp")
	}
	if _, ok := turn.RegisterChild(); ok || child.Complete() || turn.FinishForeground() {
		t.Fatal("frozen recorder accepted a lifecycle mutation")
	}
	if err := recorder.Freeze(); err != nil {
		t.Fatalf("repeated complete freeze should be idempotent: %v", err)
	}
}

func TestBatchRecorderAllowsLiveChildToRegisterSuccessorAfterForeground(t *testing.T) {
	recorder := smallMemoryRecorder(t, 1, 8)
	turn, err := recorder.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	child, registered := turn.RegisterChild()
	if !registered || !turn.FinishForeground() {
		t.Fatal("register initial child and finish foreground")
	}
	recorder.CloseAdmission()
	successor, ok := child.Handoff()
	if !ok {
		t.Fatal("live child could not hand off to its successor after foreground completion")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := recorder.WaitDrained(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait with a live successor error=%v, want deadline exceeded", err)
	}
	if !successor.Complete() {
		t.Fatal("complete successor child")
	}
	if err := recorder.WaitDrained(context.Background()); err != nil {
		t.Fatalf("wait for successor drain: %v", err)
	}
	if err := recorder.Freeze(); err != nil {
		t.Fatalf("freeze after successor completion: %v", err)
	}
	if _, ok := successor.RegisterSuccessor(); ok {
		t.Fatal("completed child registered work after true drain and freeze")
	}
	snapshot, err := recorder.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.IsIncomplete() {
		t.Fatal("completed successor handoff produced an incomplete batch")
	}
}

func TestBatchRecorderChildWorkOverflowAndDuplicateCompletionMarkIncomplete(t *testing.T) {
	recorder, err := NewBatchRecorderWithConfig(RecorderConfig{
		BatchCapacity: 1, StampsPerTurn: 8, ChildWorkCapacityPerTurn: 1, MemoryLimitBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := recorder.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	child, ok := turn.RegisterChild()
	if !ok {
		t.Fatal("register first child")
	}
	if _, ok := turn.RegisterChild(); ok {
		t.Fatal("registered child beyond fixed per-turn capacity")
	}
	if !turn.FinishForeground() || !child.Complete() {
		t.Fatal("finish foreground and first child")
	}
	if child.Complete() {
		t.Fatal("duplicate child completion changed the count")
	}
	recorder.CloseAdmission()
	if err := recorder.WaitDrained(context.Background()); err != nil {
		t.Fatalf("wait for registered child drain: %v", err)
	}
	if err := recorder.Freeze(); err != nil {
		t.Fatalf("freeze bounded incomplete capture: %v", err)
	}
	snapshot, err := recorder.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	turnSnapshot, ok := snapshot.Turn(0)
	if !ok || !snapshot.IsIncomplete() || !turnSnapshot.WorkTrackingIncomplete() {
		t.Fatalf("overflow or duplicate completion was not reported incomplete: ok=%t batch=%t turn=%t", ok, snapshot.IsIncomplete(), turnSnapshot.WorkTrackingIncomplete())
	}
}

func TestBatchRecorderMarksUntrackedDurableWorkIncomplete(t *testing.T) {
	recorder := smallMemoryRecorder(t, 1, 8)
	turn, err := recorder.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	if !turn.FinishForeground() {
		t.Fatal("finish foreground")
	}
	recorder.CloseAdmission()
	if !recorder.MarkWorkTrackingIncomplete() {
		t.Fatal("record untracked durable work")
	}
	if err := recorder.WaitDrained(context.Background()); err != nil {
		t.Fatalf("wait for registered work drain: %v", err)
	}
	if err := recorder.Freeze(); err != nil {
		t.Fatalf("freeze untracked work capture: %v", err)
	}
	snapshot, err := recorder.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	turnSnapshot, ok := snapshot.Turn(0)
	if !ok || !snapshot.IsIncomplete() || !turnSnapshot.WorkTrackingIncomplete() {
		t.Fatalf("untracked durable work was not reported incomplete: ok=%t batch=%t turn=%t", ok, snapshot.IsIncomplete(), turnSnapshot.WorkTrackingIncomplete())
	}
}

func TestBatchRecorderRecordsErrorAndExplicitMissingSpanEnd(t *testing.T) {
	recorder := smallMemoryRecorder(t, 1, 8)
	turn, err := recorder.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	failed, ok := turn.Begin(30, 3, 1, 9, Span{})
	if !ok {
		t.Fatal("could not begin error span")
	}
	if !turn.End(failed, ResultError) {
		t.Fatal("could not end error span")
	}
	missing, ok := turn.Begin(31, 3, 2, 9, Span{})
	if !ok {
		t.Fatal("could not begin missing span")
	}
	if missing.ID() == 0 {
		t.Fatal("span id must be nonzero")
	}
	if !turn.FinishForeground() {
		t.Fatal("foreground completion should succeed even with an error-path missing end")
	}
	if err := recorder.Freeze(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := recorder.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	turnSnapshot, _ := snapshot.Turn(0)
	if got := turnSnapshot.MissingSpanEnds(); got != 1 {
		t.Fatalf("missing span ends=%d, want 1", got)
	}
	var foundErrorEnd bool
	for i := uint32(0); i < turnSnapshot.StampCount(); i++ {
		stamp, _ := turnSnapshot.Stamp(i)
		if stamp.Phase == PhaseEnd && stamp.SpanID == failed.ID() && stamp.Result == uint8(ResultError) {
			foundErrorEnd = true
		}
	}
	if !foundErrorEnd {
		t.Fatal("error result was not retained on the span end stamp")
	}
}

func TestBatchRecorderPreservesSpanParentageAndRejectsDuplicateEnd(t *testing.T) {
	recorder := smallMemoryRecorder(t, 1, 8)
	turn, err := recorder.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	parent, ok := turn.Begin(32, 3, 1, 9, Span{})
	if !ok {
		t.Fatal("could not begin root span")
	}
	child, ok := turn.Begin(33, 3, 2, 9, parent)
	if !ok {
		t.Fatal("could not begin child span")
	}
	if !turn.End(child, ResultOK) || !turn.End(parent, ResultCanceled) {
		t.Fatal("could not end spans")
	}
	if turn.End(parent, ResultOK) {
		t.Fatal("duplicate span end should be rejected")
	}
	if !turn.FinishForeground() {
		t.Fatal("foreground completion should succeed")
	}
	if err := recorder.Freeze(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := recorder.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	turnSnapshot, _ := snapshot.Turn(0)
	if turnSnapshot.MissingSpanEnds() != 0 {
		t.Fatalf("missing span ends=%d, want 0", turnSnapshot.MissingSpanEnds())
	}
	var childBegin, parentEnd bool
	for index := uint32(0); index < turnSnapshot.StampCount(); index++ {
		stamp, _ := turnSnapshot.Stamp(index)
		switch {
		case stamp.Phase == PhaseBegin && stamp.SpanID == child.ID():
			childBegin = stamp.ParentID == parent.ID()
		case stamp.Phase == PhaseEnd && stamp.SpanID == parent.ID():
			parentEnd = stamp.Result == uint8(ResultCanceled)
		}
	}
	if !childBegin || !parentEnd {
		t.Fatalf("parentage or terminal result missing: child=%t parent_end=%t", childBegin, parentEnd)
	}
}

func TestBatchRecorderOverflowDoesNotOverwriteAndTurnCapacityIsFixed(t *testing.T) {
	recorder := smallMemoryRecorder(t, 1, 1)
	turn, err := recorder.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.BeginTurn(); !errors.Is(err, ErrTurnCapacity) {
		t.Fatalf("second turn error=%v, want ErrTurnCapacity", err)
	}
	if !turn.Mark(40, 4, 1, 1) || turn.Mark(41, 4, 1, 1) {
		t.Fatal("turn stamp capacity was not enforced")
	}
	if !turn.FinishForeground() {
		t.Fatal("foreground completion should succeed")
	}
	if err := recorder.Freeze(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := recorder.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	turnSnapshot, _ := snapshot.Turn(0)
	stamp, _ := turnSnapshot.Stamp(0)
	if stamp.Point != 40 || turnSnapshot.Dropped() != 1 || snapshot.TurnCount() != 1 {
		t.Fatalf("overflow overwrote or expanded fixed storage: stamp=%#v dropped=%d turns=%d", stamp, turnSnapshot.Dropped(), snapshot.TurnCount())
	}
}

func TestBatchRecorderCanFreezeIncompleteForTimeoutRecovery(t *testing.T) {
	recorder := smallMemoryRecorder(t, 1, 4)
	turn, err := recorder.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	if !turn.Mark(50, 5, 1, 2) {
		t.Fatal("mark failed")
	}
	if err := recorder.FreezeIncomplete(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := recorder.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.IsIncomplete() {
		t.Fatal("timeout recovery snapshot must be explicitly incomplete")
	}
	if turn.Mark(51, 5, 1, 2) {
		t.Fatal("incomplete frozen recorder accepted a new stamp")
	}
}

func TestBatchRecorderRejectsConfigurationAboveMemoryLimitBeforeAllocation(t *testing.T) {
	_, err := NewBatchRecorderWithConfig(RecorderConfig{
		BatchCapacity:    128,
		StampsPerTurn:    4096,
		MemoryLimitBytes: 1,
	})
	if !errors.Is(err, ErrMemoryLimit) {
		t.Fatalf("constructor error=%v, want ErrMemoryLimit", err)
	}
	_, err = NewBatchRecorderWithConfig(RecorderConfig{
		BatchCapacity:    128,
		StampsPerTurn:    4096,
		MemoryLimitBytes: DefaultMemoryLimitBytes + 1,
	})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("constructor error=%v, want ErrInvalidConfig for limit above hard maximum", err)
	}
}

func TestBatchRecorderDefaultsAreFixedAndWithinRAMLimit(t *testing.T) {
	defaults := DefaultRecorderConfig()
	if defaults.BatchCapacity != 128 || defaults.StampsPerTurn != 4096 || defaults.ChildWorkCapacityPerTurn != 128 || defaults.MemoryLimitBytes != 32<<20 {
		t.Fatalf("unexpected defaults: %#v", defaults)
	}
	recorder, err := NewBatchRecorder()
	if err != nil {
		t.Fatal(err)
	}
	if recorder.ReservedBytes() > DefaultMemoryLimitBytes {
		t.Fatalf("reserved_bytes=%d exceeds limit=%d", recorder.ReservedBytes(), DefaultMemoryLimitBytes)
	}
	for i := 0; i < DefaultBatchCapacity; i++ {
		if _, err := recorder.BeginTurn(); err != nil {
			t.Fatalf("turn %d rejected before configured capacity: %v", i, err)
		}
	}
	if _, err := recorder.BeginTurn(); !errors.Is(err, ErrTurnCapacity) {
		t.Fatalf("turn beyond default capacity error=%v, want ErrTurnCapacity", err)
	}
}

func TestBatchRecorderConcurrentMarksHaveOrderedOffsets(t *testing.T) {
	recorder := smallMemoryRecorder(t, 1, 2048)
	turn, err := recorder.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	const iterations = 50
	var workersDone sync.WaitGroup
	workersDone.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func(worker int) {
			defer workersDone.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				if !turn.Mark(60, uint16(worker), uint16(iteration), uint32(worker)) {
					t.Errorf("worker %d mark %d was dropped", worker, iteration)
					return
				}
				span, ok := turn.Begin(61, uint16(worker), uint16(iteration), uint32(worker), Span{})
				if !ok || !turn.End(span, ResultOK) {
					t.Errorf("worker %d span %d failed", worker, iteration)
					return
				}
			}
		}(worker)
	}
	workersDone.Wait()
	if !turn.FinishForeground() {
		t.Fatal("foreground completion should succeed")
	}
	if err := recorder.Freeze(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := recorder.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	turnSnapshot, _ := snapshot.Turn(0)
	wantStamps := uint32(workers * iterations * 3)
	if turnSnapshot.StampCount() != wantStamps {
		t.Fatalf("stamp count=%d want=%d", turnSnapshot.StampCount(), wantStamps)
	}
	var previous int64
	for index := uint32(0); index < turnSnapshot.StampCount(); index++ {
		stamp, _ := turnSnapshot.Stamp(index)
		if stamp.OffsetNS < previous {
			t.Fatalf("stamp offsets went backwards at index %d: %d < %d", index, stamp.OffsetNS, previous)
		}
		previous = stamp.OffsetNS
	}
	if missing := turnSnapshot.MissingSpanEnds(); missing != 0 {
		t.Fatalf("missing span ends=%d want=0", missing)
	}
}

func TestBatchRecorderMarkAllocatesNothing(t *testing.T) {
	recorder := smallMemoryRecorder(t, 1, 4096)
	turn, err := recorder.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(1000, func() {
		turn.Mark(70, 7, 1, 3)
		span, ok := turn.Begin(71, 7, 1, 3, Span{})
		if !ok || !turn.End(span, ResultOK) {
			panic("span recording unexpectedly failed")
		}
	})
	if allocs != 0 {
		t.Fatalf("Mark/Begin/End allocations/op=%v want=0", allocs)
	}
}

func BenchmarkBatchRecorderMark(b *testing.B) {
	const capacity = 4096
	newTurn := func() TurnRecorder {
		recorder := smallBenchmarkRecorder(b, capacity)
		turn, err := recorder.BeginTurn()
		if err != nil {
			b.Fatal(err)
		}
		return turn
	}
	turn := newTurn()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i != 0 && i%capacity == 0 {
			b.StopTimer()
			turn = newTurn()
			b.StartTimer()
		}
		turn.Mark(80, 8, 1, uint32(i))
	}
}

func smallBenchmarkRecorder(b *testing.B, stampsPerTurn int) *BatchRecorder {
	b.Helper()
	recorder, err := NewBatchRecorderWithConfig(RecorderConfig{
		BatchCapacity: 1,
		StampsPerTurn: stampsPerTurn,
	})
	if err != nil {
		b.Fatal(err)
	}
	return recorder
}
