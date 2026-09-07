package service

import (
	"context"
	"fmt"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/security"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/coderloop"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
)

// ObservationAction は ExecuteObservation に渡す単一アクション（coderloop 型のエイリアス）
type ObservationAction = coderloop.ObservationAction

// ObservationActionResult は ExecuteObservation の単一結果（coderloop 型のエイリアス）
type ObservationActionResult = coderloop.ObservationActionResult

// ExecuteObservation は読み取り専用アクションを実行して観測結果を返す
func (w *workerExecutionService) ExecuteObservation(
	ctx context.Context,
	actions []ObservationAction,
) ([]ObservationActionResult, error) {
	if w == nil || w.owner == nil {
		return nil, fmt.Errorf("worker observation execution owner is unavailable")
	}
	if _, err := w.owner.ValidateExecutionContext(ctx); err != nil {
		return nil, fmt.Errorf("worker observation execution admission denied: %w", err)
	}
	results := make([]ObservationActionResult, 0, len(actions))
	for i := 0; i < len(actions); i++ {
		a := actions[i]
		actionCtx := ctx
		cancel := func() {}
		if !isCanonicalTestObservation(a) {
			seconds := w.config.CommandTimeout
			if seconds <= 0 {
				seconds = config.DefaultWorkerCommandTimeoutSeconds
			}
			if int64(seconds) > int64((1<<63-1)/time.Second) {
				return results, fmt.Errorf("Worker observation timeout is out of range")
			}
			actionCtx, cancel = context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
		}
		result := w.executeSingleObservation(actionCtx, a)
		actionErr := actionCtx.Err()
		cancel()
		// Do not accept late output or begin the next action after ownership expires.
		_, ownerErr := w.owner.ValidateExecutionContext(ctx)
		if actionErr == nil {
			actionErr = ownerErr
		}
		if err := actionErr; err != nil {
			admissionErr := fmt.Errorf("worker observation execution admission expired: %w", err)
			expired := coderloop.NewObservationActionResult(a.Action, a.Target, "", admissionErr)
			expired.TestStatus, expired.TestReceipt = result.TestStatus, result.TestReceipt
			results = append(results, expired)
			return results, admissionErr
		}
		results = append(results, result)
		// Adjacent verification hints form one canonical verification request.
		// No receipt is reused across intervening actions or separate batches.
		if result.TestStatus != "" {
			for i+1 < len(actions) && isCanonicalTestObservation(actions[i+1]) {
				i++
				projection := result
				projection.Action, projection.Target = actions[i].Action, actions[i].Target
				results = append(results, projection)
			}
		}
	}
	return results, nil
}

