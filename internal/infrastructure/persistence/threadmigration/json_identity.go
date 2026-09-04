package threadmigration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	// JSONIdentityMaxInputBytes bounds one payload accepted by AuditJSONIdentity.
	JSONIdentityMaxInputBytes = 32 * 1024 * 1024

	// JSONIdentityMaxDepth bounds the number of JSON pointer components. The
	// root value is at depth zero, so a direct member of the root is depth one.
	JSONIdentityMaxDepth = 128
)

const (
	JSONIdentityKeyThreadID       = "thread_id"
	JSONIdentityKeyClosedThreadID = "closed_thread_id"
	JSONIdentityKeyLastThreadID   = "last_thread_id"
	JSONIdentityKeyDiscussionID   = "discussion_id"
	JSONIdentityKeyThreadSeq      = "thread_seq"
	JSONIdentityKeyThreadKind     = "thread_kind"
)

// JSONIdentityValueKind describes the JSON representation of an identity
// field. Values other than null, strings, and bounded JSON integers are other.
type JSONIdentityValueKind string

const (
	JSONIdentityValueNull    JSONIdentityValueKind = "null"
	JSONIdentityValueString  JSONIdentityValueKind = "string"
	JSONIdentityValueInteger JSONIdentityValueKind = "integer"
	JSONIdentityValueOther   JSONIdentityValueKind = "other"
)

// JSONIdentityClassification is the bounded machine decision attached to an
// identity-field occurrence. It deliberately does not carry any arbitrary
// payload value or inferred alias.
type JSONIdentityClassification string

const (
	JSONIdentityClassificationEmpty           JSONIdentityClassification = "empty"
	JSONIdentityClassificationLegacyNumeric   JSONIdentityClassification = "legacy_numeric"
	JSONIdentityClassificationCanonicalThread JSONIdentityClassification = "canonical_thread_id"
	JSONIdentityClassificationCanonicalSeq    JSONIdentityClassification = "canonical_seq"
	JSONIdentityClassificationCanonicalKind   JSONIdentityClassification = "canonical_kind"
	JSONIdentityClassificationNull            JSONIdentityClassification = "null"
	JSONIdentityClassificationInvalid         JSONIdentityClassification = "invalid"
)

// JSONIdentityOccurrence is one exact-key identity reference found in a JSON
// value. StringValue is meaningful for string values; IntegerValue is
// meaningful for integer values. The classification is intentionally bounded
// and is the only interpretation the auditor makes.
type JSONIdentityOccurrence struct {
	Pointer        string                     `json:"pointer"`
	Key            string                     `json:"key"`
	ValueKind      JSONIdentityValueKind      `json:"value_kind"`
	Classification JSONIdentityClassification `json:"classification"`
	StringValue    string                     `json:"string_value,omitempty"`
	IntegerValue   *int64                     `json:"integer_value,omitempty"`
}

// JSONIdentityAuditReceipt is the deterministic, non-transforming result of
// auditing one JSON payload. The canonical occurrence digest covers only the
// sorted Occurrences JSON, never the original payload or arbitrary fields.
type JSONIdentityAuditReceipt struct {
	InputSHA256               string                   `json:"input_sha256"`
	OccurrenceCount           int                      `json:"occurrence_count"`
	Occurrences               []JSONIdentityOccurrence `json:"occurrences"`
	CanonicalOccurrenceSHA256 string                   `json:"canonical_occurrence_sha256"`
}

