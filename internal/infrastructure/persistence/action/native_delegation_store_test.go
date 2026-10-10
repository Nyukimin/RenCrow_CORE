package action

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestNewJSONLStoreFailsClosedOnCorruptNativeDelegationHistory(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *JSONLStore, domainaction.Action, domainaction.Attempt)
	}{
		{
			name: "payload hash mismatch",
			mutate: func(t *testing.T, store *JSONLStore, _ domainaction.Action, _ domainaction.Attempt) {
				rewriteLastAttempt(t, store, func(attempt *domainaction.Attempt) {
					attempt.NativeDelegation.Open.PayloadSHA256 = strings.Repeat("0", 64)
				})
			},
		},
		{
			name: "invalid mutation status",
			mutate: func(t *testing.T, store *JSONLStore, _ domainaction.Action, _ domainaction.Attempt) {
				rewriteLastAttempt(t, store, func(attempt *domainaction.Attempt) {
					attempt.NativeDelegation.Open.Status = domainaction.NativeMutationStatusAccepted
				})
			},
		},
		{
			name: "start before accepted open",
			mutate: func(t *testing.T, store *JSONLStore, _ domainaction.Action, _ domainaction.Attempt) {
				rewriteLastAttempt(t, store, func(attempt *domainaction.Attempt) {
					payload := []byte(`{"start":true}`)
					hash := sha256.Sum256(payload)
					attempt.NativeDelegation.Start = &domainaction.NativeMutation{
						Key: "start-key", Payload: payload, PayloadSHA256: hex.EncodeToString(hash[:]),
						Status: domainaction.NativeMutationStatusPrepared,
					}
				})
			},
		},
		{
			name: "duplicate native action for one Task Run",
			mutate: func(t *testing.T, store *JSONLStore, action domainaction.Action, _ domainaction.Attempt) {
				duplicate := action
				duplicate.ActionID = modulecore.NewActionID()
				appendJSONLine(t, store.actionPath, duplicate)
			},
		},
		{
			name: "native action moved in history",
			mutate: func(t *testing.T, store *JSONLStore, action domainaction.Action, _ domainaction.Attempt) {
				moved := action
				moved.TaskID = modulecore.NewTaskID()
				moved.RunID = modulecore.NewRunID()
				appendJSONLine(t, store.actionPath, moved)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "actions")
			store, action, attempt := createPreparedNativeDelegation(t, root)
			test.mutate(t, store, action, attempt)
			if _, err := NewJSONLStore(root); err == nil {
				t.Fatal("NewJSONLStore() accepted corrupt native delegation state")
			}
		})
	}
}