func (w *workerExecutionService) executeSingleObservation(
	ctx context.Context,
	a ObservationAction,
) ObservationActionResult {
	switch a.Action {
	case "shell_command":
		argv, err := observationCommandArgs(a.Target)
		if err != nil {
			return coderloop.NewObservationActionResult(a.Action, a.Target, "", err)
		}
		if argv[0] == "go" {
			return w.executeObservationTestImpact(ctx, a)
		}
		if argv[0] == "git" {
			if err := w.validateObservationGitWorkspace(ctx); err != nil {
				return coderloop.NewObservationActionResult(a.Action, a.Target, "", err)
			}
		}
		var paths []string
		regularFiles := false
		switch argv[0] {
		case "git":
			invocation, parseErr := observationGitArgs(argv)
			if parseErr != nil {
				return coderloop.NewObservationActionResult(a.Action, a.Target, "", parseErr)
			}
			for _, operand := range invocation.diffOperands {
				if operand.mayBeRevision {
					object, _, resolveErr := runWorkerCommand(ctx, w.config.Workspace, "git", "rev-parse", "--verify", "--end-of-options", operand.value+"^{object}")
					if resolveErr == nil && strings.TrimSpace(object) != "" {
						continue
					}
				}
				paths = append(paths, operand.value)
			}
			argv = invocation.argv
		case "find":
			paths, err = observationFindRoots(argv[1:])
		case "cat", "head", "tail", "wc", "grep":
			paths, err = observationReadFiles(argv)
			regularFiles = true
		}
		if err != nil {
			return coderloop.NewObservationActionResult(a.Action, a.Target, "", err)
		}
		guard := security.NewSandboxGuard()
		for _, path := range paths {
			target := path
			if !filepath.IsAbs(target) {
				// Preserve parent components for the canonical guard to reject.
				target = w.config.Workspace + string(filepath.Separator) + target
			}
			if !guard.IsPathWithinWorkspace(target, w.config.Workspace) {
				return coderloop.NewObservationActionResult(a.Action, a.Target, "", fmt.Errorf("observation file/root outside or unresolved workspace: %q", path))
			}
			if regularFiles {
				info, statErr := os.Stat(target)
				if statErr != nil || !info.Mode().IsRegular() {
					return coderloop.NewObservationActionResult(a.Action, a.Target, "", fmt.Errorf("observation file must exist and be regular: %q", path))
				}
			}
		}

		stdout, stderr, err := runWorkerCommand(ctx, w.config.Workspace, argv[0], argv[1:]...)
		return coderloop.NewObservationActionResult(a.Action, a.Target, stdout+stderr, err)

	case "mcp_tool":
		if w.mcpCaller == nil {
			return coderloop.NewObservationActionResult(a.Action, a.Target, "",
				fmt.Errorf("mcp_tool requested but MCP caller not configured"))
		}
		args := a.Args
		if args == nil {
			args = map[string]any{}
		}
		output, err := w.mcpCaller.CallTool(ctx, a.Target, args)
		return coderloop.NewObservationActionResult(a.Action, a.Target, output, err)

	default:
		return coderloop.NewObservationActionResult(a.Action, a.Target, "",
			fmt.Errorf("unsupported observation action: %q", a.Action))
	}
}

