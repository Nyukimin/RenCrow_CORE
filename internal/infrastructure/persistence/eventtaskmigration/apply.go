package eventtaskmigration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func applyFresh(ctx context.Context, path string, events []modulecore.EventEnvelope, expectedHash string) (resultErr error) {
	if err := requireAbsentTarget(path); err != nil {
		return err
	}
	store, err := eventstore.NewSQLiteStore(path)
	if err != nil {
		return coded("target_store", "create fresh target: %v", err)
	}
	created := true
	defer func() {
		if resultErr != nil && created {
			if cleanupErr := cleanupAppliedTargets(resolvedPaths{targetEventStore: path}, true, false, false); cleanupErr != nil {
				resultErr = fmt.Errorf("%w; clean failed event target: %v", resultErr, cleanupErr)
			}
		}
	}()
	if err := store.AppendBatch(ctx, events); err != nil {
		_ = store.Close()
		return coded("target_store", "append migration batch: %v", err)
	}
	if err := verifyTarget(ctx, store, events, expectedHash); err != nil {
		_ = store.Close()
		return err
	}
	if err := store.Close(); err != nil {
		return coded("target_store", "close target: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return coded("target_store", "secure target: %v", err)
	}
	if err := verifyTargetTables(ctx, path, events); err != nil {
		return err
	}
	created = false
	return nil
}

func applyAllFresh(ctx context.Context, paths resolvedPaths, plan prepared) (resultErr error) {
	eventCreated := false
	reportsCreated := false
	resilienceCreated := false
	defer func() {
		if resultErr != nil {
			if cleanupErr := cleanupAppliedTargets(paths, eventCreated, reportsCreated, resilienceCreated); cleanupErr != nil {
				resultErr = fmt.Errorf("%w; clean incomplete migration targets: %v", resultErr, cleanupErr)
			}
		}
	}()
	if err := applyFresh(ctx, paths.targetEventStore, plan.events, plan.manifest.CanonicalOutputSetSHA256); err != nil {
		return err
	}
	eventCreated = true
	if err := writeAndVerifyExecutionReports(paths.targetExecutionReports, plan.reports, plan.manifest.CanonicalExecutionReportsSHA256); err != nil {
		return err
	}
	reportsCreated = true
	if err := writeAndVerifyResilienceRoot(paths.targetResilienceRoot, plan.resilience); err != nil {
		return err
	}
	resilienceCreated = true
	return nil
}

func cleanupAppliedTargets(paths resolvedPaths, eventCreated, reportsCreated, resilienceCreated bool) error {
	var cleanupErrors []error
	var files []string
	if eventCreated {
		files = append(files, paths.targetEventStore, paths.targetEventStore+"-wal", paths.targetEventStore+"-shm", paths.targetEventStore+"-journal")
	}
	if reportsCreated {
		files = append(files, paths.targetExecutionReports)
	}
	for _, path := range files {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			cleanupErrors = append(cleanupErrors, err)
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("refuse to remove unsafe migration target %s", path))
			continue
		}
		if err := os.Remove(path); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if !resilienceCreated {
		return errors.Join(cleanupErrors...)
	}
	if info, err := os.Lstat(paths.targetResilienceRoot); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("refuse to remove unsafe migration target %s", paths.targetResilienceRoot))
		} else if err := os.RemoveAll(paths.targetResilienceRoot); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		cleanupErrors = append(cleanupErrors, err)
	}
	return errors.Join(cleanupErrors...)
}

func requireAbsentTarget(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		_, err := os.Lstat(candidate)
		if err == nil {
			return coded("target_exists", "target or sidecar already exists")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return coded("target_store", "inspect target: %v", err)
		}
	}
	return nil
}

func verifyTarget(ctx context.Context, store *eventstore.SQLiteStore, expected []modulecore.EventEnvelope, expectedHash string) error {
	actual := make([]modulecore.EventEnvelope, len(expected))
	for i, event := range expected {
		stored, found, err := store.GetByID(ctx, event.EventID)
		if err != nil || !found {
			return coded("target_verify", "read event %q after apply: found=%v err=%v", event.EventID, found, err)
		}
		expectedJSON, _ := json.Marshal(event)
		storedJSON, _ := json.Marshal(stored)
		if string(expectedJSON) != string(storedJSON) {
			return coded("target_verify", "stored event %q differs from planned event", event.EventID)
		}
		actual[i] = stored
	}
	digest, err := canonicalOutputSHA(actual)
	if err != nil || digest != expectedHash {
		return coded("target_verify", "canonical output checksum mismatch")
	}
	return nil
}

func verifyTargetTables(ctx context.Context, path string, expected []modulecore.EventEnvelope) error {
	db, err := openReadOnly(path)
	if err != nil {
		return coded("target_verify", "reopen fresh target: %v", err)
	}
	defer db.Close()
	var count, minimum, maximum, distinct int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MIN(event_seq),0), COALESCE(MAX(event_seq),0), COUNT(DISTINCT event_seq) FROM event_envelope`).Scan(&count, &minimum, &maximum, &distinct); err != nil {
		return coded("target_verify", "read target sequence set: %v", err)
	}
	if count != len(expected) || distinct != len(expected) || minimum != sequenceMinimum(len(expected)) || maximum != len(expected) {
		return coded("target_verify", "target row or sequence set differs from plan")
	}
	rows, err := db.QueryContext(ctx, `SELECT event_id, dependency_event_id, relation_type FROM event_dependency`)
	if err != nil {
		return coded("target_verify", "read target dependency set: %v", err)
	}
	defer rows.Close()
	actual := make(map[string]int)
	for rows.Next() {
		var eventID, dependencyID, relation string
		if err := rows.Scan(&eventID, &dependencyID, &relation); err != nil {
			return coded("target_verify", "scan target dependency: %v", err)
		}
		actual[eventID+"\x00"+dependencyID+"\x00"+relation]++
	}
	if err := rows.Err(); err != nil {
		return coded("target_verify", "read target dependency set: %v", err)
	}
	expectedEdges := make(map[string]int)
	for _, event := range expected {
		if event.CausationEventID != "" {
			expectedEdges[string(event.EventID)+"\x00"+string(event.CausationEventID)+"\x00causation"]++
		}
		for _, dependencyID := range event.DependencyEventIDs {
			expectedEdges[string(event.EventID)+"\x00"+string(dependencyID)+"\x00dependency"]++
		}
	}
	if len(actual) != len(expectedEdges) {
		return coded("target_verify", "target dependency count differs from plan")
	}
	for key, value := range expectedEdges {
		if value != 1 || actual[key] != 1 {
			return coded("target_verify", "target dependency set differs from plan")
		}
	}
	return nil
}

func sequenceMinimum(count int) int {
	if count == 0 {
		return 0
	}
	return 1
}
