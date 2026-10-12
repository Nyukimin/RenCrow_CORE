package config

import "fmt"

// TaskStoreConfig selects optional behavior of the canonical Task store
// (workspace tasks/*.jsonl). The log stays the only source of truth: nothing
// here changes what is written, so a setting can be turned off again.
type TaskStoreConfig struct {
	Index TaskStoreIndexConfig `yaml:"index"`
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
	return nil
}
