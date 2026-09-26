//go:build !windows

package kernel

func managedRGenerationDirectoryName(generation string) string {
	return generation
}

func rejectManagedRGenerationPathCollision(string, string) error {
	return nil
}
