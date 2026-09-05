package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func cmdTasks() {
	manager, err := loadTaskManager(getConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize task manager: %v\n", err)
		os.Exit(1)
	}
	code := runTasksCommand(os.Args[2:], manager, os.Stdout, os.Stderr)
	if code != 0 {
		os.Exit(code)
	}
}

type taskCommandStore interface {
	Create(context.Context, domaintask.Task, domaintask.SharedRoleContext) (domaintask.Task, error)
	Queue(context.Context, modulecore.TaskID) (domaintask.Task, error)
	Start(context.Context, modulecore.TaskID) (domaintask.Task, error)
	Wait(context.Context, modulecore.TaskID, string) (domaintask.Task, error)
	Block(context.Context, modulecore.TaskID, string) (domaintask.Task, error)
	Resume(context.Context, modulecore.TaskID) (domaintask.Task, error)
	Succeed(context.Context, modulecore.TaskID, string) (domaintask.Task, error)
	Fail(context.Context, modulecore.TaskID, string, []string) (domaintask.Task, error)
	Cancel(context.Context, modulecore.TaskID, string) (domaintask.Task, error)
	Supersede(context.Context, modulecore.TaskID, modulecore.TaskID) (domaintask.Task, error)
	UpdateStatus(context.Context, modulecore.TaskID, domaintask.Status, string, string, []string) (domaintask.Task, error)
	List(context.Context, domaintask.Filter) ([]domaintask.Task, error)
	Get(context.Context, modulecore.TaskID) (domaintask.Task, error)
	Context(context.Context, modulecore.TaskID) (domaintask.SharedRoleContext, error)
	Notifications(context.Context, int, bool) ([]domaintask.Notification, error)
}

func runTasksCommand(args []string, manager taskCommandStore, out io.Writer, errOut io.Writer) int {
	compact := hasFlag(args, "--compact")
	args = removeTaskFlag(args, "--compact")
	subcommand := "list"
	if len(args) > 0 {
		subcommand = strings.ToLower(strings.TrimSpace(args[0]))
	}
	pretty := !compact
	switch subcommand {
	case "list":
		filter, jsonOut, err := parseTasksListArgs(args[1:])
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		items, err := manager.List(context.Background(), filter)
		if err != nil {
			fmt.Fprintf(errOut, "failed to list tasks: %v\n", err)
			return 1
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{"items": items}, pretty)
			return 0
		}
		if len(items) == 0 {
			fmt.Fprintln(out, "No tasks")
			return 0
		}
		for _, item := range items {
			fmt.Fprintf(out, "%s | %s | %s | %s | %s\n", item.TaskID, item.Status, item.Route, item.Assignee, item.Title)
		}
		return 0
	case "show":
		taskID, err := parseTaskIDArg(args, 1, "usage: rencrow tasks show <task_id>")
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		value, err := manager.Get(context.Background(), taskID)
		if err != nil {
			fmt.Fprintf(errOut, "failed to get task: %v\n", err)
			return 1
		}
		shared, err := manager.Context(context.Background(), taskID)
		if err != nil && !errors.Is(err, taskmanager.ErrNotFound) {
			fmt.Fprintf(errOut, "failed to get task context: %v\n", err)
			return 1
		}
		writeJSONCLI(out, map[string]any{"task": value, "context": shared}, pretty)
		return 0
	case "create":
		draft, shared, jsonOut, err := parseTasksCreateArgs(args[1:])
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		value, err := manager.Create(context.Background(), draft, shared)
		if err != nil {
			fmt.Fprintf(errOut, "failed to create task: %v\n", err)
			return 1
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{"task": value}, pretty)
			return 0
		}
		fmt.Fprintf(out, "created %s\n", value.TaskID)
		return 0
	case "queue", "start", "wait", "block", "resume", "succeed", "fail", "cancel", "supersede":
		return runTaskTransition(subcommand, args[1:], manager, out, errOut, pretty)
	case "status":
		taskID, err := parseTaskIDArg(args, 1, "usage: rencrow tasks status <task_id> <status> [--summary text] [--reason text]")
		if err != nil || len(args) < 3 {
			if err == nil {
				err = fmt.Errorf("usage: rencrow tasks status <task_id> <status> [--summary text] [--reason text]")
			}
			fmt.Fprintln(errOut, err)
			return 1
		}
		status := domaintask.Status(strings.TrimSpace(args[2]))
		value, err := manager.UpdateStatus(context.Background(), taskID, status, stringFlag(args[3:], "--summary"), stringFlag(args[3:], "--reason"), nil)
		if err != nil {
			fmt.Fprintf(errOut, "failed to update task: %v\n", err)
			return 1
		}
		writeJSONCLI(out, map[string]any{"task": value}, pretty)
		return 0
	case "notifications":
		limit, jsonOut := parseTaskNotificationsArgs(args[1:])
		items, err := manager.Notifications(context.Background(), limit, true)
		if err != nil {
			fmt.Fprintf(errOut, "failed to list notifications: %v\n", err)
			return 1
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{"items": items}, pretty)
			return 0
		}
		if len(items) == 0 {
			fmt.Fprintln(out, "No task notifications")
			return 0
		}
		for _, item := range items {
			fmt.Fprintf(out, "%s | %s | %s | %s\n", item.TaskID, item.Level, item.Status, item.Title)
		}
		return 0
	default:
		fmt.Fprintf(errOut, "unknown tasks subcommand: %s\n", subcommand)
		fmt.Fprintln(errOut, "usage: rencrow tasks [list|show|create|queue|start|wait|block|resume|succeed|fail|cancel|supersede|status|notifications]")
		return 1
	}
}

