package browseractor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	modulebrowser "github.com/Nyukimin/RenCrow_CORE/modules/browseractor"
)

func TestExecCommandCancellationChild(t *testing.T) {
	if os.Getenv("RENCROW_BROWSER_ACTOR_CANCEL_CHILD") != "1" {
		return
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGTERM)
	if readyPath := os.Getenv("RENCROW_BROWSER_ACTOR_CANCEL_READY"); readyPath != "" {
		if err := os.WriteFile(readyPath, []byte("ready"), 0600); err != nil {
			os.Exit(2)
		}
	}
	<-interrupt
	_, _ = os.Stdout.Write([]byte(`{"status":"failed","error":{"code":"CANCELLED"}}`))
	os.Exit(0)
}

func TestExecCommandSendsSoftSignalAndCapturesCancellationOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps the native CommandContext hard-cancel behavior")
	}
	t.Setenv("RENCROW_BROWSER_ACTOR_CANCEL_CHILD", "1")
	readyPath := filepath.Join(t.TempDir(), "ready")
	t.Setenv("RENCROW_BROWSER_ACTOR_CANCEL_READY", readyPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commandDone := make(chan struct{})
	readyResult := make(chan error, 1)
	go func() {
		defer close(readyResult)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		timeout := time.NewTimer(5 * time.Second)
		defer timeout.Stop()
		for {
			select {
			case <-commandDone:
				readyResult <- errors.New("sidecar exited before installing its cancellation handler")
				return
			case <-timeout.C:
				cancel()
				readyResult <- errors.New("timed out waiting for sidecar cancellation readiness")
				return
			case <-ticker.C:
				if _, err := os.Stat(readyPath); err == nil {
					cancel()
					readyResult <- nil
					return
				} else if !errors.Is(err, os.ErrNotExist) {
					cancel()
					readyResult <- err
					return
				}
			}
		}
	}()
	stdout, _, exitCode, err := execCommand(ctx, os.Args[0], []string{"-test.run=^TestExecCommandCancellationChild$"}, nil)
	close(commandDone)
	if readyErr := <-readyResult; readyErr != nil {
		t.Fatal(readyErr)
	}
	if !errors.Is(err, context.Canceled) || strings.TrimSpace(string(stdout)) != `{"status":"failed","error":{"code":"CANCELLED"}}` {
		t.Fatalf("sidecar did not flush exact cancellation output before fallback: stdout=%q exit=%d err=%v", stdout, exitCode, err)
	}
}

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
