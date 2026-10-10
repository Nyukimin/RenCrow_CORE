package task

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"sort"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// The sidecar (index/seg-legacy.tsi) is the persisted form of the in-memory
// index: length-prefixed little-endian binary with a version, the committed
// prefix and line count of every indexed file, the highest writer generations
// and a CRC32C trailer. It is derived data. Deleting it, or failing to read it
// for any reason, only costs a rebuild from the log; it never changes what the
// store answers. The reader therefore accepts exactly one format version and
// treats everything else as "no sidecar".
//
// Layout (all integers little-endian):
//
//	header   magic "RTSI" | version u16 | flags u16 | own_ordinal u16 | kinds u16 |
//	         own_name [24]byte | applied [kinds]u64 | lines [kinds]u64 |
//	         n_tasks n_runs n_lines n_notifs n_receipts n_relations (u32 each) |
//	         max_run_generation u64 | max_receipt_generation u64 | total_len u64
//	body     4 string tables | tasks | runs | line table (4 columns) |
//	         notifications | receipts | relations
//	trailer  crc32c u32 (of everything before it) | magic "ISTR"
//
// The segment identity (own_ordinal, own_name) is "0, legacy": this version
// persists the one segment that exists today (the files in the store root).
const (
	sidecarMagic       = "RTSI"
	sidecarEndMagic    = "ISTR"
	sidecarVersion     = 1
	sidecarNameLen     = 24
	sidecarLegacyName  = "legacy"
	sidecarHeaderFixed = 4 + 2 + 2 + 2 + 2 + sidecarNameLen + 8*int(kindCount)*2 + 4*6 + 8*3
	sidecarTrailerLen  = 4 + 4
	// Smallest encoded size of one record, used to refuse an element count that
	// the remaining bytes cannot hold before allocating for it.
	sidecarTaskRecLen  = 16 + 8 + 4 + 8*4 + 2*4 + 1
	sidecarRunRecLen   = 16 + 8 + 8 + 4 + 5*4 + 2*2 + 1
	sidecarNotifRecLen = 8 + 4 + 16 + 8 + 4 + 4 + 1
)

var (
	// errSidecarCorrupt marks a sidecar that cannot be trusted: torn, truncated,
	// bit-flipped, or internally inconsistent. The segment is rebuilt from the log.
	errSidecarCorrupt = errors.New("task index sidecar is corrupt")
	// errSidecarVersion marks a sidecar written in another format version. It is
	// not damaged; the segment is rebuilt and a new sidecar replaces it.
	errSidecarVersion = errors.New("task index sidecar has an unsupported version")
)

func sidecarCorruptf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errSidecarCorrupt, fmt.Sprintf(format, args...))
}

// sidecarHeader is the decoded fixed header.
type sidecarHeader struct {
	Version              uint16
	Flags                uint16
	OwnOrdinal           uint16
	OwnName              string
	Applied              [kindCount]int64
	Lines                [kindCount]int64
	MaxRunGeneration     uint64
	MaxReceiptGeneration uint64
	Tasks                uint32
	Runs                 uint32
	LineTable            uint32
	Notifications        uint32
	Receipts             uint32
	Relations            uint32
}

// indexSnapshot is a consistent copy of the index taken under its read lock.
// Records that commits modify in place (Task and Run summaries, the chain
// links) are copied; the append-only columns are shared, because the elements
// below the captured length never change, so encoding can run without the lock.
type indexSnapshot struct {
	applied, lines                         [kindCount]int64
	maxRunGeneration, maxReceiptGeneration uint64
	tasks                                  []taskRec
	runs                                   []runRec
	lineOff                                []uint64
	lineLen, lineCRC, lineNext             []uint32
	notifs                                 []notifRec
	receipts                               map[string]receiptRec
	relations                              map[uint32]taskRelations
	titles, assignees, modules, enums      []string
}

// totalLines is the number of log lines the snapshot covers, over all files.
func (s *indexSnapshot) totalLines() int64 {
	var total int64
	for _, n := range s.lines {
		total += n
	}
	return total
}

