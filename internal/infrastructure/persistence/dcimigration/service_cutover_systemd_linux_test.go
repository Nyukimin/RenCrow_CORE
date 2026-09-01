//go:build linux

package dcimigration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type linuxSystemdFake struct {
	calls []linuxSystemdFakeCall

	showQueue    []linuxSystemdCommandResult
	enabledQueue []linuxSystemdCommandResult
	ssQueue      []linuxSystemdCommandResult
	actions      map[string]linuxSystemdCommandResult

	httpQueue []linuxSystemdHTTPResponse
	sleeps    []int
}

type linuxSystemdFakeCall struct {
	name string
	args []string
}

func (fake *linuxSystemdFake) run(_ context.Context, name string, args []string) linuxSystemdCommandResult {
	fake.calls = append(fake.calls, linuxSystemdFakeCall{name: name, args: append([]string(nil), args...)})
	if name == linuxSystemdSSCommand {
		return linuxSystemdPopCommand(&fake.ssQueue, linuxSystemdCommandResult{})
	}
	if len(args) > 1 && args[1] == "show" {
		return linuxSystemdPopCommand(&fake.showQueue, linuxSystemdCommandResult{})
	}
	if len(args) > 1 && args[1] == "is-enabled" {
		return linuxSystemdPopCommand(&fake.enabledQueue, linuxSystemdCommandResult{})
	}
	key := name + " " + strings.Join(args, " ")
	if result, ok := fake.actions[key]; ok {
		return result
	}
	return linuxSystemdCommandResult{}
}

func linuxSystemdPopCommand(queue *[]linuxSystemdCommandResult, fallback linuxSystemdCommandResult) linuxSystemdCommandResult {
	if len(*queue) == 0 {
		return fallback
	}
	result := (*queue)[0]
	*queue = (*queue)[1:]
	return result
}

func (fake *linuxSystemdFake) httpGet(_ context.Context, endpoint string) linuxSystemdHTTPResponse {
	if endpoint != linuxSystemdCutoverReadiness {
		return linuxSystemdHTTPResponse{Err: errors.New("unexpected endpoint")}
	}
	if len(fake.httpQueue) == 0 {
		return linuxSystemdHTTPResponse{}
	}
	response := fake.httpQueue[0]
	fake.httpQueue = fake.httpQueue[1:]
	return response
}

func (fake *linuxSystemdFake) sleep(_ context.Context, _ time.Duration) error {
	fake.sleeps = append(fake.sleeps, 1)
	return nil
}

type linuxSystemdFixture struct {
	manager *linuxSystemdCutoverServiceManager
	fake    *linuxSystemdFake
	runtime string
	config  string
	sha     string
}

