package turninputmigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
)

const (
	eventEnvelopeTable       = "event_envelope"
	conversationReceiptTable = "conversation_turn_receipt"
	messageReceivedEventType = "message.received"
	agentResponseEventType   = "agent.response"
)

type evidencePlan struct {
	events           map[string][]eventRecord
	receipts         map[string][]conversationReceipt
	eventHash        string
	conversationHash string
}

type eventRecord struct {
	eventID   string
	traceID   string
	eventType string
	jobID     string
	messageID string
	sessionID string
	text      string
	channel   string
	chatID    string
	route     string
	canonical []byte
}

type conversationReceipt struct {
	turnID         string
	traceID        string
	rootTaskID     string
	sessionID      string
	userMessageID  string
	agentMessageID string
	result         canonicalIdentity
	canonical      []byte
}

type canonicalReceiptIdentityDTO struct {
	RootTaskID     string `json:"root_task_id"`
	TurnID         string `json:"turn_id"`
	TraceID        string `json:"trace_id"`
	UserMessageID  string `json:"user_message_id"`
	AgentMessageID string `json:"agent_message_id"`
}

func loadEvidence(ctx context.Context, eventPath, conversationPath string, legacyJobs map[string]legacyReference) (evidencePlan, error) {
	events, eventHash, err := queryEventEvidence(ctx, eventPath, legacyJobs)
	if err != nil {
		return evidencePlan{}, err
	}
	traceToJob := make(map[string]string)
	for jobID, values := range events {
		for _, event := range values {
			if previous, exists := traceToJob[event.traceID]; exists && previous != jobID {
				return evidencePlan{}, blocked("evidence_contradictory", errors.New("one trace maps to multiple legacy jobs"))
			}
			traceToJob[event.traceID] = jobID
		}
	}
	receipts, conversationHash, err := queryConversationEvidence(ctx, conversationPath, traceToJob)
	if err != nil {
		return evidencePlan{}, err
	}
	return evidencePlan{events: events, receipts: receipts, eventHash: eventHash, conversationHash: conversationHash}, nil
}

func openSQLiteReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, blocked("context_canceled", err)
	}
	dsn := (&url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(path),
		RawQuery: "mode=ro&_pragma=busy_timeout%3d5000",
	}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, blocked("database_open_failed", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, blocked("database_open_failed", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		_ = db.Close()
		return nil, blocked("database_open_failed", err)
	}
	var queryOnly int
	if err := db.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil || queryOnly != 1 {
		_ = db.Close()
		return nil, blocked("database_open_failed", errors.New("SQLite query_only was not enabled"))
	}
	return db, nil
}

func queryEventEvidence(ctx context.Context, path string, legacyJobs map[string]legacyReference) (map[string][]eventRecord, string, error) {
	db, err := openSQLiteReadOnly(ctx, path)
	if err != nil {
		return nil, "", err
	}
	defer db.Close()
	if err := requireColumns(ctx, db, eventEnvelopeTable, []string{"event_id", "trace_id", "envelope_json"}); err != nil {
		return nil, "", blocked("event_schema_invalid", err)
	}
	rows, err := db.QueryContext(ctx, "SELECT event_id, trace_id, envelope_json FROM event_envelope ORDER BY rowid ASC")
	if err != nil {
		return nil, "", blocked("event_query_failed", err)
	}
	defer rows.Close()
	events := make(map[string][]eventRecord)
	records := make([]string, 0)
	for rows.Next() {
		var eventIDValue, traceValue, envelopeValue any
		if err := rows.Scan(&eventIDValue, &traceValue, &envelopeValue); err != nil {
			return nil, "", blocked("event_query_failed", err)
		}
		eventID, err := sqlString(eventIDValue)
		if err != nil {
			return nil, "", blocked("event_schema_invalid", err)
		}
		rowTrace, err := sqlString(traceValue)
		if err != nil {
			return nil, "", blocked("event_schema_invalid", err)
		}
		envelope, err := sqlBytes(envelopeValue)
		if err != nil {
			return nil, "", blocked("event_schema_invalid", err)
		}
		event, relevant, parseErr := parseEventEnvelope(eventID, rowTrace, envelope, legacyJobs)
		if parseErr != nil {
			return nil, "", blocked("event_evidence_invalid", parseErr)
		}
		if !relevant {
			continue
		}
		events[event.jobID] = append(events[event.jobID], event)
		records = append(records, string(event.canonical))
	}
	if err := rows.Err(); err != nil {
		return nil, "", blocked("event_query_failed", err)
	}
	return events, writeHashStrings(records), nil
}

