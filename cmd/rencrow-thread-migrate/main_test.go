package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/threadmigration"
)

type testReceipt struct {
	Schema         string `json:"schema"`
	Status         string `json:"status"`
	RedisCount     int    `json:"redis_count"`
	QdrantCount    int    `json:"qdrant_count"`
	RedisSHA256    string `json:"redis_sha256"`
	QdrantSHA256   string `json:"qdrant_sha256"`
	SnapshotSHA256 string `json:"snapshot_sha256"`
	ErrorCode      string `json:"error_code"`
}

func TestRunVerifyExternalSuccess(t *testing.T) {
	inputPath := writeTestExternalSnapshot(t)
	var stdout bytes.Buffer
	code := runWithOperations([]string{"verify-external", "--input", inputPath}, &stdout, externalOperations{
		verify: verifyExternalSnapshot,
	})
	if code != 0 {
		t.Fatalf("runWithOperations() code = %d, want 0; output = %q", code, stdout.String())
	}
	receipt := decodeSingleReceipt(t, stdout.String())
	if receipt.Schema != "rencrow.threadmigration.verify_external.v1" {
		t.Errorf("receipt schema = %q", receipt.Schema)
	}
	if receipt.Status != "verified" {
		t.Errorf("receipt status = %q", receipt.Status)
	}
	if receipt.RedisCount != 0 || receipt.QdrantCount != 0 {
		t.Errorf("receipt counts = %d/%d, want 0/0", receipt.RedisCount, receipt.QdrantCount)
	}
	if receipt.RedisSHA256 == "" || receipt.QdrantSHA256 == "" || receipt.SnapshotSHA256 == "" {
		t.Errorf("receipt hashes = %q/%q/%q, want non-empty", receipt.RedisSHA256, receipt.QdrantSHA256, receipt.SnapshotSHA256)
	}
	if receipt.ErrorCode != "" {
		t.Errorf("receipt error_code = %q, want empty", receipt.ErrorCode)
	}
}

func TestRunBuildRejectsArguments(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing", args: []string{"build"}},
		{name: "missing output", args: []string{"build", "--l1-source", "l1.sqlite", "--archive-source", "archive.sqlite", "--topic-source", "topics.jsonl", "--external-snapshot", "snapshot.json"}},
		{name: "blank source", args: []string{"build", "--l1-source", " ", "--archive-source", "archive.sqlite", "--topic-source", "topics.jsonl", "--external-snapshot", "snapshot.json", "--output-dir", "out"}},
		{name: "unknown option", args: []string{"build", "--l1-source", "l1.sqlite", "--archive-source", "archive.sqlite", "--topic-source", "topics.jsonl", "--external-snapshot", "snapshot.json", "--output-dir", "out", "--other", "x"}},
		{name: "extra argument", args: []string{"build", "--l1-source", "l1.sqlite", "--archive-source", "archive.sqlite", "--topic-source", "topics.jsonl", "--external-snapshot", "snapshot.json", "--output-dir", "out", "extra"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			called := false
			code := runWithOperations(test.args, &stdout, externalOperations{
				build: func(context.Context, threadmigration.BuildOptions) (threadmigration.BuildReceipt, error) {
					called = true
					return threadmigration.BuildReceipt{}, nil
				},
			})
			if code == 0 {
				t.Fatalf("runWithOperations() code = 0, want failure; output = %q", stdout.String())
			}
			if called {
				t.Fatal("runWithOperations() invoked build op for invalid arguments")
			}
			receipt := decodeSingleBuildReceipt(t, stdout.String())
			assertBlockedBuildReceipt(t, receipt, buildErrorInvalidArguments)
		})
	}
}

func TestRunBuildFailureDoesNotExposeDetails(t *testing.T) {
	rawError := errors.New("secret raw error at /private/l1.sqlite with content")
	var stdout bytes.Buffer
	code := runWithOperations([]string{
		"build", "--l1-source", "/private/l1.sqlite", "--archive-source", "/private/archive.sqlite",
		"--topic-source", "/private/topics.jsonl", "--external-snapshot", "/private/snapshot.json", "--output-dir", "/private/out",
	}, &stdout, externalOperations{
		build: func(context.Context, threadmigration.BuildOptions) (threadmigration.BuildReceipt, error) {
			return threadmigration.BuildReceipt{}, rawError
		},
	})
	if code == 0 {
		t.Fatalf("runWithOperations() code = 0, want failure; output = %q", stdout.String())
	}
	assertBlockedBuildReceipt(t, decodeSingleBuildReceipt(t, stdout.String()), buildErrorBuildFailed)
	if strings.Contains(stdout.String(), rawError.Error()) || strings.Contains(stdout.String(), "/private") {
		t.Fatalf("receipt exposed error or path: %q", stdout.String())
	}
}

