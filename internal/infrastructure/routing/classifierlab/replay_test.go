package classifierlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainrouting "github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestReplayDatasetRejectsDuplicateFieldsAndCrossSplitGroups(t *testing.T) {
	duplicate := `{"case_id":"a","case_id":"b","origin":"synthetic","group_id":"g","split":"test","message":"synthetic","turn_fixture":{},"expected_routes":["CHAT"],"label_status":"draft","needs_context":false,"risk_tags":[]}`
	if _, err := DecodeReplayDataset(strings.NewReader(duplicate)); err == nil {
		t.Fatal("duplicate JSON field was accepted")
	}
	first := replayCaseJSON(t, ReplayCase{CaseID: "a", Origin: "synthetic", GroupID: "shared", Split: "test", Message: "synthetic a", TurnFixture: map[string]json.RawMessage{}, ExpectedRoutes: []string{"CHAT"}, LabelStatus: "draft"})
	second := replayCaseJSON(t, ReplayCase{CaseID: "b", Origin: "synthetic", GroupID: "shared", Split: "dev", Message: "synthetic b", TurnFixture: map[string]json.RawMessage{}, ExpectedRoutes: []string{"CHAT"}, LabelStatus: "draft"})
	if _, err := DecodeReplayDataset(bytes.NewReader(append(first, second...))); err == nil || !strings.Contains(err.Error(), "leaks") {
		t.Fatalf("cross-split group error = %v", err)
	}
}

func TestReplayTurnFixtureAcceptsOnlyReconstructableMetadata(t *testing.T) {
	fixture, err := ParseReplayTurnFixture(map[string]json.RawMessage{
		"session_id": []byte(`"session-1"`), "viewer_recipient": []byte(`"viewer-1"`),
	})
	if err != nil || fixture.SessionID != "session-1" || fixture.ViewerRecipient != "viewer-1" {
		t.Fatalf("allowlisted turn fixture = %+v, err=%v", fixture, err)
	}
	if _, err := ParseReplayTurnFixture(map[string]json.RawMessage{"old_route": []byte(`"CODE"`)}); err == nil {
		t.Fatal("unknown turn_fixture key was accepted")
	}
	if _, err := ParseReplayTurnFixture(map[string]json.RawMessage{"session_id": []byte(`"bad\nvalue"`)}); err == nil {
		t.Fatal("control characters in session_id were accepted")
	}
}

func TestReplayOrderIsIndependentOfJSONLRowOrder(t *testing.T) {
	cases := []ReplayCase{
		{CaseID: "c", GroupID: "g3", Split: "test"},
		{CaseID: "a", GroupID: "g1", Split: "test"},
		{CaseID: "b", GroupID: "g2", Split: "test"},
	}
	forward, err := ReplayOrder(cases, "test", 431)
	if err != nil {
		t.Fatal(err)
	}
	reversedInput := []ReplayCase{cases[2], cases[1], cases[0]}
	reversed, err := ReplayOrder(reversedInput, "test", 431)
	if err != nil {
		t.Fatal(err)
	}
	for index := range forward {
		if forward[index].CaseID != reversed[index].CaseID {
			t.Fatalf("orders differ at index %d: %q != %q", index, forward[index].CaseID, reversed[index].CaseID)
		}
	}
	if ReplayRequestID("exp", "case-secret", "candidate", 0) != ReplayRequestID("exp", "case-secret", "candidate", 0) {
		t.Fatal("request ID was not stable")
	}
}

