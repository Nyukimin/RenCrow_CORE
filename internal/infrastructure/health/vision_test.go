package health

import (
	"context"
	"errors"
	"testing"

	domainhealth "github.com/Nyukimin/RenCrow_CORE/internal/domain/health"
	domainvision "github.com/Nyukimin/RenCrow_CORE/internal/domain/vision"
)

type visionHealthStub struct {
	report domainvision.HealthReport
	err    error
}

func (s visionHealthStub) Analyze(context.Context, domainvision.AnalyzeRequest) (domainvision.AnalyzeResult, error) {
	return domainvision.AnalyzeResult{}, nil
}

func (s visionHealthStub) Health(context.Context) (domainvision.HealthReport, error) {
	return s.report, s.err
}

func TestVisionCheckReportsReady(t *testing.T) {
	check := NewVisionCheck(visionHealthStub{report: domainvision.HealthReport{
		OK: true, Status: "ready", Provider: "openai_compatible", Model: "Vision",
		Ready: domainvision.ReadyState{ModelLoaded: true},
	}})
	result := check.Run(context.Background())
	if result.Status != domainhealth.StatusOK {
		t.Fatalf("result = %+v", result)
	}
}

func TestVisionCheckReportsDownWithoutFallback(t *testing.T) {
	check := NewVisionCheck(visionHealthStub{err: errors.New("Wild unavailable")})
	result := check.Run(context.Background())
	if result.Status != domainhealth.StatusDown {
		t.Fatalf("result = %+v", result)
	}
}
