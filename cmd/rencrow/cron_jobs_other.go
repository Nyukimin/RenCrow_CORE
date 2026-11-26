//go:build !darwin

package main

import (
	"context"
	"time"
)

type cronJobsRecord struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Type         string `json:"record_type"`
	Schedule     string `json:"schedule"`
	RuntimeState string `json:"runtime_state"`
	OwnerState   string `json:"owner_state"`
	NextRun      string `json:"next_run"`
}

type cronJobsSnapshot struct {
	ObservedAt       time.Time        `json:"observed_at"`
	Availability     string           `json:"availability"`
	Scope            string           `json:"scope"`
	UnobservedScopes []string         `json:"unobserved_scopes"`
	SourceState      string           `json:"source_state"`
	Issues           []string         `json:"issues"`
	Records          []cronJobsRecord `json:"records"`
}

type unsupportedCronJobsReader struct{}

func newCronJobsReader() unsupportedCronJobsReader { return unsupportedCronJobsReader{} }

func (unsupportedCronJobsReader) ReadCronJobs(context.Context) cronJobsSnapshot {
	return cronJobsSnapshot{Availability: "unsupported", Scope: "current_user_crontab", UnobservedScopes: []string{"other_users", "system"}, SourceState: "unsupported", Issues: []string{"platform_unsupported"}, Records: []cronJobsRecord{}}
}
