//go:build windows

package kernel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var windowsAppContainerUserenv = windows.NewLazySystemDLL("userenv.dll")
var windowsCreateAppContainerProfile = windowsAppContainerUserenv.NewProc("CreateAppContainerProfile")
var windowsDeleteAppContainerProfile = windowsAppContainerUserenv.NewProc("DeleteAppContainerProfile")
var windowsDeriveAppContainerSID = windowsAppContainerUserenv.NewProc("DeriveAppContainerSidFromAppContainerName")
var windowsConfinementACLMu sync.Mutex

type windowsConfinementLease struct {
	name           string
	sid            *windows.SID
	profileCreated bool
	granted        []string
	journal        *windowsConfinementJournal
	once           sync.Once
	err            error
}

func prepareWindowsConfinedRequest(request *windowsConfinedRequest) (_ *windowsConfinementLease, err error) {
	if request == nil {
		return nil, errors.New("Windows kernel confinement request is required")
	}
	if err := validateWindowsConfinementRequest(
		request.Workspace, request.Executable, request.Arguments, request.Environment, nil, nil, nil,
	); err != nil {
		return nil, err
	}
	if err := validateWindowsConfinementReadOnlyRoots(request.Workspace, request.ReadOnlyRoots); err != nil {
		return nil, err
	}
	journalPath := filepath.Join(managedRuntimeStateHome(), windowsConfinementLeaseDirectory)
	if windowsKernelPathsOverlap(journalPath, request.Workspace) {
		return nil, errors.New("Windows kernel workspace overlaps confinement recovery state")
	}
	if err := recoverWindowsConfinementLeases(); err != nil {
		return nil, fmt.Errorf("recover prior Windows kernel confinement leases: %w", err)
	}
	journal, err := newWindowsConfinementJournal(request)
	if err != nil {
		return nil, err
	}
	lease := &windowsConfinementLease{name: request.ProfileName, journal: journal}
	defer func() {
		if err != nil {
			err = errors.Join(err, lease.close())
		}
	}()
	sid, err := createWindowsAppContainerProfile(request.ProfileName)
	if err != nil {
		return nil, err
	}
	lease.sid = sid
	lease.profileCreated = true
	for _, grant := range windowsConfinementGrants(
		request.Executable, request.Workspace, request.ReadOnlyRoots,
	) {
		if err := grantWindowsAppContainerPath(grant.path, sid, grant.permissions, grant.inheritance); err != nil {
			return nil, fmt.Errorf("grant Windows kernel confinement path: %w", err)
		}
		lease.granted = append(lease.granted, grant.path)
	}
	return lease, nil
}

func (lease *windowsConfinementLease) close() error {
	if lease == nil {
		return nil
	}
	lease.once.Do(func() {
		for index := len(lease.granted) - 1; index >= 0; index-- {
			if err := revokeWindowsAppContainerPath(lease.granted[index], lease.sid); err != nil {
				lease.err = errors.Join(lease.err, fmt.Errorf("revoke Windows kernel confinement path: %w", err))
			}
		}
		if lease.profileCreated {
			if err := deleteWindowsAppContainerProfile(lease.name); err != nil {
				lease.err = errors.Join(lease.err, err)
			}
		}
		if lease.sid != nil {
			if err := windows.FreeSid(lease.sid); err != nil {
				lease.err = errors.Join(lease.err, err)
			}
		}
		if lease.journal != nil {
			if lease.err == nil {
				lease.err = lease.journal.finish()
			} else {
				_ = lease.journal.closeLock()
			}
		}
	})
	return lease.err
}

type windowsConfinementGrant struct {
	path        string
	permissions windows.ACCESS_MASK
	inheritance uint32
}

func windowsConfinementGrants(executable, workspace string, readOnlyRoots []string) []windowsConfinementGrant {
	grants := []windowsConfinementGrant{
		{executable, windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE, windows.NO_INHERITANCE},
		{workspace, windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.FILE_GENERIC_EXECUTE | windows.DELETE,
			windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT},
	}
	for _, root := range readOnlyRoots {
		grants = append(grants, windowsConfinementGrant{
			path: root, permissions: windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE,
			inheritance: windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		})
	}
	return grants
}

func createWindowsAppContainerProfile(name string) (*windows.SID, error) {
	namePointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	labelPointer, err := windows.UTF16PtrFromString("Synon confined kernel worker")
	if err != nil {
		return nil, err
	}
	var sid *windows.SID
	status, _, _ := windowsCreateAppContainerProfile.Call(
		uintptr(unsafe.Pointer(namePointer)), uintptr(unsafe.Pointer(labelPointer)), uintptr(unsafe.Pointer(labelPointer)),
		0, 0, uintptr(unsafe.Pointer(&sid)),
	)
	if status != 0 || sid == nil {
		return nil, fmt.Errorf("%w: CreateAppContainerProfile failed with HRESULT %#x", ErrConfinementUnavailable, status)
	}
	return sid, nil
}

func deriveWindowsAppContainerSID(name string) (*windows.SID, error) {
	namePointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	var sid *windows.SID
	status, _, _ := windowsDeriveAppContainerSID.Call(uintptr(unsafe.Pointer(namePointer)), uintptr(unsafe.Pointer(&sid)))
	if status != 0 || sid == nil {
		return nil, fmt.Errorf("%w: DeriveAppContainerSidFromAppContainerName failed with HRESULT %#x", ErrConfinementUnavailable, status)
	}
	return sid, nil
}

func deleteWindowsAppContainerProfile(name string) error {
	namePointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	status, _, _ := windowsDeleteAppContainerProfile.Call(uintptr(unsafe.Pointer(namePointer)))
	if status != 0 {
		return fmt.Errorf("DeleteAppContainerProfile failed with HRESULT %#x", status)
	}
	return nil
}

func grantWindowsAppContainerPath(path string, sid *windows.SID, permissions windows.ACCESS_MASK, inheritance uint32) error {
	return changeWindowsAppContainerPath(path, sid, permissions, windows.GRANT_ACCESS, inheritance)
}

func revokeWindowsAppContainerPath(path string, sid *windows.SID) error {
	return changeWindowsAppContainerPath(path, sid, 0, windows.REVOKE_ACCESS, windows.NO_INHERITANCE)
}

func changeWindowsAppContainerPath(path string, sid *windows.SID, permissions windows.ACCESS_MASK, mode windows.ACCESS_MODE, inheritance uint32) error {
	windowsConfinementACLMu.Lock()
	defer windowsConfinementACLMu.Unlock()
	if sid == nil {
		return errors.New("Windows kernel confinement SID is missing")
	}
	if _, err := os.Lstat(path); err != nil {
		return err
	}
	if err := validateWindowsKernelPath(path, true, false); err != nil {
		// Executable files are regular rather than directories.
		if err := validateWindowsKernelPath(path, true, true); err != nil {
			return err
		}
	}
	security, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, _, err := security.DACL()
	if err != nil || dacl == nil {
		return errors.New("Windows kernel confinement path has no usable DACL")
	}
	entry := windows.EXPLICIT_ACCESS{
		AccessPermissions: permissions,
		AccessMode:        mode,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
	updated, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, dacl)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, updated, nil)
}
