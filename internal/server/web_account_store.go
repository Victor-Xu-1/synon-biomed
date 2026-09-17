package server

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/crypto/scrypt"
	"golang.org/x/text/unicode/norm"
)

const (
	webAccountsVersion    = 2
	webAccountsV1Version  = 1
	webAccountSaltBytes   = 16
	webAccountHashBytes   = 32
	webAccountScryptN     = 1 << 14
	webAccountScryptR     = 8
	webAccountScryptP     = 1
	webAccountNameMin     = 2
	webAccountNameMax     = 64
	webAccountEmailMax    = 254
	webAccountPasswordMin = 8
	webAccountPasswordMax = 128
)

var webAccountEmailPattern = regexp.MustCompile("^[^\\s@]+@[^\\s@]+\\.[^\\s@]+$")

type webAccountValidationError struct {
	code string
}

func (e *webAccountValidationError) Error() string {
	return e.code
}

type storedWebAccount struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	DisplayName        string `json:"displayName"`
	Email              string `json:"email,omitempty"`
	NormalizedUsername string `json:"normalizedUsername"`
	NormalizedEmail    string `json:"normalizedEmail,omitempty"`
	PasswordSalt       string `json:"passwordSalt,omitempty"`
	PasswordHash       string `json:"passwordHash,omitempty"`
	EmailVerified      bool   `json:"emailVerified"`
	Status             string `json:"status"`
	CreatedAt          string `json:"createdAt"`
	UpdatedAt          string `json:"updatedAt"`
}

type storedWebExternalIdentity struct {
	ID            string `json:"id"`
	AccountID     string `json:"accountId"`
	Provider      string `json:"provider"`
	Issuer        string `json:"issuer"`
	Subject       string `json:"subject"`
	Email         string `json:"email,omitempty"`
	EmailVerified bool   `json:"emailVerified"`
	CreatedAt     string `json:"createdAt"`
	LastUsedAt    string `json:"lastUsedAt"`
}

type webAccountsDocument struct {
	Version    int                         `json:"version"`
	Accounts   []storedWebAccount          `json:"accounts"`
	Identities []storedWebExternalIdentity `json:"identities"`
}

type webAccountStore struct {
	mu                  sync.RWMutex
	path                string
	normalizedBootstrap string
	accounts            []storedWebAccount
	identities          []storedWebExternalIdentity
	dummySalt           []byte
	now                 func() time.Time
	random              io.Reader
}

