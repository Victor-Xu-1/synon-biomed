//go:build windows

package kernel

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/sys/windows"
)

const (
	windowsConfinementLeaseDirectory = "kernel-appcontainer-leases"
	windowsConfinementLeaseVersion   = 1
	maxWindowsConfinementLeaseBytes  = 4096
)

type windowsConfinementReceipt struct {
	Version       int                            `json:"version"`
	ProfileName   string                         `json:"profile_name"`
	Workspace     string                         `json:"workspace"`
	Executable    string                         `json:"executable"`
	ReadOnlyRoots []string                       `json:"read_only_roots,omitempty"`
	ReadOnlyFiles []string                       `json:"read_only_files,omitempty"`
	WritableFiles []string                       `json:"writable_files,omitempty"`
	Frozen        []windowsConfinementFrozenPath `json:"frozen,omitempty"`
}

type windowsConfinementJournal struct {
	receiptPath string
	lockPath    string
	lock        windows.Handle
}

func windowsConfinementJournalDirectory() (string, error) {
	home := managedRuntimeStateHome()
	if !filepath.IsAbs(home) {
		return "", errors.New("Windows kernel confinement state home must be absolute")
	}
	directory := filepath.Join(home, windowsConfinementLeaseDirectory)
	for ancestor := directory; ; ancestor = filepath.Dir(ancestor) {
		info, err := os.Lstat(ancestor)
		if err == nil {
			if !info.IsDir() || validateWindowsKernelPath(ancestor, true, false) != nil {
				return "", errors.New("Windows kernel confinement state parent is unsafe")
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(ancestor) == ancestor {
			return "", errors.New("Windows kernel confinement state parent is unavailable")
		}
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	if err := validateWindowsKernelPath(directory, true, false); err != nil {
		return "", fmt.Errorf("Windows kernel confinement state directory: %w", err)
	}
	return directory, nil
}

func newWindowsConfinementJournal(request *windowsConfinedRequest) (_ *windowsConfinementJournal, err error) {
	directory, err := windowsConfinementJournalDirectory()
	if err != nil {
		return nil, err
	}
	receipt := windowsConfinementReceipt{
		Version:     windowsConfinementLeaseVersion,
		ProfileName: request.ProfileName, Workspace: request.Workspace, Executable: request.Executable,
		ReadOnlyRoots: append([]string(nil), request.ReadOnlyRoots...),
		ReadOnlyFiles: append([]string(nil), request.ReadOnlyFiles...),
		WritableFiles: append([]string(nil), request.WritableFiles...),
		Frozen:        append([]windowsConfinementFrozenPath(nil), request.Frozen...),
	}
	if err := validateWindowsConfinementReceipt(receipt); err != nil {
		return nil, err
	}
	journal := &windowsConfinementJournal{
		receiptPath: filepath.Join(directory, request.ProfileName+".json"),
		lockPath:    filepath.Join(directory, request.ProfileName+".lock"),
	}
	journal.lock, err = openWindowsConfinementLock(journal.lockPath, windows.CREATE_NEW)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = journal.closeLock()
			_ = os.Remove(journal.lockPath)
		}
	}()
	raw, err := json.Marshal(receipt)
	if err != nil || len(raw) > maxWindowsConfinementLeaseBytes {
		return nil, errors.New("Windows kernel confinement receipt is invalid")
	}
	temporary := journal.receiptPath + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(temporary)
	}()
	if _, err := file.Write(raw); err != nil {
		return nil, err
	}
	if err := file.Sync(); err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(temporary, journal.receiptPath); err != nil {
		return nil, err
	}
	return journal, nil
}

func openWindowsConfinementLock(path string, disposition uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
		disposition, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, err
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = windows.CloseHandle(handle)
		return 0, errors.New("Windows kernel confinement lock is not a private ordinary file")
	}
	return handle, nil
}

func (journal *windowsConfinementJournal) closeLock() error {
	if journal == nil || journal.lock == 0 {
		return nil
	}
	err := windows.CloseHandle(journal.lock)
	journal.lock = 0
	return err
}

