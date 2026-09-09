package runmigration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	super "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type cohort struct {
	inventory         Inventory
	tasks             map[core.TaskID]domaintask.Task
	runs              map[core.RunID]domaintask.Run
	canonicalRuns     map[core.RunID]bool
	bindings          map[string]core.RunID
	rawRuns           map[string][]core.RunID
	counts            map[string]int
	queueReferences   map[string]string
	legacyTraceEvents map[core.EventID]legacyTraceRow
}

func strictJSON(b []byte, v any) error {
	if err := uniqueJSON(json.NewDecoder(bytes.NewReader(b)), 0); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return errors.New("record schema is invalid")
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return errors.New("trailing record data")
	}
	return nil
}

// DecodeJSON is the command adapter's bounded-schema decoder. It shares the
// duplicate-key rejection used for snapshot records.
func DecodeJSON(b []byte, v any) error { return strictJSON(b, v) }

// Duplicate keys are ambiguous even when encoding/json would accept the last
// value. Reject them at every depth before interpreting identity fields.
func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 128 {
		return errors.New("JSON nesting exceeds bound")
	}
	t, err := d.Token()
	if err != nil {
		return errors.New("invalid JSON")
	}
	open, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	if open != '{' && open != '[' {
		return errors.New("invalid JSON container")
	}
	seen := map[string]bool{}
	for d.More() {
		if open == '{' {
			key, e := d.Token()
			if e != nil {
				return errors.New("invalid JSON key")
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return errors.New("duplicate JSON key")
			}
			seen[s] = true
		}
		if e := uniqueJSON(d, depth+1); e != nil {
			return e
		}
	}
	_, err = d.Token()
	return err
}

func jsonLines(b []byte) [][]byte {
	var out [][]byte
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) > 0 {
			out = append(out, line)
		}
	}
	return out
}
func marshalLine(v any) []byte { b, _ := json.Marshal(v); return append(b, '\n') }

