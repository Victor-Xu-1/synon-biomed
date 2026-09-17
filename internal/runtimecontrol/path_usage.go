package runtimecontrol

func PathUsage(path string) (int64, *uint64, error) {
	bytes, err := directoryBytes(path)
	return bytes, availableBytes(path), err
}

// AvailableBytes returns the bytes available to an unprivileged writer on the
// filesystem containing path. A nil result means the platform could not
// provide a trustworthy measurement.
func AvailableBytes(path string) *uint64 {
	return availableBytes(path)
}
