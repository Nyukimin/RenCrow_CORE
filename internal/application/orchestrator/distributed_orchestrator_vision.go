package orchestrator

import domainvision "github.com/Nyukimin/RenCrow_CORE/internal/domain/vision"

// SetVisionAnalyzer installs the only raw image/video recognition path used by CORE.
func (o *DistributedOrchestrator) SetVisionAnalyzer(analyzer domainvision.Analyzer, options VisionOptions) {
	o.visionRequests = newVisionRequestProcessor(analyzer, options)
}
