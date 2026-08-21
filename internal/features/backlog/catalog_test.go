package backlog

import "testing"

func TestEmbeddedAtlasCatalogHasRequiredFeatureAreas(t *testing.T) {
	catalog, err := LoadAtlasCatalog()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"agents": false, "memory": false, "knowledge": false, "execution": false, "safety": false, "runtime": false, "interaction": false, "operations": false, "ecosystem": false}
	for _, feature := range catalog.Features {
		category, _ := feature["category"].(string)
		if _, ok := want[category]; ok {
			want[category] = true
		}
		if _, exists := feature["delivery_state"]; exists {
			t.Fatalf("static catalog must not freeze runtime delivery state: %#v", feature)
		}
	}
	for category, found := range want {
		if !found {
			t.Fatalf("catalog missing category %q", category)
		}
	}
}
