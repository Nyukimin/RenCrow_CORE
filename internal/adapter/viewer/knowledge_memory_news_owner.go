package viewer

import (
	"context"
	"errors"
	"net/http"
	"strings"

	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
)

// knowledgeMemoryNewsOwnerAccess is the trusted scope attached by the
// boundary wrapper. A missing marker is treated as public for compatibility
// with direct unit callers, while the runtime route wraps all News handlers.
type knowledgeMemoryNewsOwnerAccess struct {
	userID        string
	authenticated bool
}

type knowledgeMemoryNewsOwnerAccessKey struct{}

var errKnowledgeMemoryNewsOwnerScope = errors.New("knowledge memory news owner scope is required")

// WithKnowledgeMemoryNewsOwnerAccess binds the configured owner scope to the
// Knowledge Memory Viewer handlers. Requests without Authorization remain
// public-scope requests; requests that present Authorization must pass the
// existing direct-loopback, bearer, and CMD interaction-profile gates.
func WithKnowledgeMemoryNewsOwnerAccess(handler http.HandlerFunc, userID string, token []byte) http.HandlerFunc {
	userID = strings.TrimSpace(userID)
	configuredToken := append([]byte(nil), token...)
	return func(w http.ResponseWriter, r *http.Request) {
		if handler == nil {
			writeMemoryOwnerError(w, http.StatusServiceUnavailable, "knowledge_memory_unavailable")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if len(r.Header.Values("Authorization")) == 0 {
			handler(w, r.WithContext(withKnowledgeMemoryNewsOwnerAccess(r.Context(), knowledgeMemoryNewsOwnerAccess{})))
			return
		}
		if len(configuredToken) == 0 {
			writeMemoryOwnerError(w, http.StatusServiceUnavailable, "owner_auth_unavailable")
			return
		}
		if !memoryOwnerDirectLocalRequest(r) {
			writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
			return
		}
		if !memoryOwnerBearerAuthorized(r, configuredToken) {
			writeMemoryOwnerError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !memoryOwnerClientProfileAllowed(r) {
			writeMemoryOwnerError(w, http.StatusForbidden, "forbidden")
			return
		}
		if userID == "" {
			writeMemoryOwnerError(w, http.StatusServiceUnavailable, "owner_scope_unavailable")
			return
		}
		ctx, err := memoryOwnerOwnerContext(r.Context(), userID)
		if err != nil {
			writeMemoryOwnerError(w, http.StatusServiceUnavailable, "scope_unavailable")
			return
		}
		ctx = withKnowledgeMemoryNewsOwnerAccess(ctx, knowledgeMemoryNewsOwnerAccess{userID: userID, authenticated: true})
		handler(w, r.WithContext(ctx))
	}
}

func withKnowledgeMemoryNewsOwnerAccess(ctx context.Context, access knowledgeMemoryNewsOwnerAccess) context.Context {
	return context.WithValue(ctx, knowledgeMemoryNewsOwnerAccessKey{}, access)
}

func knowledgeMemoryNewsOwnerAccessFromContext(ctx context.Context) knowledgeMemoryNewsOwnerAccess {
	if ctx == nil {
		return knowledgeMemoryNewsOwnerAccess{}
	}
	access, ok := ctx.Value(knowledgeMemoryNewsOwnerAccessKey{}).(knowledgeMemoryNewsOwnerAccess)
	if !ok {
		return knowledgeMemoryNewsOwnerAccess{}
	}
	access.userID = strings.TrimSpace(access.userID)
	return access
}

func knowledgeMemoryNewsVisible(ctx context.Context, item domainkm.NewsKnowledgeItem) bool {
	access := knowledgeMemoryNewsOwnerAccessFromContext(ctx)
	itemUserID := strings.TrimSpace(item.UserID)
	visibility := strings.ToLower(strings.TrimSpace(item.Visibility))
	if itemUserID == "" {
		return visibility == "" || visibility == "public"
	}
	return access.authenticated && itemUserID == access.userID && (visibility == "" || visibility == "private")
}

func filterKnowledgeMemoryNews(ctx context.Context, items []domainkm.NewsKnowledgeItem) []domainkm.NewsKnowledgeItem {
	filtered := make([]domainkm.NewsKnowledgeItem, 0, len(items))
	for _, item := range items {
		if knowledgeMemoryNewsVisible(ctx, item) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func validateKnowledgeMemoryNewsWriteScope(ctx context.Context, item *domainkm.NewsKnowledgeItem) error {
	if item == nil {
		return errKnowledgeMemoryNewsOwnerScope
	}
	item.UserID = strings.TrimSpace(item.UserID)
	item.Visibility = strings.ToLower(strings.TrimSpace(item.Visibility))
	access := knowledgeMemoryNewsOwnerAccessFromContext(ctx)
	if item.UserID == "" {
		if item.Visibility != "" && item.Visibility != "public" {
			return errKnowledgeMemoryNewsOwnerScope
		}
		return nil
	}
	if !access.authenticated || item.UserID != access.userID || item.Visibility != "private" {
		return errKnowledgeMemoryNewsOwnerScope
	}
	return nil
}
