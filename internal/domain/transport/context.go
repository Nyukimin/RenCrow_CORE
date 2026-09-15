package transport

import (
	"context"
	"errors"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type ReceiptOwner interface {
	BeginRequest(context.Context, string) (Request, error)
	CompleteResponse(context.Context, modulecore.RequestID, string) (Response, error)
	FailWithoutResponse(context.Context, modulecore.RequestID, string) error
}

type AssignedRequestReceiptOwner interface {
	ReceiptOwner
	BeginAssignedRequest(context.Context, string, modulecore.RequestID) (Request, error)
}

type receiptOwnerContextKey struct{}

func WithReceiptOwner(ctx context.Context, owner ReceiptOwner) (context.Context, error) {
	if ctx == nil {
		return nil, errors.New("transport receipt context is nil")
	}
	if owner == nil {
		return nil, errors.New("transport receipt owner is required")
	}
	if _, exists := ReceiptOwnerFromContext(ctx); exists {
		return nil, errors.New("transport receipt owner is already bound")
	}
	return context.WithValue(ctx, receiptOwnerContextKey{}, owner), nil
}

func ReceiptOwnerFromContext(ctx context.Context) (ReceiptOwner, bool) {
	if ctx == nil {
		return nil, false
	}
	owner, ok := ctx.Value(receiptOwnerContextKey{}).(ReceiptOwner)
	return owner, ok && owner != nil
}