func parseEventEnvelope(eventID, rowTrace string, raw []byte, legacyJobs map[string]legacyReference) (eventRecord, bool, error) {
	object, err := strictObject(raw)
	if err != nil {
		return eventRecord{}, false, err
	}
	payload := map[string]json.RawMessage{}
	if rawPayload, ok := object["payload"]; ok {
		payload, err = strictObject(rawPayload)
		if err != nil {
			return eventRecord{}, false, errors.New("payload must be an object")
		}
	}
	eventType, err := eventField(object, payload, "event_type", "type")
	if err != nil {
		return eventRecord{}, false, err
	}
	if eventType != messageReceivedEventType && eventType != agentResponseEventType {
		return eventRecord{}, false, nil
	}
	jobID, err := payloadField(payload, "job_id")
	if err != nil {
		return eventRecord{}, false, err
	}
	if jobID == "" {
		return eventRecord{}, false, nil
	}
	if _, relevant := legacyJobs[jobID]; !relevant {
		return eventRecord{}, false, nil
	}
	if eventID == "" {
		return eventRecord{}, false, errors.New("relevant event ID is missing")
	}
	traceFromEnvelope, err := eventField(object, payload, "trace_id")
	if err != nil {
		return eventRecord{}, false, err
	}
	if rowTrace != "" && traceFromEnvelope != "" && rowTrace != traceFromEnvelope {
		return eventRecord{}, false, errors.New("event trace IDs disagree")
	}
	traceID := rowTrace
	if traceID == "" {
		traceID = traceFromEnvelope
	}
	if traceID == "" {
		return eventRecord{}, false, errors.New("relevant event trace ID is missing")
	}
	messageID, err := eventField(object, payload, "message_id", "user_message_id", "agent_message_id")
	if err != nil {
		return eventRecord{}, false, err
	}
	sessionID, err := eventField(object, payload, "session_id")
	if err != nil {
		return eventRecord{}, false, err
	}
	textValue, err := eventField(object, payload, "message_text", "user_message", "text", "message")
	if err != nil {
		return eventRecord{}, false, err
	}
	channel, err := eventField(object, payload, "channel", "channel_type")
	if err != nil {
		return eventRecord{}, false, err
	}
	chatID, err := eventField(object, payload, "chat_id", "external_conversation_id")
	if err != nil {
		return eventRecord{}, false, err
	}
	route, err := eventField(object, payload, "route")
	if err != nil {
		return eventRecord{}, false, err
	}
	event := eventRecord{
		eventID: eventID, traceID: traceID, eventType: eventType, jobID: jobID,
		messageID: messageID, sessionID: sessionID, text: textValue, channel: channel,
		chatID: chatID, route: route,
	}
	canonicalEnvelope, err := json.Marshal(object)
	if err != nil {
		return eventRecord{}, false, err
	}
	canonical, err := json.Marshal(struct {
		EventID   string          `json:"event_id"`
		TraceID   string          `json:"trace_id"`
		EventType string          `json:"event_type"`
		JobID     string          `json:"job_id"`
		MessageID string          `json:"message_id"`
		SessionID string          `json:"session_id"`
		Text      string          `json:"text"`
		Channel   string          `json:"channel"`
		ChatID    string          `json:"chat_id"`
		Route     string          `json:"route"`
		Envelope  json.RawMessage `json:"envelope"`
	}{event.eventID, event.traceID, event.eventType, event.jobID, event.messageID, event.sessionID, event.text, event.channel, event.chatID, event.route, canonicalEnvelope})
	if err != nil {
		return eventRecord{}, false, err
	}
	event.canonical = canonical
	return event, true, nil
}

