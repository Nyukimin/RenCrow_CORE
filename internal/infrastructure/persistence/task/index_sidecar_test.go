package task

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"sync"
	"testing"
)

// sidecarCorpus drives the oracle world so the index holds every kind of record
// the production log has: Tasks with relations, Runs, contexts, notifications
// and operation receipts. The returned index is the live one, built
// incrementally by the post-commit hook.
func sidecarCorpus(t *testing.T, seed int64, steps int) (*taskIndex, string) {
	t.Helper()
	world := newOracleWorld(t, seed)
	for i := 0; i < steps; i++ {
		world.step()
	}
	return world.pair.indexed.idx, world.pair.rootIndex
}

var (
	sharedCorpusOnce  sync.Once
	sharedCorpusIndex *taskIndex
)

// sharedCorpus returns one index built once per test binary: building a corpus
// replays a random workload against two stores, which is the slow part. The
// index is only read from memory by its users (its files are long closed).
func sharedCorpus(t *testing.T) *taskIndex {
	t.Helper()
	sharedCorpusOnce.Do(func() { sharedCorpusIndex, _ = sidecarCorpus(t, 11, 140) })
	if sharedCorpusIndex == nil {
		t.Fatal("shared corpus was not built")
	}
	return sharedCorpusIndex
}

// resealSidecar recomputes the CRC32C trailer so a test can change the body
// and still reach the structural checks behind the checksum.
func resealSidecar(data []byte) []byte {
	out := append([]byte(nil), data...)
	body := len(out) - 8
	binary.LittleEndian.PutUint32(out[body:], crc32.Checksum(out[:body], castagnoli))
	return out
}

func TestSidecarRoundTripPreservesTheIndex(t *testing.T) {
	live, root := sidecarCorpus(t, 11, 140)
	if len(live.tasks) == 0 || len(live.runs) == 0 || len(live.notifs) == 0 || len(live.receipts) == 0 || len(live.relations) == 0 {
		t.Fatalf("corpus is too thin: tasks=%d runs=%d notifs=%d receipts=%d relations=%d", len(live.tasks), len(live.runs), len(live.notifs), len(live.receipts), len(live.relations))
	}
	data := encodeSidecar(live.snapshot())
	restored, header, err := decodeSidecar(data)
	if err != nil {
		t.Fatalf("decode own encoding: %v", err)
	}
	if err := equivalentIndex(live, restored); err != nil {
		t.Fatalf("restored index differs from the live one: %v", err)
	}
	rebuilt, err := buildTaskIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rebuilt.close()
	if err := equivalentIndex(live, rebuilt); err != nil {
		t.Fatalf("the live index and a rebuild from the log are not equivalent (comparison is too strict): %v", err)
	}
	if err := equivalentIndex(restored, rebuilt); err != nil {
		t.Fatalf("restored index differs from a rebuild: %v", err)
	}
	if !bytes.Equal(data, encodeSidecar(restored.snapshot())) {
		t.Fatal("encoding is not deterministic: re-encoding the restored index gave other bytes")
	}
	if header.Version != 1 || header.OwnOrdinal != 0 || header.OwnName != "legacy" || header.Flags != 0 {
		t.Fatalf("header identity = %+v", header)
	}
	if header.Applied != live.applied || header.Lines != live.lines {
		t.Fatalf("header offsets/lines = %v/%v, index has %v/%v", header.Applied, header.Lines, live.applied, live.lines)
	}
	if header.MaxRunGeneration != live.maxRunGeneration || header.MaxReceiptGeneration != live.maxReceiptGeneration {
		t.Fatalf("header generations = %d/%d, index has %d/%d", header.MaxRunGeneration, header.MaxReceiptGeneration, live.maxRunGeneration, live.maxReceiptGeneration)
	}
}

func TestSidecarOfAnEmptyIndexRoundTrips(t *testing.T) {
	empty := newTaskIndex(t.TempDir())
	data := encodeSidecar(empty.snapshot())
	restored, _, err := decodeSidecar(data)
	if err != nil {
		t.Fatalf("decode empty index: %v", err)
	}
	if err := equivalentIndex(empty, restored); err != nil {
		t.Fatal(err)
	}
}

