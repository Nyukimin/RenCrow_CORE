package tradeshadowoutcomereport

import (
	"context"
	"errors"
	"fmt"
	"strings"

	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

var (
	ErrInvalidRequest = errors.New("invalid TRADE Shadow outcome report request")
	ErrUnavailable    = errors.New("TRADE Shadow outcome report unavailable")
)

type ModuleReader interface {
	ShadowOutcomeReport(ctx context.Context, correlationID, studyID string) (moduletrade.PrivateShadowOutcomeReport, error)
}

type Service struct {
	reader ModuleReader
}

type Request struct {
	RequestID string
	StudyID   string
}

type Result struct {
	Report *moduletrade.PrivateShadowOutcomeReport `json:"report,omitempty"`
}

func NewService(reader ModuleReader) (*Service, error) {
	if reader == nil {
		return nil, fmt.Errorf("TRADE Shadow outcome report reader is required")
	}
	return &Service{reader: reader}, nil
}

func (service *Service) Report(ctx context.Context, request Request) (Result, error) {
	if strings.TrimSpace(request.StudyID) == "" {
		return Result{}, ErrInvalidRequest
	}
	report, err := service.reader.ShadowOutcomeReport(ctx, request.RequestID, request.StudyID)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return Result{Report: &report}, nil
}
