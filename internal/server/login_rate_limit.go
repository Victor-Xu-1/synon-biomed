package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	loginFailureLimit      = 5
	loginFailureWindow     = 15 * time.Minute
	loginLockoutDuration   = 5 * time.Minute
	loginAttemptEntryLimit = 1024
)

type loginAttempt struct {
	windowStarted time.Time
	failures      int
	lockedUntil   time.Time
	lastSeen      time.Time
}

type loginRateLimiter struct {
	mu       sync.Mutex
	attempts map[string]loginAttempt
	now      func() time.Time
}

func newLoginRateLimiter() *loginRateLimiter {
	return &loginRateLimiter{attempts: make(map[string]loginAttempt), now: time.Now}
}

func (l *loginRateLimiter) RetryAfter(r *http.Request, username string) time.Duration {
	if l == nil {
		return 0
	}
	now := l.now().UTC()
	key := loginAttemptKey(r, username)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	attempt, ok := l.attempts[key]
	if !ok || !attempt.lockedUntil.After(now) {
		return 0
	}
	return attempt.lockedUntil.Sub(now)
}

func (l *loginRateLimiter) RecordFailure(r *http.Request, username string) {
	if l == nil {
		return
	}
	now := l.now().UTC()
	key := loginAttemptKey(r, username)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	attempt := l.attempts[key]
	if attempt.windowStarted.IsZero() || now.Sub(attempt.windowStarted) >= loginFailureWindow {
		attempt.windowStarted = now
		attempt.failures = 0
		attempt.lockedUntil = time.Time{}
	}
	attempt.failures++
	attempt.lastSeen = now
	if attempt.failures >= loginFailureLimit {
		attempt.lockedUntil = now.Add(loginLockoutDuration)
	}
	l.attempts[key] = attempt
	if len(l.attempts) > loginAttemptEntryLimit {
		l.evictOldest()
	}
}

func (l *loginRateLimiter) RecordSuccess(r *http.Request, username string) {
	if l == nil {
		return
	}
	key := loginAttemptKey(r, username)
	l.mu.Lock()
	delete(l.attempts, key)
	l.mu.Unlock()
}

func (l *loginRateLimiter) prune(now time.Time) {
	for key, attempt := range l.attempts {
		if attempt.lockedUntil.After(now) {
			continue
		}
		if attempt.lastSeen.IsZero() || now.Sub(attempt.lastSeen) >= loginFailureWindow {
			delete(l.attempts, key)
		}
	}
}

func (l *loginRateLimiter) evictOldest() {
	oldestKey := ""
	var oldest time.Time
	for key, attempt := range l.attempts {
		if oldestKey == "" || attempt.lastSeen.Before(oldest) {
			oldestKey = key
			oldest = attempt.lastSeen
		}
	}
	if oldestKey != "" {
		delete(l.attempts, oldestKey)
	}
}

func loginAttemptKey(r *http.Request, username string) string {
	host := "unknown"
	if r != nil {
		host = strings.TrimSpace(r.RemoteAddr)
		if parsed, _, err := net.SplitHostPort(host); err == nil {
			host = parsed
		}
		host = strings.Trim(host, "[]")
	}
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(username))))
	return host + ":" + hex.EncodeToString(digest[:16])
}
