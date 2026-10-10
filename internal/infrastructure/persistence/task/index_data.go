package task

import (
	"errors"
	"hash/crc32"
	"os"
	"sync"
	"sync/atomic"
	"unsafe"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	// ErrRecordCorrupt reports that a log line the index points at failed
	// verification (length, terminator, strict decode, ID or CRC32C). It is never
	// folded into domaintask.ErrNotFound: an ID that exists but cannot be read
	// must not look absent, because callers create a Task when it is.
	ErrRecordCorrupt = errors.New("task store record is corrupt")
	// ErrIndexInconsistent reports that the in-memory index cannot follow the
	// log: a record it must absorb is invalid, or a file no longer continues the
	// indexed prefix. The store fails closed instead of guessing.
	ErrIndexInconsistent = errors.New("task store index is inconsistent with the log")
)

// fileKind enumerates the log files the index follows, in dependency order:
// a Run needs its Task and a context needs its Task, so state is applied first.
// jsonlbatch appends files in name order (context before state), so every
// application of appended bytes sorts by this order instead.
type fileKind uint8

const (
	kindState fileKind = iota
	kindRun
	kindContext
	kindNotification
	kindReceipt
	kindCount
)

var kindFilename = [kindCount]string{
	kindState:        stateFilename,
	kindRun:          runFilename,
	kindContext:      contextFilename,
	kindNotification: notificationsFilename,
	kindReceipt:      taskOperationReceiptFilename,
}

func kindOfFilename(name string) (fileKind, bool) {
	for kind, filename := range kindFilename {
		if filename == name {
			return fileKind(kind), true
		}
	}
	return 0, false
}

// castagnoli is the CRC32C table; the standard library uses the CPU instruction.
var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// linePos locates one log line: its byte offset, its length without the
// terminating newline, and the CRC32C of those bytes taken when the line was
// first seen.
type linePos struct {
	off    uint64
	length uint32
	crc    uint32
}

// lineTable stores line positions as parallel arrays (20 bytes per line, no
// struct padding). A line index is 1-based; 0 means "none". next chains the
// lines of one Task or one Run in log order.
type lineTable struct {
	off    []uint64
	length []uint32
	crc    []uint32
	next   []uint32
}

func (t *lineTable) add(pos linePos) uint32 {
	t.off = append(t.off, pos.off)
	t.length = append(t.length, pos.length)
	t.crc = append(t.crc, pos.crc)
	t.next = append(t.next, 0)
	return uint32(len(t.off))
}

func (t *lineTable) pos(index uint32) linePos {
	i := index - 1
	return linePos{off: t.off[i], length: t.length[i], crc: t.crc[i]}
}

// chain appends pos to the chain whose ends are head and tail.
func (t *lineTable) chain(head, tail *uint32, pos linePos) {
	index := t.add(pos)
	if *head == 0 {
		*head = index
	} else {
		t.next[*tail-1] = index
	}
	*tail = index
}

// Task flag bits.
const (
	flagStarted   uint8 = 1 << iota // StartedAt is set
	flagFinished                    // FinishedAt is set
	flagReadOnly                    // read_only
	flagRelations                   // relations holds parent/dependency/supersedes
	flagPinned                      // expected_criteria_revision is set
	flagClaims                      // native_resume_claims is not empty
)

// taskRec is the pointer-free summary of one Task plus the positions of all of
// its log lines. The latest state line is stateTail. Only what the index answers
// by itself is kept in memory (identity, the filter keys and the sort key); the
// remaining fields are read from the log line when a caller asks for the Task,
// so adding one costs nothing until something needs it without reading.
type taskRec struct {
	key       key16
	updated   stamp
	title     uint32
	assignee  uint32
	module    uint32
	stateHead uint32
	stateTail uint32
	ctxHead   uint32
	ctxTail   uint32
	runHead   uint32 // most recent first-seen Run of the Task, as run slot + 1
	route     uint16
	status    uint16
	priority  uint16
	interrupt uint16
	flags     uint8
}

// taskRelations holds the rarely used reference fields.
type taskRelations struct {
	parent     modulecore.TaskID
	supersedes modulecore.TaskID
	deps       []modulecore.TaskID
}

const runFlagCompleted uint8 = 1

// runRec is the pointer-free summary of one Run; the latest run line is tail.
type runRec struct {
	key        key16
	generation uint64
	started    stamp
	taskSlot   uint32
	assignee   uint32
	head       uint32
	tail       uint32
	nextRun    uint32 // next Run of the same Task, as run slot + 1
	status     uint16
	reason     uint16
	flags      uint8
}

// notifRec locates one notification and keeps what ordering needs.
type notifRec struct {
	created   stamp
	key       key16
	pos       linePos
	interrupt bool
}

// receiptRec locates one stored operation receipt.
type receiptRec struct {
	generation uint64
	pos        linePos
}

// taskIndex is the in-memory ID and summary index over the Task store log. The
// log is the only source of truth; everything here is derived from it and can be
// rebuilt by reading the files again.
//
// Locking: mu guards every field below it for readers (RLock) and for the one
// merge that follows each committed transaction (Lock). applyMu serializes the
// code that builds and merges batches, so a batch is built against a stable
// index. The OS lock of jsonlbatch keeps other processes out of the files; the
// Go mutexes exist because the race detector, and correctness across
// goroutines, need Go-level ordering.
type taskIndex struct {
	root    string
	applyMu sync.Mutex
	mu      sync.RWMutex

	files   [kindCount]*os.File
	applied [kindCount]int64
	lines   [kindCount]int64

	tasks     []taskRec
	taskMap   map[key16]uint32
	runs      []runRec
	runMap    map[key16]uint32
	lt        lineTable
	notifs    []notifRec
	receipts  map[string]receiptRec
	relations map[uint32]taskRelations

	titles    internTable
	assignees internTable
	modules   internTable
	enums     internTable

	maxRunGeneration     uint64
	maxReceiptGeneration uint64

	// dirty is set when a write ended without a known commit boundary
	// (ErrCommitUncertain / ErrRecoveryRequired) or the post-commit hook could not
	// absorb a transaction. While it is set, reads resynchronize through the OS
	// lock first so they never answer from a stale state.
	dirty        atomic.Bool
	closed       atomic.Bool
	corruptReads atomic.Uint64
}

