package persona

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlutil"
)

const (
	interfaceSessionMaxRecords = 5000
	interfaceSessionMaxBytes   = int64(8 << 20)
)

type JSONLStore struct {
	discomfortPath  string
	triggerPath     string
	canonicalPath   string
	observationPath string
	metaUpdatePath  string
	sessionPath     string
	metaRoot        string
	admissionWrites *personaAdmissionState
}

func NewJSONLStore(root string) *JSONLStore {
	if root == "" {
		root = "workspace/logs/persona"
	}
	return &JSONLStore{
		discomfortPath:  filepath.Join(root, "persona_discomfort_log.jsonl"),
		triggerPath:     filepath.Join(root, "persona_trigger_log.jsonl"),
		canonicalPath:   filepath.Join(root, "canonical_response_log.jsonl"),
		observationPath: filepath.Join(root, "observation_log.jsonl"),
		metaUpdatePath:  filepath.Join(root, "meta_profile_update.jsonl"),
		sessionPath:     filepath.Join(root, "persona_interface_session.jsonl"),
		admissionWrites: newPersonaAdmissionState(),
	}
}

func NewJSONLStoreWithMetaRoot(root, metaRoot string) *JSONLStore {
	store := NewJSONLStore(root)
	store.metaRoot = metaRoot
	return store
}

func (s *JSONLStore) SetPersistenceQueue(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) error {
	if s == nil {
		return ErrPersonaAdmissionUnavailable
	}
	if err := s.admissionWrites.validateQueueBinding(queue); err != nil {
		return err
	}
	release, err := s.admissionWrites.beginSynchronous(context.Background())
	if err != nil {
		return err
	}
	defer release()
	snapshots, err := s.loadAdmissionSnapshots()
	if err != nil {
		return fmt.Errorf("warm Persona JSONL admission read snapshots: %w", err)
	}
	for table, snapshot := range snapshots {
		s.admissionWrites.setSnapshot(table, snapshot.writes, personaAdmissionListSnapshotLimit, snapshot.complete)
	}
	return s.admissionWrites.setQueue(queue)
}

func (s *JSONLStore) MatchesPersistenceQueue(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) bool {
	return s != nil && s.admissionWrites.matchesQueue(queue)
}

func (s *JSONLStore) CompactOperationalLogs() error {
	if s == nil {
		return ErrPersonaAdmissionUnavailable
	}
	release, err := s.admissionWrites.beginSynchronous(context.Background())
	if err != nil {
		return err
	}
	defer release()
	return jsonlutil.CompactLatestRecords(s.sessionPath, interfaceSessionMaxRecords)
}

func (s *JSONLStore) SaveDiscomfortLog(ctx context.Context, item domainpersona.DiscomfortLog) error {
	if err := domainpersona.ValidateDiscomfortLog(item); err != nil {
		return err
	}
	return s.saveJSONL(ctx, "persona_discomfort_log", item.EventID, s.discomfortPath, item, false)
}

func (s *JSONLStore) ListDiscomfortLogs(ctx context.Context, limit int) ([]domainpersona.DiscomfortLog, error) {
	return listJSONLItems[domainpersona.DiscomfortLog](ctx, s, "persona_discomfort_log", s.discomfortPath, limit)
}

func (s *JSONLStore) SaveTriggerLog(ctx context.Context, item domainpersona.TriggerLog) error {
	if err := domainpersona.ValidateTriggerLog(item); err != nil {
		return err
	}
	return s.saveJSONL(ctx, "persona_trigger_log", item.EventID, s.triggerPath, item, false)
}

func (s *JSONLStore) ListTriggerLogs(ctx context.Context, limit int) ([]domainpersona.TriggerLog, error) {
	return listJSONLItems[domainpersona.TriggerLog](ctx, s, "persona_trigger_log", s.triggerPath, limit)
}

func (s *JSONLStore) SaveCanonicalResponseLog(ctx context.Context, item domainpersona.CanonicalResponseLog) error {
	if err := domainpersona.ValidateCanonicalResponseLog(item); err != nil {
		return err
	}
	return s.saveJSONL(ctx, "canonical_response_log", item.EventID, s.canonicalPath, item, false)
}

