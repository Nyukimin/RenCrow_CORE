//go:build linux

package runmigration

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLinuxRunCutoverServiceUsesFixedOwnerLifecycle(t *testing.T) {
	root := t.TempDir()
	runtimePath := filepath.Join(root, "rencrow")
	configPath := filepath.Join(root, "core.yaml")
	if err := os.WriteFile(runtimePath, []byte("runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("config"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := newRunCutoverService(runtimePath, configPath)
	if err != nil {
		t.Fatal(err)
	}
	oldCommand, oldReady := runCutoverCommandOutput, runCutoverReadiness
	defer func() { runCutoverCommandOutput, runCutoverReadiness = oldCommand, oldReady }()
	var commands []string
	running := true
	runCutoverCommandOutput = func(_ context.Context, name string, args ...string) (string, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		if name == runCutoverSS {
			return "", nil
		}
		switch args[1] {
		case "show":
			if running {
				return "Id=rencrow.service\nLoadState=loaded\nActiveState=active\nMainPID=42\nExecStart=" + runtimePath + " run\n", nil
			}
			return "Id=rencrow.service\nLoadState=loaded\nActiveState=inactive\nMainPID=0\nExecStart=" + runtimePath + " run\n", nil
		case "mask":
			return "", nil
		case "stop":
			running = false
			return "", nil
		case "is-enabled":
			return "masked\n", errors.New("masked")
		case "unmask":
			return "", nil
		case "start":
			running = true
			return "", nil
		default:
			return "", errors.New("unexpected command")
		}
	}
	runCutoverReadiness = func(context.Context, *http.Client) bool { return true }

	expected := digest([]byte("runtime"))
	evidence, err := service.StopAndVerify(context.Background(), expected, configPath)
	if err != nil || !evidence.valid(expected) {
		t.Fatalf("stop evidence = %#v err=%v", evidence, err)
	}
	if err := service.Restore(context.Background(), expected); err != nil {
		t.Fatalf("restore: %v", err)
	}
	wantPrefix := []string{
		runCutoverSystemctl + " --user show rencrow.service --property=Id,LoadState,ActiveState,SubState,MainPID,ExecStart",
		runCutoverSystemctl + " --user mask --runtime rencrow.service",
		runCutoverSystemctl + " --user stop rencrow.service",
	}
	if len(commands) < len(wantPrefix) || !reflect.DeepEqual(commands[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("commands = %#v", commands)
	}
}
