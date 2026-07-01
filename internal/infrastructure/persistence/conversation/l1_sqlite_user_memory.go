package conversation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	domconv "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/memory"
)

func (s *L1SQLiteStore) CreateUserMemory(ctx context.Context, input domainmemory.CreateUserMemoryInput) (*domainmemory.UserMemory, error) {
	userID := strings.TrimSpace(input.UserID)
	if userID == "" {
		userID = "ren"
	}
	namespace, err := BuildL1Namespace(NamespaceKindUser, userID)
	if err != nil {
		return nil, err
	}
	memoryType := strings.TrimSpace(input.Type)
	if err := domainmemory.ValidateUserMemoryType(memoryType); err != nil {
		return nil, err
	}
	statement := strings.TrimSpace(input.Statement)
	if statement == "" {
		return nil, errors.New("user memory statement is required")
	}
	state := strings.TrimSpace(input.State)
	if state == "" {
		state = MemoryStateCandidate
	}
	if err := domainmemory.CanPromoteUserMemory(state, input.EvidenceEventIDs, input.Sensitivity, input.Source); err != nil {
		return nil, err
	}
	if err := validateMemoryState(state); err != nil {
		return nil, err
	}
	sensitivity := strings.TrimSpace(input.Sensitivity)
	if sensitivity == "" {
		sensitivity = "normal"
	}
	scope := strings.TrimSpace(input.Scope)
	if scope == "" {
		scope = "all_personas"
	}
	confidence := input.Confidence
	if confidence <= 0 {
		confidence = 0.5
	}
	source := strings.TrimSpace(input.Source)
	if source == "" {
		source = "viewer"
	}
	now := time.Now().UTC()
	meta := map[string]interface{}{
		"type":               memoryType,
		"user_id":            userID,
		"statement":          statement,
		"evidence_event_ids": input.EvidenceEventIDs,
		"confidence":         confidence,
		"sensitivity":        sensitivity,
		"scope":              scope,
		"active":             true,
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal user memory meta: %w", err)
	}
	id := fmt.Sprintf("%s:user_memory:%d", namespace, now.UnixNano())
	_, err = s.db.ExecContext(ctx, `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, id, namespace, "", 0, string(domconv.SpeakerMemory), statement, string(metaJSON), state, MemoryLayerL1, source, now, now)
	if err != nil {
		return nil, fmt.Errorf("failed to create user memory: %w", err)
	}
	if _, err := s.AppendEvent(ctx, "memory.user_created", namespace, "", 0, map[string]interface{}{
		"memory_id":          id,
		"user_id":            userID,
		"type":               memoryType,
		"memory_state":       state,
		"evidence_event_ids": input.EvidenceEventIDs,
	}, "memory"); err != nil {
		return nil, fmt.Errorf("failed to append user memory creation event: %w", err)
	}
	return l1EventToUserMemory(L1MemoryEvent{
		ID:          id,
		Namespace:   namespace,
		Speaker:     domconv.SpeakerMemory,
		Message:     statement,
		Meta:        meta,
		MemoryState: state,
		Layer:       MemoryLayerL1,
		Source:      source,
		CreatedAt:   now,
		UpdatedAt:   now,
	}), nil
}

func (s *L1SQLiteStore) ListUserMemories(ctx context.Context, userID string, state string, includeInactive bool, limit int) ([]domainmemory.UserMemory, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		userID = "ren"
	}
	namespace, err := BuildL1Namespace(NamespaceKindUser, userID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(state) != "" {
		if err := validateMemoryState(state); err != nil {
			return nil, err
		}
	}
	if limit <= 0 {
		limit = 20
	}
	var rows *sql.Rows
	if strings.TrimSpace(state) == "" {
		rows, err = s.db.QueryContext(ctx, `
SELECT id, namespace, session_id, thread_id, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event
WHERE namespace = ?
ORDER BY created_at DESC
LIMIT ?
`, namespace, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
SELECT id, namespace, session_id, thread_id, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event
WHERE namespace = ? AND memory_state = ?
ORDER BY created_at DESC
LIMIT ?
`, namespace, state, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query user memories: %w", err)
	}
	defer rows.Close()
	events, err := scanL1Events(rows)
	if err != nil {
		return nil, err
	}
	memories := make([]domainmemory.UserMemory, 0, len(events))
	for _, ev := range events {
		mem := l1EventToUserMemory(ev)
		if mem == nil {
			continue
		}
		if !includeInactive && !mem.Active {
			continue
		}
		memories = append(memories, *mem)
	}
	return memories, nil
}

