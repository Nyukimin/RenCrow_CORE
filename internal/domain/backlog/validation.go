package backlog

import (
	"errors"
	"strings"
)

func ValidateSourceRef(ref SourceRef) error {
	if strings.TrimSpace(ref.Type) == "" {
		return errors.New("source ref type is required")
	}
	if strings.TrimSpace(ref.Locator) == "" {
		return errors.New("source ref locator is required")
	}
	return nil
}

func ValidateEvidenceRef(ref EvidenceRef) error {
	if strings.TrimSpace(ref.Stage) == "" && strings.TrimSpace(ref.Kind) == "" {
		return errors.New("evidence stage or kind is required")
	}
	if strings.TrimSpace(ref.Ref) == "" {
		return errors.New("evidence ref is required")
	}
	return nil
}

func ValidateItem(item Item) error {
	if strings.TrimSpace(item.ItemID) == "" {
		return errors.New("item_id is required")
	}
	if strings.TrimSpace(item.Title) == "" {
		return errors.New("title is required")
	}
	if item.SchemaVersion >= SchemaVersion2 {
		if !validConceptState(item.ConceptState) {
			return errors.New("invalid concept_state")
		}
		if !validDeliveryState(item.DeliveryState) {
			return errors.New("invalid delivery_state")
		}
	}
	for _, ref := range item.SourceRefs {
		if err := ValidateSourceRef(ref); err != nil {
			return err
		}
	}
	for _, ref := range item.EvidenceRefs {
		if err := ValidateEvidenceRef(ref); err != nil {
			return err
		}
	}
	return nil
}