func TestRunBuildUsesOfflineOperationDeadline(t *testing.T) {
	var stdout bytes.Buffer
	var gotContext context.Context
	code := runWithOperations([]string{
		"build", "--l1-source", "l1.sqlite", "--archive-source", "archive.sqlite", "--topic-source", "topics.jsonl",
		"--external-snapshot", "snapshot.json", "--output-dir", "out",
	}, &stdout, externalOperations{
		build: func(ctx context.Context, _ threadmigration.BuildOptions) (threadmigration.BuildReceipt, error) {
			gotContext = ctx
			return threadmigration.BuildReceipt{}, errors.New("build unavailable")
		},
	})
	if code == 0 {
		t.Fatalf("runWithOperations() code = 0, want failure; output = %q", stdout.String())
	}
	if gotContext == nil {
		t.Fatal("runWithOperations() did not pass a context")
	}
	deadline, ok := gotContext.Deadline()
	if !ok {
		t.Fatal("runWithOperations() context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining < 29*time.Minute || remaining > 30*time.Minute {
		t.Fatalf("runWithOperations() build context deadline remaining = %s, want approximately 30m", remaining)
	}
	if gotContext.Err() != context.Canceled {
		t.Fatalf("runWithOperations() build context error after return = %v, want context.Canceled", gotContext.Err())
	}
	assertBlockedBuildReceipt(t, decodeSingleBuildReceipt(t, stdout.String()), buildErrorBuildFailed)
}

func TestRunBuildRejectsUnverifiedReadyReceipt(t *testing.T) {
	var stdout bytes.Buffer
	code := runWithOperations([]string{
		"build", "--l1-source", "l1.sqlite", "--archive-source", "archive.sqlite", "--topic-source", "topics.jsonl",
		"--external-snapshot", "snapshot.json", "--output-dir", "out",
	}, &stdout, externalOperations{
		build: func(context.Context, threadmigration.BuildOptions) (threadmigration.BuildReceipt, error) {
			return threadmigration.BuildReceipt{SchemaVersion: threadmigration.OfflineBuildSchemaVersion, Status: threadmigration.OfflineBuildStatusReady}, nil
		},
	})
	if code == 0 {
		t.Fatalf("runWithOperations() code = 0, want failure; output = %q", stdout.String())
	}
	assertBlockedBuildReceipt(t, decodeSingleBuildReceipt(t, stdout.String()), buildErrorBuildFailed)
}

func TestRunVerifyExternalRejectsArguments(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing input", args: []string{"verify-external"}},
		{name: "blank input", args: []string{"verify-external", "--input", " "}},
		{name: "unknown option", args: []string{"verify-external", "--input", "snapshot.json", "--other", "x"}},
		{name: "extra argument", args: []string{"verify-external", "--input", "snapshot.json", "extra"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			called := false
			code := runWithOperations(test.args, &stdout, externalOperations{
				verify: func(context.Context, string) (threadmigration.ExternalSnapshot, error) {
					called = true
					return threadmigration.ExternalSnapshot{}, nil
				},
			})
			if code == 0 {
				t.Fatalf("runWithOperations() code = 0, want failure; output = %q", stdout.String())
			}
			if called {
				t.Fatal("runWithOperations() invoked verify op for invalid arguments")
			}
			assertBlockedVerifyReceipt(t, decodeSingleReceipt(t, stdout.String()), "invalid_arguments")
		})
	}
}

func TestRunVerifyExternalFailureDoesNotExposeDetails(t *testing.T) {
	rawError := errors.New("secret raw error at /private/snapshot with content")
	var stdout bytes.Buffer
	code := runWithOperations([]string{"verify-external", "--input", "/private/snapshot"}, &stdout, externalOperations{
		verify: func(context.Context, string) (threadmigration.ExternalSnapshot, error) {
			return threadmigration.ExternalSnapshot{}, rawError
		},
	})
	if code == 0 {
		t.Fatalf("runWithOperations() code = 0, want failure; output = %q", stdout.String())
	}
	assertBlockedVerifyReceipt(t, decodeSingleReceipt(t, stdout.String()), "verify_failed")
	if strings.Contains(stdout.String(), rawError.Error()) || strings.Contains(stdout.String(), "/private") {
		t.Fatalf("receipt exposed error or path: %q", stdout.String())
	}
}

