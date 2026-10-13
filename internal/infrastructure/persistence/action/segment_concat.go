package action

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/segmentname"
)

// ErrStoreBusy means another writer holds the store (CORE is running).
var ErrStoreBusy = errors.New("action store is in use")

// concatLockWait is how long a concat waits for the lock of the newest segment
// before it concludes that a writer is running.
var concatLockWait = 5 * time.Second

// rolledBackLogsPrefix and rolledBackLayoutPrefix name what a concat sets aside.
// Nothing is deleted: the segments stay readable in the renamed directory.
const (
	rolledBackLogsPrefix   = logsDirName + ".rolled-back-"
	rolledBackLayoutPrefix = layoutFilename + ".rolled-back-"
	concatCopyChunk        = 1 << 20
)

// ConcatFile is the outcome of a concat for one of the two logs.
type ConcatFile struct {
	Name string `json:"name"`
	// FrozenSize is what segment 0's file held when it was frozen; AppendedBytes
	// is what the segments add to it; FinalSize is what the file holds, or will
	// hold, afterwards.
	FrozenSize    int64 `json:"frozen_size"`
	AppendedBytes int64 `json:"appended_bytes"`
	FinalSize     int64 `json:"final_size"`
}

// ConcatReport describes a layout concat. It names directories and counts bytes;
// it never carries the content of a record.
type ConcatReport struct {
	// State is "layout_v1" (the store is not on layout v2: nothing to do), "plan"
	// (what a concat would do) or "done".
	State    string       `json:"state"`
	Segments int          `json:"segments"`
	Files    []ConcatFile `json:"files,omitempty"`
	// Resumed is true when the store was found in the middle of an earlier concat.
	Resumed          bool   `json:"resumed,omitempty"`
	RolledBackLogs   string `json:"rolled_back_logs,omitempty"`
	RolledBackLayout string `json:"rolled_back_layout,omitempty"`
	DurationMs       int64  `json:"duration_ms"`
}

// ConcatActionLayout returns a store on layout v2 to layout v1, offline. For each
// of the two logs, the files of the segments are appended to segment 0's file in
// ordinal order, so that file holds what the store would hold had it never
// rotated; then logs/ and LAYOUT are renamed aside and the store is layout v1
// again. Without apply it only describes what it would do.
//
// It refuses a segment that is not the size recorded when it was closed, before
// it changes anything. It can be run again after it stopped anywhere: LAYOUT
// still records the size segment 0 had, so how far the appends got is known from
// the size segment 0 has now, and an unfinished append is cut back to the last
// segment boundary and redone. The state in between (segment 0 longer than LAYOUT
// says) is refused by every opener of the store, so nothing writes to it before
// the concat is finished.
//
// The Action store has no writer lock of its own. The concat takes the OS lock of
// the newest segment, which every transaction holds, to be sure none is in
// flight, and in the same breath makes every file of every segment read-only, so
// that a writer that is still running fails to append instead of appending to a
// segment that is being copied (ErrStoreBusy when the lock is not free in time).
// Stop the process first all the same.
func ConcatActionLayout(ctx context.Context, root string, apply bool) (ConcatReport, error) {
	return concatActionLayout(ctx, root, apply, nil)
}

