package threadmigration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestAuditJSONIdentityReportsNestedPointersAndEscapes(t *testing.T) {
	canonicalThreadID := jsonIdentityTestThreadID(t, "nested")
	raw := []byte(`{"z":{"ignored":"payload","thread_id":999},"a/b~c":[{"closed_thread_id":"12","thread_kind":"idlechat","thread_seq":3},{"discussion_id":"14"}],"thread_id":"` + canonicalThreadID + `"}`)

	receipt, err := AuditJSONIdentity(raw)
	if err != nil {
		t.Fatalf("AuditJSONIdentity() error = %v", err)
	}
	if receipt.OccurrenceCount != 6 || len(receipt.Occurrences) != 6 {
		t.Fatalf("occurrence count = %d/%d, want 6", receipt.OccurrenceCount, len(receipt.Occurrences))
	}
	inputDigest := sha256.Sum256(raw)
	if receipt.InputSHA256 != hex.EncodeToString(inputDigest[:]) {
		t.Fatalf("input SHA256 = %q, want %x", receipt.InputSHA256, inputDigest)
	}
	want := []JSONIdentityOccurrence{
		{Pointer: "/a~1b~0c/0/closed_thread_id", Key: JSONIdentityKeyClosedThreadID, ValueKind: JSONIdentityValueString, Classification: JSONIdentityClassificationLegacyNumeric, StringValue: "12"},
		{Pointer: "/a~1b~0c/0/thread_kind", Key: JSONIdentityKeyThreadKind, ValueKind: JSONIdentityValueString, Classification: JSONIdentityClassificationCanonicalKind, StringValue: "idlechat"},
		{Pointer: "/a~1b~0c/0/thread_seq", Key: JSONIdentityKeyThreadSeq, ValueKind: JSONIdentityValueInteger, Classification: JSONIdentityClassificationCanonicalSeq, IntegerValue: jsonIdentityInt64(3)},
		{Pointer: "/a~1b~0c/1/discussion_id", Key: JSONIdentityKeyDiscussionID, ValueKind: JSONIdentityValueString, Classification: JSONIdentityClassificationLegacyNumeric, StringValue: "14"},
		{Pointer: "/thread_id", Key: JSONIdentityKeyThreadID, ValueKind: JSONIdentityValueString, Classification: JSONIdentityClassificationCanonicalThread, StringValue: canonicalThreadID},
		{Pointer: "/z/thread_id", Key: JSONIdentityKeyThreadID, ValueKind: JSONIdentityValueInteger, Classification: JSONIdentityClassificationLegacyNumeric, IntegerValue: jsonIdentityInt64(999)},
	}
	if !reflect.DeepEqual(receipt.Occurrences, want) {
		t.Fatalf("occurrences = %#v, want %#v", receipt.Occurrences, want)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("receipt Validate() error = %v", err)
	}
	canonical, err := receipt.CanonicalOccurrencesJSON()
	if err != nil {
		t.Fatalf("CanonicalOccurrencesJSON() error = %v", err)
	}
	if bytes.Contains(canonical, []byte("payload")) || bytes.Contains(canonical, []byte("ignored")) {
		t.Fatalf("canonical occurrence JSON leaked arbitrary payload: %s", canonical)
	}
}

