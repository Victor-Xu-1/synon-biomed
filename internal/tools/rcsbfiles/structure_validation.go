package rcsbfiles

import (
	"errors"
	"io"
)

var ErrInvalidStructure = errors.New("structure requires a complete coordinate or component atom table")

// ValidateCIF reuses the streaming download parser without requiring a remote
// accession. Locally generated and edited structures use the same grammar and
// coordinate checks as downloaded structures. The caller owns the reader.
func ValidateCIF(reader io.Reader) error {
	validator := &validatingReadCloser{reader: reader, format: FormatCIF}
	if _, err := io.Copy(io.Discard, validator); err != nil {
		return errors.Join(ErrInvalidStructure, err)
	}
	return nil
}
