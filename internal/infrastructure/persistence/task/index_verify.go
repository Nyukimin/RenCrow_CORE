package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// How the sidecar compares with the log (VerifyReport.Sidecar).
const (
	verifySidecarMatches  = "matches_log"
	verifySidecarMissing  = "missing"
	verifySidecarDiffers  = "differs_from_log"
	verifySidecarUnusable = "unusable"
)

// maxVerifyProblems bounds the problems a report spells out; Mismatches counts
// them all.
const maxVerifyProblems = 20

// VerifyFile is what the log and the index say about one file, summed over all
// segments.
type VerifyFile struct {
	Name       string `json:"name"`
	Bytes      int64  `json:"bytes"`
	Lines      int64  `json:"lines"`
	IndexBytes int64  `json:"index_bytes"`
	IndexLines int64  `json:"index_lines"`
}

// VerifyReport is the outcome of VerifyTaskStore. It is bounded: counts, file
// sizes and at most maxVerifyProblems messages that name IDs and files, never
// the content of a record.
type VerifyReport struct {
	// OK is true when the log can be indexed, the index agrees with an
	// independent fold of the log, and a usable sidecar agrees with the log.
	OK    bool         `json:"ok"`
	Files []VerifyFile `json:"files"`
	// Layout is "v1" (the files in the store root) or "v2" (segment 0 frozen and
	// a segment per day under logs/); Segments counts the segments, segment 0
	// included.
	Layout   string `json:"layout"`
	Segments int    `json:"segments"`

	Tasks         int `json:"tasks"`
	Runs          int `json:"runs"`
	Contexts      int `json:"contexts"`
	Notifications int `json:"notifications"`
	Receipts      int `json:"receipts"`

	// Sidecar is "matches_log", "missing", "unusable" (damaged or outdated:
	// harmless, it is rebuilt at the next start) or "differs_from_log" (it is
	// intact but says something else than the log, which is a failure).
	Sidecar              string `json:"sidecar"`
	SidecarReason        string `json:"sidecar_reason,omitempty"`
	SidecarReplayedLines int64  `json:"sidecar_replayed_lines,omitempty"`

	Mismatches int      `json:"mismatches"`
	Problems   []string `json:"problems,omitempty"`
	DurationMs int64    `json:"duration_ms"`
}

func (r *VerifyReport) problem(format string, args ...any) {
	r.Mismatches++
	if len(r.Problems) < maxVerifyProblems {
		r.Problems = append(r.Problems, fmt.Sprintf(format, args...))
	}
}

// VerifyTaskStore checks the index against the log (spec AC-12, INV-2/INV-3):
//   - every file's line count and length agree with the index,
//   - every Task, Run, context, notification and receipt that an independent
//     fold of the log (the code the store used before the index existed) yields
//     is found through the index with identical content, and nothing else is,
//   - a sidecar, if it is usable, restores to exactly the index built from the
//     log (so a log line changed after the sidecar was written is noticed).
//
// It reads under the same OS lock as any read and writes nothing. On a running
// production store that lock is held for the whole check, so run it on a copy.
// A defect in the log or the index is a finding in the report, not an error;
// an error means the store could not be examined at all.
func VerifyTaskStore(ctx context.Context, root string) (VerifyReport, error) {
	started := time.Now()
	var report VerifyReport
	root = filepath.Clean(root)
	layout, refs, batch, err := openReaderBatch(root)
	if err != nil {
		return report, err
	}
	report.Layout, report.Segments = "v1", len(refs)
	if layout.v2 {
		report.Layout = "v2"
	}
	err = batch.Read(withTxLabel(ctx, "Verify", ""), func() error {
		ix, buildErr := buildTaskIndexOf(root, layout, refs, true)
		if buildErr != nil {
			report.problem("the log cannot be indexed: %v", buildErr)
			return nil
		}
		defer ix.close()
		if err := compareIndexWithLog(ctx, ix, &report); err != nil {
			return err
		}
		return compareSidecar(root, layout, refs, ix, &report)
	})
	report.OK = err == nil && report.Mismatches == 0
	report.DurationMs = time.Since(started).Milliseconds()
	return report, err
}

