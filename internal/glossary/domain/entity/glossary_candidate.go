package entity

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const GlossaryCandidateState = "candidate"

const (
	glossaryCandidateMaxIDLength          = 256
	glossaryCandidateMaxTermLength        = 200
	glossaryCandidateMaxExplanationLength = 4096
	glossaryCandidateMaxSourceURLLength   = 2048
	glossaryCandidateMaxCategoryLength    = 64
	glossaryCandidateMaxProposedByLength  = 128
)

var glossaryCandidateCategoryRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// GlossaryCandidate is model-proposed glossary evidence. It is deliberately
// separate from GlossaryItem: candidates are never part of canonical lookup.
type GlossaryCandidate struct {
	ID          string    `json:"id"`
	Term        string    `json:"term"`
	Explanation string    `json:"explanation"`
	SourceURL   string    `json:"source_url"`
	Category    string    `json:"category"`
	ProposedBy  string    `json:"proposed_by"`
	State       string    `json:"state"`
	CreatedAt   time.Time `json:"created_at"`
}

// ValidateGlossaryCandidate validates the bounded, candidate-only record.
func ValidateGlossaryCandidate(candidate GlossaryCandidate) error {
	if err := validateGlossaryCandidateText(candidate.ID, "id", glossaryCandidateMaxIDLength, true); err != nil {
		return err
	}
	if err := validateGlossaryCandidateText(candidate.Term, "term", glossaryCandidateMaxTermLength, true); err != nil {
		return err
	}
	if err := validateGlossaryCandidateText(candidate.Explanation, "explanation", glossaryCandidateMaxExplanationLength, true); err != nil {
		return err
	}
	if err := validateGlossaryCandidateText(candidate.SourceURL, "source_url", glossaryCandidateMaxSourceURLLength, true); err != nil {
		return err
	}
	parsed, err := url.Parse(candidate.SourceURL)
	if err != nil || !parsed.IsAbs() || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" || parsed.User != nil {
		return errors.New("source_url must be an absolute https URL without userinfo")
	}
	if err := validateGlossaryCandidateText(candidate.Category, "category", glossaryCandidateMaxCategoryLength, true); err != nil {
		return err
	}
	if !glossaryCandidateCategoryRE.MatchString(strings.ToLower(candidate.Category)) {
		return errors.New("category contains unsupported characters")
	}
	if err := validateGlossaryCandidateText(candidate.ProposedBy, "proposed_by", glossaryCandidateMaxProposedByLength, true); err != nil {
		return err
	}
	if candidate.State != GlossaryCandidateState {
		return errors.New("state must be candidate")
	}
	if candidate.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func validateGlossaryCandidateText(value, field string, maxRunes int, required bool) error {
	if !utf8.ValidString(value) {
		return errors.New(field + " must be valid UTF-8")
	}
	trimmed := strings.TrimSpace(value)
	if required && trimmed == "" {
		return errors.New(field + " is required")
	}
	if utf8.RuneCountInString(trimmed) > maxRunes {
		return errors.New(field + " is too long")
	}
	return nil
}
