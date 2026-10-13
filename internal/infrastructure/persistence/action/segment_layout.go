package action

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/segmentname"
)

// Layout v2 splits the logs into segments (design 5.3): the two files in the
// store root become segment 0 and are frozen; writes go to logs/<YYYYMMDD>-<NN>/,
// one segment per UTC day, and to a further segment of the same day when the
// current one outgrows the size valve. A closed segment is never written again.
//
// The naming rule of the directories is shared with the Task store
// (segmentname). The rest is this store's own: which files a segment holds, how
// segment 0 is frozen, and how sizes are checked (decision D4, option (i)).
//
// LAYOUT is the version marker and the one durable record of what segment 0 held
// when it was frozen. A store without it is layout v1: the files in the root,
// writable, exactly as before segments existed.
const (
	layoutFilename    = "LAYOUT"
	layoutVersionLine = "actions-layout/v2"
	layoutFrozenWord  = "frozen"
	layoutEndLine     = "end"

	// sealedFilename is written into a segment directory when the segment is
	// closed. It records the final size of the segment's files: the one durable
	// record the size check of a closed segment compares with (decision D8; there
	// is no sidecar to hold it).
	sealedFilename    = "SEALED"
	sealedVersionLine = "actions-segment/v1"
	sealedWord        = "sealed"

	logsDirName       = "logs"
	segmentLegacyName = "legacy" // the name of segment 0 (the files in the root)

	// DefaultRotationSizeValve is the size of a segment's log files beyond which
	// the next write starts another segment of the same day (decision D3). The
	// prefix hash jsonlbatch takes of every file it appends to is proportional to
	// this size, so it bounds the commit time.
	DefaultRotationSizeValve int64 = 8 << 20

	maxSegmentsPerDay    = 99
	maxSegmentCount      = 1<<16 - 1
	sealedSegmentPerm    = 0o444
	writableDataFilePerm = 0o644
	walFilePerm          = 0o600
	layoutTmpSuffix      = ".tmp"
)

var (
	// ErrUnknownLayout means LAYOUT (or a SEALED record) names a version this
	// build does not know, or is not such a file at all. The store is not opened:
	// guessing could fork the history.
	ErrUnknownLayout = errors.New("action store layout is unknown")
	// ErrSealedSegmentChanged means a closed segment (segment 0 included) is not
	// the size it had when it was closed. The history may have been written to by
	// something else (an older binary, a hand edit, a restore from another copy),
	// so the store is not opened.
	ErrSealedSegmentChanged = errors.New("action store sealed segment changed")
	// ErrSegmentMissing means a segment the store knows of has no directory, or a
	// segment that should still be open has been closed.
	ErrSegmentMissing = errors.New("action store segment is missing")
)

// sizes are the byte sizes of the two logs of one segment, in the order of
// logFilenames.
type sizes = [fileCount]int64

// layoutInfo is what LAYOUT says. The zero value is layout v1 (no LAYOUT file).
type layoutInfo struct {
	v2 bool
	// frozen is the size of each of segment 0's files when it was frozen.
	frozen sizes
}

func layoutPath(root string) string { return filepath.Join(root, layoutFilename) }

// formatSizeRecord renders a text record of one size per log file: a version
// line, one "<word> <file> <size>" line per file and an end line, which makes a
// truncated file detectable. LAYOUT and SEALED have this shape.
func formatSizeRecord(versionLine, word string, values sizes) []byte {
	var b bytes.Buffer
	b.WriteString(versionLine + "\n")
	for i, name := range logFilenames {
		fmt.Fprintf(&b, "%s %s %d\n", word, name, values[i])
	}
	b.WriteString(layoutEndLine + "\n")
	return b.Bytes()
}

