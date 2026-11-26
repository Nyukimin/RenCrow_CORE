//go:build !darwin

package main

import (
	"context"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
)

type unsupportedSystemJobsReader struct{}

func newSystemJobsReader() viewer.SystemJobsReader {
	return unsupportedSystemJobsReader{}
}

func (unsupportedSystemJobsReader) ReadSystemJobs(context.Context) (viewer.SystemJobsSnapshot, error) {
	return viewer.SystemJobsSnapshot{
		ObservedAt: time.Now().UTC(), Availability: "unsupported",
		Sources: []viewer.SystemJobsSource{{
			ID: "launchd_unavailable", Domain: "system", State: "unsupported", Issues: []string{"platform_unsupported"},
		}},
		Records: []viewer.SystemJobRecord{},
	}, nil
}
