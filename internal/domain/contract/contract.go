package contract

import (
	"fmt"
	"strings"
)

// Contract represents an executable contract normalized from a user request.
type Contract struct {
	Goal         string
	Acceptance   []string
	Constraints  []string
	Artifacts    []string
	Verification []string
	Rollback     []string
}

// Validate checks required fields defined by OpenClaw parity spec.
func (c Contract) Validate() error {
	if strings.TrimSpace(c.Goal) == "" {
		return fmt.Errorf("goal is required")
	}
	if len(c.Acceptance) == 0 {
		return fmt.Errorf("acceptance is required")
	}
	if len(c.Constraints) == 0 {
		return fmt.Errorf("constraints is required")
	}
	if len(c.Artifacts) == 0 {
		return fmt.Errorf("artifacts is required")
	}
	if len(c.Verification) == 0 {
		return fmt.Errorf("verification is required")
	}
	if len(c.Rollback) == 0 {
		return fmt.Errorf("rollback is required")
	}
	return nil
}
