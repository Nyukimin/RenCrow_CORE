//go:build windows

package gmailintake

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const minimumGmailWindowsSIDBytes = 8

func secureGmailFile(file *os.File) error {
	if file == nil {
		return errors.New("Gmail receipt file handle is required")
	}
	info, err := file.Stat()
	if err != nil {
		return errors.New("Gmail receipt file stat failed")
	}
	if !info.Mode().IsRegular() {
		return errors.New("Gmail receipt file is not regular")
	}
	userSID, err := currentGmailWindowsUserSID()
	if err != nil {
		return err
	}
	acl, err := privateGmailWindowsACL(userSID)
	if err != nil {
		return err
	}
	pathInfo, err := os.Lstat(file.Name())
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, pathInfo) {
		return errors.New("Gmail receipt file changed during protection")
	}
	name, err := windows.UTF16PtrFromString(file.Name())
	if err != nil {
		return err
	}
	reopenedHandle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return err
	}
	reopened := os.NewFile(uintptr(reopenedHandle), file.Name())
	if reopened == nil {
		_ = windows.CloseHandle(reopenedHandle)
		return errors.New("Gmail receipt file handle is unavailable")
	}
	defer reopened.Close()
	reopenedInfo, err := reopened.Stat()
	if err != nil || !os.SameFile(info, reopenedInfo) {
		return errors.New("Gmail receipt file changed during protection")
	}
	if err := windows.SetSecurityInfo(
		windows.Handle(reopened.Fd()),
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		return err
	}
	latestInfo, err := os.Lstat(file.Name())
	if err != nil || latestInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, latestInfo) {
		return errors.New("Gmail receipt file changed during protection")
	}
	return validateGmailPermissions(file.Name(), latestInfo, 0o600)
}

func secureGmailDirectory(path string) error {
	if path == "" {
		return errors.New("Gmail receipt root is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return errors.New("inspect Gmail receipt root failed")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("Gmail receipt root is not a directory")
	}
	userSID, err := currentGmailWindowsUserSID()
	if err != nil {
		return err
	}
	acl, err := privateGmailWindowsACL(userSID)
	if err != nil {
		return err
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
		return err
	}
	latestInfo, err := os.Lstat(path)
	if err != nil || latestInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, latestInfo) {
		return errors.New("Gmail receipt root changed during protection")
	}
	return validateGmailPermissions(path, latestInfo, 0o700)
}

func validateGmailPermissions(path string, info os.FileInfo, mode os.FileMode) error {
	if path == "" || info == nil || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Gmail receipt path is unsafe")
	}
	switch mode.Perm() {
	case 0o600:
		if !info.Mode().IsRegular() {
			return errors.New("Gmail receipt file is not regular")
		}
	case 0o700:
		if !info.IsDir() {
			return errors.New("Gmail receipt root is not a directory")
		}
	default:
		return errors.New("Gmail receipt permission mode is unsupported")
	}
	return validateGmailWindowsACL(path, info)
}

func currentGmailWindowsUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	if user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return nil, errors.New("current Windows user SID is unavailable")
	}
	return user.User.Sid, nil
}

func privateGmailWindowsACL(userSID *windows.SID) (*windows.ACL, error) {
	if userSID == nil || !userSID.IsValid() {
		return nil, errors.New("current Windows user SID is unavailable")
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	sids := make([]*windows.SID, 0, 3)
	for _, sid := range []*windows.SID{userSID, systemSID, adminSID} {
		duplicate := false
		for _, existing := range sids {
			if existing.Equals(sid) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			sids = append(sids, sid)
		}
	}
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(sids))
	for _, sid := range sids {
		trusteeType := uint32(windows.TRUSTEE_IS_USER)
		if sid.Equals(adminSID) {
			trusteeType = windows.TRUSTEE_IS_GROUP
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

func validateGmailWindowsACL(path string, info os.FileInfo) error {
	if path == "" || info == nil {
		return errors.New("Gmail receipt path is unsafe")
	}
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		return errors.New("Gmail receipt security descriptor is unavailable")
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.IsValid() {
		return errors.New("Gmail receipt owner is invalid")
	}
	userSID, err := currentGmailWindowsUserSID()
	if err != nil {
		return err
	}
	if !owner.Equals(userSID) {
		return errors.New("Gmail receipt owner is not the current user")
	}
	_, _, err = descriptor.Control()
	if err != nil {
		return errors.New("Gmail receipt DACL control is unavailable")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount == 0 {
		return errors.New("Gmail receipt DACL is unsafe")
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil || ace == nil {
			return errors.New("Gmail receipt DACL contains an invalid ACE")
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("Gmail receipt DACL contains an unsupported ACE")
		}
		minimumACEBytes := int(unsafe.Offsetof(windows.ACCESS_ALLOWED_ACE{}.SidStart)) + minimumGmailWindowsSIDBytes
		if int(ace.Header.AceSize) < minimumACEBytes {
			return errors.New("Gmail receipt DACL contains an invalid SID")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || (!sid.Equals(userSID) && !sid.Equals(systemSID) && !sid.Equals(adminSID)) {
			return errors.New("Gmail receipt DACL contains an unauthorized principal")
		}
	}
	return nil
}