func newTaskIndex(root string) *taskIndex {
	return &taskIndex{
		root:      root,
		taskMap:   make(map[key16]uint32),
		runMap:    make(map[key16]uint32),
		receipts:  make(map[string]receiptRec),
		relations: make(map[uint32]taskRelations),
	}
}

// IndexStats describes the in-memory index of a store opened with Index enabled.
type IndexStats struct {
	Tasks         int
	Runs          int
	Notifications int
	Receipts      int
	// Lines is the number of log lines applied per file, in the order state,
	// run, context, notifications, receipts.
	Lines [kindCount]int64
	// AppliedBytes is the committed prefix of each file the index has absorbed.
	AppliedBytes [kindCount]int64
	// ApproxBytes estimates the heap the index holds.
	ApproxBytes int64
	Dirty       bool
	// CorruptReads counts reads that failed line verification (ErrRecordCorrupt).
	CorruptReads uint64
}

// stats returns a snapshot. The caller must not hold mu.
func (ix *taskIndex) stats() IndexStats {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return IndexStats{
		Tasks: len(ix.tasks), Runs: len(ix.runs), Notifications: len(ix.notifs), Receipts: len(ix.receipts),
		Lines: ix.lines, AppliedBytes: ix.applied,
		ApproxBytes: ix.approxBytesLocked(), Dirty: ix.dirty.Load(), CorruptReads: ix.corruptReads.Load(),
	}
}

// approxBytesLocked estimates the heap held by the index from slice capacities
// and per-entry map costs. It is an estimate for monitoring; tests measure the
// real heap separately.
func (ix *taskIndex) approxBytesLocked() int64 {
	const mapEntryOverhead = 16
	total := int64(cap(ix.tasks)) * int64(unsafe.Sizeof(taskRec{}))
	total += int64(cap(ix.runs)) * int64(unsafe.Sizeof(runRec{}))
	total += int64(cap(ix.notifs)) * int64(unsafe.Sizeof(notifRec{}))
	total += int64(cap(ix.lt.off))*8 + int64(cap(ix.lt.length))*4 + int64(cap(ix.lt.crc))*4 + int64(cap(ix.lt.next))*4
	total += int64(len(ix.taskMap)) * (int64(unsafe.Sizeof(key16{})) + 4 + mapEntryOverhead)
	total += int64(len(ix.runMap)) * (int64(unsafe.Sizeof(key16{})) + 4 + mapEntryOverhead)
	for id := range ix.receipts {
		total += int64(len(id)) + int64(unsafe.Sizeof(receiptRec{})) + mapEntryOverhead + 16
	}
	for _, table := range []*internTable{&ix.titles, &ix.assignees, &ix.modules, &ix.enums} {
		for _, value := range table.vals {
			total += int64(len(value)) + 16 + mapEntryOverhead + 4
		}
	}
	total += int64(len(ix.relations)) * 96
	return total
}

func (ix *taskIndex) close() error {
	if ix == nil || !ix.closed.CompareAndSwap(false, true) {
		return nil
	}
	var firstErr error
	for _, file := range ix.files {
		if file == nil {
			continue
		}
		if err := file.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// summaryOfTask converts a Task into the facts the index keeps.
func summaryOfTask(value domaintask.Task) taskFacts {
	facts := taskFacts{
		title: value.Title, assignee: value.Assignee, module: value.ModuleID,
		route: string(value.Route), status: string(value.Status), priority: string(value.Priority), interrupt: string(value.InterruptPolicy),
		updated: toStamp(value.UpdatedAt), readOnly: value.ReadOnly,
		hasStarted: value.StartedAt != nil, hasFinished: value.FinishedAt != nil,
		pinned: value.ExpectedCriteriaRevision != "", hasClaims: len(value.NativeResumeClaims) > 0,
	}
	if value.ParentTaskID != "" || value.SupersedesTaskID != "" || len(value.DependencyTaskIDs) > 0 {
		facts.relations = &taskRelations{
			parent: value.ParentTaskID, supersedes: value.SupersedesTaskID,
			deps: append([]modulecore.TaskID(nil), value.DependencyTaskIDs...),
		}
	}
	return facts
}

// taskFacts is a Task summary with plain strings, built while a batch is
// decoded. Interning into the index happens only at merge time, so building a
// batch (including the dry run before commit) never changes the index.
type taskFacts struct {
	title, assignee, module            string
	route, status, priority, interrupt string
	updated                            stamp
	hasStarted, hasFinished, readOnly  bool
	pinned, hasClaims                  bool
	relations                          *taskRelations
}

type runFacts struct {
	assignee, status, reason string
	generation               uint64
	started                  stamp
	hasCompleted             bool
}

func summaryOfRun(value domaintask.Run) runFacts {
	return runFacts{
		assignee: value.Assignee, status: string(value.Status), reason: string(value.StartReason),
		generation: value.WriterGeneration, started: toStamp(value.StartedAt), hasCompleted: value.CompletedAt != nil,
	}
}
