package service

import (
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	moduleworker "github.com/Nyukimin/RenCrow_CORE/modules/worker"
)

func (w *workerExecutionService) classifyExecutionFailure(result *patch.PatchExecutionResult) {
	if result == nil || result.Success || len(result.Results) == 0 {
		return
	}
	for idx, cr := range result.Results {
		if cr.Success {
			continue
		}
		classification := classifyFailure(cr.Error, cr.Output)
		result.FailedIndex = idx
		result.WithFailureMetadata(classification.Kind, classification.Reason, classification.Retryable)
		return
	}
	result.WithFailureMetadata("unknown", "execution failed", false)
}

func classifyFailure(errText, output string) moduleworker.ExecutionFailureClassification {
	return moduleworker.ClassifyExecutionFailure(errText, output)
}
