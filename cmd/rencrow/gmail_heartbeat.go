package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	gmailapp "github.com/Nyukimin/RenCrow_CORE/internal/application/gmailintake"
)

type gmailCLICollector struct{ config config.GmailHeartbeatConfig }

// boundedGmailOutput cancels the collector as soon as its output exceeds the
// bound, without retaining unbounded private mailbox data.
type boundedGmailOutput struct {
	// Do not embed bytes.Buffer: its promoted ReadFrom would let io.Copy
	// bypass this type's bounded Write when os/exec drains stdout.
	buffer   bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	exceeded bool
}

func (b *boundedGmailOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		b.exceeded = true
		if b.cancel != nil {
			b.cancel()
		}
		return 0, errors.New("Gmail collector output exceeds limit")
	}
	return b.buffer.Write(p)
}

func (c gmailCLICollector) Collect(ctx context.Context, pageToken string) (gmailapp.GmailPage, error) {
	var page gmailapp.GmailPage
	g := c.config
	args := []string{"collect", "--account", g.Account, "--credentials-file", g.CredentialsFile, "--token-file", g.TokenFile, "--query", gmailapp.GmailQuery, "--max-results", strconv.Itoa(g.MaxResults)}
	if pageToken != "" {
		args = append(args, "--page-token", pageToken)
	}
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, g.Command, args...)
	output := &boundedGmailOutput{limit: 8 * 1024 * 1024, cancel: cancel}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if output.exceeded {
			return page, errors.New("Gmail collector output exceeds limit")
		}
		if ctx.Err() != nil {
			return page, ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return page, fmt.Errorf("Gmail collector failed (exit %d)", exit.ExitCode())
		}
		return page, errors.New("Gmail collector unavailable")
	}
	decoder := json.NewDecoder(bytes.NewReader(output.buffer.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&page); err != nil {
		return page, errors.New("invalid Gmail collector envelope")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return page, errors.New("unexpected Gmail collector trailing output")
	}
	return page, nil
}