// parseSizeRecord reads a record formatSizeRecord wrote, strictly: anything but
// the exact text is an unknown layout, and a later version writes another first
// line, which this build refuses.
func parseSizeRecord(data []byte, versionLine, word, what string) (sizes, error) {
	var values sizes
	text := string(data)
	if !strings.HasSuffix(text, "\n") {
		return values, fmt.Errorf("%w: %s is not newline terminated", ErrUnknownLayout, what)
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if lines[0] != versionLine {
		first := lines[0]
		if len(first) > 40 {
			first = first[:40]
		}
		return values, fmt.Errorf("%w: %s version %q is not %q", ErrUnknownLayout, what, first, versionLine)
	}
	if len(lines) != fileCount+2 || lines[len(lines)-1] != layoutEndLine {
		return values, fmt.Errorf("%w: %s is incomplete (%d lines)", ErrUnknownLayout, what, len(lines))
	}
	for i, name := range logFilenames {
		fields := strings.Fields(lines[1+i])
		if len(fields) != 3 || fields[0] != word || fields[1] != name {
			return values, fmt.Errorf("%w: %s line %d is not a %s record of %s", ErrUnknownLayout, what, i+2, word, name)
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			return values, fmt.Errorf("%w: %s size of %s is not a size", ErrUnknownLayout, what, name)
		}
		values[i] = size
	}
	return values, nil
}

// readLayout returns the layout of the store at root.
func readLayout(root string) (layoutInfo, error) {
	data, err := os.ReadFile(layoutPath(root))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return layoutInfo{}, nil
		}
		return layoutInfo{}, err
	}
	frozen, err := parseSizeRecord(data, layoutVersionLine, layoutFrozenWord, "LAYOUT")
	if err != nil {
		return layoutInfo{}, err
	}
	return layoutInfo{v2: true, frozen: frozen}, nil
}

