package turntiming

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"time"
	"unsafe"
)

const (
	DefaultBatchCapacity            = 128
	DefaultStampsPerTurn            = 4096
	DefaultChildWorkCapacityPerTurn = 128
	DefaultMemoryLimitBytes         = 32 << 20
	maxMemoryLimitBytes             = DefaultMemoryLimitBytes
	allocatorHeadroomBytes          = 64 << 10
)

const (
	PhasePoint uint8 = iota
	PhaseBegin
	PhaseEnd
)

type Result uint8

const (
	ResultOK Result = iota
	ResultError
	ResultCanceled
	ResultTimeout
)

var (
	ErrInvalidConfig   = errors.New("turntiming: invalid recorder config")
	ErrMemoryLimit     = errors.New("turntiming: recorder exceeds memory limit")
	ErrTurnCapacity    = errors.New("turntiming: batch turn capacity reached")
	ErrAdmissionClosed = errors.New("turntiming: batch admission is closed")
	ErrAdmissionOpen   = errors.New("turntiming: batch admission must be closed before waiting for drain")
	ErrNotDrained      = errors.New("turntiming: batch is not drained")
	ErrNotFrozen       = errors.New("turntiming: batch is not frozen")
	ErrAlreadyFrozen   = errors.New("turntiming: batch is already frozen")
)

// Stamp is the compact, numeric-only row stored by the diagnostic recorder.
// Its field order matches the response-first diagnostic contract.
type Stamp struct {
	OffsetNS  int64
	SpanID    uint32
	ParentID  uint32
	Point     uint16
	Phase     uint8
	Result    uint8
	Component uint16
	Attempt   uint16
	Ref       uint32
}

// RecorderConfig sets the fixed batch and per-turn capacities. Zero fields
// select the corresponding defaults. MemoryLimitBytes cannot exceed 32 MiB.
type RecorderConfig struct {
	BatchCapacity            int
	StampsPerTurn            int
	ChildWorkCapacityPerTurn int
	MemoryLimitBytes         uint64
}

// DefaultRecorderConfig returns the response-first diagnostic defaults.
func DefaultRecorderConfig() RecorderConfig {
	return RecorderConfig{
		BatchCapacity:            DefaultBatchCapacity,
		StampsPerTurn:            DefaultStampsPerTurn,
		ChildWorkCapacityPerTurn: DefaultChildWorkCapacityPerTurn,
		MemoryLimitBytes:         DefaultMemoryLimitBytes,
	}
}

// processOrigin preserves Go's monotonic clock component and is shared by all
// batches in this process. Offsets are process-local and are not wall time.
var processOrigin = time.Now()

type turnState struct {
	stampCount      uint32
	dropped         uint32
	nextSpanID      uint32
	pendingChildren uint32
	foregroundDone  bool
	identitySet     bool
	workIncomplete  bool
	turnID          [41]byte
	traceID         [40]byte
}

type childWorkSlot struct {
	generation uint32
	active     bool
}

// BatchRecorder owns preallocated stamp, span-pair, and child-work arrays for
// one bounded diagnostic batch. It performs no I/O and starts no goroutines.
type BatchRecorder struct {
	mu                sync.Mutex
	turnCapacity      uint32
	stampsPerTurn     uint32
	childWorkCapacity uint32
	turns             []turnState
	stamps            []Stamp
	spanEnded         []uint8
	childWorks        []childWorkSlot
	turnCount         uint32
	admissionClosed   bool
	frozen            bool
	complete          bool
	incomplete        bool
	drained           chan struct{}
	drainedSet        bool
	reservedBytes     uint64
}

// TurnRecorder is a value handle for one preallocated turn slot.
type TurnRecorder struct {
	batch *BatchRecorder
	index uint32
}

// ChildWork is a generation-checked handle for one registered post-processing
// operation. Copying the handle does not create another registration.
type ChildWork struct {
	batch      *BatchRecorder
	turnIndex  uint32
	slotIndex  uint32
	generation uint32
}