func newLinuxSystemdFixture(t *testing.T) linuxSystemdFixture {
	t.Helper()
	root := t.TempDir()
	runtimePath := filepath.Join(root, "rencrow")
	configPath := filepath.Join(root, "core.yaml")
	if err := os.WriteFile(runtimePath, []byte("canonical runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("canonical config"), 0o600); err != nil {
		t.Fatal(err)
	}
	sha, _, err := hashBuildFile(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	fake := &linuxSystemdFake{
		actions:   map[string]linuxSystemdCommandResult{},
		httpQueue: []linuxSystemdHTTPResponse{{StatusCode: 200, Body: []byte(linuxSystemdReadyBody())}},
	}
	fake.showQueue = []linuxSystemdCommandResult{{ExitCode: 0, Stdout: linuxSystemdRunningShow(runtimePath, configPath)}}
	fake.enabledQueue = []linuxSystemdCommandResult{{ExitCode: 0, Stdout: "enabled\n"}}
	fake.ssQueue = []linuxSystemdCommandResult{{ExitCode: 0, Stdout: linuxSystemdListeningSS(4242)}}
	manager, err := newLinuxSystemdCutoverServiceManagerWithDeps(runtimePath, configPath, linuxSystemdCutoverDependencies{
		run:          fake.run,
		readlink:     func(string) (string, error) { return runtimePath, nil },
		httpGet:      fake.httpGet,
		sleep:        fake.sleep,
		runningPolls: 1,
		stoppedPolls: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return linuxSystemdFixture{manager: manager, fake: fake, runtime: runtimePath, config: configPath, sha: sha}
}

func linuxSystemdRunningShow(runtimePath, configPath string) string {
	return strings.Join([]string{
		"LoadState=loaded",
		"UnitFileState=enabled",
		"ActiveState=active",
		"SubState=running",
		"MainPID=4242",
		"ExecStart={ path=" + runtimePath + "; argv[]=" + runtimePath + " run ; }",
		"Environment=RENCROW_CONFIG=" + configPath,
	}, "\n") + "\n"
}

func linuxSystemdReadyBody() string {
	return `{"ok":true,"status":"ready","service":"rencrow-core","runtime":"go","ready":true}`
}

func linuxSystemdStoppedShow() string {
	return strings.Join([]string{
		"LoadState=masked",
		"UnitFileState=masked-runtime",
		"ActiveState=inactive",
		"SubState=dead",
		"MainPID=0",
		"ExecStart={ path=/var/lib/rencrow/bin/rencrow; argv[]=rencrow run ; }",
		"Environment=RENCROW_CONFIG=/var/lib/rencrow/config/core.yaml",
	}, "\n") + "\n"
}

func linuxSystemdListeningSS(pid int64) string {
	return fmt.Sprintf("State Recv-Q Send-Q Local Address:Port Peer Address:Port Process\nLISTEN 0 128 127.0.0.1:%d 0.0.0.0:* users:((\"rencrow\",pid=%d,fd=7))\n", linuxSystemdCutoverPort, pid)
}

func linuxSystemdStoppedFixture(t *testing.T) linuxSystemdFixture {
	t.Helper()
	fixture := newLinuxSystemdFixture(t)
	fixture.fake.showQueue = []linuxSystemdCommandResult{{ExitCode: 0, Stdout: linuxSystemdStoppedShow()}}
	fixture.fake.enabledQueue = []linuxSystemdCommandResult{{ExitCode: 1, Stdout: "masked-runtime\n"}}
	fixture.fake.ssQueue = []linuxSystemdCommandResult{{ExitCode: 0, Stdout: "State Recv-Q Send-Q Local Address:Port Peer Address:Port Process\n"}}
	return fixture
}

func TestLinuxSystemdCutoverServiceManagerHappyEvidence(t *testing.T) {
	fixture := newLinuxSystemdFixture(t)
	running, err := fixture.manager.VerifyRunning(context.Background(), fixture.sha)
	if err != nil {
		t.Fatalf("VerifyRunning() error = %v", err)
	}
	wantRunning := cutoverServiceRunningEvidence{Owner: 1, Enabled: 1, Unmasked: 1, Active: 1, MainPIDPositive: 1, ListenerOwned: 1, Readiness: 1, RuntimeSHA256: fixture.sha}
	if !reflect.DeepEqual(running, wantRunning) || !running.valid(fixture.sha) {
		t.Fatalf("running evidence = %#v, want %#v", running, wantRunning)
	}
	if len(fixture.fake.calls) != 3 || fixture.fake.calls[0].name != linuxSystemdSystemctlCommand || fixture.fake.calls[1].name != linuxSystemdSystemctlCommand || fixture.fake.calls[2].name != linuxSystemdSSCommand {
		t.Fatalf("running command calls = %#v", fixture.fake.calls)
	}
	for _, call := range fixture.fake.calls {
		if call.name != linuxSystemdSystemctlCommand && call.name != linuxSystemdSSCommand {
			t.Fatalf("running emitted non-canonical command = %#v", call)
		}
	}
	if fixture.fake.calls[0].args[0] != "--user" || fixture.fake.calls[0].args[1] != "show" || fixture.fake.calls[0].args[2] != linuxSystemdCutoverUnit {
		t.Fatalf("systemd show command = %#v", fixture.fake.calls[0])
	}

	stopped := linuxSystemdStoppedFixture(t)
	evidence, err := stopped.manager.VerifyStopped(context.Background())
	if err != nil {
		t.Fatalf("VerifyStopped() error = %v", err)
	}
	if want := (cutoverServiceStoppedEvidence{Masked: 1, Active: 0, MainPIDZero: 1, ListenerZero: 1}); evidence != want || !evidence.valid() {
		t.Fatalf("stopped evidence = %#v, want %#v", evidence, want)
	}
	if len(stopped.fake.calls) != 3 || stopped.fake.calls[0].name != linuxSystemdSystemctlCommand || stopped.fake.calls[0].args[1] != "show" || stopped.fake.calls[1].name != linuxSystemdSystemctlCommand || stopped.fake.calls[1].args[1] != "is-enabled" || stopped.fake.calls[2].name != linuxSystemdSSCommand {
		t.Fatalf("stopped command calls = %#v", stopped.fake.calls)
	}
}

func TestLinuxSystemdCutoverActionsUseOnlyFixedCommands(t *testing.T) {
	fixture := newLinuxSystemdFixture(t)
	if err := fixture.manager.MaskAndStop(context.Background()); err != nil {
		t.Fatalf("MaskAndStop() error = %v", err)
	}
	if err := fixture.manager.UnmaskAndStart(context.Background()); err != nil {
		t.Fatalf("UnmaskAndStart() error = %v", err)
	}
	want := []linuxSystemdFakeCall{
		{name: linuxSystemdSystemctlCommand, args: []string{"--user", "mask", "--runtime", "--now", linuxSystemdCutoverUnit}},
		{name: linuxSystemdSystemctlCommand, args: []string{"--user", "unmask", "--runtime", linuxSystemdCutoverUnit}},
		{name: linuxSystemdSystemctlCommand, args: []string{"--user", "start", linuxSystemdCutoverUnit}},
	}
	if !reflect.DeepEqual(fixture.fake.calls, want) {
		t.Fatalf("fixed action calls = %#v, want %#v", fixture.fake.calls, want)
	}
	for _, call := range fixture.fake.calls {
		if call.name != linuxSystemdSystemctlCommand {
			t.Fatalf("fixed action emitted non-systemctl command = %#v", call)
		}
	}

	failure := newLinuxSystemdFixture(t)
	failure.fake.actions[linuxSystemdSystemctlCommand+" --user unmask --runtime "+linuxSystemdCutoverUnit] = linuxSystemdCommandResult{ExitCode: 1, Stderr: "secret/path/payload"}
	err := failure.manager.UnmaskAndStart(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret/path/payload") || len(failure.fake.calls) != 1 {
		t.Fatalf("unmask failure = %v, calls = %#v", err, failure.fake.calls)
	}

	startFailure := newLinuxSystemdFixture(t)
	startFailure.fake.actions[linuxSystemdSystemctlCommand+" --user start "+linuxSystemdCutoverUnit] = linuxSystemdCommandResult{ExitCode: 1, Stderr: "secret/path/payload"}
	err = startFailure.manager.UnmaskAndStart(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret/path/payload") || len(startFailure.fake.calls) != 2 {
		t.Fatalf("start failure = %v, calls = %#v", err, startFailure.fake.calls)
	}

	maskFailure := newLinuxSystemdFixture(t)
	maskFailure.fake.actions[linuxSystemdSystemctlCommand+" --user mask --runtime --now "+linuxSystemdCutoverUnit] = linuxSystemdCommandResult{Err: errors.New("secret/path/payload")}
	err = maskFailure.manager.MaskAndStop(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret/path/payload") {
		t.Fatalf("mask failure = %v", err)
	}
}

func TestLinuxSystemdMaskedIsEnabledAcceptsPositiveExitCodesOnly(t *testing.T) {
	for _, exitCode := range []int{1, 2, 127} {
		fixture := linuxSystemdStoppedFixture(t)
		fixture.fake.enabledQueue[0].ExitCode = exitCode
		if _, err := fixture.manager.VerifyStopped(context.Background()); err != nil {
			t.Fatalf("masked exit code %d: %v", exitCode, err)
		}
	}
	for _, exitCode := range []int{0, -1} {
		fixture := linuxSystemdStoppedFixture(t)
		fixture.fake.enabledQueue[0].ExitCode = exitCode
		if _, err := fixture.manager.VerifyStopped(context.Background()); err == nil {
			t.Fatalf("masked exit code %d was accepted", exitCode)
		}
	}
}

func TestLinuxSystemdCutoverRunningRejectsInvalidEvidence(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*linuxSystemdFixture)
	}{
		{name: "load state", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, "LoadState=loaded", "LoadState=not-found", 1)
		}},
		{name: "disabled", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, "UnitFileState=enabled", "UnitFileState=disabled", 1)
		}},
		{name: "is-enabled mismatch", setup: func(f *linuxSystemdFixture) { f.fake.enabledQueue[0].Stdout = "disabled\n" }},
		{name: "active state", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, "ActiveState=active", "ActiveState=inactive", 1)
		}},
		{name: "sub state", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, "SubState=running", "SubState=dead", 1)
		}},
		{name: "missing PID", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, "MainPID=4242", "MainPID=", 1)
		}},
		{name: "exec missing", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, "ExecStart={ path=", "ExecStart=", 1)
		}},
		{name: "exec simple fallback", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, "ExecStart={ path="+f.runtime+"; argv[]="+f.runtime+" run ; }", "ExecStart="+f.runtime+" run", 1)
		}},
		{name: "exec argv missing", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, "; argv[]="+f.runtime+" run ;", " ;", 1)
		}},
		{name: "exec argv wrong command", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, "argv[]="+f.runtime+" run", "argv[]="+f.runtime+" start", 1)
		}},
		{name: "exec argv extra", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, "argv[]="+f.runtime+" run", "argv[]="+f.runtime+" run --extra", 1)
		}},
		{name: "exec argv duplicate", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, "argv[]="+f.runtime+" run ;", "argv[]="+f.runtime+" run ; argv[]="+f.runtime+" run ;", 1)
		}},
		{name: "exec wrong", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, f.runtime, "/other/runtime", 1)
		}},
		{name: "environment missing", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, "Environment=RENCROW_CONFIG="+f.config, "Environment=OTHER=/other", 1)
		}},
		{name: "environment ambiguous", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, "Environment=RENCROW_CONFIG="+f.config, "Environment=RENCROW_CONFIG="+f.config+" RENCROW_CONFIG=/other", 1)
		}},
		{name: "config wrong", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, f.config, "/other/config", 1)
		}},
		{name: "runtime hash", setup: func(f *linuxSystemdFixture) {
			if err := os.WriteFile(f.runtime, []byte("different runtime"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "runtime symlink", setup: func(f *linuxSystemdFixture) {
			moved := f.runtime + ".real"
			if err := os.Rename(f.runtime, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, f.runtime); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "readlink wrong", setup: func(f *linuxSystemdFixture) {
			f.manager.readlink = func(string) (string, error) { return "/other/runtime", nil }
		}},
		{name: "readlink deleted", setup: func(f *linuxSystemdFixture) {
			f.manager.readlink = func(string) (string, error) { return f.runtime + " (deleted)", nil }
		}},
		{name: "listener wrong PID", setup: func(f *linuxSystemdFixture) { f.fake.ssQueue[0].Stdout = linuxSystemdListeningSS(99) }},
		{name: "listener absent", setup: func(f *linuxSystemdFixture) {
			f.fake.ssQueue[0].Stdout = "State Recv-Q Send-Q Local Address:Port Peer Address:Port Process\n"
		}},
		{name: "listener duplicate", setup: func(f *linuxSystemdFixture) {
			f.fake.ssQueue[0].Stdout = linuxSystemdListeningSS(4242) + linuxSystemdListeningSS(4243)
		}},
		{name: "readiness service", setup: func(f *linuxSystemdFixture) {
			f.fake.httpQueue[0].Body = []byte(`{"ok":true,"status":"ready","service":"other","runtime":"go","ready":true}`)
		}},
		{name: "readiness false", setup: func(f *linuxSystemdFixture) {
			f.fake.httpQueue[0].Body = []byte(`{"ok":true,"status":"ready","service":"rencrow-core","runtime":"go","ready":false}`)
		}},
		{name: "readiness ok false", setup: func(f *linuxSystemdFixture) {
			f.fake.httpQueue[0].Body = []byte(`{"ok":false,"status":"ready","service":"rencrow-core","runtime":"go","ready":true}`)
		}},
		{name: "readiness runtime", setup: func(f *linuxSystemdFixture) {
			f.fake.httpQueue[0].Body = []byte(`{"ok":true,"status":"ready","service":"rencrow-core","runtime":"other","ready":true}`)
		}},
		{name: "readiness status", setup: func(f *linuxSystemdFixture) {
			f.fake.httpQueue[0].Body = []byte(`{"ok":true,"status":"not-ready","service":"rencrow-core","runtime":"go","ready":true}`)
		}},
		{name: "readiness invalid", setup: func(f *linuxSystemdFixture) { f.fake.httpQueue[0].Body = []byte(`{"service":"rencrow-core"} trailing`) }},
		{name: "readiness unknown", setup: func(f *linuxSystemdFixture) {
			f.fake.httpQueue[0].Body = []byte(`{"ok":true,"status":"ready","service":"rencrow-core","runtime":"go","ready":true,"extra":"unexpected"}`)
		}},
		{name: "readiness duplicate", setup: func(f *linuxSystemdFixture) {
			f.fake.httpQueue[0].Body = []byte(`{"ok":true,"status":"ready","service":"rencrow-core","runtime":"go","ready":true,"ready":true}`)
		}},
		{name: "readiness minimal", setup: func(f *linuxSystemdFixture) {
			f.fake.httpQueue[0].Body = []byte(`{"service":"rencrow-core","ready":true}`)
		}},
		{name: "readiness unavailable", setup: func(f *linuxSystemdFixture) { f.fake.httpQueue[0].StatusCode = 503 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLinuxSystemdFixture(t)
			test.setup(&fixture)
			_, err := fixture.manager.VerifyRunning(context.Background(), fixture.sha)
			if err == nil {
				t.Fatal("invalid running evidence was accepted")
			}
		})
	}
}

func TestLinuxSystemdCutoverRuntimeAndConfigBindingsRejectUnsafeFiles(t *testing.T) {
	tests := []struct {
		name string
		mode os.FileMode
	}{
		{name: "not executable", mode: 0o600},
		{name: "group writable", mode: 0o720},
		{name: "other writable", mode: 0o702},
		{name: "setuid", mode: os.ModeSetuid | 0o700},
		{name: "setgid", mode: os.ModeSetgid | 0o700},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLinuxSystemdFixture(t)
			if err := os.Chmod(fixture.runtime, test.mode); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(fixture.runtime)
			if err != nil {
				t.Fatal(err)
			}
			if test.mode&(os.ModeSetuid|os.ModeSetgid) != 0 && info.Mode()&(test.mode&(os.ModeSetuid|os.ModeSetgid)) == 0 {
				t.Skip("filesystem did not retain requested special mode")
			}
			_, err = fixture.manager.VerifyRunning(context.Background(), fixture.sha)
			if err == nil {
				t.Fatal("unsafe runtime mode was accepted")
			}
		})
	}

	fixture := newLinuxSystemdFixture(t)
	link := filepath.Join(t.TempDir(), "config-link")
	if err := os.Symlink(fixture.config, link); err != nil {
		t.Fatal(err)
	}
	manager, err := newLinuxSystemdCutoverServiceManagerWithDeps(fixture.runtime, link, linuxSystemdCutoverDependencies{
		run:          fixture.fake.run,
		readlink:     func(string) (string, error) { return fixture.runtime, nil },
		httpGet:      fixture.fake.httpGet,
		sleep:        fixture.fake.sleep,
		runningPolls: 1,
		stoppedPolls: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.VerifyRunning(context.Background(), fixture.sha); err == nil {
		t.Fatal("config symlink was accepted")
	}
}

func TestLinuxSystemdCutoverRunningRetriesTransientEvidence(t *testing.T) {
	fixture := newLinuxSystemdFixture(t)
	validShow := fixture.fake.showQueue[0]
	fixture.fake.showQueue = []linuxSystemdCommandResult{
		{ExitCode: 0, Stdout: strings.Replace(validShow.Stdout, "ActiveState=active", "ActiveState=activating", 1)},
		validShow,
	}
	fixture.fake.enabledQueue = []linuxSystemdCommandResult{{ExitCode: 0, Stdout: "enabled\n"}, {ExitCode: 0, Stdout: "enabled\n"}}
	fixture.fake.ssQueue = []linuxSystemdCommandResult{{ExitCode: 0, Stdout: linuxSystemdListeningSS(4242)}, {ExitCode: 0, Stdout: linuxSystemdListeningSS(4242)}}
	fixture.fake.httpQueue = []linuxSystemdHTTPResponse{{StatusCode: 200, Body: []byte(linuxSystemdReadyBody())}, {StatusCode: 200, Body: []byte(linuxSystemdReadyBody())}}
	fixture.manager.runningPolls = 2
	fixture.manager.pollInterval = 0
	if _, err := fixture.manager.VerifyRunning(context.Background(), fixture.sha); err != nil {
		t.Fatalf("transient running evidence = %v calls=%#v sleeps=%d", err, fixture.fake.calls, len(fixture.fake.sleeps))
	}
	if len(fixture.fake.sleeps) != 1 {
		t.Fatalf("sleep calls = %d, want 1", len(fixture.fake.sleeps))
	}

	readiness := newLinuxSystemdFixture(t)
	readiness.manager.runningPolls = 2
	readiness.manager.pollInterval = 0
	readiness.fake.httpQueue = []linuxSystemdHTTPResponse{
		{StatusCode: 503, Body: []byte(`{"ok":false,"status":"starting","service":"rencrow-core","runtime":"go","ready":false}`)},
		{StatusCode: 200, Body: []byte(linuxSystemdReadyBody())},
	}
	readiness.fake.showQueue = []linuxSystemdCommandResult{{ExitCode: 0, Stdout: linuxSystemdRunningShow(readiness.runtime, readiness.config)}, {ExitCode: 0, Stdout: linuxSystemdRunningShow(readiness.runtime, readiness.config)}}
	readiness.fake.enabledQueue = []linuxSystemdCommandResult{{ExitCode: 0, Stdout: "enabled\n"}, {ExitCode: 0, Stdout: "enabled\n"}}
	readiness.fake.ssQueue = []linuxSystemdCommandResult{{ExitCode: 0, Stdout: linuxSystemdListeningSS(4242)}, {ExitCode: 0, Stdout: linuxSystemdListeningSS(4242)}}
	if _, err := readiness.manager.VerifyRunning(context.Background(), readiness.sha); err != nil {
		t.Fatalf("transient readiness = %v", err)
	}
}

func TestLinuxSystemdCutoverStoppedRejectsInvalidEvidence(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*linuxSystemdFixture)
	}{
		{name: "load state missing mask", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, "LoadState=masked", "LoadState=not-found", 1)
		}},
		{name: "load state loaded", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, "LoadState=masked", "LoadState=loaded", 1)
		}},
		{name: "not masked", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, "UnitFileState=masked-runtime", "UnitFileState=enabled", 1)
		}},
		{name: "is-enabled mismatch", setup: func(f *linuxSystemdFixture) { f.fake.enabledQueue[0].Stdout = "enabled\n" }},
		{name: "active", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, "ActiveState=inactive", "ActiveState=active", 1)
		}},
		{name: "sub state", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, "SubState=dead", "SubState=running", 1)
		}},
		{name: "PID nonzero", setup: func(f *linuxSystemdFixture) {
			f.fake.showQueue[0].Stdout = strings.Replace(f.fake.showQueue[0].Stdout, "MainPID=0", "MainPID=4242", 1)
		}},
		{name: "listener remains", setup: func(f *linuxSystemdFixture) { f.fake.ssQueue[0].Stdout = linuxSystemdListeningSS(4242) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := linuxSystemdStoppedFixture(t)
			test.setup(&fixture)
			_, err := fixture.manager.VerifyStopped(context.Background())
			if err == nil {
				t.Fatal("invalid stopped evidence was accepted")
			}
		})
	}
}

