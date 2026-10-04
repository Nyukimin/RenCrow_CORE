package main

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
)

type runtimeToolRegistryViewerItem struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	SchemaJSON  string   `json:"schema_json"`
	Platforms   []string `json:"platforms"`
	Source      string   `json:"source"`
	CreatedAt   string   `json:"created_at"`
	CreatedBy   string   `json:"created_by"`
	Origin      string   `json:"origin"`
}

type runtimeToolRegistryViewerResponse struct {
	Available bool                            `json:"available"`
	Total     int                             `json:"total"`
	Items     []runtimeToolRegistryViewerItem `json:"items"`
	Error     string                          `json:"error,omitempty"`
}

func runtimeToolRegistryDatabaseViewerHandler(options viewer.DatabaseViewerOptions, selected capdomain.ToolRegistry) http.HandlerFunc {
	if remote, ok := selected.(*storagehost.ToolRegistryClient); ok && remote != nil {
		return handleRemoteToolRegistryDatabaseViewer(remote, options.RuntimeTools)
	}
	return viewer.HandleToolRegistryDatabase(options)
}

func handleRemoteToolRegistryDatabaseViewer(remote *storagehost.ToolRegistryClient, runtimeTools func(context.Context) ([]domaintool.ToolMetadata, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit := 100
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			if value > 300 {
				value = 300
			}
			limit = value
		}
		platform := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("platform")))
		if platform != "" && platform != "linux" && platform != "windows" && platform != "darwin" {
			http.Error(w, "invalid platform", http.StatusBadRequest)
			return
		}

		itemsByName := make(map[string]runtimeToolRegistryViewerItem)
		partialErrors := make([]string, 0, 2)
		if runtimeTools != nil {
			metas, err := runtimeTools(r.Context())
			if err != nil {
				partialErrors = append(partialErrors, "runtime tools unavailable")
			} else {
				for _, meta := range metas {
					if platform != "" && platform != runtime.GOOS {
						continue
					}
					name := strings.TrimSpace(meta.ToolID)
					if name == "" {
						continue
					}
					origin := strings.TrimSpace(meta.Origin)
					if origin == "" {
						origin = domaintool.OriginCoreRuntime
					}
					schemaJSON := "{}"
					if len(meta.Parameters) > 0 {
						if encoded, err := json.Marshal(meta.Parameters); err == nil {
							schemaJSON = string(encoded)
						}
					}
					itemsByName[name] = runtimeToolRegistryViewerItem{
						Name: name, Description: meta.Description, SchemaJSON: schemaJSON,
						Platforms: []string{runtime.GOOS}, Source: "runtime", Origin: origin,
					}
				}
			}
		}

		platforms := []string{platform}
		if platform == "" {
			platforms = []string{"linux", "windows", "darwin"}
		}
		registryAvailable := true
		for _, targetPlatform := range platforms {
			entries, err := remote.ListForPlatform(r.Context(), targetPlatform)
			if err != nil {
				registryAvailable = false
				partialErrors = append(partialErrors, "Tool Registry unavailable")
				continue
			}
			for _, entry := range entries {
				if _, exists := itemsByName[entry.Name]; exists {
					continue
				}
				itemsByName[entry.Name] = runtimeToolRegistryViewerItem{
					Name: entry.Name, Description: entry.Description, SchemaJSON: entry.SchemaJSON,
					Platforms: append([]string(nil), entry.Platforms...), Source: string(entry.Source),
					CreatedAt: entry.CreatedAt.UTC().Format(time.RFC3339Nano), CreatedBy: entry.CreatedBy,
					Origin: domaintool.OriginDynamicRegistry,
				}
			}
		}
		items := make([]runtimeToolRegistryViewerItem, 0, len(itemsByName))
		for _, item := range itemsByName {
			items = append(items, item)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		total := len(items)
		if len(items) > limit {
			items = items[:limit]
		}
		available := registryAvailable || total > 0
		response := runtimeToolRegistryViewerResponse{Available: available, Total: total, Items: items}
		if len(partialErrors) > 0 {
			response.Error = strings.Join(partialErrors, "; ")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}
}
