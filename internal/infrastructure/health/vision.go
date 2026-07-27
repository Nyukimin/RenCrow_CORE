package health

import (
	"context"
	"fmt"
	"time"

	domainhealth "github.com/Nyukimin/RenCrow_CORE/internal/domain/health"
	domainvision "github.com/Nyukimin/RenCrow_CORE/internal/domain/vision"
)

type VisionCheck struct {
	analyzer domainvision.Analyzer
}

func NewVisionCheck(analyzer domainvision.Analyzer) *VisionCheck {
	return &VisionCheck{analyzer: analyzer}
}

func (c *VisionCheck) Name() string {
	return "rencrow_vision"
}

func (c *VisionCheck) Run(ctx context.Context) domainhealth.CheckResult {
	started := time.Now()
	if c.analyzer == nil {
		return domainhealth.CheckResult{
			Name: c.Name(), Status: domainhealth.StatusDown,
			Message: "RenCrow_Vision analyzer is not configured", Duration: time.Since(started),
		}
	}
	report, err := c.analyzer.Health(ctx)
	if err != nil {
		return domainhealth.CheckResult{
			Name: c.Name(), Status: domainhealth.StatusDown,
			Message: err.Error(), Duration: time.Since(started),
		}
	}
	return domainhealth.CheckResult{
		Name: c.Name(), Status: domainhealth.StatusOK,
		Message:  fmt.Sprintf("ready provider=%s model=%s", report.Provider, report.Model),
		Duration: time.Since(started),
	}
}
