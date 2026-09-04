package threadmigration

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestPrepareTopicStoreBuildsCanonicalSummariesInSourceOrder(t *testing.T) {
	input := []byte(strings.Join([]string{
		`{"session_id":"legacy-a","title":"first","nested":{"n":1},"nullable":null}`,
		`{"session_id":"legacy-a","title":"second","array":[true,2,"x"]}`,
		`{"session_id":"legacy-b","title":"other","extra":"v"}`,
	}, "\n") + "\n")

	prepared, err := PrepareTopicStore(bytes.NewReader(input))
	if err != nil {
		t.Fatalf("PrepareTopicStore() error = %v", err)
	}
	if prepared.SourceCount != 3 || prepared.OutputCount != 3 || prepared.QuarantineCount != 0 {
		t.Fatalf("counts = source %d output %d quarantine %d", prepared.SourceCount, prepared.OutputCount, prepared.QuarantineCount)
	}
	if prepared.SourceSHA256 != topicSHA256Hex(input) {
		t.Fatalf("source SHA256 = %q, want %q", prepared.SourceSHA256, topicSHA256Hex(input))
	}
	if prepared.OutputSHA256 != sha256Hex(prepared.OutputJSONL) || prepared.QuarantineSHA256 != sha256Hex(prepared.QuarantineJSONL) {
		t.Fatalf("returned hashes do not bind returned bytes: %+v", prepared)
	}
	if len(prepared.QuarantineJSONL) != 0 || prepared.QuarantineSHA256 != topicSHA256Hex(nil) {
		t.Fatalf("empty quarantine = %q / %q", prepared.QuarantineJSONL, prepared.QuarantineSHA256)
	}
	if err := prepared.Plan.Validate(); err != nil {
		t.Fatalf("mapping plan Validate() error = %v", err)
	}
	if len(prepared.Plan.Generic) != 3 || len(prepared.Plan.ChatGPT) != 0 {
		t.Fatalf("mapping plan sizes = generic %d ChatGPT %d", len(prepared.Plan.Generic), len(prepared.Plan.ChatGPT))
	}

	records := decodeTopicJSONL(t, prepared.OutputJSONL)
	if len(records) != 3 {
		t.Fatalf("output records = %d, want 3", len(records))
	}
	wantSessionA := migrationSessionID(t, "legacy-a")
	wantSessionB := migrationSessionID(t, "legacy-b")
	wantFields := []map[string]any{
		{"session_id": wantSessionA, "title": "first", "nested": map[string]any{"n": float64(1)}, "nullable": nil},
		{"session_id": wantSessionA, "title": "second", "array": []any{true, float64(2), "x"}},
		{"session_id": wantSessionB, "title": "other", "extra": "v"},
	}
	wantSequences := []modulecore.ThreadSeq{1, 2, 1}
	for index, record := range records {
		var sessionID string
		var threadID modulecore.ThreadID
		var threadSeq modulecore.ThreadSeq
		var threadKind modulecore.ThreadKind
		if err := json.Unmarshal(record["session_id"], &sessionID); err != nil {
			t.Fatalf("output line %d session_id: %v", index+1, err)
		}
		if err := json.Unmarshal(record["thread_id"], &threadID); err != nil {
			t.Fatalf("output line %d thread_id: %v", index+1, err)
		}
		if err := json.Unmarshal(record["thread_seq"], &threadSeq); err != nil {
			t.Fatalf("output line %d thread_seq: %v", index+1, err)
		}
		if err := json.Unmarshal(record["thread_kind"], &threadKind); err != nil {
			t.Fatalf("output line %d thread_kind: %v", index+1, err)
		}
		if sessionID != wantFields[index]["session_id"] || threadSeq != wantSequences[index] || threadKind != modulecore.ThreadKindIdleChat {
			t.Fatalf("output line %d identity = %s/%d/%s", index+1, sessionID, threadSeq, threadKind)
		}
		if err := threadID.Validate(); err != nil {
			t.Fatalf("output line %d thread ID invalid: %v", index+1, err)
		}
		for key, want := range wantFields[index] {
			var got any
			if err := json.Unmarshal(record[key], &got); err != nil {
				t.Fatalf("output line %d field %q: %v", index+1, key, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("output line %d field %q = %#v, want %#v", index+1, key, got, want)
			}
		}
		mapping, ok := prepared.Plan.LookupGeneric(sessionID, int64(threadSeq))
		if !ok || mapping.SessionID != modulecore.SessionID(sessionID) || mapping.ThreadID != threadID || mapping.ThreadSeq != threadSeq || mapping.ThreadKind != threadKind {
			t.Fatalf("output line %d mapping = %+v, found=%v", index+1, mapping, ok)
		}
		if len(mapping.Sources) != 1 || mapping.Sources[0].Surface != "idlechat_topics" || mapping.Sources[0].RecordKey != fmt.Sprintf("topic:%d", index+1) || mapping.Sources[0].SourceSessionID != []string{"legacy-a", "legacy-a", "legacy-b"}[index] {
			t.Fatalf("output line %d source mapping = %+v", index+1, mapping.Sources)
		}
	}

	wantThreadID, err := modulecore.NewMigrationID(modulecore.CanonicalThreadID, genericSourceTable, genericSourceField, GenericSemanticKey(wantSessionA, 1))
	if err != nil {
		t.Fatal(err)
	}
	if got := mustThreadID(t, records[0], "thread_id"); got != modulecore.ThreadID(wantThreadID) {
		t.Fatalf("first thread ID = %q, want BuildPlan mapping %q", got, wantThreadID)
	}
}

func TestPrepareTopicStoreUsesExactSessionValueAndRetainsCanonicalSession(t *testing.T) {
	canonical := migrationSessionID(t, "already-canonical")
	padded := " legacy session "
	input := []byte(strings.Join([]string{
		fmt.Sprintf(`{"session_id":%q,"title":"canonical"}`, canonical),
		fmt.Sprintf(`{"session_id":%q,"title":"padded"}`, padded),
	}, "\n") + "\n")
	prepared, err := PrepareTopicStore(bytes.NewReader(input))
	if err != nil {
		t.Fatalf("PrepareTopicStore() error = %v", err)
	}
	records := decodeTopicJSONL(t, prepared.OutputJSONL)
	if got := mustString(t, records[0], "session_id"); got != canonical {
		t.Fatalf("canonical session = %q, want unchanged %q", got, canonical)
	}
	wantPadded, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "idlechat_topics", "session_id", padded)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustString(t, records[1], "session_id"); got != wantPadded {
		t.Fatalf("padded session = %q, want exact-value mapping %q", got, wantPadded)
	}
	if got := mustString(t, records[1], "session_id"); got == migrationSessionID(t, strings.TrimSpace(padded)) {
		t.Fatal("padded session was trimmed before migration")
	}
	if len(prepared.Plan.Generic) != 2 {
		t.Fatalf("mapping plan generic count = %d, want 2", len(prepared.Plan.Generic))
	}
	seenSourceSessions := map[string]bool{}
	for _, mapping := range prepared.Plan.Generic {
		for _, source := range mapping.Sources {
			seenSourceSessions[source.SourceSessionID] = true
		}
	}
	if !seenSourceSessions[canonical] || !seenSourceSessions[padded] {
		t.Fatalf("mapping plan did not retain exact source sessions: %+v", prepared.Plan.Generic)
	}

	compactInput := []byte(`{"session_id":"legacy","title":"same"}` + "\n")
	paddedInput := []byte(" \t" + string(bytes.TrimSuffix(compactInput, []byte("\n"))) + "  \r\n")
	compact, err := PrepareTopicStore(bytes.NewReader(compactInput))
	if err != nil {
		t.Fatalf("compact PrepareTopicStore() error = %v", err)
	}
	paddedResult, err := PrepareTopicStore(bytes.NewReader(paddedInput))
	if err != nil {
		t.Fatalf("padded PrepareTopicStore() error = %v", err)
	}
	if compact.SourceSHA256 == paddedResult.SourceSHA256 {
		t.Fatal("padded source bytes collapsed to the compact source hash")
	}
	if !bytes.Equal(compact.OutputJSONL, paddedResult.OutputJSONL) {
		t.Fatalf("semantically equal padded source changed canonical output:\ncompact=%s\npadded=%s", compact.OutputJSONL, paddedResult.OutputJSONL)
	}
}

