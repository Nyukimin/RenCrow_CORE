package task

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Why a sidecar was not used and the log was read instead (index rebuild
// reason=...).
const (
	rebuildMissing = "missing" // no sidecar file
	rebuildCorrupt = "corrupt" // unreadable, torn, bit-flipped or inconsistent
	rebuildVersion = "version" // written in another format version
	rebuildSize    = "size"    // covers more bytes than a log file now holds
	rebuildCount   = "count"   // the log prefix it covers has another line count
	rebuildContent = "content" // the last line it recorded for a file is not what the file holds
	rebuildReplay  = "replay"  // the log written after it could not be absorbed
	rebuildLayout  = "layout"  // it describes another layout (or another segment 0) than the store has
)

// sidecarV2File is the sidecar of a store on layout v2.
const sidecarV2File = "tasks.tsi"

// unusableSidecar says why a sidecar cannot be restored. It is not a failure of
// the store: the caller rebuilds from the log.
type unusableSidecar struct{ reason, detail string }

func (e *unusableSidecar) Error() string { return e.reason + ": " + e.detail }

// sidecarPathOf is where the sidecar of the store at root lives while it is on
// layout v1.
func sidecarPathOf(root string) string {
	return filepath.Join(root, sidecarDirName, sidecarLegacyFile)
}

// sidecarPathFor is where the sidecar of the store at root lives under the given
// layout. On layout v2 the sidecar of segment 0 (seg-legacy.tsi) is no longer
// touched: it is what a return to layout v1 (layout concat) can start from.
func sidecarPathFor(root string, v2 bool) string {
	if v2 {
		return filepath.Join(root, sidecarDirName, sidecarV2File)
	}
	return sidecarPathOf(root)
}

// openLayout reads LAYOUT and lists the segments of the store at root. On layout
// v2 it also holds segment 0 against the sizes LAYOUT recorded when it froze it,
// and refuses a store that has no segment under logs/ (LAYOUT is written after
// the first one exists). It is the one door every opener of a store goes through,
// so none of them can see a history that is not the recorded one.
func openLayout(root string) (layoutInfo, []segmentRef, error) {
	layout, err := readLayout(root)
	if err != nil {
		return layoutInfo{}, nil, err
	}
	refs, err := listSegments(root, layout.v2)
	if err != nil {
		return layoutInfo{}, nil, err
	}
	if layout.v2 {
		if len(refs) < 2 {
			return layoutInfo{}, nil, fmt.Errorf("%w: LAYOUT v2 is present but there is no segment under %s", ErrSegmentMissing, logsDirName)
		}
		if err := verifyFrozenSegmentZero(root, layout.frozen); err != nil {
			return layoutInfo{}, nil, err
		}
	}
	return layout, refs, nil
}

// openTaskIndex returns the index of the writable store at root. With persist
// the sidecar is restored when it is usable (and the log written after it is
// replayed); otherwise, or without persist, the index is built from the log.
// Either way the result is what a build from the log alone would give: the
// sidecar only shortens the way there.
func openTaskIndex(root string, layout layoutInfo, refs []segmentRef, persist bool, fp func(stage string)) (*taskIndex, error) {
	started := time.Now()
	if !persist {
		ix, err := buildTaskIndexOf(root, layout, refs, false)
		if err != nil {
			return nil, err
		}
		ix.logLoaded(started, 0)
		return ix, nil
	}
	ix, replayed, why, err := restoreTaskIndex(root, layout, refs, false, fp)
	if err != nil {
		return nil, err
	}
	if why == nil {
		ix.logLoaded(started, replayed)
		return ix, nil
	}
	return rebuildTaskIndex(root, layout, refs, false, why, started, false)
}

// rebuildTaskIndex reads the whole log because the sidecar could not be used.
// A reader (quiet) keeps the log for real problems; the writer reports every
// rebuild.
func rebuildTaskIndex(root string, layout layoutInfo, refs []segmentRef, allowMissing bool, why *unusableSidecar, started time.Time, quiet bool) (*taskIndex, error) {
	rebuildStarted := time.Now()
	ix, err := buildTaskIndexOf(root, layout, refs, allowMissing)
	if err != nil {
		return nil, err
	}
	ix.source, ix.rebuildReason = sourceRebuilt, why.reason
	if quiet {
		return ix, nil
	}
	level := "index rebuild"
	if why.reason == rebuildSize {
		// The sidecar covers bytes the log no longer has: the log was cut short
		// or replaced. The index now says what the log says; a person should look.
		level = "WARN index rebuild"
	}
	log.Printf("[TaskStore] %s seg=%s lines=%d dur=%s reason=%s detail=%q",
		level, ix.segs[ix.cur].name, ix.totalLinesLocked(), time.Since(rebuildStarted).Round(time.Millisecond), why.reason, why.detail)
	ix.logLoaded(started, 0)
	return ix, nil
}

// restoreTaskIndex loads the sidecar of the store at root, verifies it against
// the log files and replays the log written after it. A nil index with a reason
// means the sidecar is unusable; an error means the store itself cannot be
// opened (a log line the index cannot absorb in a file it must read anyway, or a
// closed segment that is not what was recorded).
func restoreTaskIndex(root string, layout layoutInfo, refs []segmentRef, allowMissingFiles bool, fp func(stage string)) (ix *taskIndex, replayed int64, why *unusableSidecar, err error) {
	// The sidecar is read whole and closed at once: nothing keeps it open, so
	// the next checkpoint can replace it on Windows too.
	data, readErr := os.ReadFile(sidecarPathFor(root, layout.v2))
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return nil, 0, &unusableSidecar{rebuildMissing, "no sidecar file"}, nil
		}
		return nil, 0, &unusableSidecar{rebuildCorrupt, "read: " + readErr.Error()}, nil
	}
	ix, h, decodeErr := decodeSidecar(data)
	if decodeErr != nil {
		reason := rebuildCorrupt
		if errors.Is(decodeErr, errSidecarVersion) {
			reason = rebuildVersion
		}
		return nil, 0, &unusableSidecar{reason, decodeErr.Error()}, nil
	}
	if (h.Version == sidecarVersionV2) != layout.v2 {
		return nil, 0, &unusableSidecar{rebuildLayout, fmt.Sprintf("sidecar version %d, store layout v2=%v", h.Version, layout.v2)}, nil
	}
	ix.root = root
	ix.allowMissing = allowMissingFiles
	if !layout.v2 {
		ix.segs[0].dir = root
		ix.fc.setDir(0, root)
	}
	if layout.v2 {
		if unusable, err := ix.attachSegments(layout, refs); err != nil || unusable != nil {
			_ = ix.close()
			return nil, 0, unusable, err
		}
	}
	if unusable := ix.checkAgainstLog(); unusable != nil {
		_ = ix.close()
		return nil, 0, unusable, nil
	}
	if unusable := ix.checkLastLines(); unusable != nil {
		_ = ix.close()
		return nil, 0, unusable, nil
	}
	if fp != nil {
		ix.replayHook = func(kind fileKind) { fp("replay-" + kindFilename[kind]) }
	}
	before := ix.totalLinesLocked()
	if err := ix.absorbAll(); err != nil {
		// The tail did not fit the restored state. The log alone decides: a build
		// from it either succeeds (the sidecar was the problem) or fails the same
		// way, and then the store is not opened.
		_ = ix.close()
		return nil, 0, &unusableSidecar{rebuildReplay, err.Error()}, nil
	}
	if err := ix.absorbSegmentsAfter(refs); err != nil {
		_ = ix.close()
		if errors.Is(err, ErrSealedSegmentChanged) {
			return nil, 0, nil, err
		}
		return nil, 0, &unusableSidecar{rebuildReplay, err.Error()}, nil
	}
	ix.replayHook = nil
	ix.source = sourceSidecar
	ix.replayedLines = ix.totalLinesLocked() - before
	return ix, ix.replayedLines, nil, nil
}