// AuditJSONIdentity parses exactly one bounded JSON value and reports only
// exact Step 05 identity keys. It performs no migration, persistence, or I/O.
func AuditJSONIdentity(raw []byte) (JSONIdentityAuditReceipt, error) {
	inputSHA256 := sha256HexJSONIdentity(raw)
	if len(raw) > JSONIdentityMaxInputBytes {
		return JSONIdentityAuditReceipt{}, fmt.Errorf("JSON identity payload exceeds %d bytes", JSONIdentityMaxInputBytes)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return JSONIdentityAuditReceipt{}, errors.New("JSON identity payload is blank")
	}
	if !utf8.Valid(raw) {
		return JSONIdentityAuditReceipt{}, errors.New("JSON identity payload is not valid UTF-8")
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	auditor := jsonIdentityAuditor{
		decoder:     decoder,
		occurrences: make([]JSONIdentityOccurrence, 0),
	}
	if err := auditor.walk("", 0, ""); err != nil {
		return JSONIdentityAuditReceipt{}, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return JSONIdentityAuditReceipt{}, fmt.Errorf("trailing JSON token: %w", err)
		}
		return JSONIdentityAuditReceipt{}, errors.New("trailing JSON token")
	}

	sortJSONIdentityOccurrences(auditor.occurrences)
	receipt := JSONIdentityAuditReceipt{
		InputSHA256:     inputSHA256,
		OccurrenceCount: len(auditor.occurrences),
		Occurrences:     auditor.occurrences,
	}
	digest, err := receipt.ComputeCanonicalOccurrenceSHA256()
	if err != nil {
		return JSONIdentityAuditReceipt{}, fmt.Errorf("hash JSON identity occurrences: %w", err)
	}
	receipt.CanonicalOccurrenceSHA256 = digest
	return receipt, nil
}

type jsonIdentityAuditor struct {
	decoder     *json.Decoder
	occurrences []JSONIdentityOccurrence
}

func (auditor *jsonIdentityAuditor) walk(pointer string, depth int, key string) error {
	if depth > JSONIdentityMaxDepth {
		return fmt.Errorf("JSON identity pointer depth exceeds %d", JSONIdentityMaxDepth)
	}
	token, err := auditor.decoder.Token()
	if err != nil {
		return fmt.Errorf("decode JSON identity value: %w", err)
	}

	if isJSONIdentityKey(key) {
		if err := auditor.record(pointer, key, token); err != nil {
			return err
		}
	}

	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		return auditor.walkObject(pointer, depth)
	case '[':
		return auditor.walkArray(pointer, depth)
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}

func (auditor *jsonIdentityAuditor) walkObject(pointer string, depth int) error {
	seenKeys := make(map[string]struct{})
	for auditor.decoder.More() {
		keyToken, err := auditor.decoder.Token()
		if err != nil {
			return fmt.Errorf("decode JSON object key: %w", err)
		}
		objectKey, ok := keyToken.(string)
		if !ok {
			return errors.New("JSON object key is not a string")
		}
		if _, duplicate := seenKeys[objectKey]; duplicate {
			return errors.New("duplicate JSON object key")
		}
		seenKeys[objectKey] = struct{}{}

		childPointer := pointer + "/" + escapeJSONPointerToken(objectKey)
		if err := auditor.walk(childPointer, depth+1, objectKey); err != nil {
			return err
		}
	}
	closeToken, err := auditor.decoder.Token()
	if err != nil {
		return fmt.Errorf("close JSON object: %w", err)
	}
	if closeToken != json.Delim('}') {
		return fmt.Errorf("JSON object closed by %q", closeToken)
	}
	return nil
}

func (auditor *jsonIdentityAuditor) walkArray(pointer string, depth int) error {
	for index := 0; auditor.decoder.More(); index++ {
		childPointer := pointer + "/" + strconv.Itoa(index)
		if err := auditor.walk(childPointer, depth+1, ""); err != nil {
			return err
		}
	}
	closeToken, err := auditor.decoder.Token()
	if err != nil {
		return fmt.Errorf("close JSON array: %w", err)
	}
	if closeToken != json.Delim(']') {
		return fmt.Errorf("JSON array closed by %q", closeToken)
	}
	return nil
}