type batchTurnContextKey struct{}

// Span is a value handle to a recorded begin stamp. The fields are private so
// callers cannot forge an end pair.
type Span struct {
	batch      *BatchRecorder
	turnIndex  uint32
	stampIndex uint32
	id         uint32
}

// ID returns the numeric span identifier, or zero for an invalid span.
func (s Span) ID() uint32 { return s.id }

// WithBatchTurnRecorder carries a turn handle through async request contexts.
// It does not replace the legacy Recorder context value.
func WithBatchTurnRecorder(ctx context.Context, turn TurnRecorder) context.Context {
	if ctx == nil || turn.batch == nil {
		return ctx
	}
	return context.WithValue(ctx, batchTurnContextKey{}, turn)
}

// BatchTurnFromContext retrieves the response-first turn handle, if present.
func BatchTurnFromContext(ctx context.Context) (TurnRecorder, bool) {
	if ctx == nil {
		return TurnRecorder{}, false
	}
	turn, ok := ctx.Value(batchTurnContextKey{}).(TurnRecorder)
	return turn, ok && turn.batch != nil
}

// BatchSnapshot is a read-only view over a frozen batch. Its stamp accessors
// return copies of individual rows and do not copy the backing arrays.
type BatchSnapshot struct {
	batch      *BatchRecorder
	turnCount  uint32
	incomplete bool
}

// TurnSnapshot is a read-only view over one frozen turn.
type TurnSnapshot struct {
	batch *BatchRecorder
	index uint32
}

// NewBatchRecorder creates a recorder with the fixed response-first defaults.
func NewBatchRecorder() (*BatchRecorder, error) {
	return NewBatchRecorderWithConfig(DefaultRecorderConfig())
}

// NewBatchRecorderWithConfig validates the complete fixed allocation before
// allocating any arrays. The estimate includes recorder/turn metadata and a
// conservative allocator headroom reserve.
func NewBatchRecorderWithConfig(config RecorderConfig) (*BatchRecorder, error) {
	defaults := DefaultRecorderConfig()
	if config.BatchCapacity == 0 {
		config.BatchCapacity = defaults.BatchCapacity
	}
	if config.StampsPerTurn == 0 {
		config.StampsPerTurn = defaults.StampsPerTurn
	}
	if config.ChildWorkCapacityPerTurn == 0 {
		config.ChildWorkCapacityPerTurn = defaults.ChildWorkCapacityPerTurn
	}
	if config.MemoryLimitBytes == 0 {
		config.MemoryLimitBytes = defaults.MemoryLimitBytes
	}
	if config.BatchCapacity < 1 || config.StampsPerTurn < 1 || config.ChildWorkCapacityPerTurn < 1 || config.MemoryLimitBytes == 0 || config.MemoryLimitBytes > maxMemoryLimitBytes {
		return nil, ErrInvalidConfig
	}
	if uint64(config.BatchCapacity) > math.MaxUint32 || uint64(config.StampsPerTurn) > math.MaxUint32 || uint64(config.ChildWorkCapacityPerTurn) > math.MaxUint32 {
		return nil, ErrInvalidConfig
	}

	reservedBytes, slots, childSlots, ok := recorderMemoryEstimate(config.BatchCapacity, config.StampsPerTurn, config.ChildWorkCapacityPerTurn)
	if !ok {
		return nil, ErrInvalidConfig
	}
	if reservedBytes > config.MemoryLimitBytes {
		return nil, ErrMemoryLimit
	}
	maxInt := uint64(^uint(0) >> 1)
	if slots > maxInt || childSlots > maxInt {
		return nil, ErrInvalidConfig
	}

	turnCapacity := uint32(config.BatchCapacity)
	stampsPerTurn := uint32(config.StampsPerTurn)
	childWorkCapacity := uint32(config.ChildWorkCapacityPerTurn)
	return &BatchRecorder{
		turnCapacity:      turnCapacity,
		stampsPerTurn:     stampsPerTurn,
		childWorkCapacity: childWorkCapacity,
		turns:             make([]turnState, config.BatchCapacity),
		stamps:            make([]Stamp, int(slots)),
		spanEnded:         make([]uint8, int(slots)),
		childWorks:        make([]childWorkSlot, int(childSlots)),
		drained:           make(chan struct{}),
		reservedBytes:     reservedBytes,
	}, nil
}