func (journal *windowsConfinementJournal) finish() error {
	if journal == nil {
		return nil
	}
	if err := os.Remove(journal.receiptPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = journal.closeLock()
		return err
	}
	if err := journal.closeLock(); err != nil {
		return err
	}
	if err := os.Remove(journal.lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func validateWindowsConfinementReceipt(receipt windowsConfinementReceipt) error {
	if receipt.Version != windowsConfinementLeaseVersion ||
		!strings.HasPrefix(receipt.ProfileName, "SynonWorker.") {
		return errors.New("Windows kernel confinement receipt identity is invalid")
	}
	if _, err := uuid.Parse(strings.TrimPrefix(receipt.ProfileName, "SynonWorker.")); err != nil {
		return errors.New("Windows kernel confinement receipt identity is invalid")
	}
	if err := validateWindowsKernelPath(receipt.Workspace, false, false); err != nil {
		return err
	}
	if err := validateWindowsKernelPath(receipt.Executable, false, true); err != nil {
		return err
	}
	return validateWindowsConfinementAuthorityAt(&windowsConfinedRequest{
		Workspace: receipt.Workspace, Executable: receipt.Executable,
		ReadOnlyRoots: receipt.ReadOnlyRoots, ReadOnlyFiles: receipt.ReadOnlyFiles,
		WritableFiles: receipt.WritableFiles, Frozen: receipt.Frozen,
	}, false)
}

// Recovery only reclaims a receipt after its owner has lost the exclusive
// lock. The lock is kernel-managed and is released even if the server crashes.
// A sharing violation denotes an active worker and must not be bypassed.
func recoverWindowsConfinementLeases() error {
	directory, err := windowsConfinementJournalDirectory()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	var failures error
	for _, entry := range entries {
		var entryFailure error
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		if err := validateWindowsKernelPath(path, true, true); err != nil {
			failures = errors.Join(failures, errors.New("Windows kernel confinement receipt path is unsafe"))
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, maxWindowsConfinementLeaseBytes+1))
		_ = file.Close()
		if readErr != nil || len(raw) > maxWindowsConfinementLeaseBytes {
			failures = errors.Join(failures, errors.New("Windows kernel confinement receipt cannot be read"))
			continue
		}
		receipt := windowsConfinementReceipt{}
		if err := json.Unmarshal(raw, &receipt); err != nil || validateWindowsConfinementReceipt(receipt) != nil ||
			entry.Name() != receipt.ProfileName+".json" {
			failures = errors.Join(failures, errors.New("Windows kernel confinement receipt is invalid"))
			continue
		}
		journal := &windowsConfinementJournal{
			receiptPath: path, lockPath: filepath.Join(directory, receipt.ProfileName+".lock"),
		}
		journal.lock, err = openWindowsConfinementLock(journal.lockPath, windows.OPEN_EXISTING)
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			continue
		}
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		sid, err := deriveWindowsAppContainerSID(receipt.ProfileName)
		if err != nil {
			_ = journal.closeLock()
			failures = errors.Join(failures, err)
			continue
		}
		for _, grant := range windowsConfinementRequestGrants(&windowsConfinedRequest{
			Workspace: receipt.Workspace, Executable: receipt.Executable,
			ReadOnlyRoots: receipt.ReadOnlyRoots, ReadOnlyFiles: receipt.ReadOnlyFiles,
			WritableFiles: receipt.WritableFiles,
		}) {
			if err := revokeWindowsConfinementGrant(grant, sid); err != nil {
				entryFailure = errors.Join(entryFailure, err)
			}
		}
		_ = windows.FreeSid(sid)
		if err := deleteWindowsAppContainerProfile(receipt.ProfileName); err != nil {
			entryFailure = errors.Join(entryFailure, err)
		}
		if entryFailure != nil {
			_ = journal.closeLock()
			failures = errors.Join(failures, entryFailure)
			continue
		}
		if err := journal.finish(); err != nil {
			failures = errors.Join(failures, err)
		}
	}
	return failures
}