func TestLinuxSystemdCutoverParsersFailClosed(t *testing.T) {
	if _, err := parseLinuxSystemdProperties("LoadState=loaded\nLoadState=loaded\n"); err == nil {
		t.Fatal("duplicate systemd property accepted")
	}
	if _, err := parseLinuxSystemdProperties("not-a-property\n"); err == nil {
		t.Fatal("malformed systemd property accepted")
	}
	if _, err := parseLinuxSystemdExecStart("{ path=/one; path=/two; }"); err == nil {
		t.Fatal("ambiguous ExecStart accepted")
	}
	if _, err := parseLinuxSystemdExecStart("ExecStart="); err == nil {
		t.Fatal("empty ExecStart accepted")
	}
	if got, err := parseLinuxSystemdExecStart("{ path=/canonical/rencrow; argv[]=/canonical/rencrow run ; }"); err != nil || got != "/canonical/rencrow" {
		t.Fatalf("rendered ExecStart = %q, %v", got, err)
	}
	if _, err := parseLinuxSystemdExecStart("/canonical/rencrow run"); err == nil {
		t.Fatal("simple ExecStart fallback accepted")
	}
	if got, err := parseLinuxSystemdExecStart(`{ path="/canonical/rencrow with space"; argv[]="/canonical/rencrow with space" run ; }`); err != nil || got != "/canonical/rencrow with space" {
		t.Fatalf("quoted ExecStart = %q, %v", got, err)
	}
	for _, value := range []string{
		"{ path=/canonical/rencrow; }",
		"{ path=/canonical/rencrow; argv[]=/canonical/rencrow start ; }",
		"{ path=/canonical/rencrow; argv[]=/canonical/rencrow run --extra ; }",
		"{ path=/canonical/rencrow; argv[]=/canonical/rencrow run ; argv[]=/canonical/rencrow run ; }",
	} {
		if _, err := parseLinuxSystemdExecStart(value); err == nil {
			t.Fatalf("invalid rendered ExecStart accepted: %q", value)
		}
	}
	if got, err := parseLinuxSystemdConfigPath("Environment=RENCROW_CONFIG=/canonical/core.yaml"); err != nil || got != "/canonical/core.yaml" {
		t.Fatalf("config environment = %q, %v", got, err)
	}
	if got, err := parseLinuxSystemdConfigPath(`Environment=RENCROW_CONFIG="/canonical/core config.yaml"`); err != nil || got != "/canonical/core config.yaml" {
		t.Fatalf("quoted config environment = %q, %v", got, err)
	}
	if _, err := parseLinuxSystemdConfigPath("RENCROW_CONFIG=/one RENCROW_CONFIG=/two"); err == nil {
		t.Fatal("ambiguous config environment accepted")
	}
	if found, _, err := parseLinuxSystemdListener("LISTEN 0 128 [::1]:18790 *:* users:(pid=42,fd=1)\n"); err != nil || !found {
		t.Fatalf("IPv6 listener = found %v, err %v", found, err)
	}
	if found, _, err := parseLinuxSystemdListener("LISTEN 0 128 127.0.0.1:18790 *:* users:(pid=42,fd=1)\nLISTEN 0 128 127.0.0.1:18790 *:* users:(pid=43,fd=1)\n"); err == nil || found {
		t.Fatalf("duplicate listener = found %v, err %v", found, err)
	}
	if _, err := parseLinuxSystemdLocalPort("127.0.0.1:0"); err == nil {
		t.Fatal("invalid listener port accepted")
	}
}