func TestJSONLStoreRejectsAdvancedNativeAttemptOnFirstSave(t *testing.T) {
	tests := []struct {
		name  string
		state func(domainaction.Action, domainaction.Attempt) (domainaction.Action, domainaction.Attempt)
	}{
		{
			name: "accepted open mutation",
			state: func(action domainaction.Action, attempt domainaction.Attempt) (domainaction.Action, domainaction.Attempt) {
				attempt.NativeDelegation.Open = nativeTestMutation("open-key", []byte(`{"open":true}`), domainaction.NativeMutationStatusAccepted, []byte(`{"session":"s1"}`), "")
				return action, attempt
			},
		},
		{
			name: "definitely rejected open mutation",
			state: func(action domainaction.Action, attempt domainaction.Attempt) (domainaction.Action, domainaction.Attempt) {
				mutation := nativeTestMutation("open-key", []byte(`{"open":true}`), domainaction.NativeMutationStatusRejected, nil, "denied")
				attempt.NativeDelegation.Open = mutation
				completedAt := attempt.StartedAt.Add(time.Second)
				attempt.Status = domainaction.AttemptStatusFailed
				attempt.CompletedAt = &completedAt
				action.Status = domainaction.StatusFailed
				action.UpdatedAt = completedAt
				action.CompletedAt = &completedAt
				return action, attempt
			},
		},
		{
			name: "fabricated terminal run result",
			state: func(action domainaction.Action, attempt domainaction.Attempt) (domainaction.Action, domainaction.Attempt) {
				attempt.NativeDelegation.Open = nativeTestMutation("open-key", []byte(`{"open":true}`), domainaction.NativeMutationStatusAccepted, []byte(`{"session":"s1"}`), "")
				attempt.NativeDelegation.Start = nativeTestMutation("start-key", []byte(`{"start":true}`), domainaction.NativeMutationStatusAccepted, []byte(`{"run":"r1"}`), "")
				attempt.NativeDelegation.RunResult = []byte(`{"status":"completed"}`)
				completedAt := attempt.StartedAt.Add(time.Second)
				attempt.Status = domainaction.AttemptStatusSucceeded
				attempt.CompletedAt = &completedAt
				action.Status = domainaction.StatusSucceeded
				action.UpdatedAt = completedAt
				action.CompletedAt = &completedAt
				return action, attempt
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Now().UTC()
			attemptID := modulecore.NewAttemptID()
			action := domainaction.Action{
				ActionID: modulecore.NewActionID(), TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(),
				Kind: domainaction.KindDelegation, Name: domainaction.NativeDelegationActionName,
				Status: domainaction.StatusOpen, CurrentAttemptID: attemptID, CreatedAt: now, UpdatedAt: now,
			}
			attempt := domainaction.Attempt{
				AttemptID: attemptID, ActionID: action.ActionID, StartReason: domainaction.AttemptStartReasonFirst,
				Status: domainaction.AttemptStatusRunning, StartedAt: now, NativeDelegation: &domainaction.NativeDelegation{Mode: domainaction.NativeDelegationModeOpenStart},
			}
			action, attempt = test.state(action, attempt)
			store, err := NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
			if err != nil {
				t.Fatal(err)
			}
			err = store.Transaction(context.Background(), func(tx domainaction.Store) error {
				if err := tx.SaveAction(context.Background(), action); err != nil {
					return err
				}
				return tx.SaveAttempt(context.Background(), attempt)
			})
			if err == nil {
				t.Fatal("SaveAttempt() accepted an advanced native state as a new attempt ID")
			}
		})
	}
}

func TestNewJSONLStoreRejectsAdvancedNativeFirstAttemptRecord(t *testing.T) {
	tests := []struct {
		name  string
		state func(*domainaction.Action, *domainaction.Attempt)
	}{
		{
			name: "accepted open mutation",
			state: func(_ *domainaction.Action, attempt *domainaction.Attempt) {
				attempt.NativeDelegation.Open = nativeTestMutation("open-key", []byte(`{"open":true}`), domainaction.NativeMutationStatusAccepted, []byte(`{"session":"s1"}`), "")
			},
		},
		{
			name: "definitely rejected open mutation",
			state: func(action *domainaction.Action, attempt *domainaction.Attempt) {
				attempt.NativeDelegation.Open = nativeTestMutation("open-key", []byte(`{"open":true}`), domainaction.NativeMutationStatusRejected, nil, "denied")
				completedAt := attempt.StartedAt.Add(time.Second)
				attempt.Status = domainaction.AttemptStatusFailed
				attempt.CompletedAt = &completedAt
				action.Status = domainaction.StatusFailed
				action.UpdatedAt = completedAt
				action.CompletedAt = &completedAt
			},
		},
		{
			name: "fabricated terminal run result",
			state: func(action *domainaction.Action, attempt *domainaction.Attempt) {
				attempt.NativeDelegation.Open = nativeTestMutation("open-key", []byte(`{"open":true}`), domainaction.NativeMutationStatusAccepted, []byte(`{"session":"s1"}`), "")
				attempt.NativeDelegation.Start = nativeTestMutation("start-key", []byte(`{"start":true}`), domainaction.NativeMutationStatusAccepted, []byte(`{"run":"r1"}`), "")
				attempt.NativeDelegation.RunResult = []byte(`{"status":"completed"}`)
				completedAt := attempt.StartedAt.Add(time.Second)
				attempt.Status = domainaction.AttemptStatusSucceeded
				attempt.CompletedAt = &completedAt
				action.Status = domainaction.StatusSucceeded
				action.UpdatedAt = completedAt
				action.CompletedAt = &completedAt
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "actions")
			store, err := NewJSONLStore(root)
			if err != nil {
				t.Fatal(err)
			}
			manager := actionmanager.New(store)
			action, attempt, err := manager.EnsureNativeDelegation(context.Background(), actionmanager.EnsureNativeDelegationInput{
				TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), ExpectedCriteriaRevision: strings.Repeat("a", 64),
			})
			if err != nil {
				t.Fatal(err)
			}
			test.state(&action, &attempt)
			rewriteOnlyAction(t, store.actionPath, action)
			rewriteOnlyAttempt(t, store.attemptPath, attempt)
			if _, err := NewJSONLStore(root); err == nil {
				t.Fatal("NewJSONLStore() accepted an advanced native state as the first attempt record")
			}
		})
	}
}

