package threadmigration

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	// TopicStoreMaxLineBytes bounds one legacy JSONL record before parsing.
	TopicStoreMaxLineBytes = 32 * 1024 * 1024

	TopicStoreQuarantineSchemaVersion  = "rencrow.threadmigration.idlechat_topic_quarantine.v1"
	TopicStoreErrorBlankLine           = "blank_line"
	TopicStoreErrorInvalidUTF8         = "invalid_utf8"
	TopicStoreErrorMalformedJSON       = "malformed_json"
	TopicStoreErrorNonObject           = "non_object"
	TopicStoreErrorMissingSessionID    = "missing_session_id"
	TopicStoreErrorInvalidSessionID    = "invalid_session_id"
	TopicStoreErrorRecordTypePresent   = "record_type_present"
	TopicStoreErrorThreadIdentityFound = "thread_identity_present"
	TopicStoreErrorLineTooLarge        = "line_too_large"
)

var (
	// ErrTopicStoreWrongPhase marks input that is no longer a legacy source.
	// A canonical record must not be turned into a second migration candidate.
	ErrTopicStoreWrongPhase   = errors.New("idlechat topic store input is not a legacy source")
	ErrTopicStoreLineTooLarge = errors.New("idlechat topic store line exceeds the configured limit")
)

// TopicStoreQuarantineRecord is a non-plaintext receipt for one raw-corruption
// input line. RawHash and RawBase64 cover the line payload without its JSONL
// line ending; SourceSHA256 in TopicStorePreparation covers the exact source
// bytes, including line endings.
type TopicStoreQuarantineRecord struct {
	SchemaVersion string `json:"schema_version"`
	Line          int    `json:"line"`
	ErrorCode     string `json:"error_code"`
	RawSHA256     string `json:"raw_sha256"`
	RawBase64     string `json:"raw_base64"`
}

// TopicStorePreparation is the in-memory, apply-ready result of preparing a
// legacy IdleChat topic store. It contains no file handles or runtime state.
type TopicStorePreparation struct {
	SourceCount     int `json:"source_count"`
	OutputCount     int `json:"output_count"`
	QuarantineCount int `json:"quarantine_count"`

	SourceSHA256     string `json:"source_sha256"`
	OutputSHA256     string `json:"output_sha256"`
	QuarantineSHA256 string `json:"quarantine_sha256"`

	OutputJSONL     []byte `json:"-"`
	QuarantineJSONL []byte `json:"-"`
	Plan            Plan   `json:"plan"`
}

// TopicStorePreparationError identifies a deterministic line-level decision
// that blocks the whole preparation. In particular, record_type and any
// preexisting thread identity indicate a wrong phase and are never quarantined.
type TopicStorePreparationError struct {
	Line      int
	ErrorCode string
	Cause     error
}

func (e *TopicStorePreparationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Cause == nil {
		return fmt.Sprintf("topic store line %d: %s", e.Line, e.ErrorCode)
	}
	return fmt.Sprintf("topic store line %d: %s: %v", e.Line, e.ErrorCode, e.Cause)
}

