package storagehost

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	l1MaxPayloadBytes      = 8 << 20
	l1MaxJSONValueBytes    = 1 << 20
	l1MaxTextBytes         = 2 << 20
	l1MaxJSONTextBytes     = 64 << 10
	l1MaxJSONDepth         = 24
	l1MaxJSONNodes         = 50_000
	l1MaxCollectionEntries = 2_048
	l1MaxListLimit         = 1_000
	l1MaxRecallItems       = 512
	l1MaxSearchURLs        = 256
)

type l1SaveMessagePayload struct {
	SessionID   string                `json:"session_id"`
	ThreadID    modulecore.ThreadID   `json:"thread_id"`
	ThreadSeq   modulecore.ThreadSeq  `json:"thread_seq"`
	ThreadKind  modulecore.ThreadKind `json:"thread_kind"`
	Namespace   string                `json:"namespace"`
	Message     domconv.Message       `json:"message"`
	MemoryState string                `json:"memory_state"`
}

type l1SaveSearchCachePayload struct {
	Provider    string        `json:"provider"`
	RawQuery    string        `json:"raw_query"`
	ResultsJSON string        `json:"results_json"`
	SourceURLs  []string      `json:"source_urls"`
	TTL         time.Duration `json:"ttl_ns"`
}

type l1SearchCacheQueryPayload struct {
	Provider string    `json:"provider"`
	RawQuery string    `json:"raw_query"`
	Now      time.Time `json:"now"`
}

type l1SimilarSearchCachePayload struct {
	Provider  string    `json:"provider"`
	RawQuery  string    `json:"raw_query"`
	Now       time.Time `json:"now"`
	Threshold float64   `json:"threshold"`
}

type l1InvalidateSearchCachePayload struct {
	Provider string `json:"provider"`
	RawQuery string `json:"raw_query"`
}

type l1KnowledgeSearchPayload struct {
	Domain string `json:"domain"`
	Query  string `json:"query"`
	Limit  int    `json:"limit"`
}

type l1WikiSearchPayload struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type l1AppendEventPayload struct {
	EventType  string                `json:"event_type"`
	Namespace  string                `json:"namespace"`
	SessionID  string                `json:"session_id"`
	ThreadID   modulecore.ThreadID   `json:"thread_id"`
	ThreadSeq  modulecore.ThreadSeq  `json:"thread_seq"`
	ThreadKind modulecore.ThreadKind `json:"thread_kind"`
	Payload    json.RawMessage       `json:"payload"`
	Source     string                `json:"source"`
}

type l1RecentEventsPayload struct {
	Namespace string `json:"namespace"`
	Limit     int    `json:"limit"`
}

type l1UpdateMemoryStatePayload struct {
	ID          string `json:"id"`
	MemoryState string `json:"memory_state"`
}

type l1PromoteMemoryPayload struct {
	ID              string `json:"id"`
	TargetNamespace string `json:"target_namespace"`
	PromotedBy      string `json:"promoted_by"`
}

type l1RecentByNamespacePayload struct {
	Namespace string `json:"namespace"`
	Limit     int    `json:"limit"`
}

type l1RecentByStatePayload struct {
	MemoryState string `json:"memory_state"`
	Limit       int    `json:"limit"`
}

type l1RecentBySessionPayload struct {
	SessionID string `json:"session_id"`
	Limit     int    `json:"limit"`
}

type l1LatestThreadPayload struct {
	SessionID string `json:"session_id"`
}

type l1SaveRecallTracePayload struct {
	Trace l1RecallTraceDTO `json:"trace"`
}

type l1RecentRecallTracesPayload struct {
	SessionID string `json:"session_id"`
	Limit     int    `json:"limit"`
}