// snapshot copies the index state a checkpoint writes. The read lock is held
// only for the memory copies (milliseconds for tens of thousands of Tasks); the
// encoding and the file I/O happen after it is released.
func (ix *taskIndex) snapshot() *indexSnapshot {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	n := len(ix.lt.off)
	snap := &indexSnapshot{
		applied:              ix.applied,
		lines:                ix.lines,
		maxRunGeneration:     ix.maxRunGeneration,
		maxReceiptGeneration: ix.maxReceiptGeneration,
		tasks:                append(make([]taskRec, 0, len(ix.tasks)), ix.tasks...),
		runs:                 append(make([]runRec, 0, len(ix.runs)), ix.runs...),
		lineOff:              ix.lt.off[:n:n],
		lineLen:              ix.lt.length[:n:n],
		lineCRC:              ix.lt.crc[:n:n],
		lineNext:             append(make([]uint32, 0, n), ix.lt.next...),
		notifs:               ix.notifs[:len(ix.notifs):len(ix.notifs)],
		receipts:             make(map[string]receiptRec, len(ix.receipts)),
		relations:            make(map[uint32]taskRelations, len(ix.relations)),
		titles:               ix.titles.vals[:len(ix.titles.vals):len(ix.titles.vals)],
		assignees:            ix.assignees.vals[:len(ix.assignees.vals):len(ix.assignees.vals)],
		modules:              ix.modules.vals[:len(ix.modules.vals):len(ix.modules.vals)],
		enums:                ix.enums.vals[:len(ix.enums.vals):len(ix.enums.vals)],
	}
	for id, rec := range ix.receipts {
		snap.receipts[id] = rec
	}
	for slot, rel := range ix.relations {
		snap.relations[slot] = rel
	}
	return snap
}

type sidecarEncoder struct{ buf []byte }

func (e *sidecarEncoder) u8(v uint8)   { e.buf = append(e.buf, v) }
func (e *sidecarEncoder) u16(v uint16) { e.buf = binary.LittleEndian.AppendUint16(e.buf, v) }
func (e *sidecarEncoder) u32(v uint32) { e.buf = binary.LittleEndian.AppendUint32(e.buf, v) }
func (e *sidecarEncoder) u64(v uint64) { e.buf = binary.LittleEndian.AppendUint64(e.buf, v) }
func (e *sidecarEncoder) i64(v int64)  { e.u64(uint64(v)) }
func (e *sidecarEncoder) i32(v int32)  { e.u32(uint32(v)) }
func (e *sidecarEncoder) str(v string) {
	e.u32(uint32(len(v)))
	e.buf = append(e.buf, v...)
}
func (e *sidecarEncoder) stamp(v stamp) { e.i64(v.sec); e.i32(v.nsec) }
func (e *sidecarEncoder) key(v key16)   { e.buf = append(e.buf, v[:]...) }
func (e *sidecarEncoder) pos(v linePos) { e.u64(v.off); e.u32(v.length); e.u32(v.crc) }

// strings writes one intern table: the number of entries after the empty
// string (id 0, implicit), then each value.
func (e *sidecarEncoder) strings(vals []string) {
	if len(vals) <= 1 {
		e.u32(0)
		return
	}
	e.u32(uint32(len(vals) - 1))
	for _, v := range vals[1:] {
		e.str(v)
	}
}