func (s *JSONLStore) ListCanonicalResponseLogs(ctx context.Context, limit int) ([]domainpersona.CanonicalResponseLog, error) {
	return listJSONLItems[domainpersona.CanonicalResponseLog](ctx, s, "canonical_response_log", s.canonicalPath, limit)
}

func (s *JSONLStore) SaveObservationLog(ctx context.Context, item domainpersona.ObservationLog) error {
	if err := domainpersona.ValidateObservationLog(item); err != nil {
		return err
	}
	return s.saveJSONL(ctx, "observation_log", item.EventID, s.observationPath, item, false)
}

func (s *JSONLStore) ListObservationLogs(ctx context.Context, limit int) ([]domainpersona.ObservationLog, error) {
	return listJSONLItems[domainpersona.ObservationLog](ctx, s, "observation_log", s.observationPath, limit)
}

// FindObservationLogByID returns the latest JSONL record with the exact primary ID.
func (s *JSONLStore) FindObservationLogByID(ctx context.Context, eventID string) (domainpersona.ObservationLog, bool, error) {
	if s == nil {
		return domainpersona.ObservationLog{}, false, ErrPersonaAdmissionUnavailable
	}
	if admission, ok := persistencequeue.AdmissionFromContext(ctx); ok {
		write, found, complete, err := s.admissionWrites.observationSnapshotAndPending(admission, eventID)
		if err != nil {
			return domainpersona.ObservationLog{}, false, err
		}
		if found {
			item, err := decodePersonaAdmissionWrite[domainpersona.ObservationLog](write)
			if err != nil {
				return domainpersona.ObservationLog{}, false, err
			}
			return item, true, nil
		}
		if !complete {
			return domainpersona.ObservationLog{}, false, ErrPersonaAdmissionSnapshotUnavailable
		}
		return domainpersona.ObservationLog{}, false, nil
	}
	var found domainpersona.ObservationLog
	matched := false
	if err := jsonlutil.Read(s.observationPath, func(line []byte) error {
		var item domainpersona.ObservationLog
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		if item.EventID == eventID {
			found = item
			matched = true
		}
		return nil
	}); err != nil {
		return domainpersona.ObservationLog{}, false, err
	}
	return found, matched, nil
}

func (s *JSONLStore) SaveMetaProfileUpdate(ctx context.Context, item domainpersona.MetaProfileUpdate) error {
	if err := domainpersona.ValidateMetaProfileUpdate(item); err != nil {
		return err
	}
	return s.saveJSONL(ctx, "meta_profile_update", item.UpdateID, s.metaUpdatePath, item, false)
}

func (s *JSONLStore) ListMetaProfileUpdates(ctx context.Context, limit int) ([]domainpersona.MetaProfileUpdate, error) {
	return listJSONLItems[domainpersona.MetaProfileUpdate](ctx, s, "meta_profile_update", s.metaUpdatePath, limit)
}

func (s *JSONLStore) SaveInterfaceSession(ctx context.Context, item domainpersona.InterfaceSession) error {
	if err := domainpersona.ValidateInterfaceSession(item); err != nil {
		return err
	}
	return s.saveJSONL(ctx, "persona_interface_session", item.SessionID, s.sessionPath, item, true)
}

func (s *JSONLStore) ListInterfaceSessions(ctx context.Context, limit int) ([]domainpersona.InterfaceSession, error) {
	return listJSONLItems[domainpersona.InterfaceSession](ctx, s, "persona_interface_session", s.sessionPath, limit)
}