func recorderMemoryEstimate(turnCapacity, stampsPerTurn, childWorkCapacityPerTurn int) (uint64, uint64, uint64, bool) {
	const maxUint64 = ^uint64(0)
	turns := uint64(turnCapacity)
	perTurn := uint64(stampsPerTurn)
	childrenPerTurn := uint64(childWorkCapacityPerTurn)
	if perTurn != 0 && turns > maxUint64/perTurn {
		return 0, 0, 0, false
	}
	slots := turns * perTurn
	if childrenPerTurn != 0 && turns > maxUint64/childrenPerTurn {
		return 0, 0, 0, false
	}
	childSlots := turns * childrenPerTurn
	stampSize := uint64(unsafe.Sizeof(Stamp{}))
	if stampSize != 0 && slots > maxUint64/stampSize {
		return 0, 0, 0, false
	}
	stampBytes := slots * stampSize
	childWorkSize := uint64(unsafe.Sizeof(childWorkSlot{}))
	if childWorkSize != 0 && childSlots > maxUint64/childWorkSize {
		return 0, 0, 0, false
	}
	childWorkBytes := childSlots * childWorkSize
	if turns > maxUint64/uint64(unsafe.Sizeof(turnState{})) {
		return 0, 0, 0, false
	}
	turnBytes := turns * uint64(unsafe.Sizeof(turnState{}))
	metadataBase := uint64(unsafe.Sizeof(BatchRecorder{})) + uint64(unsafe.Sizeof(processOrigin))
	if turnBytes > maxUint64-metadataBase {
		return 0, 0, 0, false
	}
	metadataBytes := metadataBase + turnBytes
	if allocatorHeadroomBytes > maxUint64-metadataBytes {
		return 0, 0, 0, false
	}
	totalBytes := metadataBytes + allocatorHeadroomBytes
	for _, bytes := range []uint64{stampBytes, slots, childWorkBytes} {
		if bytes > maxUint64-totalBytes {
			return 0, 0, 0, false
		}
		totalBytes += bytes
	}
	return totalBytes, slots, childSlots, true
}

// ReservedBytes reports the conservative fixed-allocation estimate checked by
// the constructor. It includes allocator headroom and all recorder metadata.
func (b *BatchRecorder) ReservedBytes() uint64 {
	if b == nil {
		return 0
	}
	return b.reservedBytes
}

// CloseAdmission prevents new turns and root children from being registered.
// Existing child handles may still register successors until they complete.
func (b *BatchRecorder) CloseAdmission() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if !b.frozen {
		b.admissionClosed = true
		b.signalDrainedLocked()
	}
	b.mu.Unlock()
}

// MarkWorkTrackingIncomplete records durable work that has not been associated
// with a turn child handle. A drained snapshot will retain this incomplete bit.
func (b *BatchRecorder) MarkWorkTrackingIncomplete() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.frozen {
		return false
	}
	b.incomplete = true
	for index := uint32(0); index < b.turnCount; index++ {
		b.turns[index].workIncomplete = true
	}
	return true
}

