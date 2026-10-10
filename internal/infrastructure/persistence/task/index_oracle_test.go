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
