package task

import (
	"bytes"
	"sort"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// bestN keeps the n best items under less as they are offered, so a scan over
// every Task never materializes the candidates it will discard. less must be a
// strict total order, which makes the result independent of the offer order.
// n <= 0 keeps everything.
type bestN[T any] struct {
	n     int
	less  func(a, b T) bool
	items []T // for n > 0: a heap with the worst kept item at index 0
}

func newBestN[T any](n int, less func(a, b T) bool) *bestN[T] {
	return &bestN[T]{n: n, less: less}
}

func (c *bestN[T]) add(item T) {
	if c.n <= 0 {
		c.items = append(c.items, item)
		return
	}
	if len(c.items) < c.n {
		c.items = append(c.items, item)
		c.siftUp(len(c.items) - 1)
		return
	}
	if c.less(item, c.items[0]) {
		c.items[0] = item
		c.siftDown(0)
	}
}

func (c *bestN[T]) siftUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if !c.less(c.items[parent], c.items[i]) {
			return
		}
		c.items[parent], c.items[i] = c.items[i], c.items[parent]
		i = parent
	}
}

func (c *bestN[T]) siftDown(i int) {
	for {
		worst, left, right := i, 2*i+1, 2*i+2
		if left < len(c.items) && c.less(c.items[worst], c.items[left]) {
			worst = left
		}
		if right < len(c.items) && c.less(c.items[worst], c.items[right]) {
			worst = right
		}
		if worst == i {
			return
		}
		c.items[i], c.items[worst] = c.items[worst], c.items[i]
		i = worst
	}
}

// result returns the kept items, best first.
func (c *bestN[T]) result() []T {
	sort.Slice(c.items, func(i, j int) bool { return c.less(c.items[i], c.items[j]) })
	return c.items
}

// overlaySnapshot is a copy of the transaction's pending records, taken so the
// index scan does not hold the transaction's state lock.
type overlaySnapshot struct {
	tasks  map[key16]domaintask.Task
	runs   map[key16]domaintask.Run
	notifs []overlayNotification
}

// taskCand is a Task that matched a ListTasks filter: either a committed Task
// (pos) or a pending one from the transaction (value).
type taskCand struct {
	updated stamp
	key     key16
	pos     linePos
	value   *domaintask.Task
}

func taskCandBefore(a, b taskCand) bool {
	if c := a.updated.compare(b.updated); c != 0 {
		return c > 0 // newest first
	}
	return bytes.Compare(a.key[:], b.key[:]) < 0
}

// collectTasksLocked finds the Tasks matching filter, layering the pending
// Tasks over the committed ones. Caller holds mu (read).
func (ix *taskIndex) collectTasksLocked(filter domaintask.Filter, pending map[key16]domaintask.Task, emit func(taskCand)) {
	var (
		wantStatus, wantRoute, wantModule uint32
		assigneeIDs                       []uint32
		impossible                        bool
	)
	if filter.Status != "" {
		id, ok := ix.enums.lookup(string(filter.Status))
		wantStatus, impossible = id, impossible || !ok
	}
	if filter.Route != "" {
		id, ok := ix.enums.lookup(string(filter.Route))
		wantRoute, impossible = id, impossible || !ok
	}
	if filter.ModuleID != "" {
		id, ok := ix.modules.lookup(filter.ModuleID)
		wantModule, impossible = id, impossible || !ok
	}
	if filter.Assignee != "" {
		assigneeIDs = ix.assignees.equalFold(filter.Assignee)
		impossible = impossible || len(assigneeIDs) == 0
	}
	if !impossible {
		for i := range ix.tasks {
			rec := &ix.tasks[i]
			if filter.Status != "" && uint32(rec.status) != wantStatus {
				continue
			}
			if filter.Route != "" && uint32(rec.route) != wantRoute {
				continue
			}
			if filter.ModuleID != "" && rec.module != wantModule {
				continue
			}
			if filter.Assignee != "" && !containsID(assigneeIDs, rec.assignee) {
				continue
			}
			if _, overridden := pending[rec.key]; overridden {
				continue
			}
			emit(taskCand{updated: rec.updated, key: rec.key, pos: ix.lt.pos(rec.stateTail)})
		}
	}
	for key, value := range pending {
		if !taskMatchesFilter(value, filter) {
			continue
		}
		v := value
		emit(taskCand{updated: toStamp(v.UpdatedAt), key: key, value: &v})
	}
}