// encodeSidecar serializes a snapshot. The same snapshot always yields the same
// bytes (map-backed sections are written in key order).
func encodeSidecar(snap *indexSnapshot) []byte {
	receiptIDs := make([]string, 0, len(snap.receipts))
	for id := range snap.receipts {
		receiptIDs = append(receiptIDs, id)
	}
	sort.Strings(receiptIDs)
	relationSlots := make([]uint32, 0, len(snap.relations))
	for slot := range snap.relations {
		relationSlots = append(relationSlots, slot)
	}
	sort.Slice(relationSlots, func(i, j int) bool { return relationSlots[i] < relationSlots[j] })

	estimate := sidecarHeaderFixed + sidecarTrailerLen +
		len(snap.tasks)*sidecarTaskRecLen + len(snap.runs)*sidecarRunRecLen +
		len(snap.lineOff)*20 + len(snap.notifs)*sidecarNotifRecLen + len(receiptIDs)*64 + 1024
	e := &sidecarEncoder{buf: make([]byte, 0, estimate)}

	e.buf = append(e.buf, sidecarMagic...)
	e.u16(sidecarVersion)
	e.u16(0) // flags
	e.u16(0) // own_ordinal
	e.u16(uint16(kindCount))
	name := make([]byte, sidecarNameLen)
	copy(name, sidecarLegacyName)
	e.buf = append(e.buf, name...)
	for _, v := range snap.applied {
		e.i64(v)
	}
	for _, v := range snap.lines {
		e.i64(v)
	}
	e.u32(uint32(len(snap.tasks)))
	e.u32(uint32(len(snap.runs)))
	e.u32(uint32(len(snap.lineOff)))
	e.u32(uint32(len(snap.notifs)))
	e.u32(uint32(len(receiptIDs)))
	e.u32(uint32(len(relationSlots)))
	e.u64(snap.maxRunGeneration)
	e.u64(snap.maxReceiptGeneration)
	totalLenAt := len(e.buf)
	e.u64(0) // total_len, patched below

	e.strings(snap.titles)
	e.strings(snap.assignees)
	e.strings(snap.modules)
	e.strings(snap.enums)

	for i := range snap.tasks {
		r := &snap.tasks[i]
		e.key(r.key)
		e.stamp(r.updated)
		e.u32(r.title)
		e.u32(r.assignee)
		e.u32(r.module)
		e.u32(r.stateHead)
		e.u32(r.stateTail)
		e.u32(r.ctxHead)
		e.u32(r.ctxTail)
		e.u32(r.runHead)
		e.u16(r.route)
		e.u16(r.status)
		e.u16(r.priority)
		e.u16(r.interrupt)
		e.u8(r.flags)
	}
	for i := range snap.runs {
		r := &snap.runs[i]
		e.key(r.key)
		e.u64(r.generation)
		e.stamp(r.started)
		e.u32(r.taskSlot)
		e.u32(r.assignee)
		e.u32(r.head)
		e.u32(r.tail)
		e.u32(r.nextRun)
		e.u16(r.status)
		e.u16(r.reason)
		e.u8(r.flags)
	}
	for _, v := range snap.lineOff {
		e.u64(v)
	}
	for _, v := range snap.lineLen {
		e.u32(v)
	}
	for _, v := range snap.lineCRC {
		e.u32(v)
	}
	for _, v := range snap.lineNext {
		e.u32(v)
	}
	for i := range snap.notifs {
		n := &snap.notifs[i]
		e.stamp(n.created)
		e.key(n.key)
		e.pos(n.pos)
		if n.interrupt {
			e.u8(1)
		} else {
			e.u8(0)
		}
	}
	for _, id := range receiptIDs {
		rec := snap.receipts[id]
		e.str(id)
		e.u64(rec.generation)
		e.pos(rec.pos)
	}
	for _, slot := range relationSlots {
		rel := snap.relations[slot]
		e.u32(slot)
		e.str(string(rel.parent))
		e.str(string(rel.supersedes))
		e.u32(uint32(len(rel.deps)))
		for _, dep := range rel.deps {
			e.str(string(dep))
		}
	}

	total := uint64(len(e.buf) + sidecarTrailerLen)
	binary.LittleEndian.PutUint64(e.buf[totalLenAt:], total)
	e.u32(crc32.Checksum(e.buf, castagnoli))
	e.buf = append(e.buf, sidecarEndMagic...)
	return e.buf
}

type sidecarDecoder struct {
	b   []byte
	off int
	err error
}

func (d *sidecarDecoder) take(n int) []byte {
	if d.err != nil {
		return nil
	}
	if n < 0 || n > len(d.b)-d.off {
		d.err = sidecarCorruptf("record runs past the end at offset %d", d.off)
		return nil
	}
	out := d.b[d.off : d.off+n]
	d.off += n
	return out
}

func (d *sidecarDecoder) u8() uint8 {
	if v := d.take(1); v != nil {
		return v[0]
	}
	return 0
}

func (d *sidecarDecoder) u16() uint16 {
	if v := d.take(2); v != nil {
		return binary.LittleEndian.Uint16(v)
	}
	return 0
}

func (d *sidecarDecoder) u32() uint32 {
	if v := d.take(4); v != nil {
		return binary.LittleEndian.Uint32(v)
	}
	return 0
}

func (d *sidecarDecoder) u64() uint64 {
	if v := d.take(8); v != nil {
		return binary.LittleEndian.Uint64(v)
	}
	return 0
}

func (d *sidecarDecoder) i64() int64 { return int64(d.u64()) }
func (d *sidecarDecoder) i32() int32 { return int32(d.u32()) }

func (d *sidecarDecoder) str() string {
	n := d.u32()
	if d.err != nil {
		return ""
	}
	return string(d.take(int(n)))
}

func (d *sidecarDecoder) stamp() stamp {
	sec := d.i64()
	return stamp{sec: sec, nsec: d.i32()}
}

