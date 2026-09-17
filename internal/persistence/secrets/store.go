package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

var vaultMagic = []byte("SYNSEC01")

var ErrSecretNotFound = errors.New("secret not found")

const DefaultUserID = "local"

type Secret struct {
	ID                    string            `json:"id"`
	UserID                string            `json:"userId,omitempty"`
	Provider              string            `json:"provider"`
	Name                  string            `json:"name,omitempty"`
	Value                 string            `json:"value,omitempty"`
	Credentials           map[string]string `json:"credentials,omitempty"`
	CredentialData        map[string]any    `json:"credentialData"`
	Description           string            `json:"description,omitempty"`
	DescriptionPresent    bool              `json:"descriptionPresent,omitempty"`
	CredentialType        string            `json:"credentialType,omitempty"`
	CredentialTypePresent bool              `json:"credentialTypePresent,omitempty"`
	Buckets               []string          `json:"buckets,omitempty"`
	BucketsPresent        bool              `json:"bucketsPresent,omitempty"`
	Region                string            `json:"region,omitempty"`
	RegionPresent         bool              `json:"regionPresent,omitempty"`
	CreatedAt             time.Time         `json:"createdAt"`
	UpdatedAt             time.Time         `json:"updatedAt"`
}

type CreateOptions struct {
	PreserveNameWhitespace bool
	Validate               func(existing []Secret, candidate *Secret) error
}

type UpdateOptions struct {
	SkipNoop               bool
	PreserveNameWhitespace bool
	Validate               func(existing []Secret, original Secret, candidate *Secret) error
}

type Store struct {
	mu  sync.Mutex
	dir string
	now func() time.Time
}

func New(root string) *Store {
	return &Store{dir: filepath.Join(root, "secrets"), now: time.Now}
}

func (s *Store) Create(secret Secret) (Secret, error) {
	return s.CreateWithOptions(secret, CreateOptions{})
}

// CreateWithOptions performs policy validation while the cross-process vault
// lock is held, so compatibility uniqueness checks cannot race with a writer.
func (s *Store) CreateWithOptions(secret Secret, options CreateOptions) (Secret, error) {
	release, err := s.begin()
	if err != nil {
		return Secret{}, err
	}
	defer release()
	secret.ID = strings.TrimSpace(secret.ID)
	secret.UserID = normalizedUserID(secret.UserID)
	secret.Provider = strings.TrimSpace(secret.Provider)
	if !options.PreserveNameWhitespace {
		secret.Name = strings.TrimSpace(secret.Name)
	}
	if secret.ID == "" || secret.Provider == "" {
		return Secret{}, errors.New("secret id and provider are required")
	}
	items, key, err := s.loadLocked()
	if err != nil {
		return Secret{}, err
	}
	for _, item := range items {
		if item.ID == secret.ID {
			return Secret{}, fmt.Errorf("secret %q already exists", secret.ID)
		}
	}
	if options.Validate != nil {
		existing := make([]Secret, len(items))
		for index := range items {
			existing[index] = cloneSecret(items[index])
		}
		if err := options.Validate(existing, &secret); err != nil {
			return Secret{}, err
		}
	}
	if err := normalizeSecretCredentials(&secret); err != nil {
		return Secret{}, err
	}
	inferSecretPresence(&secret)
	now := s.now().UTC()
	secret.CreatedAt, secret.UpdatedAt = now, now
	secret = cloneSecret(secret)
	items = append(items, secret)
	if err := s.saveLocked(items, key); err != nil {
		return Secret{}, err
	}
	return cloneSecret(secret), nil
}

func (s *Store) List() ([]Secret, error) {
	return s.list("")
}

func (s *Store) ListForUser(userID string) ([]Secret, error) {
	return s.list(normalizedUserID(userID))
}

func (s *Store) list(userID string) ([]Secret, error) {
	release, err := s.begin()
	if err != nil {
		return nil, err
	}
	defer release()
	items, _, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	filtered := make([]Secret, 0, len(items))
	for _, item := range items {
		item.UserID = normalizedUserID(item.UserID)
		if userID == "" || item.UserID == userID {
			filtered = append(filtered, cloneSecret(item))
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].Name == filtered[j].Name {
			return filtered[i].ID < filtered[j].ID
		}
		return filtered[i].Name < filtered[j].Name
	})
	return filtered, nil
}

func (s *Store) Resolve(id string) (Secret, bool, error) {
	return s.resolve(id, "", false)
}

func (s *Store) ResolveForUser(id, userID string) (Secret, bool, error) {
	return s.resolve(id, normalizedUserID(userID), true)
}

