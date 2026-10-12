package task

import (
	"os"
	"strconv"
	"testing"
)

func oracleSeedCount(t *testing.T) int {
	t.Helper()
	if raw := os.Getenv("RENCROW_TASKINDEX_ORACLE_SEEDS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			t.Fatalf("RENCROW_TASKINDEX_ORACLE_SEEDS=%q is not a positive integer", raw)
		}
		return n
	}
	if testing.Short() {
		return 2
	}
	return 4
}

// TestIndexedStoreMatchesFullFoldOnRandomOperations is the oracle differential
// test (spec R-2, R-3, R-9): the same reproducible random operation sequence
// goes to a store that folds every record on every transaction and to a store
// with the index; every read result and every error must be identical, across
// restarts as well.
func TestIndexedStoreMatchesFullFoldOnRandomOperations(t *testing.T) {
	steps := 160
	if testing.Short() {
		steps = 80
	}
	if raw := os.Getenv("RENCROW_TASKINDEX_ORACLE_STEPS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			t.Fatalf("RENCROW_TASKINDEX_ORACLE_STEPS=%q is not a positive integer", raw)
		}
		steps = n
	}
	for seed := int64(1); seed <= int64(oracleSeedCount(t)); seed++ {
		seed := seed
		t.Run("seed-"+strconv.FormatInt(seed, 10), func(t *testing.T) {
			world := newOracleWorld(t, seed)
			for i := 0; i < steps; i++ {
				world.step()
			}
			world.compare()
			world.pair.reopen()
			world.compare()
			for i := 0; i < steps/2; i++ {
				world.step()
			}
			world.compare()
			world.pair.reopen()
			world.compare()
			t.Logf("tasks=%d runs=%d outcomes(ok,err)=%v", len(world.tasks), len(world.runs), world.pair.outcomes)
		})
	}
}

// TestPersistedIndexMatchesFullFoldOnRandomOperations is the same oracle
// differential test with the index persisted (spec R-8, R-9, AC-13): the same
// random operations go to the store that folds every record and to a store
// whose index lives in a sidecar, and the latter is restarted over and over in
// every way (graceful, crashed with a lagging sidecar, crashed after a
// checkpoint, sidecar bit-flipped, deleted, truncated). Every observable result
// and every error must match, and after each restart the index must equal a
// build from the log.
func TestPersistedIndexMatchesFullFoldOnRandomOperations(t *testing.T) {
	steps := 150
	if testing.Short() {
		steps = 70
	}
	if raw := os.Getenv("RENCROW_TASKINDEX_ORACLE_STEPS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			t.Fatalf("RENCROW_TASKINDEX_ORACLE_STEPS=%q is not a positive integer", raw)
		}
		steps = n
	}
	for seed := int64(1); seed <= int64(oracleSeedCount(t)); seed++ {
		seed := seed
		t.Run("seed-"+strconv.FormatInt(seed, 10), func(t *testing.T) {
			world := newOracleWorldWith(t, seed, true)
			for i := 0; i < steps; i++ {
				world.step()
				if i%25 == 24 {
					world.pair.reopen()
					world.compareSample(5)
				}
			}
			world.compare()
			world.pair.reopen()
			world.compare()
			if len(world.pair.sources) < 2 || world.pair.sources[sourceSidecar] == 0 {
				t.Errorf("the sequence did not exercise restore and rebuild: %v", world.pair.sources)
			}
			t.Logf("tasks=%d runs=%d reopens=%d sources=%v outcomes(ok,err)=%v", len(world.tasks), len(world.runs), world.pair.reopens, world.pair.sources, world.pair.outcomes)
		})
	}
}

// TestSegmentedIndexMatchesFullFoldOnRandomOperations is the oracle differential
// test with the indexed store writing to segments (spec R-9 with rotation, AC-12):
// the same random operations go to the store that folds every record in one set
// of files and to a store that migrated to layout v2 at its first open, whose
// clock moves a day or two at a time and whose size valve is small, so it closes
// segments all through the sequence; it is stopped and restarted in every way
// (graceful, crashed with a lagging sidecar, sidecar damaged or deleted). Every
// observable result and every error must match, and after each restart the index
// must equal a build from all the segments.
func TestSegmentedIndexMatchesFullFoldOnRandomOperations(t *testing.T) {
	steps := 150
	if testing.Short() {
		steps = 70
	}
	if raw := os.Getenv("RENCROW_TASKINDEX_ORACLE_STEPS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			t.Fatalf("RENCROW_TASKINDEX_ORACLE_STEPS=%q is not a positive integer", raw)
		}
		steps = n
	}
	for seed := int64(1); seed <= int64(oracleSeedCount(t)); seed++ {
		seed := seed
		t.Run("seed-"+strconv.FormatInt(seed, 10), func(t *testing.T) {
			world := newOracleWorldRotating(t, seed, 24<<10)
			for i := 0; i < steps; i++ {
				world.step()
				if i%25 == 24 {
					world.pair.reopen()
					world.compareSample(5)
				}
			}
			world.compare()
			world.pair.reopen()
			world.compare()
			stats, _ := world.pair.indexed.IndexStats()
			if stats.Segments < 4 {
				t.Errorf("the sequence ended with %d segments: rotation was not exercised enough", stats.Segments)
			}
			if len(world.pair.sources) < 2 || world.pair.sources[sourceSidecar] == 0 {
				t.Errorf("the sequence did not exercise restore and rebuild: %v", world.pair.sources)
			}
			t.Logf("tasks=%d runs=%d segments=%d reopens=%d sources=%v outcomes(ok,err)=%v", len(world.tasks), len(world.runs), stats.Segments, world.pair.reopens, world.pair.sources, world.pair.outcomes)
		})
	}
}