func removeTaskFlag(args []string, name string) []string {
	filtered := make([]string, 0, len(args))
	for _, argument := range args {
		if strings.EqualFold(strings.TrimSpace(argument), name) {
			continue
		}
		filtered = append(filtered, argument)
	}
	return filtered
}

func runTaskTransition(subcommand string, args []string, manager taskCommandStore, out, errOut io.Writer, pretty bool) int {
	taskID, err := parseTaskIDArg(args, 0, "task_id is required")
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	var value domaintask.Task
	switch subcommand {
	case "queue":
		value, err = manager.Queue(context.Background(), taskID)
	case "start":
		value, err = manager.Start(context.Background(), taskID)
	case "wait":
		value, err = manager.Wait(context.Background(), taskID, stringFlag(args[1:], "--reason"))
	case "block":
		value, err = manager.Block(context.Background(), taskID, stringFlag(args[1:], "--reason"))
	case "resume":
		value, err = manager.Resume(context.Background(), taskID)
	case "succeed":
		value, err = manager.Succeed(context.Background(), taskID, stringFlag(args[1:], "--summary"))
	case "fail":
		value, err = manager.Fail(context.Background(), taskID, stringFlag(args[1:], "--summary"), nil)
	case "cancel":
		value, err = manager.Cancel(context.Background(), taskID, stringFlag(args[1:], "--summary"))
	case "supersede":
		replacement, parseErr := parseTaskIDArg(args, 1, "replacement_task_id is required")
		if parseErr != nil {
			fmt.Fprintln(errOut, parseErr)
			return 1
		}
		value, err = manager.Supersede(context.Background(), taskID, replacement)
	}
	if err != nil {
		fmt.Fprintf(errOut, "failed to %s task: %v\n", subcommand, err)
		return 1
	}
	writeJSONCLI(out, map[string]any{"task": value}, pretty)
	return 0
}

func parseTaskIDArg(args []string, index int, message string) (modulecore.TaskID, error) {
	if index >= len(args) || strings.TrimSpace(args[index]) == "" {
		return "", fmt.Errorf("%s", message)
	}
	value := modulecore.TaskID(strings.TrimSpace(args[index]))
	if err := value.Validate(); err != nil {
		return "", fmt.Errorf("invalid task_id: %w", err)
	}
	return value, nil
}