func TestSidecarRejectsEveryDamagedForm(t *testing.T) {
	live := sharedCorpus(t)
	data := encodeSidecar(live.snapshot())
	if _, _, err := decodeSidecar(data); err != nil {
		t.Fatalf("baseline decode: %v", err)
	}
	flip := func(at int) []byte {
		out := append([]byte(nil), data...)
		out[at] ^= 0x10
		return out
	}
	cases := map[string][]byte{
		"empty":        nil,
		"one byte":     data[:1],
		"header only":  data[:40],
		"cut in half":  data[:len(data)/2],
		"cut one byte": data[:len(data)-1],
		"extra byte":   append(append([]byte(nil), data...), 0),
		"first byte":   flip(0),
		"header byte":  flip(20),
		"crc byte":     flip(len(data) - 6),
		"end magic":    flip(len(data) - 2),
	}
	for i := 1; i <= 20; i++ {
		cases["body flip "+string(rune('a'+i))] = flip(len(data) * i / 22)
	}
	for name, damaged := range cases {
		if _, _, err := decodeSidecar(damaged); !errors.Is(err, errSidecarCorrupt) {
			t.Errorf("%s: decode error = %v, want errSidecarCorrupt", name, err)
		}
	}
}

func TestSidecarOfAnotherVersionIsNotAccepted(t *testing.T) {
	live := sharedCorpus(t)
	data := encodeSidecar(live.snapshot())
	for name, mutate := range map[string]func([]byte){
		"future version": func(b []byte) { binary.LittleEndian.PutUint16(b[4:], 2) },
		"version zero":   func(b []byte) { binary.LittleEndian.PutUint16(b[4:], 0) },
		"unknown flags":  func(b []byte) { binary.LittleEndian.PutUint16(b[6:], 1) },
	} {
		damaged := append([]byte(nil), data...)
		mutate(damaged)
		// A matching checksum isolates the version check from the CRC check.
		if _, _, err := decodeSidecar(resealSidecar(damaged)); !errors.Is(err, errSidecarVersion) {
			t.Errorf("%s: decode error = %v, want errSidecarVersion", name, err)
		}
	}
	other := append([]byte(nil), data...)
	copy(other[0:4], "XXXX")
	if _, _, err := decodeSidecar(resealSidecar(other)); !errors.Is(err, errSidecarCorrupt) {
		t.Errorf("wrong magic: decode error = %v, want errSidecarCorrupt", err)
	}
}

// A sidecar whose checksum holds but whose references are impossible (a bug in
// a future writer, or a deliberate edit) must be refused instead of indexed:
// the loader never trusts the checksum alone.
func TestSidecarRefusesImpossibleReferencesBehindAValidChecksum(t *testing.T) {
	live := sharedCorpus(t)
	cases := map[string]func(s *indexSnapshot){
		"state tail beyond the line table": func(s *indexSnapshot) { s.tasks[0].stateTail = uint32(len(s.lineOff)) + 5 },
		"state head is zero":               func(s *indexSnapshot) { s.tasks[0].stateHead = 0 },
		"task slot of a run out of range":  func(s *indexSnapshot) { s.runs[0].taskSlot = uint32(len(s.tasks)) },
		"chain cycle": func(s *indexSnapshot) {
			head := s.tasks[0].stateHead
			s.lineNext = append([]uint32(nil), s.lineNext...)
			s.lineNext[head-1] = head
		},
		"line shared by two chains": func(s *indexSnapshot) { s.tasks[1].stateHead = s.tasks[0].stateHead },
		"duplicate task key":        func(s *indexSnapshot) { s.tasks[1].key = s.tasks[0].key },
		"duplicate run key":         func(s *indexSnapshot) { s.runs[1].key = s.runs[0].key },
		"title id out of range":     func(s *indexSnapshot) { s.tasks[0].title = uint32(len(s.titles)) + 3 },
		"enum id out of range":      func(s *indexSnapshot) { s.tasks[0].status = uint16(len(s.enums)) + 3 },
		"relation slot out of range": func(s *indexSnapshot) {
			s.relations = map[uint32]taskRelations{uint32(len(s.tasks)) + 1: {}}
		},
		"line beyond every applied prefix": func(s *indexSnapshot) {
			s.lineOff = append([]uint64(nil), s.lineOff...)
			s.lineOff[0] = uint64(s.applied[kindState]+s.applied[kindRun]+s.applied[kindContext]+s.applied[kindNotification]+s.applied[kindReceipt]) + 1000
		},
		"run chain of a task skips a run": func(s *indexSnapshot) {
			for i := range s.tasks {
				if s.tasks[i].runHead != 0 {
					s.tasks[i].runHead = 0
					return
				}
			}
		},
		"flags promise relations that are missing": func(s *indexSnapshot) {
			for i := range s.tasks {
				if s.tasks[i].flags&flagRelations == 0 {
					s.tasks[i].flags |= flagRelations
					return
				}
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			snap := live.snapshot()
			mutate(snap)
			if _, _, err := decodeSidecar(encodeSidecar(snap)); !errors.Is(err, errSidecarCorrupt) {
				t.Fatalf("decode error = %v, want errSidecarCorrupt", err)
			}
		})
	}
}