// observationCommandArgs accepts one literal argv, never a shell program.
func observationCommandArgs(command string) ([]string, error) {
	var args []string
	var word strings.Builder
	var quote rune
	started, escaped := false, false
	flush := func() {
		if started {
			args = append(args, word.String())
			word.Reset()
			started = false
		}
	}
	for _, r := range command {
		if unicode.IsControl(r) && r != '\t' {
			return nil, fmt.Errorf("control character in observation command")
		}
		if escaped {
			if quote == '"' && r != '"' && r != '\\' && r != '$' && r != '`' {
				word.WriteRune('\\')
			}
			word.WriteRune(r)
			started = true
			escaped = false
			continue
		}
		if quote == '\'' {
			if r == '\'' {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		if r == '$' || r == '`' {
			return nil, fmt.Errorf("shell expansion is not allowed in observation commands")
		}
		if quote == '"' {
			if r == '"' {
				quote = 0
			} else if r == '\\' {
				escaped = true
			} else {
				word.WriteRune(r)
			}
			continue
		}
		switch {
		case r == '\\':
			escaped = true
			started = true
		case r == '\'' || r == '"':
			quote = r
			started = true
		case unicode.IsSpace(r):
			flush()
		case strings.ContainsRune(";&|<>(){}*?[]~#", r):
			return nil, fmt.Errorf("shell syntax is not allowed in observation commands")
		default:
			word.WriteRune(r)
			started = true
		}
	}
	if quote != 0 || escaped {
		return nil, fmt.Errorf("unfinished quote or escape in observation command")
	}
	flush()
	if len(args) == 0 {
		return nil, fmt.Errorf("observation command is empty")
	}
	allowed := false
	switch args[0] {
	case "find":
		if _, err := observationFindRoots(args[1:]); err != nil {
			return nil, err
		}
		allowed = true
	case "cat", "head", "tail", "wc", "grep":
		if _, err := observationReadFiles(args); err != nil {
			return nil, err
		}
		allowed = true
	case "git":
		if _, err := observationGitArgs(args); err != nil {
			return nil, err
		}
		allowed = true
	case "go":
		if len(args) > 1 {
			switch args[1] {
			case "test", "build", "vet":
				allowed = true
			}
		}
	}
	if !allowed {
		return nil, fmt.Errorf("command not in observation allowlist: %q", command)
	}
	return args, nil
}

func observationFindRoots(args []string) ([]string, error) {
	var roots []string
	expression := false
	for i := 0; i < len(args); i++ {
		token := args[i]
		if !expression && !strings.HasPrefix(token, "-") && token != "!" && token != "(" && token != ")" {
			roots = append(roots, token)
			continue
		}
		expression = true
		switch token {
		case "-print", "-print0", "-empty", "-true", "-false", "-prune", "-a", "-and", "-o", "-or", "!", "(", ")":
		case "-name", "-iname", "-path", "-ipath", "-type", "-maxdepth", "-mindepth":
			i++
			if i == len(args) {
				return nil, fmt.Errorf("find observation predicate requires a value: %s", token)
			}
			value := args[i]
			switch token {
			case "-type":
				if len(value) != 1 || !strings.Contains("bcdflps", value) {
					return nil, fmt.Errorf("invalid find observation type: %q", value)
				}
			case "-maxdepth", "-mindepth":
				n, err := strconv.Atoi(value)
				if err != nil || n < 0 {
					return nil, fmt.Errorf("invalid find observation depth: %q", value)
				}
			}
		default:
			return nil, fmt.Errorf("find predicate is not allowed in observation: %q", token)
		}
	}
	if len(roots) == 0 {
		roots = []string{"."}
	}
	return roots, nil
}

type observationGitOperand struct {
	value         string
	mayBeRevision bool
}
type observationGitInvocation struct {
	argv         []string
	diffOperands []observationGitOperand
}

func observationGitArgs(args []string) (observationGitInvocation, error) {
	var operands []observationGitOperand
	if len(args) < 2 {
		return observationGitInvocation{}, fmt.Errorf("Git observation requires a subcommand")
	}
	sub := args[1]
	switch sub {
	case "grep", "show", "log", "diff", "ls-files", "status":
	default:
		return observationGitInvocation{}, fmt.Errorf("Git observation subcommand is not allowed: %s", sub)
	}
	for i := 2; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if sub == "diff" {
				for _, path := range args[i+1:] {
					operands = append(operands, observationGitOperand{value: path})
				}
			}
			break
		}
		if !strings.HasPrefix(arg, "-") {
			if sub == "diff" {
				operands = append(operands, observationGitOperand{value: arg, mayBeRevision: true})
			}
			continue
		}
		flag, value, assigned := strings.Cut(arg, "=")
		kind := ""
		switch sub {
		case "status":
			switch flag {
			case "-s", "--short", "-b", "--branch", "--show-stash", "--ignored", "-u", "-uno", "-uall":
				kind = "flag"
			case "--porcelain":
				if !assigned || value == "v1" || value == "v2" {
					kind = "optional"
				}
			case "--untracked-files":
				if !assigned || value == "no" || value == "normal" || value == "all" {
					kind = "optional"
				}
			}
		case "ls-files":
			switch flag {
			case "-z", "--cached", "--deleted", "--modified", "--others", "--ignored", "--stage", "--unmerged", "--exclude-standard", "--full-name":
				kind = "flag"
			}
		case "grep":
			switch flag {
			case "-n", "--line-number", "-i", "--ignore-case", "-I", "-w", "--word-regexp", "-F", "--fixed-strings", "-E", "--extended-regexp", "-G", "--basic-regexp", "-l", "-L", "-h", "-H", "-c", "--count", "-v", "--invert-match", "-q", "--quiet", "--no-textconv", "--cached", "--full-name":
				kind = "flag"
			case "-e":
				kind = "text"
			case "-m", "--max-count":
				kind = "number"
			}
		case "log", "show", "diff":
			switch flag {
			case "-p", "--patch", "--stat", "--numstat", "--name-only", "--name-status", "--summary", "--shortstat", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--exit-code", "--quiet", "-w", "--ignore-all-space", "--ignore-space-at-eol":
				kind = "flag"
			case "-U", "--unified":
				kind = "number"
			}
			if sub != "diff" {
				switch flag {
				case "--oneline", "--no-decorate", "--decorate", "--all", "--branches", "--tags", "--remotes", "--reverse", "--first-parent", "--no-merges":
					kind = "flag"
				case "-n", "--max-count":
					kind = "number"
				case "--since", "--until", "--after", "--before", "--author", "--committer", "--grep":
					kind = "text"
				case "--format", "--pretty":
					kind = "format"
				}
			}
		}
		if kind == "" {
			return observationGitInvocation{}, fmt.Errorf("Git observation option is not allowed: %s", arg)
		}
		if kind == "optional" {
			continue
		}
		if kind == "flag" {
			if assigned {
				return observationGitInvocation{}, fmt.Errorf("unexpected Git observation option value: %s", arg)
			}
			continue
		}
		if !assigned {
			i++
			if i == len(args) {
				return observationGitInvocation{}, fmt.Errorf("Git observation option requires a value: %s", arg)
			}
			value = args[i]
		}
		if kind == "number" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 {
				return observationGitInvocation{}, fmt.Errorf("invalid Git observation number: %s", value)
			}
		}
		if kind == "format" {
			switch value {
			case "oneline", "short", "medium", "full", "fuller", "reference", "raw":
			default:
				return observationGitInvocation{}, fmt.Errorf("custom Git observation formats are not allowed")
			}
		}
	}
	safe := []string{"git", "--no-pager", "--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "log.showSignature=false", "-c", "format.pretty=medium", sub}
	switch sub {
	case "show", "log", "diff":
		safe = append(safe, "--no-ext-diff", "--no-textconv")
	case "grep":
		safe = append(safe, "--no-textconv")
	}
	return observationGitInvocation{argv: append(safe, args[2:]...), diffOperands: operands}, nil
}

