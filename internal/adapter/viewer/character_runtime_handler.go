package viewer

import (
	"net/http"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/characterruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	avatarfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/avatar"
)

func HandleCharacterRuntime(service *characterruntime.Service, listener orchestrator.EventListener) http.HandlerFunc {
	return avatarfeature.HandleCharacterRuntime(service, listener)
}