type l1SearchCacheDTO struct {
	QueryHash       string    `json:"query_hash"`
	NormalizedQuery string    `json:"normalized_query"`
	Provider        string    `json:"provider"`
	RawQuery        string    `json:"raw_query"`
	ResultsJSON     string    `json:"results_json"`
	SourceURLs      []string  `json:"source_urls"`
	RetrievedAt     time.Time `json:"retrieved_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type l1SearchCacheResult struct {
	Entry *l1SearchCacheDTO `json:"entry"`
}

type l1AffectedResult struct {
	Affected int64 `json:"affected"`
}

type l1KnowledgeItemDTO struct {
	ID           string          `json:"id"`
	StagingID    string          `json:"staging_id"`
	Domain       string          `json:"domain"`
	Title        string          `json:"title"`
	SourceID     string          `json:"source_id"`
	SourceURL    string          `json:"source_url"`
	RawText      string          `json:"raw_text"`
	RawHash      string          `json:"raw_hash"`
	SummaryDraft string          `json:"summary_draft"`
	Keywords     []string        `json:"keywords"`
	LicenseNote  string          `json:"license_note"`
	Meta         json.RawMessage `json:"meta"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type l1KnowledgeSearchResult struct {
	Items []l1KnowledgeItemDTO `json:"items"`
}

type l1WikiPageDTO struct {
	PageID          string    `json:"page_id"`
	Path            string    `json:"path"`
	Title           string    `json:"title"`
	Type            string    `json:"type"`
	Status          string    `json:"status"`
	Owner           string    `json:"owner"`
	CanonicalSource string    `json:"canonical_source"`
	SourcePaths     []string  `json:"source_paths"`
	Related         []string  `json:"related"`
	Summary         string    `json:"summary"`
	ContentHash     string    `json:"content_hash"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type l1WikiSearchResult struct {
	Items []l1WikiPageDTO `json:"items"`
}

type l1EventLogDTO struct {
	ID         string                `json:"id"`
	EventType  string                `json:"event_type"`
	Namespace  string                `json:"namespace"`
	SessionID  string                `json:"session_id"`
	ThreadID   modulecore.ThreadID   `json:"thread_id"`
	ThreadSeq  modulecore.ThreadSeq  `json:"thread_seq"`
	ThreadKind modulecore.ThreadKind `json:"thread_kind"`
	Payload    json.RawMessage       `json:"payload"`
	Source     string                `json:"source"`
	CreatedAt  time.Time             `json:"created_at"`
}

type l1EventLogResult struct {
	Event l1EventLogDTO `json:"event"`
}

type l1RecentEventsResult struct {
	Events []l1EventLogDTO `json:"events"`
}

type l1MemoryEventDTO struct {
	ID          string                `json:"id"`
	Namespace   string                `json:"namespace"`
	SessionID   string                `json:"session_id"`
	ThreadID    modulecore.ThreadID   `json:"thread_id"`
	ThreadSeq   modulecore.ThreadSeq  `json:"thread_seq"`
	ThreadKind  modulecore.ThreadKind `json:"thread_kind"`
	Speaker     domconv.Speaker       `json:"speaker"`
	Message     string                `json:"message"`
	Meta        json.RawMessage       `json:"meta"`
	MemoryState string                `json:"memory_state"`
	Layer       string                `json:"layer"`
	Source      string                `json:"source"`
	CreatedAt   time.Time             `json:"created_at"`
	UpdatedAt   time.Time             `json:"updated_at"`
}

type l1MemoryEventResult struct {
	Event *l1MemoryEventDTO `json:"event"`
}

type l1MemoryEventsResult struct {
	Events []l1MemoryEventDTO `json:"events"`
}

type l1LatestThreadResult struct {
	ThreadID   modulecore.ThreadID   `json:"thread_id"`
	ThreadSeq  modulecore.ThreadSeq  `json:"thread_seq"`
	ThreadKind modulecore.ThreadKind `json:"thread_kind"`
	Found      bool                  `json:"found"`
}

type l1RecallTraceDTO struct {
	TraceID    modulecore.TraceID     `json:"trace_id"`
	TurnID     modulecore.TurnID      `json:"turn_id"`
	RootTaskID modulecore.TaskID      `json:"root_task_id"`
	SessionID  string                 `json:"session_id"`
	OwnerID    string                 `json:"owner_id"`
	Role       string                 `json:"role"`
	Items      []l1RecallTraceItemDTO `json:"items"`
	CreatedAt  time.Time              `json:"created_at"`
}

type l1RecallTraceItemDTO struct {
	Layer         string    `json:"layer"`
	Kind          string    `json:"kind"`
	MemoryID      string    `json:"memory_id"`
	SourceID      string    `json:"source_id"`
	SourceType    string    `json:"source_type"`
	Summary       string    `json:"summary"`
	Query         string    `json:"query"`
	Provider      string    `json:"provider"`
	SourceURLs    []string  `json:"source_urls"`
	RetrievedAt   time.Time `json:"retrieved_at"`
	Score         float32   `json:"score"`
	Decision      string    `json:"decision"`
	Status        string    `json:"status"`
	Reason        string    `json:"reason"`
	MemoryState   string    `json:"memory_state"`
	Sensitivity   string    `json:"sensitivity"`
	PromptSection string    `json:"prompt_section"`
	TokenCount    int       `json:"token_count"`
	PromptIndex   int       `json:"prompt_index"`
}

type l1RecallTraceResult struct {
	Trace l1RecallTraceDTO `json:"trace"`
}

type l1RecentRecallTracesResult struct {
	Traces []l1RecallTraceDTO `json:"traces"`
}

// l1CallResult keeps strict DTO and domain reconstruction inside Client.Call's
// response decode. That lets the shared client's payload-keyed retry identity
// survive a completed mutation whose result cannot be reconstructed.
type l1CallResult struct {
	destination any
	validate    func() error
	mutating    bool
	err         error
}

func (result *l1CallResult) UnmarshalJSON(raw []byte) error {
	if result == nil || result.destination == nil {
		return errors.New("l1 result destination is missing")
	}
	decodeErr := decodeL1Result(raw, result.destination)
	if decodeErr == nil && result.validate != nil {
		decodeErr = result.validate()
	}
	if decodeErr == nil {
		return nil
	}
	result.err = decodeErr
	if result.mutating {
		return errors.New("l1 mutation result could not be reconstructed")
	}
	return nil
}

func decodeL1Payload(raw json.RawMessage, destination any) error {
	if err := inspectL1JSON(raw, l1MaxPayloadBytes, l1MaxTextBytes, l1MaxJSONDepth, l1MaxJSONNodes, l1MaxCollectionEntries, true); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("l1 payload has trailing data")
	}
	return nil
}

func marshalL1Payload(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, NewError(ErrorCodeSchemaRejected, "l1 payload could not be encoded")
	}
	if err := inspectL1JSON(encoded, l1MaxPayloadBytes, l1MaxTextBytes, l1MaxJSONDepth, l1MaxJSONNodes, l1MaxCollectionEntries, true); err != nil {
		return nil, NewError(ErrorCodeSchemaRejected, "l1 payload exceeds the bounded JSON contract")
	}
	return json.RawMessage(encoded), nil
}

func l1ReadyResult(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil || inspectL1JSON(encoded, l1MaxPayloadBytes, l1MaxTextBytes, l1MaxJSONDepth, l1MaxJSONNodes, l1MaxCollectionEntries, true) != nil {
		return nil, NewError(ErrorCodeStoreUnavailable, "l1 owner returned an invalid or oversized result")
	}
	return value, nil
}

func decodeL1Result(raw json.RawMessage, destination any) error {
	if err := inspectL1JSON(raw, l1MaxPayloadBytes, l1MaxTextBytes, l1MaxJSONDepth, l1MaxJSONNodes, l1MaxCollectionEntries, true); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("l1 result has trailing data")
	}
	return nil
}

func inspectL1JSON(raw []byte, maxBytes, maxStringBytes, maxDepth, maxNodes, maxCollection int, requireObject bool) error {
	if len(raw) == 0 || len(raw) > maxBytes || !utf8.Valid(raw) {
		return errors.New("l1 JSON size or encoding is invalid")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || (requireObject && trimmed[0] != '{') {
		return errors.New("l1 JSON object is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := consumeL1JSONValue(decoder, 1, &l1JSONBudget{maxStringBytes: maxStringBytes, maxDepth: maxDepth, maxNodes: maxNodes, maxCollection: maxCollection}); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("l1 JSON contains trailing data")
		}
		return err
	}
	return nil
}

type l1JSONBudget struct {
	maxStringBytes int
	maxDepth       int
	maxNodes       int
	maxCollection  int
	nodes          int
}

func consumeL1JSONValue(decoder *json.Decoder, depth int, budget *l1JSONBudget) error {
	if depth > budget.maxDepth {
		return errors.New("l1 JSON nesting limit exceeded")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	budget.nodes++
	if budget.nodes > budget.maxNodes {
		return errors.New("l1 JSON node limit exceeded")
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			seen := make(map[string]struct{})
			entries := 0
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || !utf8.ValidString(key) || len(key) > budget.maxStringBytes {
					return errors.New("l1 JSON object key is invalid")
				}
				if _, duplicate := seen[key]; duplicate {
					return errors.New("l1 JSON object has duplicate keys")
				}
				seen[key] = struct{}{}
				entries++
				budget.nodes++
				if entries > budget.maxCollection || budget.nodes > budget.maxNodes {
					return errors.New("l1 JSON object limit exceeded")
				}
				if err := consumeL1JSONValue(decoder, depth+1, budget); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("l1 JSON object is malformed")
			}
		case '[':
			entries := 0
			for decoder.More() {
				entries++
				if entries > budget.maxCollection {
					return errors.New("l1 JSON array limit exceeded")
				}
				if err := consumeL1JSONValue(decoder, depth+1, budget); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("l1 JSON array is malformed")
			}
		default:
			return errors.New("l1 JSON delimiter is invalid")
		}
	case string:
		if !utf8.ValidString(value) || len(value) > budget.maxStringBytes {
			return errors.New("l1 JSON string limit exceeded")
		}
	case json.Number:
		parsed, err := strconv.ParseFloat(string(value), 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return errors.New("l1 JSON number is non-finite or invalid")
		}
	case bool, nil:
	default:
		return errors.New("l1 JSON value type is invalid")
	}
	return nil
}

func l1ObjectToRaw(value map[string]interface{}) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage("null"), nil
	}
	if err := validateL1GoJSONValue(reflect.ValueOf(value), 1, 0); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if err := inspectL1JSON(encoded, l1MaxJSONValueBytes, l1MaxJSONTextBytes, l1MaxJSONDepth, l1MaxJSONNodes, l1MaxCollectionEntries, true); err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}

func l1ObjectFromRaw(raw json.RawMessage) (map[string]interface{}, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	if err := inspectL1JSON(raw, l1MaxJSONValueBytes, l1MaxJSONTextBytes, l1MaxJSONDepth, l1MaxJSONNodes, l1MaxCollectionEntries, true); err != nil {
		return nil, err
	}
	var value map[string]interface{}
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, errors.New("l1 metadata object is invalid")
	}
	return value, nil
}

func validateL1GoJSONValue(value reflect.Value, depth, nodes int) error {
	if depth > l1MaxJSONDepth || nodes > l1MaxJSONNodes {
		return errors.New("l1 metadata nesting or node limit exceeded")
	}
	if !value.IsValid() {
		return nil
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		return validateL1GoJSONValue(value.Elem(), depth+1, nodes+1)
	case reflect.String:
		if !utf8.ValidString(value.String()) || len(value.String()) > l1MaxJSONTextBytes {
			return errors.New("l1 metadata string is invalid or oversized")
		}
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
	case reflect.Float32, reflect.Float64:
		if math.IsNaN(value.Float()) || math.IsInf(value.Float(), 0) {
			return errors.New("l1 metadata contains a non-finite number")
		}
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		if value.Type().Key().Kind() != reflect.String || value.Len() > l1MaxCollectionEntries {
			return errors.New("l1 metadata map shape or size is invalid")
		}
		iter := value.MapRange()
		for iter.Next() {
			key := iter.Key().String()
			if !utf8.ValidString(key) || len(key) > l1MaxJSONTextBytes {
				return errors.New("l1 metadata key is invalid or oversized")
			}
			if err := validateL1GoJSONValue(iter.Value(), depth+1, nodes+1); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return nil
		}
		if value.Len() > l1MaxCollectionEntries {
			return errors.New("l1 metadata array is oversized")
		}
		for index := 0; index < value.Len(); index++ {
			if err := validateL1GoJSONValue(value.Index(index), depth+1, nodes+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			field := value.Field(index)
			if value.Type().Field(index).PkgPath != "" {
				continue
			}
			if err := validateL1GoJSONValue(field, depth+1, nodes+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("l1 metadata contains a non-JSON value")
	}
	return nil
}

func (dto l1SearchCacheDTO) domain() (*l1sqlite.L1SearchCacheEntry, error) {
	entry := &l1sqlite.L1SearchCacheEntry{
		QueryHash: dto.QueryHash, NormalizedQuery: dto.NormalizedQuery, Provider: dto.Provider,
		RawQuery: dto.RawQuery, ResultsJSON: dto.ResultsJSON, SourceURLs: dto.SourceURLs,
		RetrievedAt: dto.RetrievedAt, ExpiresAt: dto.ExpiresAt, CreatedAt: dto.CreatedAt, UpdatedAt: dto.UpdatedAt,
	}
	if !validL1SearchCacheEntry(entry) {
		return nil, errors.New("l1 search cache result is invalid")
	}
	return entry, nil
}

func l1SearchCacheFromDomain(entry *l1sqlite.L1SearchCacheEntry) (*l1SearchCacheDTO, error) {
	if entry == nil {
		return nil, nil
	}
	if !validL1SearchCacheEntry(entry) {
		return nil, errors.New("l1 search cache owner result is invalid")
	}
	return &l1SearchCacheDTO{
		QueryHash: entry.QueryHash, NormalizedQuery: entry.NormalizedQuery, Provider: entry.Provider,
		RawQuery: entry.RawQuery, ResultsJSON: entry.ResultsJSON, SourceURLs: entry.SourceURLs,
		RetrievedAt: entry.RetrievedAt, ExpiresAt: entry.ExpiresAt, CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt,
	}, nil
}

func validL1SearchCacheEntry(entry *l1sqlite.L1SearchCacheEntry) bool {
	if entry == nil || !validL1Hex(entry.QueryHash, 64) || !validL1Text(entry.NormalizedQuery, l1MaxJSONTextBytes, true) ||
		!validL1Provider(entry.Provider, false) || !validL1Text(entry.RawQuery, l1MaxJSONTextBytes, true) ||
		len(entry.ResultsJSON) > l1MaxJSONValueBytes || len(entry.ResultsJSON) == 0 ||
		!validL1StringSlice(entry.SourceURLs, l1MaxSearchURLs, l1MaxJSONTextBytes) ||
		!validL1Time(entry.RetrievedAt, false) || !validL1Time(entry.ExpiresAt, false) ||
		!validL1Time(entry.CreatedAt, false) || !validL1Time(entry.UpdatedAt, false) ||
		entry.ExpiresAt.Before(entry.RetrievedAt) || entry.UpdatedAt.Before(entry.CreatedAt) {
		return false
	}
	return inspectL1JSON([]byte(entry.ResultsJSON), l1MaxJSONValueBytes, l1MaxJSONTextBytes, l1MaxJSONDepth, l1MaxJSONNodes, l1MaxCollectionEntries, false) == nil
}

func (dto l1KnowledgeItemDTO) domain() (l1sqlite.L1KnowledgeItem, error) {
	meta, err := l1ObjectFromRaw(dto.Meta)
	if err != nil {
		return l1sqlite.L1KnowledgeItem{}, err
	}
	item := l1sqlite.L1KnowledgeItem{
		ID: dto.ID, StagingID: dto.StagingID, Domain: dto.Domain, Title: dto.Title, SourceID: dto.SourceID,
		SourceURL: dto.SourceURL, RawText: dto.RawText, RawHash: dto.RawHash, SummaryDraft: dto.SummaryDraft,
		Keywords: dto.Keywords, LicenseNote: dto.LicenseNote, Meta: meta, CreatedAt: dto.CreatedAt, UpdatedAt: dto.UpdatedAt,
	}
	if !validL1KnowledgeItem(item) {
		return l1sqlite.L1KnowledgeItem{}, errors.New("l1 knowledge result is invalid")
	}
	return item, nil
}

func l1KnowledgeFromDomain(item l1sqlite.L1KnowledgeItem) (l1KnowledgeItemDTO, error) {
	meta, err := l1ObjectToRaw(item.Meta)
	if err != nil {
		return l1KnowledgeItemDTO{}, err
	}
	dto := l1KnowledgeItemDTO{
		ID: item.ID, StagingID: item.StagingID, Domain: item.Domain, Title: item.Title, SourceID: item.SourceID,
		SourceURL: item.SourceURL, RawText: item.RawText, RawHash: item.RawHash, SummaryDraft: item.SummaryDraft,
		Keywords: item.Keywords, LicenseNote: item.LicenseNote, Meta: meta, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
	if !validL1KnowledgeItem(item) {
		return l1KnowledgeItemDTO{}, errors.New("l1 knowledge owner result is invalid")
	}
	return dto, nil
}

func validL1KnowledgeItem(item l1sqlite.L1KnowledgeItem) bool {
	return validL1Text(item.ID, 512, true) && validL1Text(item.StagingID, 512, false) && validL1Label(item.Domain) &&
		validL1Text(item.Title, l1MaxJSONTextBytes, false) && validL1Text(item.SourceID, 512, false) &&
		validL1Text(item.SourceURL, 8<<10, false) && validL1Text(item.RawText, l1MaxTextBytes, false) &&
		validL1Text(item.RawHash, 256, false) && validL1Text(item.SummaryDraft, l1MaxJSONTextBytes, false) &&
		validL1StringSlice(item.Keywords, 512, 4<<10) && validL1Text(item.LicenseNote, 8<<10, false) &&
		validL1Time(item.CreatedAt, true) && validL1Time(item.UpdatedAt, true)
}

func (dto l1WikiPageDTO) domain() (l1sqlite.WikiPageIndexItem, error) {
	item := l1sqlite.WikiPageIndexItem{
		PageID: dto.PageID, Path: dto.Path, Title: dto.Title, Type: dto.Type, Status: dto.Status,
		Owner: dto.Owner, CanonicalSource: dto.CanonicalSource, SourcePaths: dto.SourcePaths, Related: dto.Related,
		Summary: dto.Summary, ContentHash: dto.ContentHash, CreatedAt: dto.CreatedAt, UpdatedAt: dto.UpdatedAt,
	}
	if !validL1WikiPage(item) {
		return l1sqlite.WikiPageIndexItem{}, errors.New("l1 wiki result is invalid")
	}
	return item, nil
}

func l1WikiFromDomain(item l1sqlite.WikiPageIndexItem) (l1WikiPageDTO, error) {
	dto := l1WikiPageDTO{
		PageID: item.PageID, Path: item.Path, Title: item.Title, Type: item.Type, Status: item.Status,
		Owner: item.Owner, CanonicalSource: item.CanonicalSource, SourcePaths: item.SourcePaths, Related: item.Related,
		Summary: item.Summary, ContentHash: item.ContentHash, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
	if !validL1WikiPage(item) {
		return l1WikiPageDTO{}, errors.New("l1 wiki owner result is invalid")
	}
	return dto, nil
}

func validL1WikiPage(item l1sqlite.WikiPageIndexItem) bool {
	if !validL1Text(item.PageID, 512, true) || !validL1Text(item.Path, 4<<10, true) || !validL1Text(item.Title, l1MaxJSONTextBytes, true) ||
		!validL1Text(item.Type, 64, true) || !validL1Text(item.Status, 64, true) || !validL1Text(item.Owner, 256, false) ||
		!validL1Text(item.CanonicalSource, 4<<10, true) || !validL1StringSlice(item.SourcePaths, 1_000, 4<<10) ||
		!validL1StringSlice(item.Related, 1_000, 4<<10) || !validL1Text(item.Summary, l1MaxJSONTextBytes, false) ||
		!validL1Text(item.ContentHash, 256, false) || !validL1Time(item.CreatedAt, false) || !validL1Time(item.UpdatedAt, false) {
		return false
	}
	switch item.Type {
	case "index", "log", "concept", "module", "spec", "runbook":
	default:
		return false
	}
	switch item.Status {
	case l1sqlite.WikiPageStatusDraft, l1sqlite.WikiPageStatusActive:
	default:
		return false
	}
	return true
}

func (dto l1EventLogDTO) domain() (l1sqlite.L1EventLogEntry, error) {
	payload, err := l1ObjectFromRaw(dto.Payload)
	if err != nil {
		return l1sqlite.L1EventLogEntry{}, err
	}
	entry := l1sqlite.L1EventLogEntry{
		ID: dto.ID, EventType: dto.EventType, Namespace: dto.Namespace, SessionID: dto.SessionID,
		ThreadID: dto.ThreadID, ThreadSeq: dto.ThreadSeq, ThreadKind: dto.ThreadKind,
		Payload: payload, Source: dto.Source, CreatedAt: dto.CreatedAt,
	}
	if !validL1EventLogEntry(entry) {
		return l1sqlite.L1EventLogEntry{}, errors.New("l1 event result is invalid")
	}
	return entry, nil
}

func l1EventLogFromDomain(entry l1sqlite.L1EventLogEntry) (l1EventLogDTO, error) {
	payload, err := l1ObjectToRaw(entry.Payload)
	if err != nil {
		return l1EventLogDTO{}, err
	}
	dto := l1EventLogDTO{
		ID: entry.ID, EventType: entry.EventType, Namespace: entry.Namespace, SessionID: entry.SessionID,
		ThreadID: entry.ThreadID, ThreadSeq: entry.ThreadSeq, ThreadKind: entry.ThreadKind,
		Payload: payload, Source: entry.Source, CreatedAt: entry.CreatedAt,
	}
	if !validL1EventLogEntry(entry) {
		return l1EventLogDTO{}, errors.New("l1 event owner result is invalid")
	}
	return dto, nil
}

func validL1EventLogEntry(entry l1sqlite.L1EventLogEntry) bool {
	if !validL1Text(entry.ID, 2_048, true) || !validL1EventName(entry.EventType) || !validL1Namespace(entry.Namespace, false) ||
		!validL1SessionThreadTuple(entry.SessionID, entry.ThreadID, entry.ThreadSeq, entry.ThreadKind) ||
		!validL1Text(entry.Source, 128, true) || !validL1Time(entry.CreatedAt, false) {
		return false
	}
	_, err := l1ObjectToRaw(entry.Payload)
	return err == nil
}

func (dto l1MemoryEventDTO) domain() (l1sqlite.L1MemoryEvent, error) {
	meta, err := l1ObjectFromRaw(dto.Meta)
	if err != nil {
		return l1sqlite.L1MemoryEvent{}, err
	}
	event := l1sqlite.L1MemoryEvent{
		ID: dto.ID, Namespace: dto.Namespace, SessionID: dto.SessionID, ThreadID: dto.ThreadID,
		ThreadSeq: dto.ThreadSeq, ThreadKind: dto.ThreadKind, Speaker: dto.Speaker, Message: dto.Message,
		Meta: meta, MemoryState: dto.MemoryState, Layer: dto.Layer, Source: dto.Source,
		CreatedAt: dto.CreatedAt, UpdatedAt: dto.UpdatedAt,
	}
	if !validL1MemoryEvent(event) {
		return l1sqlite.L1MemoryEvent{}, errors.New("l1 memory result is invalid")
	}
	return event, nil
}

func l1MemoryEventFromDomain(event l1sqlite.L1MemoryEvent) (l1MemoryEventDTO, error) {
	meta, err := l1ObjectToRaw(event.Meta)
	if err != nil {
		return l1MemoryEventDTO{}, err
	}
	dto := l1MemoryEventDTO{
		ID: event.ID, Namespace: event.Namespace, SessionID: event.SessionID, ThreadID: event.ThreadID,
		ThreadSeq: event.ThreadSeq, ThreadKind: event.ThreadKind, Speaker: event.Speaker, Message: event.Message,
		Meta: meta, MemoryState: event.MemoryState, Layer: event.Layer, Source: event.Source,
		CreatedAt: event.CreatedAt, UpdatedAt: event.UpdatedAt,
	}
	if !validL1MemoryEvent(event) {
		return l1MemoryEventDTO{}, errors.New("l1 memory owner result is invalid")
	}
	return dto, nil
}

func validL1MemoryEvent(event l1sqlite.L1MemoryEvent) bool {
	if !validL1Text(event.ID, 2_048, true) || !validL1Namespace(event.Namespace, false) ||
		!validL1SessionThreadTuple(event.SessionID, event.ThreadID, event.ThreadSeq, event.ThreadKind) ||
		!validL1Text(string(event.Speaker), 128, true) || !validL1Text(event.Message, l1MaxTextBytes, true) ||
		!validL1MemoryState(event.MemoryState) || !validL1Text(event.Layer, 128, true) ||
		!validL1Text(event.Source, 256, true) || !validL1Time(event.CreatedAt, false) || !validL1Time(event.UpdatedAt, false) {
		return false
	}
	_, err := l1ObjectToRaw(event.Meta)
	return err == nil
}

func (dto l1RecallTraceDTO) domain() (domconv.RecallTrace, error) {
	trace := domconv.RecallTrace{
		TraceID: dto.TraceID, TurnID: dto.TurnID, RootTaskID: dto.RootTaskID,
		SessionID: dto.SessionID, OwnerID: dto.OwnerID, Role: dto.Role, CreatedAt: dto.CreatedAt,
	}
	if dto.Items != nil {
		trace.Items = make([]domconv.RecallTraceItem, len(dto.Items))
		for index, item := range dto.Items {
			trace.Items[index] = item.domain()
		}
	}
	if !validL1RecallTrace(trace) {
		return domconv.RecallTrace{}, errors.New("l1 recall trace is invalid")
	}
	return trace, nil
}

func l1RecallTraceFromDomain(trace domconv.RecallTrace) (l1RecallTraceDTO, error) {
	if !validL1RecallTrace(trace) {
		return l1RecallTraceDTO{}, errors.New("l1 recall trace owner result is invalid")
	}
	dto := l1RecallTraceDTO{
		TraceID: trace.TraceID, TurnID: trace.TurnID, RootTaskID: trace.RootTaskID,
		SessionID: trace.SessionID, OwnerID: trace.OwnerID, Role: trace.Role, CreatedAt: trace.CreatedAt,
	}
	if trace.Items != nil {
		dto.Items = make([]l1RecallTraceItemDTO, len(trace.Items))
		for index, item := range trace.Items {
			dto.Items[index] = l1RecallTraceItemFromDomain(item)
		}
	}
	return dto, nil
}

func (dto l1RecallTraceItemDTO) domain() domconv.RecallTraceItem {
	return domconv.RecallTraceItem{
		Layer: dto.Layer, Kind: dto.Kind, MemoryID: dto.MemoryID, SourceID: dto.SourceID,
		SourceType: dto.SourceType, Summary: dto.Summary, Query: dto.Query, Provider: dto.Provider,
		SourceURLs: dto.SourceURLs, RetrievedAt: dto.RetrievedAt, Score: dto.Score, Decision: dto.Decision,
		Status: dto.Status, Reason: dto.Reason, MemoryState: dto.MemoryState, Sensitivity: dto.Sensitivity,
		PromptSection: dto.PromptSection, TokenCount: dto.TokenCount, PromptIndex: dto.PromptIndex,
	}
}

func l1RecallTraceItemFromDomain(item domconv.RecallTraceItem) l1RecallTraceItemDTO {
	return l1RecallTraceItemDTO{
		Layer: item.Layer, Kind: item.Kind, MemoryID: item.MemoryID, SourceID: item.SourceID,
		SourceType: item.SourceType, Summary: item.Summary, Query: item.Query, Provider: item.Provider,
		SourceURLs: item.SourceURLs, RetrievedAt: item.RetrievedAt, Score: item.Score, Decision: item.Decision,
		Status: item.Status, Reason: item.Reason, MemoryState: item.MemoryState, Sensitivity: item.Sensitivity,
		PromptSection: item.PromptSection, TokenCount: item.TokenCount, PromptIndex: item.PromptIndex,
	}
}

func validL1RecallTrace(trace domconv.RecallTrace) bool {
	if trace.TraceID.Validate() != nil || trace.TurnID.Validate() != nil || trace.RootTaskID.Validate() != nil ||
		!validL1SessionID(trace.SessionID, false) || !validL1Text(trace.OwnerID, 256, false) ||
		!validL1Text(trace.Role, 128, false) || !validL1Time(trace.CreatedAt, true) || len(trace.Items) > l1MaxRecallItems {
		return false
	}
	for _, item := range trace.Items {
		if !validL1Text(item.Layer, 128, false) || !validL1Text(item.Kind, 128, false) ||
			!validL1Text(item.MemoryID, 512, false) || !validL1Text(item.SourceID, 512, false) ||
			!validL1Text(item.SourceType, 128, false) || !validL1Text(item.Summary, l1MaxJSONTextBytes, false) ||
			!validL1Text(item.Query, l1MaxJSONTextBytes, false) || !validL1Text(item.Provider, 128, false) ||
			!validL1StringSlice(item.SourceURLs, 32, 8<<10) || !validL1Time(item.RetrievedAt, true) ||
			math.IsNaN(float64(item.Score)) || math.IsInf(float64(item.Score), 0) ||
			!validL1Text(item.Decision, 128, false) || !validL1Text(item.Status, 128, false) ||
			!validL1Text(item.Reason, l1MaxJSONTextBytes, false) || !validL1Text(item.MemoryState, 128, false) ||
			!validL1Text(item.Sensitivity, 128, false) || !validL1Text(item.PromptSection, 128, false) ||
			item.TokenCount < 0 || item.TokenCount > 1_000_000 || item.PromptIndex < 0 || item.PromptIndex > 1_000_000 {
			return false
		}
	}
	return true
}

func validL1SessionID(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	return modulecore.SessionID(value).Validate() == nil
}

func validL1SessionThreadTuple(sessionID string, threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind) bool {
	if sessionID != "" && !validL1SessionID(sessionID, false) {
		return false
	}
	if threadID == "" {
		return threadSeq == 0 && threadKind == ""
	}
	return sessionID != "" && threadID.Validate() == nil && threadSeq.Validate() == nil && threadKind.Validate() == nil
}

func validL1Namespace(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	return validL1Text(value, 1_024, true) && strings.TrimSpace(value) == value && l1sqlite.ValidateL1Namespace(value) == nil
}

func validL1MemoryState(value string) bool {
	switch value {
	case l1sqlite.MemoryStateObserved, l1sqlite.MemoryStateCandidate, l1sqlite.MemoryStateConfirmed, l1sqlite.MemoryStatePinned:
		return true
	default:
		return false
	}
}

func validL1EventName(value string) bool {
	if len(value) == 0 || len(value) > 128 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, char := range value[1:] {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func validL1Provider(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	if len(value) > 128 {
		return false
	}
	first := value[0]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || (first >= '0' && first <= '9')) {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func validL1Label(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func validL1Limit(limit int) bool { return limit >= 0 && limit <= l1MaxListLimit }

func validL1Time(value time.Time, allowZero bool) bool {
	if value.IsZero() {
		return allowZero
	}
	_, err := value.MarshalJSON()
	return err == nil
}

func validL1Text(value string, maxBytes int, required bool) bool {
	if !utf8.ValidString(value) || len(value) > maxBytes || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	return !required || strings.TrimSpace(value) != ""
}

func validL1StringSlice(values []string, maxEntries, maxStringBytes int) bool {
	if len(values) > maxEntries {
		return false
	}
	for _, value := range values {
		if !validL1Text(value, maxStringBytes, true) {
			return false
		}
	}
	return true
}

func validL1Hex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, char := range value {
		if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') {
			continue
		}
		return false
	}
	return true
}