func (s *Store) resolve(id, userID string, scoped bool) (Secret, bool, error) {
	release, err := s.begin()
	if err != nil {
		return Secret{}, false, err
	}
	defer release()
	items, _, err := s.loadLocked()
	if err != nil {
		return Secret{}, false, err
	}
	for _, item := range items {
		item.UserID = normalizedUserID(item.UserID)
		if item.ID == id && (!scoped || item.UserID == userID) {
			return cloneSecret(item), true, nil
		}
	}
	return Secret{}, false, nil
}

func (s *Store) Update(id string, update func(*Secret) error) (Secret, error) {
	return s.update(id, "", false, update, UpdateOptions{})
}

func (s *Store) UpdateForUser(id, userID string, update func(*Secret) error) (Secret, error) {
	return s.update(id, normalizedUserID(userID), true, update, UpdateOptions{})
}

func (s *Store) UpdateForUserWithOptions(
	id, userID string, update func(*Secret) error, options UpdateOptions,
) (Secret, error) {
	return s.update(id, normalizedUserID(userID), true, update, options)
}

func (s *Store) update(id, userID string, scoped bool, update func(*Secret) error, options UpdateOptions) (Secret, error) {
	if update == nil {
		return Secret{}, errors.New("secret update function is required")
	}
	release, err := s.begin()
	if err != nil {
		return Secret{}, err
	}
	defer release()
	items, key, err := s.loadLocked()
	if err != nil {
		return Secret{}, err
	}
	for index := range items {
		items[index].UserID = normalizedUserID(items[index].UserID)
		if items[index].ID != id || (scoped && items[index].UserID != userID) {
			continue
		}
		candidate := cloneSecret(items[index])
		original := cloneSecret(candidate)
		originalCredentials := cloneCredentials(candidate.Credentials)
		originalCredentialData := cloneCredentialData(candidate.CredentialData)
		if err := update(&candidate); err != nil {
			return Secret{}, err
		}
		candidate.ID = items[index].ID
		candidate.UserID = items[index].UserID
		candidate.Provider = strings.TrimSpace(candidate.Provider)
		if !options.PreserveNameWhitespace {
			candidate.Name = strings.TrimSpace(candidate.Name)
		}
		if candidate.Provider == "" {
			return Secret{}, errors.New("secret provider is required")
		}
		if options.Validate != nil {
			existing := make([]Secret, len(items))
			for itemIndex := range items {
				existing[itemIndex] = cloneSecret(items[itemIndex])
			}
			if err := options.Validate(existing, original, &candidate); err != nil {
				return Secret{}, err
			}
		}
		candidate.CreatedAt = items[index].CreatedAt
		candidate.UpdatedAt = items[index].UpdatedAt
		credentialsChanged := !reflect.DeepEqual(candidate.Credentials, originalCredentials)
		credentialDataChanged := !reflect.DeepEqual(candidate.CredentialData, originalCredentialData)
		switch {
		case credentialDataChanged:
			if err := candidate.SetCredentialObject(candidate.CredentialData); err != nil {
				return Secret{}, err
			}
		case credentialsChanged:
			candidate.CredentialData = credentialDataFromStrings(candidate.Credentials)
		default:
			if err := normalizeSecretCredentials(&candidate); err != nil {
				return Secret{}, err
			}
		}
		inferSecretPresence(&candidate)
		if options.SkipNoop && reflect.DeepEqual(candidate, original) {
			return cloneSecret(original), nil
		}
		candidate.UpdatedAt = s.now().UTC()
		candidate = cloneSecret(candidate)
		items[index] = candidate
		if err := s.saveLocked(items, key); err != nil {
			return Secret{}, err
		}
		return cloneSecret(candidate), nil
	}
	return Secret{}, fmt.Errorf("%w: %q", ErrSecretNotFound, id)
}

func (s *Store) Delete(id string) (bool, error) {
	return s.delete(id, "", false)
}

func (s *Store) DeleteForUser(id, userID string) (bool, error) {
	return s.delete(id, normalizedUserID(userID), true)
}

func (s *Store) delete(id, userID string, scoped bool) (bool, error) {
	release, err := s.begin()
	if err != nil {
		return false, err
	}
	defer release()
	items, key, err := s.loadLocked()
	if err != nil {
		return false, err
	}
	next := make([]Secret, 0, len(items))
	removed := false
	for _, item := range items {
		item.UserID = normalizedUserID(item.UserID)
		if item.ID == id && (!scoped || item.UserID == userID) {
			removed = true
			continue
		}
		next = append(next, item)
	}
	if removed {
		if err := s.saveLocked(next, key); err != nil {
			return false, err
		}
	}
	return removed, nil
}

