package classifierlab

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

const (
	MaxReplayCases     = 100000
	MaxReplayLineBytes = 1 << 20
)

func replayRouteNames() []string {
	names := make([]string, len(routeLabels))
	for index, label := range routeLabels {
		names[index] = string(label)
	}
	return names
}

// ReplayRouteNames returns a copy of the fixed route vocabulary for reports
// and fixtures that need to assert complete per-route support.
func ReplayRouteNames() []string { return replayRouteNames() }

// ReplayCase is one source JSONL record. The corpus remains the source of
// messages; prediction records intentionally retain only case IDs and hashes.
type ReplayCase struct {
	CaseID         string                     `json:"case_id"`
	Origin         string                     `json:"origin"`
	GroupID        string                     `json:"group_id"`
	Split          string                     `json:"split"`
	Message        string                     `json:"message"`
	TurnFixture    map[string]json.RawMessage `json:"turn_fixture"`
	ExpectedRoutes []string                   `json:"expected_routes"`
	LabelStatus    string                     `json:"label_status"`
	NeedsContext   bool                       `json:"needs_context"`
	RiskTags       []string                   `json:"risk_tags"`
	AnnotationNote string                     `json:"annotation_note,omitempty"`
}

// ReplayTurnFixture contains the allowlisted TurnInput metadata that can be
// reconstructed without conversation history or ingress identity.
type ReplayTurnFixture struct {
	SessionID       string
	ViewerRecipient string
}

func ParseReplayTurnFixture(fields map[string]json.RawMessage) (ReplayTurnFixture, error) {
	fixture := ReplayTurnFixture{}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		raw := fields[key]
		if len(raw) == 0 || len(raw) > 1024 {
			return ReplayTurnFixture{}, fmt.Errorf("turn_fixture.%s is empty or exceeds 1024 bytes", key)
		}
		value, err := decodePreparedJSON[string](raw)
		if err != nil || value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") || len(value) > 256 {
			return ReplayTurnFixture{}, fmt.Errorf("turn_fixture.%s must be a bounded normalized string", key)
		}
		switch key {
		case "session_id":
			fixture.SessionID = value
		case "viewer_recipient":
			fixture.ViewerRecipient = value
		default:
			return ReplayTurnFixture{}, fmt.Errorf("unsupported turn_fixture key %q", key)
		}
	}
	return fixture, nil
}

// ReplayPrediction is the privacy-bounded per-case output contract described
// by the classifier experiment spec. Null fields mean the variant did not
// produce that observation; error/abstain never becomes a CHAT prediction.
type ReplayPrediction struct {
	CaseID                 string             `json:"case_id"`
	GroupID                string             `json:"group_id"`
	Origin                 string             `json:"origin"`
	Split                  string             `json:"split"`
	Variant                string             `json:"variant"`
	Repetition             int                `json:"repetition"`
	RiskTags               []string           `json:"risk_tags"`
	RuleMatched            *bool              `json:"rule_matched"`
	ClassifierReached      *bool              `json:"classifier_reached"`
	ExpectedRoutes         []string           `json:"expected_routes"`
	LabelStatus            string             `json:"label_status"`
	NeedsContext           bool               `json:"needs_context"`
	LegacyClassifiedRoute  *string            `json:"legacy_classified_route"`
	LegacyConfidence       *float64           `json:"legacy_confidence"`
	LegacyErrorCode        *string            `json:"legacy_error_code"`
	CandidateSelectedRoute *string            `json:"candidate_selected_route"`
	CandidateProbabilities map[string]float64 `json:"candidate_probabilities"`
	CandidateSelectedProb  *float64           `json:"candidate_selected_probability"`
	CandidateStatus        *string            `json:"candidate_status"`
	CandidateReasonCode    *string            `json:"candidate_reason_code,omitempty"`
	CandidateErrorCode     *string            `json:"candidate_error_code"`
	FallbackUsed           *bool              `json:"fallback_used"`
	FinalRoute             *string            `json:"final_route"`
	LegacyClassifyWallMS   *float64           `json:"legacy_classify_wall_ms"`
	CandidateCallWallMS    *float64           `json:"candidate_call_wall_ms"`
	CandidateServerMS      *float64           `json:"candidate_server_ms"`
	ShadowQueueWaitMS      *float64           `json:"shadow_queue_wait_ms"`
	DecisionWallMS         *float64           `json:"decision_wall_ms"`
	MioTTFTMS              *float64           `json:"mio_ttft_ms"`
	MioFinalMS             *float64           `json:"mio_final_ms"`
	NextTurnReadyMS        *float64           `json:"next_turn_ready_ms"`
	ManifestID             string             `json:"manifest_id"`
}