func (e *TopicStorePreparationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type topicStoreLegacyRecord struct {
	lineNo          int
	recordKey       string
	sourceSessionID string
	sessionID       string
	threadSeq       modulecore.ThreadSeq
	fields          map[string]json.RawMessage
}

// PrepareTopicStore converts legacy IdleChat summary JSONL into canonical,
// standalone summary JSONL. It reads only from source and returns all output
// in memory; persistence and activation belong to a later owner operation.
func PrepareTopicStore(source io.Reader) (TopicStorePreparation, error) {
	if source == nil {
		return TopicStorePreparation{}, errors.New("topic store source reader is nil")
	}

	sourceHasher := sha256.New()
	reader := bufio.NewReaderSize(source, 64*1024)
	legacyRecords := make([]topicStoreLegacyRecord, 0, 32)
	facts := make([]LegacyThreadFact, 0, 32)
	quarantine := bytes.NewBuffer(nil)
	quarantineCount := 0
	sessionSequences := make(map[string]int64)
	sourceCount := 0

	for lineNo := 1; ; lineNo++ {
		rawLine, eof, err := readTopicStoreLine(reader, sourceHasher)
		if err != nil {
			return TopicStorePreparation{}, err
		}
		if eof {
			break
		}
		sourceCount++
		payload := topicStoreLinePayload(rawLine)
		if len(payload) > TopicStoreMaxLineBytes {
			return TopicStorePreparation{}, fmt.Errorf("%w: line %d (%s)", ErrTopicStoreLineTooLarge, lineNo, TopicStoreErrorLineTooLarge)
		}

		if !utf8.Valid(payload) {
			appendTopicStoreQuarantine(quarantine, lineNo, TopicStoreErrorInvalidUTF8, payload)
			quarantineCount++
			continue
		}

		fields, phaseErr, errorCode, err := parseTopicStoreLegacyObject(payload, lineNo)
		if phaseErr {
			return TopicStorePreparation{}, err
		}
		if err != nil {
			appendTopicStoreQuarantine(quarantine, lineNo, errorCode, payload)
			quarantineCount++
			continue
		}

		rawSessionID, err := topicStoreSessionID(fields)
		if err != nil {
			errorCode := TopicStoreErrorInvalidSessionID
			if _, present := fields["session_id"]; !present {
				errorCode = TopicStoreErrorMissingSessionID
			}
			appendTopicStoreQuarantine(quarantine, lineNo, errorCode, payload)
			quarantineCount++
			continue
		}
		canonicalSessionID, err := canonicalTopicStoreSessionID(rawSessionID)
		if err != nil {
			return TopicStorePreparation{}, fmt.Errorf("topic store line %d: map session ID: %w", lineNo, err)
		}

		sequence := sessionSequences[canonicalSessionID] + 1
		if sequence <= 0 {
			return TopicStorePreparation{}, fmt.Errorf("topic store line %d: thread sequence overflow", lineNo)
		}
		sessionSequences[canonicalSessionID] = sequence
		recordKey := "topic:" + strconv.Itoa(lineNo)
		legacyRecords = append(legacyRecords, topicStoreLegacyRecord{
			lineNo:          lineNo,
			recordKey:       recordKey,
			sourceSessionID: rawSessionID,
			sessionID:       canonicalSessionID,
			threadSeq:       modulecore.ThreadSeq(sequence),
			fields:          fields,
		})
		facts = append(facts, LegacyThreadFact{
			Surface:        "idlechat_topics",
			RecordKey:      recordKey,
			SessionID:      canonicalSessionID,
			LegacyThreadID: sequence,
			KindHint:       string(modulecore.ThreadKindIdleChat),
		})
	}

	plan, err := BuildPlan(facts)
	if err != nil {
		return TopicStorePreparation{}, fmt.Errorf("build IdleChat topic mapping plan: %w", err)
	}
	if err := attachTopicStoreSourceSessions(&plan, legacyRecords); err != nil {
		return TopicStorePreparation{}, err
	}

	output := bytes.NewBuffer(nil)
	mappingsByTuple := make(map[string]ThreadMapping, len(plan.Generic))
	for _, mapping := range plan.Generic {
		key := GenericSemanticKey(string(mapping.SessionID), int64(mapping.ThreadSeq))
		mappingsByTuple[key] = mapping
	}
	for _, record := range legacyRecords {
		mapping, ok := mappingsByTuple[GenericSemanticKey(record.sessionID, int64(record.threadSeq))]
		if !ok {
			return TopicStorePreparation{}, fmt.Errorf("mapping plan lost source tuple %q/%d", record.sessionID, record.threadSeq)
		}
		fields := make(map[string]json.RawMessage, len(record.fields)+3)
		for key, value := range record.fields {
			fields[key] = append(json.RawMessage(nil), value...)
		}
		fields["session_id"] = topicStoreRawJSON(string(mapping.SessionID))
		fields["thread_id"] = topicStoreRawJSON(string(mapping.ThreadID))
		fields["thread_seq"] = topicStoreRawJSON(int64(mapping.ThreadSeq))
		fields["thread_kind"] = topicStoreRawJSON(string(mapping.ThreadKind))
		encoded, err := json.Marshal(fields)
		if err != nil {
			return TopicStorePreparation{}, fmt.Errorf("marshal IdleChat topic line %d: %w", record.lineNo, err)
		}
		output.Write(encoded)
		output.WriteByte('\n')
	}

	outputBytes := output.Bytes()
	quarantineBytes := quarantine.Bytes()
	return TopicStorePreparation{
		SourceCount:      sourceCount,
		OutputCount:      len(legacyRecords),
		QuarantineCount:  quarantineCount,
		SourceSHA256:     digestBytes(sourceHasher.Sum(nil)),
		OutputSHA256:     digestBytesFromBytes(outputBytes),
		QuarantineSHA256: digestBytesFromBytes(quarantineBytes),
		OutputJSONL:      append([]byte(nil), outputBytes...),
		QuarantineJSONL:  append([]byte(nil), quarantineBytes...),
		Plan:             plan,
	}, nil
}

func attachTopicStoreSourceSessions(plan *Plan, records []topicStoreLegacyRecord) error {
	if plan == nil {
		return errors.New("topic store mapping plan is nil")
	}
	sourceSessions := make(map[string]string, len(records))
	for _, record := range records {
		sourceSessions[record.recordKey] = record.sourceSessionID
	}
	for mappingIndex := range plan.Generic {
		for sourceIndex := range plan.Generic[mappingIndex].Sources {
			source := &plan.Generic[mappingIndex].Sources[sourceIndex]
			sourceSessionID, ok := sourceSessions[source.RecordKey]
			if !ok {
				return fmt.Errorf("mapping plan lost source session for %q", source.RecordKey)
			}
			source.SourceSessionID = sourceSessionID
		}
	}
	hash, err := plan.ComputeMappingSHA256()
	if err != nil {
		return fmt.Errorf("hash IdleChat topic mapping plan: %w", err)
	}
	plan.MappingSHA256 = hash
	plan.buildLookupIndexes()
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("validate IdleChat topic mapping plan: %w", err)
	}
	return nil
}

