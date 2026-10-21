package classifierlab

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
)

type MetricRate struct {
	Value *float64 `json:"value"`
	Count int      `json:"count"`
	Total int      `json:"total"`
}

type RouteQuality struct {
	Support   int      `json:"support"`
	Predicted int      `json:"predicted"`
	TruePos   int      `json:"true_positive"`
	Precision *float64 `json:"precision"`
	Recall    *float64 `json:"recall"`
	F1        *float64 `json:"f1"`
}

type LatencySummary struct {
	Count int      `json:"count"`
	P50MS *float64 `json:"p50_ms"`
	P95MS *float64 `json:"p95_ms"`
}

type CalibrationBin struct {
	LowerBound float64  `json:"lower_bound"`
	UpperBound float64  `json:"upper_bound"`
	Count      int      `json:"count"`
	Confidence *float64 `json:"mean_confidence"`
	Accuracy   *float64 `json:"accuracy"`
}

type VariantMetrics struct {
	Variant                  string                    `json:"variant"`
	Cases                    int                       `json:"cases"`
	VerifiedSingleLabel      int                       `json:"verified_single_label_cases"`
	VerifiedMultiLabel       int                       `json:"verified_multi_label_cases"`
	Unverified               int                       `json:"unverified_cases"`
	NeedsContext             int                       `json:"needs_context_cases"`
	PredictionRows           int                       `json:"prediction_rows"`
	Accepted                 int                       `json:"accepted"`
	Correct                  int                       `json:"correct"`
	Abstained                int                       `json:"abstained"`
	Errored                  int                       `json:"errored"`
	CandidateCalls           int                       `json:"candidate_calls"`
	CandidateErrors          int                       `json:"candidate_errors"`
	CandidateAbstentions     int                       `json:"candidate_abstentions"`
	FallbackUsed             int                       `json:"fallback_used"`
	CandidateErrorRate       MetricRate                `json:"candidate_error_rate"`
	CandidateAbstentionRate  MetricRate                `json:"candidate_abstention_rate"`
	FallbackRate             MetricRate                `json:"fallback_rate"`
	RuleMatched              int                       `json:"rule_matched_cases"`
	RuleMatchRate            MetricRate                `json:"rule_match_rate"`
	ClassifierReached        int                       `json:"classifier_reached_cases"`
	ClassifierReachedRate    MetricRate                `json:"classifier_reached_rate"`
	MultiLabelAgreement      int                       `json:"multi_label_agreement"`
	Accuracy                 MetricRate                `json:"accuracy_all_verified_single_label"`
	AcceptedOnlyAccuracy     MetricRate                `json:"accepted_only_accuracy"`
	Coverage                 MetricRate                `json:"coverage"`
	SchemaSuccessRate        MetricRate                `json:"schema_success_rate"`
	MultiLabelAgreementRate  MetricRate                `json:"multi_label_agreement_rate"`
	SupportedMacroF1         *float64                  `json:"supported_macro_f1"`
	MacroF1                  *float64                  `json:"macro_f1_all_routes"`
	MissingSupportRoutes     []string                  `json:"missing_support_routes"`
	PerRoute                 map[string]RouteQuality   `json:"per_route"`
	Confusion                map[string]map[string]int `json:"confusion_matrix"`
	ErrorCodes               map[string]int            `json:"error_codes"`
	Latencies                map[string]LatencySummary `json:"latencies"`
	CandidateECE             *float64                  `json:"candidate_ece"`
	CandidateCalibrationBins []CalibrationBin          `json:"candidate_calibration_bins"`
	CriticalNewErrors        int                       `json:"critical_new_errors"`
}

type PairedBootstrapInterval struct {
	Groups    int      `json:"groups"`
	Resamples int      `json:"resamples"`
	MeanDiff  *float64 `json:"candidate_minus_legacy_mean_macro_f1"`
	Lower95   *float64 `json:"lower_95"`
	Upper95   *float64 `json:"upper_95"`
	Status    string   `json:"status"`
}