const MaxReplayPredictions = 200000

// HybridReplayResult contains only the observed decisions and stable error
// codes. It deliberately excludes the input text and raw engine errors.
type HybridReplayResult struct {
	CandidateStatus     CandidateStatus
	CandidateReasonCode string
	CandidateRoute      *routing.Route
	CandidateErrorCode  string
	CandidateWallMS     float64
	LegacyRoute         *routing.Route
	LegacyConfidence    *float64
	LegacyErrorCode     string
	LegacyWallMS        float64
	FallbackUsed        bool
	FinalRoute          *routing.Route
}

// RunHybridReplay calls the candidate first and invokes legacy synchronously
// only when the candidate errors, abstains, or fails the caller's explicit
// calibrated acceptance predicate. A nil predicate or engine fails closed.
func RunHybridReplay(ctx context.Context, candidate CandidateClassifier, legacy agent.Classifier,
	input CandidateInput, turn conversation.TurnInput, accept func(CandidateResult) bool) (HybridReplayResult, error) {
	if ctx == nil || candidate == nil || legacy == nil || accept == nil {
		return HybridReplayResult{}, errors.New("hybrid replay requires context, candidate, legacy, and acceptance predicate")
	}
	result := HybridReplayResult{}
	candidateStarted := time.Now()
	candidateOutput, candidateErr := candidate.ClassifyCandidate(ctx, input)
	result.CandidateWallMS = float64(time.Since(candidateStarted)) / float64(time.Millisecond)
	if candidateErr != nil {
		result.CandidateErrorCode = "CANDIDATE_ERROR"
	} else {
		result.CandidateStatus = candidateOutput.Status
		if validMetricCode(candidateOutput.ReasonCode) {
			result.CandidateReasonCode = candidateOutput.ReasonCode
		}
		if candidateOutput.Status == CandidateClassified && candidateOutput.Route != nil && isReplayRoute(string(*candidateOutput.Route)) {
			route := routing.Route(*candidateOutput.Route)
			result.CandidateRoute = &route
		}
	}
	if candidateErr == nil && candidateOutput.Status == CandidateClassified && result.CandidateRoute != nil && accept(candidateOutput) {
		final := *result.CandidateRoute
		result.FinalRoute = &final
		return result, nil
	}

	result.FallbackUsed = true
	legacyStarted := time.Now()
	legacyOutput, legacyErr := legacy.Classify(ctx, turn)
	result.LegacyWallMS = float64(time.Since(legacyStarted)) / float64(time.Millisecond)
	if legacyErr != nil || !isReplayRoute(legacyOutput.Route.String()) {
		result.LegacyErrorCode = "LEGACY_ERROR"
		return result, nil
	}
	legacyRoute := legacyOutput.Route
	confidence := legacyOutput.Confidence
	result.LegacyRoute = &legacyRoute
	result.LegacyConfidence = &confidence
	final := legacyRoute
	result.FinalRoute = &final
	return result, nil
}

// LoadReplayDataset reads strict, bounded UTF-8 JSONL and rejects group
// leakage across splits before a caller selects one split for replay.
func LoadReplayDataset(name string) ([]ReplayCase, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open replay dataset: %w", err)
	}
	defer file.Close()
	return DecodeReplayDataset(file)
}

