package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/resilience"
)

func TestIncidentSignatureMaterialRemovesVolatileValues(t *testing.T) {
	a := incidentSignatureMaterial("panic", "exit-code", "exited", "2", []byte("2026-07-18T10:00:00+0000 host rencrow[100]: panic: nil pointer\n2026-07-18T10:00:00+0000 host rencrow[100]: goroutine 41 [running]\n2026-07-18T10:00:00+0000 host rencrow[100]: main.run(0x1234)\n2026-07-18T10:00:00+0000 host rencrow[100]: /file.go:88"))
	b := incidentSignatureMaterial("panic", "exit-code", "exited", "2", []byte("2026-07-19T14:31:22+0000 host rencrow[901]: panic: nil pointer\n2026-07-19T14:31:22+0000 host rencrow[901]: goroutine 99 [running]\n2026-07-19T14:31:22+0000 host rencrow[901]: main.run(0xabcd)\n2026-07-19T14:31:22+0000 host rencrow[901]: /file.go:91"))
	if a != b {
		t.Fatalf("volatile stack values changed signature material:\nA=%s\nB=%s", a, b)
	}
}

func TestRequestRepairUsesCodeRouteAndReturnsJobID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/viewer/repair/run" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["target_route"] != "CODE2" || body["target_agent"] != "shiro" {
			t.Fatalf("repair was routed through chat: %+v", body)
		}
		_, _ = w.Write([]byte(`{"ok":true,"job_id":"repair-test"}`))
	}))
	defer server.Close()
	t.Setenv("RENCROW_RESILIENCE_BASE_URL", server.URL)
	t.Setenv("RENCROW_RESILIENCE_DIR", t.TempDir())
	t.Setenv("RENCROW_RESILIENCE_REPAIR_ROUTE", "CODE2")

	jobID, err := requestRepair(&resilience.Incident{Signature: "incident-test", Kind: "panic"})
	if err != nil || jobID != "repair-test" {
		t.Fatalf("jobID=%q err=%v", jobID, err)
	}
}

func TestProbeOpenAIModelsAndRepairRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := probeOpenAIModels(server.URL, ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RENCROW_RESILIENCE_REPAIR_ROUTE", "code4")
	if got := selectedRepairRoute(); got != "CODE4" {
		t.Fatalf("got %s", got)
	}
	t.Setenv("RENCROW_RESILIENCE_REPAIR_ROUTE", "CHAT")
	if got := selectedRepairRoute(); got != "CODE2" {
		t.Fatalf("unsafe route did not fall back to CODE2: %s", got)
	}
}

func TestClassifyStopFindsPanic(t *testing.T) {
	if got := classifyStop("exit-code", "exited", []byte("panic: boom")); got != "panic" {
		t.Fatalf("got %q", got)
	}
	if got := classifyStop("timeout", "killed", []byte("ordinary log")); !strings.Contains(got, "exit") {
		t.Fatalf("got %q", got)
	}
}

func TestParseCoreProbeEligibilityRequiresRunningAndStartupGrace(t *testing.T) {
	properties := "ActiveState=active\nSubState=running\nExecMainStartTimestampMonotonic=100000000\n"
	if parseCoreProbeEligibility(properties, 129_999_999) {
		t.Fatal("CORE inside startup grace was probed")
	}
	if !parseCoreProbeEligibility(properties, 130_000_000) {
		t.Fatal("stable running CORE was not eligible")
	}
	if parseCoreProbeEligibility(strings.Replace(properties, "SubState=running", "SubState=stop-sigterm", 1), 200_000_000) {
		t.Fatal("stopping CORE was probed")
	}
}
