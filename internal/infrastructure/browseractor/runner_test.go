package browseractor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	modulebrowser "github.com/Nyukimin/RenCrow_CORE/modules/browseractor"
)

func TestRunnerRunPassesJSONToSidecar(t *testing.T) {
	var stdinSeen string
	runner := NewRunner(Config{NodeBinary: "node", RunnerPath: "tools/browser_actor/run_browser_actor.mjs"}).WithCommandRunner(
		func(_ context.Context, command string, args []string, stdin []byte) ([]byte, []byte, int, error) {
			if command != "node" {
				t.Fatalf("command=%s", command)
			}
			if len(args) != 3 || args[1] != "run" || args[2] != "--json" {
				t.Fatalf("args=%v", args)
			}
			stdinSeen = string(stdin)
			resp := modulebrowser.RunResponse{SchemaVersion: modulebrowser.SchemaVersion, RunID: "run_1", Status: modulebrowser.StatusCompleted}
			out, _ := json.Marshal(resp)
			return out, nil, 0, nil
		},
	)
	resp, err := runner.Run(context.Background(), modulebrowser.RunRequest{
		RunID:    "run_1",
		StartURL: "file:///tmp/example.html",
		Actions:  []modulebrowser.Action{{Type: "open"}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if resp.Status != modulebrowser.StatusCompleted {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if !strings.Contains(stdinSeen, `"start_url":"file:///tmp/example.html"`) {
		t.Fatalf("sidecar stdin did not include request: %s", stdinSeen)
	}
}

func TestRunnerRunAppliesProfileStoragePath(t *testing.T) {
	var reqSeen modulebrowser.RunRequest
	runner := NewRunner(Config{
		NodeBinary:   "node",
		RunnerPath:   "tools/browser_actor/run_browser_actor.mjs",
		ProfileRoot:  "workspace/browser_profiles",
		ArtifactRoot: "workspace/browser_runs",
	}).WithCommandRunner(
		func(_ context.Context, _ string, _ []string, stdin []byte) ([]byte, []byte, int, error) {
			if err := json.Unmarshal(stdin, &reqSeen); err != nil {
				t.Fatalf("unmarshal stdin: %v", err)
			}
			resp := modulebrowser.RunResponse{SchemaVersion: modulebrowser.SchemaVersion, RunID: "run_1", Status: modulebrowser.StatusCompleted}
			out, _ := json.Marshal(resp)
			return out, nil, 0, nil
		},
	)
	_, err := runner.Run(context.Background(), modulebrowser.RunRequest{
		RunID:     "run_1",
		StartURL:  "file:///tmp/example.html",
		ProfileID: "work",
		Actions:   []modulebrowser.Action{{Type: "open"}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if reqSeen.StorageStatePath != "workspace/browser_profiles/work/storage_state.json" {
		t.Fatalf("storage_state_path=%q", reqSeen.StorageStatePath)
	}
}

func TestRunnerDoctorParsesSidecarJSON(t *testing.T) {
	runner := NewRunner(Config{}).WithCommandRunner(
		func(context.Context, string, []string, []byte) ([]byte, []byte, int, error) {
			return []byte(`{"schema_version":"1.0","ok":true,"checks":[{"name":"node","ok":true,"status":"ok"}]}`), nil, 0, nil
		},
	)
	resp, err := runner.Doctor(context.Background())
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}
	if !resp.OK || len(resp.Checks) != 1 {
		t.Fatalf("unexpected doctor response: %+v", resp)
	}
}