func concatActionLayout(ctx context.Context, root string, apply bool, failpoint func(string)) (ConcatReport, error) {
	started := time.Now()
	fire := func(stage string) {
		if failpoint != nil {
			failpoint(stage)
		}
	}
	report := ConcatReport{State: "plan"}
	root = filepath.Clean(root)
	layout, err := readLayout(root)
	if err != nil {
		return report, err
	}
	if !layout.v2 {
		report.State = "layout_v1"
		return report, nil
	}

	// Where the segments are: under logs/, or, when an earlier concat stopped after
	// it renamed logs/, under the directory it was renamed to.
	segBase := logsDir(root)
	names, err := segmentname.ListDirs(segBase)
	if err != nil {
		return report, err
	}
	finishOnly := false
	if len(names) == 0 {
		rolled, err := newestRolledBackLogs(root)
		if err != nil {
			return report, err
		}
		if rolled == "" {
			return report, fmt.Errorf("%w: LAYOUT v2 is present but there is no segment to concatenate", ErrSegmentMissing)
		}
		segBase = filepath.Join(root, rolled)
		if names, err = segmentname.ListDirs(segBase); err != nil {
			return report, err
		}
		finishOnly, report.Resumed = true, true
	}
	report.Segments = len(names)
	dirs := make([]string, len(names))
	for i, name := range names {
		dirs[i] = filepath.Join(segBase, name)
	}

	// What the end state is, and where segment 0 is now.
	segSizes := make([]sizes, len(dirs))
	var appended sizes
	for i, dir := range dirs {
		if segSizes[i], err = segmentFileSizes(dir); err != nil {
			return report, err
		}
		for f := range appended {
			appended[f] += segSizes[i][f]
		}
		// A closed segment is the size recorded when it was closed. Its record is
		// the one thing that says the history is whole; a concat does not merge a
		// segment that was changed.
		if i < len(dirs)-1 || sealedFileExists(dir) {
			record, known, err := readSealed(dir)
			if err != nil {
				return report, err
			}
			if !known {
				return report, fmt.Errorf("%w: segment %s is closed but has no %s record", ErrSealedSegmentChanged, names[i], sealedFilename)
			}
			if err := requireSize(segmentRef{ord: i + 1, name: names[i]}, record, segSizes[i]); err != nil {
				return report, err
			}
		}
	}
	now, err := segmentFileSizes(root)
	if err != nil {
		return report, err
	}
	for f, name := range logFilenames {
		final := layout.frozen[f] + appended[f]
		report.Files = append(report.Files, ConcatFile{Name: name, FrozenSize: layout.frozen[f], AppendedBytes: appended[f], FinalSize: final})
		if now[f] < layout.frozen[f] || now[f] > final || (finishOnly && now[f] != final) {
			return report, fmt.Errorf("%w: segment 0 file %s is %d bytes; LAYOUT recorded %d and the segments add %d", ErrSealedSegmentChanged, name, now[f], layout.frozen[f], appended[f])
		}
		if now[f] != layout.frozen[f] {
			report.Resumed = true
		}
	}
	if !apply {
		report.DurationMs = time.Since(started).Milliseconds()
		return report, nil
	}

	if !finishOnly {
		if err := settleSegmentsForConcat(ctx, root, dirs); err != nil {
			return report, err
		}
		if err := quiesceAndFreeze(ctx, dirs); err != nil {
			return report, err
		}
		fire("concat-frozen")
		if err := unfreezeSegmentZero(root); err != nil {
			return report, fmt.Errorf("make segment 0 writable: %w", err)
		}
		fire("concat-unfrozen")
		for f, name := range logFilenames {
			if err := ctx.Err(); err != nil {
				return report, err
			}
			path := filepath.Join(root, name)
			if err := appendSegmentsToFile(path, name, layout.frozen[f], now[f], segSizes, f, dirs, fire); err != nil {
				return report, err
			}
			segmentPaths := make([]string, len(dirs))
			for i, dir := range dirs {
				segmentPaths[i] = filepath.Join(dir, name)
			}
			if err := verifyAppended(path, layout.frozen[f], segmentPaths); err != nil {
				return report, fmt.Errorf("%s after the concat: %w", name, err)
			}
		}
		fire("concat-verified")
		stamp := time.Now().UTC().Format("20060102-150405")
		rolledLogs := rolledBackLogsPrefix + stamp
		if err := os.Rename(segBase, filepath.Join(root, rolledLogs)); err != nil {
			return report, fmt.Errorf("set logs/ aside: %w", err)
		}
		report.RolledBackLogs = rolledLogs
		if err := jsonlbatch.SyncDirectory(root); err != nil {
			return report, fmt.Errorf("sync the action store directory: %w", err)
		}
		fire("concat-logs-renamed")
	}

	stamp := time.Now().UTC().Format("20060102-150405")
	rolledLayout := rolledBackLayoutPrefix + stamp
	if err := os.Rename(layoutPath(root), filepath.Join(root, rolledLayout)); err != nil {
		return report, fmt.Errorf("set LAYOUT aside: %w", err)
	}
	report.RolledBackLayout = rolledLayout
	if err := jsonlbatch.SyncDirectory(root); err != nil {
		return report, fmt.Errorf("sync the action store directory: %w", err)
	}
	fire("concat-done")
	report.State = "done"
	report.DurationMs = time.Since(started).Milliseconds()
	return report, nil
}