func (s *webAccountStore) EmailForID(userID string) (string, bool) {
	if s == nil {
		return "", false
	}
	userID = strings.TrimSpace(userID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, account := range s.accounts {
		if account.ID == userID && strings.TrimSpace(account.Email) != "" {
			return strings.TrimSpace(account.Email), true
		}
	}
	return "", false
}

func (s *webAccountStore) EmailVerifiedForID(userID string) bool {
	if s == nil {
		return false
	}
	userID = strings.TrimSpace(userID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, account := range s.accounts {
		if account.ID == userID {
			return account.EmailVerified
		}
	}
	return false
}

func openWebAccountStore(dataDir, bootstrapUsername string) (*webAccountStore, error) {
	store := &webAccountStore{
		normalizedBootstrap: normalizeWebAccountName(bootstrapUsername),
		now:                 time.Now,
		random:              rand.Reader,
		dummySalt:           make([]byte, webAccountSaltBytes),
	}
	if _, err := io.ReadFull(store.random, store.dummySalt); err != nil {
		return nil, fmt.Errorf("initialize Web account timing salt: %w", err)
	}
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return store, nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create Web account data directory: %w", err)
	}
	store.path = filepath.Join(dataDir, "webui-accounts.json")
	raw, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		if err := store.persistLocked(nil, nil); err != nil {
			return nil, err
		}
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Web accounts: %w", err)
	}
	document, migrated, err := decodeWebAccountsDocument(raw)
	if err != nil {
		return nil, err
	}
	store.accounts = document.Accounts
	store.identities = document.Identities
	if migrated {
		if err := persistWebAccountV1Backup(store.path, raw); err != nil {
			return nil, err
		}
		if err := store.persistLocked(store.accounts, store.identities); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func decodeWebAccountsDocument(raw []byte) (webAccountsDocument, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document webAccountsDocument
	if err := decoder.Decode(&document); err != nil {
		return webAccountsDocument{}, false, fmt.Errorf("parse Web accounts: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return webAccountsDocument{}, false, errors.New("parse Web accounts: expected one JSON value")
	}
	migrated := document.Version == webAccountsV1Version
	if !migrated && document.Version != webAccountsVersion {
		return webAccountsDocument{}, false, fmt.Errorf("unsupported Web accounts version %d", document.Version)
	}
	seenNames := map[string]struct{}{}
	seenEmails := map[string]struct{}{}
	seenIDs := map[string]struct{}{}
	for index, account := range document.Accounts {
		if strings.TrimSpace(account.ID) == "" || account.Username == "" || account.DisplayName == "" ||
			account.NormalizedUsername != normalizeWebAccountName(account.Username) {
			return webAccountsDocument{}, false, fmt.Errorf("invalid Web account at index %d", index)
		}
		if _, duplicate := seenIDs[account.ID]; duplicate {
			return webAccountsDocument{}, false, fmt.Errorf("duplicate Web account id %q", account.ID)
		}
		if _, duplicate := seenNames[account.NormalizedUsername]; duplicate {
			return webAccountsDocument{}, false, fmt.Errorf("duplicate Web account username %q", account.Username)
		}
		if (account.PasswordSalt == "") != (account.PasswordHash == "") {
			return webAccountsDocument{}, false, fmt.Errorf("incomplete Web account password material at index %d", index)
		}
		if account.PasswordSalt != "" {
			salt, saltErr := base64.RawURLEncoding.DecodeString(account.PasswordSalt)
			hash, hashErr := base64.RawURLEncoding.DecodeString(account.PasswordHash)
			if saltErr != nil || hashErr != nil || len(salt) != webAccountSaltBytes || len(hash) != webAccountHashBytes {
				return webAccountsDocument{}, false, fmt.Errorf("invalid Web account password material at index %d", index)
			}
		}
		if _, err := time.Parse(time.RFC3339Nano, account.CreatedAt); err != nil {
			return webAccountsDocument{}, false, fmt.Errorf("invalid Web account timestamp at index %d", index)
		}
		if migrated {
			document.Accounts[index].Status = "active"
			document.Accounts[index].UpdatedAt = account.CreatedAt
		} else {
			if account.Status != "active" {
				return webAccountsDocument{}, false, fmt.Errorf("invalid Web account status at index %d", index)
			}
			if _, err := time.Parse(time.RFC3339Nano, account.UpdatedAt); err != nil {
				return webAccountsDocument{}, false, fmt.Errorf("invalid Web account update timestamp at index %d", index)
			}
		}
		if account.Email != "" {
			if account.NormalizedEmail != normalizeWebAccountEmail(account.Email) ||
				!webAccountEmailPattern.MatchString(account.Email) {
				return webAccountsDocument{}, false, fmt.Errorf("invalid Web account email at index %d", index)
			}
			if _, duplicate := seenEmails[account.NormalizedEmail]; duplicate {
				return webAccountsDocument{}, false, fmt.Errorf("duplicate Web account email %q", account.Email)
			}
			seenEmails[account.NormalizedEmail] = struct{}{}
		} else if account.NormalizedEmail != "" {
			return webAccountsDocument{}, false, fmt.Errorf("Web account normalized email without email at index %d", index)
		}
		seenIDs[account.ID] = struct{}{}
		seenNames[account.NormalizedUsername] = struct{}{}
	}
	if document.Accounts == nil {
		document.Accounts = []storedWebAccount{}
	}
	if migrated {
		document.Version = webAccountsVersion
		document.Identities = []storedWebExternalIdentity{}
	}
	if document.Identities == nil {
		document.Identities = []storedWebExternalIdentity{}
	}
	accountIDs := seenIDs
	seenIdentityIDs := map[string]struct{}{}
	seenSubjects := map[string]struct{}{}
	for index, identity := range document.Identities {
		key := identity.Provider + "\x00" + identity.Issuer + "\x00" + identity.Subject
		if strings.TrimSpace(identity.ID) == "" || strings.TrimSpace(identity.Provider) == "" ||
			strings.TrimSpace(identity.Issuer) == "" || strings.TrimSpace(identity.Subject) == "" {
			return webAccountsDocument{}, false, fmt.Errorf("invalid Web external identity at index %d", index)
		}
		if _, ok := accountIDs[identity.AccountID]; !ok {
			return webAccountsDocument{}, false, fmt.Errorf("Web external identity references unknown account at index %d", index)
		}
		if _, duplicate := seenIdentityIDs[identity.ID]; duplicate {
			return webAccountsDocument{}, false, fmt.Errorf("duplicate Web external identity id %q", identity.ID)
		}
		if _, duplicate := seenSubjects[key]; duplicate {
			return webAccountsDocument{}, false, errors.New("duplicate Web external identity subject")
		}
		if _, err := time.Parse(time.RFC3339Nano, identity.CreatedAt); err != nil {
			return webAccountsDocument{}, false, fmt.Errorf("invalid Web external identity creation timestamp at index %d", index)
		}
		if _, err := time.Parse(time.RFC3339Nano, identity.LastUsedAt); err != nil {
			return webAccountsDocument{}, false, fmt.Errorf("invalid Web external identity usage timestamp at index %d", index)
		}
		seenIdentityIDs[identity.ID] = struct{}{}
		seenSubjects[key] = struct{}{}
	}
	return document, migrated, nil
}

func (s *webAccountStore) Register(name, email, password string) (synonLinkAuthUser, error) {
	displayName, normalizedName, normalizedEmail, err := validateWebAccountInput(name, email, password)
	if err != nil {
		return synonLinkAuthUser{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if normalizedName == s.normalizedBootstrap {
		return synonLinkAuthUser{}, &webAccountValidationError{code: "USERNAME_EXISTS"}
	}
	for _, account := range s.accounts {
		if account.NormalizedUsername == normalizedName {
			return synonLinkAuthUser{}, &webAccountValidationError{code: "USERNAME_EXISTS"}
		}
		if normalizedEmail != "" && account.NormalizedEmail == normalizedEmail {
			return synonLinkAuthUser{}, &webAccountValidationError{code: "EMAIL_EXISTS"}
		}
	}
	salt := make([]byte, webAccountSaltBytes)
	if _, err := io.ReadFull(s.random, salt); err != nil {
		return synonLinkAuthUser{}, fmt.Errorf("generate Web account salt: %w", err)
	}
	hash, err := deriveWebAccountPassword(password, salt)
	if err != nil {
		return synonLinkAuthUser{}, err
	}
	cleanEmail := strings.TrimSpace(norm.NFKC.String(email))
	account := storedWebAccount{
		ID:                 "synonbiomed-web-" + uuid.NewString(),
		Username:           displayName,
		DisplayName:        displayName,
		Email:              cleanEmail,
		NormalizedUsername: normalizedName,
		NormalizedEmail:    normalizedEmail,
		PasswordSalt:       base64.RawURLEncoding.EncodeToString(salt),
		PasswordHash:       base64.RawURLEncoding.EncodeToString(hash),
		EmailVerified:      false,
		Status:             "active",
		CreatedAt:          s.now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:          s.now().UTC().Format(time.RFC3339Nano),
	}
	next := append(append([]storedWebAccount(nil), s.accounts...), account)
	if err := s.persistLocked(next, s.identities); err != nil {
		return synonLinkAuthUser{}, err
	}
	s.accounts = next
	return account.publicUser(), nil
}

func (s *webAccountStore) Authenticate(username, password string) (synonLinkAuthUser, bool, error) {
	normalized := normalizeWebAccountName(username)
	s.mu.RLock()
	var candidate *storedWebAccount
	for index := range s.accounts {
		if s.accounts[index].NormalizedUsername == normalized {
			copy := s.accounts[index]
			candidate = &copy
			break
		}
	}
	dummySalt := append([]byte(nil), s.dummySalt...)
	s.mu.RUnlock()

	salt := dummySalt
	expected := make([]byte, webAccountHashBytes)
	if candidate != nil {
		var err error
		salt, err = base64.RawURLEncoding.DecodeString(candidate.PasswordSalt)
		if err != nil {
			return synonLinkAuthUser{}, false, fmt.Errorf("decode Web account salt: %w", err)
		}
		expected, err = base64.RawURLEncoding.DecodeString(candidate.PasswordHash)
		if err != nil {
			return synonLinkAuthUser{}, false, fmt.Errorf("decode Web account hash: %w", err)
		}
	}
	actual, err := deriveWebAccountPassword(password, salt)
	if err != nil {
		return synonLinkAuthUser{}, false, err
	}
	if candidate == nil || candidate.Status != "active" || candidate.PasswordHash == "" ||
		subtle.ConstantTimeCompare(actual, expected) != 1 {
		return synonLinkAuthUser{}, false, nil
	}
	return candidate.publicUser(), true, nil
}

func (s *webAccountStore) persistLocked(accounts []storedWebAccount, identities []storedWebExternalIdentity) error {
	if s.path == "" {
		return nil
	}
	document := webAccountsDocument{Version: webAccountsVersion, Accounts: accounts, Identities: identities}
	if document.Accounts == nil {
		document.Accounts = []storedWebAccount{}
	}
	if document.Identities == nil {
		document.Identities = []storedWebExternalIdentity{}
	}
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Web accounts: %w", err)
	}
	return replacePrivateJSONFile(s.path, append(raw, '\n'))
}

func validateWebAccountInput(name, email, password string) (string, string, string, error) {
	displayName := strings.TrimSpace(norm.NFKC.String(name))
	nameUnits := len(utf16.Encode([]rune(displayName)))
	if nameUnits < webAccountNameMin || nameUnits > webAccountNameMax ||
		strings.IndexFunc(displayName, func(value rune) bool { return value < 32 || value == 127 }) >= 0 {
		return "", "", "", &webAccountValidationError{code: "INVALID_NAME"}
	}
	passwordUnits := len(utf16.Encode([]rune(password)))
	if passwordUnits < webAccountPasswordMin || passwordUnits > webAccountPasswordMax {
		return "", "", "", &webAccountValidationError{code: "INVALID_PASSWORD"}
	}
	cleanEmail := strings.TrimSpace(norm.NFKC.String(email))
	if cleanEmail != "" &&
		(len(utf16.Encode([]rune(cleanEmail))) > webAccountEmailMax || !utf8.ValidString(cleanEmail) ||
			!webAccountEmailPattern.MatchString(cleanEmail)) {
		return "", "", "", &webAccountValidationError{code: "INVALID_EMAIL"}
	}
	return displayName, normalizeWebAccountName(displayName), normalizeWebAccountEmail(cleanEmail), nil
}

func normalizeWebAccountName(value string) string {
	return strings.ToLower(strings.TrimSpace(norm.NFKC.String(value)))
}

func normalizeWebAccountEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(norm.NFKC.String(value)))
}

func deriveWebAccountPassword(password string, salt []byte) ([]byte, error) {
	hash, err := scrypt.Key(
		[]byte(password),
		salt,
		webAccountScryptN,
		webAccountScryptR,
		webAccountScryptP,
		webAccountHashBytes,
	)
	if err != nil {
		return nil, fmt.Errorf("derive Web account password: %w", err)
	}
	return hash, nil
}

func (a storedWebAccount) publicUser() synonLinkAuthUser {
	return synonLinkAuthUser{
		ID:          a.ID,
		UserID:      a.ID,
		Username:    a.Username,
		DisplayName: a.DisplayName,
		Email:       a.Email,
		Provider:    "local",
	}
}

func persistWebAccountV1Backup(path string, raw []byte) error {
	backupPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".v1.bak"
	file, err := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create Web account v1 backup: %w", err)
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(backupPath)
		}
	}()
	if _, err := file.Write(raw); err != nil {
		return fmt.Errorf("write Web account v1 backup: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync Web account v1 backup: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close Web account v1 backup: %w", err)
	}
	remove = false
	return nil
}
