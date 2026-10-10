package task

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// openReaderIndex gives a read-only store its index. The index is a snapshot
// taken under the same OS lock a read takes, so the WAL is settled and every
// byte on disk is committed; nothing is written (no sidecar, no checkpoint).
//
// Without require, a store whose writer never produced a sidecar keeps reading
// the way it always did: the index would otherwise change what such a reader
// accepts (the index refuses IDs that are not in canonical form, the fold
// does not), and the writer has not opted into it. With a sidecar present, or
// with require, the reader needs an index and builds one in memory if the
// sidecar cannot be used.
func (s *JSONLStore) openReaderIndex(require bool) error {
	if !require {
		if _, err := os.Stat(sidecarPathOf(s.root)); err != nil {
			return nil
		}
	}
	var ix *taskIndex
	err := s.batch.Read(withTxLabel(context.Background(), "ReaderIndexOpen", ""), func() error {
		restored, _, why, err := restoreTaskIndex(s.root, true, nil)
		if err != nil {
			return err
		}
		if why == nil {
			ix = restored
			return nil
		}
		rebuilt, err := rebuildTaskIndex(s.root, true, why, time.Now(), true)
		if err != nil {
			return err
		}
		ix = rebuilt
		return nil
	})
	if err != nil {
		if require {
			return fmt.Errorf("open task index: %w", err)
		}
		log.Printf("[TaskStore] WARN reader could not use the index and reads the log directly: %v", err)
		return nil
	}
	s.idx = ix
	return nil
}

// TaskHistoryLine is one line of the log that belongs to a Task, exactly as it
// is stored.
type TaskHistoryLine struct {
	// File is the log file the line is in (task_state.jsonl, task_run.jsonl, ...).
	File string `json:"file"`
	// Offset is the byte offset of the line in that file.
	Offset int64 `json:"offset"`
	// Line is the stored record, verbatim.
	Line json.RawMessage `json:"line"`
}

// TaskHistory returns every stored line of the Task: all versions of the Task,
// of its context, the lines of all its Runs, and its notifications, ordered by
// file (state, run, context, notifications) and then by position in the log.
// Each line is verified (length, terminator, CRC32C) before it is returned. It
// needs the index, which a reader has when its writer keeps a sidecar or when it
// was opened with NewIndexedJSONLReader.
func (s *JSONLStore) TaskHistory(ctx context.Context, taskID modulecore.TaskID) ([]TaskHistoryLine, error) {
	if s == nil || s.idx == nil {
		return nil, fmt.Errorf("task history needs the task index")
	}
	release, err := s.beginIndexRead(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := taskID.Validate(); err != nil {
		return nil, err
	}
	key, err := parseCanonicalKey(string(taskID), taskKeyPrefix)
	if err != nil {
		return nil, domaintask.ErrNotFound
	}
	type located struct {
		kind fileKind
		pos  linePos
	}
	var found []located
	ix := s.idx
	ix.mu.RLock()
	slot, ok := ix.taskMap[key]
	if !ok {
		ix.mu.RUnlock()
		return nil, domaintask.ErrNotFound
	}
	rec := &ix.tasks[slot]
	for _, pos := range ix.lt.positions(rec.stateHead) {
		found = append(found, located{kindState, pos})
	}
	for run := rec.runHead; run != 0; run = ix.runs[run-1].nextRun {
		for _, pos := range ix.lt.positions(ix.runs[run-1].head) {
			found = append(found, located{kindRun, pos})
		}
	}
	for _, pos := range ix.lt.positions(rec.ctxHead) {
		found = append(found, located{kindContext, pos})
	}
	for i := range ix.notifs {
		if ix.notifs[i].key == key {
			found = append(found, located{kindNotification, ix.notifs[i].pos})
		}
	}
	ix.mu.RUnlock()

	sort.SliceStable(found, func(i, j int) bool {
		if found[i].kind != found[j].kind {
			return found[i].kind < found[j].kind
		}
		return found[i].pos.off < found[j].pos.off
	})
	lines := make([]TaskHistoryLine, 0, len(found))
	for _, item := range found {
		line, err := ix.readLine(item.kind, item.pos)
		if err != nil {
			return nil, err
		}
		lines = append(lines, TaskHistoryLine{File: kindFilename[item.kind], Offset: int64(item.pos.off), Line: json.RawMessage(bytes.TrimSpace(line))})
	}
	return lines, nil
}