func (d *sidecarDecoder) key() (k key16) {
	copy(k[:], d.take(16))
	return k
}

func (d *sidecarDecoder) pos() linePos {
	off := d.u64()
	length := d.u32()
	return linePos{off: off, length: length, crc: d.u32()}
}

// remaining is the number of undecoded body bytes (excluding the trailer).
func (d *sidecarDecoder) remaining() int { return len(d.b) - sidecarTrailerLen - d.off }

// count reads an element count and refuses one the remaining bytes cannot hold.
func (d *sidecarDecoder) count(minRecord int) int {
	n := int(d.u32())
	if d.err != nil {
		return 0
	}
	if minRecord > 0 && n > d.remaining()/minRecord {
		d.err = sidecarCorruptf("count %d does not fit in the remaining %d bytes", n, d.remaining())
		return 0
	}
	return n
}

func (d *sidecarDecoder) strings(table *internTable) {
	n := d.count(4)
	if d.err != nil || n == 0 {
		return
	}
	table.vals = make([]string, 1, n+1)
	table.ids = make(map[string]uint32, n)
	for i := 1; i <= n; i++ {
		value := d.str()
		if d.err != nil {
			return
		}
		if value == "" {
			d.err = sidecarCorruptf("string table holds an empty entry")
			return
		}
		if _, dup := table.ids[value]; dup {
			d.err = sidecarCorruptf("string table repeats a value")
			return
		}
		table.vals = append(table.vals, value)
		table.ids[value] = uint32(i)
	}
}

