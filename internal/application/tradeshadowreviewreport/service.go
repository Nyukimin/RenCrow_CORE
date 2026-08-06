package tradeshadowreviewreport

import (
	"context"
	"errors"
	"fmt"
	"strings"

	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

var (
	ErrInvalidRequest = errors.New("invalid TRADE Shadow review report request")
	ErrUnavailable    = errors.New("TRADE Shadow review report unavailable")
)

type ModuleReader interface {
	ShadowReviewReport(context.Context, string, string) (moduletrade.PrivateShadowReviewReport, error)
}

type Service struct{ reader ModuleReader }

type Request struct {
	RequestID string
	StudyID   string
}
type Result struct {
	Report *moduletrade.PrivateShadowReviewReport `json:"report,omitempty"`
}

func NewService(reader ModuleReader) (*Service, error) {
	if reader == nil {
		return nil, fmt.Errorf("TRADE Shadow review report reader is required")
	}
	return &Service{reader: reader}, nil
}

func (service *Service) Report(ctx context.Context, request Request) (Result, error) {
	if strings.TrimSpace(request.StudyID) == "" {
		return Result{}, ErrInvalidRequest
	}
	report, err := service.reader.ShadowReviewReport(ctx, request.RequestID, request.StudyID)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return Result{Report: &report}, nil
}