func isCanonicalTestObservation(a ObservationAction) bool {
	if a.Action != "shell_command" {
		return false
	}
	argv, err := observationCommandArgs(a.Target)
	return err == nil && argv[0] == "go"
}

func (w *workerExecutionService) executeObservationTestImpact(ctx context.Context, a ObservationAction) ObservationActionResult {
	status, receipt, reason := "blocked", "", ""
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		reason = err.Error()
	} else {
		scope, scopeErr := w.inspectTestImpactScope()
		if scopeErr != nil {
			reason = scopeErr.Error()
		} else if scope.status == "not_applicable" {
			reason = "observation verification requires a canonical repository test plan"
		} else {
			status, receipt, reason = w.runTestImpact(ctx, identity.TaskID, scope)
		}
	}
	detail := "test-impact=" + status
	if receipt != "" {
		detail += " receipt=" + receipt
	}
	if reason != "" {
		detail += "; " + reason
	}
	var result ObservationActionResult
	if status == "passed" {
		result = coderloop.NewObservationActionResult(a.Action, a.Target, detail, nil)
	} else {
		result = coderloop.NewObservationActionResult(a.Action, a.Target, "", fmt.Errorf("%s", detail))
	}
	result.TestStatus, result.TestReceipt = status, receipt
	return result
}