// writeFileAtomically makes path exist, whole or not at all: temporary file,
// fsync, rename, fsync of the directory. installed says whether the file is in
// place: an error after the rename (the directory could not be synced) leaves a
// file that may or may not survive a power cut, which the caller must not treat
// as "nothing happened".
func writeFileAtomically(path string, data []byte, failpoint func(string), stagePrefix string) (installed bool, err error) {
	tmp := path + layoutTmpSuffix
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return false, fmt.Errorf("create %s temporary file: %w", filepath.Base(path), err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return false, fmt.Errorf("write %s temporary file: %w", filepath.Base(path), err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return false, fmt.Errorf("sync %s temporary file: %w", filepath.Base(path), err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return false, fmt.Errorf("close %s temporary file: %w", filepath.Base(path), err)
	}
	if failpoint != nil {
		failpoint(stagePrefix + "-tmp-synced")
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, fmt.Errorf("install %s: %w", filepath.Base(path), err)
	}
	if failpoint != nil {
		failpoint(stagePrefix + "-renamed")
	}
	if err := jsonlbatch.SyncDirectory(filepath.Dir(path)); err != nil {
		return true, fmt.Errorf("sync the directory after %s: %w", filepath.Base(path), err)
	}
	return true, nil
}

// segmentRef names one segment: its ordinal, its name ("legacy" for segment 0)
// and the directory holding its files.
type segmentRef struct {
	ord  int
	name string
	dir  string
}

func (r segmentRef) paths() [fileCount]string {
	var p [fileCount]string
	for i, name := range logFilenames {
		p[i] = filepath.Join(r.dir, name)
	}
	return p
}

func logsDir(root string) string { return filepath.Join(root, logsDirName) }

func segmentDir(root, name string) string { return filepath.Join(logsDir(root), name) }

func sealedPath(dir string) string { return filepath.Join(dir, sealedFilename) }

// listSegments lists the segments of the store in ordinal order, segment 0 first.
// On layout v1 only segment 0 exists; a directory under logs/ that holds data
// without a LAYOUT file is an inconsistent store (not a migration that stopped
// early, which leaves an empty one) and is refused.
func listSegments(root string, v2 bool) ([]segmentRef, error) {
	names, err := segmentname.ListDirs(logsDir(root))
	if err != nil {
		return nil, fmt.Errorf("list action store segments: %w", err)
	}
	refs := []segmentRef{{ord: 0, name: segmentLegacyName, dir: root}}
	if !v2 {
		for _, name := range names {
			if empty, err := segmentHoldsNoData(segmentDir(root, name)); err != nil {
				return nil, err
			} else if !empty {
				return nil, fmt.Errorf("%w: logs/%s holds data but there is no LAYOUT file", ErrUnknownLayout, name)
			}
		}
		return refs, nil
	}
	if len(names)+1 > maxSegmentCount {
		return nil, fmt.Errorf("action store has %d segments, more than the %d an ordinal can name", len(names)+1, maxSegmentCount)
	}
	for i, name := range names {
		refs = append(refs, segmentRef{ord: i + 1, name: name, dir: segmentDir(root, name)})
	}
	return refs, nil
}

// segmentHoldsNoData reports whether none of the logs of the directory has a
// byte in it.
func segmentHoldsNoData(dir string) (bool, error) {
	sz, err := segmentFileSizes(dir)
	if err != nil {
		return false, err
	}
	for _, v := range sz {
		if v > 0 {
			return false, nil
		}
	}
	return true, nil
}

// segmentFileSizes stats the logs of a segment directory; a missing file is
// empty.
func segmentFileSizes(dir string) (sizes, error) {
	var out sizes
	for i, name := range logFilenames {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return out, err
		}
		out[i] = info.Size()
	}
	return out, nil
}

// readSealed returns the size record of a closed segment; ok is false when the
// segment has none (it was closed by a rotation that stopped before it wrote
// one).
func readSealed(dir string) (record sizes, ok bool, err error) {
	data, err := os.ReadFile(sealedPath(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return sizes{}, false, nil
		}
		return sizes{}, false, err
	}
	record, err = parseSizeRecord(data, sealedVersionLine, sealedWord, "SEALED of "+filepath.Base(dir))
	if err != nil {
		return sizes{}, false, err
	}
	return record, true, nil
}

// writeSealed records the final sizes of the closed segment in dir. installed
// says whether the record is in place, which it can be when an error is returned
// (the directory could not be synced afterwards).
func writeSealed(dir string, final sizes, failpoint func(string)) (installed bool, err error) {
	return writeFileAtomically(sealedPath(dir), formatSizeRecord(sealedVersionLine, sealedWord, final), failpoint, "sealed")
}

// verifyFrozenSegmentZero holds segment 0 against the sizes LAYOUT recorded when
// it was frozen. It is the independent record of that history.
func verifyFrozenSegmentZero(root string, frozen sizes) error {
	actual, err := segmentFileSizes(root)
	if err != nil {
		return err
	}
	for i, name := range logFilenames {
		if actual[i] != frozen[i] {
			return fmt.Errorf("%w: segment 0 file %s is %d bytes, LAYOUT recorded %d when it was frozen", ErrSealedSegmentChanged, name, actual[i], frozen[i])
		}
	}
	return nil
}

// segmentZeroFiles are the paths freezing protects: the two data files and the
// WAL. The lock file stays writable; every opener needs it.
func segmentZeroFiles(root string) []string {
	return segmentFilesOf(root)
}

// segmentFilesOf are the data files and the WAL of the segment in dir.
func segmentFilesOf(dir string) []string {
	paths := make([]string, 0, fileCount+1)
	for _, name := range logFilenames {
		paths = append(paths, filepath.Join(dir, name))
	}
	return append(paths, filepath.Join(dir, ".jsonlbatch.wal"))
}

// freezeSegmentZero makes the files of segment 0 read-only (a read-only
// attribute on Windows). An older binary then fails to open them for writing.
// It is a guard, not the defense (the size check is: the owner of the files, and
// root, can write regardless), so a failure is returned for the caller to report
// and the files that could be frozen are.
func freezeSegmentZero(root string) error { return freezeFiles(segmentZeroFiles(root)) }

// unfreezeSegmentZero is freezeSegmentZero undone (layout concat).
func unfreezeSegmentZero(root string) error { return unfreezeFiles(segmentZeroFiles(root)) }

func freezeFiles(paths []string) error {
	var errs []error
	for _, path := range paths {
		if err := chmodIfExists(path, sealedSegmentPerm); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// unfreezeFiles makes the data files writable again and the WAL (the last path)
// owner-only, the modes jsonlbatch creates them with.
func unfreezeFiles(paths []string) error {
	var errs []error
	for i, path := range paths {
		perm := os.FileMode(writableDataFilePerm)
		if i == len(paths)-1 {
			perm = walFilePerm
		}
		if err := chmodIfExists(path, perm); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func chmodIfExists(path string, perm os.FileMode) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode().Perm() == perm {
		return nil
	}
	return os.Chmod(path, perm)
}

// segmentZeroFrozen reports whether every existing file of segment 0 is
// read-only for its owner.
func segmentZeroFrozen(root string) bool {
	for _, path := range segmentZeroFiles(root) {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.Mode().Perm()&0o200 != 0 {
			return false
		}
	}
	return true
}