func eventField(object, payload map[string]json.RawMessage, keys ...string) (string, error) {
	var chosen string
	for _, key := range keys {
		payloadValue, payloadOK := payload[key]
		objectValue, objectOK := object[key]
		payloadString, payloadErr := rawOptionalString(payloadValue, payloadOK)
		objectString, objectErr := rawOptionalString(objectValue, objectOK)
		if payloadErr != nil || objectErr != nil {
			return "", errors.New("event field must be a string")
		}
		if payloadString != "" && objectString != "" && payloadString != objectString {
			return "", errors.New("event top-level and payload fields disagree")
		}
		value := payloadString
		if value == "" {
			value = objectString
		}
		if value != "" {
			if chosen != "" && chosen != value {
				return "", errors.New("event aliases disagree")
			}
			chosen = value
		}
	}
	return chosen, nil
}

func payloadField(payload map[string]json.RawMessage, key string) (string, error) {
	raw, ok := payload[key]
	if !ok {
		return "", nil
	}
	return decodeString(raw)
}

func rawOptionalString(raw json.RawMessage, present bool) (string, error) {
	if !present {
		return "", nil
	}
	return decodeString(raw)
}

func queryConversationEvidence(ctx context.Context, path string, traceToJob map[string]string) (map[string][]conversationReceipt, string, error) {
	db, err := openSQLiteReadOnly(ctx, path)
	if err != nil {
		return nil, "", err
	}
	defer db.Close()
	if err := requireColumns(ctx, db, conversationReceiptTable, []string{"turn_id", "trace_id", "root_task_id", "session_id", "user_message_id", "agent_message_id", "result_json"}); err != nil {
		return nil, "", blocked("conversation_schema_invalid", err)
	}
	rows, err := db.QueryContext(ctx, "SELECT turn_id, trace_id, root_task_id, session_id, user_message_id, agent_message_id, result_json FROM conversation_turn_receipt ORDER BY rowid ASC")
	if err != nil {
		return nil, "", blocked("conversation_query_failed", err)
	}
	defer rows.Close()
	receipts := make(map[string][]conversationReceipt)
	records := make([]string, 0)
	for rows.Next() {
		values := make([]any, 7)
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, "", blocked("conversation_query_failed", err)
		}
		turnID, err := sqlString(values[0])
		if err != nil {
			return nil, "", blocked("conversation_schema_invalid", err)
		}
		traceID, err := sqlString(values[1])
		if err != nil {
			return nil, "", blocked("conversation_schema_invalid", err)
		}
		jobID, relevant := traceToJob[traceID]
		if !relevant {
			continue
		}
		rootTaskID, err := sqlString(values[2])
		if err != nil {
			return nil, "", blocked("conversation_schema_invalid", err)
		}
		sessionID, err := sqlString(values[3])
		if err != nil {
			return nil, "", blocked("conversation_schema_invalid", err)
		}
		userMessageID, err := sqlString(values[4])
		if err != nil {
			return nil, "", blocked("conversation_schema_invalid", err)
		}
		agentMessageID, err := sqlString(values[5])
		if err != nil {
			return nil, "", blocked("conversation_schema_invalid", err)
		}
		resultJSON, err := sqlBytes(values[6])
		if err != nil {
			return nil, "", blocked("conversation_schema_invalid", err)
		}
		result, err := parseConversationResult(resultJSON, turnID, traceID, rootTaskID, sessionID, userMessageID, agentMessageID)
		if err != nil {
			return nil, "", blocked("conversation_evidence_invalid", err)
		}
		receipt := conversationReceipt{
			turnID: turnID, traceID: traceID, rootTaskID: rootTaskID, sessionID: sessionID,
			userMessageID: userMessageID, agentMessageID: agentMessageID, result: result,
		}
		canonical, err := json.Marshal(struct {
			TurnID         string                      `json:"turn_id"`
			TraceID        string                      `json:"trace_id"`
			RootTaskID     string                      `json:"root_task_id"`
			SessionID      string                      `json:"session_id"`
			UserMessageID  string                      `json:"user_message_id"`
			AgentMessageID string                      `json:"agent_message_id"`
			Result         canonicalReceiptIdentityDTO `json:"result"`
		}{turnID, traceID, rootTaskID, sessionID, userMessageID, agentMessageID, canonicalReceiptIdentityDTO{
			RootTaskID: string(result.rootTaskID), TurnID: string(result.turnID), TraceID: string(result.traceID),
			UserMessageID: string(result.userMessageID), AgentMessageID: string(result.agentMessageID),
		}})
		if err != nil {
			return nil, "", blocked("conversation_evidence_invalid", err)
		}
		receipt.canonical = canonical
		receipts[jobID] = append(receipts[jobID], receipt)
		records = append(records, string(canonical))
	}
	if err := rows.Err(); err != nil {
		return nil, "", blocked("conversation_query_failed", err)
	}
	return receipts, writeHashStrings(records), nil
}

