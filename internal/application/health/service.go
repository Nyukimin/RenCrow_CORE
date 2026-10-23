package health

import (
	"context"
	"sync"

	domainhealth "github.com/Nyukimin/RenCrow_CORE/internal/domain/health"
)

// HealthService はヘルスチェックを管理・実行するサービス
type HealthService struct {
	checks []domainhealth.Check
}

// NewHealthService は新しい HealthService を作成
func NewHealthService(checks ...domainhealth.Check) *HealthService {
	return &HealthService{checks: checks}
}

// RunChecks は全チェックを並行実行し HealthReport を返す
func (s *HealthService) RunChecks(ctx context.Context) domainhealth.HealthReport {
	results := make([]domainhealth.CheckResult, len(s.checks))
	var wg sync.WaitGroup

	for i, check := range s.checks {
		wg.Add(1)
		go func(idx int, c domainhealth.Check) {
			defer wg.Done()
			results[idx] = c.Run(ctx)
		}(i, check)
	}

	wg.Wait()
	return domainhealth.Aggregate(results)
}

type readinessOptional interface {
	ReadinessOptional() bool
}

// IsReady は全必須チェックが OK かどうかを返す。ReadinessOptionalな
// チェックのdegradedだけは受付可否に影響しない。
func (s *HealthService) IsReady(ctx context.Context) bool {
	report := s.RunChecks(ctx)
	for i, result := range report.Checks {
		switch result.Status {
		case domainhealth.StatusOK:
			continue
		case domainhealth.StatusDegraded:
			if i < len(s.checks) {
				if optional, ok := s.checks[i].(readinessOptional); ok && optional.ReadinessOptional() {
					continue
				}
			}
			return false
		default:
			return false
		}
	}
	return true
}