// attachSegments ties a decoded version 2 index to the segments on disk. The
// segments the sidecar lists must be the first ones on disk, under the same
// names: a missing or renamed one is a lost history and the store is not
// opened. A sidecar whose idea of segment 0 is not what LAYOUT recorded is
// merely unusable (LAYOUT is the record of segment 0). A closed segment whose
// files are not the sizes the sidecar recorded when it closed was written to
// afterwards: the store is not opened.
func (ix *taskIndex) attachSegments(layout layoutInfo, refs []segmentRef) (*unusableSidecar, error) {
	if int(ix.cur) >= len(refs) {
		return nil, fmt.Errorf("%w: the index has segment %d open but only %d segments exist", ErrSegmentMissing, ix.cur, len(refs))
	}
	for i := 1; i <= int(ix.cur); i++ {
		if refs[i].name != ix.segs[i].name {
			return nil, fmt.Errorf("%w: segment %d was %q when the index was written, it is now %q", ErrSegmentMissing, i, ix.segs[i].name, refs[i].name)
		}
	}
	if ix.segs[0].size != layout.frozen {
		return &unusableSidecar{rebuildLayout, "the sidecar's record of segment 0 differs from LAYOUT"}, nil
	}
	for i := 0; i <= int(ix.cur); i++ {
		ix.segs[i].dir = refs[i].dir
		ix.fc.setDir(refs[i].ord, refs[i].dir)
	}
	for i := 1; i < int(ix.cur); i++ {
		actual, err := segmentFileSizes(ix.segs[i].dir)
		if err != nil {
			return nil, err
		}
		if actual != ix.segs[i].size {
			return nil, fmt.Errorf("%w: segment %s is %v bytes, it was %v when the index recorded it as closed", ErrSealedSegmentChanged, ix.segs[i].name, actual, ix.segs[i].size)
		}
	}
	return nil, nil
}

func totalLines(lines [kindCount]int64) int64 {
	var total int64
	for _, n := range lines {
		total += n
	}
	return total
}

