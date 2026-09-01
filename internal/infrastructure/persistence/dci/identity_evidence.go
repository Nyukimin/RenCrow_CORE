package dci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	// IdentityEvidenceSchemaVersion identifies the bounded, read-only evidence
	// returned for one canonical DCI action.
	IdentityEvidenceSchemaVersion = "rencrow.dci.identity-evidence/v1"

	// MaxIdentityEvidenceEvents is the hard upper bound for one verified trace.
	// It is deliberately conservative and is applied before any unbounded
	// event or projection data can be retained by this verifier.
	MaxIdentityEvidenceEvents = 256

	identityEvidenceStatusPassed = "passed"
)

// IdentityEvidenceSearchReader is the narrow DCI owner lookup consumed by
// IdentityEvidenceVerifier.
type IdentityEvidenceSearchReader interface {
	FindSearchResultByActionID(context.Context, modulecore.ActionID) (domaindci.SearchResult, bool, error)
}

// IdentityEvidenceTraceReader is the bounded Event Store lookup consumed by
// IdentityEvidenceVerifier.
type IdentityEvidenceTraceReader interface {
	ListByTraceID(context.Context, modulecore.TraceID, int) ([]modulecore.EventEnvelope, error)
}

// IdentityEvidenceL1Reader is shared by the current and archive L1 owners.
type IdentityEvidenceL1Reader interface {
	FindStagingItemByNamespaceEventID(context.Context, string, string) (l1sqlite.L1StagingItem, bool, error)
}

// IdentityEvidenceVerifier checks one complete DCI action across its owner
// stores.  It retains no event, evidence, query, path, or projection data.
type IdentityEvidenceVerifier struct {
	search  IdentityEvidenceSearchReader
	events  IdentityEvidenceTraceReader
	current IdentityEvidenceL1Reader
	archive IdentityEvidenceL1Reader
}

// NewIdentityEvidenceVerifier constructs the read-only cross-owner verifier.
func NewIdentityEvidenceVerifier(search IdentityEvidenceSearchReader, events IdentityEvidenceTraceReader, current, archive IdentityEvidenceL1Reader) *IdentityEvidenceVerifier {
	return &IdentityEvidenceVerifier{search: search, events: events, current: current, archive: archive}
}

// IdentityEvidence is bounded, path-free evidence that one completed,
// authenticated DCI action has matching Event, current L1, and archive L1
// owner records.
type IdentityEvidence struct {
	SchemaVersion string `json:"schema_version"`
	Status        string `json:"status"`

	ActionID  modulecore.ActionID `json:"action_id"`
	TraceID   modulecore.TraceID  `json:"trace_id"`
	ActorKind string              `json:"actor_kind"`
	ActorID   string              `json:"actor_id"`

	SearchStatus           string `json:"search_status"`
	EventCount             int    `json:"event_count"`
	StepCount              int    `json:"step_count"`
	EvidenceCount          int    `json:"evidence_count"`
	CurrentProjectionCount int    `json:"current_projection_count"`
	ArchiveProjectionCount int    `json:"archive_projection_count"`
	EventGraphSHA256       string `json:"event_graph_sha256"`
}

// Validate checks the public evidence projection without reading any owner
// store.  It intentionally accepts only the successful terminal projection.
func (e IdentityEvidence) Validate() error {
	return ValidateIdentityEvidence(e)
}

// ValidateIdentityEvidence checks the public evidence projection without
// reading any owner store. It intentionally accepts only the successful
// terminal projection and never reports caller-controlled values.
func ValidateIdentityEvidence(e IdentityEvidence) error {
	if e.SchemaVersion != IdentityEvidenceSchemaVersion || e.Status != identityEvidenceStatusPassed {
		return errors.New("identity evidence status is invalid")
	}
	if err := e.ActionID.Validate(); err != nil {
		return errors.New("identity evidence action is invalid")
	}
	if err := e.TraceID.Validate(); err != nil {
		return errors.New("identity evidence trace is invalid")
	}
	if err := domaindci.ValidateActor(e.ActorKind, e.ActorID); err != nil {
		return errors.New("identity evidence actor is invalid")
	}
	if e.SearchStatus != "completed" {
		return errors.New("identity evidence search status is invalid")
	}
	if e.EventCount <= 0 || e.StepCount <= 0 || e.EvidenceCount <= 0 || e.CurrentProjectionCount <= 0 || e.ArchiveProjectionCount <= 0 || e.CurrentProjectionCount != e.EvidenceCount || e.ArchiveProjectionCount != e.EvidenceCount {
		return errors.New("identity evidence counts are invalid")
	}
	if e.EventCount > MaxIdentityEvidenceEvents || e.StepCount > MaxIdentityEvidenceEvents || e.EvidenceCount > MaxIdentityEvidenceEvents {
		return errors.New("identity evidence counts exceed the bound")
	}
	if expected := 3 + 2*e.StepCount + e.EvidenceCount; e.EventCount != expected || expected > MaxIdentityEvidenceEvents {
		return errors.New("identity evidence event count is inconsistent")
	}
	if !isIdentityEvidenceSHA256(e.EventGraphSHA256) {
		return errors.New("identity evidence graph hash is invalid")
	}
	return nil
}