func (s *L1SQLiteStore) ListPromptInjectableUserMemories(ctx context.Context, userID string, persona string, limit int) ([]domainmemory.UserMemory, error) {
	if limit <= 0 {
		limit = 12
	}
	items, err := s.ListUserMemories(ctx, userID, "", false, limit*4)
	if err != nil {
		return nil, err
	}
	out := make([]domainmemory.UserMemory, 0, limit)
	for _, item := range items {
		if !domainmemory.IsUserMemoryPromptInjectable(item, persona) {
			continue
		}
		out = append(out, item)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *L1SQLiteStore) UpdateUserMemoryState(ctx context.Context, id string, state string, reason string) (*domainmemory.UserMemory, error) {
	ev, err := s.memoryByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(ev.Namespace, "user:") {
		return nil, fmt.Errorf("memory is not user namespace: %s", ev.Namespace)
	}
	mem := l1EventToUserMemory(*ev)
	if mem == nil {
		return nil, errors.New("memory is not user memory")
	}
	if err := domainmemory.CanPromoteUserMemory(state, mem.EvidenceEventIDs, mem.Sensitivity, reason); err != nil {
		return nil, err
	}
	if err := s.UpdateMemoryState(ctx, id, state); err != nil {
		return nil, err
	}
	ev.MemoryState = state
	ev.UpdatedAt = time.Now().UTC()
	mem = l1EventToUserMemory(*ev)
	return mem, nil
}

func (s *L1SQLiteStore) ForgetUserMemory(ctx context.Context, id string, reason string) (*domainmemory.UserMemory, error) {
	ev, err := s.memoryByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(ev.Namespace, "user:") {
		return nil, fmt.Errorf("memory is not user namespace: %s", ev.Namespace)
	}
	meta := ev.Meta
	if meta == nil {
		meta = map[string]interface{}{}
	}
	meta["active"] = false
	meta["forget_reason"] = strings.TrimSpace(reason)
	meta["forgot_at"] = time.Now().UTC().Format(time.RFC3339)
	if err := s.updateMemoryMeta(ctx, id, meta); err != nil {
		return nil, err
	}
	if _, err := s.AppendEvent(ctx, "memory.user_forgotten", ev.Namespace, ev.SessionID, ev.ThreadID, map[string]interface{}{
		"memory_id": id,
		"reason":    reason,
	}, "memory"); err != nil {
		return nil, err
	}
	ev.Meta = meta
	ev.UpdatedAt = time.Now().UTC()
	return l1EventToUserMemory(*ev), nil
}

func (s *L1SQLiteStore) SupersedeUserMemory(ctx context.Context, oldID string, newID string, reason string) (*domainmemory.UserMemory, error) {
	old, err := s.memoryByID(ctx, oldID)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(old.Namespace, "user:") {
		return nil, fmt.Errorf("memory is not user namespace: %s", old.Namespace)
	}
	if strings.TrimSpace(newID) != "" {
		newMem, err := s.memoryByID(ctx, newID)
		if err != nil {
			return nil, err
		}
		if old.Namespace != newMem.Namespace {
			return nil, errors.New("superseding memory must be in the same user namespace")
		}
	}
	meta := old.Meta
	if meta == nil {
		meta = map[string]interface{}{}
	}
	meta["active"] = false
	meta["superseded_by"] = strings.TrimSpace(newID)
	meta["supersede_reason"] = strings.TrimSpace(reason)
	meta["superseded_at"] = time.Now().UTC().Format(time.RFC3339)
	if err := s.updateMemoryMeta(ctx, oldID, meta); err != nil {
		return nil, err
	}
	if _, err := s.AppendEvent(ctx, "memory.user_superseded", old.Namespace, old.SessionID, old.ThreadID, map[string]interface{}{
		"memory_id":     oldID,
		"superseded_by": strings.TrimSpace(newID),
		"reason":        reason,
	}, "memory"); err != nil {
		return nil, err
	}
	old.Meta = meta
	old.UpdatedAt = time.Now().UTC()
	return l1EventToUserMemory(*old), nil
}

func (s *L1SQLiteStore) updateMemoryMeta(ctx context.Context, id string, meta map[string]interface{}) error {
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal memory meta: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE l1_memory_event
SET meta_json = ?, updated_at = ?
WHERE id = ?
`, string(metaJSON), time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("failed to update memory meta: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to inspect memory meta update: %w", err)
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func l1EventToUserMemory(ev L1MemoryEvent) *domainmemory.UserMemory {
	if !strings.HasPrefix(ev.Namespace, "user:") {
		return nil
	}
	memoryType := metaStringValue(ev.Meta, "type")
	if memoryType == "" {
		return nil
	}
	userID := strings.TrimPrefix(ev.Namespace, "user:")
	active := true
	if raw, ok := ev.Meta["active"]; ok {
		if b, ok := raw.(bool); ok {
			active = b
		}
	}
	confidence := 0.5
	if raw, ok := ev.Meta["confidence"]; ok {
		switch v := raw.(type) {
		case float64:
			confidence = v
		case float32:
			confidence = float64(v)
		}
	}
	return &domainmemory.UserMemory{
		ID:               ev.ID,
		Namespace:        ev.Namespace,
		UserID:           userID,
		Type:             memoryType,
		Statement:        firstNonEmptyString(metaStringValue(ev.Meta, "statement"), ev.Message),
		EvidenceEventIDs: metaStringSliceValue(ev.Meta, "evidence_event_ids"),
		Confidence:       confidence,
		Sensitivity:      firstNonEmptyString(metaStringValue(ev.Meta, "sensitivity"), "normal"),
		State:            ev.MemoryState,
		Scope:            firstNonEmptyString(metaStringValue(ev.Meta, "scope"), "all_personas"),
		Active:           active,
		LifecycleStatus:  metaStringValue(ev.Meta, "lifecycle_status"),
		DecayScore:       metaFloatValue(ev.Meta, "decay_score"),
		SupersededBy:     metaStringValue(ev.Meta, "superseded_by"),
		CreatedAt:        ev.CreatedAt,
		UpdatedAt:        ev.UpdatedAt,
	}
}

func metaStringValue(meta map[string]interface{}, key string) string {
	if meta == nil {
		return ""
	}
	if raw, ok := meta[key]; ok {
		if s, ok := raw.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func metaStringSliceValue(meta map[string]interface{}, key string) []string {
	if meta == nil {
		return nil
	}
	raw, ok := meta[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return append([]string(nil), v...)
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	default:
		return nil
	}
}

func metaFloatValue(meta map[string]interface{}, key string) float64 {
	if meta == nil {
		return 0
	}
	raw, ok := meta[key]
	if !ok {
		return 0
	}
	switch v := raw.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		return 0
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
