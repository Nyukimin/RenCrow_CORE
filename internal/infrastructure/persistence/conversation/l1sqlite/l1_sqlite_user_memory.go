package l1sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
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
	metaJSON, err := marshalL1MetaJSON(meta, "failed to marshal user memory meta")
	if err != nil {
		return nil, err
	}
	id := fmt.Sprintf("%s:user_memory:%d:%d", namespace, now.UnixNano(), l1IDSequence.Add(1))
	_, err = s.db.ExecContext(ctx, `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, id, namespace, "", 0, string(domconv.SpeakerMemory), statement, metaJSON, state, MemoryLayerL1, source, now, now)
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

const userMemoryCandidateIDPrefix = "user-memory-candidate/sha256:"

// CreateUserMemoryCandidateWithRequest persists one owner-proposed user
// memory and its audit event as one SQLite transaction. The request ID is the
// stable idempotency identity; state and source are intentionally owned here,
// rather than by the caller-controlled input.
func (s *L1SQLiteStore) CreateUserMemoryCandidateWithRequest(ctx context.Context, requestID, actorID string, input domainmemory.CreateUserMemoryInput) (*domainmemory.UserMemory, bool, error) {
	requestID = strings.TrimSpace(requestID)
	actorID = strings.TrimSpace(actorID)
	if requestID == "" {
		return nil, false, errors.New("user memory request_id is required")
	}
	if actorID == "" {
		return nil, false, errors.New("user memory actor_id is required")
	}
	normalized, namespace, err := normalizeUserMemoryCandidateInput(input)
	if err != nil {
		return nil, false, err
	}
	source := "agent:" + actorID
	candidateID := userMemoryCandidateID(requestID)

	meta := map[string]interface{}{
		"type":               normalized.Type,
		"user_id":            normalized.UserID,
		"statement":          normalized.Statement,
		"evidence_event_ids": normalized.EvidenceEventIDs,
		"confidence":         normalized.Confidence,
		"sensitivity":        normalized.Sensitivity,
		"scope":              normalized.Scope,
		"active":             true,
		"actor_id":           actorID,
		"request_id":         requestID,
	}
	metaJSON, err := marshalL1MetaJSON(meta, "failed to marshal user memory candidate meta")
	if err != nil {
		return nil, false, err
	}
	now := time.Now().UTC()
	expected := domainmemory.UserMemory{
		ID:               candidateID,
		Namespace:        namespace,
		UserID:           normalized.UserID,
		Type:             normalized.Type,
		Statement:        normalized.Statement,
		EvidenceEventIDs: append([]string(nil), normalized.EvidenceEventIDs...),
		Confidence:       normalized.Confidence,
		Sensitivity:      normalized.Sensitivity,
		State:            MemoryStateCandidate,
		Scope:            normalized.Scope,
		Active:           true,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	existing, found, err := findL1MemoryEventByID(ctx, tx, candidateID)
	if err != nil {
		return nil, false, rollbackL1Tx(tx, err)
	}
	if found {
		existingMemory, err := strictUserMemoryFromEvent(existing)
		if err != nil {
			return nil, false, rollbackL1Tx(tx, fmt.Errorf("existing user memory candidate is invalid: %w", err))
		}
		if existing.Source != source || !userMemoryLogicalEqual(*existingMemory, expected) {
			return nil, false, rollbackL1Tx(tx, errors.New("user memory request idempotency conflict"))
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return existingMemory, true, nil
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, candidateID, namespace, "", 0, string(domconv.SpeakerMemory), normalized.Statement, metaJSON,
		MemoryStateCandidate, MemoryLayerL1, source, now, now); err != nil {
		return nil, false, rollbackL1Tx(tx, fmt.Errorf("failed to create user memory candidate: %w", err))
	}
	if _, err := appendL1EventLog(ctx, tx, "memory.user_created", namespace, "", 0, map[string]interface{}{
		"memory_id":          candidateID,
		"request_id":         requestID,
		"actor_id":           actorID,
		"user_id":            normalized.UserID,
		"type":               normalized.Type,
		"memory_state":       MemoryStateCandidate,
		"evidence_event_ids": normalized.EvidenceEventIDs,
	}, source); err != nil {
		return nil, false, rollbackL1Tx(tx, fmt.Errorf("failed to append user memory candidate audit event: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return &expected, false, nil
}

func normalizeUserMemoryCandidateInput(input domainmemory.CreateUserMemoryInput) (domainmemory.CreateUserMemoryInput, string, error) {
	input.UserID = strings.TrimSpace(input.UserID)
	if input.UserID == "" {
		return domainmemory.CreateUserMemoryInput{}, "", errors.New("user memory user_id is required")
	}
	input.Type = strings.TrimSpace(input.Type)
	if err := domainmemory.ValidateUserMemoryType(input.Type); err != nil {
		return domainmemory.CreateUserMemoryInput{}, "", err
	}
	input.Statement = strings.TrimSpace(input.Statement)
	if input.Statement == "" {
		return domainmemory.CreateUserMemoryInput{}, "", errors.New("user memory statement is required")
	}
	if len(input.EvidenceEventIDs) > 0 {
		evidenceIDs := make([]string, len(input.EvidenceEventIDs))
		for i, evidenceID := range input.EvidenceEventIDs {
			evidenceIDs[i] = strings.TrimSpace(evidenceID)
			if evidenceIDs[i] == "" {
				return domainmemory.CreateUserMemoryInput{}, "", fmt.Errorf("user memory evidence_event_ids[%d] is required", i)
			}
		}
		input.EvidenceEventIDs = evidenceIDs
	} else {
		input.EvidenceEventIDs = nil
	}
	if input.Confidence <= 0 {
		input.Confidence = 0.5
	}
	input.Sensitivity = strings.TrimSpace(input.Sensitivity)
	if input.Sensitivity == "" {
		input.Sensitivity = "normal"
	}
	input.Scope = strings.TrimSpace(input.Scope)
	if input.Scope == "" {
		input.Scope = "all_personas"
	}
	input.State = MemoryStateCandidate
	input.Source = ""
	namespace, err := BuildL1Namespace(NamespaceKindUser, input.UserID)
	if err != nil {
		return domainmemory.CreateUserMemoryInput{}, "", err
	}
	if err := domainmemory.CanPromoteUserMemory(MemoryStateCandidate, input.EvidenceEventIDs, input.Sensitivity, ""); err != nil {
		return domainmemory.CreateUserMemoryInput{}, "", err
	}
	return input, namespace, nil
}

func userMemoryCandidateID(requestID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(requestID)))
	return userMemoryCandidateIDPrefix + hex.EncodeToString(digest[:])
}

type l1SQLRowQueryer interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

func findL1MemoryEventByID(ctx context.Context, queryer l1SQLRowQueryer, id string) (L1MemoryEvent, bool, error) {
	if strings.TrimSpace(id) == "" {
		return L1MemoryEvent{}, false, errors.New("user memory id is required")
	}
	events, err := scanL1EventRows(queryer.QueryRowContext(ctx, `
SELECT id, namespace, session_id, thread_id, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event
WHERE id = ?
`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return L1MemoryEvent{}, false, nil
	}
	if err != nil {
		return L1MemoryEvent{}, false, err
	}
	if len(events) != 1 {
		return L1MemoryEvent{}, false, errors.New("user memory exact lookup returned an invalid row count")
	}
	return events[0], true, nil
}

// FindUserMemoryByID performs an exact primary-ID lookup and rejects rows
// whose namespace, user projection, or memory metadata do not agree.
func (s *L1SQLiteStore) FindUserMemoryByID(ctx context.Context, id string) (domainmemory.UserMemory, bool, error) {
	event, found, err := findL1MemoryEventByID(ctx, s.db, strings.TrimSpace(id))
	if err != nil || !found {
		return domainmemory.UserMemory{}, found, err
	}
	memory, err := strictUserMemoryFromEvent(event)
	if err != nil {
		return domainmemory.UserMemory{}, false, err
	}
	return *memory, true, nil
}

// FindUserMemoryEventByID returns the exact storage event only when its
// validated user projection belongs to userID. Archive workflows use this
// owner-scoped form so they can copy the canonical event without rebuilding
// hidden metadata from a public projection.
func (s *L1SQLiteStore) FindUserMemoryEventByID(ctx context.Context, userID, id string) (L1MemoryEvent, bool, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return L1MemoryEvent{}, false, errors.New("user memory user_id is required")
	}
	event, found, err := findL1MemoryEventByID(ctx, s.db, strings.TrimSpace(id))
	if err != nil || !found {
		return L1MemoryEvent{}, found, err
	}
	memory, err := strictUserMemoryFromEvent(event)
	if err != nil {
		return L1MemoryEvent{}, false, err
	}
	if memory.UserID != userID || event.Namespace != NamespaceKindUser+":"+userID {
		return L1MemoryEvent{}, false, nil
	}
	return event, true, nil
}

func strictUserMemoryFromEvent(event L1MemoryEvent) (*domainmemory.UserMemory, error) {
	if event.Speaker != domconv.SpeakerMemory {
		return nil, errors.New("user memory row speaker is invalid")
	}
	if event.Layer != MemoryLayerL1 {
		return nil, errors.New("user memory row layer is invalid")
	}
	if !strings.HasPrefix(event.Namespace, NamespaceKindUser+":") {
		return nil, errors.New("user memory row namespace is not user-scoped")
	}
	memory := l1EventToUserMemory(event)
	if memory == nil {
		return nil, errors.New("user memory row metadata is invalid")
	}
	if err := domainmemory.ValidateUserMemoryType(memory.Type); err != nil {
		return nil, err
	}
	if err := domainmemory.ValidateMemoryState(memory.State); err != nil {
		return nil, err
	}
	if memory.UserID == "" || event.Namespace != NamespaceKindUser+":"+memory.UserID {
		return nil, errors.New("user memory row user namespace mismatch")
	}
	if metaUserID := metaStringValue(event.Meta, "user_id"); metaUserID == "" || metaUserID != memory.UserID {
		return nil, errors.New("user memory row user_id metadata mismatch")
	}
	if metaStatement := metaStringValue(event.Meta, "statement"); metaStatement == "" || metaStatement != strings.TrimSpace(event.Message) || memory.Statement != strings.TrimSpace(event.Message) {
		return nil, errors.New("user memory row statement metadata mismatch")
	}
	for i, evidenceID := range memory.EvidenceEventIDs {
		if strings.TrimSpace(evidenceID) == "" {
			return nil, fmt.Errorf("user memory row evidence_event_ids[%d] is empty", i)
		}
	}
	return memory, nil
}

func userMemoryLogicalEqual(left, right domainmemory.UserMemory) bool {
	left.CreatedAt = time.Time{}
	left.UpdatedAt = time.Time{}
	right.CreatedAt = time.Time{}
	right.UpdatedAt = time.Time{}
	return reflect.DeepEqual(left, right)
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
ORDER BY created_at DESC, rowid DESC
LIMIT ?
`, namespace, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
SELECT id, namespace, session_id, thread_id, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event
WHERE namespace = ? AND memory_state = ?
ORDER BY created_at DESC, rowid DESC
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

// ListUserMemoriesPage provides an exact, searchable owner-facing projection.
// It is intentionally separate from bounded prompt recall so Viewer browsing
// cannot widen the LLM injection contract.
func (s *L1SQLiteStore) ListUserMemoriesPage(ctx context.Context, userID, state string, includeInactive bool, query string, limit, offset int) ([]domainmemory.UserMemory, bool, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		userID = "ren"
	}
	namespace, err := BuildL1Namespace(NamespaceKindUser, userID)
	if err != nil {
		return nil, false, err
	}
	state = strings.TrimSpace(state)
	if state != "" {
		if err := validateMemoryState(state); err != nil {
			return nil, false, err
		}
	}
	query = strings.TrimSpace(query)
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		return nil, false, errors.New("user memory offset must be non-negative")
	}

	where := "namespace = ?"
	args := []interface{}{namespace}
	if !includeInactive {
		where += " AND active = 1"
	}
	if state != "" {
		where += " AND memory_state = ?"
		args = append(args, state)
	}
	if query != "" {
		where += " AND (instr(lower(statement), lower(?)) > 0 OR instr(lower(evidence_text), lower(?)) > 0)"
		args = append(args, query, query)
	}

	pageArgs := append(append([]interface{}{}, args...), limit+1, offset)
	projectionSource := "l1_user_memory_viewer_projection INDEXED BY idx_l1_user_memory_viewer_page"
	if query != "" {
		projectionSource = "l1_user_memory_viewer_projection INDEXED BY idx_l1_user_memory_viewer_search_cover"
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, namespace, user_id, memory_type, memory_state, active, statement, evidence_text,
       confidence, sensitivity, scope, lifecycle_status, decay_score, superseded_by, created_at, updated_at
FROM `+projectionSource+`
WHERE `+where+`
ORDER BY created_at DESC, id DESC
LIMIT ? OFFSET ?
`, pageArgs...)
	if err != nil {
		return nil, false, fmt.Errorf("failed to query user memory page: %w", err)
	}
	defer rows.Close()
	items := make([]domainmemory.UserMemory, 0, limit+1)
	for rows.Next() {
		var item domainmemory.UserMemory
		var active int
		var evidenceJSON string
		if err := rows.Scan(
			&item.ID, &item.Namespace, &item.UserID, &item.Type, &item.State, &active,
			&item.Statement, &evidenceJSON, &item.Confidence, &item.Sensitivity, &item.Scope,
			&item.LifecycleStatus, &item.DecayScore, &item.SupersededBy, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, false, err
		}
		if err := json.Unmarshal([]byte(evidenceJSON), &item.EvidenceEventIDs); err != nil {
			continue
		}
		item.Active = active == 1
		if item.Namespace != NamespaceKindUser+":"+item.UserID || strings.TrimSpace(item.Statement) == "" {
			continue
		}
		if err := domainmemory.ValidateUserMemoryType(item.Type); err != nil {
			continue
		}
		if err := domainmemory.ValidateMemoryState(item.State); err != nil {
			continue
		}
		validEvidence := true
		for _, evidenceID := range item.EvidenceEventIDs {
			if strings.TrimSpace(evidenceID) == "" {
				validEvidence = false
				break
			}
		}
		if !validEvidence {
			continue
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
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
	metaJSON, err := marshalL1MetaJSON(meta, "failed to marshal memory meta")
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE l1_memory_event
SET meta_json = ?, updated_at = ?
WHERE id = ?
`, metaJSON, time.Now().UTC(), id)
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
