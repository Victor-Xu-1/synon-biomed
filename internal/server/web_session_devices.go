package server

import (
	"crypto/subtle"
	"errors"
	"slices"
	"strings"
	"time"
)

type webDeviceProjection struct {
	view      webSessionView
	createdAt time.Time
	lastSeen  time.Time
	expiresAt time.Time
}

func (s *webSessionStore) List(accountID, currentToken string) ([]webSessionView, error) {
	if s == nil {
		return nil, errors.New("Web session store is unavailable")
	}
	currentHash := ""
	if strings.TrimSpace(currentToken) != "" {
		currentHash = webSecretHash(currentToken)
	}
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	previousSessions, previousEvents := s.snapshotLocked()
	if s.compactExpiredLocked(now) {
		if err := s.persistLocked(); err != nil {
			s.restoreLocked(previousSessions, previousEvents)
			return nil, err
		}
	}
	devices := make(map[string]*webDeviceProjection)
	for _, session := range s.sessions {
		if session.AccountID == accountID {
			projectWebSessionDevice(devices, session, currentHash)
		}
	}
	views := make([]webSessionView, 0, len(devices))
	for _, device := range devices {
		views = append(views, device.view)
	}
	slices.SortFunc(views, func(left, right webSessionView) int {
		if left.Current != right.Current {
			if left.Current {
				return -1
			}
			return 1
		}
		return strings.Compare(right.LastSeenAt, left.LastSeenAt)
	})
	return views, nil
}

func projectWebSessionDevice(devices map[string]*webDeviceProjection, session storedWebSession, currentHash string) {
	legacy := !validWebSecretHash(session.DeviceIDHash)
	deviceID := webSessionDeviceID(session)
	createdAt, _ := time.Parse(time.RFC3339Nano, session.CreatedAt)
	lastSeenAt, _ := time.Parse(time.RFC3339Nano, session.LastSeenAt)
	expiresAt, _ := time.Parse(time.RFC3339Nano, session.ExpiresAt)
	current := currentHash != "" && subtle.ConstantTimeCompare([]byte(session.TokenHash), []byte(currentHash)) == 1
	projection := devices[deviceID]
	if projection == nil {
		devices[deviceID] = &webDeviceProjection{
			view: webSessionView{
				ID: deviceID, AuthMethod: session.AuthMethod, CreatedAt: session.CreatedAt,
				LastSeenAt: session.LastSeenAt, ExpiresAt: session.ExpiresAt,
				Remembered: session.Remembered, UserAgent: session.UserAgent,
				IPAddress: session.IPAddress, NetworkClass: session.NetworkClass, Current: current, Legacy: legacy,
			},
			createdAt: createdAt, lastSeen: lastSeenAt, expiresAt: expiresAt,
		}
		return
	}
	if createdAt.Before(projection.createdAt) {
		projection.createdAt = createdAt
		projection.view.CreatedAt = session.CreatedAt
	}
	if lastSeenAt.After(projection.lastSeen) {
		projection.lastSeen = lastSeenAt
		projection.view.LastSeenAt = session.LastSeenAt
		projection.view.AuthMethod = session.AuthMethod
		projection.view.UserAgent = session.UserAgent
		projection.view.IPAddress = session.IPAddress
		projection.view.NetworkClass = session.NetworkClass
	}
	if expiresAt.After(projection.expiresAt) {
		projection.expiresAt = expiresAt
		projection.view.ExpiresAt = session.ExpiresAt
	}
	projection.view.Remembered = projection.view.Remembered || session.Remembered
	projection.view.Current = projection.view.Current || current
}

func webSessionDeviceID(session storedWebSession) string {
	identity := session.DeviceIDHash
	if !validWebSecretHash(identity) {
		identity = webSecretHash("legacy-session-device\x00" + session.ID)
	}
	return "device-" + webSecretHash("web-device\x00"+identity)
}