func (auditor *jsonIdentityAuditor) record(pointer, key string, token json.Token) error {
	occurrence := JSONIdentityOccurrence{Pointer: pointer, Key: key}
	switch value := token.(type) {
	case nil:
		occurrence.ValueKind = JSONIdentityValueNull
		occurrence.Classification = JSONIdentityClassificationNull
	case string:
		occurrence.ValueKind = JSONIdentityValueString
		occurrence.StringValue = value
		occurrence.Classification = classifyJSONIdentityString(key, value)
	case json.Number:
		integer, ok := parseJSONIdentityInteger(value)
		if !ok {
			if isJSONIdentityNumericKey(key) {
				return fmt.Errorf("identity field %q contains a JSON number that is not an int64 integer", key)
			}
			occurrence.ValueKind = JSONIdentityValueOther
			occurrence.Classification = JSONIdentityClassificationInvalid
			break
		}
		occurrence.ValueKind = JSONIdentityValueInteger
		occurrence.IntegerValue = &integer
		occurrence.Classification = classifyJSONIdentityInteger(key, integer)
	case json.Delim:
		occurrence.ValueKind = JSONIdentityValueOther
		occurrence.Classification = JSONIdentityClassificationInvalid
	default:
		occurrence.ValueKind = JSONIdentityValueOther
		occurrence.Classification = JSONIdentityClassificationInvalid
	}
	auditor.occurrences = append(auditor.occurrences, occurrence)
	return nil
}

func classifyJSONIdentityString(key, value string) JSONIdentityClassification {
	if value == "" {
		return JSONIdentityClassificationEmpty
	}
	switch key {
	case JSONIdentityKeyThreadSeq:
		// ThreadSeq is canonical only as a positive JSON integer. A string
		// numeric value is deliberately not accepted as a sequence.
		return JSONIdentityClassificationInvalid
	case JSONIdentityKeyThreadKind:
		if modulecore.ThreadKind(value).Validate() == nil {
			return JSONIdentityClassificationCanonicalKind
		}
		return JSONIdentityClassificationInvalid
	case JSONIdentityKeyDiscussionID:
		if _, ok := parseLegacyNumericString(value); ok {
			return JSONIdentityClassificationLegacyNumeric
		}
		return JSONIdentityClassificationInvalid
	default:
		if modulecore.ThreadID(value).Validate() == nil {
			return JSONIdentityClassificationCanonicalThread
		}
		if _, ok := parseLegacyNumericString(value); ok {
			return JSONIdentityClassificationLegacyNumeric
		}
		return JSONIdentityClassificationInvalid
	}
}

func classifyJSONIdentityInteger(key string, value int64) JSONIdentityClassification {
	switch key {
	case JSONIdentityKeyThreadSeq:
		if modulecore.ThreadSeq(value).Validate() == nil {
			return JSONIdentityClassificationCanonicalSeq
		}
		return JSONIdentityClassificationInvalid
	case JSONIdentityKeyThreadKind:
		return JSONIdentityClassificationInvalid
	default:
		// Numeric values under all legacy thread-reference keys, including
		// discussion_id, remain legacy observations. The consumer decides
		// whether zero or a negative value is usable for its source schema.
		return JSONIdentityClassificationLegacyNumeric
	}
}

func isJSONIdentityNumericKey(key string) bool {
	return key != JSONIdentityKeyThreadKind
}