func sealedFileExists(dir string) bool {
	ok, _ := sealedExists(dir)
	return ok
}

// newestRolledBackLogs is the most recent directory a concat renamed logs/ to.
func newestRolledBackLogs(root string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	newest := ""
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), rolledBackLogsPrefix) && entry.Name() > newest {
			newest = entry.Name()
		}
	}
	return newest, nil
}

// settleSegmentsForConcat checks that no segment has a WAL transaction pending.
// The newest segment is the one that was written to, so a crash can have left a
// pending transaction there: it is repaired the way any writer would repair it (a
// rollback of the transaction that never committed) before its files are copied.
// A pending transaction in any other segment is not something a crash leaves; the
// concat stops.
func settleSegmentsForConcat(ctx context.Context, root string, dirs []string) error {
	if err := jsonlbatch.CheckSettled(root, logFilenames[:]); err != nil {
		return fmt.Errorf("segment 0 WAL is not settled: %w", err)
	}
	for i, dir := range dirs {
		err := jsonlbatch.CheckSettled(dir, logFilenames[:])
		if err == nil {
			continue
		}
		if i != len(dirs)-1 || !errors.Is(err, jsonlbatch.ErrRecoveryRequired) {
			return fmt.Errorf("segment %s is closed but its WAL is not settled: %w", filepath.Base(dir), err)
		}
		store, newErr := jsonlbatch.New(dir, logFilenames[:])
		if newErr != nil {
			return fmt.Errorf("open segment %s to repair its WAL: %w", filepath.Base(dir), newErr)
		}
		if recoverErr := store.Recover(ctx); recoverErr != nil {
			return fmt.Errorf("repair the WAL of segment %s: %w", filepath.Base(dir), recoverErr)
		}
	}
	return nil
}