// WaitDrained waits on recorder state transitions after admission is closed.
// It does not poll a durable store or use timed sleeps as evidence of drain.
func (b *BatchRecorder) WaitDrained(ctx context.Context) error {
	if b == nil {
		return ErrInvalidConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b.mu.Lock()
	if b.frozen {
		if b.complete {
			b.mu.Unlock()
			return nil
		}
		b.mu.Unlock()
		return ErrAlreadyFrozen
	}
	if !b.admissionClosed {
		b.mu.Unlock()
		return ErrAdmissionOpen
	}
	b.signalDrainedLocked()
	drained := b.drained
	b.mu.Unlock()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *BatchRecorder) allTurnsDrainedLocked() bool {
	for index := uint32(0); index < b.turnCount; index++ {
		state := &b.turns[index]
		if !state.foregroundDone || state.pendingChildren != 0 {
			return false
		}
	}
	return true
}

func (b *BatchRecorder) signalDrainedLocked() {
	if !b.admissionClosed || b.drainedSet || !b.allTurnsDrainedLocked() || b.drained == nil {
		return
	}
	close(b.drained)
	b.drainedSet = true
}

// BeginTurn reserves the next fixed turn slot. Slots are never recycled within
// a batch, which preserves the batch's bounded insertion history.
func (b *BatchRecorder) BeginTurn() (TurnRecorder, error) {
	if b == nil {
		return TurnRecorder{}, ErrInvalidConfig
	}
	b.mu.Lock()
	if b.frozen {
		b.mu.Unlock()
		return TurnRecorder{}, ErrAlreadyFrozen
	}
	if b.admissionClosed {
		b.mu.Unlock()
		return TurnRecorder{}, ErrAdmissionClosed
	}
	if b.turnCount >= b.turnCapacity {
		b.mu.Unlock()
		return TurnRecorder{}, ErrTurnCapacity
	}
	index := b.turnCount
	b.turnCount++
	b.mu.Unlock()
	return TurnRecorder{batch: b, index: index}, nil
}

// Mark appends a point stamp. Repeated points and attempts are retained.
// False means the fixed buffer is full or the batch has already frozen.
func (t TurnRecorder) Mark(point, component, attempt uint16, ref uint32) bool {
	return t.MarkResult(point, component, attempt, ref, ResultOK)
}

// Index returns the fixed batch slot for a recorder handle. The bool is false
// for a zero or forged handle that does not refer to a reserved turn.
func (t TurnRecorder) Index() (uint32, bool) {
	b := t.batch
	if b == nil {
		return 0, false
	}
	b.mu.Lock()
	valid := t.index < b.turnCount
	b.mu.Unlock()
	return t.index, valid
}

// SetIdentity stores the accepted canonical TurnID and TraceID in fixed-size
// metadata attached to this slot. It copies at most the canonical ID lengths
// and accepts one identity per turn.
func (t TurnRecorder) SetIdentity(turnID, traceID string) bool {
	if !validCanonicalFixedID(turnID, "turn_") || !validCanonicalFixedID(traceID, "trc_") {
		return false
	}
	b := t.batch
	if b == nil {
		return false
	}
	b.mu.Lock()
	if b.frozen || t.index >= b.turnCount {
		b.mu.Unlock()
		return false
	}
	state := &b.turns[t.index]
	if state.identitySet {
		b.mu.Unlock()
		return false
	}
	copy(state.turnID[:], turnID)
	copy(state.traceID[:], traceID)
	state.identitySet = true
	b.mu.Unlock()
	return true
}

func validCanonicalFixedID(value, prefix string) bool {
	if len(prefix) != 4 && len(prefix) != 5 || len(value) != len(prefix)+36 || !strings.HasPrefix(value, prefix) {
		return false
	}
	const uuidHyphens = "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
	for index := 0; index < 36; index++ {
		character := value[len(prefix)+index]
		if uuidHyphens[index] == '-' {
			if character != '-' {
				return false
			}
			continue
		}
		if !isLowerHex(character) {
			return false
		}
	}
	version := value[len(prefix)+14]
	if version != '5' && version != '7' {
		return false
	}
	variant := value[len(prefix)+19]
	return variant == '8' || variant == '9' || variant == 'a' || variant == 'b'
}

func isLowerHex(character byte) bool {
	return character >= '0' && character <= '9' || character >= 'a' && character <= 'f'
}

// MarkResult appends a point stamp carrying a terminal result when relevant.
func (t TurnRecorder) MarkResult(point, component, attempt uint16, ref uint32, result Result) bool {
	b := t.batch
	if b == nil || result > ResultTimeout {
		return false
	}
	b.mu.Lock()
	if b.frozen || t.index >= b.turnCount {
		b.mu.Unlock()
		return false
	}
	state := &b.turns[t.index]
	stamp := Stamp{
		Point:     point,
		Phase:     PhasePoint,
		Result:    uint8(result),
		Component: component,
		Attempt:   attempt,
		Ref:       ref,
	}
	ok := b.appendLocked(t.index, state, stamp)
	b.mu.Unlock()
	return ok
}

// Begin appends a begin stamp and returns its span handle. A zero Span is a
// root span; a nonzero parent must belong to this turn and batch.
func (t TurnRecorder) Begin(point, component, attempt uint16, ref uint32, parent Span) (Span, bool) {
	b := t.batch
	if b == nil {
		return Span{}, false
	}
	b.mu.Lock()
	if b.frozen || t.index >= b.turnCount {
		b.mu.Unlock()
		return Span{}, false
	}
	state := &b.turns[t.index]
	parentID := uint32(0)
	if parent.id != 0 {
		if parent.batch != b || parent.turnIndex != t.index || parent.stampIndex >= state.stampCount {
			incrementDropped(state)
			b.mu.Unlock()
			return Span{}, false
		}
		parentStamp := b.stamps[b.stampOffset(t.index, parent.stampIndex)]
		if parentStamp.Phase != PhaseBegin || parentStamp.SpanID != parent.id {
			incrementDropped(state)
			b.mu.Unlock()
			return Span{}, false
		}
		parentID = parent.id
	}
	if state.nextSpanID == math.MaxUint32 {
		incrementDropped(state)
		b.mu.Unlock()
		return Span{}, false
	}
	if state.stampCount >= b.stampsPerTurn {
		incrementDropped(state)
		b.mu.Unlock()
		return Span{}, false
	}
	spanID := state.nextSpanID + 1
	stampIndex := state.stampCount
	stamp := Stamp{
		SpanID:    spanID,
		ParentID:  parentID,
		Point:     point,
		Phase:     PhaseBegin,
		Component: component,
		Attempt:   attempt,
		Ref:       ref,
	}
	if !b.appendLocked(t.index, state, stamp) {
		b.mu.Unlock()
		return Span{}, false
	}
	state.nextSpanID = spanID
	span := Span{batch: b, turnIndex: t.index, stampIndex: stampIndex, id: spanID}
	b.mu.Unlock()
	return span, true
}

// End appends the end stamp for a Begin handle. Duplicate, foreign, malformed,
// or invalid-result ends are rejected without changing the recorded pair.
func (t TurnRecorder) End(span Span, result Result) bool {
	b := t.batch
	if b == nil || result > ResultTimeout || span.id == 0 || span.batch != b || span.turnIndex != t.index {
		return false
	}
	b.mu.Lock()
	if b.frozen || t.index >= b.turnCount {
		b.mu.Unlock()
		return false
	}
	state := &b.turns[t.index]
	if span.stampIndex >= state.stampCount {
		b.mu.Unlock()
		return false
	}
	beginOffset := b.stampOffset(t.index, span.stampIndex)
	begin := b.stamps[beginOffset]
	if begin.Phase != PhaseBegin || begin.SpanID != span.id || b.spanEnded[beginOffset] != 0 {
		b.mu.Unlock()
		return false
	}
	stamp := Stamp{
		SpanID:    span.id,
		ParentID:  begin.ParentID,
		Point:     begin.Point,
		Phase:     PhaseEnd,
		Result:    uint8(result),
		Component: begin.Component,
		Attempt:   begin.Attempt,
		Ref:       begin.Ref,
	}
	if !b.appendLocked(t.index, state, stamp) {
		b.mu.Unlock()
		return false
	}
	b.spanEnded[beginOffset] = 1
	b.mu.Unlock()
	return true
}

// RegisterChild reserves one fixed-capacity child-work slot. Root children are
// registered by the foreground producer before FinishForeground.
func (t TurnRecorder) RegisterChild() (ChildWork, bool) {
	b := t.batch
	if b == nil {
		return ChildWork{}, false
	}
	b.mu.Lock()
	if b.frozen {
		b.mu.Unlock()
		return ChildWork{}, false
	}
	if t.index >= b.turnCount {
		b.incomplete = true
		b.mu.Unlock()
		return ChildWork{}, false
	}
	state := &b.turns[t.index]
	if b.admissionClosed || state.foregroundDone || state.pendingChildren == math.MaxUint32 {
		b.markWorkIncompleteLocked(t.index)
		b.mu.Unlock()
		return ChildWork{}, false
	}
	child, ok := b.allocateChildWorkLocked(t.index, state)
	if !ok {
		b.markWorkIncompleteLocked(t.index)
	}
	b.mu.Unlock()
	return child, ok
}

// RegisterSuccessor adds another child while this child remains live. It is
// permitted after foreground completion and after admission has closed.
func (c ChildWork) RegisterSuccessor() (ChildWork, bool) {
	b := c.batch
	if b == nil {
		return ChildWork{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	state, slot := b.childWorkStateLocked(c)
	if state == nil || slot == nil {
		b.markChildWorkInvalidLocked(c.turnIndex)
		return ChildWork{}, false
	}
	if state.pendingChildren == math.MaxUint32 {
		b.markWorkIncompleteLocked(c.turnIndex)
		return ChildWork{}, false
	}
	successor, ok := b.allocateChildWorkLocked(c.turnIndex, state)
	if !ok {
		b.markWorkIncompleteLocked(c.turnIndex)
	}
	return successor, ok
}

// Handoff atomically transfers this live registration to one successor. The
// pending count remains nonzero across foreground completion and drain wait.
func (c ChildWork) Handoff() (ChildWork, bool) {
	b := c.batch
	if b == nil {
		return ChildWork{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, slot := b.childWorkStateLocked(c)
	if slot == nil {
		b.markChildWorkInvalidLocked(c.turnIndex)
		return ChildWork{}, false
	}
	if slot.generation == math.MaxUint32 {
		b.markWorkIncompleteLocked(c.turnIndex)
		return ChildWork{}, false
	}
	slot.generation++
	return ChildWork{batch: b, turnIndex: c.turnIndex, slotIndex: c.slotIndex, generation: slot.generation}, true
}

// Complete releases this exact child registration once. Duplicate or stale
// completion cannot decrement another child's count and marks the capture
// incomplete so the recorder never presents an ambiguous drain as complete.
func (c ChildWork) Complete() bool {
	b := c.batch
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	state, slot := b.childWorkStateLocked(c)
	if state == nil || slot == nil || state.pendingChildren == 0 {
		b.markChildWorkInvalidLocked(c.turnIndex)
		return false
	}
	slot.active = false
	state.pendingChildren--
	b.signalDrainedLocked()
	return true
}

func (b *BatchRecorder) allocateChildWorkLocked(turnIndex uint32, state *turnState) (ChildWork, bool) {
	if state.pendingChildren == math.MaxUint32 {
		return ChildWork{}, false
	}
	start := int(turnIndex) * int(b.childWorkCapacity)
	for offset := uint32(0); offset < b.childWorkCapacity; offset++ {
		slotIndex := offset
		slot := &b.childWorks[start+int(slotIndex)]
		if slot.active || slot.generation == math.MaxUint32 {
			continue
		}
		slot.generation++
		slot.active = true
		state.pendingChildren++
		return ChildWork{batch: b, turnIndex: turnIndex, slotIndex: slotIndex, generation: slot.generation}, true
	}
	return ChildWork{}, false
}

func (b *BatchRecorder) childWorkStateLocked(c ChildWork) (*turnState, *childWorkSlot) {
	if b.frozen || c.turnIndex >= b.turnCount || c.slotIndex >= b.childWorkCapacity {
		return nil, nil
	}
	state := &b.turns[c.turnIndex]
	offset := int(c.turnIndex)*int(b.childWorkCapacity) + int(c.slotIndex)
	slot := &b.childWorks[offset]
	if !slot.active || slot.generation != c.generation {
		return nil, nil
	}
	return state, slot
}

func (b *BatchRecorder) markChildWorkInvalidLocked(turnIndex uint32) {
	if b.frozen {
		return
	}
	if turnIndex < b.turnCount {
		b.markWorkIncompleteLocked(turnIndex)
		return
	}
	b.incomplete = true
}

func (b *BatchRecorder) markWorkIncompleteLocked(turnIndex uint32) {
	b.incomplete = true
	if turnIndex < b.turnCount {
		b.turns[turnIndex].workIncomplete = true
	}
}

// MarkWorkTrackingIncomplete is used when durable work exists but cannot be
// tied to a child handle in this diagnostic batch.
func (t TurnRecorder) MarkWorkTrackingIncomplete() bool {
	b := t.batch
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.frozen || t.index >= b.turnCount {
		return false
	}
	b.markWorkIncompleteLocked(t.index)
	return true
}

// FinishForeground marks the foreground turn complete. It may be called once;
// registered child handles can still record and hand off until completion.
func (t TurnRecorder) FinishForeground() bool {
	b := t.batch
	if b == nil {
		return false
	}
	b.mu.Lock()
	if b.frozen || t.index >= b.turnCount {
		b.mu.Unlock()
		return false
	}
	state := &b.turns[t.index]
	if state.foregroundDone {
		b.mu.Unlock()
		return false
	}
	state.foregroundDone = true
	b.signalDrainedLocked()
	b.mu.Unlock()
	return true
}

func (b *BatchRecorder) appendLocked(turnIndex uint32, state *turnState, stamp Stamp) bool {
	if state.stampCount >= b.stampsPerTurn {
		incrementDropped(state)
		return false
	}
	stamp.OffsetNS = time.Since(processOrigin).Nanoseconds()
	offset := b.stampOffset(turnIndex, state.stampCount)
	b.stamps[offset] = stamp
	state.stampCount++
	return true
}

func (b *BatchRecorder) stampOffset(turnIndex, stampIndex uint32) int {
	return int(turnIndex)*int(b.stampsPerTurn) + int(stampIndex)
}

func incrementDropped(state *turnState) {
	if state.dropped != math.MaxUint32 {
		state.dropped++
	}
}

// Freeze seals a fully drained batch. It is idempotent after a successful
// complete freeze; a batch with unfinished turns or children remains writable.
func (b *BatchRecorder) Freeze() error {
	if b == nil {
		return ErrInvalidConfig
	}
	b.mu.Lock()
	if b.frozen {
		if b.complete {
			b.mu.Unlock()
			return nil
		}
		b.mu.Unlock()
		return ErrAlreadyFrozen
	}
	if !b.allTurnsDrainedLocked() {
		b.mu.Unlock()
		return ErrNotDrained
	}
	b.admissionClosed = true
	b.frozen = true
	b.complete = !b.incomplete
	b.signalDrainedLocked()
	b.mu.Unlock()
	return nil
}

// FreezeIncomplete seals a timed-out or otherwise incomplete batch so it can
// be exported with an explicit incomplete marker.
func (b *BatchRecorder) FreezeIncomplete() error {
	if b == nil {
		return ErrInvalidConfig
	}
	b.mu.Lock()
	if b.frozen {
		b.mu.Unlock()
		return ErrAlreadyFrozen
	}
	b.admissionClosed = true
	b.frozen = true
	b.incomplete = true
	b.complete = false
	b.mu.Unlock()
	return nil
}

// Snapshot returns a view only after Freeze or FreezeIncomplete succeeds.
func (b *BatchRecorder) Snapshot() (BatchSnapshot, error) {
	if b == nil {
		return BatchSnapshot{}, ErrInvalidConfig
	}
	b.mu.Lock()
	if !b.frozen {
		b.mu.Unlock()
		return BatchSnapshot{}, ErrNotFrozen
	}
	snapshot := BatchSnapshot{batch: b, turnCount: b.turnCount, incomplete: !b.complete}
	b.mu.Unlock()
	return snapshot, nil
}

// TurnCount returns the number of turns reserved in the batch.
func (s BatchSnapshot) TurnCount() uint32 { return s.turnCount }

// IsIncomplete distinguishes timeout/failure recovery from a drained batch.
func (s BatchSnapshot) IsIncomplete() bool { return s.incomplete }

// Turn returns a view of a frozen turn by its zero-based batch index.
func (s BatchSnapshot) Turn(index uint32) (TurnSnapshot, bool) {
	if s.batch == nil || index >= s.turnCount {
		return TurnSnapshot{}, false
	}
	return TurnSnapshot{batch: s.batch, index: index}, true
}

// StampCount returns the number of recorded stamps in this turn.
func (s TurnSnapshot) StampCount() uint32 {
	if s.batch == nil || s.index >= s.batch.turnCount {
		return 0
	}
	return s.batch.turns[s.index].stampCount
}

// Stamp returns a copy of one recorded stamp.
func (s TurnSnapshot) Stamp(index uint32) (Stamp, bool) {
	if s.batch == nil || s.index >= s.batch.turnCount || index >= s.batch.turns[s.index].stampCount {
		return Stamp{}, false
	}
	return s.batch.stamps[s.batch.stampOffset(s.index, index)], true
}

// Dropped returns the saturating count of point/span stamps rejected at
// capacity or malformed Begin input.
func (s TurnSnapshot) Dropped() uint32 {
	if s.batch == nil || s.index >= s.batch.turnCount {
		return 0
	}
	return s.batch.turns[s.index].dropped
}

// Identity returns the accepted canonical IDs associated with this turn.
// String conversion is intentionally deferred until snapshot/export time.
func (s TurnSnapshot) Identity() (turnID, traceID string, ok bool) {
	if s.batch == nil || s.index >= s.batch.turnCount {
		return "", "", false
	}
	state := &s.batch.turns[s.index]
	if !state.identitySet {
		return "", "", false
	}
	return string(state.turnID[:]), string(state.traceID[:]), true
}

// MissingSpanEnds counts Begin stamps with no accepted End stamp. It is
// evaluated only through a frozen snapshot, outside the recording hot path.
func (s TurnSnapshot) MissingSpanEnds() uint32 {
	if s.batch == nil || s.index >= s.batch.turnCount {
		return 0
	}
	state := &s.batch.turns[s.index]
	base := s.batch.stampOffset(s.index, 0)
	var missing uint32
	for index := uint32(0); index < state.stampCount; index++ {
		offset := base + int(index)
		if s.batch.stamps[offset].Phase == PhaseBegin && s.batch.spanEnded[offset] == 0 {
			missing++
		}
	}
	return missing
}

// ForegroundDone reports whether the foreground producer completed.
func (s TurnSnapshot) ForegroundDone() bool {
	return s.batch != nil && s.index < s.batch.turnCount && s.batch.turns[s.index].foregroundDone
}

// PendingChildren reports registered post-processing not yet completed.
func (s TurnSnapshot) PendingChildren() uint32 {
	if s.batch == nil || s.index >= s.batch.turnCount {
		return 0
	}
	return s.batch.turns[s.index].pendingChildren
}

// WorkTrackingIncomplete reports whether this turn has missing or invalid
// child-work registration evidence.
func (s TurnSnapshot) WorkTrackingIncomplete() bool {
	return s.batch != nil && s.index < s.batch.turnCount && s.batch.turns[s.index].workIncomplete
}
