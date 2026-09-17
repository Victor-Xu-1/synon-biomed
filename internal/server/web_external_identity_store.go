package server

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"
)

type webExternalIdentityClaims struct {
	Provider             string
	Issuer               string
	Subject              string
	Email                string
	EmailVerified        bool
	DisplayName          string
	RequireVerifiedEmail bool
}

func (s *webAccountStore) UserByID(accountID string) (synonLinkAuthUser, bool) {
	if s == nil {
		return synonLinkAuthUser{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, account := range s.accounts {
		if account.ID == accountID && account.Status == "active" {
			return account.publicUser(), true
		}
	}
	return synonLinkAuthUser{}, false
}

func (s *webAccountStore) ResolveExternalIdentity(
	claims webExternalIdentityClaims,
) (synonLinkAuthUser, bool, error) {
	if s == nil {
		return synonLinkAuthUser{}, false, errors.New("Web account store is unavailable")
	}
	claims.Provider = strings.ToLower(strings.TrimSpace(claims.Provider))
	claims.Issuer = strings.TrimSpace(claims.Issuer)
	claims.Subject = strings.TrimSpace(claims.Subject)
	claims.Email = strings.TrimSpace(norm.NFKC.String(claims.Email))
	if !validExternalIdentityPart(claims.Provider, 128) ||
		!validExternalIdentityPart(claims.Issuer, 2048) ||
		!validExternalIdentityPart(claims.Subject, 1024) {
		return synonLinkAuthUser{}, false, &webAccountValidationError{code: "EXTERNAL_IDENTITY_INVALID"}
	}
	normalizedEmail, err := validatedExternalIdentityEmail(claims)
	if err != nil {
		return synonLinkAuthUser{}, false, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	s.mu.Lock()
	defer s.mu.Unlock()

	for identityIndex := range s.identities {
		identity := s.identities[identityIndex]
		if identity.Provider != claims.Provider || identity.Issuer != claims.Issuer ||
			identity.Subject != claims.Subject {
			continue
		}
		return s.updateResolvedExternalIdentityLocked(identityIndex, claims, normalizedEmail, now)
	}
	if normalizedEmail != "" {
		for _, account := range s.accounts {
			if account.NormalizedEmail == normalizedEmail {
				return synonLinkAuthUser{}, false, &webAccountValidationError{code: "EXTERNAL_EMAIL_LINK_REQUIRED"}
			}
		}
	}
	return s.createExternalIdentityLocked(claims, normalizedEmail, now)
}

func validatedExternalIdentityEmail(claims webExternalIdentityClaims) (string, error) {
	if claims.Email == "" {
		if claims.RequireVerifiedEmail {
			return "", &webAccountValidationError{code: "EXTERNAL_VERIFIED_EMAIL_REQUIRED"}
		}
		return "", nil
	}
	if len(claims.Email) > webAccountEmailMax || !claims.EmailVerified || !webAccountEmailPattern.MatchString(claims.Email) {
		return "", &webAccountValidationError{code: "EXTERNAL_VERIFIED_EMAIL_REQUIRED"}
	}
	return normalizeWebAccountEmail(claims.Email), nil
}

func validExternalIdentityPart(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes &&
		strings.IndexFunc(value, func(character rune) bool { return character < 32 || character == 127 }) < 0
}

func (s *webAccountStore) updateResolvedExternalIdentityLocked(
	identityIndex int,
	claims webExternalIdentityClaims,
	normalizedEmail string,
	now string,
) (synonLinkAuthUser, bool, error) {
	identity := s.identities[identityIndex]
	for accountIndex := range s.accounts {
		account := s.accounts[accountIndex]
		if account.ID != identity.AccountID || account.Status != "active" {
			continue
		}
		if normalizedEmail != "" {
			for _, other := range s.accounts {
				if other.ID != account.ID && other.NormalizedEmail == normalizedEmail {
					return synonLinkAuthUser{}, false, &webAccountValidationError{code: "EXTERNAL_EMAIL_LINK_REQUIRED"}
				}
			}
		}
		nextAccounts := append([]storedWebAccount(nil), s.accounts...)
		nextIdentities := append([]storedWebExternalIdentity(nil), s.identities...)
		identity = nextIdentities[identityIndex]
		account = nextAccounts[accountIndex]
		identity.LastUsedAt = now
		if normalizedEmail != "" {
			identity.Email = claims.Email
			identity.EmailVerified = true
			account.Email = claims.Email
			account.NormalizedEmail = normalizedEmail
			account.EmailVerified = true
		}
		account.UpdatedAt = now
		nextIdentities[identityIndex] = identity
		nextAccounts[accountIndex] = account
		if err := s.persistLocked(nextAccounts, nextIdentities); err != nil {
			return synonLinkAuthUser{}, false, err
		}
		s.accounts = nextAccounts
		s.identities = nextIdentities
		user := account.publicUser()
		user.Provider = claims.Provider
		return user, false, nil
	}
	return synonLinkAuthUser{}, false, &webAccountValidationError{code: "ACCOUNT_DISABLED"}
}

func (s *webAccountStore) createExternalIdentityLocked(
	claims webExternalIdentityClaims,
	normalizedEmail string,
	now string,
) (synonLinkAuthUser, bool, error) {
	displayName := externalIdentityDisplayName(claims.DisplayName, claims.Email, claims.Provider)
	username := s.availableExternalIdentityUsernameLocked(displayName, claims.Provider)
	account := storedWebAccount{
		ID: "synonbiomed-web-" + uuid.NewString(), Username: username, DisplayName: displayName,
		Email: claims.Email, NormalizedUsername: normalizeWebAccountName(username),
		NormalizedEmail: normalizedEmail, EmailVerified: normalizedEmail != "", Status: "active",
		CreatedAt: now, UpdatedAt: now,
	}
	identity := storedWebExternalIdentity{
		ID: "synonbiomed-identity-" + uuid.NewString(), AccountID: account.ID,
		Provider: claims.Provider, Issuer: claims.Issuer, Subject: claims.Subject,
		Email: claims.Email, EmailVerified: normalizedEmail != "", CreatedAt: now, LastUsedAt: now,
	}
	nextAccounts := append(append([]storedWebAccount(nil), s.accounts...), account)
	nextIdentities := append(append([]storedWebExternalIdentity(nil), s.identities...), identity)
	if err := s.persistLocked(nextAccounts, nextIdentities); err != nil {
		return synonLinkAuthUser{}, false, err
	}
	s.accounts = nextAccounts
	s.identities = nextIdentities
	user := account.publicUser()
	user.Provider = claims.Provider
	return user, true, nil
}

func (s *webAccountStore) LoginMethods(accountID string) []string {
	if s == nil {
		return []string{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	methods := make([]string, 0, 3)
	for _, account := range s.accounts {
		if account.ID == accountID && account.PasswordHash != "" {
			methods = append(methods, "local")
			break
		}
	}
	for _, identity := range s.identities {
		if identity.AccountID == accountID && !slices.Contains(methods, identity.Provider) {
			methods = append(methods, identity.Provider)
		}
	}
	slices.Sort(methods)
	return methods
}

func (s *webAccountStore) availableExternalIdentityUsernameLocked(displayName, provider string) string {
	base := strings.TrimSpace(displayName)
	if base == "" {
		base = externalIdentityFallbackName(provider)
	}
	used := map[string]struct{}{s.normalizedBootstrap: {}}
	for _, account := range s.accounts {
		used[account.NormalizedUsername] = struct{}{}
	}
	if _, exists := used[normalizeWebAccountName(base)]; !exists {
		return base
	}
	for suffix := 2; suffix < 10000; suffix++ {
		candidate := fmt.Sprintf("%s %d", base, suffix)
		if _, exists := used[normalizeWebAccountName(candidate)]; !exists {
			return candidate
		}
	}
	return externalIdentityFallbackName(provider) + " " + uuid.NewString()[:8]
}

func externalIdentityDisplayName(value, email, provider string) string {
	name := strings.TrimSpace(norm.NFKC.String(value))
	if name == "" {
		name = strings.TrimSpace(strings.SplitN(email, "@", 2)[0])
	}
	if len(utf16.Encode([]rune(name))) < webAccountNameMin ||
		strings.IndexFunc(name, func(value rune) bool { return value < 32 || value == 127 }) >= 0 {
		name = externalIdentityFallbackName(provider)
	}
	for len(utf16.Encode([]rune(name))) > webAccountNameMax {
		runes := []rune(name)
		name = strings.TrimSpace(string(runes[:len(runes)-1]))
	}
	return name
}

func externalIdentityFallbackName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case webGoogleProviderID:
		return "Google user"
	case webAppleProviderID:
		return "Apple user"
	case webWeChatProviderID:
		return "WeChat user"
	default:
		return "Synon user"
	}
}
