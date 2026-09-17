package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

const (
	webAuthTransactionCookieName = "synon_auth_tx"
	webAuthTransactionCookiePath = "/api/auth/"
	webAuthTransactionTTL        = 10 * time.Minute
	webAuthTransactionLimit      = 256
)

var errWebAuthTransactionInvalid = errors.New("external authentication transaction is missing, expired, or already used")

type webAuthTransaction struct {
	ProviderID   string
	State        string
	CookieToken  string
	Nonce        string
	PKCEVerifier string
	RedirectURL  string
	ReturnTo     string
	Remember     bool
	ExpiresAt    time.Time
}

type webAuthTransactionStore struct {
	mu           sync.Mutex
	transactions map[string]webAuthTransaction
	now          func() time.Time
	random       io.Reader
}

func newWebAuthTransactionStore() *webAuthTransactionStore {
	return &webAuthTransactionStore{
		transactions: map[string]webAuthTransaction{},
		now:          time.Now,
		random:       rand.Reader,
	}
}

func (s *webAuthTransactionStore) Create(
	providerID, redirectURL, returnTo string,
	remember bool,
) (webAuthTransaction, error) {
	state, err := s.randomSecret()
	if err != nil {
		return webAuthTransaction{}, err
	}
	cookieToken, err := s.randomSecret()
	if err != nil {
		return webAuthTransaction{}, err
	}
	nonce, err := s.randomSecret()
	if err != nil {
		return webAuthTransaction{}, err
	}
	verifier, err := s.randomSecret()
	if err != nil {
		return webAuthTransaction{}, err
	}
	transaction := webAuthTransaction{
		ProviderID: strings.TrimSpace(providerID), State: state, CookieToken: cookieToken,
		Nonce: nonce, PKCEVerifier: verifier, RedirectURL: redirectURL,
		ReturnTo: returnTo, Remember: remember,
		ExpiresAt: s.now().UTC().Add(webAuthTransactionTTL),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.compactLocked()
	if len(s.transactions) >= webAuthTransactionLimit {
		var oldestKey string
		var oldest time.Time
		for key, candidate := range s.transactions {
			if oldestKey == "" || candidate.ExpiresAt.Before(oldest) {
				oldestKey, oldest = key, candidate.ExpiresAt
			}
		}
		delete(s.transactions, oldestKey)
	}
	s.transactions[webSecretHash(state)] = transaction
	return transaction, nil
}

func (s *webAuthTransactionStore) Consume(state, cookieToken string) (webAuthTransaction, error) {
	state = strings.TrimSpace(state)
	cookieToken = strings.TrimSpace(cookieToken)
	if state == "" || cookieToken == "" {
		return webAuthTransaction{}, errWebAuthTransactionInvalid
	}
	key := webSecretHash(state)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.compactLocked()
	transaction, ok := s.transactions[key]
	if !ok {
		return webAuthTransaction{}, errWebAuthTransactionInvalid
	}
	if !s.now().UTC().Before(transaction.ExpiresAt) {
		delete(s.transactions, key)
		return webAuthTransaction{}, errWebAuthTransactionInvalid
	}
	if subtle.ConstantTimeCompare([]byte(webSecretHash(cookieToken)), []byte(webSecretHash(transaction.CookieToken))) != 1 ||
		subtle.ConstantTimeCompare([]byte(webSecretHash(state)), []byte(webSecretHash(transaction.State))) != 1 {
		return webAuthTransaction{}, errWebAuthTransactionInvalid
	}
	delete(s.transactions, key)
	return transaction, nil
}

func (s *webAuthTransactionStore) compactLocked() {
	now := s.now().UTC()
	for key, transaction := range s.transactions {
		if !now.Before(transaction.ExpiresAt) {
			delete(s.transactions, key)
		}
	}
}

func (s *webAuthTransactionStore) randomSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(s.random, raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func webPKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