// Verify reads one DCI result, its exact trace, and one current/archive L1
// projection per evidence.  Reader errors are intentionally reduced to a
// bounded code so private owner values cannot cross the evidence boundary.
func (v *IdentityEvidenceVerifier) VerifyAction(ctx context.Context, actionID modulecore.ActionID) (IdentityEvidence, error) {
	if ctx == nil {
		return IdentityEvidence{}, identityEvidenceError("context")
	}
	if err := ctx.Err(); err != nil {
		return IdentityEvidence{}, identityEvidenceError("context")
	}
	if err := actionID.Validate(); err != nil {
		return IdentityEvidence{}, identityEvidenceError("action")
	}
	if v == nil || v.search == nil || v.events == nil || v.current == nil || v.archive == nil {
		return IdentityEvidence{}, identityEvidenceError("reader")
	}

	result, found, err := v.search.FindSearchResultByActionID(ctx, actionID)
	if err != nil {
		return IdentityEvidence{}, identityEvidenceError("dci_read")
	}
	if !found {
		return IdentityEvidence{}, identityEvidenceError("dci_missing")
	}
	if err := ctx.Err(); err != nil {
		return IdentityEvidence{}, identityEvidenceError("context")
	}
	if err := validateIdentityEvidenceResult(result, actionID); err != nil {
		return IdentityEvidence{}, err
	}

	traceEvents, err := v.events.ListByTraceID(ctx, result.Trace.TraceID, MaxIdentityEvidenceEvents)
	if err != nil {
		return IdentityEvidence{}, identityEvidenceError("event_read")
	}
	if len(traceEvents) > MaxIdentityEvidenceEvents {
		return IdentityEvidence{}, identityEvidenceError("event_bound")
	}
	graphSHA, err := verifyIdentityEvidenceEvents(ctx, result, traceEvents)
	if err != nil {
		return IdentityEvidence{}, err
	}

	for _, evidence := range result.Pack.Evidence {
		if err := ctx.Err(); err != nil {
			return IdentityEvidence{}, identityEvidenceError("context")
		}
		current, found, err := v.current.FindStagingItemByNamespaceEventID(ctx, "kb:dci", string(evidence.CreatedByEventID))
		if err != nil {
			return IdentityEvidence{}, identityEvidenceError("current_l1_read")
		}
		if !found {
			return IdentityEvidence{}, identityEvidenceError("current_l1_missing")
		}
		archive, found, err := v.archive.FindStagingItemByNamespaceEventID(ctx, "kb:dci", string(evidence.CreatedByEventID))
		if err != nil {
			return IdentityEvidence{}, identityEvidenceError("archive_l1_read")
		}
		if !found {
			return IdentityEvidence{}, identityEvidenceError("archive_l1_missing")
		}
		if err := validateIdentityEvidenceProjection(current, evidence, result.Trace.ActionID, result.Trace.TraceID); err != nil {
			return IdentityEvidence{}, err
		}
		if err := validateIdentityEvidenceProjection(archive, evidence, result.Trace.ActionID, result.Trace.TraceID); err != nil {
			return IdentityEvidence{}, err
		}
		if !sameIdentityEvidenceProjection(current, archive) {
			return IdentityEvidence{}, identityEvidenceError("l1_projection_mismatch")
		}
	}
	if err := ctx.Err(); err != nil {
		return IdentityEvidence{}, identityEvidenceError("context")
	}

	evidence := IdentityEvidence{
		SchemaVersion:          IdentityEvidenceSchemaVersion,
		Status:                 identityEvidenceStatusPassed,
		ActionID:               result.Trace.ActionID,
		TraceID:                result.Trace.TraceID,
		ActorKind:              result.Trace.ActorKind,
		ActorID:                result.Trace.ActorID,
		SearchStatus:           result.Trace.Status,
		EventCount:             len(traceEvents),
		StepCount:              len(result.Trace.Steps),
		EvidenceCount:          len(result.Pack.Evidence),
		CurrentProjectionCount: len(result.Pack.Evidence),
		ArchiveProjectionCount: len(result.Pack.Evidence),
		EventGraphSHA256:       graphSHA,
	}
	if err := evidence.Validate(); err != nil {
		return IdentityEvidence{}, identityEvidenceError("evidence_projection")
	}
	return evidence, nil
}