func TestLinuxSystemdCutoverBoundsContextAndErrors(t *testing.T) {
	fixture := newLinuxSystemdFixture(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.manager.VerifyRunning(canceled, fixture.sha); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled running = %v", err)
	}
	if len(fixture.fake.calls) != 0 {
		t.Fatalf("commands after canceled context = %#v", fixture.fake.calls)
	}
	if _, err := fixture.manager.VerifyRunning(context.Background(), strings.Repeat("A", 64)); err == nil {
		t.Fatal("uppercase runtime hash accepted")
	}

	commandOutput := newLinuxSystemdFixture(t)
	commandOutput.fake.showQueue[0].Stdout = strings.Repeat("x", linuxSystemdMaxOutputBytes+1)
	if _, err := commandOutput.manager.VerifyRunning(context.Background(), commandOutput.sha); err == nil {
		t.Fatal("oversized systemd output accepted")
	}

	readiness := newLinuxSystemdFixture(t)
	readiness.fake.httpQueue[0].Body = []byte(strings.Repeat("x", linuxSystemdMaxReadinessBytes+1))
	if _, err := readiness.manager.VerifyRunning(context.Background(), readiness.sha); err == nil {
		t.Fatal("oversized readiness accepted")
	}

	secret := "private-secret-path-payload"
	leak := newLinuxSystemdFixture(t)
	leak.fake.showQueue[0].Err = errors.New(secret)
	if _, err := leak.manager.VerifyRunning(context.Background(), leak.sha); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("command error leak = %v", err)
	}
	readlinkLeak := newLinuxSystemdFixture(t)
	readlinkLeak.manager.readlink = func(string) (string, error) { return "", errors.New(secret) }
	if _, err := readlinkLeak.manager.VerifyRunning(context.Background(), readlinkLeak.sha); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("readlink error leak = %v", err)
	}
	httpLeak := newLinuxSystemdFixture(t)
	httpLeak.fake.httpQueue[0] = linuxSystemdHTTPResponse{Err: errors.New(secret)}
	if _, err := httpLeak.manager.VerifyRunning(context.Background(), httpLeak.sha); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("readiness error leak = %v", err)
	}

	if _, err := fixture.manager.VerifyStopped(nil); err == nil {
		t.Fatal("nil stopped context accepted")
	}
}

func TestLinuxSystemdBoundedBufferReportsOverflow(t *testing.T) {
	buffer := &linuxSystemdBoundedBuffer{limit: 3}
	if n, err := buffer.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatalf("initial bounded write = %d, %v", n, err)
	}
	if n, err := buffer.Write([]byte("d")); !errors.Is(err, errLinuxSystemdOutputLimit) || n != 0 {
		t.Fatalf("overflow write = %d, %v", n, err)
	}
	if got := buffer.String(); got != "abc" {
		t.Fatalf("buffer after overflow = %q", got)
	}
	partial := &linuxSystemdBoundedBuffer{limit: 3}
	if n, err := partial.Write([]byte("abcd")); !errors.Is(err, errLinuxSystemdOutputLimit) || n != 3 || partial.String() != "abc" {
		t.Fatalf("partial overflow = n=%d err=%v body=%q", n, err, partial.String())
	}
}
