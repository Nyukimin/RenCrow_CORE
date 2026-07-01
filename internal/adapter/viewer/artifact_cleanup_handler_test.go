package viewer

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	artifactcleanupapp "github.com/Nyukimin/picoclaw_multiLLM/internal/application/artifactcleanup"
)

func TestHandleArtifactCleanupDryRun(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tmp", "reindex.partial")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(path, old, old)
	handler := HandleArtifactCleanup(artifactcleanupapp.NewService(root, filepath.Join(root, "logs", "cleanup.jsonl")))
	rec := httptest.NewRecorder()

	handler(rec, httptest.NewRequest(http.MethodPost, "/viewer/artifact-cleanup", strings.NewReader(`{"paths":["tmp/reindex.partial"],"max_age_hours":24,"dry_run":true}`)))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"candidates"`) || !strings.Contains(rec.Body.String(), `"dry_run":true`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