func TestReadJSONLLinesAcceptsMaximumRecordWithWriterLineEndings(t *testing.T) {
	now := time.Now().UTC()
	payload := []byte(`{"open":true}`)
	mutation := nativeTestMutation("open-key", payload, domainaction.NativeMutationStatusPrepared, nil, "")
	attempt := domainaction.Attempt{
		AttemptID: modulecore.NewAttemptID(), ActionID: modulecore.NewActionID(),
		StartReason: domainaction.AttemptStartReasonFirst, Status: domainaction.AttemptStatusRunning,
		StartedAt: now, NativeDelegation: &domainaction.NativeDelegation{Mode: domainaction.NativeDelegationModeOpenStart, Open: mutation},
	}
	if err := attempt.Validate(); err != nil {
		t.Fatalf("test native attempt is invalid: %v", err)
	}
	encoded, err := json.Marshal(attempt)
	if err != nil {
		t.Fatal(err)
	}
	for _, lineEnding := range []struct {
		name  string
		bytes []byte
	}{
		{name: "LF", bytes: []byte{'\n'}},
		{name: "CRLF", bytes: []byte{'\r', '\n'}},
	} {
		t.Run(lineEnding.name, func(t *testing.T) {
			contentLength := jsonlbatch.MaxJSONLRecordBytes - len(lineEnding.bytes) + 1
			if len(encoded) > contentLength {
				t.Fatal("base native JSON unexpectedly exceeds record boundary")
			}
			record := append([]byte(nil), encoded...)
			record = append(record, bytes.Repeat([]byte{' '}, contentLength-len(record))...)
			record = append(record, lineEnding.bytes...)
			path := filepath.Join(t.TempDir(), "attempts.jsonl")
			if err := os.WriteFile(path, record, 0o644); err != nil {
				t.Fatal(err)
			}
			items, err := readJSONLLines[domainaction.Attempt](context.Background(), path)
			if err != nil {
				t.Fatalf("read exact-boundary %s record: %v", lineEnding.name, err)
			}
			if len(items) != 1 || items[0].NativeDelegation == nil || items[0].NativeDelegation.Open.PayloadSHA256 != mutation.PayloadSHA256 {
				t.Fatal("exact-boundary native record did not round-trip")
			}
		})
	}
}

func TestNewJSONLStoreRejectsOversizedValidNativeJSONRecord(t *testing.T) {
	root := filepath.Join(t.TempDir(), "actions")
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	manager := actionmanager.New(store)
	action, attempt, err := manager.EnsureNativeDelegation(context.Background(), actionmanager.EnsureNativeDelegationInput{
		TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), ExpectedCriteriaRevision: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	largePayload := bytes.Repeat([]byte{'x'}, jsonlbatch.MaxJSONLRecordBytes*3/4+1)
	attempt.NativeDelegation.Open = nativeTestMutation("large-open-key", largePayload, domainaction.NativeMutationStatusPrepared, nil, "")
	if err := attempt.Validate(); err != nil {
		t.Fatalf("oversized native JSON test record must have valid hash and domain shape: %v", err)
	}
	appendJSONLine(t, store.attemptPath, attempt)
	if _, err := NewJSONLStore(root); err == nil {
		t.Fatal("NewJSONLStore() accepted an oversized valid-hash native JSONL record")
	}
	if action.CurrentAttemptID != attempt.AttemptID {
		t.Fatal("test fixture lost canonical native attempt identity")
	}
}

func TestNativeDelegationCriteriaRevisionIsImmutableAfterAttemptCreation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "actions")
	store, _, attempt := createPreparedNativeDelegation(t, root)
	err := store.Transaction(context.Background(), func(tx domainaction.Store) error {
		current, err := tx.GetAttempt(context.Background(), attempt.AttemptID)
		if err != nil {
			return err
		}
		current.NativeDelegation.ExpectedCriteriaRevision = strings.Repeat("b", 64)
		return tx.SaveAttempt(context.Background(), current)
	})
	if err == nil {
		t.Fatal("Action owner allowed criteria revision to change after first save")
	}
	loaded, err := store.GetAttempt(context.Background(), attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.NativeDelegation.ExpectedCriteriaRevision != strings.Repeat("a", 64) {
		t.Fatalf("failed mutation changed the frozen Action criteria pin: %+v", loaded.NativeDelegation)
	}
}