func validateIdentityEvidenceResult(result domaindci.SearchResult, actionID modulecore.ActionID) error {
	if len(result.Trace.Steps) > MaxIdentityEvidenceEvents || len(result.Pack.Evidence) > MaxIdentityEvidenceEvents {
		return identityEvidenceError("result_bound")
	}
	if err := domaindci.ValidateStoredSearchResult(result); err != nil {
		return identityEvidenceError("dci_result")
	}
	if result.Trace.ActionID != actionID || result.Pack.ActionID != actionID {
		return identityEvidenceError("action_mismatch")
	}
	if result.Trace.ActorAttribution != domaindci.ActorAttributionAuthenticated || domaindci.ValidateActor(result.Trace.ActorKind, result.Trace.ActorID) != nil {
		return identityEvidenceError("actor")
	}
	if result.Trace.Mode != "dci" || result.Trace.Status != "completed" || result.Trace.ErrorMessage != "" || len(result.Pack.Evidence) == 0 || result.Trace.FinalEvidenceCount != len(result.Pack.Evidence) {
		return identityEvidenceError("result_status")
	}
	if len(result.Trace.Steps) == 0 {
		return identityEvidenceError("steps")
	}
	return nil
}

func verifyIdentityEvidenceEvents(ctx context.Context, result domaindci.SearchResult, events []modulecore.EventEnvelope) (string, error) {
	if len(events) == 0 {
		return "", identityEvidenceError("event_missing")
	}
	if expected := 3 + 2*len(result.Trace.Steps) + len(result.Pack.Evidence); len(events) != expected || expected > MaxIdentityEvidenceEvents {
		return "", identityEvidenceError("event_count")
	}
	if err := modulecore.ValidateEventEnvelopeGraph(events); err != nil {
		return "", identityEvidenceError("event_graph")
	}

	byID := make(map[modulecore.EventID]modulecore.EventEnvelope, len(events))
	byType := make(map[string][]modulecore.EventEnvelope)
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return "", identityEvidenceError("context")
		}
		if event.ComponentID != "dci" || event.TraceID != result.Trace.TraceID || event.ActionID != result.Trace.ActionID || event.ActorKind != result.Trace.ActorKind || event.ActorID != result.Trace.ActorID {
			return "", identityEvidenceError("event_binding")
		}
		if _, exists := byID[event.EventID]; exists {
			return "", identityEvidenceError("event_duplicate")
		}
		byID[event.EventID] = event
		switch event.EventType {
		case "dci.search.requested", "dci.search.started", "dci.source.selected", "dci.file.read", "dci.evidence.created", "dci.search.completed":
			byType[event.EventType] = append(byType[event.EventType], event)
		default:
			return "", identityEvidenceError("event_type")
		}
		if event.EventType != "dci.evidence.created" && event.EvidenceID != "" {
			return "", identityEvidenceError("event_evidence_binding")
		}
	}
	requested := byType["dci.search.requested"]
	started := byType["dci.search.started"]
	selected := byType["dci.source.selected"]
	reads := byType["dci.file.read"]
	evidenceEvents := byType["dci.evidence.created"]
	terminals := byType["dci.search.completed"]
	if len(requested) != 1 || len(started) != 1 || len(selected) != len(result.Trace.Steps) || len(reads) != len(result.Trace.Steps) || len(evidenceEvents) != len(result.Pack.Evidence) || len(terminals) != 1 {
		return "", identityEvidenceError("event_type_count")
	}
	if requested[0].CausationEventID != "" || len(requested[0].DependencyEventIDs) != 0 || started[0].CausationEventID != requested[0].EventID || len(started[0].DependencyEventIDs) != 0 {
		return "", identityEvidenceError("event_root")
	}
	if !identityEvidencePayloadHasOnly(requested[0].Payload, "query") || !identityEvidencePayloadHasString(requested[0].Payload, "query", result.Pack.Query) {
		return "", identityEvidenceError("requested_payload")
	}
	if !identityEvidencePayloadHasOnly(started[0].Payload, "query") || !identityEvidencePayloadHasString(started[0].Payload, "query", result.Pack.Query) {
		return "", identityEvidenceError("started_payload")
	}

	readByID := make(map[modulecore.EventID]modulecore.EventEnvelope, len(reads))
	selectedByReadID := make(map[modulecore.EventID]modulecore.EventEnvelope, len(reads))
	readStepIndex := make(map[modulecore.EventID]int, len(reads))
	selectedEventIDs := make(map[modulecore.EventID]struct{}, len(selected))
	for stepIndex, step := range result.Trace.Steps {
		if err := ctx.Err(); err != nil {
			return "", identityEvidenceError("context")
		}
		if step.StepNo != stepIndex+1 {
			return "", identityEvidenceError("step_order")
		}
		read, ok := byID[step.EventID]
		if !ok || read.EventType != "dci.file.read" || read.EventID == "" || read.CausationEventID == "" {
			return "", identityEvidenceError("step_event")
		}
		if _, exists := readByID[read.EventID]; exists {
			return "", identityEvidenceError("step_duplicate")
		}
		readByID[read.EventID] = read
		selectedEvent, ok := byID[read.CausationEventID]
		if !ok || selectedEvent.EventType != "dci.source.selected" || len(selectedEvent.DependencyEventIDs) != 0 {
			return "", identityEvidenceError("source_binding")
		}
		if _, exists := selectedByReadID[read.EventID]; exists {
			return "", identityEvidenceError("source_duplicate")
		}
		if _, exists := selectedEventIDs[selectedEvent.EventID]; exists {
			return "", identityEvidenceError("source_duplicate")
		}
		selectedByReadID[read.EventID] = selectedEvent
		selectedEventIDs[selectedEvent.EventID] = struct{}{}
		readStepIndex[read.EventID] = stepIndex
		if len(read.DependencyEventIDs) != 0 {
			return "", identityEvidenceError("read_dependencies")
		}
		if !identityEvidencePayloadHasOnly(selectedEvent.Payload, "file_path") || !identityEvidencePayloadHasString(selectedEvent.Payload, "file_path", step.FilePath) {
			return "", identityEvidenceError("source_payload")
		}
		if !identityEvidencePayloadHasOnly(read.Payload, "file_path", "status", "result_count", "error") ||
			!identityEvidencePayloadHasString(read.Payload, "file_path", step.FilePath) ||
			!identityEvidencePayloadHasString(read.Payload, "status", step.Status) ||
			!identityEvidencePayloadHasInt(read.Payload, "result_count", step.ResultCount) ||
			!identityEvidencePayloadHasString(read.Payload, "error", step.ErrorMessage) {
			return "", identityEvidenceError("read_payload")
		}
	}
	if len(selectedByReadID) != len(readByID) || len(selectedEventIDs) != len(selected) {
		return "", identityEvidenceError("source_count")
	}

	createdByID := make(map[modulecore.EventID]modulecore.EventEnvelope, len(evidenceEvents))
	evidenceEventIDsByReadID := make(map[modulecore.EventID][]modulecore.EventID, len(readByID))
	lastEvidenceStepIndex := -1
	for _, evidence := range result.Pack.Evidence {
		created, ok := byID[evidence.CreatedByEventID]
		if !ok || created.EventType != "dci.evidence.created" || created.EvidenceID != evidence.EvidenceID || created.CausationEventID == "" || len(created.DependencyEventIDs) != 0 {
			return "", identityEvidenceError("evidence_event")
		}
		if _, exists := createdByID[created.EventID]; exists {
			return "", identityEvidenceError("evidence_duplicate")
		}
		if _, ok := readByID[created.CausationEventID]; !ok {
			return "", identityEvidenceError("evidence_read_binding")
		}
		stepIndex := readStepIndex[created.CausationEventID]
		if stepIndex < lastEvidenceStepIndex {
			return "", identityEvidenceError("evidence_order")
		}
		lastEvidenceStepIndex = stepIndex
		createdByID[created.EventID] = created
		evidenceEventIDsByReadID[created.CausationEventID] = append(evidenceEventIDsByReadID[created.CausationEventID], created.EventID)
		if !identityEvidencePayloadHasOnly(created.Payload, "file_path", "line_start", "line_end", "snippet", "source_id", "reason", "confidence") ||
			!identityEvidencePayloadHasString(created.Payload, "file_path", evidence.FilePath) ||
			!identityEvidencePayloadHasInt(created.Payload, "line_start", evidence.LineStart) ||
			!identityEvidencePayloadHasInt(created.Payload, "line_end", evidence.LineEnd) ||
			!identityEvidencePayloadHasString(created.Payload, "snippet", evidence.Snippet) ||
			!identityEvidencePayloadHasString(created.Payload, "source_id", evidence.SourceID) ||
			!identityEvidencePayloadHasString(created.Payload, "reason", evidence.Reason) ||
			!identityEvidencePayloadHasFloat(created.Payload, "confidence", evidence.Confidence) {
			return "", identityEvidenceError("evidence_payload")
		}
	}
	if len(createdByID) != len(evidenceEvents) {
		return "", identityEvidenceError("evidence_count")
	}

	// The selected/read chain is the canonical sequential DCI route: the
	// first source selection follows started, and each later selection follows
	// the previous step's final evidence in pack order, or its read when that
	// step produced no evidence.
	expectedSelectionCause := started[0].EventID
	for _, step := range result.Trace.Steps {
		read := readByID[step.EventID]
		selectedEvent := selectedByReadID[read.EventID]
		if selectedEvent.CausationEventID != expectedSelectionCause {
			return "", identityEvidenceError("source_chain")
		}
		expectedSelectionCause = read.EventID
		if evidenceIDs := evidenceEventIDsByReadID[read.EventID]; len(evidenceIDs) > 0 {
			expectedSelectionCause = evidenceIDs[len(evidenceIDs)-1]
		}
	}

	terminal := terminals[0]
	if !identityEvidencePayloadHasOnly(terminal.Payload, "status", "evidence_count", "limitations") ||
		!identityEvidencePayloadHasString(terminal.Payload, "status", "completed") ||
		!identityEvidencePayloadHasInt(terminal.Payload, "evidence_count", len(result.Pack.Evidence)) ||
		!identityEvidencePayloadHasStringSlice(terminal.Payload, "limitations", result.Pack.Limitations) {
		return "", identityEvidenceError("terminal_payload")
	}
	if len(terminal.DependencyEventIDs) != 0 && len(result.Pack.Evidence) <= 1 {
		return "", identityEvidenceError("terminal_dependencies")
	}
	if len(result.Pack.Evidence) > 0 {
		lastEvidenceID := result.Pack.Evidence[len(result.Pack.Evidence)-1].CreatedByEventID
		if terminal.CausationEventID != lastEvidenceID {
			return "", identityEvidenceError("terminal_cause")
		}
		expectedDependencies := make([]modulecore.EventID, 0, len(result.Pack.Evidence)-1)
		for _, evidence := range result.Pack.Evidence[:len(result.Pack.Evidence)-1] {
			expectedDependencies = append(expectedDependencies, evidence.CreatedByEventID)
		}
		sort.Slice(expectedDependencies, func(left, right int) bool { return expectedDependencies[left] < expectedDependencies[right] })
		if !sameIdentityEvidenceEventIDs(terminal.DependencyEventIDs, expectedDependencies) {
			return "", identityEvidenceError("terminal_join")
		}
	} else {
		return "", identityEvidenceError("evidence_missing")
	}

	return identityEvidenceGraphSHA256(events)
}

