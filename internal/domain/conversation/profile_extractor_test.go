package conversation

import (
	"errors"
	"strings"
	"testing"
)

func TestProfileExtractionErrorCategoriesAreTypedAndSecretSafe(t *testing.T) {
	secret := errors.New("provider response TOP-SECRET")

	for _, tc := range []struct {
		name string
		got  error
		want ProfileExtractionErrorCode
		ref  error
	}{
		{
			name: "unavailable",
			got:  NewProfileExtractionUnavailableError(secret),
			want: ProfileExtractionErrorUnavailable,
			ref:  ErrProfileExtractorUnavailable,
		},
		{
			name: "invalid",
			got:  NewProfileExtractionInvalidError(secret),
			want: ProfileExtractionErrorInvalid,
			ref:  ErrProfileExtractorInvalid,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !errors.Is(tc.got, tc.ref) {
				t.Fatalf("error=%v is not category %v", tc.got, tc.ref)
			}
			if got := ProfileExtractionErrorCodeOf(tc.got); got != tc.want {
				t.Fatalf("code=%q want %q", got, tc.want)
			}
			if strings.Contains(tc.got.Error(), "TOP-SECRET") {
				t.Fatalf("typed error leaked provider detail: %q", tc.got.Error())
			}
		})
	}
}

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
