package llm

import (
	"context"
	"strings"
)

type busySourceContextKey struct{}

func WithBusySource(ctx context.Context, source string) context.Context {
	source = strings.TrimSpace(source)
	if source == "" {
		return ctx
	}
	return context.WithValue(ctx, busySourceContextKey{}, source)
}

func BusySourceFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	source, _ := ctx.Value(busySourceContextKey{}).(string)
	return strings.TrimSpace(source)
}
