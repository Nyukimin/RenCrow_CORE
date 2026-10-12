package config

import "fmt"

// TaskStoreConfig selects optional behavior of the canonical Task store
// (workspace tasks/*.jsonl). The log stays the only source of truth: nothing
// here changes what is written, so a setting can be turned off again.
type TaskStoreConfig struct {
	Index    TaskStoreIndexConfig    `yaml:"index"`
	Rotation TaskStoreRotationConfig `yaml:"rotation"`
}

// MinTaskStoreRotationSizeValve is the smallest size valve the configuration
// accepts. Below it a store would open a segment every few transactions.
const MinTaskStoreRotationSizeValve int64 = 1 << 20

// TaskStoreRotationConfig controls the daily segments of the Task store (layout
// v2: the files in tasks/ become segment 0 and are frozen, writes go to
// tasks/logs/<YYYYMMDD>-<NN>/).
type TaskStoreRotationConfig struct {
	// Enabled starts a segment per UTC day, and moves a store that is still on
	// the plain layout to segments the first time it is opened with this on: the
	// files in tasks/ are frozen (made read-only, their sizes recorded in
	// tasks/LAYOUT) and writes go to a new segment. The commit time then depends
	// on the size of the current segment instead of on the whole history.
	//
	// Turning it off again does not return the store to the plain layout: it only
	// stops new segments (the store keeps writing to the newest one). Going back
	// is the offline command "rencrow tasks layout concat". An older binary must
	// not be started on a store that has segments. The default is false;
	// switching it on is a deployment decision (see the runbook). It requires
	// task_store.index.enabled and task_store.index.persist.
	Enabled bool `yaml:"enabled"`

	// SizeValveBytes is the size of a segment's log files beyond which the next
	// write starts another segment of the same day. 0 selects the default, 8 MiB.
	SizeValveBytes int64 `yaml:"size_valve_bytes"`
}

// TaskStoreIndexConfig controls the in-memory ID and summary index of the Task
// store.
type TaskStoreIndexConfig struct {
	// Enabled keeps the index. Reads and transactions then touch only the
	// records they name instead of folding the whole history, and the restart
	// recoveries read Tasks and Runs by ID. It is built from the log at open and
	// followed after every commit; a store opened without it reads the same
	// files. The default is false: turning it on is a deployment decision.
	//
	// It applies to the process that owns the Task store: the runtime in the
	// local profile, the storage host in the remote profile (set it in the
	// storage host's configuration; the runtime's value has no effect there).
	Enabled bool `yaml:"enabled"`

	// Persist keeps the index in a sidecar file (index/seg-legacy.tsi next to the
	// log) so that the next start restores it instead of reading the whole log.
	// The writer replaces the sidecar periodically and when it closes. The
	// sidecar is derived data: a missing, damaged, or outdated one only costs a
	// rebuild from the log, and turning this off again leaves the log untouched.
	// It copies the index, so it requires Enabled. The default is false.
	Persist bool `yaml:"persist"`
}

func (c TaskStoreConfig) validate() error {
	if c.Index.Persist && !c.Index.Enabled {
		return fmt.Errorf("task_store.index.persist requires task_store.index.enabled=true")
	}
	if c.Rotation.Enabled && !(c.Index.Enabled && c.Index.Persist) {
		return fmt.Errorf("task_store.rotation.enabled requires task_store.index.enabled=true and task_store.index.persist=true")
	}
	if v := c.Rotation.SizeValveBytes; v != 0 && v < MinTaskStoreRotationSizeValve {
		return fmt.Errorf("task_store.rotation.size_valve_bytes must be 0 (the default) or at least %d", MinTaskStoreRotationSizeValve)
	}
	return nil
}
