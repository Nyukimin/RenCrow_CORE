package llmops

import "testing"

func TestEstimateConfidenceIsKnown(t *testing.T) {
	known := []EstimateConfidence{
		EstimateConfidenceMeasuredLocal,
		EstimateConfidenceMeasuredCommunity,
		EstimateConfidenceCalibrated,
		EstimateConfidenceEstimated,
		EstimateConfidenceUnsupported,
	}
	for _, c := range known {
		if !c.IsKnown() {
			t.Fatalf("%q should be known", c)
		}
	}
	if EstimateConfidence("future_value").IsKnown() {
		t.Fatal("unknown confidence must not be reported as known")
	}
	if EstimateConfidence("").IsKnown() {
		t.Fatal("empty confidence must not be reported as known")
	}
}

func TestEstimateConfidenceIsMeasured(t *testing.T) {
	if !EstimateConfidenceMeasuredLocal.IsMeasured() || !EstimateConfidenceMeasuredCommunity.IsMeasured() {
		t.Fatal("measured_* levels must report IsMeasured")
	}
	for _, c := range []EstimateConfidence{EstimateConfidenceCalibrated, EstimateConfidenceEstimated, EstimateConfidenceUnsupported, "other"} {
		if c.IsMeasured() {
			t.Fatalf("%q must not report IsMeasured", c)
		}
	}
}

func TestModelFitAssessmentPointerFieldsDefaultToNil(t *testing.T) {
	var a ModelFitAssessment
	if a.BestQuant != nil || a.EstimatedTPS != nil || a.MeasuredTPS != nil || a.PrefillTPS != nil || a.TTFTMs != nil || a.DiskSizeGB != nil {
		t.Fatal("pointer fields must default to nil (not reported)")
	}
}
