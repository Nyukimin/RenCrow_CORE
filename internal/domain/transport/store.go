package transport

import (
	"context"
	"errors"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var ErrNotFound = errors.New("transport receipt not found")

type Store interface {
	Transaction(context.Context, func(Store) error) error
	ReadTransaction(context.Context, func(Store) error) error
	SaveRequest(context.Context, Request) error
	GetRequest(context.Context, modulecore.RequestID) (Request, error)
	ListRequests(context.Context, RequestFilter) ([]Request, error)
	SaveResponse(context.Context, Response) error
	GetResponse(context.Context, modulecore.ResponseID) (Response, error)
	ListResponses(context.Context, ResponseFilter) ([]Response, error)
}
