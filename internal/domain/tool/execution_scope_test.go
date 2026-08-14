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

func TestToolExecutionScopeInternalDataScopeRequiresAuthenticatedAgentOrchestrator(t *testing.T) {
	valid := ToolExecutionScope{
		RequestID:            "req-internal-valid",
		ActorKind:            ActorKindAgent,
		ActorID:              "mio",
		AllowedDataScopes:    []string{DataScopeInternal},
		AuthenticationSource: AuthenticationSourceAgentOrchestrator,
		AgentRole:            "worker",
		Purpose:              "ops",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("authenticated agent internal scope error = %v", err)
	}
	if !valid.Allows(DataScopeInternal) {
		t.Fatal("authenticated agent internal scope must be granted")
	}

	cases := []ToolExecutionScope{
		{
			RequestID:            "req-internal-http-agent",
			ActorKind:            ActorKindAgent,
			ActorID:              "mio",
			AllowedDataScopes:    []string{DataScopeInternal},
			AuthenticationSource: AuthenticationSourceHTTP,
			AgentRole:            "worker",
			Purpose:              "ops",
		},
		{
			RequestID:            "req-internal-user",
			ActorKind:            ActorKindUser,
			ActorID:              "user-a",
			AuthenticatedUserID:  "user-a",
			AllowedDataScopes:    []string{DataScopeInternal},
			AuthenticationSource: AuthenticationSourceAgentOrchestrator,
			AgentRole:            "worker",
			Purpose:              "ops",
		},
		{
			RequestID:            "req-internal-http-user",
			ActorKind:            ActorKindUser,
			ActorID:              "user-a",
			AuthenticatedUserID:  "user-a",
			AllowedDataScopes:    []string{DataScopeInternal},
			AuthenticationSource: AuthenticationSourceHTTP,
			AgentRole:            "worker",
			Purpose:              "ops",
		},
		{
			RequestID:            "req-internal-missing-role",
			ActorKind:            ActorKindAgent,
			ActorID:              "mio",
			AllowedDataScopes:    []string{DataScopeInternal},
			AuthenticationSource: AuthenticationSourceAgentOrchestrator,
			Purpose:              "ops",
		},
		{
			RequestID:            "req-internal-missing-purpose",
			ActorKind:            ActorKindAgent,
			ActorID:              "mio",
			AllowedDataScopes:    []string{DataScopeInternal},
			AuthenticationSource: AuthenticationSourceAgentOrchestrator,
			AgentRole:            "worker",
		},
	}
	for _, scope := range cases {
		if err := scope.Validate(); err == nil {
			t.Fatalf("scope %#v must reject internal access", scope)
		}
	}
}

func TestDeriveAgentToolExecutionScopeCarriesOnlyValidatedDelegatedScopes(t *testing.T) {
	parent := ToolExecutionScope{
		RequestID:            "req-parent",
		ActorKind:            ActorKindUser,
		ActorID:              "user-a",
		AuthenticatedUserID:  "user-a",
		AllowedDataScopes:    []string{DataScopePublic, DataScopeUser},
		AuthenticationSource: AuthenticationSourceHTTP,
	}
	ctx := WithToolExecutionScope(context.Background(), parent)

	derivedCtx, err := DeriveAgentToolExecutionScope(ctx, "req-child", "shiro", "worker", "ops", true)
	if err != nil {
		t.Fatalf("DeriveAgentToolExecutionScope() error = %v", err)
	}
	derived, ok := ToolExecutionScopeFromContext(derivedCtx)
	if !ok {
		t.Fatal("derived context is missing trusted scope")
	}
	if derived.RequestID != "req-child" || derived.ActorKind != ActorKindAgent || derived.ActorID != "shiro" {
		t.Fatalf("derived identity = %#v", derived)
	}
	if derived.AuthenticationSource != AuthenticationSourceAgentOrchestrator || derived.AgentRole != "worker" || derived.Purpose != "ops" {
		t.Fatalf("derived trust metadata = %#v", derived)
	}
	if derived.AuthenticatedUserID != "user-a" {
		t.Fatalf("authenticated user = %q, want user-a", derived.AuthenticatedUserID)
	}
	if !derived.Allows(DataScopePublic) || !derived.Allows(DataScopeUser) || !derived.Allows(DataScopeInternal) || len(derived.AllowedDataScopes) != 3 {
		t.Fatalf("derived data scopes = %#v", derived.AllowedDataScopes)
	}
}

func TestDeriveAgentToolExecutionScopeDoesNotInheritInternalWithoutGrant(t *testing.T) {
	parent := ToolExecutionScope{
		RequestID:            "req-parent",
		ActorKind:            ActorKindAgent,
		ActorID:              "mio",
		AllowedDataScopes:    []string{DataScopePublic, DataScopeInternal},
		AuthenticationSource: AuthenticationSourceAgentOrchestrator,
		AgentRole:            "worker",
		Purpose:              "ops",
	}
	ctx := WithToolExecutionScope(context.Background(), parent)

	derivedCtx, err := DeriveAgentToolExecutionScope(ctx, "req-child", "shiro", "worker", "ops", false)
	if err != nil {
		t.Fatalf("DeriveAgentToolExecutionScope() error = %v", err)
	}
	derived, ok := ToolExecutionScopeFromContext(derivedCtx)
	if !ok {
		t.Fatal("derived context is missing trusted scope")
	}
	if derived.Allows(DataScopeInternal) {
		t.Fatalf("derived scope inherited internal access: %#v", derived)
	}
	if !derived.Allows(DataScopePublic) {
		t.Fatalf("derived scope lost public access: %#v", derived)
	}
}

func TestDeriveAgentToolExecutionScopeRejectsInvalidParentBeforeGrantingInternal(t *testing.T) {
	parent := ToolExecutionScope{
		RequestID:            "req-parent",
		ActorKind:            ActorKindAgent,
		ActorID:              "mio",
		AllowedDataScopes:    []string{DataScopeInternal},
		AuthenticationSource: AuthenticationSourceAgentOrchestrator,
	}
	_, err := DeriveAgentToolExecutionScope(WithToolExecutionScope(context.Background(), parent), "req-child", "shiro", "worker", "ops", true)
	if err == nil {
		t.Fatal("invalid parent scope must be rejected before internal scope derivation")
	}
}

func TestToolExecutionScopeCannotBeReadFromEmptyContext(t *testing.T) {
	if _, ok := ToolExecutionScopeFromContext(context.Background()); ok {
		t.Fatal("empty context must not produce an authenticated scope")
	}
}
