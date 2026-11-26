//go:build darwin

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	cronJobsCommandPath        = "/usr/bin/crontab"
	cronJobsCommandTimeout     = 2 * time.Second
	cronJobsCommandOutputLimit = 128 * 1024
	cronJobsMaxScheduleBytes   = 128
	cronJobsMaxExecutableBytes = 256
	cronJobsMaxExecutableName  = 48
)

var (
	errCronJobsTimeout            = errors.New("crontab inspection timed out")
	errCronJobsOutputTooLarge     = errors.New("crontab output exceeded its inspection bound")
	cronJobsFiveFieldPattern      = regexp.MustCompile(`^[A-Za-z0-9*/,-]+$`)
	cronJobsEnvPattern            = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	cronJobsExecutablePattern     = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
	cronJobsExecutableNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
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

type cronJobsCommandRunner func(context.Context) ([]byte, []byte, error)

func parseCronJobsOutput(data []byte) cronJobsSnapshot {
	return parseCronJobsOutputAt(data, time.Now().UTC())
}

func parseCronJobsOutputAt(data []byte, observedAt time.Time) cronJobsSnapshot {
	snapshot := cronJobsSnapshot{
		ObservedAt: observedAt.UTC(), Availability: "available", Scope: "current_user_crontab",
		UnobservedScopes: []string{"other_users", "system"},
		SourceState:      "available", Issues: []string{}, Records: []cronJobsRecord{},
	}
	if len(data) == 0 {
		snapshot.Availability, snapshot.SourceState = "empty", "empty"
		return snapshot
	}
	seenIDs := make(map[string]int)
	for _, rawLine := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || cronJobsEnvPattern.MatchString(line) {
			continue
		}
		schedule, command, ok := splitCronJobsLine(line)
		if !ok {
			if isRenCrowCronRegistration(line) {
				record := makeCronJobsRecord(line, "unknown", "unknown")
				record.Title = "未分類の定期登録"
				seenIDs[record.ID]++
				if seenIDs[record.ID] > 1 {
					record.ID += "-" + strconv.Itoa(seenIDs[record.ID])
				}
				snapshot.Records = append(snapshot.Records, record)
				snapshot.Availability = "partial"
				snapshot.SourceState = "partial"
				addCronJobsIssue(&snapshot, "schedule_unrecognized")
			}
			continue
		}
		if !isRenCrowCronRegistration(command) {
			continue
		}
		recordType, title := classifyCronJobsRegistration(command)
		record := makeCronJobsRecord(line, schedule, recordType)
		record.Title = title
		seenIDs[record.ID]++
		if seenIDs[record.ID] > 1 {
			record.ID += "-" + strconv.Itoa(seenIDs[record.ID])
		}
		snapshot.Records = append(snapshot.Records, record)
	}
	return snapshot
}

func collectCronJobs(ctx context.Context, run cronJobsCommandRunner) cronJobsSnapshot {
	now := time.Now().UTC()
	if context.Cause(ctx) != nil {
		return cronJobsUnavailable(now, "timeout", "read_timeout")
	}
	if run == nil {
		return cronJobsUnavailable(now, "unavailable", "reader_unavailable")
	}
	stdout, stderr, err := run(ctx)
	if errors.Is(err, errCronJobsTimeout) || errors.Is(context.Cause(ctx), context.DeadlineExceeded) || errors.Is(context.Cause(ctx), context.Canceled) {
		return cronJobsUnavailable(now, "timeout", "read_timeout")
	}
	if errors.Is(err, errCronJobsOutputTooLarge) {
		return cronJobsUnavailable(now, "too_large", "output_limit_reached")
	}
	if err != nil {
		if isCronJobsNoCrontab(stderr) {
			return cronJobsUnavailable(now, "empty", "no_crontab")
		}
		return cronJobsUnavailable(now, "unavailable", "read_failed")
	}
	snapshot := parseCronJobsOutputAt(stdout, now)
	if snapshot.SourceState == "empty" {
		addCronJobsIssue(&snapshot, "no_entries")
	}
	return snapshot
}

