package tradeshadowoutcomereport

import (
	"context"
	"errors"
	"testing"

	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

type reportReaderStub struct {
	requestID string
	studyID   string
	result    moduletrade.PrivateShadowOutcomeReport
	err       error
}

func (stub *reportReaderStub) ShadowOutcomeReport(_ context.Context, requestID, studyID string) (moduletrade.PrivateShadowOutcomeReport, error) {
	stub.requestID, stub.studyID = requestID, studyID
	return stub.result, stub.err
}

func TestReportPassesStudyAndRequestIdentity(t *testing.T) {
	reader := &reportReaderStub{result: validReportEnvelope()}
	service, err := NewService(reader)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Report(context.Background(), Request{RequestID: "request-1", StudyID: "study-1"})
	if err != nil || result.Report == nil || reader.requestID != "request-1" || reader.studyID != "study-1" {
		t.Fatalf("result=%+v err=%v reader=%+v", result, err, reader)
	}
}

func TestReportRejectsInvalidRequestAndWrapsUnavailable(t *testing.T) {
	reader := &reportReaderStub{result: validReportEnvelope(), err: errors.New("offline")}
	service, _ := NewService(reader)
	if _, err := service.Report(context.Background(), Request{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected invalid request, got %v", err)
	}
	if _, err := service.Report(context.Background(), Request{RequestID: "request-1", StudyID: "study-1"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
}

func validReportEnvelope() moduletrade.PrivateShadowOutcomeReport {
	return moduletrade.PrivateShadowOutcomeReport{
		ContractVersion: "trade-private/v1", ExecutionMode: "DISABLED", Environment: "SHADOW",
		Report: moduletrade.ShadowOutcomeReport{SchemaVersion: 1, ContractVersion: moduletrade.ShadowOutcomeReportContractVersion, StudyID: "study-1", Environment: "SHADOW", ObservationCount: 1, PendingOutcomeCount: 1, LabelCounts: map[string]int64{"success": 0, "failure": 0, "neutral": 0, "inconclusive": 0}, ReviewState: "pending_outcomes"},
	}
}
