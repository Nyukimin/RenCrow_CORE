package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The tests in this file hold `rencrow tasks` and `rencrow actions` to two
// rules that must hold before anything is opened:
//
//   - `--help` / `-h` anywhere on the command line prints the usage and exits 0.
//     Nothing is read (not even the config file) and nothing is written.
//   - `rencrow tasks` without a subcommand prints the usage and exits 2; it does
//     not panic.
//
// The entry tests run the real main() in a child process (the same way the
// gmail collector test does), with RENCROW_CONFIG pointing at a file that does
// not exist and a working directory that is empty: a command that reads the
// config fails, and a command that writes leaves something in the directory.

const cliEntryChildEnv = "RENCROW_CLI_ENTRY_TEST_ARGS"

// TestCLIEntryChild is the child process of runCLIEntry. It is skipped in a
// normal run.
func TestCLIEntryChild(t *testing.T) {
	raw := os.Getenv(cliEntryChildEnv)
	if raw == "" {
		t.Skip("child of the CLI entry tests")
	}
	os.Args = append([]string{"rencrow"}, strings.Split(raw, "\x1f")...)
	main()
	os.Exit(0) // main returned: the command succeeded (exit 0 without os.Exit)
}

type cliEntryResult struct {
	code           int
	stdout, stderr string
	leftovers      []string // names in the (initially empty) working directory
}

func runCLIEntry(t *testing.T, args ...string) cliEntryResult {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCLIEntryChild$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		cliEntryChildEnv+"="+strings.Join(args, "\x1f"),
		"RENCROW_CONFIG="+filepath.Join(dir, "no-such-dir", "config.yaml"),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := cliEntryResult{stdout: stdout.String(), stderr: stderr.String()}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		result.code = exit.ExitCode()
	default:
		t.Fatalf("run %v: %v", args, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		result.leftovers = append(result.leftovers, entry.Name())
	}
	return result
}

func requireHelpOutput(t *testing.T, args []string, result cliEntryResult, wantUsage string) {
	t.Helper()
	if result.code != 0 {
		t.Fatalf("%v: exit=%d stdout=%q stderr=%q, want exit 0", args, result.code, result.stdout, result.stderr)
	}
	if strings.TrimSpace(result.stderr) != "" {
		t.Fatalf("%v: stderr=%q, want nothing (the config must not be read)", args, result.stderr)
	}
	if !strings.Contains(result.stdout, wantUsage) {
		t.Fatalf("%v: stdout=%q, want it to contain %q", args, result.stdout, wantUsage)
	}
	if len(result.leftovers) != 0 {
		t.Fatalf("%v: left %v in the working directory", args, result.leftovers)
	}
}

func TestTasksWithoutASubcommandPrintsUsageAndExits2(t *testing.T) {
	for _, args := range [][]string{{"tasks"}, {"tasks", "--compact"}} {
		result := runCLIEntry(t, args...)
		if strings.Contains(result.stderr, "panic") || strings.Contains(result.stderr, "goroutine") {
			t.Fatalf("%v panicked:\n%s", args, result.stderr)
		}
		if result.code != 2 || strings.TrimSpace(result.stdout) != "" || !strings.Contains(result.stderr, "usage: rencrow tasks") {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q, want exit 2 with the usage on stderr only", args, result.code, result.stdout, result.stderr)
		}
		if strings.Contains(result.stderr, "failed to") {
			t.Fatalf("%v read the config or the store before it printed the usage: %q", args, result.stderr)
		}
	}
}

func TestTasksHelpAtTheEntryReadsAndWritesNothing(t *testing.T) {
	for _, args := range [][]string{
		{"tasks", "--help"},
		{"tasks", "-h"},
		{"tasks", "list", "--help"},
		{"tasks", "list", "-h"},
		{"tasks", "--help", "list"},
		{"tasks", "--compact", "show", "--help"},
		{"tasks", "create", "--title", "never created", "--help"},
		{"tasks", "cancel", "tsk_00000000-0000-7000-8000-000000000001", "-h"},
		{"tasks", "history", "--help"},
		{"tasks", "verify", "-h"},
		{"tasks", "rebuild-index", "--help"},
		{"tasks", "layout", "--help"},
		{"tasks", "layout", "show", "--help"},
		{"tasks", "layout", "concat", "--apply", "--help"},
		{"tasks", "reap-queued", "--help"},
		{"tasks", "reap-queued", "-h"},
		{"tasks", "reap-queued", "--created-before", "2026-01-02T03:04:05Z", "--apply", "--help"},
		{"tasks", "no-such-subcommand", "--help"},
	} {
		result := runCLIEntry(t, args...)
		requireHelpOutput(t, args, result, "usage: rencrow tasks")
	}
}

