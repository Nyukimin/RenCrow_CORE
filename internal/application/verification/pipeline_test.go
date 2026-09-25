package verification

import (
	"context"
	"errors"
	"testing"
	"time"

	"strings"

	domainverification "github.com/Nyukimin/RenCrow_CORE/internal/domain/verification"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type stubEvidenceReader struct {
	evidence []domainverification.EvidenceRef
	err      error
}

type stubClaimExtractor struct {
	claims []domainverification.Claim
	err    error
}

func (s stubClaimExtractor) ExtractClaims(context.Context, Request, domainverification.TriggerLevel) ([]domainverification.Claim, error) {
	return s.claims, s.err
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
		TaskID:        modulecore.NewTaskID(),
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

func TestPipelineRejectsMissingOrInvalidTaskIDBeforeAnyMode(t *testing.T) {
	repo := &stubRepository{}
	p := NewPipeline(Options{Policy: domainverification.VerificationPolicy{Enabled: false}, Repository: repo})
	for _, taskID := range []modulecore.TaskID{"", modulecore.TaskID(modulecore.NewMessageID())} {
		_, err := p.VerifyResponse(context.Background(), Request{DraftResponse: "draft", SessionID: "session-1", TaskID: taskID})
		if err == nil {
			t.Fatalf("TaskID %q was accepted", taskID)
		}
	}
	if len(repo.reports) != 0 {
		t.Fatalf("invalid TaskID reached repository: %#v", repo.reports)
	}
}

func TestPipelineHighRiskWithoutEvidenceReaderDoesNotPretendSuccess(t *testing.T) {
	p := NewPipeline(Options{Policy: domainverification.VerificationPolicy{Enabled: true}})
	result, err := p.VerifyResponse(context.Background(), Request{
		DraftResponse: "ニュースでは新モデルが2026年に発表されました。",
		UserMessage:   "ニュースを要約して",
		Route:         "RESEARCH",
		SessionID:     "session-1",
		TaskID:        modulecore.NewTaskID(),
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
		TaskID:        modulecore.NewTaskID(),
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
		TaskID:        modulecore.NewTaskID(),
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
	result, err := p.VerifyResponse(context.Background(), Request{
		DraftResponse: "ニュースでは新モデルが2026年に発表されました。",
		UserMessage:   "ニュース",
		SessionID:     "session-1",
		TaskID:        modulecore.NewTaskID(),
	})
	if err == nil {
		t.Fatal("expected persistence error")
	}
	// The store failure is written onto the report as a diagnostic after the Save, and that
	// diagnostic sits outside the digested body, so the returned report still carries the
	// digest of its own content instead of an unstamped or borrowed hash.
	if err := result.Report.Validate(); err != nil {
		t.Fatalf("the annotated report must still match its own content digest, got %v", err)
	}
	if result.Report.ErrorKind != domainverification.ErrorPersistenceFailed {
		t.Fatalf("expected the persistence diagnostic, got %+v", result.Report)
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

// TestPipelineStampsContentHashOnFinalReportBody pins where the producer sets the digest:
// the counts and the overall status only become final after the claims are recounted, so
// the pipeline stamps the body it is about to persist. The stored row and the report handed
// back then carry the digest of the content they actually hold.
func TestPipelineStampsContentHashOnFinalReportBody(t *testing.T) {
	repo := &stubRepository{}
	p := NewPipeline(Options{
		Policy:     domainverification.VerificationPolicy{Enabled: true},
		Repository: repo,
		EvidenceReader: stubEvidenceReader{evidence: []domainverification.EvidenceRef{{
			ID:          "ev-1",
			SourceType:  domainverification.EvidenceVectorKB,
			SourceID:    "kb:movie",
			Supports:    true,
			RetrievedAt: time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC),
		}}},
	})
	result, err := p.VerifyResponse(context.Background(), Request{
		DraftResponse: "この作品は2014年公開です。",
		UserMessage:   "作品情報を教えて",
		SessionID:     "session-1",
		TaskID:        modulecore.NewTaskID(),
		Now:           time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("VerifyResponse failed: %v", err)
	}
	if len(repo.reports) != 1 {
		t.Fatalf("expected exactly one saved report, got %+v", repo.reports)
	}
	saved := repo.reports[0]
	if saved.ArtifactID != result.Report.ArtifactID {
		t.Fatalf("the saved row and the returned report are different artifacts: %s vs %s", saved.ArtifactID, result.Report.ArtifactID)
	}
	if saved.ContentHash == "" || result.Report.ContentHash == "" {
		t.Fatalf("the producer must stamp the final body: saved=%q returned=%q", saved.ContentHash, result.Report.ContentHash)
	}
	// The persisted value has to be the digest of the persisted body, so a caller can verify
	// a row it read back without recomputing or replacing anything.
	if err := saved.Validate(); err != nil {
		t.Fatalf("saved report must validate: %v", err)
	}
	if err := result.Report.Validate(); err != nil {
		t.Fatalf("returned report must validate: %v", err)
	}
	if saved.ContentHash != result.Report.ContentHash {
		t.Fatalf("saved hash %s differs from returned hash %s", saved.ContentHash, result.Report.ContentHash)
	}
	// The stamped value covers the body only: reminting the ArtifactID and appending a
	// successor later describes the same content, so it keeps the same digest.
	reminted := saved
	reminted.ArtifactID = modulecore.NewArtifactID()
	reminted.SupersededBy = modulecore.NewArtifactID()
	if reminted.ArtifactID == saved.ArtifactID {
		t.Fatal("a reminted artifact id must differ")
	}
	if got, want := verificationReportDigestOf(t, reminted), saved.ContentHash; got != want {
		t.Fatalf("identity metadata changed the stamped digest: got %s want %s", got, want)
	}
}

// TestPipelineStampsReportsReturnedWithoutASave covers the runs that never reach the store:
// the disabled policy and the run with no repository. Their reports are handed back as they
// are, so they still carry the digest of their own body.
func TestPipelineStampsReportsReturnedWithoutASave(t *testing.T) {
	disabled, err := NewPipeline(Options{Policy: domainverification.VerificationPolicy{Enabled: false}}).
		VerifyResponse(context.Background(), Request{DraftResponse: "これは2014年公開です。", SessionID: "session-1", TaskID: modulecore.NewTaskID()})
	if err != nil {
		t.Fatalf("disabled run failed: %v", err)
	}
	if err := disabled.Report.Validate(); err != nil {
		t.Fatalf("the disabled report must carry its own content digest, got %v", err)
	}
	if disabled.Report.ErrorKind != domainverification.ErrorVerifierDisabled {
		t.Fatalf("the disabled diagnostic must stay, got %+v", disabled.Report)
	}

	noStore, err := NewPipeline(Options{Policy: domainverification.VerificationPolicy{Enabled: true}}).
		VerifyResponse(context.Background(), Request{DraftResponse: "ニュースでは新モデルが2026年に発表されました。", UserMessage: "ニュースを教えて", SessionID: "session-1", TaskID: modulecore.NewTaskID()})
	if err != nil {
		t.Fatalf("run without a repository failed: %v", err)
	}
	if noStore.Report.ClaimCount == 0 {
		t.Fatalf("expected claims for a high-risk draft, got %+v", noStore.Report)
	}
	if err := noStore.Report.Validate(); err != nil {
		t.Fatalf("the report returned without a Save must carry its own content digest, got %v", err)
	}
}

// TestPipelineDoesNotSaveAReportWhoseBodyCannotBeEncoded covers the one body field that
// encoding/json can refuse: an evidence timestamp outside the encodable range. The run has
// to report the encoding failure instead of persisting a row whose digest was never
// computed over the bytes that would have been written.
func TestPipelineDoesNotSaveAReportWhoseBodyCannotBeEncoded(t *testing.T) {
	repo := &stubRepository{}
	p := NewPipeline(Options{
		Policy:     domainverification.VerificationPolicy{Enabled: true},
		Repository: repo,
		EvidenceReader: stubEvidenceReader{evidence: []domainverification.EvidenceRef{{
			ID:          "ev-1",
			SourceType:  domainverification.EvidenceVectorKB,
			Supports:    true,
			RetrievedAt: time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC),
		}}},
	})
	_, err := p.VerifyResponse(context.Background(), Request{
		DraftResponse: "この作品は2014年公開です。",
		UserMessage:   "作品情報を教えて",
		SessionID:     "session-1",
		TaskID:        modulecore.NewTaskID(),
	})
	if err == nil {
		t.Fatal("a body that cannot be encoded must return an error instead of a stored hash")
	}
	if !strings.Contains(err.Error(), "content hash") {
		t.Fatalf("err=%v, want it to name the content hash it could not compute", err)
	}
	if len(repo.reports) != 0 {
		t.Fatalf("Save must not be called when the body digest could not be computed, got %+v", repo.reports)
	}
}

func verificationReportDigestOf(t *testing.T, item domainverification.VerificationReport) string {
	t.Helper()
	digest, err := domainverification.ComputeVerificationReportContentHash(item)
	if err != nil {
		t.Fatalf("compute report content hash: %v", err)
	}
	return digest
}

// TestPipelineStampsReportsThatSkipClaimEvaluation covers the two runs that stop before any
// claim is evaluated: the extractor returning no claims and the extractor failing. Both hand
// a finished body to the store, so the saved row and the returned report keep the digest of
// that body, and the extraction diagnostic stays outside the digested content.
func TestPipelineStampsReportsThatSkipClaimEvaluation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		extractor   stubClaimExtractor
		wantStatus  domainverification.VerificationStatus
		wantSkip    string
		wantErrKind domainverification.ErrorKind
	}{
		{
			name:       "no claims",
			extractor:  stubClaimExtractor{},
			wantStatus: domainverification.StatusNotChecked,
			wantSkip:   "no factual claims",
		},
		{
			name:        "extraction error",
			extractor:   stubClaimExtractor{err: errors.New("extractor unavailable")},
			wantStatus:  domainverification.StatusNotChecked,
			wantErrKind: domainverification.ErrorClaimExtractionFailed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &stubRepository{}
			p := NewPipeline(Options{
				Policy:     domainverification.VerificationPolicy{Enabled: true},
				Repository: repo,
				Extractor:  tc.extractor,
			})
			result, err := p.VerifyResponse(context.Background(), Request{
				DraftResponse: "この作品は2014年公開です。",
				UserMessage:   "作品情報を教えて",
				SessionID:     "session-1",
				TaskID:        modulecore.NewTaskID(),
			})
			if err != nil {
				t.Fatalf("VerifyResponse failed: %v", err)
			}
			if result.Report.Status != tc.wantStatus {
				t.Fatalf("status=%s, want %s", result.Report.Status, tc.wantStatus)
			}
			if result.Report.SkipReason != tc.wantSkip {
				t.Fatalf("skip_reason=%q, want %q", result.Report.SkipReason, tc.wantSkip)
			}
			if result.Report.ErrorKind != tc.wantErrKind {
				t.Fatalf("error_kind=%s, want %s", result.Report.ErrorKind, tc.wantErrKind)
			}
			if len(repo.reports) != 1 {
				t.Fatalf("expected exactly one saved report, got %+v", repo.reports)
			}
			saved := repo.reports[0]
			if saved.ContentHash == "" || saved.ContentHash != result.Report.ContentHash {
				t.Fatalf("saved=%q returned=%q, want the same stamped digest", saved.ContentHash, result.Report.ContentHash)
			}
			if got, want := saved.ContentHash, verificationReportDigestOf(t, saved); got != want {
				t.Fatalf("saved hash %s is not the digest of the saved body %s", got, want)
			}
			if err := result.Report.Validate(); err != nil {
				t.Fatalf("returned report must validate, got %v", err)
			}
		})
	}
}
