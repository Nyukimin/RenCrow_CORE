package nativeharnessclient

import (
	"encoding/json"
	"errors"
	"fmt"
)

const (
	// coreCallerPrincipal and coreCallerDefaultOrigin are the premises the 05
	// delegation section states about the Harness config that CORE starts
	// (the harness_config row): the connection is the CORE caller and what it
	// sends without a proof is Automation.
	coreCallerPrincipal     = "core:local"
	coreCallerDefaultOrigin = "automation"
)

// ErrHarnessCallerProfile reports a Harness config that does not satisfy the
// premises of the 05 delegation section. The error never contains the config
// text.
var ErrHarnessCallerProfile = errors.New("harness config does not match the CORE issuer settings")

// harnessCallerView is the part of the Harness config that CORE reads: the
// caller section named by the 05 harness_config row. Every other field of
// that config belongs to RenCrow_Harness and is neither read nor validated.
type harnessCallerView struct {
	Caller *struct {
		Principal     string `json:"principal"`
		DefaultOrigin string `json:"default_origin"`
		RelayIssuers  []struct {
			Issuer   string `json:"issuer"`
			KeyID    string `json:"key_id"`
			Audience string `json:"audience"`
		} `json:"relay_issuers"`
	} `json:"caller"`
}

// CheckHarnessCallerProfile verifies, from the Harness config bytes, the
// startup premises of the 05 section: caller.principal is core:local,
// caller.default_origin is automation, the settings' audience is that
// principal, and caller.relay_issuers contains exactly the settings' issuer,
// key_id and audience. The settings are validated first.
func CheckHarnessCallerProfile(settings IssuerSettings, harnessConfigJSON []byte) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	var view harnessCallerView
	if err := json.Unmarshal(harnessConfigJSON, &view); err != nil {
		return fmt.Errorf("%w: the config is not a JSON object", ErrHarnessCallerProfile)
	}
	caller := view.Caller
	if caller == nil {
		return fmt.Errorf("%w: caller section is missing", ErrHarnessCallerProfile)
	}
	if caller.Principal != coreCallerPrincipal {
		return fmt.Errorf("%w: caller.principal must be %s", ErrHarnessCallerProfile, coreCallerPrincipal)
	}
	if caller.DefaultOrigin != coreCallerDefaultOrigin {
		return fmt.Errorf("%w: caller.default_origin must be %s", ErrHarnessCallerProfile, coreCallerDefaultOrigin)
	}
	if settings.Audience != caller.Principal {
		return fmt.Errorf("%w: audience must be the caller principal %s", ErrHarnessCallerProfile, caller.Principal)
	}
	for _, allowed := range caller.RelayIssuers {
		if allowed.Issuer == settings.Issuer && allowed.KeyID == settings.KeyID && allowed.Audience == settings.Audience {
			return nil
		}
	}
	return fmt.Errorf("%w: caller.relay_issuers has no entry for the issuer, key_id and audience", ErrHarnessCallerProfile)
}
