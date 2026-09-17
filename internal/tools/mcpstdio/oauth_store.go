package mcpstdio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	secretstore "synon-go/internal/persistence/secrets"
	"time"
)

func oauthTokenPath(root string, serverName string) string {
	return filepath.Join(root, ".synon", "mcp-oauth", NormalizeName(serverName)+".json")
}

var oauthTokenCacheMu sync.Mutex

const oauthTokenVaultUserID = "runtime"

func saveOAuthToken(root string, serverName string, token oauthTokenCache) error {
	oauthTokenCacheMu.Lock()
	defer oauthTokenCacheMu.Unlock()
	return saveOAuthTokenLocked(root, serverName, token)
}

func loadOAuthAccessToken(root string, serverName string) (string, bool) {
	token, ok := loadOAuthToken(root, serverName)
	if !ok {
		return "", false
	}
	return token.AccessToken, true
}

func loadOAuthAccessTokenForRemote(root string, config ServerConfig) (string, bool) {
	identity := strings.TrimSpace(config.ConnectorID)
	if identity == "" {
		return "", false
	}
	token, ok := loadOAuthToken(root, identity)
	if !ok {
		return "", false
	}
	origin, err := canonicalRemoteMCPOrigin(config.URL)
	if err != nil || token.Origin == "" || token.Origin != origin {
		return "", false
	}
	return token.AccessToken, true
}

func loadOAuthToken(root string, serverName string) (oauthTokenCache, bool) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(serverName) == "" {
		return oauthTokenCache{}, false
	}
	oauthTokenCacheMu.Lock()
	defer oauthTokenCacheMu.Unlock()
	store := oauthTokenVault(root)
	secretID := oauthTokenSecretID(serverName)
	secret, found, err := store.ResolveForUser(secretID, oauthTokenVaultUserID)
	if err != nil {
		return oauthTokenCache{}, false
	}
	if !found {
		token, legacyFound := loadLegacyOAuthToken(root, serverName)
		if !legacyFound || saveOAuthTokenLocked(root, serverName, token) != nil {
			return oauthTokenCache{}, false
		}
		secret, found, err = store.ResolveForUser(secretID, oauthTokenVaultUserID)
		if err != nil || !found {
			return oauthTokenCache{}, false
		}
	} else if err := removeLegacyOAuthToken(root, serverName); err != nil {
		return oauthTokenCache{}, false
	}
	token, ok := oauthTokenFromSecret(secret)
	if !ok {
		return oauthTokenCache{}, false
	}
	if strings.TrimSpace(token.AccessToken) == "" || (token.ExpiresAt > 0 && token.ExpiresAt <= time.Now().Unix()) {
		_, _ = store.DeleteForUser(secretID, oauthTokenVaultUserID)
		return oauthTokenCache{}, false
	}
	return token, true
}

func saveOAuthTokenForRemoteOrigin(root, serverName, rawURL string, token oauthTokenCache) error {
	origin, err := canonicalRemoteMCPOrigin(rawURL)
	if err != nil {
		return err
	}
	token.Origin = origin
	return saveOAuthToken(root, serverName, token)
}

func saveOAuthTokenLocked(root string, serverName string, token oauthTokenCache) error {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(serverName) == "" {
		return errors.New("OAuth token root and server are required")
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return errors.New("OAuth access token is required")
	}
	store := oauthTokenVault(root)
	secretID := oauthTokenSecretID(serverName)
	credentials := map[string]string{
		"accessToken": token.AccessToken,
		"tokenType":   token.TokenType,
		"expiresAt":   strconv.FormatInt(token.ExpiresAt, 10),
		"origin":      token.Origin,
	}
	_, found, err := store.ResolveForUser(secretID, oauthTokenVaultUserID)
	if err != nil {
		return err
	}
	if found {
		_, err = store.UpdateForUser(secretID, oauthTokenVaultUserID, func(secret *secretstore.Secret) error {
			secret.Credentials = credentials
			return nil
		})
	} else {
		_, err = store.Create(secretstore.Secret{
			ID: secretID, UserID: oauthTokenVaultUserID, Provider: "mcp-oauth",
			Name: NormalizeName(serverName), Credentials: credentials,
		})
	}
	if err != nil {
		return err
	}
	return removeLegacyOAuthToken(root, serverName)
}

func oauthTokenVault(root string) *secretstore.Store {
	return secretstore.New(filepath.Join(root, ".synon", "mcp-oauth-vault"))
}

func oauthTokenSecretID(serverName string) string {
	sum := sha256.Sum256([]byte(NormalizeName(serverName)))
	return "mcp-oauth-" + hex.EncodeToString(sum[:16])
}

func oauthTokenFromSecret(secret secretstore.Secret) (oauthTokenCache, bool) {
	accessToken := strings.TrimSpace(secret.Credentials["accessToken"])
	if accessToken == "" {
		return oauthTokenCache{}, false
	}
	expiresAt, err := strconv.ParseInt(secret.Credentials["expiresAt"], 10, 64)
	if err != nil {
		return oauthTokenCache{}, false
	}
	return oauthTokenCache{
		AccessToken: accessToken,
		TokenType:   secret.Credentials["tokenType"],
		ExpiresAt:   expiresAt,
		Origin:      secret.Credentials["origin"],
	}, true
}

func loadLegacyOAuthToken(root string, serverName string) (oauthTokenCache, bool) {
	path := oauthTokenPath(root, serverName)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return oauthTokenCache{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return oauthTokenCache{}, false
	}
	var token oauthTokenCache
	if err := json.Unmarshal(raw, &token); err != nil || strings.TrimSpace(token.AccessToken) == "" {
		return oauthTokenCache{}, false
	}
	return token, true
}

func removeLegacyOAuthToken(root string, serverName string) error {
	path := oauthTokenPath(root, serverName)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove legacy MCP OAuth token: %w", err)
	}
	return nil
}

// MigrateLegacyOAuthTokens removes every legacy plaintext bearer-token file.
// It is intended to run before the HTTP server starts accepting requests.
func MigrateLegacyOAuthTokens(root string) error {
	root = strings.TrimSpace(root)
	if root == "" {
		return errors.New("OAuth token root is required")
	}
	directory := filepath.Join(root, ".synon", "mcp-oauth")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy MCP OAuth directory: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("legacy MCP OAuth token %q is not a regular file", entry.Name())
		}
		serverName := strings.TrimSuffix(entry.Name(), ".json")
		token, found := loadLegacyOAuthToken(root, serverName)
		if !found {
			return fmt.Errorf("legacy MCP OAuth token %q is invalid", entry.Name())
		}
		if token.ExpiresAt > 0 && token.ExpiresAt <= time.Now().Unix() {
			if err := removeLegacyOAuthToken(root, serverName); err != nil {
				return err
			}
			continue
		}
		if err := saveOAuthToken(root, serverName, token); err != nil {
			return fmt.Errorf("migrate legacy MCP OAuth token %q: %w", entry.Name(), err)
		}
	}
	return nil
}