func parseTasksListArgs(args []string) (domaintask.Filter, bool, error) {
	filter := domaintask.Filter{Limit: 20}
	jsonOut := false
	for i := 0; i < len(args); i++ {
		value := strings.TrimSpace(args[i])
		switch strings.ToLower(value) {
		case "--json":
			jsonOut = true
		case "--status":
			if i+1 >= len(args) {
				return filter, false, fmt.Errorf("--status requires a value")
			}
			filter.Status = domaintask.Status(strings.TrimSpace(args[i+1]))
			i++
		case "--module", "--module-id":
			if i+1 >= len(args) {
				return filter, false, fmt.Errorf("%s requires a value", value)
			}
			filter.ModuleID = strings.TrimSpace(args[i+1])
			i++
		case "--assignee":
			if i+1 >= len(args) {
				return filter, false, fmt.Errorf("--assignee requires a value")
			}
			filter.Assignee = strings.TrimSpace(args[i+1])
			i++
		case "--route":
			if i+1 >= len(args) {
				return filter, false, fmt.Errorf("--route requires a value")
			}
			filter.Route = domaintask.Route(strings.ToUpper(strings.TrimSpace(args[i+1])))
			i++
		default:
			if limit, err := strconv.Atoi(value); err == nil && limit > 0 {
				filter.Limit = limit
			}
		}
	}
	return filter, jsonOut, nil
}

func parseTasksCreateArgs(args []string) (domaintask.Task, domaintask.SharedRoleContext, bool, error) {
	var draft domaintask.Task
	var shared domaintask.SharedRoleContext
	jsonOut := false
	for i := 0; i < len(args); i++ {
		value := strings.TrimSpace(args[i])
		switch strings.ToLower(value) {
		case "--json":
			jsonOut = true
		case "--title":
			if i+1 >= len(args) {
				return draft, shared, false, fmt.Errorf("--title requires a value")
			}
			draft.Title = strings.TrimSpace(args[i+1])
			shared.UserIntent = draft.Title
			i++
		case "--module", "--module-id":
			if i+1 >= len(args) {
				return draft, shared, false, fmt.Errorf("%s requires a value", value)
			}
			draft.ModuleID, shared.ModuleID = strings.TrimSpace(args[i+1]), strings.TrimSpace(args[i+1])
			i++
		case "--module-root":
			if i+1 >= len(args) {
				return draft, shared, false, fmt.Errorf("--module-root requires a value")
			}
			draft.ModuleRoot, shared.ModuleRoot = strings.TrimSpace(args[i+1]), strings.TrimSpace(args[i+1])
			i++
		case "--route":
			if i+1 >= len(args) {
				return draft, shared, false, fmt.Errorf("--route requires a value")
			}
			draft.Route = domaintask.Route(strings.ToUpper(strings.TrimSpace(args[i+1])))
			i++
		case "--assignee":
			if i+1 >= len(args) {
				return draft, shared, false, fmt.Errorf("--assignee requires a value")
			}
			draft.Assignee = strings.TrimSpace(args[i+1])
			i++
		case "--owner-id":
			if i+1 >= len(args) {
				return draft, shared, false, fmt.Errorf("--owner-id requires a value")
			}
			draft.OwnerID = strings.TrimSpace(args[i+1])
			i++
		case "--parent-task-id":
			parsed, err := parseCanonicalTaskOption[modulecore.TaskID](args, &i, value)
			if err != nil {
				return draft, shared, false, err
			}
			draft.ParentTaskID = parsed
		case "--dependency-task-id":
			parsed, err := parseCanonicalTaskOption[modulecore.TaskID](args, &i, value)
			if err != nil {
				return draft, shared, false, err
			}
			draft.DependencyTaskIDs = append(draft.DependencyTaskIDs, parsed)
		case "--origin-session-id":
			parsed, err := parseCanonicalTaskOption[modulecore.SessionID](args, &i, value)
			if err != nil {
				return draft, shared, false, err
			}
			draft.OriginSessionID = parsed
		case "--origin-thread-id":
			parsed, err := parseCanonicalTaskOption[modulecore.ThreadID](args, &i, value)
			if err != nil {
				return draft, shared, false, err
			}
			draft.OriginThreadID = parsed
		case "--origin-turn-id":
			parsed, err := parseCanonicalTaskOption[modulecore.TurnID](args, &i, value)
			if err != nil {
				return draft, shared, false, err
			}
			draft.OriginTurnID = parsed
		case "--origin-message-id":
			parsed, err := parseCanonicalTaskOption[modulecore.MessageID](args, &i, value)
			if err != nil {
				return draft, shared, false, err
			}
			draft.OriginMessageID = parsed
		case "--workstream-id":
			parsed, err := parseCanonicalTaskOption[modulecore.WorkstreamID](args, &i, value)
			if err != nil {
				return draft, shared, false, err
			}
			draft.WorkstreamID = parsed
		case "--goal-id":
			parsed, err := parseCanonicalTaskOption[modulecore.GoalID](args, &i, value)
			if err != nil {
				return draft, shared, false, err
			}
			draft.GoalID = parsed
		case "--supersedes-task-id":
			parsed, err := parseCanonicalTaskOption[modulecore.TaskID](args, &i, value)
			if err != nil {
				return draft, shared, false, err
			}
			draft.SupersedesTaskID = parsed
		case "--priority":
			if i+1 >= len(args) {
				return draft, shared, false, fmt.Errorf("--priority requires a value")
			}
			draft.Priority = domaintask.Priority(strings.ToLower(strings.TrimSpace(args[i+1])))
			i++
		case "--read-only":
			draft.ReadOnly = true
		case "--plan":
			if i+1 >= len(args) {
				return draft, shared, false, fmt.Errorf("--plan requires a value")
			}
			shared.CurrentPlan = strings.TrimSpace(args[i+1])
			i++
		default:
			return draft, shared, false, fmt.Errorf("unknown create option: %s", value)
		}
	}
	if strings.TrimSpace(draft.Title) == "" {
		return draft, shared, false, fmt.Errorf("--title is required")
	}
	return draft, shared, jsonOut, nil
}

