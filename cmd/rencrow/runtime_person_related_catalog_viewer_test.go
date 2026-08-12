package main

import (
	"context"
	"testing"

	personrelatedcatalog "github.com/Nyukimin/RenCrow_CORE/internal/application/personrelatedcatalog"
)

func TestRuntimePersonRelatedCatalogLookupByPersonIDUsesExactReadOnlyID(t *testing.T) {
	moviePath := seedRuntimeMovieCatalog(t)
	hobbyPath := seedRuntimeHobbyGraph(t)
	lookup, err := prepareRuntimePersonRelatedCatalogLookup(context.Background(), moviePath, hobbyPath)
	if err != nil {
		t.Fatal(err)
	}

	result, err := lookup.LookupByPersonID(context.Background(), "p1", personrelatedcatalog.CategoryMovie, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].MovieCatalogPersonID != "p1" || result.Items[0].DisplayName != "Heat" {
		t.Fatalf("unexpected exact-id movie result: %+v", result)
	}

	if _, err := lookup.LookupByPersonID(context.Background(), "unknown", personrelatedcatalog.CategoryMovie, 1); err == nil {
		t.Fatal("unknown exact person id must not resolve by name")
	}
}

func TestRuntimePersonRelatedCatalogLookupByPersonIDReadsNonMovieCategoryWithoutCollection(t *testing.T) {
	moviePath := seedRuntimeMovieCatalog(t)
	hobbyPath := seedRuntimeHobbyGraph(t)
	lookup, err := prepareRuntimePersonRelatedCatalogLookup(context.Background(), moviePath, hobbyPath)
	if err != nil {
		t.Fatal(err)
	}
	seedRuntimeHobbyDrama(t, hobbyPath)

	result, err := lookup.LookupByPersonID(context.Background(), "p1", personrelatedcatalog.CategoryDrama, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].MovieCatalogPersonID != "p1" || result.Items[0].DisplayName != "Drama One" || result.Items[0].NameOriginal != "Drama One" {
		t.Fatalf("unexpected exact-id drama result: %+v", result)
	}
}