// decodeSidecar parses and validates a sidecar. It returns an index that is not
// yet attached to files (root and files are the caller's to set) together with
// the header. A checksum alone is not trusted: every reference is range-checked
// and every chain walked, so a sidecar that is wrong but well-formed is refused
// here instead of misleading a read later.
func decodeSidecar(data []byte) (*taskIndex, sidecarHeader, error) {
	var h sidecarHeader
	if len(data) < sidecarHeaderFixed+sidecarTrailerLen {
		return nil, h, sidecarCorruptf("%d bytes is shorter than the header", len(data))
	}
	if string(data[:4]) != sidecarMagic {
		return nil, h, sidecarCorruptf("bad magic")
	}
	h.Version = binary.LittleEndian.Uint16(data[4:])
	h.Flags = binary.LittleEndian.Uint16(data[6:])
	kinds := binary.LittleEndian.Uint16(data[10:])
	if h.Version != sidecarVersion || h.Flags != 0 || int(kinds) != int(kindCount) {
		return nil, h, fmt.Errorf("%w: version %d flags %#x kinds %d", errSidecarVersion, h.Version, h.Flags, kinds)
	}
	if string(data[len(data)-4:]) != sidecarEndMagic {
		return nil, h, sidecarCorruptf("bad end magic (torn or truncated)")
	}
	body := len(data) - sidecarTrailerLen
	if got, want := crc32.Checksum(data[:body], castagnoli), binary.LittleEndian.Uint32(data[body:]); got != want {
		return nil, h, sidecarCorruptf("CRC32C mismatch")
	}

	d := &sidecarDecoder{b: data, off: 8}
	h.OwnOrdinal = d.u16()
	d.u16() // kinds, checked above
	nameBytes := d.take(sidecarNameLen)
	h.OwnName = string(bytes.TrimRight(nameBytes, "\x00"))
	for k := range h.Applied {
		h.Applied[k] = d.i64()
	}
	for k := range h.Lines {
		h.Lines[k] = d.i64()
	}
	h.Tasks, h.Runs, h.LineTable = d.u32(), d.u32(), d.u32()
	h.Notifications, h.Receipts, h.Relations = d.u32(), d.u32(), d.u32()
	h.MaxRunGeneration, h.MaxReceiptGeneration = d.u64(), d.u64()
	totalLen := d.u64()
	if d.err != nil {
		return nil, h, d.err
	}
	if totalLen != uint64(len(data)) {
		return nil, h, sidecarCorruptf("recorded length %d, file has %d", totalLen, len(data))
	}
	if h.OwnOrdinal != 0 || h.OwnName != sidecarLegacyName {
		return nil, h, sidecarCorruptf("sidecar belongs to segment %d %q", h.OwnOrdinal, h.OwnName)
	}
	for k := fileKind(0); k < kindCount; k++ {
		if h.Applied[k] < 0 || h.Lines[k] < 0 || (h.Applied[k] == 0) != (h.Lines[k] == 0) {
			return nil, h, sidecarCorruptf("%s: applied %d bytes with %d lines", kindFilename[k], h.Applied[k], h.Lines[k])
		}
	}

	ix := newTaskIndex("")
	ix.applied, ix.lines = h.Applied, h.Lines
	ix.maxRunGeneration, ix.maxReceiptGeneration = h.MaxRunGeneration, h.MaxReceiptGeneration

	d.strings(&ix.titles)
	d.strings(&ix.assignees)
	d.strings(&ix.modules)
	d.strings(&ix.enums)

	if int(h.Tasks) > d.remaining()/sidecarTaskRecLen || int(h.Runs) > d.remaining()/sidecarRunRecLen {
		return nil, h, sidecarCorruptf("record counts exceed the file")
	}
	ix.tasks = make([]taskRec, h.Tasks)
	ix.taskMap = make(map[key16]uint32, h.Tasks)
	for i := range ix.tasks {
		r := &ix.tasks[i]
		r.key = d.key()
		r.updated = d.stamp()
		r.title, r.assignee, r.module = d.u32(), d.u32(), d.u32()
		r.stateHead, r.stateTail, r.ctxHead, r.ctxTail, r.runHead = d.u32(), d.u32(), d.u32(), d.u32(), d.u32()
		r.route, r.status, r.priority, r.interrupt = d.u16(), d.u16(), d.u16(), d.u16()
		r.flags = d.u8()
		if d.err == nil {
			if _, dup := ix.taskMap[r.key]; dup {
				return nil, h, sidecarCorruptf("task key repeats at slot %d", i)
			}
			ix.taskMap[r.key] = uint32(i)
		}
	}
	ix.runs = make([]runRec, h.Runs)
	ix.runMap = make(map[key16]uint32, h.Runs)
	for i := range ix.runs {
		r := &ix.runs[i]
		r.key = d.key()
		r.generation = d.u64()
		r.started = d.stamp()
		r.taskSlot, r.assignee, r.head, r.tail, r.nextRun = d.u32(), d.u32(), d.u32(), d.u32(), d.u32()
		r.status, r.reason = d.u16(), d.u16()
		r.flags = d.u8()
		if d.err == nil {
			if _, dup := ix.runMap[r.key]; dup {
				return nil, h, sidecarCorruptf("run key repeats at slot %d", i)
			}
			ix.runMap[r.key] = uint32(i)
		}
	}
	if int(h.LineTable) > d.remaining()/20 {
		return nil, h, sidecarCorruptf("line table does not fit in the file")
	}
	n := int(h.LineTable)
	ix.lt.off = make([]uint64, n)
	ix.lt.length = make([]uint32, n)
	ix.lt.crc = make([]uint32, n)
	ix.lt.next = make([]uint32, n)
	for i := range ix.lt.off {
		ix.lt.off[i] = d.u64()
	}
	for i := range ix.lt.length {
		ix.lt.length[i] = d.u32()
	}
	for i := range ix.lt.crc {
		ix.lt.crc[i] = d.u32()
	}
	for i := range ix.lt.next {
		ix.lt.next[i] = d.u32()
	}
	if int(h.Notifications) > d.remaining()/sidecarNotifRecLen {
		return nil, h, sidecarCorruptf("notification count exceeds the file")
	}
	ix.notifs = make([]notifRec, h.Notifications)
	for i := range ix.notifs {
		r := &ix.notifs[i]
		r.created = d.stamp()
		r.key = d.key()
		r.pos = d.pos()
		r.interrupt = d.u8() == 1
	}
	for i := uint32(0); i < h.Receipts && d.err == nil; i++ {
		id := d.str()
		rec := receiptRec{generation: d.u64(), pos: d.pos()}
		if d.err != nil {
			break
		}
		if _, dup := ix.receipts[id]; dup || id == "" {
			return nil, h, sidecarCorruptf("receipt operation id is empty or repeats")
		}
		ix.receipts[id] = rec
	}
	for i := uint32(0); i < h.Relations && d.err == nil; i++ {
		slot := d.u32()
		rel := taskRelations{parent: modulecore.TaskID(d.str()), supersedes: modulecore.TaskID(d.str())}
		deps := d.count(4)
		for j := 0; j < deps && d.err == nil; j++ {
			rel.deps = append(rel.deps, modulecore.TaskID(d.str()))
		}
		if d.err != nil {
			break
		}
		if _, dup := ix.relations[slot]; dup {
			return nil, h, sidecarCorruptf("relations repeat for slot %d", slot)
		}
		ix.relations[slot] = rel
	}
	if d.err != nil {
		return nil, h, d.err
	}
	if d.remaining() != 0 {
		return nil, h, sidecarCorruptf("%d unread bytes before the trailer", d.remaining())
	}
	if err := ix.validateStructure(); err != nil {
		return nil, h, err
	}
	return ix, h, nil
}

