package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func TestGmailCollectorMissingExecutableAndBoundedOutput(t *testing.T) {
	collector := gmailCLICollector{config: config.GmailHeartbeatConfig{Command: "rencrow-gmail-definitely-missing", MaxResults: 20}}
	if _, err := collector.Collect(context.Background(), ""); err == nil || strings.Contains(err.Error(), "credentials") {
		t.Fatalf("unsafe unavailable error: %v", err)
	}
	output := &boundedGmailOutput{limit: 3}
	if _, err := output.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte("private-mail")); err == nil || output.buffer.String() != "abc" {
		t.Fatal("oversized private output retained")
	}
}

func TestGmailCollectorOverflowCancelsChild(t *testing.T) {
	if os.Getenv("RENCROW_GMAIL_OVERFLOW_TEST_CHILD") == "1" {
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", 1024))
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGmailCollectorOverflowCancelsChild$")
	cmd.Env = append(os.Environ(), "RENCROW_GMAIL_OVERFLOW_TEST_CHILD=1")
	output := &boundedGmailOutput{limit: 8, cancel: cancel}
	cmd.Stdout = output
	started := time.Now()
	err := cmd.Run()
	if err == nil || !output.exceeded || output.buffer.Len() != 0 || time.Since(started) >= 4*time.Second {
		t.Fatalf("overflow did not promptly terminate child: err=%v exceeded=%t elapsed=%s", err, output.exceeded, time.Since(started))
	}
}
