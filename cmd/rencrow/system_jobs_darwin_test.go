//go:build darwin

package main

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
)

func TestParseSystemJobsRuntimeStateUsesWholeStateLineAndPIDEvidence(t *testing.T) {
	tests := []struct {
		name   string
		output string
		pid    int
		want   string
	}{
		{name: "not running", output: "state = not running\n", want: "not_running"},
		{name: "waiting", output: "state = waiting\n", want: "waiting"},
		{name: "running with pid", output: "state = running\npid = 91\n", pid: 91, want: "running"},
		{name: "running without pid", output: "state = running\n", want: ""},
		{name: "running with zero pid", output: "state = running\npid = 0\n", want: ""},
		{name: "unknown state", output: "state = future-state\n", pid: 91, want: ""},
		{name: "embedded text is not a state row", output: "note = state = running\n", pid: 91, want: ""},
		{name: "empty state", output: "state = \n", pid: 91, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseSystemJobsRuntimeState([]byte(test.output), test.pid); got != test.want {
				t.Fatalf("state=%q, want %q", got, test.want)
			}
		})
	}
}

func TestDarwinSystemJobsRootsUseCanonicalLaunchdDirectories(t *testing.T) {
	roots := darwinSystemJobsRoots()
	if len(roots) != 4 {
		t.Fatalf("root count=%d, want 4: %+v", len(roots), roots)
	}
	if roots[0].id != "system_launch_daemons" || roots[0].domain != "system" || roots[0].path != "/Library/LaunchDaemons" {
		t.Fatalf("system launchd root=%+v, want /Library/LaunchDaemons in system domain", roots[0])
	}
	systemPlistPath, err := canonicalResilienceStackPlistPathForHome(resilienceStackDomain, "com.rencrow.core", "")
	if err != nil || filepath.Dir(systemPlistPath) != roots[0].path {
		t.Fatalf("system root differs from canonical resolver: root=%q canonical=%q err=%v", roots[0].path, systemPlistPath, err)
	}

	uid := os.Getuid()
	domain := "user/" + strconv.Itoa(uid)
	account, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		t.Fatal(err)
	}
	launchAgents, rencrowLaunchd := filepath.Join(account.HomeDir, "Library", "LaunchAgents"), filepath.Join(account.HomeDir, ".rencrow", "launchd")
	if uid == 501 {
		if plistPath, pathErr := canonicalResilienceStackPlistPathForHome(resilienceStackUser501, "com.rencrow.core", account.HomeDir); pathErr == nil {
			launchAgents = filepath.Dir(plistPath)
		}
		if plistPath, pathErr := canonicalResilienceStackPlistPathForHome(resilienceStackUser501, "com.rencrow.portal", account.HomeDir); pathErr == nil {
			rencrowLaunchd = filepath.Dir(plistPath)
		}
	}
	if roots[1].id != "shared_launch_agents" || roots[1].domain != domain || roots[1].path != "/Library/LaunchAgents" {
		t.Fatalf("shared LaunchAgents root=%+v, want domain=%q path=%q", roots[1], domain, "/Library/LaunchAgents")
	}
	if roots[2].id != "user_launch_agents" || roots[2].domain != domain || roots[2].path != launchAgents {
		t.Fatalf("user LaunchAgents root=%+v, want domain=%q path=%q", roots[2], domain, launchAgents)
	}
	if roots[3].id != "user_rencrow_launchd" || roots[3].domain != domain || roots[3].path != rencrowLaunchd {
		t.Fatalf("RenCrow launchd root=%+v, want domain=%q path=%q", roots[3], domain, rencrowLaunchd)
	}
}
