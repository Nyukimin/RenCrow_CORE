package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"sort"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// appendChunk is a run of complete log lines that continues file kind at offset.
type appendChunk struct {
	kind   fileKind
	offset int64
	data   []byte
}

type batchTask struct {
	key   key16
	facts taskFacts
	pos   linePos
}

type batchRun struct {
	key, taskKey key16
	facts        runFacts
	pos          linePos
}

type batchContext struct {
	key key16
	pos linePos
}

type batchReceipt struct {
	id        string
	rec       receiptRec
	duplicate bool
}

type activeRun struct {
	run key16
	has bool
}

// indexBatch is the decoded and validated effect of appended log lines on the
// index. Building it never changes the index, so the same function serves the
// dry run before a commit, the update after a commit, the replay of a tail, and
// the initial build: a record the index would reject can be refused before it
// reaches the log.
type indexBatch struct {
	ix    *taskIndex
	has   [kindCount]bool
	end   [kindCount]int64
	lines [kindCount]int64

	tasks    []batchTask
	runs     []batchRun
	contexts []batchContext
	notifs   []notifRec
	receipts []batchReceipt

	// Fold state for records that depend on earlier records of the same batch.
	newTasks       map[key16]struct{}
	pins           map[key16]taskPin
	latestRun      map[key16]domaintask.Run
	active         map[key16]activeRun
	latestReceipts map[string]TaskOperationReceipt

	maxRunGeneration     uint64
	maxReceiptGeneration uint64
}

// decodeStrictLine decodes one JSONL record the way the previous reader did:
// unknown fields and trailing values are errors.
func decodeStrictLine[T any](line []byte, out *T) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("JSONL record contains trailing value")
		}
		return err
	}
	return nil
}

// buildBatch decodes chunks (at most one per file) in dependency order and folds
// them over the current index. The caller holds applyMu.
func (ix *taskIndex) buildBatch(chunks []appendChunk) (*indexBatch, error) {
	ordered := append([]appendChunk(nil), chunks...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].kind < ordered[j].kind })
	b := &indexBatch{
		ix:             ix,
		newTasks:       make(map[key16]struct{}),
		pins:           make(map[key16]taskPin),
		latestRun:      make(map[key16]domaintask.Run),
		active:         make(map[key16]activeRun),
		latestReceipts: make(map[string]TaskOperationReceipt),
	}
	for i, chunk := range ordered {
		if chunk.kind >= kindCount {
			return nil, fmt.Errorf("%w: unknown file kind %d", ErrIndexInconsistent, chunk.kind)
		}
		if i > 0 && ordered[i-1].kind == chunk.kind {
			return nil, fmt.Errorf("%w: %s appears twice in one batch", ErrIndexInconsistent, kindFilename[chunk.kind])
		}
		if chunk.offset != ix.applied[chunk.kind] {
			return nil, fmt.Errorf("%w: %s append at %d does not continue the indexed prefix %d", ErrIndexInconsistent, kindFilename[chunk.kind], chunk.offset, ix.applied[chunk.kind])
		}
		if err := b.scan(chunk); err != nil {
			return nil, err
		}
		b.has[chunk.kind] = true
		b.end[chunk.kind] = chunk.offset + int64(len(chunk.data))
	}
	return b, nil
}

func (b *indexBatch) scan(chunk appendChunk) error {
	data := chunk.data
	if len(data) > 0 && data[len(data)-1] != '\n' {
		return fmt.Errorf("%w: %s has an unterminated final line at %d", ErrIndexInconsistent, kindFilename[chunk.kind], chunk.offset+int64(len(data)))
	}
	start := 0
	for start < len(data) {
		end := bytes.IndexByte(data[start:], '\n')
		raw := data[start : start+end]
		pos := linePos{off: uint64(chunk.offset) + uint64(start), length: uint32(len(raw)), crc: crc32.Checksum(raw, castagnoli)}
		start += end + 1
		b.lines[chunk.kind]++
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			continue
		}
		if err := b.absorb(chunk.kind, trimmed, pos); err != nil {
			return err
		}
	}
	return nil
}

func (b *indexBatch) absorb(kind fileKind, line []byte, pos linePos) error {
	var err error
	switch kind {
	case kindState:
		err = b.absorbTask(line, pos)
	case kindRun:
		err = b.absorbRun(line, pos)
	case kindContext:
		err = b.absorbContext(line, pos)
	case kindNotification:
		err = b.absorbNotification(line, pos)
	case kindReceipt:
		err = b.absorbReceipt(line, pos)
	default:
		err = fmt.Errorf("unsupported file kind %d", kind)
	}
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %s offset %d: %w", ErrIndexInconsistent, kindFilename[kind], pos.off, err)
}

