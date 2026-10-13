package config

import "fmt"

// ActionStoreConfig selects optional behavior of the canonical Action store
// (workspace state/actions/*.jsonl). The logs stay the only source of truth:
// nothing here changes what is written, so a setting can be turned off again.
type ActionStoreConfig struct {
	Index    ActionStoreIndexConfig    `yaml:"index"`
	Rotation ActionStoreRotationConfig `yaml:"rotation"`

	// VerifyRunReferencesAtStart makes the restart recovery check, for every
	// Action in the store, that the Run it names still exists in the Task store,
	// and refuse to start when one does not. That check read the whole Action
	// history at every start and was the reason a single dangling reference could
	// keep the whole process from starting. By default the recovery checks only
	// the Actions it acts on (the open ones, the native delegations and those of
	// a Run that is still running); the same whole-history check is available on
	// demand as "rencrow actions verify --check-runs". The default is false.
	VerifyRunReferencesAtStart bool `yaml:"verify_run_references_at_start"`
}

// ActionStoreIndexConfig controls the in-memory snapshot of the Action store.
type ActionStoreIndexConfig struct {
	// Enabled keeps a decoded snapshot of both logs in memory. Reads and
	// transactions then touch only the records they name instead of decoding and
	// validating the whole history, and the restart recoveries ask for the Actions
	// they need instead of listing all of them. It is built from the logs at open
	// and followed after every commit; a store opened without it reads the same
	// files, and the setting can be removed again at any time. The default is
	// false: turning it on is a deployment decision.
	//
	// It applies to the process that owns the Action store.
	Enabled bool `yaml:"enabled"`
}

// MinActionStoreRotationSizeValve is the smallest size valve the configuration
// accepts. Below it a store would open a segment every few transactions.
const MinActionStoreRotationSizeValve int64 = 1 << 20

// ActionStoreRotationConfig controls the daily segments of the Action store
// (layout v2: the files in state/actions become segment 0 and are frozen, writes
// go to state/actions/logs/<YYYYMMDD>-<NN>/).
type ActionStoreRotationConfig struct {
	// Enabled starts a segment per UTC day, and moves a store that is still on
	// the plain layout to segments the first time it is opened with this on: the
	// files in state/actions are frozen (made read-only, their sizes recorded in
	// state/actions/LAYOUT) and writes go to a new segment. The commit time then
	// depends on the size of the current segment instead of on the whole history.
	//
	// Turning it off again does not return the store to the plain layout: it only
	// stops new segments (the store keeps writing to the newest one). Going back
	// is the offline command "rencrow actions layout concat". An older binary must
	// not be started on a store that has segments. The default is false;
	// switching it on is a deployment decision (see the runbook). It requires
	// action_store.index.enabled.
	Enabled bool `yaml:"enabled"`

	// SizeValveBytes is the size of a segment's logs beyond which the next write
	// starts another segment of the same day. 0 selects the default, 8 MiB.
	SizeValveBytes int64 `yaml:"size_valve_bytes"`
}

func (c ActionStoreConfig) validate() error {
	if c.Rotation.Enabled && !c.Index.Enabled {
		return fmt.Errorf("action_store.rotation.enabled requires action_store.index.enabled=true")
	}
	if v := c.Rotation.SizeValveBytes; v != 0 && v < MinActionStoreRotationSizeValve {
		return fmt.Errorf("action_store.rotation.size_valve_bytes must be 0 (the default) or at least %d", MinActionStoreRotationSizeValve)
	}
	return nil
}
