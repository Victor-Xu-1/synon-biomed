package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
)

func (s *webSessionStore) randomSecret() (string, error) {
	raw := make([]byte, webSessionSecretBytes)
	if _, err := io.ReadFull(s.random, raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func validWebSecretHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validWebSecret(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == webSessionSecretBytes
}

func webSecretHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
