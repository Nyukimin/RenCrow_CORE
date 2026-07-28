package worker

import (
	"errors"
	"strings"
	"testing"
)

func TestIsAutonomousRoute(t *testing.T) {
	for _, route := range []string{"OPS", "CODE", "CODE1", "CODE2", "CODE3", "CODE4", "PLAN", "ANALYZE", "RESEARCH", "WILD"} {
		if !IsAutonomousRoute(route) {
			t.Fatalf("%s should be autonomous", route)
		}
	}
	if IsAutonomousRoute("CHAT") {
		t.Fatal("CHAT should not be autonomous")
	}
}

func TestCapabilityForRoute(t *testing.T) {
	if got := CapabilityForRoute("CODE2"); got != CapabilityCodeChange {
		t.Fatalf("code capability = %s", got)
	}
	if got := CapabilityForRoute("OPS"); got != CapabilityGenericExecution {
		t.Fatalf("ops capability = %s", got)
	}
}

func TestRouteExecutionSteps(t *testing.T) {
	code := RouteExecutionSteps("CODE", true)
	for _, want := range []string{"routing.decision", "shiro.delegate", "coder.execute", "shiro.verify", "done"} {
		if !containsWorkerString(code, want) {
			t.Fatalf("CODE steps missing %s: %+v", want, code)
		}
	}
	ops := RouteExecutionSteps("OPS", false)
	if !containsWorkerString(ops, "shiro.execute") || !containsWorkerString(ops, "error") {
		t.Fatalf("OPS failure steps unexpected: %+v", ops)
	}
}

func TestClassifyExecutorFailure(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{errors.New("proposal is invalid"), FailureProposalInvalid},
		// 素の "not found" は広すぎるため判定に含めない（"module not found" 等を
		// コマンド不在と誤分類する）。実際の os/exec の文言で検証する
		{errors.New(`exec: "binary": executable file not found in $PATH`), FailureCommandMissing},
		{errors.New(`'binary' is not recognized as an internal or external command`), FailureCommandMissing},
		{errors.New("gateway model unavailable"), FailureProviderUnavailable},
		{errors.New("patch failed"), FailureApply},
	}
	for _, tt := range tests {
		if got := ClassifyExecutorFailure(tt.err); got != tt.want {
			t.Fatalf("failure = %s, want %s", got, tt.want)
		}
	}
}

func TestBuildExecutorRetryMessage(t *testing.T) {
	got := BuildExecutorRetryMessage("do it", "CODE", "", "", 2)
	for _, want := range []string{"do it", "retry_attempt: 2", "route: CODE", "failure_kind: unknown", "failure_reason: execution failed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("retry message missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Do not defer required fixes to the user") {
		t.Fatalf("retry message should not force manual-only commands into executable output:\n%s", got)
	}
	if !strings.Contains(got, "policy blocks") || !strings.Contains(got, "policy reason") {
		t.Fatalf("retry message missing manual-only lifecycle guidance:\n%s", got)
	}
}

func containsWorkerString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