func validateIdentityEvidenceProjection(item l1sqlite.L1StagingItem, evidence domaindci.Evidence, actionID modulecore.ActionID, traceID modulecore.TraceID) error {
	if item.ID == "" || item.Kind != l1sqlite.L1StagingKindSearchResult || item.Namespace != "kb:dci" || item.EventID != string(evidence.CreatedByEventID) || item.SourceID != dciSourceID(evidence.FilePath) || item.SourceURL != dciSyntheticSourceURL(evidence.FilePath) || item.RawText != evidence.Snippet {
		return identityEvidenceError("l1_projection")
	}
	rawHash := sha256.Sum256([]byte(item.RawText))
	if item.RawHash != hex.EncodeToString(rawHash[:]) || !isIdentityEvidenceSHA256(item.RawHash) {
		return identityEvidenceError("l1_raw_hash")
	}
	if item.Meta == nil ||
		!identityEvidenceMetaString(item.Meta, "source_kind", "dci") ||
		!identityEvidenceMetaString(item.Meta, "search_action_id", string(actionID)) ||
		!identityEvidenceMetaString(item.Meta, "trace_id", string(traceID)) ||
		!identityEvidenceMetaString(item.Meta, "evidence_id", string(evidence.EvidenceID)) ||
		!identityEvidenceMetaString(item.Meta, "evidence_created_event_id", string(evidence.CreatedByEventID)) {
		return identityEvidenceError("l1_metadata")
	}
	return nil
}

