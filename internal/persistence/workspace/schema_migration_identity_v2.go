package workspace

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"unicode/utf8"
)

const schemaMigrationIdentityV2Domain = "synon.schema-migration.identity.v2"

type schemaMigrationIdentityV2 struct {
	CallbackID        string
	RuleSpec          string
	PreflightIdentity string
}

func (migration versionedSchemaMigration) validatedChecksum() (string, error) {
	if migration.version <= 20 {
		if migration.identityV2 != nil {
			return "", errors.New("legacy migration cannot declare identity v2")
		}
		return migration.checksum(), nil
	}
	return migration.checksumV2()
}

func (migration versionedSchemaMigration) checksumV2() (string, error) {
	if migration.version < 21 {
		return "", errors.New("identity v2 requires migration version 21 or newer")
	}
	if migration.identityV2 == nil {
		return "", errors.New("identity v2 metadata is required")
	}
	if migration.name == "" || migration.identityV2.CallbackID == "" || migration.identityV2.RuleSpec == "" || migration.identityV2.PreflightIdentity == "" {
		return "", errors.New("identity v2 fields must be non-empty")
	}
	values := append([]string{migration.name}, migration.statements...)
	values = append(values, migration.identityV2.CallbackID, migration.identityV2.RuleSpec, migration.identityV2.PreflightIdentity)
	for _, value := range values {
		if !utf8.ValidString(value) {
			return "", errors.New("identity v2 fields must be valid UTF-8")
		}
	}

	digest := sha256.New()
	writeSchemaIdentityFrame(digest, []byte(schemaMigrationIdentityV2Domain))
	writeSchemaIdentityUint64(digest, uint64(migration.version))
	writeSchemaIdentityFrame(digest, []byte(migration.name))
	writeSchemaIdentityUint64(digest, uint64(len(migration.statements)))
	for _, statement := range migration.statements {
		writeSchemaIdentityFrame(digest, []byte(statement))
	}
	writeSchemaIdentityFrame(digest, []byte(migration.identityV2.CallbackID))
	writeSchemaIdentityFrame(digest, []byte(migration.identityV2.RuleSpec))
	writeSchemaIdentityFrame(digest, []byte(migration.identityV2.PreflightIdentity))
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writeSchemaIdentityUint64(target hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	writeSchemaIdentityFrame(target, encoded[:])
}

func writeSchemaIdentityFrame(target hash.Hash, payload []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(payload)))
	_, _ = target.Write(length[:])
	_, _ = target.Write(payload)
}

func validateSchemaMigrationIdentityRegistry(migrations []versionedSchemaMigration) error {
	for index, migration := range migrations {
		if migration.version != index+1 {
			return fmt.Errorf("migration registry is not contiguous at version %d", index+1)
		}
		if _, err := migration.validatedChecksum(); err != nil {
			return fmt.Errorf("migration %d: %w", migration.version, err)
		}
		if migration.identityV2 != nil && !schemaMigrationIdentityHandlersKnown(*migration.identityV2) {
			return fmt.Errorf("migration %d: identity v2 handler is not registered", migration.version)
		}
	}
	return validateSchemaMigrationRemediationRegistry(migrations)
}