// quiesceAndFreeze makes sure no transaction is in flight on the newest segment
// (it takes the lock every transaction holds) and, while it holds it, makes every
// file of every segment read-only. A writer that starts later fails to open its
// WAL for appending. ErrStoreBusy means the lock was not free within
// concatLockWait.
func quiesceAndFreeze(ctx context.Context, dirs []string) error {
	newest := dirs[len(dirs)-1]
	reader, err := jsonlbatch.OpenReader(newest, logFilenames[:])
	if err != nil {
		return fmt.Errorf("open segment %s to take its lock: %w", filepath.Base(newest), err)
	}
	lockCtx, cancel := context.WithTimeout(ctx, concatLockWait)
	defer cancel()
	err = reader.Read(lockCtx, func() error {
		var errs []error
		for _, dir := range dirs {
			if err := freezeFiles(segmentFilesOf(dir)); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return fmt.Errorf("%w: the lock of segment %s was not free within %s", ErrStoreBusy, filepath.Base(newest), concatLockWait)
		}
		return fmt.Errorf("freeze the segments: %w", err)
	}
	return nil
}

// appendSegmentsToFile makes the segment 0 file at path hold its frozen content
// followed by the file f of every segment, given that it holds `current` bytes
// now: the segments that fit whole are kept, a segment that was being appended is
// cut back to its start, and the rest are appended.
func appendSegmentsToFile(path, name string, frozen, current int64, segSizes []sizes, f int, dirs []string, fire func(string)) error {
	boundary := frozen
	done := 0
	for done < len(dirs) && boundary+segSizes[done][f] <= current {
		boundary += segSizes[done][f]
		done++
	}
	if current != boundary {
		// What is past the last whole segment must be the start of the next one, as
		// an append that stopped half way leaves it. Anything else was written by
		// something else, and is not cut away.
		if done >= len(dirs) {
			return fmt.Errorf("%w: %s holds %d bytes more than the segments add", ErrSealedSegmentChanged, name, current-boundary)
		}
		if err := verifyPrefixOf(path, boundary, current-boundary, filepath.Join(dirs[done], name)); err != nil {
			return fmt.Errorf("%w: the %d bytes of %s past segment %d are not the start of segment %s: %v", ErrSealedSegmentChanged, current-boundary, name, done, filepath.Base(dirs[done]), err)
		}
		if err := os.Truncate(path, boundary); err != nil {
			return fmt.Errorf("cut %s back to the end of segment %d: %w", name, done, err)
		}
		if err := syncPath(path); err != nil {
			return err
		}
		fire("concat-truncated")
	}
	for i := done; i < len(dirs); i++ {
		if segSizes[i][f] > 0 {
			if err := appendFileTo(path, filepath.Join(dirs[i], name)); err != nil {
				return fmt.Errorf("append %s of segment %s: %w", name, filepath.Base(dirs[i]), err)
			}
			boundary += segSizes[i][f]
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			if info.Size() != boundary {
				return fmt.Errorf("%s is %d bytes after appending segment %s, want %d", name, info.Size(), filepath.Base(dirs[i]), boundary)
			}
		}
		fire(fmt.Sprintf("concat-appended-%s-%d", name, i))
	}
	return nil
}

// verifyPrefixOf checks that the n bytes of path from offset are the first n
// bytes of src.
func verifyPrefixOf(path string, offset, n int64, src string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	got, want := make([]byte, concatCopyChunk), make([]byte, concatCopyChunk)
	for n > 0 {
		chunk := int64(len(got))
		if n < chunk {
			chunk = n
		}
		if _, err := file.ReadAt(got[:chunk], offset); err != nil {
			return err
		}
		if _, err := io.ReadFull(in, want[:chunk]); err != nil {
			return err
		}
		if !bytes.Equal(got[:chunk], want[:chunk]) {
			return errors.New("the bytes differ")
		}
		offset += chunk
		n -= chunk
	}
	return nil
}

// appendFileTo appends the content of src to dst and makes it durable.
func appendFileTo(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func syncPath(path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// verifyAppended compares what the file holds after its first `frozen` bytes with
// the segment files, one after the other, byte for byte.
func verifyAppended(path string, frozen int64, segmentPaths []string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Seek(frozen, io.SeekStart); err != nil {
		return err
	}
	got, want := make([]byte, concatCopyChunk), make([]byte, concatCopyChunk)
	for _, segment := range segmentPaths {
		in, err := os.Open(segment)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		for {
			n, readErr := io.ReadFull(in, want)
			if n > 0 {
				if _, err := io.ReadFull(file, got[:n]); err != nil {
					_ = in.Close()
					return fmt.Errorf("the file is shorter than the segments it should hold: %w", err)
				}
				if !bytes.Equal(got[:n], want[:n]) {
					_ = in.Close()
					return fmt.Errorf("the appended bytes differ from segment %s", filepath.Base(filepath.Dir(segment)))
				}
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
					break
				}
				_ = in.Close()
				return readErr
			}
		}
		_ = in.Close()
	}
	var extra [1]byte
	if n, _ := file.Read(extra[:]); n != 0 {
		return errors.New("the file is longer than the segments it should hold")
	}
	return nil
}