func TestActionsHelpAtTheEntryReadsAndWritesNothing(t *testing.T) {
	for _, args := range [][]string{
		{"actions", "--help"},
		{"actions", "-h"},
		{"actions", "verify", "--help"},
		{"actions", "verify", "--check-runs", "-h"},
		{"actions", "layout", "--help"},
		{"actions", "layout", "show", "--help"},
		{"actions", "layout", "show", "-h"},
		{"actions", "layout", "concat", "--apply", "--help"},
		{"actions", "no-such-subcommand", "-h"},
	} {
		result := runCLIEntry(t, args...)
		requireHelpOutput(t, args, result, "usage: rencrow actions")
	}
}

func TestTasksHelpNamesTheSubcommandThatWasAsked(t *testing.T) {
	for args, want := range map[string]string{
		"tasks list --help":                "usage: rencrow tasks list",
		"tasks show -h":                    "usage: rencrow tasks show <task_id>",
		"tasks reap-queued --help":         "usage: rencrow tasks reap-queued --created-before",
		"tasks layout concat --help":       "rencrow tasks layout concat [--apply]",
		"actions verify -h":                "usage: rencrow actions verify [--check-runs]",
		"actions layout show --help":       "rencrow actions layout show",
		"actions layout concat --apply -h": "rencrow actions layout concat [--apply]",
	} {
		fields := strings.Fields(args)
		result := runCLIEntry(t, fields...)
		requireHelpOutput(t, fields, result, want)
	}
}

func TestTasksCommandsWithoutHelpAreNotCaughtByTheHelpGuard(t *testing.T) {
	// A command line that does not ask for help still goes on to read the
	// config; here that fails because the file does not exist.
	for _, args := range [][]string{{"tasks", "list"}, {"actions", "verify"}} {
		result := runCLIEntry(t, args...)
		if result.code == 0 || !strings.Contains(result.stderr, "config") {
			t.Fatalf("%v: exit=%d stderr=%q, want a config failure", args, result.code, result.stderr)
		}
	}
}

func TestRunTasksCommandWithoutASubcommandReturnsUsageStatusInsteadOfPanicking(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"--compact"}, {"--COMPACT", "--compact"}} {
		var stdout, stderr bytes.Buffer
		// A nil store: the command must decide before it touches the store.
		code := runTasksCommand(args, nil, &stdout, &stderr)
		if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage: rencrow tasks") {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q, want exit 2 with the usage on stderr only", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestRunTasksCommandHelpDoesNotTouchTheStore(t *testing.T) {
	for _, args := range [][]string{
		{"list", "--help"}, {"list", "-h"}, {"show", "--help"}, {"create", "--title", "x", "--help"},
		{"queue", "-h"}, {"status", "--help"}, {"notifications", "-h"}, {"--help"}, {"-h"},
	} {
		var stdout, stderr bytes.Buffer
		code := runTasksCommand(args, nil, &stdout, &stderr) // a nil store panics if it is used
		if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "usage: rencrow tasks") {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q, want exit 0 with the usage on stdout only", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestRunActionsCommandHelpDoesNotTouchTheStores(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created")
	for _, args := range [][]string{
		{"--help"}, {"-h"}, {"verify", "--help"}, {"verify", "--check-runs", "-h"},
		{"layout", "--help"}, {"layout", "show", "--help"}, {"layout", "concat", "--apply", "-h"},
	} {
		var stdout, stderr bytes.Buffer
		code := runActionsCommand(args, root, root, &stdout, &stderr)
		if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "usage: rencrow actions") {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q, want exit 0 with the usage on stdout only", args, code, stdout.String(), stderr.String())
		}
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a help request created %s (stat err=%v)", root, err)
	}
}

func TestTasksReapHelpFormsOfTheFlagPackageExitZero(t *testing.T) {
	// reap-queued parses with the standard flag package; its own help forms
	// (-help, -h, --help) are a request for the usage, not a bad command line.
	for _, args := range [][]string{{"reap-queued", "-help"}, {"reap-queued", "-h"}, {"reap-queued", "--help"}} {
		var stdout, stderr bytes.Buffer
		code := runTasksReapCommand(args, filepath.Join(t.TempDir(), "not-created"), &stdout, &stderr)
		if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "usage: rencrow tasks reap-queued") {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q, want exit 0 with the usage on stdout only", args, code, stdout.String(), stderr.String())
		}
	}
}
