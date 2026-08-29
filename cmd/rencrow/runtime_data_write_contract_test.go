package main

import (
	"reflect"
	"testing"
)

func assertRuntimeDataWriteEContract(t *testing.T, routes []runtimeDataWriteRoute, want runtimeDataWriteRoute) {
	t.Helper()
	if len(routes) != 1 || routes[0].Store != want.Store || routes[0].Operation != want.Operation || routes[0].Access != want.Access || !reflect.DeepEqual(routes[0].RequiredPayloadFields, want.RequiredPayloadFields) || !reflect.DeepEqual(routes[0].OptionalPayloadFields, want.OptionalPayloadFields) {
		t.Fatalf("routes=%#v want=%#v", routes, want)
	}
}