// openReaderBatch opens the read side of the store files, refusing a root that
// holds a retired layout or a history that is not the recorded one. It returns
// the layout, the segments the reader covers and the jsonlbatch Store whose lock
// the reader holds while it reads.
func openReaderBatch(root string) (layoutInfo, []segmentRef, *jsonlbatch.Store, error) {
	if info, err := os.Stat(root); err != nil {
		return layoutInfo{}, nil, nil, fmt.Errorf("task store %q: %w", root, err)
	} else if !info.IsDir() {
		return layoutInfo{}, nil, nil, fmt.Errorf("task store %q is not a directory", root)
	}
	if err := rejectLegacyTaskStoreFiles(root); err != nil {
		return layoutInfo{}, nil, nil, err
	}
	layout, refs, err := openLayout(root)
	if err != nil {
		return layoutInfo{}, nil, nil, err
	}
	refs, batch, err := openReaderBatchFor(layout, refs)
	return layout, refs, batch, err
}

// openReaderBatchFor opens the reader of the newest segment that can be opened:
// a segment directory that has just been created may not be complete yet, and
// the one before it is then the newest history there is. It returns the segments
// up to and including that one.
func openReaderBatchFor(layout layoutInfo, refs []segmentRef) ([]segmentRef, *jsonlbatch.Store, error) {
	for {
		current := refs[len(refs)-1]
		filenames, err := taskBatchReaderFilenames(current.dir)
		var batch *jsonlbatch.Store
		if err == nil {
			batch, err = jsonlbatch.OpenReader(current.dir, filenames)
		}
		if err == nil {
			return refs, batch, nil
		}
		if !layout.v2 || len(refs) <= 2 {
			return nil, nil, err
		}
		refs = refs[:len(refs)-1]
	}
}

// compareSidecar holds a usable sidecar against the index built from the log.
func compareSidecar(root string, layout layoutInfo, refs []segmentRef, built *taskIndex, report *VerifyReport) error {
	restored, replayed, why, err := restoreTaskIndex(root, layout, refs, true, nil)
	if err != nil {
		return err
	}
	if why != nil {
		if why.reason == rebuildMissing {
			report.Sidecar = verifySidecarMissing
		} else {
			report.Sidecar, report.SidecarReason = verifySidecarUnusable, why.reason
		}
		return nil
	}
	defer restored.close()
	report.SidecarReplayedLines = replayed
	if diff := equivalentIndex(restored, built); diff != nil {
		report.Sidecar = verifySidecarDiffers
		report.problem("the sidecar restores to an index that differs from the log: %v", diff)
		return nil
	}
	report.Sidecar = verifySidecarMatches
	return nil
}

