//go:build !taskindexdefault

package task

import "testing"

func TestNewJSONLStoreOpensWithoutIndexByDefault(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, ok := store.IndexStats(); ok {
		t.Fatal("NewJSONLStore opened an index although the index is opt-in")
	}
}
