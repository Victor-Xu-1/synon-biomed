package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
)

var errAgentFileContentTypeMismatch = errors.New("file content does not match its declared format")

var errAgentFileStructureInvalid = errors.New("file structure is incomplete or invalid")

// validateAgentFileContentType applies the existing scientific file media-type
// contract to actual bytes at every ingress. It is a signature check, not a
// substitute for format-specific parsing. ReadAt preserves the caller's cursor.
func validateAgentFileContentType(filename string, file *os.File) error {
	accepted, err := agentPublicScientificAcceptedTypes(filename)
	if err != nil {
		return nil // The scientific-file contract does not own this format.
	}
	var header [512]byte
	n, err := file.ReadAt(header[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if n == 0 {
		return nil // Empty-file semantics belong to the format validator.
	}
	content := bytes.TrimPrefix(header[:n], []byte{0xef, 0xbb, 0xbf})
	detected, _, err := mime.ParseMediaType(http.DetectContentType(content))
	if err != nil {
		return err
	}
	// The HTTP sniffer does not understand every scientific container. Its
	// plain-text and octet-stream defaults are inconclusive; only a positive
	// signature for an incompatible format can reject bytes here.
	if detected == "text/plain" || detected == "application/octet-stream" {
		return nil
	}
	for _, candidate := range accepted {
		if candidate == detected {
			return nil
		}
	}
	return fmt.Errorf("%w: %s contains %s", errAgentFileContentTypeMismatch, filename, detected)
}
