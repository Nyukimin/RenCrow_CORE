package verification

import (
	"context"
	"fmt"
	"strings"
	"time"

	domainverification "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/verification"
)

type Request struct {
	DraftResponse string
	UserMessage   string
	Route         string
	SessionID     string
	Channel       string
	ChatID        string
	JobID         string
	Now           time.Time
}

type Result struct {
	Response string
	Report   domainverification.VerificationReport
}

type ClaimExtractor interface {
	ExtractClaims(ctx context.Context, req Request, level domainverification.TriggerLevel) ([]domainverification.Claim, error)
}

type EvidenceReader interface {
	ReadEvidence(ctx context.Context, claim domainverification.Claim, req Request) ([]domainverification.EvidenceRef, error)
}

type ReportRepository interface {
	Save(ctx context.Context, report domainverification.VerificationReport) error
}

type Pipeline struct {
	enabled        bool
	mode           string
	defaultLevel   domainverification.TriggerLevel
	extractor      ClaimExtractor
	evidenceReader EvidenceReader
	repository     ReportRepository
	now            func() time.Time
}

type Options struct {
	Policy         domainverification.VerificationPolicy
	Extractor      ClaimExtractor
	EvidenceReader EvidenceReader
	Repository     ReportRepository
	Now            func() time.Time
}