func parseConversationResult(raw []byte, turnID, traceID, rootTaskID, sessionID, userMessageID, agentMessageID string) (canonicalIdentity, error) {
	fields, err := strictObject(raw)
	if err != nil {
		return canonicalIdentity{}, err
	}
	allowed := []string{"root_task_id", "turn_id", "trace_id", "user_message_id", "agent_message_id", "session_id"}
	if !exactKeys(fields, allowed, nil) {
		return canonicalIdentity{}, errors.New("conversation receipt result schema is not exact")
	}
	values := make(map[string]string, len(allowed))
	for _, key := range allowed {
		value, err := decodeString(fields[key])
		if err != nil {
			return canonicalIdentity{}, errors.New("conversation receipt result contains non-string IDs")
		}
		values[key] = value
	}
	if values["turn_id"] != turnID || values["trace_id"] != traceID || values["root_task_id"] != rootTaskID || values["session_id"] != sessionID || values["user_message_id"] != userMessageID || values["agent_message_id"] != agentMessageID {
		return canonicalIdentity{}, errors.New("conversation receipt columns and result disagree")
	}
	identity := canonicalIdentity{
		rootTaskID: modulecore.TaskID(values["root_task_id"]), turnID: modulecore.TurnID(values["turn_id"]),
		traceID: modulecore.TraceID(values["trace_id"]), userMessageID: modulecore.MessageID(values["user_message_id"]),
		agentMessageID: modulecore.MessageID(values["agent_message_id"]),
	}
	if err := identity.validate(); err != nil {
		return canonicalIdentity{}, errors.New("conversation receipt IDs are invalid")
	}
	if err := modulecore.SessionID(sessionID).Validate(); err != nil {
		return canonicalIdentity{}, errors.New("conversation receipt SessionID is invalid")
	}
	return identity, nil
}

