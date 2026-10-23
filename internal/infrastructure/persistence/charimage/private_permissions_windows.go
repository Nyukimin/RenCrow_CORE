//go:build windows

package charimage

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const minimumCharImageSIDBytes = 8

func validatePrivateDatabaseDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("CharImage history parent directory is unavailable: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("CharImage history parent directory is unsafe")
	}
	if err := validatePrivateCharImageACL(path, true); err != nil {
		return fmt.Errorf("CharImage history parent directory permissions are unsafe: %w", err)
	}
	return nil
}

func secureCreatedPrivateDatabase(path string, file *os.File) error {
	_ = file
	userSID, err := charImageCurrentUserSID()
	if err != nil {
		return err
	}
	acl, err := charImagePrivateACL(userSID)
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		return fmt.Errorf("protect new CharImage history database ACL: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("new CharImage history database changed during ACL protection")
	}
	return validateExistingPrivateDatabase(path, info)
}

func validateExistingPrivateDatabase(path string, info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("existing CharImage history database is not a regular file")
	}
	if err := validatePrivateCharImageACL(path, false); err != nil {
		return err
	}
	return nil
}

func validatePrivateCharImageACL(path string, directory bool) error {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		return errors.New("security descriptor is unavailable")
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.IsValid() {
		return errors.New("owner is invalid")
	}
	userSID, err := charImageCurrentUserSID()
	if err != nil {
		return err
	}
	if !owner.Equals(userSID) {
		return errors.New("path is not owned by the operating user")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount == 0 {
		return errors.New("DACL is unsafe")
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return errors.New("SID validation failed")
	}
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return errors.New("SID validation failed")
	}
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("DACL contains an unsupported ACE")
		}
		if int(ace.Header.AceSize) < int(unsafe.Offsetof(windows.ACCESS_ALLOWED_ACE{}.SidStart))+minimumCharImageSIDBytes {
			return errors.New("DACL contains an invalid SID")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() {
			return errors.New("path DACL contains an invalid SID")
		}
		trusted := sid.Equals(userSID) || sid.Equals(systemSID) || sid.Equals(adminSID)
		if !trusted && (!directory || ace.Mask&charImageDirectoryMutationAccess != 0) {
			return errors.New("path is accessible by an unauthorized principal")
		}
	}
	return nil
}

const charImageDirectoryMutationAccess = windows.GENERIC_ALL | windows.GENERIC_WRITE |
	windows.WRITE_DAC | windows.WRITE_OWNER | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA |
	windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES | 0x00010000 | 0x00000040

func charImageCurrentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return nil, errors.New("CharImage history operating user SID is unavailable")
	}
	return user.User.Sid, nil
}

func charImagePrivateACL(userSID *windows.SID) (*windows.ACL, error) {
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	sids := []*windows.SID{userSID, systemSID, adminSID}
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(sids))
	for index, sid := range sids {
		duplicate := false
		for _, previous := range sids[:index] {
			if sid.Equals(previous) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		trusteeType := uint32(windows.TRUSTEE_IS_USER)
		if sid.Equals(adminSID) {
			trusteeType = uint32(windows.TRUSTEE_IS_GROUP)
		}
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_TYPE(trusteeType),
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	return windows.ACLFromEntries(entries, nil)
}