// ComputeVariantMetrics reports label quality only for verified, single-label
// cases. Abstentions and errors remain in the all-case denominator.
func ComputeVariantMetrics(records []ReplayPrediction, variant string) (VariantMetrics, error) {
	if variant != "legacy" && variant != "candidate" && variant != "hybrid-replay" && variant != "rules-only" {
		return VariantMetrics{}, fmt.Errorf("unsupported metrics variant %q", variant)
	}
	selected := make([]ReplayPrediction, 0)
	for _, record := range records {
		if record.Variant == variant {
			selected = append(selected, record)
		}
	}
	if len(selected) == 0 {
		return VariantMetrics{}, fmt.Errorf("no predictions for variant %q", variant)
	}
	caseIDs := make(map[string]struct{})
	identities := make(map[string]struct{}, len(selected))
	for _, record := range selected {
		if record.CaseID == "" || record.GroupID == "" || record.Repetition < 0 || record.LabelStatus != "draft" && record.LabelStatus != "verified" {
			return VariantMetrics{}, errors.New("prediction identity or label status is invalid")
		}
		identity := fmt.Sprintf("%s\x00%d", record.CaseID, record.Repetition)
		if _, exists := identities[identity]; exists {
			return VariantMetrics{}, fmt.Errorf("duplicate prediction for case %q and repetition %d", record.CaseID, record.Repetition)
		}
		identities[identity] = struct{}{}
		caseIDs[record.CaseID] = struct{}{}
	}
	metrics := VariantMetrics{
		Variant: variant, Cases: len(caseIDs), PredictionRows: len(selected), PerRoute: make(map[string]RouteQuality, len(replayRouteNames())),
		Confusion: make(map[string]map[string]int), ErrorCodes: make(map[string]int),
		Latencies: make(map[string]LatencySummary),
	}
	for _, route := range replayRouteNames() {
		metrics.PerRoute[route] = RouteQuality{}
	}
	eligible := 0
	acceptedEligible := 0
	schemaSuccess := 0
	multiLabelTotal := 0
	candidateCalls := 0
	candidateErrors := 0
	candidateAbstentions := 0
	fallbackUsed := 0
	ruleKnown := 0
	classifierReachedKnown := 0
	candidateCalibration := make([]calibrationObservation, 0)
	latencies := map[string][]float64{
		"legacy_classify_wall_ms": nil, "candidate_call_wall_ms": nil,
		"candidate_server_ms": nil, "shadow_queue_wait_ms": nil,
		"decision_wall_ms": nil, "mio_ttft_ms": nil,
		"mio_final_ms": nil, "next_turn_ready_ms": nil,
	}
	for _, record := range selected {
		if record.CandidateStatus != nil || record.CandidateErrorCode != nil {
			candidateCalls++
		}
		if record.CandidateErrorCode != nil {
			candidateErrors++
		}
		if record.CandidateStatus != nil && *record.CandidateStatus == "abstain" {
			candidateAbstentions++
		}
		if record.FallbackUsed != nil && *record.FallbackUsed {
			fallbackUsed++
		}
		appendLatency := func(name string, value *float64) error {
			if value == nil {
				return nil
			}
			if math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 {
				return fmt.Errorf("prediction %q has invalid %s", record.CaseID, name)
			}
			latencies[name] = append(latencies[name], *value)
			return nil
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
			if err := appendLatency(name, value); err != nil {
				return VariantMetrics{}, err
			}
		}
		if code := predictionErrorCode(record, variant); code != "" {
			metrics.ErrorCodes[code]++
		}
		if record.Repetition != 0 {
			continue
		}
		if record.LabelStatus != "verified" {
			metrics.Unverified++
		}
		if record.NeedsContext {
			metrics.NeedsContext++
		}
		if record.RuleMatched != nil {
			ruleKnown++
			if *record.RuleMatched {
				metrics.RuleMatched++
			}
		}
		if record.ClassifierReached != nil {
			classifierReachedKnown++
			if *record.ClassifierReached {
				metrics.ClassifierReached++
			}
		}
		if (variant == "candidate" || variant == "legacy") && record.ClassifierReached != nil && !*record.ClassifierReached {
			continue
		}
		if len(record.ExpectedRoutes) > 1 && record.LabelStatus == "verified" && !record.NeedsContext {
			metrics.VerifiedMultiLabel++
			multiLabelTotal++
			predicted := predictionRoute(record, variant)
			if predicted != nil {
				metrics.Accepted++
				for _, allowed := range record.ExpectedRoutes {
					if *predicted == allowed {
						metrics.MultiLabelAgreement++
						break
					}
				}
			}
			continue
		}
		if record.LabelStatus != "verified" || record.NeedsContext || len(record.ExpectedRoutes) != 1 {
			continue
		}
		eligible++
		metrics.VerifiedSingleLabel++
		truth := record.ExpectedRoutes[0]
		if !isReplayRoute(truth) {
			return VariantMetrics{}, fmt.Errorf("prediction %q contains unknown expected route %q", record.CaseID, truth)
		}
		class := metrics.PerRoute[truth]
		class.Support++
		metrics.PerRoute[truth] = class
		predicted := predictionRoute(record, variant)
		if predicted == nil {
			if predictionHasValidSchema(record, variant) {
				schemaSuccess++
			}
			if predictionWasAbstention(record, variant) {
				metrics.Abstained++
			} else {
				metrics.Errored++
			}
			confusionCount(metrics.Confusion, truth, "ABSTAIN_OR_ERROR")
			continue
		}
		if !isReplayRoute(*predicted) {
			return VariantMetrics{}, fmt.Errorf("prediction %q contains unknown route %q", record.CaseID, *predicted)
		}
		metrics.Accepted++
		acceptedEligible++
		if predictionHasValidSchema(record, variant) {
			schemaSuccess++
		}
		confusionCount(metrics.Confusion, truth, *predicted)
		predictedClass := metrics.PerRoute[*predicted]
		predictedClass.Predicted++
		if *predicted == truth {
			metrics.Correct++
			predictedClass.TruePos++
		}
		metrics.PerRoute[*predicted] = predictedClass
		if variant == "candidate" && record.CandidateSelectedProb != nil {
			probability := *record.CandidateSelectedProb
			if math.IsNaN(probability) || math.IsInf(probability, 0) || probability < 0 || probability > 1 {
				return VariantMetrics{}, fmt.Errorf("prediction %q has invalid candidate probability", record.CaseID)
			}
			candidateCalibration = append(candidateCalibration, calibrationObservation{confidence: probability, correct: *predicted == truth})
		}
	}
	for _, className := range replayRouteNames() {
		class := metrics.PerRoute[className]
		precision := float64(0)
		if class.Predicted > 0 {
			precision = float64(class.TruePos) / float64(class.Predicted)
		}
		class.Precision = &precision
		class.Recall = ratePointer(class.TruePos, class.Support)
		if class.Support > 0 && class.Recall != nil {
			class.F1 = harmonicMean(class.Precision, class.Recall)
		}
		metrics.PerRoute[className] = class
		if class.Support == 0 {
			metrics.MissingSupportRoutes = append(metrics.MissingSupportRoutes, className)
		} else if class.F1 != nil {
			if metrics.SupportedMacroF1 == nil {
				value := 0.0
				metrics.SupportedMacroF1 = &value
			}
			*metrics.SupportedMacroF1 += *class.F1
		}
	}
	if metrics.SupportedMacroF1 != nil {
		supported := len(replayRouteNames()) - len(metrics.MissingSupportRoutes)
		*metrics.SupportedMacroF1 /= float64(supported)
	}
	if len(metrics.MissingSupportRoutes) == 0 {
		metrics.MacroF1 = metrics.SupportedMacroF1
	}
	metrics.Accuracy = makeRate(metrics.Correct, eligible)
	metrics.AcceptedOnlyAccuracy = makeRate(metrics.Correct, acceptedEligible)
	metrics.Coverage = makeRate(acceptedEligible, eligible)
	metrics.SchemaSuccessRate = makeRate(schemaSuccess, eligible)
	metrics.MultiLabelAgreementRate = makeRate(metrics.MultiLabelAgreement, multiLabelTotal)
	metrics.CandidateCalls = candidateCalls
	metrics.CandidateErrors = candidateErrors
	metrics.CandidateAbstentions = candidateAbstentions
	metrics.FallbackUsed = fallbackUsed
	metrics.CandidateErrorRate = makeRate(candidateErrors, candidateCalls)
	metrics.CandidateAbstentionRate = makeRate(candidateAbstentions, candidateCalls)
	metrics.FallbackRate = makeRate(fallbackUsed, candidateCalls)
	metrics.RuleMatchRate = makeRate(metrics.RuleMatched, ruleKnown)
	metrics.ClassifierReachedRate = makeRate(metrics.ClassifierReached, classifierReachedKnown)
	metrics.CandidateCalibrationBins, metrics.CandidateECE = calibrationSummary(candidateCalibration, 10)
	for name, values := range latencies {
		metrics.Latencies[name] = summarizeLatency(values)
	}
	metrics.CriticalNewErrors = criticalNewErrors(selected, records)
	return metrics, nil
}