func TestRunVerifyExternalRejectsInvalidSnapshot(t *testing.T) {
	var stdout bytes.Buffer
	code := runWithOperations([]string{"verify-external", "--input", "snapshot.json"}, &stdout, externalOperations{
		verify: func(context.Context, string) (threadmigration.ExternalSnapshot, error) {
			return threadmigration.ExternalSnapshot{}, nil
		},
	})
	if code == 0 {
		t.Fatalf("runWithOperations() code = 0, want failure; output = %q", stdout.String())
	}
	assertBlockedVerifyReceipt(t, decodeSingleReceipt(t, stdout.String()), "verify_failed")
}

func TestVerifyExternalRejectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := verifyExternalSnapshot(ctx, "/private/snapshot"); err == nil {
		t.Fatal("canceled context was accepted")
	}
}

func TestRunVerifyExternalOutputFailure(t *testing.T) {
	snapshot, err := threadmigration.NewExternalSnapshot(nil, nil)
	if err != nil {
		t.Fatalf("NewExternalSnapshot() error = %v", err)
	}
	code := runWithOperations([]string{"verify-external", "--input", "snapshot.json"}, failingWriter{}, externalOperations{
		verify: func(context.Context, string) (threadmigration.ExternalSnapshot, error) {
			return snapshot, nil
		},
	})
	if code == 0 {
		t.Fatal("runWithOperations() code = 0, want output failure")
	}
}

func TestRunVerifyExternalNilOperationFailsClosed(t *testing.T) {
	var stdout bytes.Buffer
	code := runWithOperations([]string{"verify-external", "--input", "snapshot.json"}, &stdout, externalOperations{})
	if code == 0 {
		t.Fatalf("runWithOperations() code = 0, want failure; output = %q", stdout.String())
	}
	assertBlockedVerifyReceipt(t, decodeSingleReceipt(t, stdout.String()), "verify_failed")
}

func TestRunWithOperationsNilWriterFailsClosed(t *testing.T) {
	if code := runWithOperations([]string{"verify-external", "--input", "snapshot.json"}, nil, externalOperations{}); code == 0 {
		t.Fatal("runWithOperations() accepted a nil writer")
	}
}

