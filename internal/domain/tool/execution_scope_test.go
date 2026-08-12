package tool

import (
	"context"
	"reflect"
	"testing"
)

func TestToolExecutionScopeValidationAndContextRoundTrip(t *testing.T) {
	scope, err := NewToolExecutionScope(
		"req-1",
		ActorKindAgent,
		"mio",
		"user-a",
		[]string{DataScopePublic, DataScopeUser},
		AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope() error = %v", err)
	}

	ctx := WithToolExecutionScope(context.Background(), scope)
	got, ok := ToolExecutionScopeFromContext(ctx)
	if !ok {
		t.Fatal("ToolExecutionScopeFromContext() did not find trusted scope")
	}
	if !reflect.DeepEqual(got, scope) {
		t.Fatalf("scope = %#v, want %#v", got, scope)
	}

	// The constructor owns a copy of the caller's slice and the context value
	// cannot be changed by mutating that original slice.
	scope.AllowedDataScopes[0] = "forged"
	if got.AllowedDataScopes[0] != DataScopePublic {
		t.Fatalf("context scope shared mutable slice: %#v", got.AllowedDataScopes)
	}
}

func TestToolExecutionScopeRejectsInvalidOrConflictingClaims(t *testing.T) {
	cases := []ToolExecutionScope{
		{},
		{RequestID: "req", ActorKind: ActorKindUser, ActorID: "user-a", AuthenticationSource: AuthenticationSourceHTTP},
		{RequestID: "req", ActorKind: ActorKindAgent, ActorID: "mio", AllowedDataScopes: []string{DataScopeUser}, AuthenticationSource: AuthenticationSourceAgentOrchestrator},
		{RequestID: "req", ActorKind: ActorKindAgent, ActorID: "mio", AuthenticatedUserID: "user-a", AllowedDataScopes: []string{"private"}, AuthenticationSource: AuthenticationSourceAgentOrchestrator},
		{RequestID: "req", ActorKind: ActorKindAgent, ActorID: "mio", AllowedDataScopes: []string{DataScopePublic, DataScopePublic}, AuthenticationSource: AuthenticationSourceAgentOrchestrator},
		{RequestID: "req", ActorKind: ActorKindAgent, ActorID: "mio", AuthenticatedUserID: "user-a", AllowedDataScopes: []string{DataScopePublic}, AuthenticationSource: "untrusted"},
	}
	for i, scope := range cases {
		if err := scope.Validate(); err == nil {
			t.Fatalf("case %d scope %#v should fail closed", i, scope)
		}
	}
}

func TestToolExecutionScopeCannotBeReadFromEmptyContext(t *testing.T) {
	if _, ok := ToolExecutionScopeFromContext(context.Background()); ok {
		t.Fatal("empty context must not produce an authenticated scope")
	}
}
