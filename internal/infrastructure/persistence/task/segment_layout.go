package task

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

// Layout v2 splits the log into segments (spec 3, 4.1-4.2): the six original files in the
// store root become segment 0 and are frozen; writes go to logs/<YYYYMMDD>-<NN>/,
// one segment per UTC day, and to a further segment of the same day when the
// current one outgrows the size valve. A closed segment is never written again.
// Layout v3 adds the Task-owned delegation report journal to those sealed files.
//
// LAYOUT is the version marker and the one durable record of what segment 0 held
// when it was frozen. A store without it is layout v1: the files in the root,
// writable, exactly as before segments existed.
const (
	layoutFilename      = "LAYOUT"
	layoutVersionLineV2 = "tasks-layout/v2"
	layoutVersionLine   = "tasks-layout/v3"
	layoutEndLine       = "end"
	layoutFrozenWord    = "frozen"
	logsDirName         = "logs"
	segmentLegacyName   = "legacy" // the name of segment 0 (the files in the root)

	// DefaultRotationSizeValve is the size of a segment's log files beyond which
	// the next write starts another segment of the same day (decision D3). The
	// prefix hash jsonlbatch takes of every file it appends to is proportional to
	// this size, so it bounds the commit time.
	DefaultRotationSizeValve int64 = 8 << 20

	maxSegmentOrdinal    = 1<<16 - 1
	maxSegmentsPerDay    = 99
	sizedFileCount       = 7 // five indexed files, fences, and Task-owned reports
	segmentNameLen       = len("20060102-01")
	sealedSegmentPerm    = 0o444
	writableDataFilePerm = 0o644
	walFilePerm          = 0o600
)

// sizedFilenames are the files whose final size identifies a closed segment:
// kindState..kindReceipt in order, then the fence log (which the index does not
// follow).
var sizedFilenames = [sizedFileCount]string{
	stateFilename, runFilename, contextFilename, notificationsFilename,
	taskOperationReceiptFilename, taskExecutionFenceFilename, taskDelegationReportFilename,
}

const fenceSizeIndex = sizedFileCount - 2

var (
	// ErrUnknownLayout means LAYOUT names a version this build does not know (or
	// is not a LAYOUT file at all). The store is not opened: guessing could fork
	// the history.
	ErrUnknownLayout = errors.New("task store layout is unknown")
	// ErrSealedSegmentChanged means a closed segment (segment 0 included) is not
	// the size it had when it was closed. The history may have been written to by
	// something else (an older binary, a hand edit, a restore from another copy),
	// so the store is not opened.
	ErrSealedSegmentChanged = errors.New("task store sealed segment changed")
	// ErrSegmentMissing means a segment the store knows of has no directory.
	ErrSegmentMissing = errors.New("task store segment is missing")
)

// layoutInfo is what LAYOUT says. The zero value is layout v1 (no LAYOUT file).
type layoutInfo struct {
	v2      bool
	version int
	// A v2 LAYOUT predates the report file and therefore leaves its size at zero.
	frozen [sizedFileCount]int64
}

func layoutPath(root string) string { return filepath.Join(root, layoutFilename) }

// formatLayout renders LAYOUT. The last line makes a truncated file detectable.
func formatLayout(frozen [sizedFileCount]int64) []byte {
	var b bytes.Buffer
	b.WriteString(layoutVersionLine + "\n")
	for i, name := range sizedFilenames {
		fmt.Fprintf(&b, "%s %s %d\n", layoutFrozenWord, name, frozen[i])
	}
	b.WriteString(layoutEndLine + "\n")
	return b.Bytes()
}

// parseLayout reads known LAYOUT versions strictly. A v2 store has no report
// journal bytes in segment 0; its seventh frozen size is therefore zero.
func parseLayout(data []byte) (layoutInfo, error) {
	text := string(data)
	if !strings.HasSuffix(text, "\n") {
		return layoutInfo{}, fmt.Errorf("%w: LAYOUT is not newline terminated", ErrUnknownLayout)
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) == 0 || (lines[0] != layoutVersionLineV2 && lines[0] != layoutVersionLine) {
		first := ""
		if len(lines) > 0 {
			first = lines[0]
			if len(first) > 40 {
				first = first[:40]
			}
		}
		return layoutInfo{}, fmt.Errorf("%w: LAYOUT version %q is not supported", ErrUnknownLayout, first)
	}
	version, fileCount := 3, sizedFileCount
	if lines[0] == layoutVersionLineV2 {
		version, fileCount = 2, sizedFileCount-1
	}
	if len(lines) != fileCount+2 || lines[len(lines)-1] != layoutEndLine {
		return layoutInfo{}, fmt.Errorf("%w: LAYOUT is incomplete (%d lines)", ErrUnknownLayout, len(lines))
	}
	info := layoutInfo{v2: true, version: version}
	for i, name := range sizedFilenames[:fileCount] {
		fields := strings.Fields(lines[1+i])
		if len(fields) != 3 || fields[0] != layoutFrozenWord || fields[1] != name {
			return layoutInfo{}, fmt.Errorf("%w: LAYOUT line %d is not a frozen record of %s", ErrUnknownLayout, i+2, name)
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			return layoutInfo{}, fmt.Errorf("%w: LAYOUT size of %s is not a size", ErrUnknownLayout, name)
		}
		info.frozen[i] = size
	}
	return info, nil
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
	return parseLayout(data)
}

