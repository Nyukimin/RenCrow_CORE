package autonomous

import (
	"context"
	"errors"
	"testing"

	domaincontract "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/contract"
	domainexecution "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/execution"
)

type plannerStub struct {
	plan Plan
	err  error
}

func (s plannerStub) Plan(_ context.Context, _ domaincontract.Contract) (Plan, error) {
	return s.plan, s.err
}

type applierStub struct {
	calls int
	errAt map[string]error
}

func (s *applierStub) Apply(_ context.Context, step Step) error {
	s.calls++
	if s.errAt == nil {
		return nil
	}
	if err, ok := s.errAt[step.Name]; ok {
		return err
	}
	return nil
}

type verifierStub struct {
	seq []verifyResult
	i   int
}

type verifyResult struct {
	ok     bool
	reason string
	err    error
}

func (s *verifierStub) Verify(_ context.Context, _ domaincontract.Contract) (bool, string, error) {
	if len(s.seq) == 0 {
		return true, "", nil
	}
	idx := s.i
	if idx >= len(s.seq) {
		idx = len(s.seq) - 1
	}
	r := s.seq[idx]
	s.i++
	return r.ok, r.reason, r.err
}

type repairerStub struct {
	calls int
	plan  Plan
	err   error
}

func (s *repairerStub) Repair(_ context.Context, _ domaincontract.Contract, _ string) (Plan, error) {
	s.calls++
	return s.plan, s.err
}

type reportStoreStub struct {
	calls int
	last  domainexecution.ExecutionReport
}

func (s *reportStoreStub) Save(_ context.Context, report domainexecution.ExecutionReport) error {
	s.calls++
	s.last = report
	return nil
}

func testContract() domaincontract.Contract {
	return domaincontract.Contract{
		Goal:         "TTS実装して",
		Acceptance:   []string{"音声ファイル生成成功"},
		Constraints:  []string{"破壊的操作禁止"},
		Artifacts:    []string{"execution_report.json"},
		Verification: []string{"再生成功"},
		Rollback:     []string{"元に戻す"},
	}
}

func TestServiceRun_VerifyPass(t *testing.T) {
	svc := NewService(
		plannerStub{plan: Plan{Steps: []Step{{Name: "apply"}}}},
		&applierStub{},
		&verifierStub{seq: []verifyResult{{ok: true}}},
		&repairerStub{},
		1,
	)

	report, err := svc.Run(context.Background(), testContract())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if report.Status != StatusPassed {
		t.Fatalf("expected passed, got %s", report.Status)
	}
	if report.RepairCount != 0 {
		t.Fatalf("expected no repair, got %d", report.RepairCount)
	}
}

func TestServiceRun_RepairThenPass(t *testing.T) {
	repair := &repairerStub{plan: Plan{Steps: []Step{{Name: "repair-step"}}}}
	svc := NewService(
		plannerStub{plan: Plan{Steps: []Step{{Name: "apply-step"}}}},
		&applierStub{},
		&verifierStub{seq: []verifyResult{{ok: false, reason: "tts playback failed"}, {ok: true}}},
		repair,
		2,
	)

	report, err := svc.Run(context.Background(), testContract())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if report.Status != StatusPassed {
		t.Fatalf("expected passed, got %s", report.Status)
	}
	if report.RepairCount != 1 {
		t.Fatalf("expected repair once, got %d", report.RepairCount)
	}
	if repair.calls != 1 {
		t.Fatalf("expected repair call once, got %d", repair.calls)
	}
}

func TestServiceRun_FailWhenRepairExhausted(t *testing.T) {
	repair := &repairerStub{plan: Plan{Steps: []Step{{Name: "repair-step"}}}}
	svc := NewService(
		plannerStub{plan: Plan{Steps: []Step{{Name: "apply-step"}}}},
		&applierStub{},
		&verifierStub{seq: []verifyResult{{ok: false, reason: "still failed"}, {ok: false, reason: "still failed"}}},
		repair,
		1,
	)

	report, err := svc.Run(context.Background(), testContract())
	if err == nil {
		t.Fatal("expected error when repair exhausted")
	}
	if report.Status != StatusFailed {
		t.Fatalf("expected failed, got %s", report.Status)
	}
	if repair.calls != 1 {
		t.Fatalf("expected one repair call, got %d", repair.calls)
	}
}

