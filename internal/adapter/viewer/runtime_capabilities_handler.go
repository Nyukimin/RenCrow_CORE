package viewer

import (
	"context"
	"net/http"
	"sort"

	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

// RuntimeCapabilityProvider reads the metadata from the currently wired
// production Worker Runner. The Viewer projection deliberately accepts only
// metadata and never executes a Tool or reconstructs its schema.
type RuntimeCapabilityProvider func(context.Context) ([]domaintool.ToolMetadata, error)

type runtimeCapabilityViewerItem struct {
	ToolID      string `json:"tool_id"`
	Version     string `json:"version"`
	Category    string `json:"category"`
	Origin      string `json:"origin"`
	Description string `json:"description"`
	Available   bool   `json:"available"`
}

type runtimeCapabilitiesViewerResponse struct {
	Available bool                          `json:"available"`
	Total     int                           `json:"total"`
	Items     []runtimeCapabilityViewerItem `json:"items"`
}

// HandleRuntimeCapabilities exposes a read-only, fail-closed projection of
// the current Worker Tool metadata to the authenticated Viewer boundary.
// Provider errors are intentionally generalized so implementation details,
// paths, and credentials cannot reach the HTTP response.
func HandleRuntimeCapabilities(provider RuntimeCapabilityProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireViewerMethod(w, r, http.MethodGet) {
			return
		}

		if provider == nil {
			writeJSON(w, http.StatusOK, runtimeCapabilitiesViewerResponse{Items: []runtimeCapabilityViewerItem{}})
			return
		}
		metadata, err := provider(r.Context())
		if err != nil {
			writeJSON(w, http.StatusOK, runtimeCapabilitiesViewerResponse{Items: []runtimeCapabilityViewerItem{}})
			return
		}

		items := make([]runtimeCapabilityViewerItem, 0, len(metadata))
		for _, meta := range metadata {
			if meta.ToolID == "" {
				continue
			}
			items = append(items, runtimeCapabilityViewerItem{
				ToolID:      meta.ToolID,
				Version:     meta.Version,
				Category:    meta.Category,
				Origin:      meta.Origin,
				Description: meta.Description,
				Available:   true,
			})
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].ToolID != items[j].ToolID {
				return items[i].ToolID < items[j].ToolID
			}
			if items[i].Version != items[j].Version {
				return items[i].Version < items[j].Version
			}
			if items[i].Category != items[j].Category {
				return items[i].Category < items[j].Category
			}
			if items[i].Origin != items[j].Origin {
				return items[i].Origin < items[j].Origin
			}
			return items[i].Description < items[j].Description
		})

		writeJSON(w, http.StatusOK, runtimeCapabilitiesViewerResponse{
			Available: true,
			Total:     len(items),
			Items:     items,
		})
	}
}