func (s *webSessionStore) BindCurrentDevice(accountID, currentToken string, metadata webSessionMetadata) error {
	if s == nil {
		return errors.New("Web session store is unavailable")
	}
	if !validWebSecretHash(metadata.DeviceIDHash) {
		return errors.New("Web device identity is invalid")
	}
	currentHash := webSecretHash(currentToken)
	s.mu.Lock()
	defer s.mu.Unlock()
	currentIndex := -1
	for index := range s.sessions {
		if s.sessions[index].AccountID == accountID &&
			subtle.ConstantTimeCompare([]byte(s.sessions[index].TokenHash), []byte(currentHash)) == 1 {
			currentIndex = index
			break
		}
	}
	if currentIndex < 0 {
		return errSynonLinkInvalidSession
	}
	previousSessions, previousEvents := s.snapshotLocked()
	changed := false
	if s.sessions[currentIndex].DeviceIDHash == "" {
		s.sessions[currentIndex].DeviceIDHash = metadata.DeviceIDHash
		s.sessions[currentIndex].IPAddress = metadata.IPAddress
		s.sessions[currentIndex].NetworkClass = metadata.NetworkClass
		changed = true
	} else {
		deviceIDHash := s.sessions[currentIndex].DeviceIDHash
		if deviceIDHash != metadata.DeviceIDHash {
			s.sessions[currentIndex].DeviceIDHash = metadata.DeviceIDHash
			deviceIDHash = metadata.DeviceIDHash
			changed = true
		}
		for index := range s.sessions {
			if s.sessions[index].AccountID == accountID && s.sessions[index].DeviceIDHash == deviceIDHash &&
				(s.sessions[index].IPAddress != metadata.IPAddress ||
					s.sessions[index].NetworkClass != metadata.NetworkClass) {
				s.sessions[index].IPAddress = metadata.IPAddress
				s.sessions[index].NetworkClass = metadata.NetworkClass
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	if err := s.persistLocked(); err != nil {
		s.restoreLocked(previousSessions, previousEvents)
		return err
	}
	return nil
}

func (s *webSessionStore) RevokeDevice(accountID, deviceID, currentToken string) (int, bool, error) {
	if s == nil {
		return 0, false, errors.New("Web session store is unavailable")
	}
	currentHash := webSecretHash(currentToken)
	s.mu.Lock()
	defer s.mu.Unlock()
	targetDeviceID := strings.TrimSpace(deviceID)
	for _, session := range s.sessions {
		if session.AccountID == accountID && session.ID == targetDeviceID {
			targetDeviceID = webSessionDeviceID(session)
			break
		}
	}
	previousSessions, previousEvents := s.snapshotLocked()
	next := make([]storedWebSession, 0, len(s.sessions))
	revoked := 0
	current := false
	for _, session := range s.sessions {
		if session.AccountID == accountID && webSessionDeviceID(session) == targetDeviceID {
			revoked++
			current = current || subtle.ConstantTimeCompare([]byte(session.TokenHash), []byte(currentHash)) == 1
			continue
		}
		next = append(next, session)
	}
	if revoked == 0 {
		return 0, false, nil
	}
	s.sessions = next
	s.rebuildTokenIndexLocked()
	s.recordEventLocked(storedWebSecurityEvent{AccountID: accountID, Type: "device_revoked", Success: true})
	if err := s.persistLocked(); err != nil {
		s.restoreLocked(previousSessions, previousEvents)
		return 0, false, err
	}
	return revoked, current, nil
}

func (s *webSessionStore) RevokeOthers(accountID, currentToken string) (int, error) {
	if s == nil {
		return 0, errors.New("Web session store is unavailable")
	}
	currentHash := webSecretHash(currentToken)
	s.mu.Lock()
	defer s.mu.Unlock()
	currentDeviceID := ""
	for _, session := range s.sessions {
		if session.AccountID == accountID && subtle.ConstantTimeCompare([]byte(session.TokenHash), []byte(currentHash)) == 1 {
			currentDeviceID = webSessionDeviceID(session)
			break
		}
	}
	if currentDeviceID == "" {
		return 0, errSynonLinkInvalidSession
	}
	previousSessions, previousEvents := s.snapshotLocked()
	next := make([]storedWebSession, 0, len(s.sessions))
	revoked := 0
	for _, session := range s.sessions {
		if session.AccountID == accountID && webSessionDeviceID(session) != currentDeviceID {
			revoked++
			continue
		}
		next = append(next, session)
	}
	if revoked == 0 {
		return 0, nil
	}
	s.sessions = next
	s.rebuildTokenIndexLocked()
	s.recordEventLocked(storedWebSecurityEvent{AccountID: accountID, Type: "other_devices_revoked", Success: true})
	if err := s.persistLocked(); err != nil {
		s.restoreLocked(previousSessions, previousEvents)
		return 0, err
	}
	return revoked, nil
}