func cronJobsUnavailable(observedAt time.Time, state, issue string) cronJobsSnapshot {
	return cronJobsSnapshot{
		ObservedAt: observedAt.UTC(), Availability: state, Scope: "current_user_crontab",
		UnobservedScopes: []string{"other_users", "system"},
		SourceState:      state, Issues: []string{issue}, Records: []cronJobsRecord{},
	}
}

func addCronJobsIssue(snapshot *cronJobsSnapshot, issue string) {
	for _, existing := range snapshot.Issues {
		if existing == issue {
			return
		}
	}
	snapshot.Issues = append(snapshot.Issues, issue)
}

func isCronJobsNoCrontab(stderr []byte) bool {
	message := strings.TrimSpace(string(stderr))
	return strings.HasPrefix(message, "crontab: no crontab for ") && !strings.Contains(message, "\n")
}

func splitCronJobsLine(line string) (string, string, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", "", false
	}
	if strings.HasPrefix(fields[0], "@") {
		schedule := strings.ToLower(fields[0])
		switch schedule {
		case "@yearly", "@annually", "@monthly", "@weekly", "@daily", "@midnight", "@hourly", "@reboot":
			if len(schedule) > cronJobsMaxScheduleBytes {
				return "", "", false
			}
			return schedule, strings.Join(fields[1:], " "), true
		default:
			return "", "", false
		}
	}
	if len(fields) < 6 {
		return "", "", false
	}
	for index, field := range fields[:5] {
		if !validCronJobsField(field, index) {
			return "", "", false
		}
	}
	schedule := strings.Join(fields[:5], " ")
	if len(schedule) > cronJobsMaxScheduleBytes {
		return "", "", false
	}
	return schedule, strings.Join(fields[5:], " "), true
}

func validCronJobsField(field string, index int) bool {
	if !cronJobsFiveFieldPattern.MatchString(field) {
		return false
	}
	minimum, maximum := 0, 59
	if index == 1 {
		maximum = 23
	} else if index == 2 {
		minimum, maximum = 1, 31
	} else if index == 3 {
		minimum, maximum = 1, 12
	} else if index == 4 {
		maximum = 7
	}
	for _, segment := range strings.Split(field, ",") {
		if segment == "" {
			return false
		}
		base, step, hasStep := strings.Cut(segment, "/")
		if hasStep && (step == "" || strings.Contains(step, "/") || !cronJobsNumericRange(step, 1, maximum)) {
			return false
		}
		if base == "*" {
			continue
		}
		start, end, ranged := strings.Cut(base, "-")
		if ranged && end == "" {
			return false
		}
		if !cronJobsFieldValue(start, index, minimum, maximum) || (ranged && !cronJobsFieldValue(end, index, minimum, maximum)) {
			return false
		}
	}
	return true
}

func cronJobsFieldValue(value string, index, minimum, maximum int) bool {
	if cronJobsNumericRange(value, minimum, maximum) {
		return true
	}
	if index == 3 {
		return cronJobsMonthNames[strings.ToLower(value)]
	}
	if index == 4 {
		return cronJobsWeekdayNames[strings.ToLower(value)]
	}
	return false
}

func cronJobsNumericRange(value string, minimum, maximum int) bool {
	if value == "" {
		return false
	}
	parsed, err := strconv.Atoi(value)
	return err == nil && parsed >= minimum && parsed <= maximum
}

var cronJobsMonthNames = map[string]bool{
	"jan": true, "feb": true, "mar": true, "apr": true, "may": true, "jun": true,
	"jul": true, "aug": true, "sep": true, "oct": true, "nov": true, "dec": true,
}

var cronJobsWeekdayNames = map[string]bool{
	"sun": true, "mon": true, "tue": true, "wed": true, "thu": true, "fri": true, "sat": true,
}

