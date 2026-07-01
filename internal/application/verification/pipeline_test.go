package verification

import (
	"context"
	"errors"
	"testing"
	"time"

	domainverification "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/verification"
)

type stubEvidenceReader struct {
	evidence []domainverification.EvidenceRef
	err      error
}

func (s stubEvidenceReader) ReadEvidence(context.Context, domainverification.Claim, Request) ([]domainverification.EvidenceRef, error) {
	return s.evidence, s.err
}

type stubRepository struct {
	reports []domainverification.VerificationReport
	err     error
}

func (s *stubRepository) Save(_ context.Context, report domainverification.VerificationReport) error {
	if s.err != nil {
		return s.err
	}
	s.reports = append(s.reports, report)
	return nil
}

func TestPipelineDisabledReturnsNotChecked(t *testing.T) {
	p := NewPipeline(Options{Policy: domainverification.VerificationPolicy{Enabled: false}})
	result, err := p.VerifyResponse(context.Background(), Request{
		DraftResponse: "これは2014年公開です。",
		SessionID:     "session-1",
		JobID:         "job-1",
	})
	if err != nil {
		t.Fatalf("VerifyResponse failed: %v", err)
	}
	if result.Response != "これは2014年公開です。" {
		t.Fatalf("expected draft response to be preserved, got %q", result.Response)
	}
	if result.Report.Status != domainverification.StatusNotChecked {
		t.Fatalf("expected not_checked, got %s", result.Report.Status)
	}
	if result.Report.ErrorKind != domainverification.ErrorVerifierDisabled {
		t.Fatalf("expected disabled error kind, got %s", result.Report.ErrorKind)
	}
}

func TestPipelineHighRiskWithoutEvidenceReaderDoesNotPretendSuccess(t *testing.T) {
	p := NewPipeline(Options{Policy: domainverification.VerificationPolicy{Enabled: true}})
	result, err := p.VerifyResponse(context.Background(), Request{
		DraftResponse: "ニュースでは新モデルが2026年に発表されました。",
		UserMessage:   "ニュースを要約して",
		Route:         "RESEARCH",
		SessionID:     "session-1",
		JobID:         "job-1",
		Now:           time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("VerifyResponse failed: %v", err)
	}
	if result.Report.Status == domainverification.StatusVerified {
		t.Fatal("missing evidence reader must not produce verified status")
	}
	if result.Report.ClaimCount == 0 {
		t.Fatal("expected high-risk factual response to produce claims")
	}
	if result.Report.NotCheckedCount == 0 {
		t.Fatalf("expected not_checked claims, got %+v", result.Report)
	}
}

func TestPipelineMarksSingleEvidenceAsWeaklySupported(t *testing.T) {
	p := NewPipeline(Options{
		Policy: domainverification.VerificationPolicy{Enabled: true},
		EvidenceReader: stubEvidenceReader{evidence: []domainverification.EvidenceRef{{
			ID:         "ev-1",
			SourceType: domainverification.EvidenceVectorKB,
			SourceID:   "kb:movie",
			Supports:   true,
		}}},
	})
	result, err := p.VerifyResponse(context.Background(), Request{
		DraftResponse: "この作品は2014年公開です。",
		UserMessage:   "作品情報を教えて",
		SessionID:     "session-1",
		JobID:         "job-1",
	})
	if err != nil {
		t.Fatalf("VerifyResponse failed: %v", err)
	}
	if result.Report.Status != domainverification.StatusWeaklySupported {
		t.Fatalf("expected weakly_supported, got %s", result.Report.Status)
	}
}

func TestPipelineDryRunDoesNotRewriteUnsupportedHighRiskResponse(t *testing.T) {
	p := NewPipeline(Options{Policy: domainverification.VerificationPolicy{Enabled: true, Mode: "dry_run"}})
	result, err := p.VerifyResponse(context.Background(), Request{
		DraftResponse: "ニュースでは新モデルが2026年に発表されました。",
		UserMessage:   "ニュースを教えて",
		SessionID:     "session-1",
		JobID:         "job-1",
	})
	if err != nil {
		t.Fatalf("VerifyResponse failed: %v", err)
	}
	if result.Response != "ニュースでは新モデルが2026年に発表されました。" {
		t.Fatalf("dry-run must preserve response, got %q", result.Response)
	}
	if result.Report.Status == domainverification.StatusVerified {
		t.Fatal("unsupported high-risk report must not be verified")
	}
}

func TestPipelinePersistenceFailureIsReturned(t *testing.T) {
	repo := &stubRepository{err: errors.New("disk full")}
	p := NewPipeline(Options{
		Policy:     domainverification.VerificationPolicy{Enabled: true},
		Repository: repo,
	})
	_, err := p.VerifyResponse(context.Background(), Request{
		DraftResponse: "ニュースでは新モデルが2026年に発表されました。",
		UserMessage:   "ニュース",
		SessionID:     "session-1",
		JobID:         "job-1",
	})
	if err == nil {
		t.Fatal("expected persistence error")
	}
}

func TestDetermineTriggerLevel(t *testing.T) {
	if got := DetermineTriggerLevel(Request{UserMessage: "今日のニュースを教えて"}, domainverification.TriggerLow); got != domainverification.TriggerHigh {
		t.Fatalf("expected high trigger, got %s", got)
	}
	if got := DetermineTriggerLevel(Request{UserMessage: "おすすめ作品を教えて"}, domainverification.TriggerLow); got != domainverification.TriggerMedium {
		t.Fatalf("expected medium trigger, got %s", got)
	}
	if got := DetermineTriggerLevel(Request{UserMessage: "こんにちは"}, domainverification.TriggerLow); got != domainverification.TriggerLow {
		t.Fatalf("expected low trigger, got %s", got)
	}
}