func (b *indexBatch) taskKnown(key key16) bool {
	if _, ok := b.newTasks[key]; ok {
		return true
	}
	_, ok := b.ix.taskMap[key]
	return ok
}

func (b *indexBatch) absorbTask(line []byte, pos linePos) error {
	var value domaintask.Task
	if err := decodeStrictLine(line, &value); err != nil {
		return err
	}
	if err := value.Validate(); err != nil {
		return err
	}
	key, err := parseCanonicalKey(string(value.TaskID), taskKeyPrefix)
	if err != nil {
		return err
	}
	// The same transitions foldTaskRecords refuses between two versions of a Task.
	previous, known, err := b.previousPin(key)
	if err != nil {
		return err
	}
	if known {
		if err := checkTaskPinTransition(previous, value); err != nil {
			return err
		}
	}
	b.pins[key] = pinOfTask(value)
	b.tasks = append(b.tasks, batchTask{key: key, facts: summaryOfTask(value), pos: pos})
	b.newTasks[key] = struct{}{}
	return nil
}

// previousPin returns the pin state of the latest version of a Task seen by
// this batch or, failing that, by the index. The index keeps two flags per Task,
// so the line is read back only when the previous version carried a criteria
// revision or Resume claims.
func (b *indexBatch) previousPin(key key16) (taskPin, bool, error) {
	if pin, ok := b.pins[key]; ok {
		return pin, true, nil
	}
	flags, pos, ok := b.ix.committedPinLocator(key)
	if !ok {
		return taskPin{}, false, nil
	}
	pin, err := b.ix.readTaskPin(flags, pos, key)
	return pin, true, err
}

func (b *indexBatch) absorbRun(line []byte, pos linePos) error {
	var value domaintask.Run
	if err := decodeStrictLine(line, &value); err != nil {
		return err
	}
	if err := value.Validate(); err != nil {
		return err
	}
	runKey, err := parseCanonicalKey(string(value.RunID), runKeyPrefix)
	if err != nil {
		return err
	}
	taskKey, err := parseCanonicalKey(string(value.TaskID), taskKeyPrefix)
	if err != nil {
		return err
	}
	if !b.taskKnown(taskKey) {
		return fmt.Errorf("run %s refers to a task that is not in the log", value.RunID)
	}
	previous, known, err := b.previousRun(runKey)
	if err != nil {
		return err
	}
	if known {
		if err := validateRunUpdate(previous, value); err != nil {
			return fmt.Errorf("run update is invalid: %w", err)
		}
	}
	current := b.activeOf(taskKey)
	if value.Status == domaintask.RunStatusRunning {
		if current.has && current.run != runKey {
			return fmt.Errorf("task %s has multiple active runs", value.TaskID)
		}
		b.active[taskKey] = activeRun{run: runKey, has: true}
	} else if current.has && current.run == runKey {
		b.active[taskKey] = activeRun{}
	}
	b.latestRun[runKey] = value
	b.runs = append(b.runs, batchRun{key: runKey, taskKey: taskKey, facts: summaryOfRun(value), pos: pos})
	if value.WriterGeneration > b.maxRunGeneration {
		b.maxRunGeneration = value.WriterGeneration
	}
	return nil
}

// previousRun returns the latest version of a Run seen by this batch or, failing
// that, by the index.
func (b *indexBatch) previousRun(key key16) (domaintask.Run, bool, error) {
	if run, ok := b.latestRun[key]; ok {
		return run, true, nil
	}
	slot, ok := b.ix.runMap[key]
	if !ok {
		return domaintask.Run{}, false, nil
	}
	run, err := b.ix.readRunAt(b.ix.lt.pos(b.ix.runs[slot].tail), key)
	if err != nil {
		return domaintask.Run{}, false, err
	}
	return run, true, nil
}

// activeOf returns the Run of the Task that is currently running, as seen by
// this batch layered over the index.
func (b *indexBatch) activeOf(taskKey key16) activeRun {
	if state, ok := b.active[taskKey]; ok {
		return state
	}
	state := activeRun{}
	if slot, ok := b.ix.taskMap[taskKey]; ok {
		if running, found := b.ix.enums.lookup(string(domaintask.RunStatusRunning)); found {
			for run := b.ix.tasks[slot].runHead; run != 0; run = b.ix.runs[run-1].nextRun {
				if uint32(b.ix.runs[run-1].status) == running {
					state = activeRun{run: b.ix.runs[run-1].key, has: true}
					break
				}
			}
		}
	}
	b.active[taskKey] = state
	return state
}

