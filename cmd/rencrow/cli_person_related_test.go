package main

import (
	"strings"
	"testing"
	"time"

	personrelatedcatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/personrelatedcatalog"
)

func TestParsePersonRelatedCollectBatchArgsDefaults(t *testing.T) {
	opts, err := parsePersonRelatedCollectBatchArgs(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Familiarity != "known" {
		t.Fatalf("expected default familiarity 'known', got %q", opts.Familiarity)
	}
	if len(opts.Categories) != len(personrelatedcatalogapp.CollectionSweepCategories) {
		t.Fatalf("expected default categories %v, got %v", personrelatedcatalogapp.CollectionSweepCategories, opts.Categories)
	}
	if opts.Interval != 500*time.Millisecond {
		t.Fatalf("expected default interval 500ms, got %v", opts.Interval)
	}
	if opts.MaxSteps != 0 || opts.MaxDuration != 0 || opts.LimitPersons != 0 || opts.JSON {
		t.Fatalf("expected unbounded non-JSON defaults, got %+v", opts)
	}
}

func TestParsePersonRelatedCollectBatchArgsExplicit(t *testing.T) {
	opts, err := parsePersonRelatedCollectBatchArgs([]string{
		"--familiarity", "all",
		"--categories", "drama, music",
		"--interval", "2s",
		"--max-steps", "12",
		"--max-duration", "90s",
		"--limit-persons", "3",
		"--json",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Familiarity != "all" {
		t.Fatalf("expected familiarity 'all', got %q", opts.Familiarity)
	}
	if len(opts.Categories) != 2 || opts.Categories[0] != "drama" || opts.Categories[1] != "music" {
		t.Fatalf("expected categories [drama music], got %v", opts.Categories)
	}
	if opts.Interval != 2*time.Second || opts.MaxSteps != 12 || opts.MaxDuration != 90*time.Second || opts.LimitPersons != 3 || !opts.JSON {
		t.Fatalf("unexpected parsed options: %+v", opts)
	}
}

func TestParsePersonRelatedCollectBatchArgsRejectsInvalid(t *testing.T) {
	cases := [][]string{
		{"--familiarity", "seen"},
		{"--categories", "movie"},
		{"--categories", ""},
		{"--interval", "-1s"},
		{"--interval", "fast"},
		{"--max-steps", "-1"},
		{"--max-duration", "soon"},
		{"--limit-persons", "-2"},
		{"--unknown-flag"},
		{"--familiarity"},
	}
	for _, args := range cases {
		if _, err := parsePersonRelatedCollectBatchArgs(args); err == nil {
			t.Fatalf("expected error for args %v", args)
		}
	}
}

func TestPersonRelatedCollectBatchUsageWithoutSubcommand(t *testing.T) {
	var out, errOut strings.Builder
	code := runPersonRelatedCollectBatch([]string{"--familiarity", "invalid"}, &out, &errOut)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "--familiarity") {
		t.Fatalf("expected flag error message, got %q", errOut.String())
	}
}
