//go:build !darwin

package main

import (
	"context"
	"errors"
	"os"
)

type unsupportedResilienceStackOperator struct{}

func newResilienceStackOperator() resilienceStackOperator {
	return unsupportedResilienceStackOperator{}
}

func resilienceStackFileOwner(os.FileInfo) (int, error) {
	return 0, errors.New("launchd stack file ownership checks are available only on macOS")
}

func acquireResilienceStackLock() (func(), error) {
	return nil, errors.New("external launchd stack reconciliation is available only on macOS")
}

func (unsupportedResilienceStackOperator) ValidateTarget(context.Context, resilienceStackService) error {
	return errors.New("external launchd stack reconciliation is available only on macOS")
}

func (unsupportedResilienceStackOperator) Inspect(context.Context, resilienceStackService) (resilienceStackSnapshot, error) {
	return resilienceStackSnapshot{}, errors.New("external launchd stack reconciliation is available only on macOS")
}

func (unsupportedResilienceStackOperator) Readiness(context.Context, resilienceStackService) error {
	return errors.New("external launchd stack reconciliation is available only on macOS")
}

func (unsupportedResilienceStackOperator) Bootout(context.Context, resilienceStackService) error {
	return errors.New("external launchd stack reconciliation is available only on macOS")
}

func (unsupportedResilienceStackOperator) Bootstrap(context.Context, resilienceStackService) error {
	return errors.New("external launchd stack reconciliation is available only on macOS")
}