func TestJSONLStoreSaveActionCannotMoveNativeIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "actions")
	store, action, _ := createPreparedNativeDelegation(t, root)
	mutations := []struct {
		name   string
		change func(*domainaction.Action)
	}{
		{name: "TaskID", change: func(value *domainaction.Action) { value.TaskID = modulecore.NewTaskID() }},
		{name: "RunID", change: func(value *domainaction.Action) { value.RunID = modulecore.NewRunID() }},
		{name: "Kind", change: func(value *domainaction.Action) { value.Kind = domainaction.KindTool }},
		{name: "Name", change: func(value *domainaction.Action) { value.Name = "ordinary.action" }},
		{name: "CurrentAttemptID", change: func(value *domainaction.Action) { value.CurrentAttemptID = modulecore.NewAttemptID() }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			err := store.Transaction(context.Background(), func(tx domainaction.Store) error {
				current, err := tx.GetAction(context.Background(), action.ActionID)
				if err != nil {
					return err
				}
				mutation.change(&current)
				return tx.SaveAction(context.Background(), current)
			})
			if err == nil {
				t.Fatalf("SaveAction() changed native Action identity field %s", mutation.name)
			}
			loaded, err := store.GetAction(context.Background(), action.ActionID)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.TaskID != action.TaskID || loaded.RunID != action.RunID || loaded.Kind != domainaction.KindDelegation || loaded.Name != domainaction.NativeDelegationActionName || loaded.CurrentAttemptID != action.CurrentAttemptID {
				t.Fatalf("rejected %s update changed the persisted Action", mutation.name)
			}
		})
	}
}

func createPreparedNativeDelegation(t *testing.T, root string) (*JSONLStore, domainaction.Action, domainaction.Attempt) {
	t.Helper()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	manager := actionmanager.New(store)
	action, attempt, err := manager.EnsureNativeDelegation(context.Background(), actionmanager.EnsureNativeDelegationInput{
		TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), ExpectedCriteriaRevision: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, attempt, err = manager.PrepareNativeMutation(context.Background(), actionmanager.PrepareNativeMutationInput{
		ActionID: action.ActionID, AttemptID: attempt.AttemptID,
		Slot: domainaction.NativeMutationSlotOpen, Key: "open-key", Payload: []byte(`{"open":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, action, attempt
}

func rewriteLastAttempt(t *testing.T, store *JSONLStore, mutate func(*domainaction.Attempt)) {
	t.Helper()
	data, err := os.ReadFile(store.attemptPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) == 0 {
		t.Fatal("attempt log has no records")
	}
	var attempt domainaction.Attempt
	if err := json.Unmarshal(lines[len(lines)-1], &attempt); err != nil {
		t.Fatal(err)
	}
	mutate(&attempt)
	updated, err := json.Marshal(attempt)
	if err != nil {
		t.Fatal(err)
	}
	lines[len(lines)-1] = updated
	if err := os.WriteFile(store.attemptPath, append(bytes.Join(lines, []byte{'\n'}), '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rewriteOnlyAction(t *testing.T, path string, value domainaction.Action) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rewriteOnlyAttempt(t *testing.T, path string, value domainaction.Attempt) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func nativeTestMutation(key string, payload []byte, status domainaction.NativeMutationStatus, result []byte, errorCode string) *domainaction.NativeMutation {
	hash := sha256.Sum256(payload)
	return &domainaction.NativeMutation{
		Key: key, Payload: append([]byte(nil), payload...), PayloadSHA256: hex.EncodeToString(hash[:]),
		Status: status, Result: append([]byte(nil), result...), ErrorCode: errorCode,
	}
}

func appendJSONLine(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