func TestAuditJSONIdentityClassifiesCanonicalLegacyNullAndInvalidValues(t *testing.T) {
	canonicalThreadID := jsonIdentityTestThreadID(t, "classifications")
	raw := []byte(`{"thread_id":1,"closed_thread_id":"` + canonicalThreadID + `","last_thread_id":"2","discussion_id":"` + canonicalThreadID + `","thread_seq":7,"thread_kind":"system","nested":{"thread_id":null,"thread_seq":"7","thread_kind":"not-a-kind","last_thread_id":true}}`)
	receipt, err := AuditJSONIdentity(raw)
	if err != nil {
		t.Fatalf("AuditJSONIdentity() error = %v", err)
	}
	if receipt.OccurrenceCount != 10 {
		t.Fatalf("occurrence count = %d, want 10", receipt.OccurrenceCount)
	}
	byPointer := make(map[string]JSONIdentityOccurrence, len(receipt.Occurrences))
	for _, occurrence := range receipt.Occurrences {
		byPointer[occurrence.Pointer] = occurrence
	}
	assertJSONIdentityOccurrence(t, byPointer["/thread_id"], JSONIdentityKeyThreadID, JSONIdentityValueInteger, JSONIdentityClassificationLegacyNumeric, "", 1)
	assertJSONIdentityOccurrence(t, byPointer["/closed_thread_id"], JSONIdentityKeyClosedThreadID, JSONIdentityValueString, JSONIdentityClassificationCanonicalThread, canonicalThreadID, 0)
	assertJSONIdentityOccurrence(t, byPointer["/last_thread_id"], JSONIdentityKeyLastThreadID, JSONIdentityValueString, JSONIdentityClassificationLegacyNumeric, "2", 0)
	assertJSONIdentityOccurrence(t, byPointer["/discussion_id"], JSONIdentityKeyDiscussionID, JSONIdentityValueString, JSONIdentityClassificationInvalid, canonicalThreadID, 0)
	assertJSONIdentityOccurrence(t, byPointer["/thread_seq"], JSONIdentityKeyThreadSeq, JSONIdentityValueInteger, JSONIdentityClassificationCanonicalSeq, "", 7)
	assertJSONIdentityOccurrence(t, byPointer["/thread_kind"], JSONIdentityKeyThreadKind, JSONIdentityValueString, JSONIdentityClassificationCanonicalKind, "system", 0)
	assertJSONIdentityOccurrence(t, byPointer["/nested/thread_id"], JSONIdentityKeyThreadID, JSONIdentityValueNull, JSONIdentityClassificationNull, "", 0)
	assertJSONIdentityOccurrence(t, byPointer["/nested/thread_seq"], JSONIdentityKeyThreadSeq, JSONIdentityValueString, JSONIdentityClassificationInvalid, "7", 0)
	assertJSONIdentityOccurrence(t, byPointer["/nested/thread_kind"], JSONIdentityKeyThreadKind, JSONIdentityValueString, JSONIdentityClassificationInvalid, "not-a-kind", 0)
	assertJSONIdentityOccurrence(t, byPointer["/nested/last_thread_id"], JSONIdentityKeyLastThreadID, JSONIdentityValueOther, JSONIdentityClassificationInvalid, "", 0)
}

func TestAuditJSONIdentityFindsPartialTupleWithoutTransformingIt(t *testing.T) {
	receipt, err := AuditJSONIdentity([]byte(`{"session_id":"legacy-session","thread_id":42,"other":{"title":"kept"}}`))
	if err != nil {
		t.Fatalf("AuditJSONIdentity() error = %v", err)
	}
	if receipt.OccurrenceCount != 1 || receipt.Occurrences[0].Key != JSONIdentityKeyThreadID || receipt.Occurrences[0].IntegerValue == nil || *receipt.Occurrences[0].IntegerValue != 42 {
		t.Fatalf("partial tuple receipt = %+v", receipt)
	}
	if strings.Contains(string(mustJSONIdentityCanonical(t, receipt)), "legacy-session") {
		t.Fatal("auditor included unrelated tuple data")
	}
}

func TestAuditJSONIdentityRejectsDuplicateKeysAtEveryDepth(t *testing.T) {
	inputs := []string{
		`{"thread_id":1,"thread_id":2}`,
		`{"outer":{"thread_seq":1,"thread_seq":2}}`,
		`[{"thread_kind":"system","thread_kind":"document"}]`,
		`{"a":1,"\u0061":2}`,
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			if _, err := AuditJSONIdentity([]byte(input)); err == nil {
				t.Fatal("duplicate object key unexpectedly accepted")
			}
		})
	}
}

func TestAuditJSONIdentityRejectsBlankMalformedTrailingAndNonIntegerIdentityNumbers(t *testing.T) {
	inputs := []string{
		"",
		" \n\t ",
		`{"thread_id":`,
		`{"thread_id":1} false`,
		`{"thread_seq":1.0}`,
		`{"thread_id":1e3}`,
		`{"thread_id":9223372036854775808}`,
		`{"thread_seq":-9223372036854775809}`,
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			if _, err := AuditJSONIdentity([]byte(input)); err == nil {
				t.Fatal("invalid JSON identity payload unexpectedly accepted")
			}
		})
	}
}

func TestAuditJSONIdentityRejectsInvalidUTF8BeforeJSONDecoding(t *testing.T) {
	raw := []byte{'{', '"', 't', 'h', 'r', 'e', 'a', 'd', '_', 'i', 'd', '"', ':', '"', 0xff, '"', '}'}
	if _, err := AuditJSONIdentity(raw); err == nil || !strings.Contains(err.Error(), "valid UTF-8") {
		t.Fatalf("invalid UTF-8 error = %v, want bounded UTF-8 rejection", err)
	}
}

