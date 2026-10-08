//go:build windows

package config

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const minimumStorageHostWindowsSIDBytes = 8

var errStorageHostTokenUnreadable = errors.New("storage.host.token_file is unreadable")

// validateConfidentialFileAccess is the Windows native confidential-file
// rule: the file must be owned by the operating user and every access-allowed
// ACE must name that user, SYSTEM or Administrators. An ACE for Everyone,
// Authenticated Users or any other principal is rejected, because the secret
// in that file (the storage host bearer token or the Human relay HMAC key)
// authenticates its holder. label names the setting in the error.
func validateConfidentialFileAccess(label, path string, info os.FileInfo) error {
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		return errors.New(label + " security descriptor is unavailable: " + path)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.IsValid() {
		return errors.New(label + " owner is invalid: " + path)
	}
	userSID, err := currentConfidentialFileUserSID(label)
	if err != nil {
		return err
	}
	if !owner.Equals(userSID) {
		return errors.New(label + " is not owned by the operating user: " + path)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount == 0 {
		return errors.New(label + " DACL is unsafe: " + path)
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return errors.New(label + " SID check failed")
	}
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return errors.New(label + " SID check failed")
	}
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil || ace == nil {
			return errors.New(label + " DACL contains an invalid ACE")
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New(label + " DACL contains an unsupported ACE")
		}
		minimumACEBytes := int(unsafe.Offsetof(windows.ACCESS_ALLOWED_ACE{}.SidStart)) + minimumStorageHostWindowsSIDBytes
		if int(ace.Header.AceSize) < minimumACEBytes {
			return errors.New(label + " DACL contains an invalid SID")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || (!sid.Equals(userSID) && !sid.Equals(systemSID) && !sid.Equals(adminSID)) {
			return errors.New(label + " is readable by a principal other than the operating user: " + path)
		}
	}
	return nil
}

func currentConfidentialFileUserSID(label string) (*windows.SID, error) {
	token, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	if token == nil || token.User.Sid == nil || !token.User.Sid.IsValid() {
		return nil, errors.New(label + " current user SID is unavailable")
	}
	return token.User.Sid, nil
}
