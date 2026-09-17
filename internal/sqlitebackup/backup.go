package sqlitebackup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	moderncsqlite "modernc.org/sqlite"
)

const (
	backupPagesPerStep = 1024
	backupBusyRetries  = 50
	backupBusyDelay    = 10 * time.Millisecond
)

type Result struct {
	Directory string
	Path      string
	SHA256    string
	SizeBytes int64
}

// BackupFromConn creates a consistent SQLite backup beneath an existing,
// private root. The caller owns the returned directory and must remove it when
// the snapshot is no longer needed.
func BackupFromConn(ctx context.Context, source *sql.Conn, root string) (result Result, err error) {
	if source == nil {
		return Result{}, errors.New("sqlite backup source connection is required")
	}
	root, err = validatePrivateRoot(root)
	if err != nil {
		return Result{}, err
	}
	directory, err := createPrivateBackupDirectory(root)
	if err != nil {
		return Result{}, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(directory)
		}
	}()
	target := filepath.Join(directory, "snapshot.sqlite")
	if _, err := os.Lstat(target); err == nil {
		return Result{}, errors.New("sqlite backup destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, fmt.Errorf("inspect sqlite backup destination: %w", err)
	}
	if err := runBackup(ctx, source, target); err != nil {
		return Result{}, err
	}
	if err := os.Chmod(target, 0o600); err != nil {
		return Result{}, fmt.Errorf("secure sqlite backup: %w", err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		return Result{}, fmt.Errorf("inspect sqlite backup: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return Result{}, errors.New("sqlite backup is not a private regular file")
	}
	digest, err := fileDigest(target)
	if err != nil {
		return Result{}, err
	}
	keep = true
	return Result{Directory: directory, Path: target, SHA256: digest, SizeBytes: info.Size()}, nil
}

func runBackup(ctx context.Context, source *sql.Conn, target string) error {
	err := source.Raw(func(driverConnection any) error {
		backuper, ok := driverConnection.(interface {
			NewBackup(string) (*moderncsqlite.Backup, error)
		})
		if !ok {
			return errors.New("sqlite driver does not support online backup")
		}
		backup, err := backuper.NewBackup(target)
		if err != nil {
			return err
		}
		finished := false
		defer func() {
			if !finished {
				_ = backup.Finish()
			}
		}()
		busyRetries := 0
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			more, err := backup.Step(backupPagesPerStep)
			if err != nil {
				if isContention(err) && busyRetries < backupBusyRetries {
					busyRetries++
					if err := waitForRetry(ctx); err != nil {
						return err
					}
					continue
				}
				return err
			}
			busyRetries = 0
			if more {
				continue
			}
			finished = true
			return backup.Finish()
		}
	})
	if err != nil {
		return fmt.Errorf("create sqlite online backup: %w", err)
	}
	return nil
}

func validatePrivateRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("sqlite backup root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved != absolute {
		return "", errors.New("sqlite backup root must not contain symbolic links")
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect sqlite backup root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return "", errors.New("sqlite backup root must be a private 0700 directory")
	}
	if err := validateRootOwner(info); err != nil {
		return "", err
	}
	return absolute, nil
}

func createPrivateBackupDirectory(root string) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", fmt.Errorf("generate sqlite backup directory name: %w", err)
		}
		directory := filepath.Join(root, "backup-"+hex.EncodeToString(random[:]))
		if err := os.Mkdir(directory, 0o700); err == nil {
			if err := os.Chmod(directory, 0o700); err != nil {
				_ = os.Remove(directory)
				return "", fmt.Errorf("secure sqlite backup directory: %w", err)
			}
			if _, err := validatePrivateRoot(directory); err != nil {
				_ = os.Remove(directory)
				return "", err
			}
			return directory, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("create sqlite backup directory: %w", err)
		}
	}
	return "", errors.New("create unique sqlite backup directory: retry limit reached")
}

func waitForRetry(ctx context.Context) error {
	timer := time.NewTimer(backupBusyDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isContention(err error) bool {
	var sqliteError *moderncsqlite.Error
	if !errors.As(err, &sqliteError) {
		return false
	}
	primary := sqliteError.Code() & 0xff
	return primary == 5 || primary == 6
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("hash sqlite backup: %w", err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("hash sqlite backup: %w", err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
