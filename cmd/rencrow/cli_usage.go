package main

import (
	"fmt"
	"io"
	"strings"
)

// The usage text of `rencrow tasks` and `rencrow actions`, and what happens when
// the command line asks for it.
//
// --help / -h anywhere on the command line prints the usage on stdout and exits
// 0. The request is answered before the config file or any store is opened, so a
// help request reads and writes nothing, whatever else is on the command line
// (`layout concat --apply --help` applies nothing). Because the check looks at
// every argument, a value that is itself "-h" or "--help" (for example
// `create --title -h`) is also taken as the request for help.
//
// `rencrow tasks` with no subcommand is a bad command line: the usage goes to
// stderr and the status is 2 (the same status reap-queued uses for one).

const cliExitUsage = 2

// isHelpArg reports whether one argument is a request for the usage text.
func isHelpArg(arg string) bool {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "--help", "-h":
		return true
	}
	return false
}

func hasHelpArg(args []string) bool {
	for _, arg := range args {
		if isHelpArg(arg) {
			return true
		}
	}
	return false
}

// firstSubcommandArg is the first argument that is neither a request for help
// nor --compact: the subcommand a help request is about. It is "" when there is
// none.
func firstSubcommandArg(args []string) string {
	for _, arg := range args {
		if isHelpArg(arg) || strings.EqualFold(strings.TrimSpace(arg), "--compact") {
			continue
		}
		return strings.ToLower(strings.TrimSpace(arg))
	}
	return ""
}

// ---- rencrow tasks ----

const (
	tasksUsageList          = "usage: rencrow tasks list [--status <status>] [--module <module_id>] [--assignee <name>] [--route <route>] [<limit>] [--json]"
	tasksUsageShow          = "usage: rencrow tasks show <task_id>"
	tasksUsageCreate        = "usage: rencrow tasks create --title <text> [--module <module_id>] [--module-root <path>] [--route <route>] [--assignee <name>] [--owner-id <id>] [--priority <priority>] [--plan <text>] [--read-only] [--parent-task-id <task_id>] [--dependency-task-id <task_id>]... [--supersedes-task-id <task_id>] [--origin-session-id <id>] [--origin-thread-id <id>] [--origin-turn-id <id>] [--origin-message-id <id>] [--workstream-id <id>] [--goal-id <id>] [--json]"
	tasksUsageQueue         = "usage: rencrow tasks queue <task_id>"
	tasksUsageStart         = "usage: rencrow tasks start <task_id>"
	tasksUsageWait          = "usage: rencrow tasks wait <task_id> [--reason <text>]"
	tasksUsageBlock         = "usage: rencrow tasks block <task_id> [--reason <text>]"
	tasksUsageResume        = "usage: rencrow tasks resume <task_id>"
	tasksUsageSucceed       = "usage: rencrow tasks succeed <task_id> [--summary <text>]"
	tasksUsageFail          = "usage: rencrow tasks fail <task_id> [--summary <text>]"
	tasksUsageCancel        = "usage: rencrow tasks cancel <task_id> [--summary <text>]"
	tasksUsageSupersede     = "usage: rencrow tasks supersede <task_id> <replacement_task_id>"
	tasksUsageStatus        = "usage: rencrow tasks status <task_id> <status> [--summary text] [--reason text]"
	tasksUsageNotifications = "usage: rencrow tasks notifications [<limit>] [--json]"
	tasksUsageHistory       = "usage: rencrow tasks history <task_id>"
	tasksUsageVerify        = "usage: rencrow tasks verify"
	tasksUsageRebuildIndex  = "usage: rencrow tasks rebuild-index"
	tasksUsageLayout        = "usage: rencrow tasks layout show | rencrow tasks layout concat [--apply]"
	tasksUsageReap          = "usage: rencrow tasks reap-queued --created-before <RFC3339> [--min-age 24h] [--batch-size 2000] [--apply] [--compact]"
)

// tasksSubcommands is the one list of the `rencrow tasks` subcommands: the
// summary line and the help text are both made from it.
var tasksSubcommands = []struct{ name, usage string }{
	{"list", tasksUsageList},
	{"show", tasksUsageShow},
	{"create", tasksUsageCreate},
	{"queue", tasksUsageQueue},
	{"start", tasksUsageStart},
	{"wait", tasksUsageWait},
	{"block", tasksUsageBlock},
	{"resume", tasksUsageResume},
	{"succeed", tasksUsageSucceed},
	{"fail", tasksUsageFail},
	{"cancel", tasksUsageCancel},
	{"supersede", tasksUsageSupersede},
	{"status", tasksUsageStatus},
	{"notifications", tasksUsageNotifications},
	{"history", tasksUsageHistory},
	{"verify", tasksUsageVerify},
	{"rebuild-index", tasksUsageRebuildIndex},
	{"layout", tasksUsageLayout},
	{"reap-queued", tasksUsageReap},
}

// tasksUsageSummary is the one-line usage printed when the subcommand is
// missing or unknown.
func tasksUsageSummary() string {
	names := make([]string, 0, len(tasksSubcommands))
	for _, sub := range tasksSubcommands {
		names = append(names, sub.name)
	}
	return "usage: rencrow tasks [" + strings.Join(names, "|") + "]"
}

// tasksHelpText is the text of `rencrow tasks ... --help`: the usage of the
// subcommand that was named, or the usage of all of them.
func tasksHelpText(args []string) string {
	name := firstSubcommandArg(args)
	for _, sub := range tasksSubcommands {
		if sub.name == name {
			return sub.usage
		}
	}
	var text strings.Builder
	text.WriteString(tasksUsageSummary())
	text.WriteString("\n\n")
	for _, sub := range tasksSubcommands {
		text.WriteString(sub.usage)
		text.WriteString("\n")
	}
	text.WriteString("\n--compact (anywhere on the command line) prints JSON on one line.\n--help / -h prints this text and does nothing else.")
	return text.String()
}

// tasksPreflight answers the command lines that must be answered before the
// config file or the Task store is opened: a request for help (usage on out,
// status 0) and a missing subcommand (usage on errOut, status 2). done is false
// for every other command line.
func tasksPreflight(args []string, out, errOut io.Writer) (status int, done bool) {
	if hasHelpArg(args) {
		fmt.Fprintln(out, tasksHelpText(args))
		return 0, true
	}
	if len(removeTaskFlag(args, "--compact")) == 0 {
		fmt.Fprintln(errOut, tasksUsageSummary())
		return cliExitUsage, true
	}
	return 0, false
}

// ---- rencrow actions ----

const (
	actionsUsageSummary = "usage: rencrow actions verify [--check-runs] [--compact] | layout show | layout concat [--apply]"
	actionsUsageVerify  = "usage: rencrow actions verify [--check-runs] [--compact]"
	actionsUsageLayout  = "usage: rencrow actions layout show [--compact] | rencrow actions layout concat [--apply] [--compact]"
)

// actionsHelpText is the text of `rencrow actions ... --help`.
func actionsHelpText(args []string) string {
	switch firstSubcommandArg(args) {
	case "verify":
		return actionsUsageVerify
	case "layout":
		return actionsUsageLayout
	}
	return actionsUsageSummary + "\n\n" + actionsUsageVerify + "\n" + actionsUsageLayout + "\n\n--help / -h prints this text and does nothing else."
}

// actionsPreflight answers a request for help before the config file or any
// store is opened (usage on out, status 0). done is false for every other
// command line.
func actionsPreflight(args []string, out io.Writer) (status int, done bool) {
	if hasHelpArg(args) {
		fmt.Fprintln(out, actionsHelpText(args))
		return 0, true
	}
	return 0, false
}
