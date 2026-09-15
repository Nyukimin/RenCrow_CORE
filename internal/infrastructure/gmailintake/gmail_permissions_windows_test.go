//go:build windows

package gmailintake

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSecureGmailFileWindowsProtectsFileInheritedFromPermissiveDirectory(t *testing.T) {
	root := t.TempDir()
	setGmailPermissionTestACL(t, root, windows.WinWorldSid, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT)

	file, err := os.CreateTemp(root, "receipt-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := secureGmailFile(file); err != nil {
		t.Fatalf("secureGmailFile rejected a newly created file: %v", err)
	}
	if _, err := file.WriteString(`{"schema":"rencrow.gmail.v1"}`); err != nil {
		t.Fatalf("write after protection failed: %v", err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGmailPermissions(file.Name(), info, 0o600); err != nil {
		t.Fatalf("protected file did not validate: %v", err)
	}
}

func TestValidateGmailPermissionsWindowsRejectsEveryone(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "receipt-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	setGmailPermissionTestACL(t, file.Name(), windows.WinWorldSid, windows.NO_INHERITANCE)
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGmailPermissions(file.Name(), info, 0o600); err == nil {
		t.Fatal("Everyone ACL was accepted")
	}
}

func setGmailPermissionTestACL(t *testing.T, path string, sidType windows.WELL_KNOWN_SID_TYPE, inheritance uint32) {
	t.Helper()
	sid, err := windows.CreateWellKnownSid(sidType)
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		t.Fatal(err)
	}
}