// checkAgainstLog compares what the sidecar claims about each file of the
// current segment (how many bytes it covers and how many lines those hold) with
// the file itself:
//   - the file is at least as long as the covered prefix,
//   - the prefix ends at a line end,
//   - the prefix holds exactly the recorded number of lines.
//
// The line count is the check that the sidecar and the log are the same
// history: reading the prefix costs one pass over the files (about the size of
// the log, no decoding), which is why it is done only on a restore. Closed
// segments are held to their recorded sizes instead (attachSegments).
func (ix *taskIndex) checkAgainstLog() *unusableSidecar {
	type result struct{ why *unusableSidecar }
	results := make([]result, kindCount)
	var wg sync.WaitGroup
	for kind := fileKind(0); kind < kindCount; kind++ {
		wg.Add(1)
		go func(kind fileKind) {
			defer wg.Done()
			results[kind].why = ix.checkFileAgainstLog(kind)
		}(kind)
	}
	wg.Wait()
	for _, r := range results {
		if r.why != nil {
			return r.why
		}
	}
	return nil
}

func (ix *taskIndex) checkFileAgainstLog(kind fileKind) *unusableSidecar {
	applied, lines := ix.applied[kind], ix.lines[kind]
	file, release, err := ix.fc.acquire(ix.cur, kind)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if applied != 0 {
				return &unusableSidecar{rebuildSize, fmt.Sprintf("%s is missing but the sidecar covers %d bytes", kindFilename[kind], applied)}
			}
			if !ix.allowMissing {
				return &unusableSidecar{rebuildCorrupt, fmt.Sprintf("open %s: %v", kindFilename[kind], err)}
			}
			return nil
		}
		return &unusableSidecar{rebuildCorrupt, fmt.Sprintf("open %s: %v", kindFilename[kind], err)}
	}
	defer release()
	info, err := file.Stat()
	if err != nil {
		return &unusableSidecar{rebuildCorrupt, fmt.Sprintf("stat %s: %v", kindFilename[kind], err)}
	}
	if info.Size() < applied {
		return &unusableSidecar{rebuildSize, fmt.Sprintf("%s holds %d bytes, the sidecar covers %d", kindFilename[kind], info.Size(), applied)}
	}
	if applied == 0 {
		return nil
	}
	var last [1]byte
	if _, err := file.ReadAt(last[:], applied-1); err != nil || last[0] != '\n' {
		return &unusableSidecar{rebuildCount, fmt.Sprintf("%s: the covered prefix of %d bytes does not end at a line end", kindFilename[kind], applied)}
	}
	got, err := countNewlines(file, applied)
	if err != nil {
		return &unusableSidecar{rebuildCorrupt, fmt.Sprintf("read %s: %v", kindFilename[kind], err)}
	}
	if got != lines {
		return &unusableSidecar{rebuildCount, fmt.Sprintf("%s: the covered prefix holds %d lines, the sidecar says %d", kindFilename[kind], got, lines)}
	}
	return nil
}

// checkLastLines reads back the last indexed line of every file and compares its
// CRC32C with the one the sidecar recorded. Line counts and sizes alone would
// accept a sidecar written for other bytes that happen to have the same shape (a
// log restored from another copy, a line rewritten in place); the last line of a
// file is the one a sidecar written for these bytes cannot get wrong, and
// reading five lines costs nothing. Positions order by segment first, so the
// last line of a kind is in the newest segment that has one.
func (ix *taskIndex) checkLastLines() *unusableSidecar {
	var last [kindCount]struct {
		pos linePos
		ok  bool
	}
	offer := func(kind fileKind, pos linePos) {
		if !last[kind].ok || pos.off > last[kind].pos.off {
			last[kind].pos, last[kind].ok = pos, true
		}
	}
	for i := range ix.tasks {
		offer(kindState, ix.lt.pos(ix.tasks[i].stateTail))
		if ix.tasks[i].ctxTail != 0 {
			offer(kindContext, ix.lt.pos(ix.tasks[i].ctxTail))
		}
	}
	for i := range ix.runs {
		offer(kindRun, ix.lt.pos(ix.runs[i].tail))
	}
	for i := range ix.notifs {
		offer(kindNotification, ix.notifs[i].pos)
	}
	for _, rec := range ix.receipts {
		offer(kindReceipt, rec.pos)
	}
	for kind := fileKind(0); kind < kindCount; kind++ {
		if !last[kind].ok {
			continue
		}
		if _, err := ix.readLine(kind, last[kind].pos); err != nil {
			return &unusableSidecar{rebuildContent, fmt.Sprintf("%s: the last indexed line is not the one the sidecar recorded: %v", kindFilename[kind], err)}
		}
	}
	ix.corruptReads.Store(0) // a failed probe here is not a corrupt read of the live index
	return nil
}

// countNewlines counts the '\n' bytes in the first n bytes of file.
func countNewlines(file *os.File, n int64) (int64, error) {
	const chunk = 4 << 20
	buf := make([]byte, chunk)
	var count, offset int64
	for offset < n {
		want := int64(chunk)
		if n-offset < want {
			want = n - offset
		}
		read, err := file.ReadAt(buf[:want], offset)
		if err != nil && !(errors.Is(err, io.EOF) && int64(read) == want) {
			return 0, err
		}
		count += int64(bytes.Count(buf[:read], []byte{'\n'}))
		offset += int64(read)
	}
	return count, nil
}