func (b *indexBatch) absorbContext(line []byte, pos linePos) error {
	var value domaintask.SharedRoleContext
	if err := decodeStrictLine(line, &value); err != nil {
		return err
	}
	key, err := parseCanonicalKey(string(value.TaskID), taskKeyPrefix)
	if err != nil {
		return err
	}
	if !b.taskKnown(key) {
		return fmt.Errorf("context refers to task %s that is not in the log", value.TaskID)
	}
	b.contexts = append(b.contexts, batchContext{key: key, pos: pos})
	return nil
}

func (b *indexBatch) absorbNotification(line []byte, pos linePos) error {
	var value domaintask.Notification
	if err := decodeStrictLine(line, &value); err != nil {
		return err
	}
	key, err := parseCanonicalKey(string(value.TaskID), taskKeyPrefix)
	if err != nil {
		return err
	}
	b.notifs = append(b.notifs, notifRec{created: toStamp(value.CreatedAt), key: key, pos: pos, interrupt: value.Interrupt})
	return nil
}

func (b *indexBatch) absorbReceipt(line []byte, pos linePos) error {
	var value TaskOperationReceipt
	if err := decodeStrictLine(line, &value); err != nil {
		return fmt.Errorf("%w: %v", ErrTaskOperationReceiptCorrupt, err)
	}
	if err := validateTaskOperationReceipt(value); err != nil {
		return fmt.Errorf("%w: %v", ErrTaskOperationReceiptCorrupt, err)
	}
	previous, known := b.latestReceipts[value.OperationID]
	if !known {
		if rec, ok := b.ix.receipts[value.OperationID]; ok {
			stored, err := b.ix.readReceiptAt(rec.pos, value.OperationID)
			if err != nil {
				return err
			}
			previous, known = stored, true
		}
	}
	if known {
		if !sameTaskOperationReceipt(previous, value) {
			return fmt.Errorf("%w: operation_id %q has conflicting duplicate receipts", ErrTaskOperationReceiptCorrupt, value.OperationID)
		}
		b.receipts = append(b.receipts, batchReceipt{id: value.OperationID, duplicate: true})
		return nil
	}
	b.latestReceipts[value.OperationID] = value
	b.receipts = append(b.receipts, batchReceipt{id: value.OperationID, rec: receiptRec{generation: value.WriterGeneration, pos: pos}})
	if value.WriterGeneration > b.maxReceiptGeneration {
		b.maxReceiptGeneration = value.WriterGeneration
	}
	return nil
}

// resolvedTask is a Task summary with every string interned.
type resolvedTask struct {
	title, assignee, module uint32
	route, status           uint16
	priority, interrupt     uint16
}

type resolvedRun struct {
	assignee uint32
	status   uint16
	reason   uint16
}

func (ix *taskIndex) internEnum(value string) (uint16, error) {
	id, err := ix.enums.intern(value)
	if err != nil {
		return 0, err
	}
	if id > 0xFFFF {
		return 0, errors.New("too many distinct enumerated values in the log")
	}
	return uint16(id), nil
}