func (s *Store) loadLocked() ([]Secret, []byte, error) {
	key, err := s.loadKeyLocked()
	if err != nil {
		return nil, nil, err
	}
	raw, err := readRegularFile(filepath.Join(s.dir, "vault.enc"))
	if errors.Is(err, os.ErrNotExist) {
		return []Secret{}, key, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if len(raw) < len(vaultMagic) || !bytes.Equal(raw[:len(vaultMagic)], vaultMagic) {
		return nil, nil, errors.New("secret vault header is invalid")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	if len(raw) < len(vaultMagic)+gcm.NonceSize()+gcm.Overhead() {
		return nil, nil, errors.New("secret vault payload is truncated")
	}
	offset := len(vaultMagic)
	nonce := raw[offset : offset+gcm.NonceSize()]
	plain, err := gcm.Open(nil, nonce, raw[offset+gcm.NonceSize():], vaultMagic)
	if err != nil {
		return nil, nil, errors.New("secret vault authentication failed")
	}
	var items []Secret
	if err := json.Unmarshal(plain, &items); err != nil {
		return nil, nil, err
	}
	for index := range items {
		if err := normalizeSecretCredentials(&items[index]); err != nil {
			return nil, nil, fmt.Errorf("normalize encrypted secret %q credentials: %w", items[index].ID, err)
		}
		inferSecretPresence(&items[index])
	}
	return items, key, nil
}

func (s *Store) loadKeyLocked() ([]byte, error) {
	path := filepath.Join(s.dir, "master.key")
	key, err := readRegularFile(path)
	if err == nil {
		if len(key) != 32 {
			return nil, errors.New("secret vault key length is invalid")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	tmp, err := writeTemporary(s.dir, keyTempBase, key)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := atomicPublish(tmp, path, false); err != nil {
		return nil, fmt.Errorf("publish secret vault key: %w", err)
	}
	ok = true
	return key, nil
}

func (s *Store) saveLocked(items []Secret, key []byte) error {
	plain, err := json.Marshal(items)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	raw := append(append(append([]byte{}, vaultMagic...), nonce...), gcm.Seal(nil, nonce, plain, vaultMagic)...)
	tmp, err := writeTemporary(s.dir, vaultTempBase, raw)
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, "vault.enc")
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := atomicPublish(tmp, path, true); err != nil {
		return err
	}
	ok = true
	return nil
}

func normalizedUserID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultUserID
	}
	return value
}

func cloneSecret(secret Secret) Secret {
	secret.Credentials = cloneCredentials(secret.Credentials)
	secret.CredentialData = cloneCredentialData(secret.CredentialData)
	if secret.Buckets != nil {
		secret.Buckets = append([]string{}, secret.Buckets...)
	}
	return secret
}

func (secret Secret) CredentialObject() map[string]any {
	if secret.CredentialData != nil {
		return cloneCredentialData(secret.CredentialData)
	}
	return credentialDataFromStrings(secret.Credentials)
}

func (secret *Secret) SetCredentialObject(credentials map[string]any) error {
	if secret == nil {
		return errors.New("secret is required")
	}
	if credentials == nil {
		secret.CredentialData = nil
		secret.Credentials = nil
		return nil
	}
	encoded, err := json.Marshal(credentials)
	if err != nil {
		return fmt.Errorf("encode credential object: %w", err)
	}
	var copied map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&copied); err != nil {
		return fmt.Errorf("decode credential object: %w", err)
	}
	derived := make(map[string]string, len(copied))
	for key, value := range copied {
		if text, ok := value.(string); ok {
			derived[key] = text
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encode credential field %q: %w", key, err)
		}
		derived[key] = string(raw)
	}
	secret.CredentialData = copied
	secret.Credentials = derived
	return nil
}

func normalizeSecretCredentials(secret *Secret) error {
	if secret.CredentialData != nil {
		return secret.SetCredentialObject(secret.CredentialData)
	}
	secret.CredentialData = credentialDataFromStrings(secret.Credentials)
	return nil
}

func credentialDataFromStrings(credentials map[string]string) map[string]any {
	if credentials == nil {
		return nil
	}
	result := make(map[string]any, len(credentials))
	for key, value := range credentials {
		result[key] = value
	}
	return result
}

func cloneCredentialData(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = cloneCredentialValue(value)
	}
	return cloned
}

func cloneCredentialValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneCredentialData(typed)
	case []any:
		cloned := make([]any, len(typed))
		for index := range typed {
			cloned[index] = cloneCredentialValue(typed[index])
		}
		return cloned
	default:
		return typed
	}
}

func inferSecretPresence(secret *Secret) {
	if secret.Description != "" {
		secret.DescriptionPresent = true
	}
	if secret.CredentialType != "" {
		secret.CredentialTypePresent = true
	}
	if secret.Buckets != nil {
		secret.BucketsPresent = true
	}
	if secret.Region != "" {
		secret.RegionPresent = true
	}
}

func cloneCredentials(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