// observationReadFiles is the positive argument grammar for process-based reads.
// It identifies ALL input files, including grep pattern files; it never treats
// option values as ordinary operands or admits implicit stdin/recursive reads.
func observationReadFiles(argv []string) ([]string, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("observation read requires a program")
	}
	program := argv[0]
	booleanFlags, valueFlags := "", ""
	longFlags := map[string]string{}
	switch program {
	case "cat":
		booleanFlags = "AbeEnstTuv"
		longFlags = map[string]string{"show-all": "A", "number-nonblank": "b", "show-ends": "E", "number": "n", "squeeze-blank": "s", "show-tabs": "T", "show-nonprinting": "v"}
	case "head", "tail":
		booleanFlags, valueFlags = "qv", "nc"
		longFlags = map[string]string{"quiet": "q", "silent": "q", "verbose": "v", "lines": "n", "bytes": "c"}
	case "wc":
		booleanFlags = "cmlLw"
		longFlags = map[string]string{"bytes": "c", "chars": "m", "lines": "l", "max-line-length": "L", "words": "w"}
	case "grep":
		booleanFlags, valueFlags = "EFGivwxclLnbHhoqIs", "efmABC"
		longFlags = map[string]string{"extended-regexp": "E", "fixed-strings": "F", "basic-regexp": "G", "ignore-case": "i", "invert-match": "v", "word-regexp": "w", "line-regexp": "x", "count": "c", "files-with-matches": "l", "files-without-match": "L", "line-number": "n", "byte-offset": "b", "with-filename": "H", "no-filename": "h", "only-matching": "o", "quiet": "q", "silent": "q", "no-messages": "s", "regexp": "e", "file": "f", "max-count": "m", "after-context": "A", "before-context": "B", "context": "C"}
	default:
		return nil, fmt.Errorf("unsupported observation read program: %s", program)
	}
	var operands, files []string
	explicitPattern, ended := false, false
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if !ended && arg == "--" {
			ended = true
			continue
		}
		if ended || !strings.HasPrefix(arg, "-") || arg == "-" {
			operands = append(operands, arg)
			continue
		}
		flags, supplied, hasValue := strings.TrimPrefix(arg, "-"), "", false
		if strings.HasPrefix(arg, "--") {
			name, value, assigned := strings.Cut(arg[2:], "=")
			var ok bool
			flags, ok = longFlags[name]
			if !ok {
				return nil, fmt.Errorf("unknown or unconfined observation read option: %s", arg)
			}
			supplied, hasValue = value, assigned
		}
		for j := 0; j < len(flags); j++ {
			flag := flags[j]
			if strings.ContainsRune(booleanFlags, rune(flag)) {
				if hasValue {
					return nil, fmt.Errorf("unexpected observation read option value: %s", arg)
				}
				continue
			}
			if !strings.ContainsRune(valueFlags, rune(flag)) {
				return nil, fmt.Errorf("unknown or unconfined observation read option: %s", arg)
			}
			value := supplied
			if !hasValue {
				if j+1 < len(flags) {
					value = flags[j+1:]
				} else {
					i++
					if i == len(argv) {
						return nil, fmt.Errorf("observation read option requires a value: %s", arg)
					}
					value = argv[i]
				}
			}
			switch {
			case program == "grep" && flag == 'e':
				explicitPattern = true
			case program == "grep" && flag == 'f':
				explicitPattern = true
				files = append(files, value)
			default:
				number := value
				if (program == "head" || program == "tail") && len(number) > 0 && (number[0] == '+' || number[0] == '-') {
					number = number[1:]
				}
				if number == "" {
					return nil, fmt.Errorf("invalid observation read count: %q", value)
				}
				for _, r := range number {
					if r < '0' || r > '9' {
						return nil, fmt.Errorf("invalid observation read count: %q", value)
					}
				}
				if _, err := strconv.ParseUint(number, 10, 63); err != nil {
					return nil, fmt.Errorf("invalid observation read count: %q", value)
				}
			}
			break // Value consumes the rest of a combined short-option token.
		}
	}
	if program == "grep" && !explicitPattern {
		if len(operands) == 0 {
			return nil, fmt.Errorf("observation grep requires a pattern")
		}
		operands = operands[1:]
	}
	if len(operands) == 0 {
		return nil, fmt.Errorf("observation read requires explicit input files")
	}
	files = append(files, operands...)
	for _, file := range files {
		if file == "" || file == "-" {
			return nil, fmt.Errorf("observation read does not admit empty or stdin file input")
		}
	}
	return files, nil
}

func (w *workerExecutionService) validateObservationGitWorkspace(ctx context.Context) error {
	workspace := w.config.Workspace
	if strings.TrimSpace(workspace) == "" {
		return fmt.Errorf("Git observation workspace is required")
	}
	root, err := os.Stat(workspace)
	if err != nil || !root.IsDir() {
		return fmt.Errorf("Git observation workspace is unavailable")
	}
	marker, err := os.Stat(filepath.Join(workspace, ".git"))
	if err != nil || (!marker.IsDir() && !marker.Mode().IsRegular()) {
		return fmt.Errorf("Git observation workspace must be a repository root")
	}
	top, _, err := runWorkerCommand(ctx, workspace, "git", "--no-pager", "--no-optional-locks", "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("Git observation workspace resolution failed: %w", err)
	}
	// Git emits exactly one absolute top-level path followed by a newline.
	// Stat/SameFile compares the actual directory, including workspace aliases.
	top = strings.TrimSuffix(strings.TrimSuffix(top, "\n"), "\r")
	if !filepath.IsAbs(top) {
		return fmt.Errorf("Git observation workspace returned an invalid root")
	}
	effective, err := os.Stat(top)
	if err != nil || !os.SameFile(root, effective) {
		return fmt.Errorf("Git observation workspace differs from effective repository worktree")
	}
	return nil
}