func NewPipeline(opts Options) *Pipeline {
	policy := opts.Policy.Normalized()
	extractor := opts.Extractor
	if extractor == nil {
		extractor = DefaultClaimExtractor{}
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Pipeline{
		enabled:        policy.Enabled,
		mode:           strings.TrimSpace(policy.Mode),
		defaultLevel:   policy.Default,
		extractor:      extractor,
		evidenceReader: opts.EvidenceReader,
		repository:     opts.Repository,
		now:            now,
	}
}

func (p *Pipeline) VerifyResponse(ctx context.Context, req Request) (Result, error) {
	req = p.normalizeRequest(req)
	if !p.enabled {
		report := p.newReport(req, domainverification.TriggerLow)
		report.Status = domainverification.StatusNotChecked
		report.SkipReason = "verifier disabled"
		report.ErrorKind = domainverification.ErrorVerifierDisabled
		return Result{Response: req.DraftResponse, Report: report}, nil
	}

	level := DetermineTriggerLevel(req, p.defaultLevel)
	report := p.newReport(req, level)
	claims, err := p.extractor.ExtractClaims(ctx, req, level)
	if err != nil {
		report.Status = domainverification.StatusNotChecked
		report.ErrorKind = domainverification.ErrorClaimExtractionFailed
		report.Error = err.Error()
		return p.finish(ctx, req, report)
	}
	if len(claims) == 0 {
		report.Status = domainverification.StatusNotChecked
		report.SkipReason = "no factual claims"
		return p.finish(ctx, req, report)
	}

	report.Claims = make([]domainverification.Claim, 0, len(claims))
	report.Questions = make([]domainverification.VerificationQuestion, 0, len(claims))
	for i, claim := range claims {
		if claim.Priority == "" {
			claim.Priority = level
		}
		if claim.Status == "" {
			claim.Status = domainverification.StatusNotChecked
		}
		report.Questions = append(report.Questions, domainverification.VerificationQuestion{
			ID:      domainverification.VerificationQuestionID(fmt.Sprintf("vq_%03d", i+1)),
			ClaimID: claim.ID,
			Query:   buildVerificationQuery(claim.Text),
		})
		claim = p.evaluateClaim(ctx, req, claim)
		report.Claims = append(report.Claims, claim)
		report.Evidence = append(report.Evidence, claim.Evidence...)
	}
	recountReport(&report)
	report.Status = overallStatus(report.Claims)
	response := req.DraftResponse
	if p.mode != "dry_run" {
		response = reviseResponse(req.DraftResponse, report)
	}
	result := Result{Response: response, Report: report}
	return p.persist(ctx, result)
}

func (p *Pipeline) normalizeRequest(req Request) Request {
	req.DraftResponse = strings.TrimSpace(req.DraftResponse)
	req.UserMessage = strings.TrimSpace(req.UserMessage)
	req.Route = strings.TrimSpace(req.Route)
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.Channel = strings.TrimSpace(req.Channel)
	req.ChatID = strings.TrimSpace(req.ChatID)
	req.JobID = strings.TrimSpace(req.JobID)
	if req.Now.IsZero() {
		req.Now = p.now()
	}
	if req.JobID == "" {
		req.JobID = "job_unknown"
	}
	if req.SessionID == "" {
		req.SessionID = "session_unknown"
	}
	return req
}

func (p *Pipeline) newReport(req Request, level domainverification.TriggerLevel) domainverification.VerificationReport {
	return domainverification.VerificationReport{
		ID:           "verify_" + req.JobID,
		JobID:        req.JobID,
		SessionID:    req.SessionID,
		Route:        req.Route,
		Status:       domainverification.StatusNotChecked,
		TriggerLevel: level,
		CreatedAt:    req.Now,
	}
}

func (p *Pipeline) evaluateClaim(ctx context.Context, req Request, claim domainverification.Claim) domainverification.Claim {
	if p.evidenceReader == nil {
		claim.Status = domainverification.StatusNotChecked
		claim.Reason = "evidence reader unavailable"
		return claim
	}
	evidence, err := p.evidenceReader.ReadEvidence(ctx, claim, req)
	if err != nil {
		claim.Status = domainverification.StatusNotChecked
		claim.Reason = "evidence unavailable: " + err.Error()
		return claim
	}
	claim.Evidence = evidence
	if len(evidence) == 0 {
		if claim.Priority == domainverification.TriggerHigh {
			claim.Status = domainverification.StatusUnsupported
			claim.Reason = "no supporting evidence"
			return claim
		}
		claim.Status = domainverification.StatusNotChecked
		claim.Reason = "no evidence returned"
		return claim
	}
	supports := 0
	conflicts := 0
	for _, ev := range evidence {
		if ev.Conflicts {
			conflicts++
		}
		if ev.Supports {
			supports++
		}
	}
	switch {
	case conflicts > 0:
		claim.Status = domainverification.StatusConflict
		claim.Reason = "conflicting evidence"
	case supports > 1:
		claim.Status = domainverification.StatusVerified
		claim.Reason = "multiple supporting evidence refs"
	case supports == 1:
		claim.Status = domainverification.StatusWeaklySupported
		claim.Reason = "single supporting evidence ref"
	default:
		claim.Status = domainverification.StatusUnsupported
		claim.Reason = "evidence did not support claim"
	}
	return claim
}

func (p *Pipeline) finish(ctx context.Context, req Request, report domainverification.VerificationReport) (Result, error) {
	recountReport(&report)
	return p.persist(ctx, Result{Response: req.DraftResponse, Report: report})
}

func (p *Pipeline) persist(ctx context.Context, result Result) (Result, error) {
	if p.repository == nil {
		return result, nil
	}
	if err := p.repository.Save(ctx, result.Report); err != nil {
		result.Report.ErrorKind = domainverification.ErrorPersistenceFailed
		result.Report.Error = err.Error()
		return result, fmt.Errorf("save verification report: %w", err)
	}
	return result, nil
}

func recountReport(r *domainverification.VerificationReport) {
	r.ClaimCount = len(r.Claims)
	r.VerifiedCount = 0
	r.WeakCount = 0
	r.UnsupportedCount = 0
	r.ConflictCount = 0
	r.NotCheckedCount = 0
	for _, claim := range r.Claims {
		switch claim.Status {
		case domainverification.StatusVerified:
			r.VerifiedCount++
		case domainverification.StatusWeaklySupported:
			r.WeakCount++
		case domainverification.StatusUnsupported:
			r.UnsupportedCount++
		case domainverification.StatusConflict:
			r.ConflictCount++
		case domainverification.StatusNotChecked:
			r.NotCheckedCount++
		}
	}
}

func overallStatus(claims []domainverification.Claim) domainverification.VerificationStatus {
	if len(claims) == 0 {
		return domainverification.StatusNotChecked
	}
	status := domainverification.StatusVerified
	for _, claim := range claims {
		switch claim.Status {
		case domainverification.StatusConflict:
			return domainverification.StatusConflict
		case domainverification.StatusUnsupported:
			status = domainverification.StatusUnsupported
		case domainverification.StatusWeaklySupported:
			if status != domainverification.StatusUnsupported {
				status = domainverification.StatusWeaklySupported
			}
		case domainverification.StatusNotChecked:
			if status == domainverification.StatusVerified {
				status = domainverification.StatusNotChecked
			}
		}
	}
	return status
}

func buildVerificationQuery(text string) string {
	return "Verify the claim using independent evidence: " + strings.TrimSpace(text)
}

func reviseResponse(draft string, report domainverification.VerificationReport) string {
	if report.Status == domainverification.StatusConflict {
		return draft + "\n\n[verification: conflict detected]"
	}
	if report.Status == domainverification.StatusUnsupported && report.TriggerLevel == domainverification.TriggerHigh {
		return draft + "\n\n[verification: unsupported claims require caution]"
	}
	return draft
}