func (s *JSONLStore) saveJSONL(ctx context.Context, table, key, path string, item any, bounded bool) error {
	if s == nil {
		return ErrPersonaAdmissionUnavailable
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	immutablePayload := string(payload)
	if admission, ok := persistencequeue.AdmissionFromContext(ctx); ok {
		return s.admissionWrites.enqueue(admission, table, key, "jsonl."+table+".append", immutablePayload, func(context.Context) error {
			if bounded {
				return appendJSONLPayloadBounded(path, immutablePayload, interfaceSessionMaxRecords, interfaceSessionMaxBytes)
			}
			return appendJSONLPayload(path, immutablePayload)
		})
	}
	release, err := s.admissionWrites.beginSynchronous(ctx)
	if err != nil {
		return err
	}
	defer release()
	if bounded {
		err = appendJSONLPayloadBounded(path, immutablePayload, interfaceSessionMaxRecords, interfaceSessionMaxBytes)
	} else {
		err = appendJSONLPayload(path, immutablePayload)
	}
	if err == nil {
		s.admissionWrites.recordSynchronous(table, key, immutablePayload)
	}
	return err
}

func listJSONLItems[T any](ctx context.Context, s *JSONLStore, table, path string, limit int) ([]T, error) {
	if s == nil {
		return nil, ErrPersonaAdmissionUnavailable
	}
	if limit <= 0 {
		limit = 50
	}
	if admission, ok := persistencequeue.AdmissionFromContext(ctx); ok {
		snapshot, admissionWrites, _, err := s.admissionWrites.snapshotAndPendingFor(admission, table, limit)
		if err != nil {
			return nil, err
		}
		items := make([]T, 0, len(snapshot))
		for _, write := range snapshot {
			item, err := decodePersonaAdmissionWrite[T](write)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return mergePersonaAdmissionWrites(items, admissionWrites, limit)
	}
	return jsonlutil.ListLatest[T](path, limit)
}

func (s *JSONLStore) loadAdmissionSnapshots() (map[string]personaReadSnapshot, error) {
	snapshots := make(map[string]personaReadSnapshot, 6)
	if snapshot, err := snapshotJSONLValues[domainpersona.DiscomfortLog]("persona_discomfort_log", s.discomfortPath, personaAdmissionListSnapshotLimit); err != nil {
		return nil, err
	} else {
		snapshots["persona_discomfort_log"] = snapshot
	}
	if snapshot, err := snapshotJSONLValues[domainpersona.TriggerLog]("persona_trigger_log", s.triggerPath, personaAdmissionListSnapshotLimit); err != nil {
		return nil, err
	} else {
		snapshots["persona_trigger_log"] = snapshot
	}
	if snapshot, err := snapshotJSONLValues[domainpersona.CanonicalResponseLog]("canonical_response_log", s.canonicalPath, personaAdmissionListSnapshotLimit); err != nil {
		return nil, err
	} else {
		snapshots["canonical_response_log"] = snapshot
	}
	if snapshot, err := snapshotJSONLValues[domainpersona.ObservationLog]("observation_log", s.observationPath, personaAdmissionListSnapshotLimit); err != nil {
		return nil, err
	} else {
		snapshots["observation_log"] = snapshot
	}
	if snapshot, err := snapshotJSONLValues[domainpersona.MetaProfileUpdate]("meta_profile_update", s.metaUpdatePath, personaAdmissionListSnapshotLimit); err != nil {
		return nil, err
	} else {
		snapshots["meta_profile_update"] = snapshot
	}
	if snapshot, err := snapshotJSONLValues[domainpersona.InterfaceSession]("persona_interface_session", s.sessionPath, personaAdmissionListSnapshotLimit); err != nil {
		return nil, err
	} else {
		snapshots["persona_interface_session"] = snapshot
	}
	return snapshots, nil
}

func snapshotJSONLValues[T any](table, path string, limit int) (personaReadSnapshot, error) {
	items, err := jsonlutil.ListLatest[T](path, limit+1)
	if err != nil {
		return personaReadSnapshot{}, err
	}
	complete := len(items) <= limit
	if len(items) > limit {
		items = items[:limit]
	}
	writes := make([]personaAdmissionWrite, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		key, ok := personaRecordKey(item)
		if !ok {
			return personaReadSnapshot{}, fmt.Errorf("unsupported Persona snapshot record %T", item)
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		payload, err := json.Marshal(item)
		if err != nil {
			return personaReadSnapshot{}, err
		}
		writes = append(writes, personaAdmissionWrite{table: table, key: key, payload: string(payload)})
	}
	return personaReadSnapshot{writes: writes, complete: complete}, nil
}

func appendJSONLPayload(path, payload string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append([]byte(payload), '\n'))
	return err
}

func appendJSONLPayloadBounded(path, payload string, maxRecords int, maxBytes int64) error {
	if err := appendJSONLPayload(path, payload); err != nil {
		return err
	}
	if maxRecords <= 0 && maxBytes <= 0 {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if maxBytes > 0 && info.Size() <= maxBytes {
		return nil
	}
	return jsonlutil.CompactLatestRecords(path, maxRecords)
}
