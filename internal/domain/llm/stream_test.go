package llm

import (
	"context"
	"testing"
)

func TestStreamCallbackContextRoundTrip(t *testing.T) {
	if cb := StreamCallbackFromContext(context.Background()); cb != nil {
		t.Fatal("empty context should not contain stream callback")
	}

	var tokens []string
	cb := StreamCallback(func(token string) {
		tokens = append(tokens, token)
	})
	ctx := ContextWithStreamCallback(context.Background(), cb)
	got := StreamCallbackFromContext(ctx)
	if got == nil {
		t.Fatal("expected callback from context")
	}
	got("hello")
	got(" world")
	if len(tokens) != 2 || tokens[0] != "hello" || tokens[1] != " world" {
		t.Fatalf("callback tokens mismatch: %#v", tokens)
	}
}
