package task

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
)

// buildChunkBytes is how much of a file one build step absorbs. A larger step
// means fewer merges; a smaller one keeps the transient decode state small.
const buildChunkBytes = 8 << 20

// buildTaskIndex builds the index of the store at root from its log. It runs
// once at open, before the store is shared, with the writer lock held and the
// WAL settled, so every byte on disk is committed. Within a segment the files are
// absorbed one at a time in dependency order (state, run, context, notifications,
// receipts) so a record never refers to something not yet read; segments are
// absorbed in ordinal order.
func buildTaskIndex(root string) (*taskIndex, error) {
	return buildTaskIndexFrom(root, false)
}

// buildTaskIndexFrom is buildTaskIndex for a caller that may find a file
// missing (a reader opened before the writer created the receipt log): a missing
// file is an empty one.
func buildTaskIndexFrom(root string, allowMissing bool) (*taskIndex, error) {
	layout, err := readLayout(root)
	if err != nil {
		return nil, err
	}
	refs, err := listSegments(root, layout.v2)
	if err != nil {
		return nil, err
	}
	return buildTaskIndexOf(root, layout, refs, allowMissing)
}

// buildTaskIndexOf is buildTaskIndexFrom for a layout and segment list the
// caller has already read.
func buildTaskIndexOf(root string, layout layoutInfo, refs []segmentRef, allowMissing bool) (*taskIndex, error) {
	ix := newTaskIndex(root)
	ix.allowMissing = allowMissing
	ix.initLayout(layout, refs[0])
	if err := ix.absorbAll(); err != nil {
		_ = ix.close()
		return nil, err
	}
	if err := ix.absorbSegmentsAfter(refs); err != nil {
		_ = ix.close()
		return nil, err
	}
	ix.source = sourceBuilt
	return ix, nil
}

// initLayout records the layout the index follows. On layout v2 the frozen sizes
// LAYOUT holds are the final sizes of segment 0.
func (ix *taskIndex) initLayout(layout layoutInfo, zero segmentRef) {
	ix.layoutV2 = layout.v2
	ix.segs[0] = segInfo{name: zero.name, dir: zero.dir}
	if layout.v2 {
		ix.segs[0].size = layout.frozen
	}
	ix.fc.setDir(0, zero.dir)
}

// absorbSegmentsAfter closes the current segment and absorbs every segment of
// refs that follows it, in order. The caller has already absorbed the current
// segment (to its end).
func (ix *taskIndex) absorbSegmentsAfter(refs []segmentRef) error {
	for i := int(ix.cur) + 1; i < len(refs); i++ {
		if refs[i].ord != uint16(i) {
			return fmt.Errorf("%w: segment %q has ordinal %d at position %d", ErrIndexInconsistent, refs[i].name, refs[i].ord, i)
		}
		if err := ix.closeCurrentSegment(refs[i]); err != nil {
			return err
		}
		if err := ix.absorbAll(); err != nil {
			return err
		}
	}
	return nil
}

// closeCurrentSegment ends the current segment and makes next the one the index
// follows. The final sizes of the segment that ends are read from its files and
// must be what the index has absorbed; the index starts the next segment empty.
// It takes applyMu and mu for the change.
func (ix *taskIndex) closeCurrentSegment(next segmentRef) error {
	ix.applyMu.Lock()
	defer ix.applyMu.Unlock()
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if int(next.ord) != len(ix.segs) || next.ord > maxSegmentOrdinal {
		return fmt.Errorf("%w: segment %q cannot follow %d segments", ErrIndexInconsistent, next.name, len(ix.segs))
	}
	done := &ix.segs[ix.cur]
	sizes, err := segmentFileSizes(done.dir)
	if err != nil {
		return fmt.Errorf("measure segment %s for the task index: %w", done.name, err)
	}
	if ix.layoutV2 && ix.cur == 0 && sizes != done.size {
		return fmt.Errorf("%w: segment 0 is not the size LAYOUT recorded (%v, now %v)", ErrSealedSegmentChanged, done.size, sizes)
	}
	for kind := fileKind(0); kind < kindCount; kind++ {
		if sizes[kind] != ix.applied[kind] {
			return fmt.Errorf("%w: segment %s file %s is %d bytes, the index absorbed %d", ErrIndexInconsistent, done.name, kindFilename[kind], sizes[kind], ix.applied[kind])
		}
	}
	done.size, done.lines = sizes, ix.lines
	ix.segs = append(ix.segs, segInfo{name: next.name, dir: next.dir})
	ix.fc.setDir(next.ord, next.dir)
	ix.cur = next.ord
	ix.applied, ix.lines = [kindCount]int64{}, [kindCount]int64{}
	return nil
}