func DecodeReplayDataset(reader io.Reader) ([]ReplayCase, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), MaxReplayLineBytes)
	cases := make([]ReplayCase, 0, 128)
	caseIDs := make(map[string]struct{})
	groupSplits := make(map[string]string)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Bytes()
		if len(line) == 0 || !utf8.Valid(line) {
			return nil, fmt.Errorf("dataset line %d is empty or invalid UTF-8", lineNumber)
		}
		item, err := decodePreparedJSON[ReplayCase](line)
		if err != nil {
			return nil, fmt.Errorf("decode dataset line %d: %w", lineNumber, err)
		}
		if err := validateReplayCase(item); err != nil {
			return nil, fmt.Errorf("validate dataset line %d (%q): %w", lineNumber, item.CaseID, err)
		}
		if _, exists := caseIDs[item.CaseID]; exists {
			return nil, fmt.Errorf("dataset contains duplicate case_id %q", item.CaseID)
		}
		caseIDs[item.CaseID] = struct{}{}
		if split, exists := groupSplits[item.GroupID]; exists && split != item.Split {
			return nil, fmt.Errorf("group_id %q leaks across splits %q and %q", item.GroupID, split, item.Split)
		}
		groupSplits[item.GroupID] = item.Split
		cases = append(cases, item)
		if len(cases) > MaxReplayCases {
			return nil, fmt.Errorf("dataset exceeds %d cases", MaxReplayCases)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read replay dataset: %w", err)
	}
	if len(cases) == 0 {
		return nil, errors.New("replay dataset has no cases")
	}
	return cases, nil
}

func validateReplayCase(item ReplayCase) error {
	if strings.TrimSpace(item.CaseID) == "" || len(item.CaseID) > 256 || strings.TrimSpace(item.GroupID) == "" {
		return errors.New("case_id and group_id are required")
	}
	if item.Origin != "synthetic" && item.Origin != "real_conversation" && item.Origin != "challenge" {
		return fmt.Errorf("unsupported origin %q", item.Origin)
	}
	switch item.Split {
	case "dev", "calibration", "test", "challenge":
	default:
		return fmt.Errorf("unsupported split %q", item.Split)
	}
	if strings.TrimSpace(item.Message) == "" {
		return errors.New("message must not be blank")
	}
	if _, err := ParseReplayTurnFixture(item.TurnFixture); err != nil {
		return err
	}
	if item.LabelStatus != "draft" && item.LabelStatus != "verified" {
		return fmt.Errorf("unsupported label_status %q", item.LabelStatus)
	}
	seenRoutes := make(map[string]struct{}, len(item.ExpectedRoutes))
	for _, route := range item.ExpectedRoutes {
		if !isReplayRoute(route) {
			return fmt.Errorf("unknown expected route %q", route)
		}
		if _, exists := seenRoutes[route]; exists {
			return fmt.Errorf("duplicate expected route %q", route)
		}
		seenRoutes[route] = struct{}{}
	}
	if item.LabelStatus == "verified" && len(item.ExpectedRoutes) == 0 && !item.NeedsContext {
		return errors.New("verified case requires expected_routes or needs_context=true")
	}
	for _, tag := range item.RiskTags {
		if strings.TrimSpace(tag) == "" {
			return errors.New("risk_tags must not contain empty values")
		}
	}
	return nil
}

// ReplayOrder returns the selected split in reproducible seeded order. Sorting
// case IDs before shuffling makes the order independent of JSONL row order.
func ReplayOrder(cases []ReplayCase, split string, seed int64) ([]ReplayCase, error) {
	if split == "" {
		return nil, errors.New("split is required")
	}
	selected := make([]ReplayCase, 0)
	for _, item := range cases {
		if item.Split == split {
			selected = append(selected, item)
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("dataset has no cases in split %q", split)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].CaseID < selected[j].CaseID })
	random := rand.New(rand.NewSource(seed))
	random.Shuffle(len(selected), func(i, j int) { selected[i], selected[j] = selected[j], selected[i] })
	return selected, nil
}

