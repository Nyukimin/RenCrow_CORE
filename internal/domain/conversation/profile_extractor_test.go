package conversation

import "testing"

func TestProfileExtractionResult_HasData_Empty(t *testing.T) {
	r := &ProfileExtractionResult{}
	if r.HasData() {
		t.Error("empty result should not have data")
	}
}

func TestProfileExtractionResult_HasData_EmptyMaps(t *testing.T) {
	r := &ProfileExtractionResult{
		NewPreferences: map[string]string{},
		NewFacts:       []string{},
	}
	if r.HasData() {
		t.Error("result with empty maps should not have data")
	}
}

func TestProfileExtractionResult_HasData_WithPreferences(t *testing.T) {
	r := &ProfileExtractionResult{
		NewPreferences: map[string]string{"lang": "Go"},
	}
	if !r.HasData() {
		t.Error("result with preferences should have data")
	}
}

func TestProfileExtractionResult_HasData_WithFacts(t *testing.T) {
	r := &ProfileExtractionResult{
		NewFacts: []string{"likes Go"},
	}
	if !r.HasData() {
		t.Error("result with facts should have data")
	}
}

func TestProfileExtractionResult_HasData_Both(t *testing.T) {
	r := &ProfileExtractionResult{
		NewPreferences: map[string]string{"lang": "Go"},
		NewFacts:       []string{"developer"},
	}
	if !r.HasData() {
		t.Error("result with both should have data")
	}
}
