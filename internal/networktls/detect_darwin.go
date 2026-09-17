//go:build darwin

package networktls

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"time"
)

// detectCorporateAnchors compares the administrator keychain with the public
// root bundle used by command-line runtimes. Any additional trusted anchor is
// the same automatic signal used to relax certificate-profile strictness for
// client-only bundled connector handshakes. Failures are returned to the
// resolver, which always fails closed to strict mode.
func detectCorporateAnchors(ctx context.Context) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	detectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	publicRaw, err := os.ReadFile("/etc/ssl/cert.pem")
	if err != nil {
		return false, err
	}
	public, err := certificateFingerprints(publicRaw)
	if err != nil {
		return false, err
	}
	command := exec.CommandContext(detectCtx, "/usr/bin/security", "find-certificate", "-a", "-p", "/Library/Keychains/System.keychain")
	keychainRaw, err := command.Output()
	if err != nil {
		return false, err
	}
	keychain, err := certificateFingerprints(keychainRaw)
	if err != nil {
		return false, err
	}
	for fingerprint := range keychain {
		if _, found := public[fingerprint]; !found {
			return true, nil
		}
	}
	return false, nil
}

func certificateFingerprints(raw []byte) (map[[sha256.Size]byte]struct{}, error) {
	output := make(map[[sha256.Size]byte]struct{})
	remaining := bytes.Clone(raw)
	for {
		block, rest := pem.Decode(remaining)
		if block == nil {
			break
		}
		remaining = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		output[sha256.Sum256(certificate.Raw)] = struct{}{}
	}
	if len(output) == 0 {
		return nil, errors.New("certificate source contained no parseable certificates")
	}
	return output, nil
}
