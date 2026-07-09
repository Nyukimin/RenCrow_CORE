package viewer

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	historyrepairapp "github.com/Nyukimin/RenCrow_CORE/internal/application/historyrepair"
)

func TestHandleHistoryRepairJSONL(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "logs", "job_history.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\"job_id\":\"ok\"}\n{\"bad\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := HandleHistoryRepairJSONL(historyrepairapp.NewJSONLRepairService(root, filepath.Join(root, "logs", "history_repair.jsonl")))
	rec := httptest.NewRecorder()

	handler(rec, httptest.NewRequest(http.MethodPost, "/viewer/history-repair/jsonl", strings.NewReader(`{"path":"logs/job_history.jsonl","requested_by":"coder"}`)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"status":"repaired"`, `"invalid_lines":1`, `"quarantine_path"`, `"repaired_path"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("response missing %s: %s", want, rec.Body.String())
		}
	}
}
