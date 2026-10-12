package task

import (
	"errors"
	"hash/crc32"
	"path/filepath"
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

// linePos locates one log line: the segment it is in, its byte offset in that
// segment's file, its length without the terminating newline, and the CRC32C of
// those bytes taken when the line was first seen.
//
// off packs the segment ordinal into its upper 16 bits and the file offset into
// the lower 48. Segment 0 (the files in the store root, and the only segment of a
// store on layout v1) has ordinal 0, so its positions are the plain file offsets
// they always were, and positions order the way the log does: by segment, then
// by offset. Nothing outside these accessors computes with off.
type linePos struct {
	off    uint64
	length uint32
	crc    uint32
}

const (
	posOffsetBits = 48
	posOffsetMask = 1<<posOffsetBits - 1
)

// packPos builds the off of a line at fileOffset in segment ord.
func packPos(ord uint16, fileOffset int64) uint64 {
	return uint64(ord)<<posOffsetBits | uint64(fileOffset)&posOffsetMask
}

func (p linePos) ordinal() uint16   { return uint16(p.off >> posOffsetBits) }
func (p linePos) fileOffset() int64 { return int64(p.off & posOffsetMask) }

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

	// fc hands out read handles to the log files of every segment.
	fc *segFileCache
	// segs lists the segments in ordinal order, the current one last; cur is the
	// ordinal of the segment writes go to. applied and lines describe that current
	// segment only: the bytes of each of its files that the index has absorbed,
	// and the lines in them. A closed segment's final sizes and lines are in segs.
	// Rotation is the only thing that changes segs and cur, and it holds applyMu
	// and mu for it.
	segs    []segInfo
	cur     uint16
	applied [kindCount]int64
	lines   [kindCount]int64
	// layoutV2 is true when the store is on layout v2 (a LAYOUT file exists).
	layoutV2 bool
	// allowMissing lets a log file be absent (a reader opened before the writer
	// created it); an absent file is an empty one.
	allowMissing bool

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

	// ck persists the index to its sidecar; nil unless OpenOptions.Persist.
	ck *checkpointer
	// source and rebuildReason record how the index came to be (see IndexStats).
	source        string
	rebuildReason string
	// replayedLines is how many log lines a restore absorbed beyond the sidecar.
	replayedLines int64
	// replayHook, if set, is called after each file of a build or a tail replay
	// has been absorbed (a test seam; nil in production).
	replayHook func(kind fileKind)
}

// segInfo describes one segment. size and lines are final for a closed segment
// and meaningless for the current one.
type segInfo struct {
	name  string
	dir   string
	size  [sizedFileCount]int64
	lines [kindCount]int64
}

// How an index was obtained (IndexStats.Source).
const (
	sourceBuilt   = "built"
	sourceSidecar = "sidecar"
	sourceRebuilt = "rebuilt"
)

func newTaskIndex(root string) *taskIndex {
	ix := &taskIndex{
		root:      root,
		fc:        newSegFileCache(),
		segs:      []segInfo{{name: segmentLegacyName, dir: root}},
		taskMap:   make(map[key16]uint32),
		runMap:    make(map[key16]uint32),
		receipts:  make(map[string]receiptRec),
		relations: make(map[uint32]taskRelations),
	}
	ix.fc.setDir(0, root)
	return ix
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
	// Source says how the index was obtained at open: "built" from the log with
	// no sidecar involved, "sidecar" restored from the sidecar (plus the log
	// written after it), or "rebuilt" from the log because the sidecar could not
	// be used (RebuildReason says why).
	Source        string
	RebuildReason string
	// Checkpoints is the number of sidecars written by this writer;
	// CheckpointFailures the failures in a row since the last success.
	Checkpoints        uint64
	CheckpointFailures int64
	// Segments is the number of segments the index covers (1 on layout v1),
	// Segment the name of the one writes go to ("legacy" on layout v1) and
	// TotalLines the lines of all segments together. Lines and AppliedBytes above
	// are those of that current segment.
	Segments   int
	Segment    string
	TotalLines int64
}

// stats returns a snapshot. The caller must not hold mu.
func (ix *taskIndex) stats() IndexStats {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	stats := IndexStats{
		Tasks: len(ix.tasks), Runs: len(ix.runs), Notifications: len(ix.notifs), Receipts: len(ix.receipts),
		Lines: ix.lines, AppliedBytes: ix.applied,
		ApproxBytes: ix.approxBytesLocked(), Dirty: ix.dirty.Load(), CorruptReads: ix.corruptReads.Load(),
		Source: ix.source, RebuildReason: ix.rebuildReason,
		Segments: len(ix.segs), Segment: ix.segs[ix.cur].name, TotalLines: ix.totalLinesLocked(),
	}
	if ix.ck != nil {
		stats.Checkpoints, stats.CheckpointFailures = ix.ck.count.Load(), ix.ck.failures.Load()
	}
	return stats
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
	return ix.fc.close()
}

// totalLinesLocked is the number of lines of all segments, over all files. The
// caller holds mu (or applyMu).
func (ix *taskIndex) totalLinesLocked() int64 {
	var total int64
	for i := 0; i < int(ix.cur); i++ {
		total += totalLines(ix.segs[i].lines)
	}
	return total + totalLines(ix.lines)
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

// fenceLogPaths are the execution fence logs of all segments, oldest first.
func (ix *taskIndex) fenceLogPaths() []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	paths := make([]string, 0, len(ix.segs))
	for _, seg := range ix.segs {
		paths = append(paths, filepath.Join(seg.dir, taskExecutionFenceFilename))
	}
	return paths
}