// compareIndexWithLog holds ix against an independent fold of the files of root
// and records every disagreement in report. The caller holds the read lock of
// the store, so the files do not change during the comparison.
func compareIndexWithLog(ctx context.Context, ix *taskIndex, report *VerifyReport) error {
	// Files: lines and bytes, segment by segment; the report sums them per file.
	report.Files = report.Files[:0]
	totals := make([]VerifyFile, kindCount)
	for kind := fileKind(0); kind < kindCount; kind++ {
		totals[kind].Name = kindFilename[kind]
	}
	for ord := range ix.segs {
		seg := ix.segs[ord]
		for kind := fileKind(0); kind < kindCount; kind++ {
			indexBytes, indexLines := seg.size[kind], seg.lines[kind]
			if uint16(ord) == ix.cur {
				indexBytes, indexLines = ix.applied[kind], ix.lines[kind]
			}
			entry := VerifyFile{Name: kindFilename[kind], IndexBytes: indexBytes, IndexLines: indexLines}
			file, err := os.Open(filepath.Join(seg.dir, kindFilename[kind]))
			switch {
			case err == nil:
				info, statErr := file.Stat()
				if statErr != nil {
					_ = file.Close()
					return statErr
				}
				entry.Bytes = info.Size()
				lines, countErr := countNewlines(file, info.Size())
				_ = file.Close()
				if countErr != nil {
					return countErr
				}
				entry.Lines = lines
			case errors.Is(err, os.ErrNotExist):
				// An absent file is an empty one (a store written before receipts existed).
			default:
				return err
			}
			if entry.Bytes != entry.IndexBytes || entry.Lines != entry.IndexLines {
				report.problem("segment %s, %s: the file has %d bytes and %d lines, the index covers %d bytes and %d lines", seg.name, entry.Name, entry.Bytes, entry.Lines, entry.IndexBytes, entry.IndexLines)
			}
			totals[kind].Bytes += entry.Bytes
			totals[kind].Lines += entry.Lines
			totals[kind].IndexBytes += entry.IndexBytes
			totals[kind].IndexLines += entry.IndexLines
		}
	}
	report.Files = append(report.Files, totals...)

	// The oracle: the fold the store used before it had an index, over the files
	// of all segments in order.
	pathsOf := func(name string) []string {
		paths := make([]string, 0, len(ix.segs))
		for _, seg := range ix.segs {
			paths = append(paths, filepath.Join(seg.dir, name))
		}
		return paths
	}
	taskRecords, err := readSegmentedJSONLLines[domaintask.Task](ctx, pathsOf(stateFilename))
	if err != nil {
		report.problem("%s cannot be read: %v", stateFilename, err)
		return nil
	}
	folded, err := foldTaskRecords(taskRecords)
	if err != nil {
		report.problem("%s cannot be folded: %v", stateFilename, err)
		return nil
	}
	runRecords, err := readSegmentedJSONLLines[domaintask.Run](ctx, pathsOf(runFilename))
	if err != nil {
		report.problem("%s cannot be read: %v", runFilename, err)
		return nil
	}
	foldedRuns, err := foldRunRecords(runRecords, 0)
	if err != nil {
		report.problem("%s cannot be folded: %v", runFilename, err)
		return nil
	}
	contextRecords, err := readSegmentedJSONLLines[domaintask.SharedRoleContext](ctx, pathsOf(contextFilename))
	if err != nil {
		report.problem("%s cannot be read: %v", contextFilename, err)
		return nil
	}
	notifications, err := readSegmentedJSONLLines[domaintask.Notification](ctx, pathsOf(notificationsFilename))
	if err != nil {
		report.problem("%s cannot be read: %v", notificationsFilename, err)
		return nil
	}
	receipts, err := readTaskOperationReceipts(ctx, pathsOf(taskOperationReceiptFilename)...)
	if err != nil {
		report.problem("%s cannot be read: %v", taskOperationReceiptFilename, err)
		return nil
	}
	latestContext := make(map[modulecore.TaskID]domaintask.SharedRoleContext, len(contextRecords))
	for _, record := range contextRecords {
		latestContext[record.TaskID] = record
	}

	report.Tasks, report.Runs, report.Contexts = len(folded), len(foldedRuns), len(latestContext)
	report.Notifications, report.Receipts = len(notifications), len(receipts)

	same := func(a, b any) bool {
		left, errLeft := json.Marshal(a)
		right, errRight := json.Marshal(b)
		return errLeft == nil && errRight == nil && bytes.Equal(left, right)
	}

	if len(ix.tasks) != len(folded) || len(ix.taskMap) != len(folded) {
		report.problem("the index holds %d tasks, the log folds to %d", len(ix.taskMap), len(folded))
	}
	for _, want := range folded {
		key, err := parseCanonicalKey(string(want.TaskID), taskKeyPrefix)
		if err != nil {
			report.problem("task %s: %v", want.TaskID, err)
			continue
		}
		pos, ok := ix.taskTailLocked(key)
		if !ok {
			report.problem("task %s is in the log but not in the index", want.TaskID)
			continue
		}
		got, err := ix.readTaskAt(pos, key)
		if err != nil {
			report.problem("task %s: %v", want.TaskID, err)
			continue
		}
		if !same(want, got) {
			report.problem("task %s: the index returns another version than the log folds to", want.TaskID)
		}
	}
	if len(ix.runs) != len(foldedRuns) || len(ix.runMap) != len(foldedRuns) {
		report.problem("the index holds %d runs, the log folds to %d", len(ix.runMap), len(foldedRuns))
	}
	for _, want := range foldedRuns {
		key, err := parseCanonicalKey(string(want.RunID), runKeyPrefix)
		if err != nil {
			report.problem("run %s: %v", want.RunID, err)
			continue
		}
		pos, ok := ix.runTailLocked(key)
		if !ok {
			report.problem("run %s is in the log but not in the index", want.RunID)
			continue
		}
		got, err := ix.readRunAt(pos, key)
		if err != nil {
			report.problem("run %s: %v", want.RunID, err)
			continue
		}
		if !same(want, got) {
			report.problem("run %s: the index returns another version than the log folds to", want.RunID)
		}
	}
	withContext := 0
	for i := range ix.tasks {
		if ix.tasks[i].ctxTail != 0 {
			withContext++
		}
	}
	if withContext != len(latestContext) {
		report.problem("the index holds %d contexts, the log has %d", withContext, len(latestContext))
	}
	for taskID, want := range latestContext {
		key, err := parseCanonicalKey(string(taskID), taskKeyPrefix)
		if err != nil {
			report.problem("context of %s: %v", taskID, err)
			continue
		}
		pos, ok := ix.contextTailLocked(key)
		if !ok {
			report.problem("the context of task %s is in the log but not in the index", taskID)
			continue
		}
		got, err := ix.readContextAt(pos, key)
		if err != nil {
			report.problem("context of %s: %v", taskID, err)
			continue
		}
		if !same(want, got) {
			report.problem("context of task %s: the index returns another version than the log folds to", taskID)
		}
	}
	if len(ix.notifs) != len(notifications) {
		report.problem("the index holds %d notifications, the log has %d", len(ix.notifs), len(notifications))
	} else {
		for i, want := range notifications {
			key, err := parseCanonicalKey(string(want.TaskID), taskKeyPrefix)
			if err != nil {
				report.problem("notification %d: %v", i, err)
				continue
			}
			got, err := ix.readNotificationAt(ix.notifs[i].pos, key)
			if err != nil {
				report.problem("notification %d of task %s: %v", i, want.TaskID, err)
				continue
			}
			if !same(want, got) {
				report.problem("notification %d of task %s differs between the index and the log", i, want.TaskID)
			}
		}
	}
	if len(ix.receipts) != len(receipts) {
		report.problem("the index holds %d operation receipts, the log has %d", len(ix.receipts), len(receipts))
	}
	for id, want := range receipts {
		rec, ok := ix.receiptLocked(id)
		if !ok {
			report.problem("operation receipt %q is in the log but not in the index", id)
			continue
		}
		got, err := ix.readReceiptAt(rec.pos, id)
		if err != nil {
			report.problem("operation receipt %q: %v", id, err)
			continue
		}
		if !same(want, got) {
			report.problem("operation receipt %q differs between the index and the log", id)
		}
	}

	var maxRun, maxReceipt uint64
	for _, run := range foldedRuns {
		maxRun = max(maxRun, run.WriterGeneration)
	}
	for _, receipt := range receipts {
		maxReceipt = max(maxReceipt, receipt.WriterGeneration)
	}
	if ix.maxRunGeneration != maxRun || ix.maxReceiptGeneration != maxReceipt {
		report.problem("writer generations: index run %d receipt %d, log run %d receipt %d", ix.maxRunGeneration, ix.maxReceiptGeneration, maxRun, maxReceipt)
	}
	return nil
}

