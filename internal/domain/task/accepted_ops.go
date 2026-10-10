package task

import (
	"fmt"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

const AcceptedOPSAgentAssignee = "shiro"

// AcceptedOPSClaim binds a canonical Task to the compact Conversation receipt
// reference and the backend selected by trusted CORE admission. Origin IDs
// remain in Task's existing origin fields.
type AcceptedOPSClaim struct {
	ReceiptRef       domainconversation.AcceptedOPSInputReference `json:"receipt_ref"`
	BackendSelection domainconversation.BackendSelection          `json:"backend_selection"`
}

func (claim AcceptedOPSClaim) Validate(task Task) error {
	if err := claim.ReceiptRef.Validate(); err != nil {
		return err
	}
	if claim.BackendSelection != domainconversation.BackendShiroNativeCodingV1 {
		return fmt.Errorf("unsupported backend selection %q", claim.BackendSelection)
	}
	if task.OwnerID != AcceptedOPSAgentAssignee || task.Route != RouteOperations || task.Assignee != AcceptedOPSAgentAssignee {
		return fmt.Errorf("accepted OPS Task owner, route, or assignee conflicts with the claim")
	}
	if task.OriginSessionID == "" || task.OriginThreadID == "" || task.OriginTurnID == "" || task.OriginMessageID == "" {
		return fmt.Errorf("accepted OPS Task origin identities are required")
	}
	return nil
}
