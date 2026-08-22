package backlog

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestEmbeddedBackfillPackageValidatesCanonicalDatasetAndSpecifications(t *testing.T) {
	pkg, err := LoadBackfillPackage()
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.Items) != 114 || len(pkg.SeedFeatures) != 114 {
		t.Fatalf("canonical counts items=%d seed=%d", len(pkg.Items), len(pkg.SeedFeatures))
	}
	if pkg.DatasetID != CanonicalBackfillDatasetID || pkg.PackageSHA256 != "7fd72429f88ff6eeb4be9e08619087a6c7c7b3038ed1b81a7e0e519dab2fb4af" || pkg.Revision != 1 {
		t.Fatalf("package identity=%+v", pkg)
	}
	local, external := 0, 0
	for _, artifact := range pkg.SpecificationArtifacts {
		if artifact.BodyAvailable {
			local++
			if artifact.Content == "" {
				t.Fatalf("local specification %q has no content", artifact.SpecID)
			}
			hash := sha256.Sum256([]byte(artifact.Content))
			if got := hex.EncodeToString(hash[:]); got != artifact.ContentSHA256 {
				t.Fatalf("specification %q hash=%s want=%s", artifact.SpecID, got, artifact.ContentSHA256)
			}
		} else {
			external++
			if artifact.Content != "" {
				t.Fatalf("external specification %q copied a body", artifact.SpecID)
			}
		}
	}
	if local != 8 || external != 3 {
		t.Fatalf("specification counts local=%d external=%d", local, external)
	}
}

func TestBackfillFixtureAndJSONLRepresentCanonicalOnly(t *testing.T) {
	canonical, err := os.ReadFile("backfill/atlas_backfill_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/atlas_backfill_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, fixture) {
		t.Fatal("test fixture drifted from the canonical dataset")
	}
	file, err := os.Open("backfill/atlas_items_v2.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	count := 0
	featureIDs := map[string]struct{}{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var item BackfillItem
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			t.Fatalf("JSONL line %d: %v", count+1, err)
		}
		if _, exists := featureIDs[item.FeatureID]; exists {
			t.Fatalf("duplicate JSONL feature_id %q", item.FeatureID)
		}
		featureIDs[item.FeatureID] = struct{}{}
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 114 {
		t.Fatalf("JSONL item count=%d want=114", count)
	}
}

func TestBackfillFeatureProjectionUsesCORELifecycleOwner(t *testing.T) {
	pkg, err := LoadBackfillPackage()
	if err != nil {
		t.Fatal(err)
	}
	features := pkg.FeatureMaps()
	if len(features) != 114 {
		t.Fatalf("feature count=%d", len(features))
	}
	for _, feature := range features {
		if feature["owner_module"] != "RenCrow_CORE" {
			t.Fatalf("feature %v lifecycle owner=%v", feature["feature_id"], feature["owner_module"])
		}
	}
}
