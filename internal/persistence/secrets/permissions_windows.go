//go:build windows

package secrets

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func vaultDACL() (*windows.ACL, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("get current user for vault ACL: %w", err)
	}
	sddl := "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + user.User.Sid.String() + ")"
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("build vault ACL: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return nil, fmt.Errorf("read vault ACL: %w", err)
	}
	return dacl, nil
}

func protectPath(path string, _ bool) error {
	dacl, err := vaultDACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		return fmt.Errorf("protect vault ACL for %s: %w", path, err)
	}
	return nil
}

func protectFile(file *os.File) error {
	return protectPath(file.Name(), false)
}
