package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestParseCronJobsKeepsOnlySafeRenCrowProjection(t *testing.T) {
	input := strings.Join([]string{
		"# rencrow comment is not a registration",
		"PATH=/private/secret rencrow-secret-env",
		"*/5 1-4 * JAN,MAR 1-5 /private/bin/obs-supervisor --token=secret-token",
		"@hourly /private/bin/log-rotate-guard SECRET_ENV=private",
		"17 2 * * * /private/bin/future-rencrow-job --password=private",
		"18 3 * * * /bin/sh -c '/private/bin/other-rencrow-task --secret=hidden'",
		"17 2 * * * /bin/unrelated-job",
	}, "\n")
	snapshot := parseCronJobsOutput([]byte(input))
	if snapshot.Availability != "available" || snapshot.Scope != "current_user_crontab" || len(snapshot.UnobservedScopes) != 2 || snapshot.UnobservedScopes[0] != "other_users" || snapshot.UnobservedScopes[1] != "system" || len(snapshot.Records) != 4 {
		t.Fatalf("snapshot status/scope/record count = %q/%q/%d", snapshot.Availability, snapshot.Scope, len(snapshot.Records))
	}
	if snapshot.Records[0].Schedule != "*/5 1-4 * JAN,MAR 1-5" || snapshot.Records[0].Type != "execution_mechanism" || snapshot.Records[0].Title != "監視・起動基盤" {
		t.Fatalf("supervisor projection = %+v", snapshot.Records[0])
	}
	if snapshot.Records[1].Schedule != "@hourly" || snapshot.Records[1].Type != "job" || snapshot.Records[1].Title != "ログ整理" {
		t.Fatalf("maintenance projection = %+v", snapshot.Records[1])
	}
	if snapshot.Records[2].Schedule != "17 2 * * *" || snapshot.Records[2].Type != "unknown" || snapshot.Records[2].Title != "未分類の定期登録: future-rencrow-job" {
		t.Fatalf("unknown projection = %+v", snapshot.Records[2])
	}
	if snapshot.Records[3].Schedule != "18 3 * * *" || snapshot.Records[3].Title != "未分類の定期登録" {
		t.Fatalf("shell command did not use generic safe title = %+v", snapshot.Records[3])
	}
	for index, record := range snapshot.Records {
		if record.ID == "" || record.RuntimeState != "unknown" || record.OwnerState != "unknown" || record.NextRun != "unknown" {
			t.Errorf("record %d contains missing/assumed state: %+v", index, record)
		}
	}
	for _, forbidden := range []string{"/private", "secret", "token", "password", "unrelated-job", "PATH="} {
		if strings.Contains(fmt.Sprint(snapshot), forbidden) {
			t.Errorf("projection leaked %q", forbidden)
		}
	}
	if got := parseCronJobsOutput([]byte(input)); len(got.Records) != len(snapshot.Records) || got.Records[0].ID != snapshot.Records[0].ID || got.Records[1].ID != snapshot.Records[1].ID {
		t.Fatalf("record IDs are not stable: first=%+v second=%+v", snapshot.Records, got.Records)
	}
}

func TestParseCronJobsRetainsUnknownScheduleForRenCrowRegistration(t *testing.T) {
	snapshot := parseCronJobsOutput([]byte("invalid-schedule /private/rencrow/future --secret=hidden\n"))
	if snapshot.Availability != "partial" || len(snapshot.Records) != 1 || snapshot.Records[0].Schedule != "unknown" || snapshot.Records[0].Type != "unknown" {
		t.Fatalf("unparseable RenCrow entry was dropped or promoted: %+v", snapshot)
	}
	if strings.Contains(fmt.Sprint(snapshot), "private") || strings.Contains(fmt.Sprint(snapshot), "hidden") {
		t.Fatalf("unparseable registration leaked its command: %+v", snapshot)
	}
}

func TestCollectCronJobsDistinguishesEmptyUnavailableAndFailure(t *testing.T) {
	for _, test := range []struct {
		name           string
		stdout, stderr string
		err            error
		want           string
	}{
		{name: "empty", want: "empty"},
		{name: "no crontab", stderr: "crontab: no crontab for sample-user\n", err: errors.New("exit status 1"), want: "empty"},
		{name: "private failure", stderr: "private database detail", err: errors.New("exit status 1"), want: "unavailable"},
		{name: "timeout", err: errCronJobsTimeout, want: "timeout"},
		{name: "too large", err: errCronJobsOutputTooLarge, want: "too_large"},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := collectCronJobs(context.Background(), func(context.Context) ([]byte, []byte, error) {
				return []byte(test.stdout), []byte(test.stderr), test.err
			})
			if snapshot.Availability != test.want || len(snapshot.Records) != 0 {
				t.Fatalf("status/records=%q/%d, want %q/0", snapshot.Availability, len(snapshot.Records), test.want)
			}
			if strings.Contains(fmt.Sprint(snapshot), "private") || strings.Contains(fmt.Sprint(snapshot), "sample-user") {
				t.Fatalf("private command error leaked: %+v", snapshot)
			}
		})
	}
}

func TestCollectCronJobsDoesNotRunWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	snapshot := collectCronJobs(ctx, func(context.Context) ([]byte, []byte, error) {
		called = true
		return nil, nil, nil
	})
	if called || snapshot.Availability != "timeout" {
		t.Fatalf("canceled context invoked crontab or lost timeout state: called=%v snapshot=%+v", called, snapshot)
	}
}

func TestDarwinCronJobsReaderInvokesReadOnlyCurrentUserListCommand(t *testing.T) {
	called := false
	reader := darwinCronJobsReader{execute: func(ctx context.Context, executable string, args ...string) ([]byte, []byte, error) {
		called = true
		if ctx == nil || executable != cronJobsCommandPath || len(args) != 1 || args[0] != "-l" {
			t.Fatalf("crontab invocation = executable %q args %q ctx %v", executable, args, ctx)
		}
		return []byte("5 * * * * /usr/local/bin/log-rotate-guard\n"), nil, nil
	}}
	snapshot := reader.ReadCronJobs(context.Background())
	if !called || snapshot.Scope != "current_user_crontab" || len(snapshot.Records) != 1 || snapshot.Records[0].Title != "ログ整理" {
		t.Fatalf("read-only crontab projection = called %v snapshot %+v", called, snapshot)
	}
}

func TestBoundedCronJobsOutputRetainsOnlyConfiguredMaximum(t *testing.T) {
	output := &boundedCronJobsOutput{limit: 4}
	if written, err := output.Write([]byte("abcd-private")); err != nil || written != len("abcd-private") {
		t.Fatalf("bounded writer result = %d, %v", written, err)
	}
	data, overflow := output.snapshot()
	if !overflow || string(data) != "abcd" || len(data) != output.limit {
		t.Fatalf("bounded writer retained %q overflow=%v", data, overflow)
	}
}
