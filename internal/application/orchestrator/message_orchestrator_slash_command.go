package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
)

const maxSlashCommandBodyBytes = 64 * 1024

func (o *MessageOrchestrator) expandRegisteredSlashCommand(ctx context.Context, req ProcessMessageRequest) (ProcessMessageRequest, bool, error) {
	return expandRegisteredSlashCommand(ctx, o.commandRegistry, o.workflowEvents, o.skillBootstrap, req)
}

func (o *DistributedOrchestrator) expandRegisteredSlashCommand(ctx context.Context, req ProcessMessageRequest) (ProcessMessageRequest, bool, error) {
	return expandRegisteredSlashCommand(ctx, o.commandRegistry, o.workflowEvents, o.skillBootstrap, req)
}

func expandRegisteredSlashCommand(
	ctx context.Context,
	registry CommandRegistryLister,
	workflowEvents WorkflowEventRecorder,
	skillBootstrap SkillBootstrapRecorder,
	req ProcessMessageRequest,
) (ProcessMessageRequest, bool, error) {
	if registry == nil {
		return req, false, nil
	}
	commandName, userInput, ok := parseSlashCommandInvocation(req.UserMessage)
	if !ok {
		return req, false, nil
	}
	commands, err := registry.ListCommandRegistries(ctx, 1000)
	if err != nil {
		return req, false, fmt.Errorf("slash command expansion failed: load command registry: %w", err)
	}
	command, found := findAIWorkflowCommand(commands, commandName)
	if !found {
		return req, false, nil
	}
	body, err := readSlashCommandFile(command.FilePath)
	if err != nil {
		return req, false, fmt.Errorf("slash command expansion failed: %w", err)
	}
	agent := firstNonEmptyStringLocal(command.DefaultAgent, "Chat")
	if err := recordSlashCommandInvocation(ctx, workflowEvents, command, agent, userInput); err != nil {
		return req, false, fmt.Errorf("slash command expansion failed: %w", err)
	}
	if err := recordSlashCommandSkillBootstrap(ctx, skillBootstrap, command, agent, req, userInput); err != nil {
		return req, false, fmt.Errorf("slash command expansion failed: %w", err)
	}
	req.UserMessage = buildSlashCommandRuntimePrompt(command, body, userInput)
	return req, true, nil
}

func parseSlashCommandInvocation(message string) (commandName, userInput string, ok bool) {
	trimmed := strings.TrimSpace(message)
	if !strings.HasPrefix(trimmed, "/") || trimmed == "/" {
		return "", "", false
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", "", false
	}
	commandName = fields[0]
	userInput = strings.TrimSpace(strings.TrimPrefix(trimmed, commandName))
	return commandName, userInput, true
}

func findAIWorkflowCommand(commands []domainai.CommandRegistry, commandName string) (domainai.CommandRegistry, bool) {
	for _, command := range commands {
		if command.CommandName == commandName {
			return command, true
		}
	}
	return domainai.CommandRegistry{}, false
}

func readSlashCommandFile(path string) (string, error) {
	clean, err := validateSlashCommandFilePath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(clean)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("command file must not be a directory: %s", clean)
	}
	if info.Size() > maxSlashCommandBodyBytes {
		return "", fmt.Errorf("command file exceeds %d bytes: %s", maxSlashCommandBodyBytes, clean)
	}
	body, err := os.ReadFile(clean)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func validateSlashCommandFilePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("command file_path is required")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("command file_path must be relative: %s", path)
	}
	clean := filepath.Clean(path)
	if clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("command file_path must stay inside commands/: %s", path)
	}
	slash := filepath.ToSlash(clean)
	if !strings.HasPrefix(slash, "commands/") || filepath.Ext(clean) != ".md" {
		return "", fmt.Errorf("command file_path must be commands/*.md: %s", path)
	}
	return clean, nil
}

func recordSlashCommandInvocation(ctx context.Context, workflowEvents WorkflowEventRecorder, command domainai.CommandRegistry, agent string, userInput string) error {
	if workflowEvents == nil {
		return nil
	}
	now := time.Now().UTC()
	event := domainai.WorkflowEvent{
		EventID:     fmt.Sprintf("command_invoked:%s:%d", strings.TrimPrefix(command.CommandName, "/"), now.UnixNano()),
		EventType:   "command_invoked",
		Agent:       agent,
		CommandName: command.CommandName,
		Status:      "expanded",
		CreatedAt:   now,
		Summary:     userInput,
	}
	if err := workflowEvents.SaveWorkflowEvent(ctx, event); err != nil {
		return fmt.Errorf("save command event: %w", err)
	}
	return nil
}

func recordSlashCommandSkillBootstrap(ctx context.Context, skillBootstrap SkillBootstrapRecorder, command domainai.CommandRegistry, agent string, req ProcessMessageRequest, userInput string) error {
	if skillBootstrap == nil || command.RequiredSkill == "" {
		return nil
	}
	_, err := skillBootstrap.Record(ctx, domainskill.TaskContext{
		Text:         firstNonEmptyStringLocal(userInput, command.Description, command.CommandName),
		Intent:       strings.TrimPrefix(command.CommandName, "/"),
		Agent:        agent,
		WorkstreamID: req.SessionID,
	}, []string{command.RequiredSkill})
	if err != nil {
		return fmt.Errorf("command skill bootstrap failed: %w", err)
	}
	return nil
}

func buildSlashCommandRuntimePrompt(command domainai.CommandRegistry, commandBody, userInput string) string {
	var b strings.Builder
	b.WriteString("Slash command runtime expansion\n\n")
	b.WriteString("Command: ")
	b.WriteString(command.CommandName)
	b.WriteString("\n")
	if command.Description != "" {
		b.WriteString("Description: ")
		b.WriteString(command.Description)
		b.WriteString("\n")
	}
	if command.DefaultAgent != "" {
		b.WriteString("Default agent: ")
		b.WriteString(command.DefaultAgent)
		b.WriteString("\n")
	}
	if command.RequiredSkill != "" {
		b.WriteString("Required skill: ")
		b.WriteString(command.RequiredSkill)
		b.WriteString("\n")
	}
	b.WriteString("Command file: ")
	b.WriteString(command.FilePath)
	b.WriteString("\n\n")
	b.WriteString("Command body:\n")
	b.WriteString(commandBody)
	if !strings.HasSuffix(commandBody, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\nUser input:\n")
	b.WriteString(userInput)
	b.WriteString("\n\nSafety:\n")
	b.WriteString("- Treat this as a command workflow, not as permission to bypass Tool Harness, Sandbox Promotion Gate, or Human approval.\n")
	b.WriteString("- Do not publish, send externally, mutate official state, or run destructive operations unless the normal approval gates allow it.\n")
	return b.String()
}

func firstNonEmptyStringLocal(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