func parseCanonicalTaskOption[T interface {
	~string
	Validate() error
}](args []string, index *int, option string) (T, error) {
	var zero T
	if index == nil || *index+1 >= len(args) {
		return zero, fmt.Errorf("%s requires a value", option)
	}
	value := T(strings.TrimSpace(args[*index+1]))
	if err := value.Validate(); err != nil {
		return zero, fmt.Errorf("%s is invalid: %w", option, err)
	}
	*index = *index + 1
	return value, nil
}

func parseTaskNotificationsArgs(args []string) (int, bool) {
	limit, jsonOut := 20, false
	for _, argument := range args {
		value := strings.TrimSpace(argument)
		if strings.EqualFold(value, "--json") {
			jsonOut = true
			continue
		}
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	return limit, jsonOut
}

func stringFlag(args []string, name string) string {
	for index := 0; index+1 < len(args); index++ {
		if strings.EqualFold(strings.TrimSpace(args[index]), name) {
			return strings.TrimSpace(args[index+1])
		}
	}
	return ""
}

func loadTaskManager(configPath string) (*taskmanager.Manager, error) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	store, err := taskpersistence.NewJSONLStore(defaultTaskStorePath(cfg.WorkspaceDir))
	if err != nil {
		return nil, err
	}
	return taskmanager.New(store, taskmanager.DefaultParallelLimits()), nil
}

func defaultTaskStorePath(workspaceDir string) string {
	if strings.TrimSpace(workspaceDir) == "" {
		return ""
	}
	return filepath.Join(workspaceDir, "tasks")
}