type calibrationObservation struct {
	confidence float64
	correct    bool
}

func predictionRoute(record ReplayPrediction, variant string) *string {
	switch variant {
	case "legacy":
		if record.LegacyErrorCode == nil {
			return record.LegacyClassifiedRoute
		}
	case "candidate":
		if record.CandidateStatus != nil && *record.CandidateStatus == "classified" && record.CandidateErrorCode == nil {
			return record.CandidateSelectedRoute
		}
	case "hybrid-replay", "rules-only":
		return record.FinalRoute
	}
	return nil
}

func predictionWasAbstention(record ReplayPrediction, variant string) bool {
	if variant == "candidate" && record.CandidateStatus != nil && *record.CandidateStatus == "abstain" {
		return true
	}
	return false
}

func predictionHasValidSchema(record ReplayPrediction, variant string) bool {
	switch variant {
	case "candidate":
		return record.CandidateStatus != nil && (*record.CandidateStatus == "classified" || *record.CandidateStatus == "abstain") && record.CandidateErrorCode == nil
	case "legacy":
		return record.LegacyClassifiedRoute != nil && record.LegacyErrorCode == nil
	case "hybrid-replay", "rules-only":
		return record.FinalRoute != nil
	default:
		return false
	}
}

func predictionErrorCode(record ReplayPrediction, variant string) string {
	switch variant {
	case "legacy":
		if record.LegacyErrorCode != nil {
			return *record.LegacyErrorCode
		}
	case "candidate":
		if record.CandidateErrorCode != nil {
			return *record.CandidateErrorCode
		}
	}
	return ""
}