func buildCohort(ctx context.Context, inventory Inventory, input map[string][]byte) (map[string][]byte, map[string]int, error) {
	c := &cohort{inventory: inventory, tasks: map[core.TaskID]domaintask.Task{}, runs: map[core.RunID]domaintask.Run{}, canonicalRuns: map[core.RunID]bool{}, bindings: map[string]core.RunID{}, rawRuns: map[string][]core.RunID{}, counts: map[string]int{}}
	data := func(role string) []byte { return input[inventory.Roles[role]] }
	for _, line := range jsonLines(data("tasks")) {
		var t domaintask.Task
		if e := strictJSON(line, &t); e != nil {
			return nil, nil, e
		}
		if e := t.Validate(); e != nil {
			return nil, nil, errors.New("invalid source Task")
		}
		if prev, ok := c.tasks[t.TaskID]; ok && t.UpdatedAt.Before(prev.UpdatedAt) {
			return nil, nil, errors.New("Task history goes backwards")
		}
		c.tasks[t.TaskID] = t
	}
	for _, line := range jsonLines(data("runs")) {
		var r domaintask.Run
		if e := strictJSON(line, &r); e != nil {
			return nil, nil, e
		}
		if e := r.Validate(); e != nil {
			return nil, nil, errors.New("invalid source Run")
		}
		actor := strings.ToLower(r.Assignee)
		if e := validateMigrationActor(actor); e != nil {
			return nil, nil, errors.New("unknown source Run actor")
		}
		if _, ok := c.tasks[r.TaskID]; !ok {
			return nil, nil, errors.New("orphan source Run")
		}
		if prev, ok := c.runs[r.RunID]; ok {
			if prev.TaskID != r.TaskID || prev.Assignee != r.Assignee || prev.StartReason != r.StartReason || prev.StartCheckpointSHA256 != r.StartCheckpointSHA256 || !prev.StartedAt.Equal(r.StartedAt) || prev.WriterGeneration != r.WriterGeneration || !domaintask.CanRunTransition(prev.Status, r.Status) {
				return nil, nil, errors.New("invalid source Run history")
			}
			if domaintask.IsRunTerminal(prev.Status) && !bytes.Equal(marshalLine(prev), marshalLine(r)) {
				return nil, nil, errors.New("closed source Run changed")
			}
		}
		c.runs[r.RunID] = r
		c.canonicalRuns[r.RunID] = true
	}
	for id, r := range c.runs {
		if strings.EqualFold(r.Assignee, "lumina") && r.Status == domaintask.RunStatusRunning {
			return nil, nil, errors.New("historical Lumina cannot be an active execution")
		}
		c.rawRuns[string(id)] = []core.RunID{id}
	}
	outputs := map[string][]byte{}
	// The canonical reader initializes this empty coordination file. Include it
	// in the offline cohort so read-only owner validation preserves no-op hashes.
	outputs["tasks/.jsonlbatch.lock"] = []byte{}
	// Projection roots are discovered before references are rewritten. Cross-
	// owner references may join only one exact source value, never a fuzzy match.
	databases, e := c.readDatabases(ctx, input)
	if e != nil {
		return nil, nil, e
	}
	defer func() {
		for _, db := range databases {
			db.close()
		}
	}()
	for _, db := range databases {
		if e := c.discoverDatabase(ctx, db); e != nil {
			return nil, nil, e
		}
	}
	if e := c.restoreEventRunHistory(ctx, databases); e != nil {
		return nil, nil, e
	}
	if e := c.restoreLegacyTraceHistory(ctx, databases, input); e != nil {
		return nil, nil, e
	}
	c.queueReferences, e = readLegacyQueueReferences(ctx, databases)
	if e != nil {
		return nil, nil, e
	}
	c.counts["legacy_queue_bindings"] = len(c.queueReferences)
	idle, e := c.readIdle(input)
	if e != nil {
		return nil, nil, e
	}
	// Existing Task state is retained, except a stopped snapshot cannot keep an
	// executable running Run. The interruption is explicit and deterministic.
	active := map[core.TaskID]bool{}
	for id, r := range c.runs {
		if r.Status == domaintask.RunStatusRunning {
			if active[r.TaskID] {
				return nil, nil, errors.New("multiple active source Runs")
			}
			active[r.TaskID] = true
			if inventory.SnapshotAt.Before(r.StartedAt) {
				return nil, nil, errors.New("snapshot predates Run")
			}
			r.Status = domaintask.RunStatusInterrupted
			r.CompletedAt = &inventory.SnapshotAt
			r.Summary = "interrupted at migration snapshot"
			c.runs[id] = r
			t := c.tasks[r.TaskID]
			t.Status = domaintask.StatusWaiting
			t.WaitingReason = "resume from migrated checkpoint"
			t.UpdatedAt = inventory.SnapshotAt
			c.tasks[t.TaskID] = t
		}
	}
	for _, db := range databases {
		b, e := c.transformDatabase(ctx, db)
		if e != nil {
			return nil, nil, e
		}
		outputs[db.role+".sqlite"] = b
	}
	for role, b := range idle {
		outputs[role+idleExtension(role)] = b
	}
	if err := c.validateTaskGraph(); err != nil {
		return nil, nil, err
	}
	taskIDs := make([]string, 0, len(c.tasks))
	for id := range c.tasks {
		taskIDs = append(taskIDs, string(id))
	}
	sort.Strings(taskIDs)
	for _, id := range taskIDs {
		t := c.tasks[core.TaskID(id)]
		if t.ParentTaskID != "" {
			if _, ok := c.tasks[t.ParentTaskID]; !ok {
				return nil, nil, errors.New("orphan parent Task")
			}
		}
		for _, dep := range t.DependencyTaskIDs {
			if _, ok := c.tasks[dep]; !ok {
				return nil, nil, errors.New("orphan dependency Task")
			}
		}
		outputs["tasks/task_state.jsonl"] = append(outputs["tasks/task_state.jsonl"], marshalLine(t)...)
	}
	runIDs := make([]string, 0, len(c.runs))
	for id := range c.runs {
		runIDs = append(runIDs, string(id))
	}
	sort.Strings(runIDs)
	for _, id := range runIDs {
		r := c.runs[core.RunID(id)]
		r.WriterGeneration = 0
		outputs["tasks/task_run.jsonl"] = append(outputs["tasks/task_run.jsonl"], marshalLine(r)...)
	}
	for _, role := range []string{"contexts", "notifications"} {
		var target string
		if role == "contexts" {
			target = "task_context.jsonl"
		} else {
			target = "task_notifications.jsonl"
		}
		for _, line := range jsonLines(data(role)) {
			var taskID core.TaskID
			if role == "contexts" {
				var v domaintask.SharedRoleContext
				if e := strictJSON(line, &v); e != nil {
					return nil, nil, e
				}
				taskID = v.TaskID
			} else {
				var v domaintask.Notification
				if e := strictJSON(line, &v); e != nil {
					return nil, nil, e
				}
				taskID = v.TaskID
			}
			if _, ok := c.tasks[taskID]; !ok {
				return nil, nil, errors.New("orphan Task projection")
			}
		}
		outputs["tasks/"+target] = data(role)
	}
	for _, f := range []string{"task_state.jsonl", "task_run.jsonl"} {
		if _, ok := outputs["tasks/"+f]; !ok {
			outputs["tasks/"+f] = []byte{}
		}
	}
	c.counts["tasks"] = len(c.tasks)
	c.counts["runs"] = len(c.runs)
	return outputs, c.counts, nil
}