func parseJSONIdentityInteger(number json.Number) (int64, bool) {
	text := string(number)
	if text == "" || strings.ContainsAny(text, ".eE") {
		return 0, false
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func parseLegacyNumericString(value string) (int64, bool) {
	if value == "" || strings.ContainsAny(value, ".eE") {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func escapeJSONPointerToken(token string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(token)
}

func sortJSONIdentityOccurrences(occurrences []JSONIdentityOccurrence) {
	sort.Slice(occurrences, func(left, right int) bool {
		return jsonIdentityOccurrenceLess(occurrences[left], occurrences[right])
	})
}

func jsonIdentityOccurrenceLess(left, right JSONIdentityOccurrence) bool {
	if left.Pointer != right.Pointer {
		return left.Pointer < right.Pointer
	}
	if left.Key != right.Key {
		return left.Key < right.Key
	}
	if left.Classification != right.Classification {
		return left.Classification < right.Classification
	}
	if left.ValueKind != right.ValueKind {
		return left.ValueKind < right.ValueKind
	}
	if left.StringValue != right.StringValue {
		return left.StringValue < right.StringValue
	}
	return jsonIdentityIntegerLess(left.IntegerValue, right.IntegerValue)
}

func jsonIdentityIntegerLess(left, right *int64) bool {
	if left == nil {
		return right != nil
	}
	if right == nil {
		return false
	}
	return *left < *right
}

// CanonicalOccurrencesJSON returns the stable JSON representation used for
// CanonicalOccurrenceSHA256. It includes occurrences only, never the source
// payload, input digest, count, or any arbitrary JSON value.
func (receipt JSONIdentityAuditReceipt) CanonicalOccurrencesJSON() ([]byte, error) {
	ordered := append([]JSONIdentityOccurrence(nil), receipt.Occurrences...)
	sortJSONIdentityOccurrences(ordered)
	if ordered == nil {
		ordered = []JSONIdentityOccurrence{}
	}
	return json.Marshal(ordered)
}

// CanonicalJSON is an alias for CanonicalOccurrencesJSON, matching other
// deterministic migration receipts in this package.
func (receipt JSONIdentityAuditReceipt) CanonicalJSON() ([]byte, error) {
	return receipt.CanonicalOccurrencesJSON()
}

// ComputeCanonicalOccurrenceSHA256 computes the digest over only the sorted
// occurrence JSON.
func (receipt JSONIdentityAuditReceipt) ComputeCanonicalOccurrenceSHA256() (string, error) {
	canonical, err := receipt.CanonicalOccurrencesJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

// Validate checks receipt shape, occurrence ordering, and the stored
// canonical occurrence digest. It cannot recompute InputSHA256 without the
// original raw bytes, so it validates that digest's representation only.
func (receipt JSONIdentityAuditReceipt) Validate() error {
	if !validJSONIdentitySHA256(receipt.InputSHA256) {
		return errors.New("input SHA256 is invalid")
	}
	if receipt.OccurrenceCount < 0 || receipt.OccurrenceCount != len(receipt.Occurrences) {
		return errors.New("identity occurrence count does not match occurrences")
	}
	if receipt.Occurrences == nil {
		return errors.New("identity occurrences must be a non-nil slice")
	}
	for index, occurrence := range receipt.Occurrences {
		if err := validateJSONIdentityOccurrence(occurrence); err != nil {
			return fmt.Errorf("identity occurrence %d: %w", index, err)
		}
		if index > 0 && jsonIdentityOccurrenceLess(occurrence, receipt.Occurrences[index-1]) {
			return errors.New("identity occurrences are not stably sorted")
		}
	}
	if !validJSONIdentitySHA256(receipt.CanonicalOccurrenceSHA256) {
		return errors.New("canonical occurrence SHA256 is invalid")
	}
	computed, err := receipt.ComputeCanonicalOccurrenceSHA256()
	if err != nil {
		return fmt.Errorf("compute canonical occurrence SHA256: %w", err)
	}
	if computed != receipt.CanonicalOccurrenceSHA256 {
		return errors.New("canonical occurrence SHA256 does not match occurrences")
	}
	return nil
}

func validateJSONIdentityOccurrence(occurrence JSONIdentityOccurrence) error {
	if !isJSONIdentityKey(occurrence.Key) {
		return fmt.Errorf("unknown identity key %q", occurrence.Key)
	}
	if !validJSONPointer(occurrence.Pointer) {
		return errors.New("identity occurrence pointer is invalid")
	}
	if strings.Count(occurrence.Pointer, "/") > JSONIdentityMaxDepth || !strings.HasSuffix(occurrence.Pointer, "/"+escapeJSONPointerToken(occurrence.Key)) {
		return errors.New("identity occurrence pointer does not identify its key")
	}
	switch occurrence.ValueKind {
	case JSONIdentityValueNull:
		if occurrence.StringValue != "" || occurrence.IntegerValue != nil {
			return errors.New("null value carries a scalar")
		}
	case JSONIdentityValueString:
		if occurrence.IntegerValue != nil {
			return errors.New("string value carries an integer")
		}
	case JSONIdentityValueInteger:
		if occurrence.IntegerValue == nil || occurrence.StringValue != "" {
			return errors.New("integer value is missing")
		}
	case JSONIdentityValueOther:
		if occurrence.IntegerValue != nil || occurrence.StringValue != "" {
			return errors.New("other value carries a scalar")
		}
	default:
		return fmt.Errorf("unknown JSON value kind %q", occurrence.ValueKind)
	}
	switch occurrence.Classification {
	case JSONIdentityClassificationEmpty:
		if occurrence.ValueKind != JSONIdentityValueString || occurrence.StringValue != "" {
			return errors.New("empty classification is not an empty string")
		}
	case JSONIdentityClassificationLegacyNumeric:
		if occurrence.ValueKind != JSONIdentityValueInteger && occurrence.ValueKind != JSONIdentityValueString {
			return errors.New("legacy numeric classification has an invalid value kind")
		}
		if occurrence.ValueKind == JSONIdentityValueString {
			if _, ok := parseLegacyNumericString(occurrence.StringValue); !ok {
				return errors.New("legacy numeric classification has a nonnumeric string")
			}
		}
	case JSONIdentityClassificationCanonicalThread:
		if occurrence.Key == JSONIdentityKeyDiscussionID || occurrence.ValueKind != JSONIdentityValueString || modulecore.ThreadID(occurrence.StringValue).Validate() != nil {
			return errors.New("canonical thread classification is invalid")
		}
	case JSONIdentityClassificationCanonicalSeq:
		if occurrence.Key != JSONIdentityKeyThreadSeq || occurrence.ValueKind != JSONIdentityValueInteger || occurrence.IntegerValue == nil || modulecore.ThreadSeq(*occurrence.IntegerValue).Validate() != nil {
			return errors.New("canonical sequence classification is invalid")
		}
	case JSONIdentityClassificationCanonicalKind:
		if occurrence.Key != JSONIdentityKeyThreadKind || occurrence.ValueKind != JSONIdentityValueString || modulecore.ThreadKind(occurrence.StringValue).Validate() != nil {
			return errors.New("canonical kind classification is invalid")
		}
	case JSONIdentityClassificationNull:
		if occurrence.ValueKind != JSONIdentityValueNull {
			return errors.New("null classification is not null")
		}
	case JSONIdentityClassificationInvalid:
		// Invalid is intentionally permissive about the source scalar. The
		// auditor reports it without guessing an alternate representation.
	default:
		return fmt.Errorf("unknown identity classification %q", occurrence.Classification)
	}
	var expected JSONIdentityClassification
	switch occurrence.ValueKind {
	case JSONIdentityValueNull:
		expected = JSONIdentityClassificationNull
	case JSONIdentityValueString:
		expected = classifyJSONIdentityString(occurrence.Key, occurrence.StringValue)
	case JSONIdentityValueInteger:
		expected = classifyJSONIdentityInteger(occurrence.Key, *occurrence.IntegerValue)
	case JSONIdentityValueOther:
		expected = JSONIdentityClassificationInvalid
	}
	if occurrence.Classification != expected {
		return fmt.Errorf("classification %q does not match value", occurrence.Classification)
	}
	return nil
}

func isJSONIdentityKey(key string) bool {
	switch key {
	case JSONIdentityKeyThreadID, JSONIdentityKeyClosedThreadID, JSONIdentityKeyLastThreadID, JSONIdentityKeyDiscussionID, JSONIdentityKeyThreadSeq, JSONIdentityKeyThreadKind:
		return true
	default:
		return false
	}
}

func validJSONPointer(pointer string) bool {
	if pointer == "" {
		return true
	}
	if pointer[0] != '/' {
		return false
	}
	for index := 1; index < len(pointer); index++ {
		if pointer[index] != '~' {
			continue
		}
		if index+1 >= len(pointer) || (pointer[index+1] != '0' && pointer[index+1] != '1') {
			return false
		}
		index++
	}
	return true
}

func validJSONIdentitySHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sha256HexJSONIdentity(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
