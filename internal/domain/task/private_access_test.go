package task

import "testing"

func TestTaskPrivateAccessRequiresUserScopeAndOwner(t *testing.T) {
	for _, test := range []struct {
		name   string
		access TaskPrivateAccess
		valid  bool
	}{
		{name: "valid user", access: TaskPrivateAccess{Scope: TaskAccessScopeUser, AuthenticatedUserID: "user-1"}, valid: true},
		{name: "empty owner", access: TaskPrivateAccess{Scope: TaskAccessScopeUser}},
		{name: "padded owner", access: TaskPrivateAccess{Scope: TaskAccessScopeUser, AuthenticatedUserID: " user-1 "}},
		{name: "unknown scope", access: TaskPrivateAccess{Scope: "internal", AuthenticatedUserID: "user-1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.access.Validate()
			if test.valid && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("Validate() unexpectedly accepted invalid private access")
			}
		})
	}
}