// validateStructure checks the references a decoded index holds against each
// other: ranges, intern ids, the chains of lines and Runs, and that nothing is
// shared or left over. It reads only the index, never the log.
func (ix *taskIndex) validateStructure() error {
	nTasks, nRuns, nLines := uint32(len(ix.tasks)), uint32(len(ix.runs)), uint32(len(ix.lt.off))
	var maxApplied int64
	for _, v := range ix.applied {
		if v > maxApplied {
			maxApplied = v
		}
	}
	for i := range ix.lt.off {
		if ix.lt.off[i] > uint64(maxApplied) || uint64(ix.lt.length[i])+1 > uint64(maxApplied)-ix.lt.off[i] {
			return sidecarCorruptf("line %d lies beyond every committed prefix", i+1)
		}
		if ix.lt.next[i] > nLines {
			return sidecarCorruptf("line %d links to line %d of %d", i+1, ix.lt.next[i], nLines)
		}
	}
	walk := func(head, tail uint32, visited *uint32) (count int, err error) {
		last := uint32(0)
		for cur := head; cur != 0; cur = ix.lt.next[cur-1] {
			if cur > nLines {
				return 0, sidecarCorruptf("chain reaches line %d of %d", cur, nLines)
			}
			*visited++
			if *visited > nLines {
				return 0, sidecarCorruptf("lines are chained twice or in a cycle")
			}
			count++
			last = cur
		}
		if last != tail {
			return 0, sidecarCorruptf("chain ends at line %d, record says %d", last, tail)
		}
		return count, nil
	}
	enumOK := func(id uint16) bool { return int(id) < max(len(ix.enums.vals), 1) }
	var visited, stateLines, ctxLines, runLines uint32
	var runsVisited uint32
	for slot := uint32(0); slot < nTasks; slot++ {
		r := &ix.tasks[slot]
		if r.stateHead == 0 || r.stateTail == 0 {
			return sidecarCorruptf("task slot %d has no state line", slot)
		}
		count, err := walk(r.stateHead, r.stateTail, &visited)
		if err != nil {
			return err
		}
		stateLines += uint32(count)
		if (r.ctxHead == 0) != (r.ctxTail == 0) {
			return sidecarCorruptf("task slot %d has a half-set context chain", slot)
		}
		if r.ctxHead != 0 {
			count, err := walk(r.ctxHead, r.ctxTail, &visited)
			if err != nil {
				return err
			}
			ctxLines += uint32(count)
		}
		for run := r.runHead; run != 0; run = ix.runs[run-1].nextRun {
			if run > nRuns {
				return sidecarCorruptf("task slot %d lists run %d of %d", slot, run, nRuns)
			}
			if ix.runs[run-1].taskSlot != slot {
				return sidecarCorruptf("run %d is listed by task slot %d but belongs to %d", run, slot, ix.runs[run-1].taskSlot)
			}
			runsVisited++
			if runsVisited > nRuns {
				return sidecarCorruptf("runs are listed twice or in a cycle")
			}
		}
		if int(r.title) >= max(len(ix.titles.vals), 1) || int(r.assignee) >= max(len(ix.assignees.vals), 1) || int(r.module) >= max(len(ix.modules.vals), 1) {
			return sidecarCorruptf("task slot %d names a string that is not in the table", slot)
		}
		if !enumOK(r.route) || !enumOK(r.status) || !enumOK(r.priority) || !enumOK(r.interrupt) {
			return sidecarCorruptf("task slot %d names an enumerated value that is not in the table", slot)
		}
		_, hasRelations := ix.relations[slot]
		if (r.flags&flagRelations != 0) != hasRelations {
			return sidecarCorruptf("task slot %d: relations flag and relations table disagree", slot)
		}
	}
	for slot := uint32(0); slot < nRuns; slot++ {
		r := &ix.runs[slot]
		if r.taskSlot >= nTasks {
			return sidecarCorruptf("run slot %d belongs to task slot %d of %d", slot, r.taskSlot, nTasks)
		}
		if r.head == 0 || r.tail == 0 {
			return sidecarCorruptf("run slot %d has no run line", slot)
		}
		count, err := walk(r.head, r.tail, &visited)
		if err != nil {
			return err
		}
		runLines += uint32(count)
		if r.nextRun > nRuns {
			return sidecarCorruptf("run slot %d links to run %d of %d", slot, r.nextRun, nRuns)
		}
		if int(r.assignee) >= max(len(ix.assignees.vals), 1) || !enumOK(r.status) || !enumOK(r.reason) {
			return sidecarCorruptf("run slot %d names a value that is not in the table", slot)
		}
	}
	if visited != nLines {
		return sidecarCorruptf("%d of %d line-table entries belong to no Task or Run", nLines-visited, nLines)
	}
	if runsVisited != nRuns {
		return sidecarCorruptf("%d of %d runs belong to no Task", nRuns-runsVisited, nRuns)
	}
	for _, check := range []struct {
		kind  fileKind
		count int64
	}{
		{kindState, int64(stateLines)}, {kindRun, int64(runLines)}, {kindContext, int64(ctxLines)},
		{kindNotification, int64(len(ix.notifs))}, {kindReceipt, int64(len(ix.receipts))},
	} {
		if check.count > ix.lines[check.kind] {
			return sidecarCorruptf("%s: %d records in the index but only %d lines", kindFilename[check.kind], check.count, ix.lines[check.kind])
		}
	}
	for slot := range ix.relations {
		if slot >= nTasks {
			return sidecarCorruptf("relations for task slot %d of %d", slot, nTasks)
		}
	}
	for _, n := range ix.notifs {
		if n.pos.off > uint64(maxApplied) || uint64(n.pos.length)+1 > uint64(maxApplied)-n.pos.off {
			return sidecarCorruptf("a notification lies beyond every committed prefix")
		}
	}
	for _, rec := range ix.receipts {
		if rec.pos.off > uint64(maxApplied) || uint64(rec.pos.length)+1 > uint64(maxApplied)-rec.pos.off {
			return sidecarCorruptf("a receipt lies beyond every committed prefix")
		}
	}
	return nil
}

