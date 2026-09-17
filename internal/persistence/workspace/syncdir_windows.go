//go:build windows

package workspace

// Windows does not expose the POSIX directory fsync contract through
// os.File.Sync. Blob commits still sync the staging file before the atomic
// rename and retain their recovery marker until the remaining commit phases
// succeed; only the unsupported directory metadata flush is omitted.
func syncDirectory(string) error {
	return nil
}
