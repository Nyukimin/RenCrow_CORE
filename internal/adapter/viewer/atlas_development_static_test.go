package viewer

import (
	"os"
	"strings"
	"testing"
)

func TestAtlasViewerRendersDevelopmentMethodologyOwnerProjection(t *testing.T) {
	body, err := os.ReadFile("assets/js/tabs/atlas.js")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, required := range []string{"/viewer/atlas/development/units/", "Development Methodology", "implementation_authority_token", "terminal_outcome", "blocked_reason", "implementer_agent_id", "reviewer_agent_id"} {
		if !strings.Contains(text, required) {
			t.Fatalf("Atlas Viewer missing development field %q", required)
		}
	}
}