// absorbAll absorbs every file of the current segment from the indexed prefix to
// its end: the whole file for a new index, the tail written after a checkpoint
// for a restored one.
func (ix *taskIndex) absorbAll() error {
	for kind := fileKind(0); kind < kindCount; kind++ {
		if err := ix.absorbFile(kind); err != nil {
			return err
		}
		if ix.replayHook != nil {
			ix.replayHook(kind)
		}
	}
	return nil
}

// openSegmentFile returns the read handle of kind in the current segment. A
// missing file is (nil, nil, nil) when the index allows it.
func (ix *taskIndex) openSegmentFile(ord uint16, kind fileKind) (*os.File, func(), error) {
	file, release, err := ix.fc.acquire(ord, kind)
	if err != nil {
		if ix.allowMissing && errors.Is(err, os.ErrNotExist) {
			return nil, func() {}, nil
		}
		return nil, nil, fmt.Errorf("open %s for the task index: %w", kindFilename[kind], err)
	}
	return file, release, nil
}

// logLoaded writes the one line that says how the index came to be.
func (ix *taskIndex) logLoaded(started time.Time, replayed int64) {
	stats := ix.stats()
	rebuilt := 0
	if stats.Source == sourceRebuilt {
		rebuilt = 1
	}
	log.Printf("[TaskStore] index loaded segs=%d tasks=%d runs=%d notifications=%d receipts=%d lines=%d heap=%d dur=%s source=%s replayed=%d rebuilt=%d",
		stats.Segments, stats.Tasks, stats.Runs, stats.Notifications, stats.Receipts, stats.TotalLines, stats.ApproxBytes,
		time.Since(started).Round(time.Millisecond), stats.Source, replayed, rebuilt)
}

// absorbFile reads one whole file in line-aligned chunks and absorbs each.
func (ix *taskIndex) absorbFile(kind fileKind) error {
	file, release, err := ix.openSegmentFile(ix.cur, kind)
	if err != nil {
		return err
	}
	if file == nil {
		return nil
	}
	defer release()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat %s for the task index: %w", kindFilename[kind], err)
	}
	size := info.Size()
	buf := make([]byte, 0, buildChunkBytes)
	offset := ix.applied[kind]
	if offset > size {
		return fmt.Errorf("%w: %s is %d bytes, shorter than the %d indexed bytes", ErrIndexInconsistent, kindFilename[kind], size, offset)
	}
	for offset < size {
		want := int64(buildChunkBytes)
		for {
			if remaining := size - offset; want > remaining {
				want = remaining
			}
			if cap(buf) < int(want) {
				buf = make([]byte, 0, want)
			}
			buf = buf[:want]
			if _, err := file.ReadAt(buf, offset); err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("read %s for the task index: %w", kindFilename[kind], err)
			}
			if end := bytes.LastIndexByte(buf, '\n'); end >= 0 {
				buf = buf[:end+1]
				break
			}
			if offset+want >= size {
				return fmt.Errorf("%w: %s ends with an unterminated line at %d", ErrIndexInconsistent, kindFilename[kind], offset)
			}
			want *= 2 // a single line longer than the chunk: widen until it fits
		}
		if err := ix.apply([]appendChunk{{kind: kind, offset: offset, data: buf}}); err != nil {
			return err
		}
		offset += int64(len(buf))
	}
	return nil
}

