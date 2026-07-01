package health

import (
	"context"
	"time"
)

// Status はヘルスチェックの結果ステータス
type Status string

const (
	StatusOK       Status = "ok"
	StatusDegraded Status = "degraded"
	StatusDown     Status = "down"
)

// Check はヘルスチェック項目のインタフェース
type Check interface {
	Name() string
	Run(ctx context.Context) CheckResult
}

// CheckResult は個別チェックの結果
type CheckResult struct {
	Name     string        `json:"name"`
	Status   Status        `json:"status"`
	Message  string        `json:"message,omitempty"`
	Duration time.Duration `json:"duration_ms"`
}

// HealthReport は全チェック結果の集約
type HealthReport struct {
	Status    Status        `json:"status"`
	Checks   []CheckResult `json:"checks"`
	Timestamp time.Time    `json:"timestamp"`
}

// Aggregate は複数の CheckResult を HealthReport に集約する
func Aggregate(results []CheckResult) HealthReport {
	overall := StatusOK
	for _, r := range results {
		switch r.Status {
		case StatusDown:
			overall = StatusDown
		case StatusDegraded:
			if overall != StatusDown {
				overall = StatusDegraded
			}
		}
	}
	return HealthReport{
		Status:    overall,
		Checks:    results,
		Timestamp: time.Now(),
	}
}
