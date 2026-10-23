//go:build windows

package charimage

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestValidatePrivateDatabaseDirectoryRejectsUntrustedMutationAccess(t *testing.T) {
	directory := t.TempDir()
	everyoneSID, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_WRITE | 0x00000040,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(everyoneSID),
		},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(directory, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateDatabaseDirectory(directory); err == nil || !strings.Contains(err.Error(), "unauthorized principal") {
		t.Fatalf("validatePrivateDatabaseDirectory() error = %v, want unauthorized mutation principal", err)
	}
}
