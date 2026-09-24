//go:build windows

package kernel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	name             string
	sid              *windows.SID
	profileCreated   bool
	granted          []windowsConfinementGrant
	authorityHandles []windows.Handle
	journal          *windowsConfinementJournal
	once             sync.Once
	err              error
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
	if err := validateWindowsConfinementAuthority(request); err != nil {
		return nil, err
	}
	if err := verifyWindowsConfinementAuthorityBindings(request); err != nil {
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
	for _, expected := range request.Authority {
		handle, err := openWindowsFrozenAuthorityPath(expected)
		if err != nil {
			return nil, fmt.Errorf("pin Windows kernel authority: %w", err)
		}
		lease.authorityHandles = append(lease.authorityHandles, handle)
	}
	sid, err := createWindowsAppContainerProfile(request.ProfileName)
	if err != nil {
		return nil, err
	}
	lease.sid = sid
	lease.profileCreated = true
	// Record the complete authority before the first ACL mutation. A partial
	// recursive grant must also be revoked on failure or after a host crash.
	lease.granted = windowsConfinementRequestGrants(request)
	for _, grant := range lease.granted {
		if err := applyWindowsConfinementGrant(grant, sid); err != nil {
			return nil, fmt.Errorf("grant Windows kernel confinement path: %w", err)
		}
	}
	if err := verifyWindowsConfinementAuthorityBindings(request); err != nil {
		return nil, err
	}
	return lease, nil
}