// RebuildReport is the outcome of RebuildTaskIndex.
type RebuildReport struct {
	Tasks         int   `json:"tasks"`
	Runs          int   `json:"runs"`
	Notifications int   `json:"notifications"`
	Receipts      int   `json:"receipts"`
	Lines         int64 `json:"lines"`
	SidecarBytes  int   `json:"sidecar_bytes"`
	// Previous says what the sidecar was before: "missing", "usable" or
	// "unusable: <reason>".
	Previous   string `json:"previous_sidecar"`
	DurationMs int64  `json:"duration_ms"`
}

// RebuildTaskIndex rebuilds the sidecar of the store at root from the log, while
// no writer is running: it takes the writer lock (ErrTaskWriterBusy if another
// process holds it) and reads the log under the store's OS lock, so a pending or
// torn WAL is refused (ErrRecoveryRequired) instead of repaired; the next writer
// repairs it. The log is not modified.
func RebuildTaskIndex(ctx context.Context, root string) (RebuildReport, error) {
	started := time.Now()
	var report RebuildReport
	root = filepath.Clean(root)
	layout, refs, batch, err := openReaderBatch(root)
	if err != nil {
		return report, err
	}
	lock, err := acquireTaskWriter(root)
	if err != nil {
		return report, err
	}
	defer func() { _ = releaseTaskWriter(lock) }()

	report.Previous = describeSidecar(root, layout)
	err = batch.Read(withTxLabel(ctx, "RebuildIndex", ""), func() error {
		ix, err := buildTaskIndexOf(root, layout, refs, true)
		if err != nil {
			return err
		}
		defer ix.close()
		stats := ix.stats()
		report.Tasks, report.Runs, report.Notifications, report.Receipts = stats.Tasks, stats.Runs, stats.Notifications, stats.Receipts
		report.Lines = stats.TotalLines
		ck := newCheckpointer(ix, root, checkpointPolicy{manual: true})
		if err := ck.checkpointNow("rebuild-index"); err != nil {
			return err
		}
		if info, err := os.Stat(sidecarPathFor(root, layout.v2)); err == nil {
			report.SidecarBytes = int(info.Size())
		}
		return nil
	})
	report.DurationMs = time.Since(started).Milliseconds()
	return report, err
}

