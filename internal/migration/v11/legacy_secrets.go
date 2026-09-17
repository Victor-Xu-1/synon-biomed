package v11

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	secretstore "synon-go/internal/persistence/secrets"
)

const legacyUserSecretKeyName = "USER_SECRET_ENCRYPTION_KEY"

func importLegacyUserSecrets(ctx context.Context, snapshotDB, sourceHome, targetHome string) (int64, error) {
	db, err := sql.Open("sqlite", readOnlyDSN(snapshotDB))
	if err != nil {
		return 0, err
	}
	defer db.Close()
	tables, err := sourceTables(ctx, db)
	if err != nil {
		return 0, err
	}
	if !tables["user_secrets"] {
		return 0, nil
	}
	count, err := countTable(ctx, db, "user_secrets")
	if err != nil || count == 0 {
		return count, err
	}
	keys, err := readLegacyEncryptionKeys(sourceHome, legacyUserSecretKeyName)
	if err != nil {
		return 0, fmt.Errorf("load v1.1 user-secret encryption key: %w", err)
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, user_id, name, provider, encrypted_value, COALESCE(credential_type, ''),
			COALESCE(buckets, '[]'), COALESCE(region, ''), COALESCE(description, '')
		FROM user_secrets ORDER BY id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	vault := secretstore.New(targetHome)
	var imported int64
	for rows.Next() {
		var id, userID, name, provider, encrypted, credentialType, rawBuckets, region, description string
		if err := rows.Scan(&id, &userID, &name, &provider, &encrypted, &credentialType, &rawBuckets, &region, &description); err != nil {
			return 0, err
		}
		plaintext, err := decryptWithLegacyKeys(keys, encrypted, "userSecret")
		if err != nil {
			return 0, fmt.Errorf("decrypt v1.1 user secret %s: %w", id, err)
		}
		secret := secretstore.Secret{
			ID: id, UserID: secretstore.DefaultUserID, Name: name, Provider: provider,
			CredentialType: credentialType, Region: region, Description: description,
		}
		if err := json.Unmarshal([]byte(rawBuckets), &secret.Buckets); err != nil {
			return 0, fmt.Errorf("decode v1.1 secret buckets %s: %w", id, err)
		}
		var credentials map[string]any
		if json.Unmarshal(plaintext, &credentials) == nil && credentials != nil {
			secret.Credentials = make(map[string]string, len(credentials))
			for key, value := range credentials {
				if text, ok := value.(string); ok {
					secret.Credentials[key] = text
				} else {
					raw, marshalErr := json.Marshal(value)
					if marshalErr != nil {
						return 0, marshalErr
					}
					secret.Credentials[key] = string(raw)
				}
			}
		} else {
			secret.Value = string(plaintext)
		}
		if _, err := vault.Create(secret); err != nil {
			return 0, fmt.Errorf("re-encrypt v1.1 user secret %s: %w", id, err)
		}
		imported++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return imported, nil
}

func readLegacyEncryptionKeys(sourceHome, name string) ([][]byte, error) {
	sourceHome, err := canonicalDirectory(sourceHome)
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for _, directoryName := range []string{"auth", "session"} {
		authDirectory := filepath.Join(sourceHome, directoryName)
		if _, err := os.Lstat(authDirectory); errors.Is(err, os.ErrNotExist) {
			continue
		}
		authDirectory, err = canonicalDirectory(authDirectory)
		if err != nil {
			return nil, err
		}
		paths = append(paths, filepath.Join(authDirectory, "encryption.key"))
		backupDirectory := filepath.Join(authDirectory, ".key-backups")
		entries, err := os.ReadDir(backupDirectory)
		if err == nil {
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), "encryption.key") {
					names = append(names, entry.Name())
				}
			}
			sort.Strings(names)
			for _, entry := range names {
				paths = append(paths, filepath.Join(backupDirectory, entry))
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	keys := make([][]byte, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		key, err := readLegacyEncryptionKey(path, name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
		}
		fingerprint := fmt.Sprintf("%x", sha256.Sum256(key))
		if !seen[fingerprint] {
			seen[fingerprint] = true
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("no v1.1 encryption keys were found")
	}
	return keys, nil
}

func decryptWithLegacyKeys(keys [][]byte, encoded, purpose string) ([]byte, error) {
	var lastErr error
	for _, key := range keys {
		plaintext, err := decryptLegacySecret(key, encoded, purpose)
		if err == nil {
			return plaintext, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("no retained v1.1 encryption key authenticated the payload: %w", lastErr)
}

func readLegacyEncryptionKey(path, name string) ([]byte, error) {
	raw, err := readOptionalRegularFile(path)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if found && strings.TrimSpace(key) == name {
			decoded, err := decodeBase64Key(strings.TrimSpace(value))
			if err != nil {
				return nil, err
			}
			if len(decoded) < 16 {
				return nil, errors.New("v1.1 encryption key is shorter than 16 bytes")
			}
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("%s is missing", name)
}

func decryptLegacySecret(sourceKey []byte, encoded, purpose string) ([]byte, error) {
	if strings.HasPrefix(encoded, "v2:") {
		return decryptLegacyV2(sourceKey, strings.TrimPrefix(encoded, "v2:"), purpose)
	}
	return decryptLegacyFernet(sourceKey, encoded)
}

func decryptLegacyV2(sourceKey []byte, encoded, purpose string) ([]byte, error) {
	payload, err := decodeAnyBase64(encoded)
	if err != nil {
		return nil, err
	}
	const nonceSize = 12
	if len(payload) < nonceSize+16 {
		return nil, errors.New("v2 encrypted payload is truncated")
	}
	derived := hkdfSHA256(sourceKey, []byte("operon:aes-256-gcm:"+purpose))
	block, err := aes.NewCipher(derived)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, payload[:nonceSize], payload[nonceSize:], []byte("v2:"+purpose))
	if err != nil {
		return nil, errors.New("v2 encrypted payload authentication failed")
	}
	return plaintext, nil
}

func decryptLegacyFernet(sourceKey []byte, encoded string) ([]byte, error) {
	if len(sourceKey) != 32 {
		return nil, errors.New("Fernet key must decode to 32 bytes")
	}
	payload, err := decodeAnyBase64(encoded)
	if err != nil {
		return nil, err
	}
	if len(payload) < 1+8+aes.BlockSize+aes.BlockSize+sha256.Size || payload[0] != 0x80 {
		return nil, errors.New("Fernet payload is invalid or truncated")
	}
	signed, signature := payload[:len(payload)-sha256.Size], payload[len(payload)-sha256.Size:]
	mac := hmac.New(sha256.New, sourceKey[:16])
	_, _ = mac.Write(signed)
	if !hmac.Equal(mac.Sum(nil), signature) {
		return nil, errors.New("Fernet payload authentication failed")
	}
	iv := payload[9 : 9+aes.BlockSize]
	ciphertext := payload[9+aes.BlockSize : len(payload)-sha256.Size]
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, errors.New("Fernet ciphertext length is invalid")
	}
	block, err := aes.NewCipher(sourceKey[16:])
	if err != nil {
		return nil, err
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, ciphertext)
	padding := int(plaintext[len(plaintext)-1])
	if padding < 1 || padding > aes.BlockSize || padding > len(plaintext) {
		return nil, errors.New("Fernet padding is invalid")
	}
	for _, value := range plaintext[len(plaintext)-padding:] {
		if int(value) != padding {
			return nil, errors.New("Fernet padding is invalid")
		}
	}
	return plaintext[:len(plaintext)-padding], nil
}

func hkdfSHA256(inputKey, info []byte) []byte {
	extract := hmac.New(sha256.New, make([]byte, sha256.Size))
	_, _ = extract.Write(inputKey)
	pseudorandomKey := extract.Sum(nil)
	expand := hmac.New(sha256.New, pseudorandomKey)
	_, _ = expand.Write(info)
	_, _ = expand.Write([]byte{1})
	return expand.Sum(nil)
}

func decodeBase64Key(value string) ([]byte, error) {
	decoded, err := decodeAnyBase64(value)
	if err != nil {
		return nil, fmt.Errorf("decode v1.1 encryption key: %w", err)
	}
	return decoded, nil
}

func decodeAnyBase64(value string) ([]byte, error) {
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		if decoded, err := encoding.DecodeString(value); err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("value is not valid base64")
}

func removeLegacyKeyCopy(targetHome string) error {
	return os.Remove(filepath.Join(targetHome, "auth", "encryption.key"))
}