func readTopicStoreLine(reader *bufio.Reader, sourceHasher io.Writer) ([]byte, bool, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(chunk) > 0 {
			if _, hashErr := sourceHasher.Write(chunk); hashErr != nil {
				return nil, false, fmt.Errorf("hash topic store source: %w", hashErr)
			}
		}
		if len(line)+len(chunk) > TopicStoreMaxLineBytes+2 {
			return nil, false, fmt.Errorf("%w: line exceeds %d bytes", ErrTopicStoreLineTooLarge, TopicStoreMaxLineBytes)
		}
		line = append(line, chunk...)
		switch err {
		case nil:
			return line, false, nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			if len(line) == 0 {
				return nil, true, nil
			}
			return line, false, nil
		default:
			return nil, false, fmt.Errorf("read topic store source: %w", err)
		}
	}
}

func topicStoreLinePayload(rawLine []byte) []byte {
	payload := rawLine
	if len(payload) > 0 && payload[len(payload)-1] == '\n' {
		payload = payload[:len(payload)-1]
		if len(payload) > 0 && payload[len(payload)-1] == '\r' {
			payload = payload[:len(payload)-1]
		}
	}
	return payload
}

func parseTopicStoreLegacyObject(payload []byte, lineNo int) (map[string]json.RawMessage, bool, string, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return nil, false, TopicStoreErrorBlankLine, errors.New("blank line")
	}
	audit, err := AuditJSONIdentity(trimmed)
	if err != nil {
		return nil, false, TopicStoreErrorMalformedJSON, err
	}
	if audit.OccurrenceCount != 0 {
		return nil, true, TopicStoreErrorThreadIdentityFound, &TopicStorePreparationError{
			Line:      lineNo,
			ErrorCode: TopicStoreErrorThreadIdentityFound,
			Cause:     ErrTopicStoreWrongPhase,
		}
	}

	var value any
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return nil, false, TopicStoreErrorMalformedJSON, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, false, TopicStoreErrorNonObject, errors.New("record is not a JSON object")
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return nil, false, TopicStoreErrorMalformedJSON, err
	}
	if _, ok := fields["record_type"]; ok {
		return nil, true, TopicStoreErrorRecordTypePresent, &TopicStorePreparationError{
			Line:      lineNo,
			ErrorCode: TopicStoreErrorRecordTypePresent,
			Cause:     ErrTopicStoreWrongPhase,
		}
	}
	return fields, false, "", nil
}

func topicStoreSessionID(fields map[string]json.RawMessage) (string, error) {
	raw, ok := fields["session_id"]
	if !ok {
		return "", errors.New("session_id is missing")
	}
	var sessionID string
	if err := json.Unmarshal(raw, &sessionID); err != nil {
		return "", err
	}
	if strings.TrimSpace(sessionID) == "" {
		return "", errors.New("session_id is blank")
	}
	return sessionID, nil
}

func canonicalTopicStoreSessionID(raw string) (string, error) {
	candidate := modulecore.SessionID(raw)
	if err := candidate.Validate(); err == nil {
		return raw, nil
	}
	migrated, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "idlechat_topics", "session_id", raw)
	if err != nil {
		return "", err
	}
	canonical := modulecore.SessionID(migrated)
	if err := canonical.Validate(); err != nil {
		return "", err
	}
	return migrated, nil
}

func appendTopicStoreQuarantine(buffer *bytes.Buffer, lineNo int, errorCode string, payload []byte) {
	digest := sha256.Sum256(payload)
	record := TopicStoreQuarantineRecord{
		SchemaVersion: TopicStoreQuarantineSchemaVersion,
		Line:          lineNo,
		ErrorCode:     errorCode,
		RawSHA256:     hex.EncodeToString(digest[:]),
		RawBase64:     base64.StdEncoding.EncodeToString(payload),
	}
	encoded, _ := json.Marshal(record)
	buffer.Write(encoded)
	buffer.WriteByte('\n')
}

func topicStoreRawJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal canonical topic identity: %v", err))
	}
	return encoded
}

func digestBytes(value []byte) string {
	return hex.EncodeToString(value)
}

func digestBytesFromBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
