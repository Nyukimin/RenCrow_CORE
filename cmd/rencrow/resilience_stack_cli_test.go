package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestResilienceStackCLIUsesFailClosedStackPlan(t *testing.T) {
	if os.Getenv("RENCROW_TEST_STACK_CLI_CHILD") == "1" {
		os.Args = []string{"rencrow", "resilience", "stack-reconcile"}
		cmdResilience()
		return
	}
	skipIfResilienceStackPlanIsInstalled(t)

	cmd := exec.Command(os.Args[0], "-test.run=^TestResilienceStackCLIUsesFailClosedStackPlan$")
	cmd.Env = append(os.Environ(), "RENCROW_TEST_STACK_CLI_CHILD=1")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("stack-reconcile must fail closed when no approved plan is installed")
	}
	if !strings.Contains(string(output), "stack plan") {
		t.Fatalf("stack-reconcile was not routed to its plan preflight: err=%v output=%s", err, output)
	}
}

func TestResilienceStackCheckCLIUsesFailClosedStackPlan(t *testing.T) {
	if os.Getenv("RENCROW_TEST_STACK_CHECK_CLI_CHILD") == "1" {
		os.Args = []string{"rencrow", "resilience", "stack-check"}
		cmdResilience()
		return
	}
	skipIfResilienceStackPlanIsInstalled(t)

	cmd := exec.Command(os.Args[0], "-test.run=^TestResilienceStackCheckCLIUsesFailClosedStackPlan$")
	cmd.Env = append(os.Environ(), "RENCROW_TEST_STACK_CHECK_CLI_CHILD=1")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("stack-check must fail closed when no approved plan is installed")
	}
	if !strings.Contains(string(output), "stack plan") {
		t.Fatalf("stack-check was not routed to its plan preflight: err=%v output=%s", err, output)
	}
}

func skipIfResilienceStackPlanIsInstalled(t *testing.T) {
	t.Helper()
	_, err := os.Lstat(resilienceStackPlanPath)
	if err == nil {
		t.Skip("the protected system stack plan is installed; refusing to run a CLI test against it")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Skipf("cannot prove the protected stack plan is absent: %v", err)
	}
}

func TestResilienceStackLaunchdTemplateUsesFixedMonitorEveryTenSeconds(t *testing.T) {
	data, err := os.ReadFile("../../deploy/macos/com.rencrow.resilience-stack-monitor.plist.template")
	if err != nil {
		t.Fatal(err)
	}
	contents := string(data)
	for _, want := range []string{
		"<string>/usr/local/libexec/rencrow-stack-monitor</string>",
		"<string>resilience</string>",
		"<string>stack-reconcile</string>",
		"<key>RunAtLoad</key>",
		"<key>StartInterval</key>",
		"<integer>10</integer>",
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("launchd template is missing %q", want)
		}
	}
	if strings.Contains(contents, ".sh</string>") || strings.Contains(contents, "/bin/sh") {
		t.Fatal("root launchd template must invoke the fixed monitor binary, not a script")
	}
}