// merge applies b to the index under the write lock, as one step: readers see
// either none or all of the batch. Interning happens before any record changes,
// so a failure leaves the visible index untouched.
func (ix *taskIndex) merge(b *indexBatch) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	tasks := make([]resolvedTask, len(b.tasks))
	for i := range b.tasks {
		facts := &b.tasks[i].facts
		var err error
		r := &tasks[i]
		if r.title, err = ix.titles.intern(facts.title); err != nil {
			return err
		}
		if r.assignee, err = ix.assignees.intern(facts.assignee); err != nil {
			return err
		}
		if r.module, err = ix.modules.intern(facts.module); err != nil {
			return err
		}
		if r.route, err = ix.internEnum(facts.route); err != nil {
			return err
		}
		if r.status, err = ix.internEnum(facts.status); err != nil {
			return err
		}
		if r.priority, err = ix.internEnum(facts.priority); err != nil {
			return err
		}
		if r.interrupt, err = ix.internEnum(facts.interrupt); err != nil {
			return err
		}
	}
	for i := range b.runs {
		if !b.taskKnown(b.runs[i].taskKey) {
			return fmt.Errorf("%w: run refers to a task that is not in the index", ErrIndexInconsistent)
		}
	}
	for i := range b.contexts {
		if !b.taskKnown(b.contexts[i].key) {
			return fmt.Errorf("%w: context refers to a task that is not in the index", ErrIndexInconsistent)
		}
	}
	runs := make([]resolvedRun, len(b.runs))
	for i := range b.runs {
		facts := &b.runs[i].facts
		var err error
		r := &runs[i]
		if r.assignee, err = ix.assignees.intern(facts.assignee); err != nil {
			return err
		}
		if r.status, err = ix.internEnum(facts.status); err != nil {
			return err
		}
		if r.reason, err = ix.internEnum(facts.reason); err != nil {
			return err
		}
	}

	for i := range b.tasks {
		bt, r := &b.tasks[i], &tasks[i]
		slot, exists := ix.taskMap[bt.key]
		if !exists {
			slot = uint32(len(ix.tasks))
			ix.tasks = append(ix.tasks, taskRec{key: bt.key})
			ix.taskMap[bt.key] = slot
		}
		rec := &ix.tasks[slot]
		rec.updated = bt.facts.updated
		rec.title, rec.assignee, rec.module = r.title, r.assignee, r.module
		rec.route, rec.status, rec.priority, rec.interrupt = r.route, r.status, r.priority, r.interrupt
		rec.flags = 0
		if bt.facts.hasStarted {
			rec.flags |= flagStarted
		}
		if bt.facts.hasFinished {
			rec.flags |= flagFinished
		}
		if bt.facts.pinned {
			rec.flags |= flagPinned
		}
		if bt.facts.hasClaims {
			rec.flags |= flagClaims
		}
		if bt.facts.readOnly {
			rec.flags |= flagReadOnly
		}
		if bt.facts.relations != nil {
			rec.flags |= flagRelations
			ix.relations[slot] = *bt.facts.relations
		} else if exists {
			delete(ix.relations, slot)
		}
		ix.lt.chain(&rec.stateHead, &rec.stateTail, bt.pos)
	}
	for i := range b.runs {
		br, r := &b.runs[i], &runs[i]
		slot, exists := ix.runMap[br.key]
		if !exists {
			taskSlot := ix.taskMap[br.taskKey]
			slot = uint32(len(ix.runs))
			ix.runs = append(ix.runs, runRec{key: br.key, taskSlot: taskSlot, nextRun: ix.tasks[taskSlot].runHead})
			ix.tasks[taskSlot].runHead = slot + 1
			ix.runMap[br.key] = slot
		}
		rec := &ix.runs[slot]
		rec.generation = br.facts.generation
		rec.started = br.facts.started
		rec.assignee, rec.status, rec.reason = r.assignee, r.status, r.reason
		rec.flags = 0
		if br.facts.hasCompleted {
			rec.flags |= runFlagCompleted
		}
		ix.lt.chain(&rec.head, &rec.tail, br.pos)
	}
	for _, bc := range b.contexts {
		rec := &ix.tasks[ix.taskMap[bc.key]]
		ix.lt.chain(&rec.ctxHead, &rec.ctxTail, bc.pos)
	}
	ix.notifs = append(ix.notifs, b.notifs...)
	for _, br := range b.receipts {
		if !br.duplicate {
			ix.receipts[br.id] = br.rec
		}
	}
	for kind := fileKind(0); kind < kindCount; kind++ {
		ix.lines[kind] += b.lines[kind]
		if b.has[kind] {
			ix.applied[kind] = b.end[kind]
		}
	}
	if b.maxRunGeneration > ix.maxRunGeneration {
		ix.maxRunGeneration = b.maxRunGeneration
	}
	if b.maxReceiptGeneration > ix.maxReceiptGeneration {
		ix.maxReceiptGeneration = b.maxReceiptGeneration
	}
	if ix.ck != nil {
		var total int64
		for _, n := range ix.lines {
			total += n
		}
		ix.ck.noteLines(total)
	}
	return nil
}

// apply is the one function that updates the index from appended log bytes:
// the initial build, the replay of an unabsorbed tail and the update after a
// commit all go through it.
func (ix *taskIndex) apply(chunks []appendChunk) error {
	ix.applyMu.Lock()
	defer ix.applyMu.Unlock()
	b, err := ix.buildBatch(chunks)
	if err != nil {
		return err
	}
	return ix.merge(b)
}

// validateAppend dry-runs payloads that are about to be appended. A record the
// index would refuse must never reach the log: it would make every later replay
// and start fail closed.
func (ix *taskIndex) validateAppend(payloads map[string][]byte) error {
	ix.applyMu.Lock()
	defer ix.applyMu.Unlock()
	chunks := make([]appendChunk, 0, len(payloads))
	for name, payload := range payloads {
		kind, indexed := kindOfFilename(name)
		if !indexed {
			continue
		}
		chunks = append(chunks, appendChunk{kind: kind, offset: ix.applied[kind], data: payload})
	}
	_, err := ix.buildBatch(chunks)
	return err
}
