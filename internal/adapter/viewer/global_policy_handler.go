package viewer

import (
	"net/http"

	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policybundle"
)

type GlobalPolicyStatusProvider interface {
	Status() domainpolicy.Status
}

func HandleGlobalPolicyStatus(provider GlobalPolicyStatusProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if provider == nil {
			http.Error(w, "global policy status unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, provider.Status())
	}
}
