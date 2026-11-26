//go:build !darwin

package main

import (
	"context"
	"testing"
)

func TestSystemJobsOtherReaderReportsUnsupported(t *testing.T) {
	snapshot, err := unsupportedSystemJobsReader{}.ReadSystemJobs(context.Background())
	if err != nil || snapshot.Availability != "unsupported" || len(snapshot.Records) != 0 || len(snapshot.Sources) != 1 ||
		snapshot.Sources[0].State != "unsupported" || !containsSystemJobsIssue(snapshot.Sources[0].Issues, "platform_unsupported") {
		t.Fatalf("non-Darwin system jobs result = %+v, err=%v", snapshot, err)
	}
}
