package tradeshadowreviewreport

import (
	"context"
	"testing"

	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

type reportReaderStub struct{ studyID string }

func (stub *reportReaderStub) ShadowReviewReport(_ context.Context, _ string, studyID string) (moduletrade.PrivateShadowReviewReport, error) {
	stub.studyID = studyID
	return moduletrade.PrivateShadowReviewReport{Report: moduletrade.ShadowReviewReport{StudyID: studyID}}, nil
}

func TestReportRequiresStudyAndPassesStudyToModule(t *testing.T) {
	reader := &reportReaderStub{}
	service, err := NewService(reader)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Report(context.Background(), Request{RequestID: "report-1", StudyID: "study-1"})
	if err != nil || result.Report == nil || reader.studyID != "study-1" {
		t.Fatalf("result=%+v study=%s err=%v", result, reader.studyID, err)
	}
	if _, err := service.Report(context.Background(), Request{}); err != ErrInvalidRequest {
		t.Fatalf("err=%v", err)
	}
}