func TestPrepareTopicStoreQuarantinesRawCorruptionWithoutPlaintext(t *testing.T) {
	invalidUTF8 := []byte{0xff, '{', '}', '\n'}
	malformed := []byte(`{"session_id":` + "\n")
	blank := []byte(" \t\n")
	nonObject := []byte("[]\n")
	missingSession := []byte(`{"title":"missing"}` + "\n")
	invalidSession := []byte(`{"session_id":123}` + "\n")
	duplicateKey := []byte(`{"session_id":"first","session_id":"second"}` + "\n")
	valid := []byte(`{"session_id":"valid","title":"kept"}` + "\n")
	input := bytes.Join([][]byte{invalidUTF8, malformed, blank, nonObject, missingSession, invalidSession, duplicateKey, valid}, nil)

	prepared, err := PrepareTopicStore(bytes.NewReader(input))
	if err != nil {
		t.Fatalf("PrepareTopicStore() error = %v", err)
	}
	if prepared.SourceCount != 8 || prepared.OutputCount != 1 || prepared.QuarantineCount != 7 {
		t.Fatalf("counts = source %d output %d quarantine %d", prepared.SourceCount, prepared.OutputCount, prepared.QuarantineCount)
	}
	quarantine := decodeTopicQuarantineJSONL(t, prepared.QuarantineJSONL)
	wantPayloads := [][]byte{
		bytes.TrimSuffix(invalidUTF8, []byte("\n")),
		bytes.TrimSuffix(malformed, []byte("\n")),
		bytes.TrimSuffix(blank, []byte("\n")),
		bytes.TrimSuffix(nonObject, []byte("\n")),
		bytes.TrimSuffix(missingSession, []byte("\n")),
		bytes.TrimSuffix(invalidSession, []byte("\n")),
		bytes.TrimSuffix(duplicateKey, []byte("\n")),
	}
	wantCodes := []string{
		TopicStoreErrorInvalidUTF8,
		TopicStoreErrorMalformedJSON,
		TopicStoreErrorBlankLine,
		TopicStoreErrorNonObject,
		TopicStoreErrorMissingSessionID,
		TopicStoreErrorInvalidSessionID,
		TopicStoreErrorMalformedJSON,
	}
	if len(quarantine) != len(wantPayloads) {
		t.Fatalf("quarantine records = %d, want %d", len(quarantine), len(wantPayloads))
	}
	for index, record := range quarantine {
		if record.SchemaVersion != TopicStoreQuarantineSchemaVersion || record.Line != index+1 || record.ErrorCode != wantCodes[index] {
			t.Fatalf("quarantine[%d] = %+v", index, record)
		}
		if got := record.RawSHA256; got != topicSHA256Hex(wantPayloads[index]) {
			t.Fatalf("quarantine[%d] hash = %q, want %q", index, got, topicSHA256Hex(wantPayloads[index]))
		}
		decoded, err := base64.StdEncoding.DecodeString(record.RawBase64)
		if err != nil {
			t.Fatalf("quarantine[%d] base64: %v", index, err)
		}
		if !bytes.Equal(decoded, wantPayloads[index]) {
			t.Fatalf("quarantine[%d] raw bytes = %x, want %x", index, decoded, wantPayloads[index])
		}
		var object map[string]json.RawMessage
		line := bytes.Split(bytes.TrimSpace(prepared.QuarantineJSONL), []byte("\n"))[index]
		if err := json.Unmarshal(line, &object); err != nil {
			t.Fatalf("quarantine[%d] object: %v", index, err)
		}
		if _, exists := object["raw"]; exists {
			t.Fatalf("quarantine[%d] contains plaintext raw field", index)
		}
	}
	if got := mustString(t, decodeTopicJSONL(t, prepared.OutputJSONL)[0], "title"); got != "kept" {
		t.Fatalf("valid line was not retained: %q", got)
	}
	if prepared.SourceSHA256 != topicSHA256Hex(input) || prepared.QuarantineSHA256 != topicSHA256Hex(prepared.QuarantineJSONL) {
		t.Fatalf("hashes do not bind corruption input/output: %+v", prepared)
	}

	rerun, err := PrepareTopicStore(bytes.NewReader(input))
	if err != nil {
		t.Fatalf("rerun PrepareTopicStore() error = %v", err)
	}
	if !bytes.Equal(prepared.OutputJSONL, rerun.OutputJSONL) || !bytes.Equal(prepared.QuarantineJSONL, rerun.QuarantineJSONL) || prepared.SourceSHA256 != rerun.SourceSHA256 || prepared.OutputSHA256 != rerun.OutputSHA256 || prepared.QuarantineSHA256 != rerun.QuarantineSHA256 || !reflect.DeepEqual(prepared.Plan, rerun.Plan) {
		t.Fatal("same source bytes produced a non-deterministic preparation")
	}
}

