package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func TestHandleRuntimeCapabilitiesRejectsNonGET(t *testing.T) {
	called := false
	handler := HandleRuntimeCapabilities(func(context.Context) ([]domaintool.ToolMetadata, error) {
		called = true
		return nil, nil
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/capabilities", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusMethodNotAllowed)
	}
	if called {
		t.Fatal("runtime provider must not be called for a rejected method")
	}
}

func TestHandleRuntimeCapabilitiesReturnsSortedSafeProjection(t *testing.T) {
	metadata := []domaintool.ToolMetadata{
		{
			ToolID:      "zeta.tool",
			Version:     "2.0.0",
			Category:    "admin",
			Origin:      domaintool.OriginRenCrowTools,
			Description: "zeta description",
			Parameters: map[string]any{
				"path":   "/private/secret",
				"secret": "do-not-expose",
			},
		},
		{
			ToolID:      "alpha.tool",
			Version:     "1.0.0",
			Category:    "query",
			Origin:      domaintool.OriginCoreRuntime,
			Description: "alpha description",
		},
	}
	handler := HandleRuntimeCapabilities(func(context.Context) ([]domaintool.ToolMetadata, error) {
		return metadata, nil
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/capabilities", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response struct {
		Available bool              `json:"available"`
		Total     int               `json:"total"`
		Items     []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Available || response.Total != 2 || len(response.Items) != 2 {
		t.Fatalf("unexpected response summary: available=%v total=%d items=%d", response.Available, response.Total, len(response.Items))
	}

	wantKeys := []string{"available", "category", "description", "origin", "tool_id", "version"}
	gotIDs := make([]string, 0, len(response.Items))
	for _, raw := range response.Items {
		var item map[string]json.RawMessage
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatalf("decode item: %v", err)
		}
		gotKeys := make([]string, 0, len(item))
		for key := range item {
			gotKeys = append(gotKeys, key)
		}
		sortStrings(gotKeys)
		if !reflect.DeepEqual(gotKeys, wantKeys) {
			t.Fatalf("item keys=%v want=%v", gotKeys, wantKeys)
		}
		var itemView struct {
			ToolID    string `json:"tool_id"`
			Available bool   `json:"available"`
		}
		if err := json.Unmarshal(raw, &itemView); err != nil {
			t.Fatalf("decode item view: %v", err)
		}
		if !itemView.Available {
			t.Fatalf("item %q must be runtime-available", itemView.ToolID)
		}
		gotIDs = append(gotIDs, itemView.ToolID)
	}
	if want := []string{"alpha.tool", "zeta.tool"}; !reflect.DeepEqual(gotIDs, want) {
		t.Fatalf("tool ids=%v want=%v", gotIDs, want)
	}
	body := rec.Body.String()
	for _, forbidden := range []string{"parameters", "private/secret", "do-not-expose", "path", "secret"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
}

func TestHandleRuntimeCapabilitiesFailsClosedWithoutLeakingError(t *testing.T) {
	tests := map[string]RuntimeCapabilityProvider{
		"nil provider": nil,
		"provider error": func(context.Context) ([]domaintool.ToolMetadata, error) {
			return nil, errors.New("private path=/srv/secret token=do-not-expose")
		},
	}
	for name, provider := range tests {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			HandleRuntimeCapabilities(provider).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/capabilities", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d want=%d", rec.Code, http.StatusOK)
			}
			var response struct {
				Available bool              `json:"available"`
				Total     int               `json:"total"`
				Items     []json.RawMessage `json:"items"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Available || response.Total != 0 || response.Items == nil || len(response.Items) != 0 {
				t.Fatalf("unexpected fail-closed response: available=%v total=%d items=%v", response.Available, response.Total, response.Items)
			}
			if strings.Contains(rec.Body.String(), "private") || strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("provider error leaked: %s", rec.Body.String())
			}
		})
	}
}

func sortStrings(values []string) {
	for i := 0; i < len(values); i++ {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
}
