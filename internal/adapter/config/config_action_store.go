package config

// ActionStoreConfig selects optional behavior of the canonical Action store
// (workspace state/actions/*.jsonl). The logs stay the only source of truth:
// nothing here changes what is written, so a setting can be turned off again.
type ActionStoreConfig struct {
	Index ActionStoreIndexConfig `yaml:"index"`

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