func TestPrepareTopicStoreRejectsWrongPhaseInput(t *testing.T) {
	legacy := []byte(`{"session_id":"legacy","title":"one"}` + "\n")
	prepared, err := PrepareTopicStore(bytes.NewReader(legacy))
	if err != nil {
		t.Fatalf("legacy PrepareTopicStore() error = %v", err)
	}
	cases := []struct {
		name string
		data []byte
		code string
	}{
		{name: "canonical rerun", data: prepared.OutputJSONL, code: TopicStoreErrorThreadIdentityFound},
		{name: "record type", data: []byte(`{"session_id":"legacy","record_type":"summary"}` + "\n"), code: TopicStoreErrorRecordTypePresent},
		{name: "partial identity", data: []byte(`{"session_id":"legacy","thread_seq":1}` + "\n"), code: TopicStoreErrorThreadIdentityFound},
		{name: "nested identity", data: []byte(`{"session_id":"legacy","meta":{"thread_id":7}}` + "\n"), code: TopicStoreErrorThreadIdentityFound},
		{name: "retired discussion identity", data: []byte(`{"session_id":"legacy","discussion_id":7}` + "\n"), code: TopicStoreErrorThreadIdentityFound},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := PrepareTopicStore(bytes.NewReader(testCase.data))
			if err == nil {
				t.Fatal("PrepareTopicStore() unexpectedly accepted wrong-phase input")
			}
			if !errors.Is(err, ErrTopicStoreWrongPhase) {
				t.Fatalf("error = %v, want ErrTopicStoreWrongPhase", err)
			}
			var phaseErr *TopicStorePreparationError
			if !errors.As(err, &phaseErr) || phaseErr.Line != 1 || phaseErr.ErrorCode != testCase.code {
				t.Fatalf("phase error = %+v, want line 1 code %q", phaseErr, testCase.code)
			}
		})
	}
}