func TestEquivalentIndexSeesEveryKindOfDifference(t *testing.T) {
	live := sharedCorpus(t)
	data := encodeSidecar(live.snapshot())
	fresh := func() *taskIndex {
		ix, _, err := decodeSidecar(data)
		if err != nil {
			t.Fatal(err)
		}
		return ix
	}
	cases := map[string]func(ix *taskIndex){
		"applied":            func(ix *taskIndex) { ix.applied[kindRun]++ },
		"lines":              func(ix *taskIndex) { ix.lines[kindContext]++ },
		"run generation":     func(ix *taskIndex) { ix.maxRunGeneration++ },
		"receipt generation": func(ix *taskIndex) { ix.maxReceiptGeneration++ },
		"task updated":       func(ix *taskIndex) { ix.tasks[0].updated.nsec++ },
		"task flag":          func(ix *taskIndex) { ix.tasks[0].flags ^= flagReadOnly },
		"task title":         func(ix *taskIndex) { ix.tasks[0].title = ix.titles.mustIntern(t, "another title") },
		"task line crc":      func(ix *taskIndex) { ix.lt.crc[ix.tasks[0].stateTail-1]++ },
		"run status":         func(ix *taskIndex) { ix.runs[0].status = ix.enums.mustEnum(t, "an unseen status") },
		"run generation id":  func(ix *taskIndex) { ix.runs[0].generation++ },
		"notification":       func(ix *taskIndex) { ix.notifs[0].pos.off++ },
		"missing notif":      func(ix *taskIndex) { ix.notifs = ix.notifs[1:] },
		"receipt":            func(ix *taskIndex) { ix.receipts["extra-operation"] = receiptRec{} },
		"relation": func(ix *taskIndex) {
			for slot, rel := range ix.relations {
				rel.parent += "x"
				ix.relations[slot] = rel
				return
			}
		},
		"a task is missing": func(ix *taskIndex) {
			ix.tasks = ix.tasks[:len(ix.tasks)-1]
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			other := fresh()
			if err := equivalentIndex(live, other); err != nil {
				t.Fatalf("baseline: %v", err)
			}
			mutate(other)
			if err := equivalentIndex(live, other); err == nil {
				t.Fatal("difference not reported")
			}
		})
	}
}

func (t *internTable) mustIntern(tb testing.TB, value string) uint32 {
	tb.Helper()
	id, err := t.intern(value)
	if err != nil {
		tb.Fatal(err)
	}
	return id
}

func (t *internTable) mustEnum(tb testing.TB, value string) uint16 {
	tb.Helper()
	return uint16(t.mustIntern(tb, value))
}
