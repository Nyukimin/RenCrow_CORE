package conversation

import (
	"errors"
	"fmt"
	"strings"
)

const (
	NamespaceKindConversation = "conv"
	NamespaceKindUser         = "user"
	NamespaceKindCharacter    = "char"
	NamespaceKindKnowledge    = "kb"
)

func BuildL1Namespace(kind string, id string) (string, error) {
	kind = strings.TrimSpace(kind)
	id = strings.TrimSpace(id)
	if id == "" {
		return "", errors.New("l1 namespace id is required")
	}
	switch kind {
	case NamespaceKindConversation, NamespaceKindUser, NamespaceKindCharacter, NamespaceKindKnowledge:
		return kind + ":" + id, nil
	default:
		return "", fmt.Errorf("invalid l1 namespace kind: %s", kind)
	}
}

func ValidateL1Namespace(namespace string) error {
	namespace = strings.TrimSpace(namespace)
	parts := strings.SplitN(namespace, ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("invalid l1 namespace: %s", namespace)
	}
	switch parts[0] {
	case NamespaceKindConversation, NamespaceKindUser, NamespaceKindCharacter, NamespaceKindKnowledge:
		return nil
	default:
		return fmt.Errorf("invalid l1 namespace kind: %s", parts[0])
	}
}