// ReplayRequestID creates a stable, non-sensitive request ID for one variant
// repetition without copying the message or source case ID into HTTP logs.
func ReplayRequestID(experimentID, caseID, variant string, repetition int) string {
	value := fmt.Sprintf("%s\x00%s\x00%s\x00%d", experimentID, caseID, variant, repetition)
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

// LoadReplayPredictions reads a bounded result stream and enforces unique
// case/variant/repetition identities before metrics consume it.
func LoadReplayPredictions(name string) ([]ReplayPrediction, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return DecodeReplayPredictions(file)
}

func DecodeReplayPredictions(reader io.Reader) ([]ReplayPrediction, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), MaxReplayLineBytes)
	records := make([]ReplayPrediction, 0, 128)
	identities := make(map[string]struct{})
	caseShape := make(map[string]ReplayPrediction)
	manifestID := ""
	groupSplits := make(map[string]string)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Bytes()
		if len(line) == 0 || !utf8.Valid(line) {
			return nil, fmt.Errorf("prediction line %d is empty or invalid UTF-8", lineNumber)
		}
		record, err := decodePreparedJSON[ReplayPrediction](line)
		if err != nil {
			return nil, fmt.Errorf("decode prediction line %d: %w", lineNumber, err)
		}
		if err := validateReplayPrediction(record); err != nil {
			return nil, fmt.Errorf("validate prediction line %d (%q): %w", lineNumber, record.CaseID, err)
		}
		if manifestID == "" {
			manifestID = record.ManifestID
		} else if record.ManifestID != manifestID {
			return nil, errors.New("prediction stream contains multiple manifest IDs")
		}
		identity := fmt.Sprintf("%s\x00%s\x00%d", record.CaseID, record.Variant, record.Repetition)
		if _, exists := identities[identity]; exists {
			return nil, fmt.Errorf("duplicate prediction identity for case %q, variant %q, repetition %d", record.CaseID, record.Variant, record.Repetition)
		}
		identities[identity] = struct{}{}
		if split, exists := groupSplits[record.GroupID]; exists && split != record.Split {
			return nil, fmt.Errorf("prediction group_id %q leaks across splits", record.GroupID)
		}
		groupSplits[record.GroupID] = record.Split
		if existing, exists := caseShape[record.CaseID]; exists {
			if existing.GroupID != record.GroupID || existing.Origin != record.Origin || existing.Split != record.Split || existing.LabelStatus != record.LabelStatus || existing.NeedsContext != record.NeedsContext || strings.Join(existing.ExpectedRoutes, "\x00") != strings.Join(record.ExpectedRoutes, "\x00") {
				return nil, fmt.Errorf("prediction variants disagree on source metadata for case %q", record.CaseID)
			}
		} else {
			caseShape[record.CaseID] = record
		}
		records = append(records, record)
		if len(records) > MaxReplayPredictions {
			return nil, fmt.Errorf("prediction stream exceeds %d rows", MaxReplayPredictions)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read prediction stream: %w", err)
	}
	if len(records) == 0 {
		return nil, errors.New("prediction stream is empty")
	}
	return records, nil
}