func TestReplayPredictionsRequireStrictUniqueRows(t *testing.T) {
	valid := ReplayPrediction{
		CaseID: "case-1", GroupID: "group-1", Origin: "synthetic", Split: "test", Variant: "candidate",
		ExpectedRoutes: []string{"CHAT"}, LabelStatus: "draft", CandidateStatus: stringPointer("classified"),
		CandidateSelectedRoute: stringPointer("CHAT"), CandidateProbabilities: testProbabilityMap("CHAT"),
		CandidateSelectedProb: floatPointer(1), ManifestID: "manifest-1",
	}
	data, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if records, err := DecodeReplayPredictions(bytes.NewReader(data)); err != nil || len(records) != 1 {
		t.Fatalf("valid prediction = %d records, %v", len(records), err)
	}
	if _, err := DecodeReplayPredictions(bytes.NewReader(append(data, data...))); err == nil || !strings.Contains(err.Error(), "duplicate prediction identity") {
		t.Fatalf("duplicate prediction identity error = %v", err)
	}
	invalidRecord := valid
	invalidRecord.CandidateProbabilities = map[string]float64{"NOT_A_ROUTE": 1}
	invalid, err := json.Marshal(invalidRecord)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReplayPredictions(bytes.NewReader(append(invalid, '\n'))); err == nil {
		t.Fatal("unknown candidate probability route was accepted")
	}
}

func testProbabilityMap(selected string) map[string]float64 {
	probabilities := make(map[string]float64, len(routeLabels))
	for _, route := range routeLabels {
		probabilities[string(route)] = 0
	}
	probabilities[selected] = 1
	return probabilities
}

type hybridCandidateStub struct {
	result CandidateResult
	err    error
	order  *[]string
}

func (stub hybridCandidateStub) ClassifyCandidate(context.Context, CandidateInput) (CandidateResult, error) {
	*stub.order = append(*stub.order, "candidate")
	return stub.result, stub.err
}

type hybridLegacyStub struct {
	decision domainrouting.Decision
	err      error
	order    *[]string
}

func (stub hybridLegacyStub) Classify(context.Context, conversation.TurnInput) (domainrouting.Decision, error) {
	*stub.order = append(*stub.order, "legacy")
	return stub.decision, stub.err
}

