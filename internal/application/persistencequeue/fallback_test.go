package persistencequeue

import (
	"context"
	"errors"
	"testing"
)

func TestSynchronousFallbackCapabilityIsExplicitAndPreservesCallbackResult(t *testing.T) {
	type contextKey struct{}
	base := context.WithValue(context.Background(), contextKey{}, "preserved")
	want := context.WithValue(context.Background(), contextKey{}, "fallback")
	wantErr := errors.New("barrier failed")
	var calls int
	withFallback := WithSynchronousFallback(base, func(ctx context.Context) (context.Context, error) {
		calls++
		if ctx != base {
			t.Fatalf("fallback input context=%v, want original context", ctx)
		}
		return want, wantErr
	})
	fallback, ok := SynchronousFallbackFromContext(withFallback)
	if !ok {
		t.Fatal("SynchronousFallbackFromContext() did not find the explicit capability")
	}
	got, err := fallback(base)
	if got != want || !errors.Is(err, wantErr) || calls != 1 {
		t.Fatalf("fallback result=(%v,%v), calls=%d; want (%v,%v), one call", got, err, calls, want, wantErr)
	}
	if base.Value(contextKey{}) != "preserved" || withFallback.Value(contextKey{}) != "preserved" {
		t.Fatal("adding the fallback capability changed existing context values")
	}
}

func TestSynchronousFallbackCapabilityIsAbsentWithoutExplicitCallback(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"nil context":  nil,
		"background":   context.Background(),
		"nil callback": WithSynchronousFallback(context.Background(), nil),
	} {
		t.Run(name, func(t *testing.T) {
			if fallback, ok := SynchronousFallbackFromContext(ctx); ok || fallback != nil {
				t.Fatalf("SynchronousFallbackFromContext()=(%v,%t), want (nil,false)", fallback, ok)
			}
		})
	}
}
