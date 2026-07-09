package autonomous

import (
	"context"
	"fmt"
	"time"

	domaincontract "github.com/Nyukimin/RenCrow_CORE/internal/domain/contract"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
)

// Planner builds an executable plan from contract.
type Planner interface {
	Plan(ctx context.Context, c domaincontract.Contract) (Plan, error)
}

// Applier applies one plan step.
type Applier interface {
	Apply(ctx context.Context, step Step) error
}

// Verifier validates whether acceptance criteria are satisfied.
type Verifier interface {
	Verify(ctx context.Context, c domaincontract.Contract) (ok bool, reason string, err error)
}

// Repairer builds a repair plan from verification failure reason.
type Repairer interface {
	Repair(ctx context.Context, c domaincontract.Contract, reason string) (Plan, error)
}

// ReportStore persists execution evidence.
type ReportStore interface {
	Save(ctx context.Context, report domainexecution.ExecutionReport) error
}

// Step is a single executable action in a plan.
type Step struct {
	Name string
}

// Plan represents ordered execution steps.
type Plan struct {
	Steps []Step
}

// Status is execution result status.
type Status string

const (
	StatusPassed Status = "passed"
	StatusFailed Status = "failed"
)

// Report stores execution outcome evidence.
type Report struct {
	JobID           string
	Status          Status
	RepairCount     int
	AttemptCount    int
	ErrorKind       string
	Reason          string
	ExecutedSteps   []string
	VerificationLog []string
}

// Service executes Plan->Apply->Verify->Repair->Verify loop.
type Service struct {
	planner   Planner
	applier   Applier
	verifier  Verifier
	repairer  Repairer
	reporter  ReportStore
	maxRepair int
}

func NewService(planner Planner, applier Applier, verifier Verifier, repairer Repairer, maxRepair int) *Service {
	if maxRepair < 0 {
		maxRepair = 0
	}
	return &Service{
		planner:   planner,
		applier:   applier,
		verifier:  verifier,
		repairer:  repairer,
		maxRepair: maxRepair,
	}
}

func (s *Service) WithReportStore(store ReportStore) *Service {
	s.reporter = store
	return s
}

func (s *Service) Run(ctx context.Context, c domaincontract.Contract) (Report, error) {
	if err := c.Validate(); err != nil {
		return Report{Status: StatusFailed}, err
	}

	plan, err := s.planner.Plan(ctx, c)
	if err != nil {
		return Report{Status: StatusFailed}, err
	}

	report := Report{
		JobID:  fmt.Sprintf("job-%d", time.Now().UTC().UnixNano()),
		Status: StatusFailed,
	}

	for {
		report.AttemptCount++
		applied, err := s.applyPlan(ctx, plan)
		report.ExecutedSteps = append(report.ExecutedSteps, applied...)
		if err != nil {
			report.ErrorKind = "apply"
			report.Reason = err.Error()
			s.saveReport(ctx, c, report)
			return report, fmt.Errorf("apply failed: %w", err)
		}

		ok, reason, err := s.verifier.Verify(ctx, c)
		if err != nil {
			report.ErrorKind = "verify"
			report.Reason = err.Error()
			report.VerificationLog = append(report.VerificationLog, "verify:error:"+err.Error())
			s.saveReport(ctx, c, report)
			return report, fmt.Errorf("verify failed: %w", err)
		}
		if ok {
			report.Status = StatusPassed
			report.Reason = ""
			report.VerificationLog = append(report.VerificationLog, "verify:passed")
			s.saveReport(ctx, c, report)
			return report, nil
		}

		report.Reason = reason
		report.ErrorKind = "verify"
		report.VerificationLog = append(report.VerificationLog, "verify:failed:"+reason)
		if report.RepairCount >= s.maxRepair {
			s.saveReport(ctx, c, report)
			return report, fmt.Errorf("verification failed after %d repairs: %s", report.RepairCount, reason)
		}

		repairPlan, err := s.repairer.Repair(ctx, c, reason)
		if err != nil {
			report.ErrorKind = "repair"
			report.VerificationLog = append(report.VerificationLog, "repair:error:"+err.Error())
			s.saveReport(ctx, c, report)
			return report, fmt.Errorf("repair plan failed: %w", err)
		}
		report.RepairCount++
		plan = repairPlan
	}
}

func (s *Service) saveReport(ctx context.Context, c domaincontract.Contract, report Report) {
	if s.reporter == nil {
		return
	}
	now := time.Now().UTC()
	status := "failed"
	if report.Status == StatusPassed {
		status = "passed"
	}
	verification := make([]string, 0, len(c.Verification)+len(report.VerificationLog))
	verification = append(verification, c.Verification...)
	verification = append(verification, report.VerificationLog...)
	ev := domainexecution.ExecutionReport{
		JobID:        report.JobID,
		Goal:         c.Goal,
		Capability:   string(CapabilityGenericExecution),
		Status:       status,
		ErrorKind:    report.ErrorKind,
		Acceptance:   c.Acceptance,
		Constraints:  c.Constraints,
		Artifacts:    c.Artifacts,
		Verification: verification,
		Rollback:     c.Rollback,
		Steps:        append([]string{}, report.ExecutedSteps...),
		AttemptCount: report.AttemptCount,
		RepairCount:  report.RepairCount,
		FailureReason: report.Reason,
		Error:        report.Reason,
		CreatedAt:    now,
		FinishedAt:   now,
	}
	_ = s.reporter.Save(ctx, ev)
}

func (s *Service) applyPlan(ctx context.Context, plan Plan) ([]string, error) {
	applied := make([]string, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		applied = append(applied, step.Name)
		if err := s.applier.Apply(ctx, step); err != nil {
			return applied, fmt.Errorf("step=%s: %w", step.Name, err)
		}
	}
	return applied, nil
}
