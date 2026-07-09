package l1sqlite

import "testing"

func TestBuildL1Namespace(t *testing.T) {
	tests := []struct {
		name string
		kind string
		id   string
		want string
	}{
		{name: "conversation", kind: NamespaceKindConversation, id: "123", want: "conv:123"},
		{name: "user", kind: NamespaceKindUser, id: "U123", want: "user:U123"},
		{name: "character", kind: NamespaceKindCharacter, id: "mio", want: "char:mio"},
		{name: "knowledge", kind: NamespaceKindKnowledge, id: "movie", want: "kb:movie"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildL1Namespace(tt.kind, tt.id)
			if err != nil {
				t.Fatalf("BuildL1Namespace failed: %v", err)
			}
			if got != tt.want {
				t.Fatalf("BuildL1Namespace: want %q, got %q", tt.want, got)
			}
		})
	}
}

func TestValidateL1Namespace(t *testing.T) {
	valid := []string{"conv:123", "user:U123", "char:mio", "kb:movie"}
	for _, namespace := range valid {
		if err := ValidateL1Namespace(namespace); err != nil {
			t.Fatalf("ValidateL1Namespace(%q) failed: %v", namespace, err)
		}
	}

	invalid := []string{"", "conv:", "user:", "char:", "kb:", "misc:123", "conv:  "}
	for _, namespace := range invalid {
		if err := ValidateL1Namespace(namespace); err == nil {
			t.Fatalf("ValidateL1Namespace(%q) should fail", namespace)
		}
	}
}
