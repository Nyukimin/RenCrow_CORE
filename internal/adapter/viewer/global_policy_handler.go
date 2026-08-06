package viewer

import (
	"net/http"

	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policybundle"
)

func HandleGlobalPolicyStatus(status domainpolicy.Status) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}
