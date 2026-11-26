package main

import (
	"context"
	"testing"
)

func TestRegisteredCronScheduleReaderMapsScopeAndAvailability(t *testing.T) {
	tests := []struct {
		name         string
		snapshot     cronJobsSnapshot
		availability string
		wantError    bool
	}{
		{"available", cronJobsSnapshot{Scope: "current_user_crontab", Availability: "available", Records: []cronJobsRecord{{ID: "cron-1", Title: "ログ整理", Type: "job", Schedule: "0 * * * *", RuntimeState: "unknown"}}}, "available", false},
		{"partial", cronJobsSnapshot{Scope: "current_user_crontab", Availability: "partial"}, "partial", false},
		{"no crontab", cronJobsSnapshot{Scope: "current_user_crontab", Availability: "empty"}, "available", false},
		{"timeout", cronJobsSnapshot{Scope: "current_user_crontab", Availability: "timeout"}, "", true},
		{"unsupported", cronJobsSnapshot{Scope: "current_user_crontab", Availability: "unsupported"}, "", true},
		{"wrong scope", cronJobsSnapshot{Scope: "system", Availability: "available"}, "", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := registeredCronScheduleReader{read: func(context.Context) cronJobsSnapshot { return test.snapshot }}
			records, availability, err := reader.ListRegisteredCronSchedules(context.Background())
			if (err != nil) != test.wantError || availability != test.availability {
				t.Fatalf("availability=%q error=%v", availability, err)
			}
			if test.name == "available" && (len(records) != 1 || records[0].RecordType != "job" || records[0].Status != "unknown" || records[0].Schedule != "0 * * * *") {
				t.Fatalf("mapped records=%+v", records)
			}
		})
	}
}
