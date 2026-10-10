package task

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"io"
	"os"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// readLine reads the line at pos from the file of kind and verifies it: the
// length matches, a newline follows, and the CRC32C taken at write time still
// holds. It needs no index lock; committed lines never change.
func (ix *taskIndex) readLine(kind fileKind, pos linePos) ([]byte, error) {
	if ix.closed.Load() {
		return nil, os.ErrClosed
	}
	file := ix.files[kind]
	if file == nil {
		return nil, os.ErrClosed
	}
	buf := make([]byte, int(pos.length)+1)
	n, err := file.ReadAt(buf, int64(pos.off))
	if err != nil && !(err == io.EOF && n == len(buf)) {
		return nil, ix.corrupt(kind, pos, fmt.Sprintf("read: %v", err))
	}
	if buf[pos.length] != '\n' {
		return nil, ix.corrupt(kind, pos, "line is not newline terminated")
	}
	line := buf[:pos.length]
	if crc32.Checksum(line, castagnoli) != pos.crc {
		return nil, ix.corrupt(kind, pos, "CRC32C mismatch")
	}
	return line, nil
}

func (ix *taskIndex) corrupt(kind fileKind, pos linePos, why string) error {
	ix.corruptReads.Add(1)
	return fmt.Errorf("%w: %s offset %d length %d: %s", ErrRecordCorrupt, kindFilename[kind], pos.off, pos.length, why)
}

// decodeLineAt reads, verifies and strictly decodes one line.
func decodeLineAt[T any](ix *taskIndex, kind fileKind, pos linePos, out *T) error {
	line, err := ix.readLine(kind, pos)
	if err != nil {
		return err
	}
	if err := decodeStrictLine(bytes.TrimSpace(line), out); err != nil {
		return ix.corrupt(kind, pos, fmt.Sprintf("decode: %v", err))
	}
	return nil
}

// readTaskAt returns the Task on the line at pos, which must belong to want.
func (ix *taskIndex) readTaskAt(pos linePos, want key16) (domaintask.Task, error) {
	var value domaintask.Task
	if err := decodeLineAt(ix, kindState, pos, &value); err != nil {
		return domaintask.Task{}, err
	}
	if got, err := parseCanonicalKey(string(value.TaskID), taskKeyPrefix); err != nil || got != want {
		return domaintask.Task{}, ix.corrupt(kindState, pos, "line belongs to a different task")
	}
	return cloneTransactionTask(value), nil
}

func (ix *taskIndex) readRunAt(pos linePos, want key16) (domaintask.Run, error) {
	var value domaintask.Run
	if err := decodeLineAt(ix, kindRun, pos, &value); err != nil {
		return domaintask.Run{}, err
	}
	if got, err := parseCanonicalKey(string(value.RunID), runKeyPrefix); err != nil || got != want {
		return domaintask.Run{}, ix.corrupt(kindRun, pos, "line belongs to a different run")
	}
	return cloneTransactionRun(value), nil
}

func (ix *taskIndex) readContextAt(pos linePos, want key16) (domaintask.SharedRoleContext, error) {
	var value domaintask.SharedRoleContext
	if err := decodeLineAt(ix, kindContext, pos, &value); err != nil {
		return domaintask.SharedRoleContext{}, err
	}
	if got, err := parseCanonicalKey(string(value.TaskID), taskKeyPrefix); err != nil || got != want {
		return domaintask.SharedRoleContext{}, ix.corrupt(kindContext, pos, "line belongs to a different task")
	}
	return value, nil
}

func (ix *taskIndex) readNotificationAt(pos linePos, want key16) (domaintask.Notification, error) {
	var value domaintask.Notification
	if err := decodeLineAt(ix, kindNotification, pos, &value); err != nil {
		return domaintask.Notification{}, err
	}
	if got, err := parseCanonicalKey(string(value.TaskID), taskKeyPrefix); err != nil || got != want {
		return domaintask.Notification{}, ix.corrupt(kindNotification, pos, "line belongs to a different task")
	}
	return value, nil
}

func (ix *taskIndex) readReceiptAt(pos linePos, operationID string) (TaskOperationReceipt, error) {
	var value TaskOperationReceipt
	if err := decodeLineAt(ix, kindReceipt, pos, &value); err != nil {
		return TaskOperationReceipt{}, err
	}
	if value.OperationID != operationID {
		return TaskOperationReceipt{}, ix.corrupt(kindReceipt, pos, "line belongs to a different operation")
	}
	return value, nil
}

// The lookups below need mu held for reading (or applyMu held by the caller).

// taskTailLocked returns the position of the latest state line of the Task.
func (ix *taskIndex) taskTailLocked(key key16) (linePos, bool) {
	slot, ok := ix.taskMap[key]
	if !ok {
		return linePos{}, false
	}
	return ix.lt.pos(ix.tasks[slot].stateTail), true
}

// contextTailLocked returns the position of the latest context line.
func (ix *taskIndex) contextTailLocked(key key16) (linePos, bool) {
	slot, ok := ix.taskMap[key]
	if !ok || ix.tasks[slot].ctxTail == 0 {
		return linePos{}, false
	}
	return ix.lt.pos(ix.tasks[slot].ctxTail), true
}

// runTailLocked returns the position of the latest line of the Run.
func (ix *taskIndex) runTailLocked(key key16) (linePos, bool) {
	slot, ok := ix.runMap[key]
	if !ok {
		return linePos{}, false
	}
	return ix.lt.pos(ix.runs[slot].tail), true
}

func (ix *taskIndex) receiptLocked(operationID string) (receiptRec, bool) {
	rec, ok := ix.receipts[operationID]
	return rec, ok
}
