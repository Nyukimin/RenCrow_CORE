package orchestrator

import (
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domainverification "github.com/Nyukimin/RenCrow_CORE/internal/domain/verification"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type messageResponseAssembler struct{}

func (messageResponseAssembler) Build(response string, decision routing.Decision, taskID modulecore.TaskID) ProcessMessageResponse {
	return messageResponseAssembler{}.BuildWithVerification(response, decision, taskID, nil)
}

func (messageResponseAssembler) BuildWithVerification(response string, decision routing.Decision, taskID modulecore.TaskID, report *domainverification.VerificationReport) ProcessMessageResponse {
	return ProcessMessageResponse{
		Response:     response,
		Route:        decision.Route,
		Confidence:   decision.Confidence,
		TaskID:       taskID.String(),
		Verification: report,
	}
}

func (messageResponseAssembler) BuildChatCommand(response string, taskID modulecore.TaskID) ProcessMessageResponse {
	return ProcessMessageResponse{
		Response:   response,
		Route:      routing.RouteCHAT,
		Confidence: 1.0,
		TaskID:     taskID.String(),
	}
}