func confusionCount(matrix map[string]map[string]int, expected, predicted string) {
	row := matrix[expected]
	if row == nil {
		row = make(map[string]int)
		matrix[expected] = row
	}
	row[predicted]++
}

func ratePointer(numerator, denominator int) *float64 {
	if denominator == 0 {
		return nil
	}
	value := float64(numerator) / float64(denominator)
	return &value
}

func makeRate(numerator, denominator int) MetricRate {
	return MetricRate{Value: ratePointer(numerator, denominator), Count: numerator, Total: denominator}
}

func harmonicMean(left, right *float64) *float64 {
	if left == nil || right == nil {
		return nil
	}
	if *left+*right == 0 {
		zero := 0.0
		return &zero
	}
	value := 2 * *left * *right / (*left + *right)
	return &value
}

func summarizeLatency(values []float64) LatencySummary {
	if len(values) == 0 {
		return LatencySummary{Count: 0}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	p50 := nearestRank(sorted, 0.50)
	p95 := nearestRank(sorted, 0.95)
	return LatencySummary{Count: len(sorted), P50MS: &p50, P95MS: &p95}
}

func nearestRank(sorted []float64, percentile float64) float64 {
	index := int(math.Ceil(percentile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func calibrationSummary(observations []calibrationObservation, bins int) ([]CalibrationBin, *float64) {
	if len(observations) == 0 || bins < 1 {
		return nil, nil
	}
	counts := make([]int, bins)
	confidenceTotals := make([]float64, bins)
	correctTotals := make([]float64, bins)
	for _, observation := range observations {
		index := int(observation.confidence * float64(bins))
		if index == bins {
			index--
		}
		counts[index]++
		confidenceTotals[index] += observation.confidence
		if observation.correct {
			correctTotals[index]++
		}
	}
	result := make([]CalibrationBin, bins)
	ece := 0.0
	for index := 0; index < bins; index++ {
		item := CalibrationBin{LowerBound: float64(index) / float64(bins), UpperBound: float64(index+1) / float64(bins), Count: counts[index]}
		if counts[index] > 0 {
			meanConfidence := confidenceTotals[index] / float64(counts[index])
			accuracy := correctTotals[index] / float64(counts[index])
			item.Confidence = &meanConfidence
			item.Accuracy = &accuracy
			ece += math.Abs(meanConfidence-accuracy) * float64(counts[index]) / float64(len(observations))
		}
		result[index] = item
	}
	return result, &ece
}

func isCriticalRisk(tags []string) bool {
	for _, tag := range tags {
		if tag == "critical" || tag == "critical_route" || tag == "execution" {
			return true
		}
	}
	return false
}

func criticalNewErrors(candidateRecords, allRecords []ReplayPrediction) int {
	legacyByCase := make(map[string]ReplayPrediction)
	for _, record := range allRecords {
		if record.Variant == "legacy" && record.Repetition == 0 {
			legacyByCase[record.CaseID] = record
		}
	}
	count := 0
	for _, candidate := range candidateRecords {
		if candidate.Variant != "candidate" || candidate.Repetition != 0 || !isCriticalRisk(candidate.RiskTags) || candidate.LabelStatus != "verified" || len(candidate.ExpectedRoutes) != 1 {
			continue
		}
		if candidate.ClassifierReached != nil && !*candidate.ClassifierReached {
			continue
		}
		legacy, exists := legacyByCase[candidate.CaseID]
		if !exists || legacy.LabelStatus != "verified" || len(legacy.ExpectedRoutes) != 1 {
			continue
		}
		if legacy.ClassifierReached != nil && !*legacy.ClassifierReached {
			continue
		}
		truth := candidate.ExpectedRoutes[0]
		legacyRoute := predictionRoute(legacy, "legacy")
		candidateRoute := predictionRoute(candidate, "candidate")
		if legacyRoute != nil && *legacyRoute == truth && (candidateRoute == nil || *candidateRoute != truth) {
			count++
		}
	}
	return count
}

type pairedCase struct {
	groupID   string
	truth     string
	legacy    *string
	candidate *string
}

// BootstrapPairedMacroF1 resamples conversation groups, not repeated model
// calls. It returns an error until every fixed route has support and at least
// twenty independent groups are present.
func BootstrapPairedMacroF1(records []ReplayPrediction, resamples int, seed int64) (PairedBootstrapInterval, error) {
	if resamples < 1 {
		return PairedBootstrapInterval{}, errors.New("bootstrap resamples must be positive")
	}
	byCase := make(map[string]map[string]ReplayPrediction)
	for _, record := range records {
		if record.Variant != "candidate" && record.Variant != "legacy" {
			continue
		}
		if record.ClassifierReached != nil && !*record.ClassifierReached {
			continue
		}
		if record.Repetition != 0 || record.LabelStatus != "verified" || len(record.ExpectedRoutes) != 1 {
			continue
		}
		if byCase[record.CaseID] == nil {
			byCase[record.CaseID] = make(map[string]ReplayPrediction)
		}
		byCase[record.CaseID][record.Variant] = record
	}
	pairs := make([]pairedCase, 0, len(byCase))
	support := make(map[string]bool)
	for _, variants := range byCase {
		candidate, candidateOK := variants["candidate"]
		legacy, legacyOK := variants["legacy"]
		if !candidateOK || !legacyOK || candidate.GroupID != legacy.GroupID || len(candidate.ExpectedRoutes) != 1 || candidate.ExpectedRoutes[0] != legacy.ExpectedRoutes[0] {
			continue
		}
		pairs = append(pairs, pairedCase{
			groupID: candidate.GroupID, truth: candidate.ExpectedRoutes[0],
			legacy: predictionRoute(legacy, "legacy"), candidate: predictionRoute(candidate, "candidate"),
		})
		support[candidate.ExpectedRoutes[0]] = true
	}
	if len(pairs) == 0 {
		return PairedBootstrapInterval{}, errors.New("no paired verified cases are available")
	}
	groupCases := make(map[string][]pairedCase)
	for _, pair := range pairs {
		groupCases[pair.groupID] = append(groupCases[pair.groupID], pair)
	}
	groups := make([]string, 0, len(groupCases))
	for group := range groupCases {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	if len(groups) < 20 {
		return PairedBootstrapInterval{Groups: len(groups), Resamples: resamples, Status: "INCONCLUSIVE"}, errors.New("paired group count is below 20")
	}
	for _, route := range replayRouteNames() {
		if !support[route] {
			return PairedBootstrapInterval{Groups: len(groups), Resamples: resamples, Status: "INCONCLUSIVE"}, fmt.Errorf("route %s has no verified paired support", route)
		}
	}
	meanDiffs := make([]float64, resamples)
	random := rand.New(rand.NewSource(seed))
	for iteration := 0; iteration < resamples; iteration++ {
		sample := make([]pairedCase, 0, len(pairs))
		for range groups {
			group := groups[random.Intn(len(groups))]
			sample = append(sample, groupCases[group]...)
		}
		candidateF1, legacyF1 := pairedMacroF1(sample)
		meanDiffs[iteration] = candidateF1 - legacyF1
	}
	sort.Float64s(meanDiffs)
	mean := 0.0
	for _, difference := range meanDiffs {
		mean += difference
	}
	mean /= float64(len(meanDiffs))
	lower := nearestRank(meanDiffs, 0.025)
	upper := nearestRank(meanDiffs, 0.975)
	return PairedBootstrapInterval{Groups: len(groups), Resamples: resamples, MeanDiff: &mean, Lower95: &lower, Upper95: &upper, Status: "COMPUTED"}, nil
}

func pairedMacroF1(pairs []pairedCase) (float64, float64) {
	legacyTrue, legacyPredicted, legacyTP := make(map[string]int), make(map[string]int), make(map[string]int)
	candidateTrue, candidatePredicted, candidateTP := make(map[string]int), make(map[string]int), make(map[string]int)
	for _, pair := range pairs {
		legacyTrue[pair.truth]++
		candidateTrue[pair.truth]++
		if pair.legacy != nil {
			legacyPredicted[*pair.legacy]++
			if *pair.legacy == pair.truth {
				legacyTP[pair.truth]++
			}
		}
		if pair.candidate != nil {
			candidatePredicted[*pair.candidate]++
			if *pair.candidate == pair.truth {
				candidateTP[pair.truth]++
			}
		}
	}
	return fixedMacroF1(candidateTrue, candidatePredicted, candidateTP), fixedMacroF1(legacyTrue, legacyPredicted, legacyTP)
}

func fixedMacroF1(truth, predicted, truePositive map[string]int) float64 {
	total := 0.0
	for _, route := range replayRouteNames() {
		precision := 0.0
		if predicted[route] > 0 {
			precision = float64(truePositive[route]) / float64(predicted[route])
		}
		recall := 0.0
		if truth[route] > 0 {
			recall = float64(truePositive[route]) / float64(truth[route])
		}
		f1 := 0.0
		if precision+recall > 0 {
			f1 = 2 * precision * recall / (precision + recall)
		}
		total += f1
	}
	return total / float64(len(replayRouteNames()))
}