func mapLegacyRow(reference legacyReference, evidence evidencePlan) (rowMapping, error) {
	events := evidence.events[reference.row.jobID]
	traces := make(map[string]struct{})
	for _, event := range events {
		traces[event.traceID] = struct{}{}
	}
	if len(traces) > 1 {
		return rowMapping{}, blocked("ambiguous_evidence", errors.New("legacy job maps to multiple traces"))
	}
	if len(traces) == 0 {
		return deterministicMapping(reference.row.jobID)
	}
	var traceID string
	for value := range traces {
		traceID = value
	}
	receipts := evidence.receipts[reference.row.jobID]
	if len(receipts) == 0 {
		return deterministicMapping(reference.row.jobID)
	}
	if len(receipts) != 1 {
		return rowMapping{}, blocked("contradictory_evidence", errors.New("legacy job has duplicate conversation receipts"))
	}
	receipt := receipts[0]
	if receipt.traceID != traceID || receipt.sessionID != reference.session.id {
		return rowMapping{}, blocked("contradictory_evidence", errors.New("conversation receipt identity does not match legacy row"))
	}
	userMatches := 0
	agentMatches := 0
	for _, event := range events {
		if event.traceID != traceID {
			continue
		}
		switch event.eventType {
		case messageReceivedEventType:
			if event.messageID != receipt.userMessageID {
				continue
			}
			userMatches++
			if event.text != reference.row.userMessage || event.channel != reference.row.channel || event.chatID != reference.row.chatID || (event.sessionID != "" && event.sessionID != reference.session.id) {
				return rowMapping{}, blocked("contradictory_evidence", errors.New("message.received metadata does not match legacy row"))
			}
		case agentResponseEventType:
			if event.messageID != receipt.agentMessageID {
				continue
			}
			agentMatches++
			if event.channel != reference.row.channel || event.chatID != reference.row.chatID || event.route != reference.row.route || (event.sessionID != "" && event.sessionID != reference.session.id) {
				return rowMapping{}, blocked("contradictory_evidence", errors.New("agent.response metadata does not match legacy row"))
			}
		}
	}
	if userMatches != 1 || agentMatches != 1 {
		return rowMapping{}, blocked("contradictory_evidence", errors.New("conversation receipt lacks exact message evidence"))
	}
	if err := receipt.result.validate(); err != nil {
		return rowMapping{}, blocked("contradictory_evidence", err)
	}
	return rowMapping{identity: receipt.result, linked: true}, nil
}

func deterministicMapping(jobID string) (rowMapping, error) {
	identity, err := migrationIdentity(jobID)
	if err != nil {
		return rowMapping{}, err
	}
	return rowMapping{identity: identity}, nil
}

func migrationIdentity(jobID string) (canonicalIdentity, error) {
	rootTaskID, err := modulecore.NewMigrationID(modulecore.CanonicalTaskID, "session_history", "job_id", jobID)
	if err != nil {
		return canonicalIdentity{}, blocked("mapping_failed", err)
	}
	turnID, err := modulecore.NewMigrationID(modulecore.CanonicalTurnID, "session_history", "job_id", jobID)
	if err != nil {
		return canonicalIdentity{}, blocked("mapping_failed", err)
	}
	traceID, err := modulecore.NewMigrationID(modulecore.CanonicalTraceID, "session_history", "job_id", jobID)
	if err != nil {
		return canonicalIdentity{}, blocked("mapping_failed", err)
	}
	userMessageID, err := modulecore.NewMigrationID(modulecore.CanonicalMessageID, "session_history", "user_message", jobID)
	if err != nil {
		return canonicalIdentity{}, blocked("mapping_failed", err)
	}
	agentMessageID, err := modulecore.NewMigrationID(modulecore.CanonicalMessageID, "session_history", "agent_message", jobID)
	if err != nil {
		return canonicalIdentity{}, blocked("mapping_failed", err)
	}
	identity := canonicalIdentity{
		rootTaskID: modulecore.TaskID(rootTaskID), turnID: modulecore.TurnID(turnID), traceID: modulecore.TraceID(traceID),
		userMessageID: modulecore.MessageID(userMessageID), agentMessageID: modulecore.MessageID(agentMessageID),
	}
	if err := identity.validate(); err != nil {
		return canonicalIdentity{}, blocked("mapping_failed", err)
	}
	return identity, nil
}

func requireColumns(ctx context.Context, db *sql.DB, table string, required []string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()
	columns := make(map[string]struct{})
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		columns[strings.ToLower(name)] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(columns) == 0 {
		return errors.New("required SQLite table is missing")
	}
	for _, name := range required {
		if _, ok := columns[name]; !ok {
			return fmt.Errorf("required SQLite column is missing")
		}
	}
	return nil
}

func sqlString(value any) (string, error) {
	switch value := value.(type) {
	case string:
		return value, nil
	case []byte:
		return string(value), nil
	default:
		return "", errors.New("SQLite value is not text")
	}
}

func sqlBytes(value any) ([]byte, error) {
	switch value := value.(type) {
	case string:
		return []byte(value), nil
	case []byte:
		return append([]byte(nil), value...), nil
	default:
		return nil, errors.New("SQLite JSON value is not text")
	}
}

func sortStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
