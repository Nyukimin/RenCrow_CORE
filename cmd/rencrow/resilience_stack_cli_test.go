package main

import (
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