func sameIdentityEvidenceProjection(left, right l1sqlite.L1StagingItem) bool {
	return reflect.DeepEqual(left, right)
}

func identityEvidenceGraphSHA256(events []modulecore.EventEnvelope) (string, error) {
	ordered := append([]modulecore.EventEnvelope(nil), events...)
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].OccurredAt.Equal(ordered[right].OccurredAt) {
			return ordered[left].EventID < ordered[right].EventID
		}
		return ordered[left].OccurredAt.Before(ordered[right].OccurredAt)
	})
	hash := sha256.New()
	for _, event := range ordered {
		encoded, err := json.Marshal(event)
		if err != nil {
			return "", identityEvidenceError("event_encode")
		}
		if _, err := hash.Write(encoded); err != nil {
			return "", identityEvidenceError("event_hash")
		}
		if _, err := hash.Write([]byte{'\n'}); err != nil {
			return "", identityEvidenceError("event_hash")
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func identityEvidencePayloadHasOnly(payload map[string]any, keys ...string) bool {
	if len(payload) != len(keys) {
		return false
	}
	allowed := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		allowed[key] = struct{}{}
	}
	for key := range payload {
		if _, ok := allowed[key]; !ok {
			return false
		}
	}
	return true
}

func identityEvidencePayloadHasString(payload map[string]any, key, expected string) bool {
	actual, ok := payload[key].(string)
	return ok && actual == expected
}

func identityEvidencePayloadHasInt(payload map[string]any, key string, expected int) bool {
	actual, ok := identityEvidenceInt(payload[key])
	return ok && actual == expected
}

func identityEvidencePayloadHasFloat(payload map[string]any, key string, expected float64) bool {
	actual, ok := identityEvidenceFloat(payload[key])
	return ok && actual == expected
}

func identityEvidencePayloadHasStringSlice(payload map[string]any, key string, expected []string) bool {
	value, ok := payload[key]
	if !ok {
		return false
	}
	if value == nil {
		return len(expected) == 0
	}
	var actual []string
	switch values := value.(type) {
	case []string:
		actual = append([]string(nil), values...)
	case []any:
		actual = make([]string, len(values))
		for index, value := range values {
			stringValue, ok := value.(string)
			if !ok {
				return false
			}
			actual[index] = stringValue
		}
	default:
		return false
	}
	if len(actual) != len(expected) {
		return false
	}
	for index := range expected {
		if actual[index] != expected[index] {
			return false
		}
	}
	return true
}

func identityEvidenceInt(value any) (int, bool) {
	var number int64
	switch value := value.(type) {
	case int:
		return value, true
	case int8:
		return int(value), true
	case int16:
		return int(value), true
	case int32:
		return int(value), true
	case int64:
		number = value
	case uint:
		if uint64(value) > uint64(^uint(0)>>1) {
			return 0, false
		}
		return int(value), true
	case uint8:
		return int(value), true
	case uint16:
		return int(value), true
	case uint32:
		if uint64(value) > uint64(^uint(0)>>1) {
			return 0, false
		}
		return int(value), true
	case uint64:
		if value > uint64(^uint(0)>>1) {
			return 0, false
		}
		return int(value), true
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) || value > float64(^uint(0)>>1) || value < -float64(^uint(0)>>1)-1 {
			return 0, false
		}
		return int(value), true
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		return 0, false
	}
	if int64(int(number)) != number {
		return 0, false
	}
	return int(number), true
}

func identityEvidenceFloat(value any) (float64, bool) {
	switch value := value.(type) {
	case float64:
		return value, !math.IsNaN(value) && !math.IsInf(value, 0)
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		parsed, err := value.Float64()
		return parsed, err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
	default:
		return 0, false
	}
}

func identityEvidenceMetaString(meta map[string]interface{}, key, expected string) bool {
	actual, ok := meta[key].(string)
	return ok && actual == expected
}

func sameIdentityEvidenceEventIDs(left, right []modulecore.EventID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func isIdentityEvidenceSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func identityEvidenceError(code string) error {
	return fmt.Errorf("dci identity evidence: %s", code)
}
