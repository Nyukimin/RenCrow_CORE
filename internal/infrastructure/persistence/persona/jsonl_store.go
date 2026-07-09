package persona

import (
	"context"
	"path/filepath"

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
	}
}

func NewJSONLStoreWithMetaRoot(root, metaRoot string) *JSONLStore {
	store := NewJSONLStore(root)
	store.metaRoot = metaRoot
	return store
}

func (s *JSONLStore) CompactOperationalLogs() error {
	return jsonlutil.CompactLatestRecords(s.sessionPath, interfaceSessionMaxRecords)
}

func (s *JSONLStore) SaveDiscomfortLog(_ context.Context, item domainpersona.DiscomfortLog) error {
	if err := domainpersona.ValidateDiscomfortLog(item); err != nil {
		return err
	}
	return appendJSONL(s.discomfortPath, item)
}

func (s *JSONLStore) ListDiscomfortLogs(_ context.Context, limit int) ([]domainpersona.DiscomfortLog, error) {
	if limit <= 0 {
		limit = 50
	}
	return jsonlutil.ListLatest[domainpersona.DiscomfortLog](s.discomfortPath, limit)
}

func (s *JSONLStore) SaveTriggerLog(_ context.Context, item domainpersona.TriggerLog) error {
	if err := domainpersona.ValidateTriggerLog(item); err != nil {
		return err
	}
	return appendJSONL(s.triggerPath, item)
}

func (s *JSONLStore) ListTriggerLogs(_ context.Context, limit int) ([]domainpersona.TriggerLog, error) {
	if limit <= 0 {
		limit = 50
	}
	return jsonlutil.ListLatest[domainpersona.TriggerLog](s.triggerPath, limit)
}

func (s *JSONLStore) SaveCanonicalResponseLog(_ context.Context, item domainpersona.CanonicalResponseLog) error {
	if err := domainpersona.ValidateCanonicalResponseLog(item); err != nil {
		return err
	}
	return appendJSONL(s.canonicalPath, item)
}

func (s *JSONLStore) ListCanonicalResponseLogs(_ context.Context, limit int) ([]domainpersona.CanonicalResponseLog, error) {
	if limit <= 0 {
		limit = 50
	}
	return jsonlutil.ListLatest[domainpersona.CanonicalResponseLog](s.canonicalPath, limit)
}

func (s *JSONLStore) SaveObservationLog(_ context.Context, item domainpersona.ObservationLog) error {
	if err := domainpersona.ValidateObservationLog(item); err != nil {
		return err
	}
	return appendJSONL(s.observationPath, item)
}

func (s *JSONLStore) ListObservationLogs(_ context.Context, limit int) ([]domainpersona.ObservationLog, error) {
	if limit <= 0 {
		limit = 50
	}
	return jsonlutil.ListLatest[domainpersona.ObservationLog](s.observationPath, limit)
}

func (s *JSONLStore) SaveMetaProfileUpdate(_ context.Context, item domainpersona.MetaProfileUpdate) error {
	if err := domainpersona.ValidateMetaProfileUpdate(item); err != nil {
		return err
	}
	return appendJSONL(s.metaUpdatePath, item)
}

func (s *JSONLStore) ListMetaProfileUpdates(_ context.Context, limit int) ([]domainpersona.MetaProfileUpdate, error) {
	if limit <= 0 {
		limit = 50
	}
	return jsonlutil.ListLatest[domainpersona.MetaProfileUpdate](s.metaUpdatePath, limit)
}

func (s *JSONLStore) SaveInterfaceSession(_ context.Context, item domainpersona.InterfaceSession) error {
	if err := domainpersona.ValidateInterfaceSession(item); err != nil {
		return err
	}
	return appendJSONLBounded(s.sessionPath, item, interfaceSessionMaxRecords, interfaceSessionMaxBytes)
}

func (s *JSONLStore) ListInterfaceSessions(_ context.Context, limit int) ([]domainpersona.InterfaceSession, error) {
	if limit <= 0 {
		limit = 50
	}
	return jsonlutil.ListLatest[domainpersona.InterfaceSession](s.sessionPath, limit)
}

func appendJSONL(path string, value any) error {
	return jsonlutil.Append(path, value)
}

func appendJSONLBounded(path string, value any, maxRecords int, maxBytes int64) error {
	return jsonlutil.AppendBounded(path, value, jsonlutil.BoundOptions{
		MaxRecords: maxRecords,
		MaxBytes:   maxBytes,
	})
}
