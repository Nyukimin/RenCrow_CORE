package orchestrator

import (
	"context"
	"log"
	"strings"
	"time"

	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
)

type distributedEvidenceReporter struct {
	reporter ReportStore
}

func newDistributedEvidenceReporter(reporter ReportStore) *distributedEvidenceReporter {
	return &distributedEvidenceReporter{reporter: reporter}
}

func (r *distributedEvidenceReporter) SetReportStore(store ReportStore) {
	r.reporter = store
}

func (r *distributedEvidenceReporter) Save(ctx context.Context, jobID, goal, route string, startedAt, finishedAt time.Time, runErr error) {
	if r.reporter == nil || strings.TrimSpace(jobID) == "" || strings.TrimSpace(goal) == "" {
		return
	}
	report := domainexecution.ExecutionReport{
		JobID:        jobID,
		Goal:         goal,
		Route:        strings.ToUpper(strings.TrimSpace(route)),
		Status:       "passed",
		ErrorKind:    "",
		Acceptance:   distributedAcceptance(route),
		Verification: distributedVerification(route, runErr),
		Steps:        distributedEvidenceSteps(route, runErr),
		AttemptCount: 1,
		RepairCount:  0,
		Error:        "",
		CreatedAt:    startedAt,
		FinishedAt:   finishedAt,
	}
	if runErr != nil {
		report.Status = "failed"
		report.ErrorKind = distributedEvidenceErrorKind(runErr)
		report.Error = runErr.Error()
	}
	if err := r.reporter.Save(ctx, report); err != nil {
		log.Printf("[DistributedOrch] evidence save failed: job=%s err=%v", jobID, err)
	}
}

func distributedAcceptance(route string) []string {
	items := []string{"ルーティング完了", "最終応答生成"}
	switch strings.ToUpper(strings.TrimSpace(route)) {
	case "CHAT":
		items = append(items, "Mio 応答完了")
	case "OPS":
		items = append(items, "Worker 応答完了")
	case "CODE", "CODE1", "CODE2", "CODE3", "CODE4":
		items = append(items, "Coder 実行完了", "Worker 取りまとめ完了")
	default:
		items = append(items, "Agent 応答完了")
	}
	return items
}

func distributedVerification(route string, runErr error) []string {
	items := []string{"viewer jobs に記録されること"}
	if strings.TrimSpace(route) != "" {
		items = append(items, "route="+strings.ToUpper(strings.TrimSpace(route)))
	}
	if runErr == nil {
		items = append(items, "final:passed")
	} else {
		items = append(items, "final:failed")
	}
	return items
}

func distributedEvidenceSteps(route string, runErr error) []string {
	items := []string{"message.received", "routing.decision"}
	switch strings.ToUpper(strings.TrimSpace(route)) {
	case "CHAT":
		items = append(items, "mio.chat")
	case "OPS":
		items = append(items, "shiro.execute")
	case "CODE", "CODE1", "CODE2", "CODE3", "CODE4":
		items = append(items, "shiro.delegate", "coder.execute", "shiro.verify")
	default:
		items = append(items, "agent.execute")
	}
	if runErr != nil {
		items = append(items, "error")
	} else {
		items = append(items, "done")
	}
	return items
}

func distributedEvidenceErrorKind(runErr error) string {
	if runErr == nil {
		return ""
	}
	lower := strings.ToLower(runErr.Error())
	switch {
	case strings.Contains(lower, "verify"):
		return "verify"
	case strings.Contains(lower, "repair"), strings.Contains(lower, "retry"):
		return "repair"
	case strings.Contains(lower, "patch"), strings.Contains(lower, "command"), strings.Contains(lower, "timeout"), strings.Contains(lower, "error"):
		return "apply"
	default:
		return "other"
	}
}