func validateReplayPrediction(record ReplayPrediction) error {
	if strings.TrimSpace(record.CaseID) == "" || strings.TrimSpace(record.GroupID) == "" || record.Repetition < 0 || record.ManifestID == "" {
		return errors.New("case_id, group_id, manifest_id, and nonnegative repetition are required")
	}
	if len(record.CaseID) > 256 || len(record.GroupID) > 256 || len(record.ManifestID) > 256 || strings.ContainsAny(record.CaseID+record.GroupID+record.ManifestID, "\r\n\x00") {
		return errors.New("prediction identifiers exceed limits or contain controls")
	}
	if record.Origin != "synthetic" && record.Origin != "real_conversation" && record.Origin != "challenge" {
		return fmt.Errorf("unsupported origin %q", record.Origin)
	}
	switch record.Split {
	case "dev", "calibration", "test", "challenge":
	default:
		return fmt.Errorf("unsupported split %q", record.Split)
	}
	switch record.Variant {
	case "legacy", "candidate", "hybrid-replay", "rules-only":
	default:
		return fmt.Errorf("unsupported variant %q", record.Variant)
	}
	if record.LabelStatus != "draft" && record.LabelStatus != "verified" {
		return fmt.Errorf("unsupported label_status %q", record.LabelStatus)
	}
	seen := make(map[string]struct{}, len(record.ExpectedRoutes))
	for _, route := range record.ExpectedRoutes {
		if !isReplayRoute(route) {
			return fmt.Errorf("unknown expected route %q", route)
		}
		if _, exists := seen[route]; exists {
			return fmt.Errorf("duplicate expected route %q", route)
		}
		seen[route] = struct{}{}
	}
	for name, route := range map[string]*string{
		"legacy_classified_route":  record.LegacyClassifiedRoute,
		"candidate_selected_route": record.CandidateSelectedRoute,
		"final_route":              record.FinalRoute,
	} {
		if route != nil && !isReplayRoute(*route) {
			return fmt.Errorf("unknown %s %q", name, *route)
		}
	}
	for route, probability := range record.CandidateProbabilities {
		if !isReplayRoute(route) || math.IsNaN(probability) || math.IsInf(probability, 0) || probability < 0 || probability > 1 {
			return fmt.Errorf("candidate probability for %q is invalid", route)
		}
	}
	if len(record.CandidateProbabilities) != 0 {
		if len(record.CandidateProbabilities) != len(routeLabels) {
			return errors.New("candidate probabilities do not contain the fixed route set")
		}
		var total float64
		for _, route := range routeLabels {
			probability, exists := record.CandidateProbabilities[string(route)]
			if !exists {
				return fmt.Errorf("candidate probability for %q is missing", route)
			}
			total += probability
		}
		if math.Abs(total-1) > 1e-4 {
			return errors.New("candidate probabilities do not sum to one")
		}
	}
	if record.CandidateSelectedProb != nil && (math.IsNaN(*record.CandidateSelectedProb) || math.IsInf(*record.CandidateSelectedProb, 0) || *record.CandidateSelectedProb < 0 || *record.CandidateSelectedProb > 1) {
		return errors.New("candidate selected probability is invalid")
	}
	if record.CandidateSelectedRoute != nil && record.CandidateSelectedProb != nil && len(record.CandidateProbabilities) > 0 {
		if probability, exists := record.CandidateProbabilities[*record.CandidateSelectedRoute]; !exists || math.Abs(probability-*record.CandidateSelectedProb) > 1e-4 {
			return errors.New("candidate selected probability does not match probability map")
		}
	}
	if record.LegacyConfidence != nil && (math.IsNaN(*record.LegacyConfidence) || math.IsInf(*record.LegacyConfidence, 0) || *record.LegacyConfidence < 0 || *record.LegacyConfidence > 1) {
		return errors.New("legacy confidence is invalid")
	}
	if record.CandidateStatus != nil && *record.CandidateStatus != "classified" && *record.CandidateStatus != "abstain" {
		return fmt.Errorf("unsupported candidate status %q", *record.CandidateStatus)
	}
	if record.CandidateStatus != nil && *record.CandidateStatus == "classified" && record.CandidateErrorCode == nil && record.CandidateSelectedRoute == nil {
		return errors.New("classified candidate result has no selected route")
	}
	if record.CandidateStatus != nil && *record.CandidateStatus == "classified" && record.CandidateErrorCode == nil && len(record.CandidateProbabilities) != len(routeLabels) {
		return errors.New("classified candidate result has no complete probability map")
	}
	if record.CandidateStatus != nil && *record.CandidateStatus == "abstain" && record.CandidateSelectedRoute != nil {
		return errors.New("abstained candidate result has a selected route")
	}
	if record.CandidateStatus != nil && *record.CandidateStatus == "abstain" && record.CandidateErrorCode == nil && len(record.CandidateProbabilities) != 0 {
		return errors.New("abstained candidate result has probabilities")
	}
	for name, value := range map[string]*string{
		"legacy_error_code":     record.LegacyErrorCode,
		"candidate_error_code":  record.CandidateErrorCode,
		"candidate_reason_code": record.CandidateReasonCode,
	} {
		if value != nil && !validMetricCode(*value) {
			return fmt.Errorf("%s is not a safe categorical code", name)
		}
	}
	for name, value := range map[string]*float64{
		"legacy_classify_wall_ms": record.LegacyClassifyWallMS,
		"candidate_call_wall_ms":  record.CandidateCallWallMS,
		"candidate_server_ms":     record.CandidateServerMS,
		"shadow_queue_wait_ms":    record.ShadowQueueWaitMS,
		"decision_wall_ms":        record.DecisionWallMS,
		"mio_ttft_ms":             record.MioTTFTMS,
		"mio_final_ms":            record.MioFinalMS,
		"next_turn_ready_ms":      record.NextTurnReadyMS,
	} {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0) {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	return nil
}

func validMetricCode(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !(character >= 'A' && character <= 'Z') && !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func isReplayRoute(value string) bool {
	for _, route := range replayRouteNames() {
		if value == route {
			return true
		}
	}
	return false
}
