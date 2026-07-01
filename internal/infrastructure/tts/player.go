package tts

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// CommandSpec defines one playback command.
type CommandSpec struct {
	Name string
	Args []string
}

// PlaybackResult stores one playback execution result.
type PlaybackResult struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
}

// Player plays generated audio and verifies actual playback command success.
type Player interface {
	Play(ctx context.Context, audioPath string) (PlaybackResult, error)
}

// CommandPlayer executes configured playback commands.
type CommandPlayer struct {
	commands []CommandSpec
}

func NewCommandPlayer(commands []CommandSpec) *CommandPlayer {
	return &CommandPlayer{commands: append([]CommandSpec{}, commands...)}
}

func (p *CommandPlayer) Play(ctx context.Context, audioPath string) (PlaybackResult, error) {
	if len(p.commands) == 0 {
		return PlaybackResult{}, ErrCommandNotFound
	}

	var lastErr error
	last := PlaybackResult{}

	for _, spec := range p.commands {
		args := make([]string, 0, len(spec.Args))
		for _, a := range spec.Args {
			args = append(args, strings.ReplaceAll(a, "{audio}", audioPath))
		}
		cmd := exec.CommandContext(ctx, spec.Name, args...)
		err := cmd.Run()
		exitCode := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				exitCode = ee.ExitCode()
			} else {
				exitCode = -1
			}
		}
		last = PlaybackResult{
			Command:  strings.TrimSpace(spec.Name + " " + strings.Join(args, " ")),
			ExitCode: exitCode,
		}
		if err == nil {
			return last, nil
		}
		lastErr = err
	}

	return last, fmt.Errorf("%w: %w", ErrPlaybackFailed, lastErr)
}