func writeTestExternalSnapshot(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}
	root := filepath.Join(workingDirectory, "..", "..", "Tmp", "test-runtime", "identity-step05-sol", "verify")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	directory, err := os.MkdirTemp(root, "case-")
	if err != nil {
		t.Fatalf("os.MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "snapshot.json")
	snapshot, err := threadmigration.NewExternalSnapshot(nil, nil)
	if err != nil {
		t.Fatalf("NewExternalSnapshot() error = %v", err)
	}
	if err := threadmigration.WriteExternalSnapshotFresh(path, snapshot); err != nil {
		t.Fatalf("WriteExternalSnapshotFresh() error = %v", err)
	}
	return path
}

func TestRunCaptureExternalSuccess(t *testing.T) {
	snapshot, err := threadmigration.NewExternalSnapshot(nil, nil)
	if err != nil {
		t.Fatalf("NewExternalSnapshot() error = %v", err)
	}

	var stdout bytes.Buffer
	var gotConfig, gotOutput string
	var gotContext context.Context
	code := runWithOperations([]string{"capture-external", "--output", "snapshot.json", "--config", "config.yaml"}, &stdout, externalOperations{
		capture: func(ctx context.Context, config, output string) (threadmigration.ExternalSnapshot, error) {
			gotContext = ctx
			gotConfig = config
			gotOutput = output
			return snapshot, nil
		},
	})

	if code != 0 {
		t.Fatalf("runWithOperations() code = %d, want 0; output = %q", code, stdout.String())
	}
	if gotConfig != "config.yaml" || gotOutput != "snapshot.json" {
		t.Fatalf("runWithOperations() op paths = %q, %q", gotConfig, gotOutput)
	}
	if gotContext == nil {
		t.Fatal("runWithOperations() did not pass a context")
	}
	deadline, ok := gotContext.Deadline()
	if !ok {
		t.Fatal("runWithOperations() context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining < 4*time.Minute || remaining > 5*time.Minute {
		t.Fatalf("runWithOperations() context deadline remaining = %s, want approximately 5m", remaining)
	}
	if gotContext.Err() != context.Canceled {
		t.Fatalf("runWithOperations() context error after return = %v, want context.Canceled", gotContext.Err())
	}

	receipt := decodeSingleReceipt(t, stdout.String())
	if receipt.Schema != "rencrow.threadmigration.capture_external.v1" {
		t.Errorf("receipt schema = %q", receipt.Schema)
	}
	if receipt.Status != "captured_not_quiescence_bound" {
		t.Errorf("receipt status = %q", receipt.Status)
	}
	if receipt.RedisCount != len(snapshot.Redis) || receipt.QdrantCount != len(snapshot.Qdrant) {
		t.Errorf("receipt counts = %d/%d, want %d/%d", receipt.RedisCount, receipt.QdrantCount, len(snapshot.Redis), len(snapshot.Qdrant))
	}
	if receipt.RedisSHA256 != snapshot.RedisSHA256 || receipt.QdrantSHA256 != snapshot.QdrantSHA256 || receipt.SnapshotSHA256 != snapshot.SnapshotSHA256 {
		t.Errorf("receipt hashes do not match snapshot: %#v", receipt)
	}
	if receipt.ErrorCode != "" {
		t.Errorf("receipt error_code = %q, want empty", receipt.ErrorCode)
	}
}

func TestRunCaptureExternalRejectsArguments(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing", args: nil},
		{name: "unknown command", args: []string{"other"}},
		{name: "missing config value", args: []string{"capture-external", "--config", "--output", "out"}},
		{name: "blank config", args: []string{"capture-external", "--config", " ", "--output", "out"}},
		{name: "missing output value", args: []string{"capture-external", "--config", "config", "--output"}},
		{name: "blank output", args: []string{"capture-external", "--config", "config", "--output", "\t"}},
		{name: "unknown option", args: []string{"capture-external", "--config", "config", "--other", "out"}},
		{name: "extra argument", args: []string{"capture-external", "--config", "config", "--output", "out", "extra"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			called := false
			code := runWithOperations(test.args, &stdout, externalOperations{capture: func(context.Context, string, string) (threadmigration.ExternalSnapshot, error) {
				called = true
				return threadmigration.ExternalSnapshot{}, nil
			}})
			if code == 0 {
				t.Fatalf("runWithOperations() code = 0, want failure; output = %q", stdout.String())
			}
			if called {
				t.Fatal("runWithOperations() invoked capture op for invalid arguments")
			}
			receipt := decodeSingleReceipt(t, stdout.String())
			assertBlockedReceipt(t, receipt, "invalid_arguments")
		})
	}
}

func TestRunCaptureExternalFailureDoesNotExposeDetails(t *testing.T) {
	rawError := errors.New("secret raw error at /private/config with content")
	var stdout bytes.Buffer
	code := runWithOperations([]string{"capture-external", "--config", "/private/config", "--output", "/private/out"}, &stdout, externalOperations{capture: func(context.Context, string, string) (threadmigration.ExternalSnapshot, error) {
		return threadmigration.ExternalSnapshot{}, rawError
	}})

	if code == 0 {
		t.Fatalf("runWithOperations() code = 0, want failure; output = %q", stdout.String())
	}
	receipt := decodeSingleReceipt(t, stdout.String())
	assertBlockedReceipt(t, receipt, "capture_failed")
	if strings.Contains(stdout.String(), rawError.Error()) || strings.Contains(stdout.String(), "/private") {
		t.Fatalf("receipt exposed error or path: %q", stdout.String())
	}
}

func TestRunCaptureExternalRejectsInvalidSnapshot(t *testing.T) {
	var stdout bytes.Buffer
	code := runWithOperations([]string{"capture-external", "--config", "config", "--output", "out"}, &stdout, externalOperations{capture: func(context.Context, string, string) (threadmigration.ExternalSnapshot, error) {
		return threadmigration.ExternalSnapshot{}, nil
	}})

	if code == 0 {
		t.Fatalf("runWithOperations() code = %d, want failure; output = %q", code, stdout.String())
	}
	receipt := decodeSingleReceipt(t, stdout.String())
	assertBlockedReceipt(t, receipt, "capture_failed")
}

func TestRunCaptureExternalOutputFailure(t *testing.T) {
	code := runWithOperations([]string{"capture-external", "--config", "config", "--output", "out"}, failingWriter{}, externalOperations{capture: func(context.Context, string, string) (threadmigration.ExternalSnapshot, error) {
		return threadmigration.ExternalSnapshot{}, errors.New("capture unavailable")
	}})
	if code == 0 {
		t.Fatal("runWithOperations() code = 0, want output failure")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("raw writer failure")
}

func decodeSingleReceipt(t *testing.T, output string) testReceipt {
	t.Helper()
	if !strings.HasSuffix(output, "\n") {
		t.Fatalf("output = %q, want one newline-terminated JSON line", output)
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("output = %q, want exactly one JSON line", output)
	}
	var receipt testReceipt
	if err := json.Unmarshal([]byte(lines[0]), &receipt); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; output = %q", err, output)
	}
	return receipt
}

func decodeSingleBuildReceipt(t *testing.T, output string) threadmigration.BuildReceipt {
	t.Helper()
	if !strings.HasSuffix(output, "\n") {
		t.Fatalf("output = %q, want one newline-terminated JSON line", output)
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("output = %q, want exactly one JSON line", output)
	}
	var receipt threadmigration.BuildReceipt
	if err := json.Unmarshal([]byte(lines[0]), &receipt); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; output = %q", err, output)
	}
	return receipt
}