func TestServiceRun_FailOnApplyError(t *testing.T) {
	svc := NewService(
		plannerStub{plan: Plan{Steps: []Step{{Name: "apply-step"}}}},
		&applierStub{errAt: map[string]error{"apply-step": errors.New("apply failed")}},
		&verifierStub{seq: []verifyResult{{ok: true}}},
		&repairerStub{},
		1,
	)

	report, err := svc.Run(context.Background(), testContract())
	if err == nil {
		t.Fatal("expected apply error")
	}
	if report.Status != StatusFailed {
		t.Fatalf("expected failed, got %s", report.Status)
	}
}

func TestServiceRun_SavesExecutionReport(t *testing.T) {
	store := &reportStoreStub{}
	svc := NewService(
		plannerStub{plan: Plan{Steps: []Step{{Name: "apply-step"}}}},
		&applierStub{},
		&verifierStub{seq: []verifyResult{{ok: true}}},
		&repairerStub{},
		1,
	).WithReportStore(store)

	_, err := svc.Run(context.Background(), testContract())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("expected one report save, got %d", store.calls)
	}
	if store.last.Status != "passed" {
		t.Fatalf("expected report status passed, got %s", store.last.Status)
	}
	if store.last.Goal == "" || store.last.JobID == "" {
		t.Fatalf("report fields are incomplete: %+v", store.last)
	}
	if len(store.last.Steps) == 0 || store.last.Steps[0] != "apply-step" {
		t.Fatalf("expected steps to be recorded, got %+v", store.last.Steps)
	}
	if len(store.last.Verification) == 0 {
		t.Fatalf("expected verification logs, got %+v", store.last.Verification)
	}
}

func TestServiceRun_SavesFailureDetails(t *testing.T) {
	store := &reportStoreStub{}
	svc := NewService(
		plannerStub{plan: Plan{Steps: []Step{{Name: "apply-step"}}}},
		&applierStub{},
		&verifierStub{seq: []verifyResult{{ok: false, reason: "playback failed"}, {ok: false, reason: "playback failed"}}},
		&repairerStub{plan: Plan{Steps: []Step{{Name: "repair-step"}}}},
		1,
	).WithReportStore(store)

	_, err := svc.Run(context.Background(), testContract())
	if err == nil {
		t.Fatal("expected failure")
	}
	if store.calls != 1 {
		t.Fatalf("expected one report save, got %d", store.calls)
	}
	if store.last.Status != "failed" {
		t.Fatalf("expected failed status, got %s", store.last.Status)
	}
	if store.last.Error == "" {
		t.Fatalf("expected error details, got %+v", store.last)
	}
	if len(store.last.Verification) < 2 {
		t.Fatalf("expected failure verification logs, got %+v", store.last.Verification)
	}
	if store.last.ErrorKind != "verify" {
		t.Fatalf("expected error kind verify, got %s", store.last.ErrorKind)
	}
}

func TestServiceRun_SavesApplyErrorKind(t *testing.T) {
	store := &reportStoreStub{}
	svc := NewService(
		plannerStub{plan: Plan{Steps: []Step{{Name: "apply-step"}}}},
		&applierStub{errAt: map[string]error{"apply-step": errors.New("apply failed")}},
		&verifierStub{seq: []verifyResult{{ok: true}}},
		&repairerStub{},
		1,
	).WithReportStore(store)

	_, err := svc.Run(context.Background(), testContract())
	if err == nil {
		t.Fatal("expected apply failure")
	}
	if store.last.ErrorKind != "apply" {
		t.Fatalf("expected error kind apply, got %s", store.last.ErrorKind)
	}
}