func isRenCrowCronRegistration(command string) bool {
	lower := strings.ToLower(command)
	return strings.Contains(lower, "rencrow") || strings.Contains(lower, "obs-supervisor") || strings.Contains(lower, "log-rotate-guard")
}

func classifyCronJobsRegistration(command string) (string, string) {
	lower := strings.ToLower(command)
	if strings.Contains(lower, "obs-supervisor") {
		return "execution_mechanism", "監視・起動基盤"
	}
	if strings.Contains(lower, "log-rotate-guard") {
		return "job", "ログ整理"
	}
	if executable := safeCronJobsExecutableName(command); executable != "" {
		return "unknown", "未分類の定期登録: " + executable
	}
	return "unknown", "未分類の定期登録"
}

func safeCronJobsExecutableName(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 || len(fields[0]) > cronJobsMaxExecutableBytes || !cronJobsExecutablePattern.MatchString(fields[0]) {
		return ""
	}
	name := filepath.Base(fields[0])
	if len(name) == 0 || len(name) > cronJobsMaxExecutableName || !cronJobsExecutableNamePattern.MatchString(name) {
		return ""
	}
	switch strings.ToLower(name) {
	case "sh", "bash", "zsh", "fish", "dash", "ksh":
		return ""
	}
	return name
}

func makeCronJobsRecord(line, schedule, recordType string) cronJobsRecord {
	return cronJobsRecord{
		ID:    "cron:" + fmt.Sprintf("%x", sha256.Sum256([]byte(line))),
		Title: "未分類の定期登録", Type: recordType, Schedule: schedule,
		RuntimeState: "unknown", OwnerState: "unknown", NextRun: "unknown",
	}
}

type darwinCronJobsReader struct {
	execute func(context.Context, string, ...string) ([]byte, []byte, error)
}

func newCronJobsReader() darwinCronJobsReader { return darwinCronJobsReader{} }

func (reader darwinCronJobsReader) ReadCronJobs(ctx context.Context) cronJobsSnapshot {
	bounded, cancel := context.WithTimeout(ctx, cronJobsCommandTimeout)
	defer cancel()
	execute := reader.execute
	if execute == nil {
		execute = runCronJobsCommand
	}
	return collectCronJobs(bounded, func(ctx context.Context) ([]byte, []byte, error) {
		return execute(ctx, cronJobsCommandPath, "-l")
	})
}

func runCronJobsCommand(ctx context.Context, executable string, args ...string) ([]byte, []byte, error) {
	commandContext, cancel := context.WithTimeout(ctx, cronJobsCommandTimeout)
	defer cancel()
	command := exec.CommandContext(commandContext, executable, args...)
	command.WaitDelay = time.Second
	stdout := &boundedCronJobsOutput{limit: cronJobsCommandOutputLimit}
	stderr := &boundedCronJobsOutput{limit: cronJobsCommandOutputLimit}
	command.Stdout, command.Stderr = stdout, stderr
	err := command.Run()
	stdoutBytes, stdoutOverflow := stdout.snapshot()
	stderrBytes, stderrOverflow := stderr.snapshot()
	if stdoutOverflow || stderrOverflow {
		return nil, nil, errCronJobsOutputTooLarge
	}
	if err != nil {
		if context.Cause(commandContext) != nil {
			return nil, nil, errCronJobsTimeout
		}
		return stdoutBytes, stderrBytes, err
	}
	return stdoutBytes, stderrBytes, nil
}

type boundedCronJobsOutput struct {
	data     bytes.Buffer
	limit    int
	overflow bool
}

func (output *boundedCronJobsOutput) Write(data []byte) (int, error) {
	remaining := output.limit - output.data.Len()
	if remaining > len(data) {
		remaining = len(data)
	}
	if remaining > 0 {
		_, _ = output.data.Write(data[:remaining])
	}
	if remaining < len(data) {
		output.overflow = true
	}
	return len(data), nil
}

func (output *boundedCronJobsOutput) snapshot() ([]byte, bool) {
	return append([]byte(nil), output.data.Bytes()...), output.overflow
}