func TestHybridReplayCallsCandidateFirstAndRunsLegacyOnlyForFallback(t *testing.T) {
	address, err := conversation.NewChannelAddress("replay", "case-1")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := conversation.NewTurnInput(modulecore.NewTaskID(), "in-memory replay message", address)
	if err != nil {
		t.Fatal(err)
	}
	selected := LabelPLAN
	var order []string
	out, err := RunHybridReplay(context.Background(), hybridCandidateStub{
		result: CandidateResult{Status: CandidateClassified, Route: &selected}, order: &order,
	}, hybridLegacyStub{decision: domainrouting.Decision{Route: domainrouting.RouteCHAT, Confidence: 0.8}, order: &order},
		CandidateInput{Message: "in-memory replay message"}, turn, func(CandidateResult) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "candidate" || out.FallbackUsed || out.FinalRoute == nil || *out.FinalRoute != domainrouting.RoutePLAN {
		t.Fatalf("accepted candidate outcome = %+v, order=%v", out, order)
	}

	order = nil
	out, err = RunHybridReplay(context.Background(), hybridCandidateStub{
		result: CandidateResult{Status: CandidateClassified, Route: &selected}, order: &order,
	}, hybridLegacyStub{decision: domainrouting.Decision{Route: domainrouting.RouteCHAT, Confidence: 0.8}, order: &order},
		CandidateInput{Message: "in-memory replay message"}, turn, func(CandidateResult) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "candidate,legacy" || !out.FallbackUsed || out.FinalRoute == nil || *out.FinalRoute != domainrouting.RouteCHAT {
		t.Fatalf("rejected candidate outcome = %+v, order=%v", out, order)
	}

	order = nil
	out, err = RunHybridReplay(context.Background(), hybridCandidateStub{
		err: errors.New("candidate raw error must not be recorded"), order: &order,
	}, hybridLegacyStub{decision: domainrouting.Decision{Route: domainrouting.RouteCHAT, Confidence: 0.8}, order: &order},
		CandidateInput{Message: "in-memory replay message"}, turn, func(CandidateResult) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "candidate,legacy" || !out.FallbackUsed || out.CandidateErrorCode != "CANDIDATE_ERROR" || out.FinalRoute == nil || *out.FinalRoute != domainrouting.RouteCHAT {
		t.Fatalf("candidate-error fallback outcome = %+v, order=%v", out, order)
	}
}

func TestReplayMetricsUseOneQualityObservationAndKeepFailuresInDenominator(t *testing.T) {
	chat, plan, ops := "CHAT", "PLAN", "OPS"
	classified, abstain := "classified", "abstain"
	truths := []struct {
		id, group, truth string
		candidate        *string
		status           string
		probability      *float64
		legacy           *string
		fallback         bool
		risk             []string
	}{
		{id: "c1", group: "g1", truth: chat, candidate: &chat, status: classified, probability: floatPointer(0.8), legacy: &chat},
		{id: "c2", group: "g2", truth: plan, candidate: &chat, status: classified, probability: floatPointer(0.7), legacy: &plan, risk: []string{"critical"}},
		{id: "c3", group: "g3", truth: ops, status: abstain, legacy: &ops, fallback: true},
	}
	var records []ReplayPrediction
	for index, item := range truths {
		candidate := ReplayPrediction{
			CaseID: item.id, GroupID: item.group, Origin: "synthetic", Split: "test", Variant: "candidate",
			ExpectedRoutes: []string{item.truth}, LabelStatus: "verified", RiskTags: item.risk,
			CandidateStatus: stringPointer(item.status), CandidateSelectedRoute: item.candidate,
			CandidateSelectedProb: item.probability, CandidateCallWallMS: floatPointer(float64((index + 1) * 10)),
			FallbackUsed: boolPointer(item.fallback), ManifestID: "manifest-1",
		}
		if item.probability != nil && item.candidate != nil {
			candidate.CandidateProbabilities = map[string]float64{*item.candidate: *item.probability}
		}
		records = append(records, candidate)
		legacy := ReplayPrediction{
			CaseID: item.id, GroupID: item.group, Origin: "synthetic", Split: "test", Variant: "legacy",
			ExpectedRoutes: []string{item.truth}, LabelStatus: "verified", LegacyClassifiedRoute: item.legacy,
			LegacyClassifyWallMS: floatPointer(float64((index + 1) * 12)), ManifestID: "manifest-1",
		}
		records = append(records, legacy)
	}
	// Latency repetitions must not increase the quality denominator.
	repeated := records[0]
	repeated.Repetition = 1
	repeated.CandidateSelectedRoute = &plan
	repeated.CandidateSelectedProb = floatPointer(0.99)
	repeated.CandidateCallWallMS = floatPointer(5)
	records = append(records, repeated)

	metrics, err := ComputeVariantMetrics(records, "candidate")
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Cases != 3 || metrics.PredictionRows != 4 || metrics.VerifiedSingleLabel != 3 || metrics.Correct != 1 || metrics.Accepted != 2 || metrics.Abstained != 1 || metrics.CriticalNewErrors != 1 {
		t.Fatalf("unexpected counts: %+v", metrics)
	}
	assertRate(t, metrics.Accuracy, 1, 3, 1.0/3)
	assertRate(t, metrics.AcceptedOnlyAccuracy, 1, 2, 0.5)
	assertRate(t, metrics.Coverage, 2, 3, 2.0/3)
	assertRate(t, metrics.SchemaSuccessRate, 3, 3, 1)
	assertRate(t, metrics.CandidateAbstentionRate, 1, 4, 0.25)
	if metrics.PerRoute[plan].F1 == nil || *metrics.PerRoute[plan].F1 != 0 {
		t.Fatalf("zero-support prediction should have a defined zero F1: %+v", metrics.PerRoute[plan])
	}
	if metrics.SupportedMacroF1 == nil || math.Abs(*metrics.SupportedMacroF1-2.0/9) > 1e-12 {
		t.Fatalf("supported macro F1 = %v, want 2/9", metrics.SupportedMacroF1)
	}
	if metrics.Latencies["candidate_call_wall_ms"].P50MS == nil || *metrics.Latencies["candidate_call_wall_ms"].P50MS != 10 || metrics.Latencies["candidate_call_wall_ms"].P95MS == nil || *metrics.Latencies["candidate_call_wall_ms"].P95MS != 30 {
		t.Fatalf("candidate latency summary = %+v", metrics.Latencies["candidate_call_wall_ms"])
	}
	if metrics.CandidateECE == nil || math.Abs(*metrics.CandidateECE-0.45) > 1e-12 {
		t.Fatalf("candidate ECE = %v, want 0.45", metrics.CandidateECE)
	}
}

func TestModelQualityExcludesCasesThatDoNotReachMioClassifier(t *testing.T) {
	reached, notReached := true, false
	route := "CHAT"
	status := "classified"
	records := []ReplayPrediction{
		{CaseID: "explicit", GroupID: "g1", Origin: "synthetic", Split: "test", Variant: "candidate", LabelStatus: "verified", ExpectedRoutes: []string{"CHAT"}, ClassifierReached: &notReached},
		{CaseID: "classifier", GroupID: "g2", Origin: "synthetic", Split: "test", Variant: "candidate", LabelStatus: "verified", ExpectedRoutes: []string{"CHAT"}, ClassifierReached: &reached, CandidateStatus: &status, CandidateSelectedRoute: &route},
	}
	metrics, err := ComputeVariantMetrics(records, "candidate")
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Cases != 2 || metrics.VerifiedSingleLabel != 1 || metrics.ClassifierReached != 1 || metrics.Correct != 1 || metrics.CandidateCalls != 1 {
		t.Fatalf("candidate metrics do not use classifier-reaching quality denominator: %+v", metrics)
	}
}

func TestPairedBootstrapIsDeterministicAndRejectsInsufficientSupport(t *testing.T) {
	if _, err := BootstrapPairedMacroF1(nil, 100, 1); err == nil {
		t.Fatal("empty paired bootstrap was accepted")
	}
	var records []ReplayPrediction
	for group := 0; group < 20; group++ {
		for _, route := range replayRouteNames() {
			caseID := route + "-" + string(rune('a'+group))
			legacyRoute, candidateRoute := route, route
			records = append(records,
				ReplayPrediction{CaseID: caseID, GroupID: "group-" + string(rune('a'+group)), Variant: "legacy", LabelStatus: "verified", ExpectedRoutes: []string{route}, LegacyClassifiedRoute: &legacyRoute},
				ReplayPrediction{CaseID: caseID, GroupID: "group-" + string(rune('a'+group)), Variant: "candidate", LabelStatus: "verified", ExpectedRoutes: []string{route}, CandidateStatus: stringPointer("classified"), CandidateSelectedRoute: &candidateRoute},
			)
		}
	}
	first, err := BootstrapPairedMacroF1(records, 100, 7)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BootstrapPairedMacroF1(records, 100, 7)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "COMPUTED" || first.Lower95 == nil || *first.Lower95 != 0 || first.Upper95 == nil || *first.Upper95 != 0 || first.MeanDiff == nil || *first.MeanDiff != 0 || *first.MeanDiff != *second.MeanDiff {
		t.Fatalf("paired bootstrap = %+v, second=%+v", first, second)
	}
}

func replayCaseJSON(t *testing.T, item ReplayCase) []byte {
	t.Helper()
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func floatPointer(value float64) *float64 { return &value }
func stringPointer(value string) *string  { return &value }
func boolPointer(value bool) *bool        { return &value }

func assertRate(t *testing.T, rate MetricRate, count, total int, value float64) {
	t.Helper()
	if rate.Count != count || rate.Total != total || rate.Value == nil || math.Abs(*rate.Value-value) > 1e-12 {
		t.Fatalf("rate = %+v, want count=%d total=%d value=%f", rate, count, total, value)
	}
}
