//go:build windows || plan9

package datadir

func syncDirectory(string) error {
	return nil
}