func (c *cohort) validateTaskGraph() error {
	state := map[core.TaskID]int{}
	var visit func(core.TaskID) error
	visit = func(id core.TaskID) error {
		if state[id] == 1 {
			return errors.New("Task ownership graph cycle")
		}
		if state[id] == 2 {
			return nil
		}
		t, ok := c.tasks[id]
		if !ok {
			return errors.New("orphan Task reference")
		}
		if t.SupersedesTaskID != "" {
			if _, exists := c.tasks[t.SupersedesTaskID]; !exists {
				return errors.New("orphan superseded Task")
			}
		}
		state[id] = 1
		deps := append([]core.TaskID(nil), t.DependencyTaskIDs...)
		if t.ParentTaskID != "" {
			deps = append(deps, t.ParentTaskID)
		}
		for _, dep := range deps {
			if e := visit(dep); e != nil {
				return e
			}
		}
		state[id] = 2
		return nil
	}
	for id := range c.tasks {
		if e := visit(id); e != nil {
			return e
		}
	}
	return nil
}

func validateMigrationActor(actor string) error {
	if actor == "lumina" {
		return nil // User-confirmed historical attribution, never execution admission.
	}
	return super.ValidateActorID(actor)
}

func (c *cohort) bind(owner, field, value, taskValue, actor, status string, started, ended time.Time) (core.RunID, error) {
	if value == "" || started.IsZero() || actor == "" {
		return "", fmt.Errorf("%s execution lacks source identity, time or actor", owner)
	}
	if validateMigrationActor(actor) != nil {
		return "", errors.New("unknown historical actor")
	}
	key := owner + "\x00" + field + "\x00" + value
	if _, ok := c.bindings[key]; ok {
		return "", errors.New("duplicate execution root")
	}
	id := core.RunID(value)
	if old, ok := c.runs[id]; ok && c.canonicalRuns[id] {
		if taskValue != "" && string(old.TaskID) != taskValue || !strings.EqualFold(old.Assignee, actor) {
			return "", errors.New("conflicting canonical Run ownership")
		}
		c.bindings[key] = id
		return id, nil
	}
	// Syntactic validity is not proof of issuance by the canonical Task owner.
	// Only a Run present in that source stream is preserved verbatim.
	mapped, e := core.NewMigrationID(core.CanonicalRunID, owner, field, value)
	if e != nil {
		return "", e
	}
	id = core.RunID(mapped)
	if _, exists := c.runs[id]; exists {
		return "", errors.New("migration Run identity collision")
	}
	taskID := core.TaskID(taskValue)
	if taskValue == "" {
		mapped, e := core.NewMigrationID(core.CanonicalTaskID, owner, field, value)
		if e != nil {
			return "", e
		}
		taskID = core.TaskID(mapped)
	} else if taskID.Validate() != nil {
		return "", errors.New("invalid source Task identity")
	} else if _, exists := c.tasks[taskID]; !exists {
		return "", errors.New("orphan canonical Task reference")
	}
	rs, e := migrationRunStatus(status)
	if e != nil {
		return "", e
	}
	if rs != domaintask.RunStatusRunning && ended.IsZero() {
		return "", errors.New("terminal source execution has no completion time")
	}
	if actor == "lumina" && rs == domaintask.RunStatusRunning {
		return "", errors.New("historical Lumina cannot be an active execution")
	}
	assignee := actor
	if actor != "lumina" {
		assignee = strings.ToUpper(actor[:1]) + actor[1:]
	}
	r := domaintask.Run{RunID: id, TaskID: taskID, StartReason: domaintask.RunStartReasonFirst, Assignee: assignee, Status: rs, StartedAt: started}
	if rs != domaintask.RunStatusRunning {
		r.CompletedAt = &ended
	}
	if e := r.Validate(); e != nil {
		return "", e
	}
	if t, ok := c.tasks[taskID]; ok {
		if !strings.EqualFold(t.Assignee, actor) {
			return "", errors.New("Task actor mismatch")
		}
		for _, other := range c.runs {
			if other.TaskID == taskID {
				return "", errors.New("legacy execution order for existing Task is ambiguous")
			}
		}
	} else {
		ts := domaintask.Status(rs)
		if rs == domaintask.RunStatusInterrupted || rs == domaintask.RunStatusReassigned {
			ts = domaintask.StatusWaiting
		}
		updated := started
		if !ended.IsZero() {
			updated = ended
		}
		t := domaintask.Task{TaskID: taskID, Title: "Migrated " + owner + " execution", Route: domaintask.RouteGeneral, Assignee: assignee, Status: ts, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptSilent, CreatedAt: started, UpdatedAt: updated, StartedAt: &started}
		if domaintask.IsTerminal(ts) {
			t.FinishedAt = &ended
		}
		if ts == domaintask.StatusWaiting {
			t.WaitingReason = "historical execution checkpoint"
		}
		if e := t.Validate(); e != nil {
			return "", e
		}
		c.tasks[taskID] = t
		c.counts["created_tasks"]++
	}
	c.runs[id] = r
	c.bindings[key] = id
	c.rawRuns[value] = append(c.rawRuns[value], id)
	if owner == "superagent" && field == "run_id" {
		// Step02's documented Event converter used trace_event/run_id. This
		// exact deterministic source binding exists only inside this migration.
		alias, e := core.NewMigrationID(core.CanonicalRunID, "trace_event", "run_id", value)
		if e != nil {
			return "", e
		}
		c.rawRuns[alias] = append(c.rawRuns[alias], id)
	}
	c.counts["mapped_runs"]++
	return id, nil
}

func migrationRunStatus(s string) (domaintask.RunStatus, error) {
	switch s {
	case "completed", "ready":
		return domaintask.RunStatusSucceeded, nil
	case "paused":
		return domaintask.RunStatusWaiting, nil
	case "needs_repair":
		return domaintask.RunStatusFailed, nil
	}
	r := domaintask.RunStatus(s)
	if !domaintask.ValidRunStatus(r) {
		return "", errors.New("unknown source execution status")
	}
	return r, nil
}

func (c *cohort) resolve(owner, field, value string) (domaintask.Run, error) {
	if id, ok := c.bindings[owner+"\x00"+field+"\x00"+value]; ok {
		return c.runs[id], nil
	}
	ids := c.rawRuns[value]
	if len(ids) != 1 {
		return domaintask.Run{}, errors.New("orphan or ambiguous Run reference")
	}
	return c.runs[ids[0]], nil
}