func (lease *windowsConfinementLease) close() error {
	if lease == nil {
		return nil
	}
	lease.once.Do(func() {
		for index := len(lease.granted) - 1; index >= 0; index-- {
			if err := revokeWindowsConfinementGrant(lease.granted[index], lease.sid); err != nil {
				lease.err = errors.Join(lease.err, fmt.Errorf("revoke Windows kernel confinement path: %w", err))
			}
		}
		if lease.profileCreated && lease.err == nil {
			if err := deleteWindowsAppContainerProfile(lease.name); err != nil {
				lease.err = errors.Join(lease.err, err)
			}
		}
		if lease.sid != nil {
			if err := windows.FreeSid(lease.sid); err != nil {
				lease.err = errors.Join(lease.err, err)
			}
		}
		lease.err = errors.Join(lease.err, lease.releaseAuthorityHandles())
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

func (lease *windowsConfinementLease) releaseAuthorityHandles() error {
	if lease == nil {
		return nil
	}
	var failure error
	for _, handle := range lease.authorityHandles {
		failure = errors.Join(failure, windows.CloseHandle(handle))
	}
	lease.authorityHandles = nil
	return failure
}

func openWindowsFrozenAuthorityPath(frozen windowsConfinementFrozenPath) (windows.Handle, error) {
	if err := validateWindowsKernelPath(frozen.Path, true, false); err != nil {
		if err := validateWindowsKernelPath(frozen.Path, true, true); err != nil {
			return 0, err
		}
	}
	name, err := windows.UTF16PtrFromString(frozen.Path)
	if err != nil {
		return 0, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, err
	}
	var identity windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &identity); err != nil ||
		identity.VolumeSerialNumber != frozen.Volume || identity.FileIndexHigh != frozen.IndexHigh ||
		identity.FileIndexLow != frozen.IndexLow || identity.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return 0, errors.New("Windows kernel frozen mount identity changed")
	}
	return handle, nil
}

type windowsConfinementGrant struct {
	path        string
	permissions windows.ACCESS_MASK
	inheritance uint32
	regular     bool
	expected    *windowsConfinementFrozenPath
}

func windowsConfinementGrants(executable, workspace string, readOnlyRoots []string) []windowsConfinementGrant {
	grants := []windowsConfinementGrant{
		{path: executable, permissions: windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE, inheritance: windows.NO_INHERITANCE, regular: true},
		{path: workspace, permissions: windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.FILE_GENERIC_EXECUTE | windows.DELETE,
			inheritance: windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT},
	}
	for _, root := range readOnlyRoots {
		grants = append(grants, windowsConfinementGrant{
			path: root, permissions: windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE,
			inheritance: windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		})
	}
	return grants
}

func windowsConfinementRequestGrants(request *windowsConfinedRequest) []windowsConfinementGrant {
	grants := windowsConfinementGrants(request.Executable, request.Workspace, request.ReadOnlyRoots)
	for _, path := range request.ReadOnlyFiles {
		grants = append(grants, windowsConfinementGrant{
			path: path, permissions: windows.FILE_GENERIC_READ, regular: true,
		})
	}
	for _, path := range request.WritableFiles {
		grants = append(grants, windowsConfinementGrant{
			path: path, permissions: windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE, regular: true,
		})
	}
	if len(request.Authority) == len(grants) {
		for index := range grants {
			identity := request.Authority[index]
			grants[index].expected = &identity
		}
	}
	return grants
}

func applyWindowsConfinementGrant(grant windowsConfinementGrant, sid *windows.SID) error {
	return walkWindowsConfinementGrant(grant, func(handle windows.Handle, _ string, directory bool) error {
		inheritance := uint32(windows.NO_INHERITANCE)
		if directory {
			inheritance = grant.inheritance
		}
		return changeWindowsAppContainerHandle(handle, sid, grant.permissions, windows.GRANT_ACCESS, inheritance)
	})
}

func revokeWindowsConfinementGrant(grant windowsConfinementGrant, sid *windows.SID) error {
	if _, err := os.Lstat(grant.path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return walkWindowsConfinementGrant(grant, func(handle windows.Handle, _ string, _ bool) error {
		return changeWindowsAppContainerHandle(handle, sid, 0, windows.REVOKE_ACCESS, windows.NO_INHERITANCE)
	})
}

// An inheritable ACE covers only future children. Explicitly visit existing
// runtime files and directories, including modules installed before launch.
// Reject reparse points rather than crossing out of the granted tree.
func walkWindowsConfinementGrant(grant windowsConfinementGrant, visit func(windows.Handle, string, bool) error) error {
	var walk func(string, bool) error
	walk = func(path string, directory bool) error {
		handle, release, err := openWindowsConfinementPath(path, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.WRITE_DAC)
		if err != nil {
			return err
		}
		defer release()
		var identity windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(handle, &identity); err != nil ||
			(directory != (identity.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0)) {
			return errors.New("Windows kernel confinement grant type changed")
		}
		if path == grant.path && grant.expected != nil &&
			(identity.VolumeSerialNumber != grant.expected.Volume || identity.FileIndexHigh != grant.expected.IndexHigh ||
				identity.FileIndexLow != grant.expected.IndexLow) {
			return errWindowsConfinementPathReplaced
		}
		if err := visit(handle, path, directory); err != nil {
			return err
		}
		if !directory {
			return nil
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := walk(filepath.Join(path, entry.Name()), entry.IsDir()); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(grant.path, !grant.regular)
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

func changeWindowsAppContainerHandle(handle windows.Handle, sid *windows.SID, permissions windows.ACCESS_MASK, mode windows.ACCESS_MODE, inheritance uint32) error {
	windowsConfinementACLMu.Lock()
	defer windowsConfinementACLMu.Unlock()
	if sid == nil {
		return errors.New("Windows kernel confinement SID is missing")
	}
	security, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
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
	return windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, updated, nil)
}

// Open every component without following reparse points and keep ancestor
// handles open without FILE_SHARE_DELETE. The final handle, not its mutable
// pathname, is the ACL authority used by Get/SetSecurityInfo.
func openWindowsConfinementPath(path string, access uint32) (windows.Handle, func(), error) {
	if err := validateWindowsKernelPath(path, false, false); err != nil {
		return 0, nil, err
	}
	volume := filepath.VolumeName(path)
	components := strings.Split(path[len(volume)+1:], `\`)
	paths := make([]string, 0, len(components)+1)
	current := volume + `\`
	paths = append(paths, current)
	for _, component := range components {
		current = filepath.Join(current, component)
		paths = append(paths, current)
	}
	handles := make([]windows.Handle, 0, len(paths))
	closeAll := func() {
		for index := len(handles) - 1; index >= 0; index-- {
			_ = windows.CloseHandle(handles[index])
		}
	}
	for index, name := range paths {
		pointer, err := windows.UTF16PtrFromString(name)
		if err != nil {
			closeAll()
			return 0, nil, err
		}
		rights := uint32(windows.FILE_READ_ATTRIBUTES)
		if index == len(paths)-1 {
			rights = access
		}
		handle, err := windows.CreateFile(pointer, rights, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if err != nil {
			closeAll()
			return 0, nil, err
		}
		handles = append(handles, handle)
		var identity windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(handle, &identity); err != nil ||
			identity.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
			(index < len(paths)-1 && identity.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0) {
			closeAll()
			return 0, nil, errWindowsConfinementPathReplaced
		}
	}
	if err := validateWindowsKernelPath(path, true, false); err != nil {
		if err := validateWindowsKernelPath(path, true, true); err != nil {
			closeAll()
			return 0, nil, err
		}
	}
	return handles[len(handles)-1], closeAll, nil
}