func TestAuditJSONIdentityRejectsOversizeAndExcessiveDepth(t *testing.T) {
	oversize := bytes.Repeat([]byte(" "), JSONIdentityMaxInputBytes+1)
	if _, err := AuditJSONIdentity(oversize); err == nil {
		t.Fatal("oversize identity payload unexpectedly accepted")
	}

	accepted := strings.Repeat("[", JSONIdentityMaxDepth-1) + `{"thread_id":1}` + strings.Repeat("]", JSONIdentityMaxDepth-1)
	if _, err := AuditJSONIdentity([]byte(accepted)); err != nil {
		t.Fatalf("depth at the configured bound rejected: %v", err)
	}
	excessive := strings.Repeat("[", JSONIdentityMaxDepth) + `{"thread_id":1}` + strings.Repeat("]", JSONIdentityMaxDepth)
	if _, err := AuditJSONIdentity([]byte(excessive)); err == nil {
		t.Fatal("excessive identity pointer depth unexpectedly accepted")
	}
}

func TestAuditJSONIdentityObjectOrderDoesNotChangeOccurrencesOrCanonicalHash(t *testing.T) {
	canonicalThreadID := jsonIdentityTestThreadID(t, "order")
	first, err := AuditJSONIdentity([]byte(`{"thread_kind":"system","thread_id":"` + canonicalThreadID + `","nested":{"thread_seq":2,"last_thread_id":"3"}}`))
	if err != nil {
		t.Fatalf("first AuditJSONIdentity() error = %v", err)
	}
	second, err := AuditJSONIdentity([]byte(`{"nested":{"last_thread_id":"3","thread_seq":2},"thread_id":"` + canonicalThreadID + `","thread_kind":"system"}`))
	if err != nil {
		t.Fatalf("second AuditJSONIdentity() error = %v", err)
	}
	if !reflect.DeepEqual(first.Occurrences, second.Occurrences) || first.CanonicalOccurrenceSHA256 != second.CanonicalOccurrenceSHA256 {
		t.Fatalf("object order changed canonical occurrences/hash: first=%+v second=%+v", first, second)
	}
	if first.InputSHA256 == second.InputSHA256 {
		t.Fatal("different source bytes unexpectedly share input SHA256")
	}
	wantCanonical := mustJSONIdentityCanonical(t, first)
	digest := sha256.Sum256(wantCanonical)
	if first.CanonicalOccurrenceSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("canonical occurrence SHA256 = %q, want %x", first.CanonicalOccurrenceSHA256, digest)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("first receipt Validate() error = %v", err)
	}
	tampered := first
	tampered.CanonicalOccurrenceSHA256 = strings.Repeat("0", sha256.Size*2)
	if err := tampered.Validate(); err == nil {
		t.Fatal("tampered canonical occurrence hash unexpectedly validated")
	}
	tampered = first
	tampered.Occurrences = append([]JSONIdentityOccurrence(nil), first.Occurrences...)
	tampered.Occurrences[0].Classification = JSONIdentityClassificationInvalid
	if err := tampered.Validate(); err == nil {
		t.Fatal("tampered occurrence unexpectedly validated")
	}
}

func assertJSONIdentityOccurrence(t *testing.T, occurrence JSONIdentityOccurrence, key string, valueKind JSONIdentityValueKind, classification JSONIdentityClassification, stringValue string, integerValue int64) {
	t.Helper()
	if occurrence.Key != key || occurrence.ValueKind != valueKind || occurrence.Classification != classification || occurrence.StringValue != stringValue {
		t.Fatalf("occurrence = %+v, want key=%q kind=%q classification=%q string=%q", occurrence, key, valueKind, classification, stringValue)
	}
	if integerValue == 0 {
		if occurrence.IntegerValue != nil {
			t.Fatalf("occurrence = %+v, did not want integer value", occurrence)
		}
		return
	}
	if occurrence.IntegerValue == nil || *occurrence.IntegerValue != integerValue {
		t.Fatalf("occurrence integer = %v, want %d", occurrence.IntegerValue, integerValue)
	}
}

func jsonIdentityTestThreadID(t *testing.T, source string) string {
	t.Helper()
	value, err := modulecore.NewMigrationID(modulecore.CanonicalThreadID, "json_identity_test", "thread_id", source)
	if err != nil {
		t.Fatalf("NewMigrationID() error = %v", err)
	}
	return value
}

func jsonIdentityInt64(value int64) *int64 {
	return &value
}

func mustJSONIdentityCanonical(t *testing.T, receipt JSONIdentityAuditReceipt) []byte {
	t.Helper()
	canonical, err := receipt.CanonicalOccurrencesJSON()
	if err != nil {
		t.Fatalf("CanonicalOccurrencesJSON() error = %v", err)
	}
	var decoded []JSONIdentityOccurrence
	if err := json.Unmarshal(canonical, &decoded); err != nil {
		t.Fatalf("canonical occurrence JSON is malformed: %v", err)
	}
	return canonical
}