// describeSidecar says in a word what the sidecar of root is, without verifying
// it against the log.
func describeSidecar(root string, layout layoutInfo) string {
	data, err := os.ReadFile(sidecarPathFor(root, layout.v2))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "missing"
		}
		return "unusable: " + rebuildCorrupt
	}
	_, header, err := decodeSidecar(data)
	if err != nil {
		if errors.Is(err, errSidecarVersion) {
			return "unusable: " + rebuildVersion
		}
		return "unusable: " + rebuildCorrupt
	}
	if (header.Version == sidecarVersionV2) != layout.v2 {
		return "unusable: " + rebuildLayout
	}
	return "usable"
}

// rejectLegacyTaskStoreFiles refuses a root that still holds the retired job_*
// files, which this store does not read.
func rejectLegacyTaskStoreFiles(root string) error {
	for _, filename := range []string{"job_state.jsonl", "job_context.jsonl", "job_notifications.jsonl"} {
		if _, err := os.Lstat(filepath.Join(root, filename)); err == nil {
			return fmt.Errorf("legacy task store file %s is not supported", filename)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// readSegmentedJSONLLines reads the records of the same log file of every
// segment, oldest first: the records the one file would hold on layout v1. A
// segment without the file contributes none.
func readSegmentedJSONLLines[T any](ctx context.Context, paths []string) ([]T, error) {
	var all []T
	for _, path := range paths {
		part, err := readJSONLLines[T](ctx, path)
		if err != nil {
			return nil, err
		}
		all = append(all, part...)
	}
	return all, nil
}
