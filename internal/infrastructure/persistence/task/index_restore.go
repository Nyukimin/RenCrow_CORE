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
)

// unusableSidecar says why a sidecar cannot be restored. It is not a failure of
// the store: the caller rebuilds from the log.
type unusableSidecar struct{ reason, detail string }

func (e *unusableSidecar) Error() string { return e.reason + ": " + e.detail }

// sidecarPathOf is where the sidecar of the store at root lives.
func sidecarPathOf(root string) string {
	return filepath.Join(root, sidecarDirName, sidecarLegacyFile)
}

// openTaskIndex returns the index of the writable store at root. With persist
// the sidecar is restored when it is usable (and the log written after it is
// replayed); otherwise, or without persist, the index is built from the log.
// Either way the result is what a build from the log alone would give: the
// sidecar only shortens the way there.
func openTaskIndex(root string, persist bool, fp func(stage string)) (*taskIndex, error) {
	started := time.Now()
	if !persist {
		ix, err := buildTaskIndex(root)
		if err != nil {
			return nil, err
		}
		ix.logLoaded(started, 0)
		return ix, nil
	}
	ix, replayed, why, err := restoreTaskIndex(root, false, fp)
	if err != nil {
		return nil, err
	}
	if why == nil {
		ix.logLoaded(started, replayed)
		return ix, nil
	}
	return rebuildTaskIndex(root, false, why, started, false)
}

// rebuildTaskIndex reads the whole log because the sidecar could not be used.
// A reader (quiet) keeps the log for real problems; the writer reports every
// rebuild.
func rebuildTaskIndex(root string, allowMissing bool, why *unusableSidecar, started time.Time, quiet bool) (*taskIndex, error) {
	rebuildStarted := time.Now()
	ix, err := buildTaskIndexFrom(root, allowMissing)
	if err != nil {
		return nil, err
	}
	ix.source, ix.rebuildReason = sourceRebuilt, why.reason
	if quiet {
		return ix, nil
	}
	var lines int64
	for _, n := range ix.lines {
		lines += n
	}
	level := "index rebuild"
	if why.reason == rebuildSize {
		// The sidecar covers bytes the log no longer has: the log was cut short
		// or replaced. The index now says what the log says; a person should look.
		level = "WARN index rebuild"
	}
	log.Printf("[TaskStore] %s seg=legacy lines=%d dur=%s reason=%s detail=%q",
		level, lines, time.Since(rebuildStarted).Round(time.Millisecond), why.reason, why.detail)
	ix.logLoaded(started, 0)
	return ix, nil
}

// restoreTaskIndex loads the sidecar of the store at root, verifies it against
// the log files and replays the log written after it. A nil index with a reason
// means the sidecar is unusable; an error means the store itself cannot be
// opened (a log line the index cannot absorb in a file it must read anyway).
func restoreTaskIndex(root string, allowMissingFiles bool, fp func(stage string)) (ix *taskIndex, replayed int64, why *unusableSidecar, err error) {
	// The sidecar is read whole and closed at once: nothing keeps it open, so
	// the next checkpoint can replace it on Windows too.
	data, readErr := os.ReadFile(sidecarPathOf(root))
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return nil, 0, &unusableSidecar{rebuildMissing, "no sidecar file"}, nil
		}
		return nil, 0, &unusableSidecar{rebuildCorrupt, "read: " + readErr.Error()}, nil
	}
	ix, _, decodeErr := decodeSidecar(data)
	if decodeErr != nil {
		reason := rebuildCorrupt
		if errors.Is(decodeErr, errSidecarVersion) {
			reason = rebuildVersion
		}
		return nil, 0, &unusableSidecar{reason, decodeErr.Error()}, nil
	}
	ix.root = root
	if err := ix.openFiles(allowMissingFiles); err != nil {
		return nil, 0, nil, err
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
	before := totalLines(ix.lines)
	if err := ix.absorbAll(); err != nil {
		// The tail did not fit the restored state. The log alone decides: a build
		// from it either succeeds (the sidecar was the problem) or fails the same
		// way, and then the store is not opened.
		_ = ix.close()
		return nil, 0, &unusableSidecar{rebuildReplay, err.Error()}, nil
	}
	ix.replayHook = nil
	ix.source = sourceSidecar
	ix.replayedLines = totalLines(ix.lines) - before
	return ix, ix.replayedLines, nil, nil
}

func totalLines(lines [kindCount]int64) int64 {
	var total int64
	for _, n := range lines {
		total += n
	}
	return total
}

// checkAgainstLog compares what the sidecar claims about each file (how many
// bytes it covers and how many lines those hold) with the file itself:
//   - the file is at least as long as the covered prefix,
//   - the prefix ends at a line end,
//   - the prefix holds exactly the recorded number of lines.
//
// The line count is the check that the sidecar and the log are the same
// history: reading the prefix costs one pass over the files (about the size of
// the log, no decoding), which is why it is done only on a restore.
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
	file := ix.files[kind]
	if file == nil {
		if applied != 0 {
			return &unusableSidecar{rebuildSize, fmt.Sprintf("%s is missing but the sidecar covers %d bytes", kindFilename[kind], applied)}
		}
		return nil
	}
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
// reading five lines costs nothing.
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