// equivalentIndex reports the first difference between two quiescent indexes
// as the store sees it: same files absorbed, same IDs, same summaries (compared
// by value, because intern ids depend on the order records arrived in) and the
// same positions for every line. Slot and chain numbering may differ between an
// index built incrementally and one built from the log in one pass; neither is
// visible to a reader, so neither is compared.
func equivalentIndex(a, b *taskIndex) error {
	if a.applied != b.applied {
		return fmt.Errorf("applied bytes differ: %v vs %v", a.applied, b.applied)
	}
	if a.lines != b.lines {
		return fmt.Errorf("line counts differ: %v vs %v", a.lines, b.lines)
	}
	if a.maxRunGeneration != b.maxRunGeneration || a.maxReceiptGeneration != b.maxReceiptGeneration {
		return fmt.Errorf("writer generations differ: run %d/%d receipt %d/%d", a.maxRunGeneration, b.maxRunGeneration, a.maxReceiptGeneration, b.maxReceiptGeneration)
	}
	if len(a.tasks) != len(b.tasks) || len(a.taskMap) != len(b.taskMap) {
		return fmt.Errorf("task counts differ: %d/%d", len(a.tasks), len(b.tasks))
	}
	if len(a.runs) != len(b.runs) || len(a.runMap) != len(b.runMap) {
		return fmt.Errorf("run counts differ: %d/%d", len(a.runs), len(b.runs))
	}
	if len(a.notifs) != len(b.notifs) || len(a.receipts) != len(b.receipts) || len(a.relations) != len(b.relations) {
		return fmt.Errorf("notification/receipt/relation counts differ: %d/%d %d/%d %d/%d", len(a.notifs), len(b.notifs), len(a.receipts), len(b.receipts), len(a.relations), len(b.relations))
	}
	if len(a.lt.off) != len(b.lt.off) {
		return fmt.Errorf("line table sizes differ: %d/%d", len(a.lt.off), len(b.lt.off))
	}
	for key, slotA := range a.taskMap {
		slotB, ok := b.taskMap[key]
		if !ok {
			return fmt.Errorf("task %s is missing from the second index", key.id(taskKeyPrefix))
		}
		ra, rb := &a.tasks[slotA], &b.tasks[slotB]
		id := key.id(taskKeyPrefix)
		if ra.updated != rb.updated || ra.flags != rb.flags {
			return fmt.Errorf("task %s: updated/flags differ", id)
		}
		if a.titles.value(ra.title) != b.titles.value(rb.title) || a.assignees.value(ra.assignee) != b.assignees.value(rb.assignee) || a.modules.value(ra.module) != b.modules.value(rb.module) {
			return fmt.Errorf("task %s: title/assignee/module differ", id)
		}
		if a.enums.value(uint32(ra.route)) != b.enums.value(uint32(rb.route)) || a.enums.value(uint32(ra.status)) != b.enums.value(uint32(rb.status)) ||
			a.enums.value(uint32(ra.priority)) != b.enums.value(uint32(rb.priority)) || a.enums.value(uint32(ra.interrupt)) != b.enums.value(uint32(rb.interrupt)) {
			return fmt.Errorf("task %s: route/status/priority/interrupt differ", id)
		}
		if !samePositions(a.lt.positions(ra.stateHead), b.lt.positions(rb.stateHead)) {
			return fmt.Errorf("task %s: state lines differ", id)
		}
		if !samePositions(a.lt.positions(ra.ctxHead), b.lt.positions(rb.ctxHead)) {
			return fmt.Errorf("task %s: context lines differ", id)
		}
		runsA, runsB := a.runKeys(ra), b.runKeys(rb)
		if len(runsA) != len(runsB) {
			return fmt.Errorf("task %s: run lists differ", id)
		}
		for i := range runsA {
			if runsA[i] != runsB[i] {
				return fmt.Errorf("task %s: run lists differ", id)
			}
		}
		relA, hasA := a.relations[slotA]
		relB, hasB := b.relations[slotB]
		if hasA != hasB || relA.parent != relB.parent || relA.supersedes != relB.supersedes || len(relA.deps) != len(relB.deps) {
			return fmt.Errorf("task %s: relations differ", id)
		}
		for i := range relA.deps {
			if relA.deps[i] != relB.deps[i] {
				return fmt.Errorf("task %s: dependencies differ", id)
			}
		}
	}
	for key, slotA := range a.runMap {
		slotB, ok := b.runMap[key]
		if !ok {
			return fmt.Errorf("run %s is missing from the second index", key.id(runKeyPrefix))
		}
		ra, rb := &a.runs[slotA], &b.runs[slotB]
		id := key.id(runKeyPrefix)
		if ra.generation != rb.generation || ra.started != rb.started || ra.flags != rb.flags {
			return fmt.Errorf("run %s: generation/started/flags differ", id)
		}
		if a.assignees.value(ra.assignee) != b.assignees.value(rb.assignee) || a.enums.value(uint32(ra.status)) != b.enums.value(uint32(rb.status)) || a.enums.value(uint32(ra.reason)) != b.enums.value(uint32(rb.reason)) {
			return fmt.Errorf("run %s: assignee/status/reason differ", id)
		}
		if a.tasks[ra.taskSlot].key != b.tasks[rb.taskSlot].key {
			return fmt.Errorf("run %s: owner task differs", id)
		}
		if !samePositions(a.lt.positions(ra.head), b.lt.positions(rb.head)) {
			return fmt.Errorf("run %s: run lines differ", id)
		}
	}
	for i := range a.notifs {
		if a.notifs[i] != b.notifs[i] {
			return fmt.Errorf("notification %d differs", i)
		}
	}
	for id, recA := range a.receipts {
		if recB, ok := b.receipts[id]; !ok || recA != recB {
			return fmt.Errorf("receipt %q differs", id)
		}
	}
	return nil
}

// positions lists the lines of one chain in log order. A chain longer than the
// table is a cycle and is cut short so a comparison cannot loop.
func (t *lineTable) positions(head uint32) []linePos {
	var out []linePos
	for cur := head; cur != 0 && len(out) <= len(t.off); cur = t.next[cur-1] {
		out = append(out, t.pos(cur))
	}
	return out
}

func samePositions(a, b []linePos) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// runKeys lists the keys of the Runs of a Task in the order the index chains them.
func (ix *taskIndex) runKeys(rec *taskRec) []key16 {
	var out []key16
	for run := rec.runHead; run != 0 && len(out) <= len(ix.runs); run = ix.runs[run-1].nextRun {
		out = append(out, ix.runs[run-1].key)
	}
	return out
}
