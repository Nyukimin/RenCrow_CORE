package task

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
)

// buildChunkBytes is how much of a file one build step absorbs. A larger step
// means fewer merges; a smaller one keeps the transient decode state small.
const buildChunkBytes = 8 << 20

// buildTaskIndex opens the log files of root and absorbs all of them. It runs
// once at open, before the store is shared, with the writer lock held and the
// WAL settled, so every byte on disk is committed. Files are absorbed one at a
// time in dependency order (state, run, context, notifications, receipts) so a
// record never refers to something not yet read.
func buildTaskIndex(root string) (*taskIndex, error) {
	return buildTaskIndexFrom(root, false)
}

// buildTaskIndexFrom is buildTaskIndex for a caller that may find a file
// missing (a reader opened before the writer created the receipt log): a missing
// file is an empty one.
func buildTaskIndexFrom(root string, allowMissing bool) (*taskIndex, error) {
	ix := newTaskIndex(root)
	if err := ix.openFiles(allowMissing); err != nil {
		return nil, err
	}
	if err := ix.absorbAll(); err != nil {
		_ = ix.close()
		return nil, err
	}
	ix.source = sourceBuilt
	return ix, nil
}

// openFiles opens the log files the index reads lines from.
func (ix *taskIndex) openFiles(allowMissing bool) error {
	for kind := fileKind(0); kind < kindCount; kind++ {
		file, err := os.Open(filepath.Join(ix.root, kindFilename[kind]))
		if err != nil {
			if allowMissing && errors.Is(err, os.ErrNotExist) {
				continue
			}
			_ = ix.close()
			return fmt.Errorf("open %s for the task index: %w", kindFilename[kind], err)
		}
		ix.files[kind] = file
	}
	return nil
}

// absorbAll absorbs every file from the indexed prefix to its end: the whole
// file for a new index, the tail written after a checkpoint for a restored one.
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

// logLoaded writes the one line that says how the index came to be.
func (ix *taskIndex) logLoaded(started time.Time, replayed int64) {
	stats := ix.stats()
	var lines int64
	for _, n := range stats.Lines {
		lines += n
	}
	rebuilt := 0
	if stats.Source == sourceRebuilt {
		rebuilt = 1
	}
	log.Printf("[TaskStore] index loaded segs=1 tasks=%d runs=%d notifications=%d receipts=%d lines=%d heap=%d dur=%s source=%s replayed=%d rebuilt=%d",
		stats.Tasks, stats.Runs, stats.Notifications, stats.Receipts, lines, stats.ApproxBytes,
		time.Since(started).Round(time.Millisecond), stats.Source, replayed, rebuilt)
}

// absorbFile reads one whole file in line-aligned chunks and absorbs each.
func (ix *taskIndex) absorbFile(kind fileKind) error {
	file := ix.files[kind]
	if file == nil {
		return nil
	}
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
		if ix.files[kind] == nil {
			continue
		}
		info, err := ix.files[kind].Stat()
		if err != nil {
			return fmt.Errorf("stat %s for the task index: %w", kindFilename[kind], err)
		}
		size, applied := info.Size(), ix.applied[kind]
		if size < applied {
			return fmt.Errorf("%w: %s is %d bytes, shorter than the %d indexed bytes", ErrIndexInconsistent, kindFilename[kind], size, applied)
		}
		if size == applied {
			continue
		}
		data := make([]byte, size-applied)
		if _, err := ix.files[kind].ReadAt(data, applied); err != nil && !errors.Is(err, io.EOF) {
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

// onCommit is the jsonlbatch post-commit hook. The commit is durable, so it
// cannot fail the transaction: if the index cannot absorb the bytes it only
// remembers to resynchronize from the files.
func (ix *taskIndex) onCommit(_ context.Context, appends []jsonlbatch.CommittedAppend) {
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
	if err := ix.apply(chunks); err != nil {
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
