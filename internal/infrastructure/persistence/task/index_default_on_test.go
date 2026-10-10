//go:build taskindexdefault

package task

import "testing"

func TestNewJSONLStoreOpensWithIndexUnderTheTestTag(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, ok := store.IndexStats(); !ok {
		t.Fatal("the taskindexdefault tag did not enable the index")
	}
}