// ensureSynced brings the index up to the files. It must run with the OS lock of
// jsonlbatch held after the WAL was settled (the start of a Write callback or
// inside a Read), because only then is every byte on disk committed. Bytes
// beyond the indexed prefix are replayed through the same apply path as a
// commit; a file shorter than the indexed prefix is a log that lost committed
// data and fails closed.
func (ix *taskIndex) ensureSynced() error {
	ix.applyMu.Lock()
	defer ix.applyMu.Unlock()
	if ix.closed.Load() {
		return os.ErrClosed
	}
	chunks := make([]appendChunk, 0, int(kindCount))
	var behind int64
	for kind := fileKind(0); kind < kindCount; kind++ {
		file, release, err := ix.openSegmentFile(ix.cur, kind)
		if err != nil {
			return err
		}
		if file == nil {
			continue
		}
		info, err := file.Stat()
		if err != nil {
			release()
			return fmt.Errorf("stat %s for the task index: %w", kindFilename[kind], err)
		}
		size, applied := info.Size(), ix.applied[kind]
		if size < applied {
			release()
			return fmt.Errorf("%w: %s is %d bytes, shorter than the %d indexed bytes", ErrIndexInconsistent, kindFilename[kind], size, applied)
		}
		if size == applied {
			release()
			continue
		}
		data := make([]byte, size-applied)
		_, err = file.ReadAt(data, applied)
		release()
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read %s tail for the task index: %w", kindFilename[kind], err)
		}
		behind += int64(len(data))
		chunks = append(chunks, appendChunk{kind: kind, offset: applied, data: data})
	}
	if len(chunks) == 0 {
		ix.dirty.Store(false)
		return nil
	}
	if !ix.dirty.Load() {
		// Bytes the index never heard about although no write failed: something
		// other than this store appended to the log. Absorb them, but say so.
		log.Printf("[TaskStore] WARN index resync: %d committed bytes were not announced by a commit", behind)
	}
	b, err := ix.buildBatch(chunks)
	if err != nil {
		return err
	}
	if err := ix.merge(b); err != nil {
		return err
	}
	ix.dirty.Store(false)
	return nil
}

// onCommitFor is the jsonlbatch post-commit hook of the Store of segment ord. The
// segment is fixed in the closure: a hook never guesses which segment a commit
// belongs to from state that rotation changes.
func (ix *taskIndex) onCommitFor(ord uint16) jsonlbatch.PostCommitHook {
	return func(ctx context.Context, appends []jsonlbatch.CommittedAppend) { ix.onCommit(ctx, ord, appends) }
}

// onCommit absorbs a committed transaction of segment ord. The commit is
// durable, so it cannot fail the transaction: if the index cannot absorb the
// bytes it only remembers to resynchronize from the files.
func (ix *taskIndex) onCommit(_ context.Context, ord uint16, appends []jsonlbatch.CommittedAppend) {
	chunks := make([]appendChunk, 0, len(appends))
	for _, item := range appends {
		kind, indexed := kindOfFilename(item.Name)
		if !indexed {
			continue
		}
		chunks = append(chunks, appendChunk{kind: kind, offset: item.Offset, data: item.Payload})
	}
	if len(chunks) == 0 {
		return
	}
	if err := ix.applyTo(ord, chunks); err != nil {
		ix.dirty.Store(true)
		log.Printf("[TaskStore] WARN index could not absorb a committed transaction; it will resynchronize from the log: %v", err)
	}
}

// noteWriteResult records that a write ended without a known commit boundary.
func (ix *taskIndex) noteWriteResult(err error) {
	if err == nil {
		return
	}
	if errors.Is(err, jsonlbatch.ErrCommitUncertain) || errors.Is(err, jsonlbatch.ErrRecoveryRequired) {
		ix.dirty.Store(true)
	}
}
