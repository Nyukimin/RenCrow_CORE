package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRunRequiresFixedInputsAndReturnsBoundedReceipt(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	err := run([]string{"--snapshot-dir", "snapshot", "--output-dir", "output"}, &stdout, &stderr, func(source, output string) error {
		called = source == "snapshot" && output == "output"
		return nil
	})
	if err != nil || !called {
		t.Fatalf("run err=%v called=%v", err, called)
	}
	if got := stdout.String(); got != "{\"contract_version\":\"rencrow-core-migration-package/v1\",\"status\":\"completed\"}\n" {
		t.Fatalf("receipt=%q", got)
	}
}

func TestRunRejectsMissingExtraAndBuilderFailureWithoutSuccessReceipt(t *testing.T) {
	for _, args := range [][]string{{}, {"--snapshot-dir", "s"}, {"--snapshot-dir", "s", "--output-dir", "o", "extra"}} {
		var stdout, stderr bytes.Buffer
		if err := run(args, &stdout, &stderr, func(string, string) error { return nil }); err == nil || stdout.Len() != 0 {
			t.Fatalf("args=%v err=%v stdout=%q", args, err, stdout.String())
		}
	}
	var stdout, stderr bytes.Buffer
	secret := "must-not-leak"
	err := run([]string{"--snapshot-dir", "s", "--output-dir", "o"}, &stdout, &stderr, func(string, string) error { return errors.New(secret) })
	if err == nil || stdout.Len() != 0 || strings.Contains(stderr.String(), secret) {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}
