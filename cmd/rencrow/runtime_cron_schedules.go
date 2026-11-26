package main

import (
	"context"
	"errors"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
)

type registeredCronScheduleReader struct {
	read func(context.Context) cronJobsSnapshot
}

func newRegisteredCronScheduleReader() registeredCronScheduleReader {
	reader := newCronJobsReader()
	return registeredCronScheduleReader{read: reader.ReadCronJobs}
}

func (reader registeredCronScheduleReader) ListRegisteredCronSchedules(ctx context.Context) ([]viewer.RegisteredScheduleCronRecord, string, error) {
	if reader.read == nil {
		return nil, "", errors.New("cron reader unavailable")
	}
	snapshot := reader.read(ctx)
	if snapshot.Scope != "current_user_crontab" {
		return nil, "", errors.New("cron scope unavailable")
	}
	availability := snapshot.Availability
	switch availability {
	case "available", "partial":
	case "empty":
		availability = "available"
	default:
		return nil, "", errors.New("cron observation unavailable")
	}
	items := make([]viewer.RegisteredScheduleCronRecord, 0, len(snapshot.Records))
	for _, record := range snapshot.Records {
		items = append(items, viewer.RegisteredScheduleCronRecord{
			ID: record.ID, Title: record.Title, Schedule: record.Schedule,
			Status: record.RuntimeState, RecordType: record.Type,
		})
	}
	return items, availability, nil
}
