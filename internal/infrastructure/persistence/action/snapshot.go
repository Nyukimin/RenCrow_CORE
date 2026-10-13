package action

import (
	"fmt"
	"reflect"
	"sort"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// The snapshot is the decoded content of the two logs, kept in memory, with the
// lookups the owner API needs. The logs stay the only source of truth: the
// snapshot is built from them by the same folds the full read runs (fold.go) and
// is changed only by applying bytes that were appended to them, so it is always
// the decoding of some prefix of the logs. What that prefix is (applied) is what
// every freshness check compares with the files.
//
// On layout v2 the logs are those of every segment, read in segment order and
// folded as one: a record in a later segment replaces the version in an earlier
// one exactly as a later line replaces an earlier one in a single file. Only the
// current segment is ever appended to, so only its files are compared with the
// snapshot after the open.

// Log files, in the order their bytes are applied: an Attempt line may name an
// Action line of the same commit, so Actions go first whatever order the commit
// listed its files in.
const (
	fileAction = iota
	fileAttempt
	fileCount
)

var logFilenames = [fileCount]string{fileAction: actionFilename, fileAttempt: attemptFilename}

// snapState is one version of the snapshot's content. It is replaced as a whole
// by a rebuild and changed in place by applying a commit; both happen under the
// snapshot's write lock.
type snapState struct {
	actions  *actionFold
	attempts *attemptFold

	// byRun lists the Actions of a Run in the order their first line was applied.
	// A Run has one Action almost always, so the slices are tiny.
	byRun map[modulecore.RunID][]modulecore.ActionID
	// open is the set of Actions whose latest status is open.
	open map[modulecore.ActionID]struct{}
	// attemptsOf lists the Attempts of an Action in the order of their first line.
	attemptsOf map[modulecore.ActionID][]modulecore.AttemptID

	// applied is how many bytes of each log of the current segment the content
	// decodes, closed is how many bytes of each log the closed segments hold (all
	// of which it decodes), and records how many records of each it has folded in
	// all. On the plain layout there is one segment and closed is zero.
	applied [fileCount]int64
	closed  [fileCount]int64
	records [fileCount]int64

	// enums holds one copy of each value of the enumerated fields (Kind, Status,
	// StartReason), so that the content keeps one string for "succeeded" and not
	// one per record. names does the same for Action names, of which there are a
	// few hundred at most except for names that are different every time (a
	// Playback receipt names its chunk), which must not fill the table.
	enums map[string]string
	names map[string]string
}

// maxEnumValues and maxNameValues bound the two tables of shared strings, so that
// a value that is different every time cannot grow one without end.
const (
	maxEnumValues = 256
	maxNameValues = 1024
)

func newSnapState(actionHint, attemptHint int) *snapState {
	return &snapState{
		actions:    newActionFold(actionHint),
		attempts:   newAttemptFold(attemptHint),
		byRun:      make(map[modulecore.RunID][]modulecore.ActionID, actionHint),
		open:       make(map[modulecore.ActionID]struct{}),
		attemptsOf: make(map[modulecore.ActionID][]modulecore.AttemptID, actionHint),
		enums:      make(map[string]string),
		names:      make(map[string]string),
	}
}

// enum returns the shared copy of an enumerated value.
func (st *snapState) enum(value string) string {
	return shareFrom(st.enums, maxEnumValues, value)
}

// name returns the shared copy of an Action name.
func (st *snapState) name(value string) string {
	return shareFrom(st.names, maxNameValues, value)
}

func shareFrom(table map[string]string, limit int, value string) string {
	if shared, ok := table[value]; ok {
		return shared
	}
	if len(table) < limit {
		table[value] = value
	}
	return value
}

// shareString makes a string of a record that equals the one the content already
// holds that same string, so a record decoded from a line does not keep a second
// copy of an ID it names. Only an equal value is replaced: a record that differs is
// left as decoded and the fold refuses it as it always did.
func shareString[T ~string](value *T, known T) {
	if *value == known {
		*value = known
	}
}

// applyAction folds one Action line into the content and its lookups.
func (st *snapState) applyAction(item domainaction.Action) error {
	prev, existed := st.actions.latest[item.ActionID]
	if existed {
		shareString(&item.ActionID, prev.ActionID)
		shareString(&item.TaskID, prev.TaskID)
		shareString(&item.RunID, prev.RunID)
		shareString(&item.CurrentAttemptID, prev.CurrentAttemptID)
	}
	item.Kind = domainaction.Kind(st.enum(string(item.Kind)))
	item.Name = st.name(item.Name)
	item.Status = domainaction.Status(st.enum(string(item.Status)))
	if err := st.actions.apply(int(st.records[fileAction]), item); err != nil {
		return err
	}
	st.records[fileAction]++
	if !existed {
		st.byRun[item.RunID] = append(st.byRun[item.RunID], item.ActionID)
	}
	if item.IsOpen() {
		st.open[item.ActionID] = struct{}{}
	} else {
		delete(st.open, item.ActionID)
	}
	return nil
}

// applyAttempt folds one Attempt line into the content and its lookups.
func (st *snapState) applyAttempt(item domainaction.Attempt) error {
	prev, existed := st.attempts.latest[item.AttemptID]
	if existed {
		shareString(&item.AttemptID, prev.AttemptID)
		shareString(&item.ActionID, prev.ActionID)
	} else if owner, ok := st.actions.latest[item.ActionID]; ok {
		// An Attempt is written after the Action that names it as current, so the
		// two share their IDs.
		shareString(&item.ActionID, owner.ActionID)
		shareString(&item.AttemptID, owner.CurrentAttemptID)
	}
	item.StartReason = domainaction.AttemptStartReason(st.enum(string(item.StartReason)))
	item.Status = domainaction.AttemptStatus(st.enum(string(item.Status)))
	if err := st.attempts.apply(int(st.records[fileAttempt]), item); err != nil {
		return err
	}
	st.records[fileAttempt]++
	if !existed {
		st.attemptsOf[item.ActionID] = append(st.attemptsOf[item.ActionID], item.AttemptID)
	}
	return nil
}

func (st *snapState) action(id modulecore.ActionID) (domainaction.Action, bool) {
	v, ok := st.actions.latest[id]
	return v, ok
}

func (st *snapState) attempt(id modulecore.AttemptID) (domainaction.Attempt, bool) {
	v, ok := st.attempts.latest[id]
	return v, ok
}

// attemptsOfAction returns the Attempts of an Action. The slice is new; the
// Attempts in it share the snapshot's nested data and must be cloned before they
// leave the package.
func (st *snapState) attemptsOfAction(id modulecore.ActionID) []domainaction.Attempt {
	ids := st.attemptsOf[id]
	out := make([]domainaction.Attempt, 0, len(ids))
	for _, attemptID := range ids {
		if v, ok := st.attempts.latest[attemptID]; ok {
			out = append(out, v)
		}
	}
	return out
}

// actionCandidates returns the Actions a filter can match, or false when no
// lookup narrows it and every Action has to be looked at. The candidates are a
// superset: the caller still applies the whole filter.
func (st *snapState) actionCandidates(filter domainaction.Filter) ([]modulecore.ActionID, bool) {
	switch {
	case filter.RunID != "":
		return st.byRun[filter.RunID], true
	case filter.Status == domainaction.StatusOpen:
		ids := make([]modulecore.ActionID, 0, len(st.open))
		for id := range st.open {
			ids = append(ids, id)
		}
		return ids, true
	}
	return nil, false
}

// matchesActionFilter is the one definition of which Actions a Filter selects.
func matchesActionFilter(item domainaction.Action, filter domainaction.Filter) bool {
	if filter.TaskID != "" && item.TaskID != filter.TaskID {
		return false
	}
	if filter.RunID != "" && item.RunID != filter.RunID {
		return false
	}
	if filter.Status != "" && item.Status != filter.Status {
		return false
	}
	if filter.Kind != "" && item.Kind != filter.Kind {
		return false
	}
	if filter.Name != "" && item.Name != filter.Name {
		return false
	}
	return true
}

// matchesAttemptFilter is the one definition of which Attempts an AttemptFilter
// selects.
func matchesAttemptFilter(item domainaction.Attempt, filter domainaction.AttemptFilter) bool {
	if filter.ActionID != "" && item.ActionID != filter.ActionID {
		return false
	}
	if filter.Status != "" && item.Status != filter.Status {
		return false
	}
	return true
}

// slices returns the content as the two slices the whole-store rule takes.
func (st *snapState) slices() ([]domainaction.Action, []domainaction.Attempt) {
	actions := make([]domainaction.Action, 0, len(st.actions.latest))
	for _, item := range st.actions.latest {
		actions = append(actions, item)
	}
	attempts := make([]domainaction.Attempt, 0, len(st.attempts.latest))
	for _, item := range st.attempts.latest {
		attempts = append(attempts, item)
	}
	return actions, attempts
}

// check compares the content with the result of a full read of the same logs and
// recomputes every lookup from the records to compare it with the stored one. It
// returns one line per kind of difference. The lines carry counts, never record
// content or IDs.
func (st *snapState) check(actions []domainaction.Action, attempts []domainaction.Attempt) []string {
	var problems []string
	if len(actions) != len(st.actions.latest) {
		problems = append(problems, fmt.Sprintf("the snapshot holds %d Actions, the log %d", len(st.actions.latest), len(actions)))
	}
	if len(attempts) != len(st.attempts.latest) {
		problems = append(problems, fmt.Sprintf("the snapshot holds %d Attempts, the log %d", len(st.attempts.latest), len(attempts)))
	}
	differing, missing := 0, 0
	for _, want := range actions {
		got, ok := st.actions.latest[want.ActionID]
		switch {
		case !ok:
			missing++
		case !reflect.DeepEqual(got, want):
			differing++
		}
	}
	if missing > 0 || differing > 0 {
		problems = append(problems, fmt.Sprintf("%d Actions are missing from the snapshot and %d differ in a field", missing, differing))
	}
	differing, missing = 0, 0
	for _, want := range attempts {
		got, ok := st.attempts.latest[want.AttemptID]
		switch {
		case !ok:
			missing++
		case !reflect.DeepEqual(got, want):
			differing++
		}
	}
	if missing > 0 || differing > 0 {
		problems = append(problems, fmt.Sprintf("%d Attempts are missing from the snapshot and %d differ in a field", missing, differing))
	}

	// The lookups, recomputed from the records the snapshot itself holds.
	byRun := map[modulecore.RunID][]modulecore.ActionID{}
	open := map[modulecore.ActionID]struct{}{}
	native := map[[2]string]modulecore.ActionID{}
	for id, item := range st.actions.latest {
		byRun[item.RunID] = append(byRun[item.RunID], id)
		if item.IsOpen() {
			open[id] = struct{}{}
		}
		if isNativeDelegationAction(item) {
			native[[2]string{string(item.TaskID), string(item.RunID)}] = id
		}
	}
	attemptsOf := map[modulecore.ActionID][]modulecore.AttemptID{}
	active := map[modulecore.ActionID]modulecore.AttemptID{}
	for id, item := range st.attempts.latest {
		attemptsOf[item.ActionID] = append(attemptsOf[item.ActionID], id)
		if item.Status == domainaction.AttemptStatusRunning {
			active[item.ActionID] = id
		}
	}
	if !sameIDSets(byRun, st.byRun) {
		problems = append(problems, "the lookup of Actions by Run differs from the records")
	}
	if !sameIDSets(attemptsOf, st.attemptsOf) {
		problems = append(problems, "the lookup of Attempts by Action differs from the records")
	}
	if len(open) != len(st.open) {
		problems = append(problems, "the set of open Actions differs from the records")
	} else {
		for id := range open {
			if _, ok := st.open[id]; !ok {
				problems = append(problems, "the set of open Actions differs from the records")
				break
			}
		}
	}
	if !reflect.DeepEqual(native, st.actions.nativeByTaskRun) {
		problems = append(problems, "the lookup of native delegations differs from the records")
	}
	if !reflect.DeepEqual(active, st.attempts.activeByAction) {
		problems = append(problems, "the lookup of running Attempts differs from the records")
	}
	return problems
}

// sameIDSets compares two index maps as sets per key.
func sameIDSets[K comparable, V ~string](a, b map[K][]V) bool {
	if len(a) != len(b) {
		return false
	}
	for key, left := range a {
		right, ok := b[key]
		if !ok || len(left) != len(right) {
			return false
		}
		x := append([]V(nil), left...)
		y := append([]V(nil), right...)
		sort.Slice(x, func(i, j int) bool { return x[i] < x[j] })
		sort.Slice(y, func(i, j int) bool { return y[i] < y[j] })
		for i := range x {
			if x[i] != y[i] {
				return false
			}
		}
	}
	return true
}