// writeLayout makes LAYOUT exist, whole or not at all: temporary file, fsync,
// rename, fsync of the directory.
func writeLayout(root string, frozen [sizedFileCount]int64, failpoint func(string)) error {
	_, err := writeLayoutReport(root, frozen, failpoint)
	return err
}

// writeLayoutReport is writeLayout that also says whether LAYOUT is in place: an
// error after the rename (the directory could not be synced) leaves a LAYOUT that
// may or may not survive a power cut, which the caller must not treat as "nothing
// happened".
func writeLayoutReport(root string, frozen [sizedFileCount]int64, failpoint func(string)) (installed bool, err error) {
	path := layoutPath(root)
	tmp := path + sidecarTmpSuffix
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return false, fmt.Errorf("create LAYOUT temporary file: %w", err)
	}
	if _, err := file.Write(formatLayout(frozen)); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return false, fmt.Errorf("write LAYOUT temporary file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return false, fmt.Errorf("sync LAYOUT temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return false, fmt.Errorf("close LAYOUT temporary file: %w", err)
	}
	if failpoint != nil {
		failpoint("layout-tmp-synced")
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, fmt.Errorf("install LAYOUT: %w", err)
	}
	if failpoint != nil {
		failpoint("layout-renamed")
	}
	if err := jsonlbatch.SyncDirectory(root); err != nil {
		return true, fmt.Errorf("sync task store directory after LAYOUT: %w", err)
	}
	return true, nil
}

// segmentRef names one segment: its ordinal, its name ("legacy" for segment 0)
// and the directory holding its files.
type segmentRef struct {
	ord  uint16
	name string
	dir  string
}

func logsDir(root string) string { return filepath.Join(root, logsDirName) }

func segmentDir(root, name string) string { return filepath.Join(logsDir(root), name) }

// listSegmentDirs returns the names of the segment directories under logs/, in
// ordinal (= lexical) order. Entries that are not segment directories are
// ignored. The naming rule itself (segmentname) is shared with the other stores
// that keep segments.
func listSegmentDirs(root string) ([]string, error) {
	names, err := segmentname.ListDirs(logsDir(root))
	if err != nil {
		return nil, fmt.Errorf("list task store segments: %w", err)
	}
	return names, nil
}

// listSegments lists the segments of the store in ordinal order, segment 0 first.
// On layout v1 only segment 0 exists; a directory under logs/ that holds data
// without a LAYOUT file is an inconsistent store (not a migration that stopped
// early, which leaves an empty one) and is refused.
func listSegments(root string, v2 bool) ([]segmentRef, error) {
	names, err := listSegmentDirs(root)
	if err != nil {
		return nil, err
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
	if len(names)+1 > maxSegmentOrdinal {
		return nil, fmt.Errorf("task store has %d segments, more than the %d an ordinal can name", len(names)+1, maxSegmentOrdinal)
	}
	for i, name := range names {
		refs = append(refs, segmentRef{ord: uint16(i + 1), name: name, dir: segmentDir(root, name)})
	}
	return refs, nil
}

// segmentHoldsNoData reports whether none of the six files of the directory has
// a byte in it.
func segmentHoldsNoData(dir string) (bool, error) {
	for _, name := range sizedFilenames {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return false, err
		}
		if info.Size() > 0 {
			return false, nil
		}
	}
	return true, nil
}

// segmentFileSizes stats the six files of a segment directory; a missing file is
// empty.
func segmentFileSizes(dir string) ([sizedFileCount]int64, error) {
	var sizes [sizedFileCount]int64
	for i, name := range sizedFilenames {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return sizes, err
		}
		sizes[i] = info.Size()
	}
	return sizes, nil
}

// verifyFrozenSegmentZero holds segment 0 against the sizes LAYOUT recorded when
// it was frozen. It is the independent record of that history: the index
// sidecar is derived data and can be gone.
func verifyFrozenSegmentZero(root string, frozen [sizedFileCount]int64) error {
	actual, err := segmentFileSizes(root)
	if err != nil {
		return err
	}
	for i, name := range sizedFilenames {
		if actual[i] != frozen[i] {
			return fmt.Errorf("%w: segment 0 file %s is %d bytes, LAYOUT recorded %d when it was frozen", ErrSealedSegmentChanged, name, actual[i], frozen[i])
		}
	}
	return nil
}

// segmentZeroFiles are the paths freezing protects: the six data files and the
// WAL. The lock file stays writable; every opener needs it.
func segmentZeroFiles(root string) []string {
	paths := make([]string, 0, sizedFileCount+1)
	for _, name := range sizedFilenames {
		paths = append(paths, filepath.Join(root, name))
	}
	return append(paths, filepath.Join(root, ".jsonlbatch.wal"))
}

// freezeSegmentZero makes the files of segment 0 read-only (a read-only
// attribute on Windows). An older binary then fails to open them for writing.
// It is a guard, not the defense (the size check is: the owner of the files, and
// root, can write regardless), so a failure is returned for the caller to report
// and the files that could be frozen are.
func freezeSegmentZero(root string) error {
	var errs []error
	for _, path := range segmentZeroFiles(root) {
		if err := chmodIfExists(path, sealedSegmentPerm); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// unfreezeSegmentZero is freezeSegmentZero undone (layout concat).
func unfreezeSegmentZero(root string) error {
	paths := segmentZeroFiles(root)
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