func TestPrepareTopicStoreSequenceFollowsSourceOrderAndRerunsByteIdentically(t *testing.T) {
	firstInput := []byte(strings.Join([]string{
		`{"session_id":"same","title":"first"}`,
		`{"session_id":"same","title":"second"}`,
	}, "\n") + "\n")
	permutedInput := []byte(strings.Join([]string{
		`{"session_id":"same","title":"second"}`,
		`{"session_id":"same","title":"first"}`,
	}, "\n") + "\n")
	first, err := PrepareTopicStore(bytes.NewReader(firstInput))
	if err != nil {
		t.Fatalf("first PrepareTopicStore() error = %v", err)
	}
	rerun, err := PrepareTopicStore(bytes.NewReader(firstInput))
	if err != nil {
		t.Fatalf("rerun PrepareTopicStore() error = %v", err)
	}
	if !bytes.Equal(first.OutputJSONL, rerun.OutputJSONL) || !bytes.Equal(first.QuarantineJSONL, rerun.QuarantineJSONL) || !reflect.DeepEqual(first.Plan, rerun.Plan) {
		t.Fatal("same source bytes changed output or plan")
	}
	permuted, err := PrepareTopicStore(bytes.NewReader(permutedInput))
	if err != nil {
		t.Fatalf("permuted PrepareTopicStore() error = %v", err)
	}
	if bytes.Equal(first.OutputJSONL, permuted.OutputJSONL) {
		t.Fatal("permuting source records unexpectedly preserved sequence-bound output bytes")
	}
	firstRecords := decodeTopicJSONL(t, first.OutputJSONL)
	permutedRecords := decodeTopicJSONL(t, permuted.OutputJSONL)
	if mustInt64(t, firstRecords[0], "thread_seq") != 1 || mustInt64(t, firstRecords[1], "thread_seq") != 2 || mustInt64(t, permutedRecords[0], "thread_seq") != 1 || mustInt64(t, permutedRecords[1], "thread_seq") != 2 {
		t.Fatalf("source-order sequences not 1/2: first=%s permuted=%s", first.OutputJSONL, permuted.OutputJSONL)
	}
	if mustString(t, firstRecords[0], "title") != "first" || mustString(t, permutedRecords[0], "title") != "second" {
		t.Fatalf("source order not preserved: first=%s permuted=%s", first.OutputJSONL, permuted.OutputJSONL)
	}
}

func migrationSessionID(t *testing.T, raw string) string {
	t.Helper()
	id, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "idlechat_topics", "session_id", raw)
	if err != nil {
		t.Fatalf("migration session ID: %v", err)
	}
	return id
}

func topicSHA256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func decodeTopicJSONL(t *testing.T, data []byte) []map[string]json.RawMessage {
	t.Helper()
	lines := bytes.Split(data, []byte("\n"))
	records := make([]map[string]json.RawMessage, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var record map[string]json.RawMessage
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode JSONL line %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func decodeTopicQuarantineJSONL(t *testing.T, data []byte) []TopicStoreQuarantineRecord {
	t.Helper()
	lines := bytes.Split(data, []byte("\n"))
	records := make([]TopicStoreQuarantineRecord, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var record TopicStoreQuarantineRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode quarantine JSONL line %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func mustString(t *testing.T, record map[string]json.RawMessage, key string) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(record[key], &value); err != nil {
		t.Fatalf("field %q as string: %v", key, err)
	}
	return value
}

func mustInt64(t *testing.T, record map[string]json.RawMessage, key string) int64 {
	t.Helper()
	var value int64
	if err := json.Unmarshal(record[key], &value); err != nil {
		t.Fatalf("field %q as int64: %v", key, err)
	}
	return value
}

func mustThreadID(t *testing.T, record map[string]json.RawMessage, key string) modulecore.ThreadID {
	t.Helper()
	var value modulecore.ThreadID
	if err := json.Unmarshal(record[key], &value); err != nil {
		t.Fatalf("field %q as ThreadID: %v", key, err)
	}
	return value
}
