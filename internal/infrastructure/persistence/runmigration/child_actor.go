package runmigration

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	super "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
)

// Historical CORE subagent.Manager recorded the actual AgentName and the exact
// CreatedAt UnixNano in its execution ID (source revision 1c36fe0). This decoder
// is confined to the offline converter; archive month attribution is unrelated.
func restoreLegacyChildActor(m map[string]json.RawMessage) error {
	if textField(m, "actor_id") != "" {
		return super.ValidateActorID(textField(m, "actor_id"))
	}
	if textField(m, "agent_type") != "Subagent" {
		return errors.New("legacy child actor has no recognized producer")
	}
	id := textField(m, "subagent_id")
	parts := strings.Split(id, "_")
	if len(parts) != 3 || parts[0] != "sub" || super.ValidateActorID(parts[1]) != nil {
		return errors.New("legacy child actor identity is not recoverable")
	}
	created := timeField(m, "created_at")
	// UnixNano is undefined outside its representable range. Require a roundtrip
	// before using it as historical evidence, and reject alternate decimal forms.
	if created.IsZero() || !time.Unix(0, created.UnixNano()).Equal(created) || id != fmt.Sprintf("sub_%s_%d", parts[1], created.UnixNano()) {
		return errors.New("legacy child actor timestamp does not match its identity")
	}
	setField(m, "actor_id", parts[1])
	return nil
}