func containsID(ids []uint32, id uint32) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

// taskMatchesFilter is the filter ListTasks has always applied.
func taskMatchesFilter(item domaintask.Task, filter domaintask.Filter) bool {
	if filter.Status != "" && item.Status != filter.Status {
		return false
	}
	if filter.ModuleID != "" && item.ModuleID != filter.ModuleID {
		return false
	}
	if filter.Assignee != "" && !strings.EqualFold(item.Assignee, filter.Assignee) {
		return false
	}
	if filter.Route != "" && item.Route != filter.Route {
		return false
	}
	return true
}

// runCand is a Run that matched a ListRuns filter.
type runCand struct {
	started stamp
	key     key16
	pos     linePos
	value   *domaintask.Run
}

func runCandBefore(a, b runCand) bool {
	if c := a.started.compare(b.started); c != 0 {
		return c < 0 // oldest first
	}
	return bytes.Compare(a.key[:], b.key[:]) < 0
}

// collectRunsLocked finds the Runs matching filter, layering pending Runs over
// committed ones. Caller holds mu (read).
func (ix *taskIndex) collectRunsLocked(filter domaintask.RunFilter, taskKey key16, taskKeyValid bool, pending map[key16]domaintask.Run, emit func(runCand)) {
	var wantStatus uint32
	statusPossible := true
	if filter.Status != "" {
		wantStatus, statusPossible = ix.enums.lookup(string(filter.Status))
	}
	if statusPossible {
		add := func(rec *runRec) {
			if filter.Status != "" && uint32(rec.status) != wantStatus {
				return
			}
			if _, overridden := pending[rec.key]; overridden {
				return
			}
			emit(runCand{started: rec.started, key: rec.key, pos: ix.lt.pos(rec.tail)})
		}
		if filter.TaskID != "" {
			if taskKeyValid {
				if slot, ok := ix.taskMap[taskKey]; ok {
					for run := ix.tasks[slot].runHead; run != 0; run = ix.runs[run-1].nextRun {
						add(&ix.runs[run-1])
					}
				}
			}
		} else {
			for i := range ix.runs {
				add(&ix.runs[i])
			}
		}
	}
	for key, value := range pending {
		if filter.TaskID != "" && value.TaskID != filter.TaskID {
			continue
		}
		if filter.Status != "" && value.Status != filter.Status {
			continue
		}
		v := value
		emit(runCand{started: toStamp(v.StartedAt), key: key, value: &v})
	}
}

// notifCand is a notification that passed the interrupt filter. seq keeps the
// log order among notifications that tie on time and Task, which the previous
// stable sort preserved.
type notifCand struct {
	created stamp
	key     key16
	seq     int
	pos     linePos
	value   *domaintask.Notification
}

func notifCandBefore(a, b notifCand) bool {
	if c := a.created.compare(b.created); c != 0 {
		return c > 0 // newest first
	}
	if c := bytes.Compare(a.key[:], b.key[:]); c != 0 {
		return c < 0
	}
	return a.seq < b.seq
}

func (ix *taskIndex) collectNotificationsLocked(interruptOnly bool, pending []overlayNotification, emit func(notifCand)) {
	for i := range ix.notifs {
		rec := &ix.notifs[i]
		if interruptOnly && !rec.interrupt {
			continue
		}
		emit(notifCand{created: rec.created, key: rec.key, seq: i, pos: rec.pos})
	}
	for i := range pending {
		if interruptOnly && !pending[i].value.Interrupt {
			continue
		}
		v := pending[i].value
		emit(notifCand{created: toStamp(v.CreatedAt), key: pending[i].key, seq: len(ix.notifs) + i, value: &v})
	}
}