func assertBlockedBuildReceipt(t *testing.T, receipt threadmigration.BuildReceipt, errorCode string) {
	t.Helper()
	if receipt.SchemaVersion != threadmigration.OfflineBuildSchemaVersion {
		t.Errorf("receipt schema = %q", receipt.SchemaVersion)
	}
	if receipt.Status != threadmigration.OfflineBuildStatusBlocked {
		t.Errorf("receipt status = %q", receipt.Status)
	}
	if receipt.ErrorCode != errorCode {
		t.Errorf("receipt error_code = %q, want %q", receipt.ErrorCode, errorCode)
	}
	if err := receipt.Validate(); err != nil {
		t.Errorf("blocked receipt.Validate() error = %v", err)
	}
}

func assertBlockedReceipt(t *testing.T, receipt testReceipt, errorCode string) {
	t.Helper()
	if receipt.Schema != "rencrow.threadmigration.capture_external.v1" {
		t.Errorf("receipt schema = %q", receipt.Schema)
	}
	if receipt.Status != "blocked" {
		t.Errorf("receipt status = %q", receipt.Status)
	}
	if receipt.RedisCount != 0 || receipt.QdrantCount != 0 {
		t.Errorf("receipt counts = %d/%d, want 0/0", receipt.RedisCount, receipt.QdrantCount)
	}
	if receipt.RedisSHA256 != "" || receipt.QdrantSHA256 != "" || receipt.SnapshotSHA256 != "" {
		t.Errorf("receipt hashes = %q/%q/%q, want empty", receipt.RedisSHA256, receipt.QdrantSHA256, receipt.SnapshotSHA256)
	}
	if receipt.ErrorCode != errorCode {
		t.Errorf("receipt error_code = %q, want %q", receipt.ErrorCode, errorCode)
	}
}

func assertBlockedVerifyReceipt(t *testing.T, receipt testReceipt, errorCode string) {
	t.Helper()
	if receipt.Schema != "rencrow.threadmigration.verify_external.v1" {
		t.Errorf("receipt schema = %q", receipt.Schema)
	}
	if receipt.Status != "blocked" {
		t.Errorf("receipt status = %q", receipt.Status)
	}
	if receipt.RedisCount != 0 || receipt.QdrantCount != 0 {
		t.Errorf("receipt counts = %d/%d, want 0/0", receipt.RedisCount, receipt.QdrantCount)
	}
	if receipt.RedisSHA256 != "" || receipt.QdrantSHA256 != "" || receipt.SnapshotSHA256 != "" {
		t.Errorf("receipt hashes = %q/%q/%q, want empty", receipt.RedisSHA256, receipt.QdrantSHA256, receipt.SnapshotSHA256)
	}
	if receipt.ErrorCode != errorCode {
		t.Errorf("receipt error_code = %q, want %q", receipt.ErrorCode, errorCode)
	}
}
