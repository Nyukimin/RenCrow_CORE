package developmentmethodology

import (
	"fmt"
	"sort"
	"strings"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
)

// lifecycleTransitions is the one canonical methodology transition table.
// Task and Ledger both use this graph; neither surface keeps a second order
// list or a delivery-only transition engine. A non-terminal lifecycle state
// may terminate explicitly as blocked, failed, or cancelled. Terminal states
// never reopen, and DONE is reachable only after the user-visible verification
// stage.
var lifecycleTransitions = map[TaskState]map[TaskState]bool{
	TaskPending: {
		TaskReady:     true,
		TaskBlocked:   true,
		TaskFailed:    true,
		TaskCancelled: true,
	},
	TaskReady: {
		TaskAssigned:  true,
		TaskBlocked:   true,
		TaskFailed:    true,
		TaskCancelled: true,
	},
	TaskAssigned: {
		TaskRedVerified: true,
		TaskBlocked:     true,
		TaskFailed:      true,
		TaskCancelled:   true,
	},
	TaskRedVerified: {
		TaskGreenVerified: true,
		TaskBlocked:       true,
		TaskFailed:        true,
		TaskCancelled:     true,
	},
	TaskGreenVerified: {
		TaskRefactored: true,
		TaskBlocked:    true,
		TaskFailed:     true,
		TaskCancelled:  true,
	},
	TaskRefactored: {
		TaskReviewed:  true,
		TaskBlocked:   true,
		TaskFailed:    true,
		TaskCancelled: true,
	},
	TaskReviewed: {
		TaskDone:      true,
		TaskBlocked:   true,
		TaskFailed:    true,
		TaskCancelled: true,
	},
	TaskDone:      {},
	TaskBlocked:   {},
	TaskFailed:    {},
	TaskCancelled: {},
}

// TaskTransitionTable returns a copy so callers can inspect the central
// table without being able to mutate the domain rule.
func TaskTransitionTable() map[TaskState][]TaskState {
	out := make(map[TaskState][]TaskState, len(lifecycleTransitions))
	for from, targets := range lifecycleTransitions {
		out[from] = []TaskState{}
		for to := range targets {
			out[from] = append(out[from], to)
		}
		sort.Slice(out[from], func(left, right int) bool { return out[from][left] < out[from][right] })
	}
	return out
}

func validTaskState(value TaskState) bool {
	_, ok := lifecycleTransitions[value]
	return ok
}

func normalizeTaskState(value TaskState) TaskState {
	if strings.TrimSpace(string(value)) == "" {
		return TaskPending
	}
	return TaskState(strings.ToUpper(strings.TrimSpace(string(value))))
}

// CanTransitionTask answers whether one adjacent lifecycle transition is
// allowed. Repeating the current state is idempotent and therefore allowed.
func CanTransitionTask(from, to TaskState) bool {
	from = normalizeTaskState(from)
	to = normalizeTaskState(to)
	if !validTaskState(from) || !validTaskState(to) {
		return false
	}
	if from == to {
		return true
	}
	return lifecycleTransitions[from][to]
}

// CanTransitionLedger is the Ledger-facing name for the same canonical
// transition function used by Task.
func CanTransitionLedger(from, to string) bool {
	return CanTransitionTask(TaskState(from), TaskState(to))
}

// ValidateLedgerTransition validates one adjacent Ledger lifecycle change
// against the same graph as ValidateTaskTransition.
func ValidateLedgerTransition(from, to string) error {
	fromState, toState := TaskState(from), TaskState(to)
	if err := ValidateTaskTransition(fromState, toState); err != nil {
		return fmt.Errorf("ledger transition: %w", err)
	}
	return nil
}

func ValidateTaskTransition(from, to TaskState) error {
	from = normalizeTaskState(from)
	to = normalizeTaskState(to)
	if !validTaskState(from) || !validTaskState(to) {
		return fmt.Errorf("%w: unknown task state %q -> %q", ErrInvalidState, from, to)
	}
	if !CanTransitionTask(from, to) {
		return fmt.Errorf("%w: task %s -> %s", ErrInvalidTransition, from, to)
	}
	return nil
}

// TransitionTask applies one state transition and derives the terminal
// outcome for terminal task states. It performs no I/O and does not assign
// Implementation Unit or WIP ownership.
func TransitionTask(task Task, target TaskState) (Task, error) {
	from := normalizeTaskState(task.State)
	target = normalizeTaskState(target)
	if err := ValidateTaskTransition(from, target); err != nil {
		return task, err
	}
	if task.TaskID == "" {
		return task, fmt.Errorf("task_id is required")
	}
	if task.PlanID == "" {
		return task, fmt.Errorf("plan_id is required")
	}
	task.State = target
	if target.IsTerminal() {
		task.TerminalOutcome = outcomeForTaskState(target)
	} else {
		task.TerminalOutcome = ""
	}
	return task, nil
}

func outcomeForTaskState(state TaskState) TerminalOutcome {
	switch state {
	case TaskDone:
		return OutcomeOK
	case TaskBlocked:
		return OutcomeBlocked
	case TaskFailed:
		return OutcomeFailed
	case TaskCancelled:
		return OutcomeCancelled
	default:
		return ""
	}
}

func ValidateTerminalOutcome(outcome TerminalOutcome) error {
	if !outcome.IsTerminal() {
		return fmt.Errorf("%w: %q", ErrTerminalOutcomeRequired, outcome)
	}
	return nil
}

func IsTerminalOutcome(outcome TerminalOutcome) bool { return outcome.IsTerminal() }

// ClassifyWork validates the explicit intake classification. The domain does
// not infer architectural impact from model output or prose.
func ClassifyWork(value string) (WorkClass, error) {
	class := WorkClass(normalized(value))
	switch class {
	case WorkClassSpike, WorkClassBounded, WorkClassArchitectural:
		return class, nil
	default:
		return "", fmt.Errorf("invalid work class %q", value)
	}
}

func ClassifyScope(value string) (WorkClass, error) { return ClassifyWork(value) }

func ValidateWorkClass(value string) error {
	_, err := ClassifyWork(value)
	return err
}

// The following wrappers intentionally delegate to Backlog's owner state
// machine. They provide methodology callers a single import surface without
// copying Atlas's concept/delivery transition rules.
func CanTransitionConcept(from, to string) bool {
	return domainbacklog.ValidateConceptTransition(from, to) == nil
}

func ValidateConceptTransition(from, to string) error {
	return domainbacklog.ValidateConceptTransition(from, to)
}

func CanTransitionDelivery(from, to string) bool {
	return domainbacklog.ValidateDeliveryTransition(from, to) == nil
}

func ValidateDeliveryTransition(from, to string) error {
	return domainbacklog.ValidateDeliveryTransition(from, to)
}

func HasRequiredDeliveryEvidence(target string, refs []domainbacklog.EvidenceRef) bool {
	return domainbacklog.HasRequiredEvidence(target, refs)
}
